package flows

import (
	"net/http"

	"github.com/wangsirco/berth/internal/agent"
)

// Register adds GET /v1/net/flows?consumer=ID.
func Register(s *agent.Server, r *Reader) {
	s.Handle("GET /v1/net/flows", func(w http.ResponseWriter, req *http.Request, _ []byte) {
		c := req.URL.Query().Get("consumer")
		if c == "" || len(c) > 128 {
			c = "default"
		}
		rows, err := r.Read(req.Context(), c)
		if err != nil {
			agent.WriteErr(w, err)
			return
		}
		agent.WriteJSON(w, 200, map[string]any{"rows": rows})
	})
}
