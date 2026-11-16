package agent

import (
	"crypto/tls"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/wangsirco/berth/internal/errs"
)

// POST /v1/probe/http {url}: one GET from this host (health checks of local services).
func registerHTTPProbe(s *Server) {
	s.Handle("POST /v1/probe/http", func(w http.ResponseWriter, r *http.Request, body []byte) {
		var in struct {
			URL string `json:"url"`
		}
		json.Unmarshal(body, &in)
		if !strings.HasPrefix(in.URL, "http://") && !strings.HasPrefix(in.URL, "https://") {
			WriteErr(w, errs.New(400, "params.invalid_json"))
			return
		}
		hc := &http.Client{Timeout: 8 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}, // local apps' own certificates
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		start := time.Now()
		req, _ := http.NewRequestWithContext(r.Context(), "GET", in.URL, nil)
		resp, err := hc.Do(req)
		out := map[string]any{"ms": time.Since(start).Milliseconds()}
		if err != nil {
			out["error"] = err.Error()
		} else {
			resp.Body.Close()
			out["status"] = resp.StatusCode
		}
		WriteJSON(w, 200, out)
	})
}
