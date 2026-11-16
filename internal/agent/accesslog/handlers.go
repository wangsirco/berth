package accesslog

import (
	"context"
	"encoding/json"
	"github.com/wangsirco/berth/internal/errs"
	"log"
	"net"
	"net/http"
	"path/filepath"
	"strconv"
	"time"

	"github.com/wangsirco/berth/internal/agent"
)

// Register starts collecting the edge's access logs and serves the batches.
func Register(s *agent.Server, logsDir, dataDir string) {
	geo := NewGeo(dataDir + "/geo")
	Shared = geo
	geo.Needed = func() bool {
		m, _ := filepath.Glob(filepath.Join(logsDir, "*.log"))
		return len(m) > 0
	}
	go geo.Loop(context.Background(), log.Printf)
	c := NewCollector(logsDir, dataDir+"/accesslog-state.json", geo.Country)
	go c.Run(15*time.Second, nil)
	s.Handle("GET /v1/edge/log", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		n, _ := strconv.Atoi(r.URL.Query().Get("lines"))
		if n <= 0 || n > 2000 {
			n = 300
		}
		lines, err := Tail(logsDir, r.URL.Query().Get("domain"), n, r.URL.Query().Get("q"))
		if err != nil {
			agent.WriteErr(w, errs.New(400, "params.invalid_json"))
			return
		}
		agent.WriteJSON(w, 200, map[string]any{"items": lines})
	})
	// country codes for a batch of addresses (the panel's sign-in records)
	s.Handle("POST /v1/geo", func(w http.ResponseWriter, r *http.Request, body []byte) {
		var ips []string
		json.Unmarshal(body, &ips)
		if len(ips) > 1000 {
			ips = ips[:1000]
		}
		geo.Refresh(r.Context()) // loads (or downloads) the database on first use
		out := map[string]string{}
		for _, s := range ips {
			if ip := net.ParseIP(s); ip != nil {
				out[s] = geo.Country(ip)
			}
		}
		agent.WriteJSON(w, 200, out)
	})
	s.Handle("GET /v1/edge/stats", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		ack, _ := strconv.ParseInt(r.URL.Query().Get("ack"), 10, 64)
		agent.WriteJSON(w, 200, c.Take(r.URL.Query().Get("boot"), ack))
	})
}
