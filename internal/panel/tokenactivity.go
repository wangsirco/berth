package panel

// What AI agents (and other tokens) are doing: every MCP tool call, read or write, and every
// change a token starts through the API, kept seven days. The home page shows who is working
// right now; object pages show "Claude is working here"; the tokens page has the timeline.

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/wangsirco/berth/internal/change"
	"github.com/wangsirco/berth/internal/errs"
)

type activity struct {
	ID       int64  `json:"id"`
	TokenID  int64  `json:"token_id"`
	TS       int64  `json:"ts"`
	Method   string `json:"method"` // MCP tool name, or "api"
	Kind     string `json:"kind,omitempty"`
	Host     string `json:"host,omitempty"`
	ObjType  string `json:"obj_type,omitempty"`
	ObjID    string `json:"obj_id,omitempty"`
	ChangeID string `json:"change_id,omitempty"`
	OK       bool   `json:"ok"`
	Summary  string `json:"summary,omitempty"` // a change's status, or an error code
}

const (
	activityKeep    = 7 * 24 * 3600
	activityPerTok  = 2000
	activityActive  = 120 // seconds: "working right now"
	activityObjects = 600 // seconds: "was working here"
)

// objArgs: what a tool call is about, read from its arguments. Order matters: the most telling first.
var objArgs = []struct{ key, typ string }{
	{"project", "project"}, {"compose", "compose_project"}, {"container", "container"}, {"site_id", "site"}, {"domain", "site"},
	{"database", "database"}, {"db", "database"}, {"vm", "vm"}, {"workspace", "dev"}, {"path", "file"}, {"id", ""}, {"name", ""},
}

// objFromArgs guesses the object a call touches; typ "" means "an object of the tool's own kind".
func objFromArgs(tool string, args map[string]any) (typ, id string) {
	for _, k := range objArgs {
		v, ok := args[k.key]
		if !ok {
			continue
		}
		var s string
		switch x := v.(type) {
		case string:
			s = x
		case float64:
			s = strconv.FormatInt(int64(x), 10)
		}
		if s == "" {
			continue
		}
		typ = k.typ
		if typ == "" {
			typ = toolObject(tool)
		}
		return typ, truncate(s, 200)
	}
	return toolObject(tool), ""
}

// toolObject: the kind of object a tool is about, from its name (compose_up → compose, get_container → container).
func toolObject(tool string) string {
	t := strings.TrimPrefix(strings.TrimPrefix(strings.TrimPrefix(tool, "get_"), "list_"), "tail_")
	if i := strings.IndexByte(t, '_'); i > 0 {
		t = t[:i]
	}
	switch t {
	case "containers", "logs":
		return "container"
	case "compose":
		return "compose_project"
	case "db":
		return "database"
	case "files", "file", "read":
		return "file"
	case "hosts", "host":
		return "host"
	case "changes", "change", "plan", "tasks":
		return ""
	}
	return t
}

var activityN atomic.Int64

func (a *App) recordActivity(tok *tokenRow, act activity) {
	if tok == nil || tok.ID == 0 {
		return
	}
	act.TokenID = tok.ID
	if act.TS == 0 {
		act.TS = time.Now().Unix()
	}
	res, err := a.db.Exec(`INSERT INTO token_activity(token_id, ts, method, kind, host, obj_type, obj_id, change_id, ok, summary) VALUES(?,?,?,?,?,?,?,?,?,?)`,
		act.TokenID, act.TS, act.Method, act.Kind, act.Host, act.ObjType, act.ObjID, act.ChangeID, b2i(act.OK), truncate(act.Summary, 200))
	if err != nil {
		return
	}
	act.ID, _ = res.LastInsertId()
	if activityN.Add(1)%200 == 0 {
		go a.pruneActivity()
	}
	owner := tok.UserID
	a.hub.publish(event{Type: "token.activity", Data: map[string]any{"activity": act, "token": map[string]any{"id": tok.ID, "name": tok.Name, "kind": tok.Kind}},
		visible: func(r *http.Request) bool { u := currentUser(r); return u != nil && (u.ID == owner || u.Can("tokens.manage")) }})
}

func (a *App) pruneActivity() {
	a.db.Exec(`DELETE FROM token_activity WHERE ts < ?`, time.Now().Unix()-activityKeep)
	a.db.Exec(`DELETE FROM token_activity WHERE id IN (SELECT id FROM (SELECT id, ROW_NUMBER() OVER (PARTITION BY token_id ORDER BY id DESC) AS n FROM token_activity) WHERE n > ?)`, activityPerTok)
}

// changeActivity: a change a token started outside MCP (MCP calls are recorded where they are made).
func (a *App) changeActivity(c *change.Change) {
	if !strings.HasPrefix(c.Actor.ID, "token:") || c.Actor.Via == "mcp" {
		return
	}
	id, err := strconv.ParseInt(strings.TrimPrefix(c.Actor.ID, "token:"), 10, 64)
	if err != nil {
		return
	}
	t, err := scanToken(a.db.QueryRow(`SELECT `+tokenCols+` FROM tokens WHERE id=?`, id))
	if err != nil {
		return
	}
	act := activity{Method: "api", Kind: c.Kind, Host: c.Host, ChangeID: c.ID, OK: c.Status != "failed", Summary: c.Status}
	if len(c.Plan.Affected) > 0 {
		act.ObjType, act.ObjID = c.Plan.Affected[0].Type, c.Plan.Affected[0].ID
	}
	a.recordActivity(t, act)
}

func scanActivity(rows interface {
	Next() bool
	Scan(...any) error
}) []activity {
	out := []activity{}
	for rows.Next() {
		var x activity
		var ok int
		if rows.Scan(&x.ID, &x.TokenID, &x.TS, &x.Method, &x.Kind, &x.Host, &x.ObjType, &x.ObjID, &x.ChangeID, &ok, &x.Summary) == nil {
			x.OK = ok == 1
			out = append(out, x)
		}
	}
	return out
}

const activityCols = `id, token_id, ts, method, kind, host, obj_type, obj_id, change_id, ok, summary`

type tokenNow struct {
	ID       int64        `json:"id"`
	Name     string       `json:"name"`
	Kind     string       `json:"kind"`
	Working  bool         `json:"working"`
	Last     int64        `json:"last"`
	Count24h int          `json:"count_24h"`
	Recent   []activity   `json:"recent"`
	Running  []changeView `json:"running"`
	Pending  []changeView `json:"pending"`
}

// tokensNow: the tokens this user may see that did something in the last day, busiest first.
func (a *App) tokensNow(u *User) ([]tokenNow, error) {
	toks, err := a.listTokens(u, u.Can("tokens.manage"))
	if err != nil {
		return nil, err
	}
	now := time.Now().Unix()
	out := []tokenNow{}
	for _, t := range toks {
		var last int64
		var n int
		a.db.QueryRow(`SELECT COALESCE(MAX(ts),0), COUNT(*) FROM token_activity WHERE token_id=? AND ts>?`, t.ID, now-86400).Scan(&last, &n)
		actor := "token:" + strconv.FormatInt(t.ID, 10)
		running, _ := a.pipe.List(change.ListFilter{ActorID: actor, Status: "running", Limit: 5})
		pending, _ := a.pipe.List(change.ListFilter{ActorID: actor, Status: "pending_approval", Limit: 5})
		if n == 0 && len(running) == 0 && len(pending) == 0 {
			continue
		}
		x := tokenNow{ID: t.ID, Name: t.Name, Kind: t.Kind, Last: last, Count24h: n, Working: now-last <= activityActive || len(running) > 0,
			Running: []changeView{}, Pending: []changeView{}}
		if rows, err := a.db.Query(`SELECT `+activityCols+` FROM token_activity WHERE token_id=? ORDER BY id DESC LIMIT 5`, t.ID); err == nil {
			x.Recent = scanActivity(rows)
			rows.Close()
		}
		for _, c := range running {
			x.Running = append(x.Running, viewOf(c))
		}
		for _, c := range pending {
			x.Pending = append(x.Pending, viewOf(c))
		}
		out = append(out, x)
	}
	// working ones first, then by last activity
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && (b2i(out[j].Working) > b2i(out[j-1].Working) || (out[j].Working == out[j-1].Working && out[j].Last > out[j-1].Last)); j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out, nil
}

func init() {
	routeHooks = append(routeHooks, func(a *App, mux *http.ServeMux) {
		mux.HandleFunc("GET /api/v1/tokens/activity", a.authed("", func(w http.ResponseWriter, r *http.Request) {
			out, err := a.tokensNow(currentUser(r))
			if err != nil {
				a.fail(w, r, err)
				return
			}
			writeJSON(w, 200, map[string]any{"items": out})
		}))
		mux.HandleFunc("GET /api/v1/tokens/{id}/activity", a.authed("", func(w http.ResponseWriter, r *http.Request) {
			t, err := a.ownToken(currentUser(r), r.PathValue("id"))
			if err != nil {
				a.fail(w, r, err)
				return
			}
			before, _ := strconv.ParseInt(r.URL.Query().Get("before"), 10, 64)
			if before == 0 {
				before = 1 << 62
			}
			rows, err := a.db.Query(`SELECT `+activityCols+` FROM token_activity WHERE token_id=? AND id<? ORDER BY id DESC LIMIT 200`, t.ID, before)
			if err != nil {
				a.fail(w, r, err)
				return
			}
			defer rows.Close()
			writeJSON(w, 200, map[string]any{"items": scanActivity(rows)})
		}))
		// who worked on one object lately (site, project, container, compose, db…)
		mux.HandleFunc("GET /api/v1/hosts/{host}/activity", a.authed("", func(w http.ResponseWriter, r *http.Request) {
			q := r.URL.Query()
			typ, id := q.Get("type"), q.Get("id")
			if typ == "" || id == "" {
				a.fail(w, r, errs.New(400, "common.bad_request"))
				return
			}
			u := currentUser(r)
			rows, err := a.db.Query(`SELECT a.id, a.token_id, a.ts, a.method, a.kind, a.host, a.obj_type, a.obj_id, a.change_id, a.ok, a.summary, t.name, t.user_id
				FROM token_activity a JOIN tokens t ON t.id=a.token_id WHERE a.host=? AND a.obj_type=? AND a.obj_id=? AND a.ts>? ORDER BY a.id DESC LIMIT 20`,
				r.PathValue("host"), typ, id, time.Now().Unix()-activityObjects)
			if err != nil {
				a.fail(w, r, err)
				return
			}
			defer rows.Close()
			type row struct {
				activity
				TokenName string `json:"token_name"`
			}
			out := []row{}
			for rows.Next() {
				var x row
				var ok int
				var owner int64
				if rows.Scan(&x.ID, &x.TokenID, &x.TS, &x.Method, &x.Kind, &x.Host, &x.ObjType, &x.ObjID, &x.ChangeID, &ok, &x.Summary, &x.TokenName, &owner) == nil &&
					(owner == u.ID || u.Can("tokens.manage") || u.Can("audit.view")) {
					x.OK = ok == 1
					out = append(out, x)
				}
			}
			writeJSON(w, 200, map[string]any{"items": out})
		}))
	})
}

// mcpActivity records one tool call made through MCP.
func (a *App) mcpActivity(r *http.Request, tool string, args map[string]any, host string, c *change.Change, err error) {
	act := activity{Method: tool, Host: host, OK: err == nil}
	if c != nil {
		act.Kind, act.ChangeID, act.Summary = c.Kind, c.ID, c.Status
		if c.Status == "planned" {
			act.Summary = "planned"
		}
		if len(c.Plan.Affected) > 0 {
			act.ObjType, act.ObjID = c.Plan.Affected[0].Type, c.Plan.Affected[0].ID
		}
	}
	if act.ObjType == "" {
		act.ObjType, act.ObjID = objFromArgs(tool, args)
	}
	if err != nil {
		act.Summary = errs.From(err).Code
	}
	if b, e := json.Marshal(args); e == nil && act.ObjID == "" && len(b) < 120 && act.ObjType == "" {
		act.Summary = strings.TrimSpace(act.Summary + " " + string(b))
	}
	a.recordActivity(currentToken(r), act)
}
