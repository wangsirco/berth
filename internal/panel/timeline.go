package panel

import (
	"net/http"
	"sort"
	"strconv"

	"github.com/wangsirco/berth/internal/change"
)

type timelineItem struct {
	Type   string      `json:"type"` // change | login
	TS     int64       `json:"ts"`
	Change *changeView `json:"change,omitempty"`
	Login  *loginView  `json:"login,omitempty"`
}

type loginView struct {
	Username string `json:"username"`
	OK       bool   `json:"ok"`
	Method   string `json:"method"`
	IP       string `json:"ip"`
	Agent    string `json:"user_agent"`
	Reason   string `json:"reason,omitempty"`
}

func init() {
	routeHooks = append(routeHooks, func(a *App, mux *http.ServeMux) {
		mux.HandleFunc("GET /api/v1/timeline", a.authed("overview.view", a.handleTimeline))
	})
}

// GET /api/v1/timeline?limit=20 — the overview's "activity": changes the caller may see
// and sign-ins (everyone's for audit viewers, only their own otherwise).
func (a *App) handleTimeline(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	u := currentUser(r)
	rr := r.WithContext(contextWith(r, appKey, a))
	out := []timelineItem{}
	if list, err := a.pipe.List(change.ListFilter{Limit: limit * 3}); err == nil {
		for _, c := range list {
			if c.Kind == "terminal.open" && !u.Can("audit.view") {
				continue
			}
			if canSee(rr, c) {
				v := viewOf(c)
				out = append(out, timelineItem{Type: "change", TS: c.TS, Change: &v})
			}
		}
	}
	q, args := `SELECT ts, username, ok, method, ip, user_agent, reason FROM logins ORDER BY ts DESC LIMIT ?`, []any{limit}
	if !u.Can("audit.view") {
		q, args = `SELECT ts, username, ok, method, ip, user_agent, reason FROM logins WHERE user_id=? ORDER BY ts DESC LIMIT ?`, []any{u.ID, limit}
	}
	if rows, err := a.db.Query(q, args...); err == nil {
		for rows.Next() {
			var l loginView
			var ts int64
			if rows.Scan(&ts, &l.Username, &l.OK, &l.Method, &l.IP, &l.Agent, &l.Reason) == nil && l.Reason != "second_step" {
				lv := l
				out = append(out, timelineItem{Type: "login", TS: ts, Login: &lv})
			}
		}
		rows.Close()
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].TS > out[j].TS })
	if len(out) > limit {
		out = out[:limit]
	}
	writeJSON(w, 200, map[string]any{"items": out})
}
