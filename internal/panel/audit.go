package panel

import (
	"io"
	"net/http"
	"strconv"

	"github.com/wangsirco/berth/internal/change"
	"github.com/wangsirco/berth/internal/errs"
)

func init() {
	routeHooks = append(routeHooks, func(a *App, mux *http.ServeMux) {
		mux.HandleFunc("GET /api/v1/changes", a.authed("audit.view", a.handleChanges))
		mux.HandleFunc("GET /api/v1/changes/pending", a.authed("", a.handlePending))
		mux.HandleFunc("GET /api/v1/changes/{id}", a.authed("", a.handleChange))
		mux.HandleFunc("POST /api/v1/changes/{id}/revert", a.authed("", a.handleRevert))
		mux.HandleFunc("POST /api/v1/changes/{id}/confirm", a.authed("", a.handleConfirm))
		mux.HandleFunc("POST /api/v1/changes/{id}/reject", a.authed("", a.handleReject))
		mux.HandleFunc("POST /api/v1/hosts/{host}/changes/{kind}", a.authed("", a.handleRunKind))
		mux.HandleFunc("GET /api/v1/kinds", a.authed("", a.handleKinds))
	})
}

func (a *App) handleChanges(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := change.ListFilter{Host: q.Get("host"), Kind: q.Get("kind"), ActorID: q.Get("actor"), Via: q.Get("via"), Status: q.Get("status"),
		ObjectType: q.Get("object_type"), ObjectID: q.Get("object_id"), AISession: q.Get("ai_session"), Before: q.Get("before")}
	f.Since, _ = strconv.ParseInt(q.Get("since"), 10, 64)
	f.Until, _ = strconv.ParseInt(q.Get("until"), 10, 64)
	f.Limit, _ = strconv.Atoi(q.Get("limit"))
	list, err := a.pipe.List(f)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	items := make([]changeView, 0, len(list))
	for _, c := range list {
		items = append(items, viewOf(c))
	}
	writeJSON(w, 200, map[string]any{"items": items})
}

// canSee: super and audit viewers see everything; others see their own changes.
func canSee(r *http.Request, c *change.Change) bool {
	u := currentUser(r)
	if u.Can("audit.view") {
		return true
	}
	ac := currentActor(r)
	return c.Actor.ID == ac.ID || (c.Actor.Type == "user" && c.Actor.ID == strconv.FormatInt(u.ID, 10)) || ownsTokenActor(r, c.Actor)
}

// ownsTokenActor: a token's changes belong to the token's owner.
func ownsTokenActor(r *http.Request, ac change.Actor) bool {
	a, ok := r.Context().Value(appKey).(*App)
	if !ok || len(ac.ID) < 7 || ac.ID[:6] != "token:" {
		return false
	}
	var uid int64
	a.db.QueryRow(`SELECT user_id FROM tokens WHERE id=?`, ac.ID[6:]).Scan(&uid)
	return uid == currentUser(r).ID
}

func (a *App) handleChange(w http.ResponseWriter, r *http.Request) {
	c, err := a.pipe.Get(r.PathValue("id"))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if !canSee(r.WithContext(contextWith(r, appKey, a)), c) {
		a.fail(w, r, errs.New(404, "change.not_found"))
		return
	}
	writeJSON(w, 200, viewOf(c))
}

func (a *App) handlePending(w http.ResponseWriter, r *http.Request) {
	list, err := a.pipe.List(change.ListFilter{Status: "pending_approval", Limit: 200})
	if err != nil {
		a.fail(w, r, err)
		return
	}
	rr := r.WithContext(contextWith(r, appKey, a))
	items := []changeView{}
	for _, c := range list {
		if canSee(rr, c) {
			items = append(items, viewOf(c))
		}
	}
	writeJSON(w, 200, map[string]any{"items": items})
}

func (a *App) handleRevert(w http.ResponseWriter, r *http.Request) {
	orig, err := a.pipe.Get(r.PathValue("id"))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if orig.Undo == nil || orig.Status != "done" {
		a.fail(w, r, errs.New(409, "change.not_revertible"))
		return
	}
	k, ok := change.Get(orig.Undo.Kind)
	if !ok {
		a.fail(w, r, errs.New(409, "change.not_revertible"))
		return
	}
	if err := a.mayRun(r, k, orig.Host); err != nil {
		a.fail(w, r, err)
		return
	}
	c, err := a.pipe.Revert(r.Context(), currentActor(r), orig.ID)
	if err != nil && c == nil {
		a.fail(w, r, err)
		return
	}
	code := 200
	if err != nil {
		code = errs.From(err).Status
	}
	writeJSON(w, code, viewOf(c))
}

// approver: a person (never a token, so an AI cannot approve itself) who is super or
// owns the token that asked.
func (a *App) mayApprove(r *http.Request, c *change.Change) error {
	if err := requireSession(r); err != nil {
		return err
	}
	if currentUser(r).Role == RoleSuper || ownsTokenActor(r.WithContext(contextWith(r, appKey, a)), c.Actor) {
		return nil
	}
	return errs.New(403, "change.cannot_approve")
}

func (a *App) handleConfirm(w http.ResponseWriter, r *http.Request) {
	c, err := a.pipe.Get(r.PathValue("id"))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if err := a.mayApprove(r, c); err != nil {
		a.fail(w, r, err)
		return
	}
	done, err := a.pipe.Confirm(r.Context(), c.ID, currentActor(r))
	if err != nil && done == nil {
		a.fail(w, r, err)
		return
	}
	code := 200
	if err != nil {
		code = errs.From(err).Status
	}
	writeJSON(w, code, viewOf(done))
}

func (a *App) handleReject(w http.ResponseWriter, r *http.Request) {
	c, err := a.pipe.Get(r.PathValue("id"))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if err := a.mayApprove(r, c); err != nil {
		a.fail(w, r, err)
		return
	}
	out, err := a.pipe.Reject(c.ID, currentActor(r))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, 200, viewOf(out))
}

func (a *App) handleRunKind(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if len(raw) == 0 {
		raw = []byte(`{}`)
	}
	a.runChange(w, r, r.PathValue("host"), r.PathValue("kind"), raw)
}

type kindInfo struct {
	Name   string             `json:"name"`
	Scope  string             `json:"scope"`
	Perm   string             `json:"perm"`
	Title  string             `json:"title"`
	CLI    string             `json:"cli"`
	Params change.ParamSchema `json:"params"`
	Heavy  bool               `json:"heavy,omitempty"`
}

// GET /api/v1/kinds — what the caller may run (CLI, ⌘K, MCP read this).
func (a *App) handleKinds(w http.ResponseWriter, r *http.Request) {
	u, t := currentUser(r), currentToken(r)
	out := []kindInfo{}
	for _, k := range change.All() {
		if k.CLI == "" || (k.Perm != "" && !u.Can(k.Perm)) || (t != nil && k.Scope != "" && !t.has(k.Scope)) {
			continue
		}
		out = append(out, kindInfo{Name: k.Name, Scope: k.Scope, Perm: k.Perm, Title: T(a.locale(r), k.Title, nil), CLI: k.CLI, Params: k.Params, Heavy: k.Heavy})
	}
	writeJSON(w, 200, map[string]any{"items": out})
}
