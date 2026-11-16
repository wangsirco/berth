// Package driftapi serves the drift checks over the agent API.
package driftapi

import (
	"encoding/json"
	"net/http"

	"github.com/wangsirco/berth/internal/agent"
	"github.com/wangsirco/berth/internal/agent/drift"
	"github.com/wangsirco/berth/internal/errs"
)

// Register adds /v1/drift.
func Register(s *agent.Server) {
	s.Handle("GET /v1/drift", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		agent.WriteJSON(w, 200, map[string]any{"items": drift.Check()})
	})
	s.Handle("POST /v1/drift/versions", func(w http.ResponseWriter, r *http.Request, body []byte) {
		var p struct{ Path string }
		json.Unmarshal(body, &p)
		b, n, err := drift.Versions(p.Path)
		if err != nil {
			agent.WriteErr(w, errs.New(404, "drift.not_tracked"))
			return
		}
		agent.WriteJSON(w, 200, map[string]string{"berth": b, "now": n})
	})
	s.Handle("POST /v1/drift/accept", func(w http.ResponseWriter, r *http.Request, body []byte) {
		var p struct{ Path string }
		json.Unmarshal(body, &p)
		if err := drift.Accept(p.Path); err != nil {
			agent.WriteErr(w, errs.New(404, "drift.not_tracked"))
			return
		}
		agent.WriteJSON(w, 200, map[string]bool{"ok": true})
	})
	s.Handle("POST /v1/drift/restore", func(w http.ResponseWriter, r *http.Request, body []byte) {
		var p struct{ Path string }
		json.Unmarshal(body, &p)
		if err := drift.Restore(r.Context(), p.Path); err != nil {
			agent.WriteErr(w, errs.New(400, "drift.restore_failed", "detail", err.Error()))
			return
		}
		agent.WriteJSON(w, 200, map[string]bool{"ok": true})
	})
}
