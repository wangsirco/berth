package firewall

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/wangsirco/berth/internal/agent"
	"github.com/wangsirco/berth/internal/errs"
)

// Grace is how long an unconfirmed change lives.
var Grace = 60 * time.Second

// Register adds /v1/firewall/*.
func Register(s *agent.Server) {
	Boot(context.Background())
	s.Handle("GET /v1/firewall", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		agent.WriteJSON(w, 200, map[string]any{"state": Status(r.Context()), "listening": Listening()})
	})
	s.Handle("POST /v1/firewall/preview", func(w http.ResponseWriter, r *http.Request, body []byte) {
		var d Desired
		if err := json.Unmarshal(body, &d); err != nil {
			agent.WriteErr(w, errs.New(400, "firewall.bad_rule", "detail", err.Error()))
			return
		}
		p, err := Preview(r.Context(), d)
		if err != nil {
			agent.WriteErr(w, errs.New(400, "firewall.bad_rule", "detail", err.Error()))
			return
		}
		agent.WriteJSON(w, 200, p)
	})
	s.Handle("POST /v1/firewall/apply", func(w http.ResponseWriter, r *http.Request, body []byte) {
		var d Desired
		if err := json.Unmarshal(body, &d); err != nil {
			agent.WriteErr(w, errs.New(400, "firewall.bad_rule", "detail", err.Error()))
			return
		}
		p, err := Apply(r.Context(), d, Grace)
		if err != nil {
			agent.WriteErr(w, errs.New(500, "firewall.apply_failed", "detail", err.Error()))
			return
		}
		agent.WriteJSON(w, 200, map[string]any{"plan": p, "state": Status(r.Context())})
	})
	s.Handle("POST /v1/firewall/confirm", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		if err := Confirm(r.Context()); err != nil {
			agent.WriteErr(w, errs.New(500, "firewall.apply_failed", "detail", err.Error()))
			return
		}
		agent.WriteJSON(w, 200, Status(r.Context()))
	})
	s.Handle("POST /v1/firewall/rollback", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		if err := Rollback(r.Context()); err != nil {
			agent.WriteErr(w, errs.New(500, "firewall.apply_failed", "detail", err.Error()))
			return
		}
		agent.WriteJSON(w, 200, Status(r.Context()))
	})
	s.Handle("GET /v1/security/failures", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		f, cur, err := SSHFailures(r.Context(), r.URL.Query().Get("cursor"))
		if err != nil {
			agent.WriteErr(w, errs.New(500, "firewall.apply_failed", "detail", err.Error()))
			return
		}
		if f == nil {
			f = []AuthFailure{}
		}
		agent.WriteJSON(w, 200, map[string]any{"failures": f, "cursor": cur})
	})
	s.Handle("GET /v1/security/waf", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		ev, pos, err := ReadWAF(r.URL.Query().Get("pos"))
		if err != nil {
			agent.WriteErr(w, errs.New(500, "firewall.apply_failed", "detail", err.Error()))
			return
		}
		if ev == nil {
			ev = []WAFEvent{}
		}
		agent.WriteJSON(w, 200, map[string]any{"events": ev, "pos": pos})
	})
	s.Handle("POST /v1/firewall/bans", func(w http.ResponseWriter, r *http.Request, body []byte) {
		var bans map[string]int64
		json.Unmarshal(body, &bans)
		if err := SetBans(r.Context(), bans); err != nil {
			agent.WriteErr(w, errs.New(500, "firewall.apply_failed", "detail", err.Error()))
			return
		}
		agent.WriteJSON(w, 200, map[string]bool{"ok": true})
	})
}
