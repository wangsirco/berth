package panel

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/coder/websocket"

	"github.com/wangsirco/berth/internal/agent/pty"
	"github.com/wangsirco/berth/internal/change"
	"github.com/wangsirco/berth/internal/errs"
)

func init() {
	routeHooks = append(routeHooks, func(a *App, mux *http.ServeMux) {
		mux.HandleFunc("POST /api/v1/hosts/{host}/terminals", a.authed("terminal.use", a.handleTerminalOpen))
		mux.HandleFunc("GET /api/v1/hosts/{host}/terminals", a.authed("terminal.use", a.handleTerminals))
		mux.HandleFunc("DELETE /api/v1/hosts/{host}/terminals/{id}", a.authed("terminal.use", a.handleTerminalClose))
		mux.HandleFunc("GET /api/v1/hosts/{host}/terminals/{id}/ws", a.authed("terminal.use", a.handleTerminalWS))
		mux.HandleFunc("GET /api/v1/hosts/{host}/terminals/{id}/recording", a.authed("terminal.use", a.handleTerminalRecording))
	})
}

func ownerOf(u *User) string { return "user:" + strconv.FormatInt(u.ID, 10) }

// terminalUser: super admins get root; others the configured login user (方案 §5.8).
func (a *App) terminalUser(u *User) (string, error) {
	if u.Role == RoleSuper {
		return "root", nil
	}
	if n := a.setting("terminal.default_user"); n != "" {
		return n, nil
	}
	return "", errs.New(409, "terminal.no_default_user")
}

func (a *App) handleTerminalOpen(w http.ResponseWriter, r *http.Request) {
	if err := a.tokenScope(r, "terminal:write"); err != nil {
		a.fail(w, r, err)
		return
	}
	var in struct {
		Target    string `json:"target"`
		Container string `json:"container"`
		Cols      int    `json:"cols"`
		Rows      int    `json:"rows"`
		Record    bool   `json:"record"`
		Program   string `json:"program"` // dev: claude
	}
	if err := readJSON(r, &in); err != nil {
		a.fail(w, r, err)
		return
	}
	u, host := currentUser(r), r.PathValue("host")
	if in.Target == "container" && !u.Can("containers.manage") {
		a.fail(w, r, errs.New(403, "auth.forbidden", "perm", "containers.manage"))
		return
	}
	if (in.Target == "instance" || in.Target == "instance-console") && !u.Can("vms.manage") {
		a.fail(w, r, errs.New(403, "auth.forbidden", "perm", "vms.manage"))
		return
	}
	var login, dir string
	var err error
	if in.Target == "dev" { // a dev workspace: as its user, in its folder (方案 §5.9)
		if !u.Can("dev.use") {
			a.fail(w, r, errs.New(403, "auth.forbidden", "perm", "dev.use"))
			return
		}
		ws, err := a.devWorkspace(host, in.Container)
		if err != nil {
			a.fail(w, r, err)
			return
		}
		var st struct {
			User string `json:"user"`
		}
		a.call(r.Context(), host, "GET", "/v1/dev/"+ws.Name+"/status", nil, &st)
		login, dir = firstNonEmpty(st.User, ws.Env.User, "root"), ws.Env.Folder
		in.Container = devContainer(ws.Name)
	} else if login, err = a.terminalUser(u); err != nil {
		a.fail(w, r, err)
		return
	}
	title := host
	switch in.Target {
	case "container", "instance":
		title = in.Container
	case "dev":
		title = strings.TrimPrefix(in.Container, "berth-dev-")
		if in.Program == "claude" {
			title += " · Claude"
		} else {
			in.Program = ""
		}
	case "instance-console":
		title = in.Container + " (console)"
	}
	if in.Target == "" {
		in.Target = "host"
	}
	var info pty.Info
	if err := a.call(r.Context(), host, "POST", "/v1/pty", map[string]any{"target": in.Target, "container": in.Container, "user": login,
		"dir": dir, "program": in.Program, "cols": in.Cols, "rows": in.Rows, "record": in.Record || a.setting("terminal.record_all") == "1", "owner": ownerOf(u), "title": title}, &info); err != nil {
		a.fail(w, r, err)
		return
	}
	target := change.Object{Type: "host", ID: host, Name: host}
	switch in.Target {
	case "container":
		target = change.Object{Type: "container", ID: in.Container, Name: in.Container, Host: host}
	case "instance", "instance-console":
		target = change.Object{Type: "vm", ID: in.Container, Name: in.Container, Host: host}
	case "dev":
		target = devObj(strings.TrimPrefix(in.Container, "berth-dev-"), host)
	}
	_, _ = a.pipe.Record(currentActor(r), host, "terminal.open", map[string]any{"target": in.Target, "container": in.Container, "user": login, "session": info.ID, "record": info.Recording},
		change.Plan{Summary: "plan.terminal.open", SummaryP: map[string]any{"title": title, "user": login}, Risk: change.RiskHigh, Affected: []change.Object{target}})
	writeJSON(w, 200, info)
}

func (a *App) terminalSessions(ctx context.Context, host string) ([]pty.Info, error) {
	var list []pty.Info
	err := a.call(ctx, host, "GET", "/v1/pty", nil, &list)
	return list, err
}

// ownedSession finds a session the caller may use: their own, or any for super admins.
func (a *App) ownedSession(r *http.Request, host, id string) (*pty.Info, error) {
	list, err := a.terminalSessions(r.Context(), host)
	if err != nil {
		return nil, err
	}
	u := currentUser(r)
	for _, s := range list {
		if s.ID == id && (s.Owner == ownerOf(u) || u.Role == RoleSuper) {
			return &s, nil
		}
	}
	return nil, errs.New(404, "terminal.not_found")
}

func (a *App) handleTerminals(w http.ResponseWriter, r *http.Request) {
	list, err := a.terminalSessions(r.Context(), r.PathValue("host"))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	u := currentUser(r)
	out := []pty.Info{}
	for _, s := range list {
		if s.Owner == ownerOf(u) || (u.Role == RoleSuper && r.URL.Query().Get("all") == "1") {
			out = append(out, s)
		}
	}
	writeJSON(w, 200, map[string]any{"items": out})
}

func (a *App) handleTerminalClose(w http.ResponseWriter, r *http.Request) {
	host := r.PathValue("host")
	s, err := a.ownedSession(r, host, r.PathValue("id"))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if err := a.call(r.Context(), host, "DELETE", "/v1/pty/"+url.PathEscape(s.ID), nil, nil); err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// handleTerminalWS relays a browser WebSocket to the agent's session. websocket.Accept
// rejects other origins, which stops cross-site WebSocket hijacking with the session cookie.
func (a *App) handleTerminalWS(w http.ResponseWriter, r *http.Request) {
	host := r.PathValue("host")
	s, err := a.ownedSession(r, host, r.PathValue("id"))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	ag, err := a.agent(host)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	browser, err := websocket.Accept(w, r, nil)
	if err != nil {
		return // Accept already answered (403 for a foreign origin)
	}
	browser.SetReadLimit(1 << 20)
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	upstream, err := ag.Dial(ctx, "/v1/pty/"+url.PathEscape(s.ID)+"/attach")
	if err != nil {
		browser.Close(websocket.StatusInternalError, "agent unreachable")
		return
	}
	relay := func(from, to *websocket.Conn) {
		defer cancel()
		for {
			typ, b, err := from.Read(ctx)
			if err != nil {
				return
			}
			wctx, c := context.WithTimeout(ctx, 10*time.Second)
			err = to.Write(wctx, typ, b)
			c()
			if err != nil {
				return
			}
		}
	}
	go relay(browser, upstream)
	relay(upstream, browser)
	browser.Close(websocket.StatusNormalClosure, "")
	upstream.Close(websocket.StatusNormalClosure, "")
}

func (a *App) handleTerminalRecording(w http.ResponseWriter, r *http.Request) {
	host, id := r.PathValue("host"), r.PathValue("id")
	// recordings outlive sessions: only the opener (from the audit log) or a super admin
	u := currentUser(r)
	if u.Role != RoleSuper {
		var n int
		a.db.QueryRow(`SELECT COUNT(*) FROM changes WHERE kind='terminal.open' AND actor_type='user' AND actor_id=? AND params LIKE ?`,
			strconv.FormatInt(u.ID, 10), `%"session":"`+id+`"%`).Scan(&n)
		if n == 0 {
			a.fail(w, r, errs.New(404, "terminal.no_recording"))
			return
		}
	}
	ag, err := a.agent(host)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	rc, err := ag.Stream(r.Context(), "GET", "/v1/pty/"+url.PathEscape(id)+"/recording", nil)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	defer rc.Close()
	w.Header().Set("Content-Type", "application/x-asciicast")
	w.Header().Set("Content-Disposition", `attachment; filename="`+id+`.cast"`)
	_, _ = io.Copy(w, rc)
}

// tokenScope requires a scope from token callers (session callers pass).
func (a *App) tokenScope(r *http.Request, scope string) error {
	if t := currentToken(r); t != nil && !t.has(scope) {
		return errs.New(403, "token.out_of_scope", "scope", scope)
	}
	return nil
}
