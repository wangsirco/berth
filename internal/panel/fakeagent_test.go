package panel

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wangsirco/berth/internal/agent"
	"github.com/wangsirco/berth/internal/agent/accesslog"
	"github.com/wangsirco/berth/internal/agentproto"
	"github.com/wangsirco/berth/internal/errs"
	"github.com/wangsirco/berth/internal/pipeline"
)

// fakeAgent is a real signed agent server with canned Docker data that records calls.
type fakeAgent struct {
	mu         sync.Mutex
	calls      []string
	containers []map[string]any
	files      map[string]string
	ptyOwner   string
	edgeConfig string // last config loaded into the fake Caddy
	publicIP   string
	diag       fakeDiag
	logsDir    string
	collector  *accesslog.Collector
	buildFail  bool
	deployFail bool
	images     map[string]string // compose service -> image
	listening  []int
}

// fakeDiag is what the diagnosis probes see; tests break one thing at a time.
type fakeDiag struct {
	dns      map[string][]string
	https    map[string]any
	upstream int // HTTP status from inside the edge; 0 = unreachable
	listen   []map[string]any
	inspect  map[string]any
	errors   []string
}

func (f *fakeAgent) record(s string) {
	f.mu.Lock()
	f.calls = append(f.calls, s)
	f.mu.Unlock()
}

func (f *fakeAgent) called(methodAndPath string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls {
		if c == methodAndPath {
			return true
		}
	}
	return false
}

func (c *tc) withFakeAgent() *fakeAgent {
	dir, err := os.MkdirTemp("", "ba")
	if err != nil {
		c.t.Fatal(err)
	}
	c.t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "a.sock")
	secret := []byte("fake-agent-secret")
	f := &fakeAgent{
		containers: []map[string]any{{
			"id": "abc123def456", "name": "n8n", "image": "n8nio/n8n:1", "state": "running", "status": "Up 2 hours",
			"project": "n8n", "service": "n8n", "networks": []string{"n8n_default"},
			"mounts": []map[string]any{{"type": "volume", "name": "n8n_data", "destination": "/home/node"}},
			"ports":  []any{}, "labels": map[string]string{},
		}},
		files: map[string]string{"compose": "services:\n  n8n:\n    image: n8nio/n8n:1\n", "env": "N8N_KEY=old\n"},
	}
	s := agent.NewServer(agent.Config{Socket: sock, Keys: map[string][]byte{"k": secret}})
	s.Handle("GET /v1/docker/containers", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		f.mu.Lock()
		defer f.mu.Unlock()
		agent.WriteJSON(w, 200, f.containers)
	})
	for _, action := range []string{"start", "stop", "restart", "remove"} {
		s.Handle("POST /v1/docker/containers/{id}/"+action, func(w http.ResponseWriter, r *http.Request, _ []byte) {
			f.record("POST /v1/docker/containers/" + r.PathValue("id") + "/" + action)
			f.mu.Lock()
			switch action {
			case "stop":
				f.containers[0]["state"] = "exited"
			case "start", "restart":
				f.containers[0]["state"] = "running"
			}
			f.mu.Unlock()
			agent.WriteJSON(w, 200, map[string]bool{"ok": true})
		})
	}
	s.Handle("GET /v1/compose/projects", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		agent.WriteJSON(w, 200, []map[string]any{{"name": "n8n", "dir": "/opt/n8n", "config_files": []string{"/opt/n8n/compose.yaml"}, "services": []string{"n8n"}, "running": 1, "total": 1}})
	})
	s.Handle("GET /v1/compose/projects/{name}/files", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		f.mu.Lock()
		defer f.mu.Unlock()
		agent.WriteJSON(w, 200, map[string]string{"dir": "/opt/n8n", "file": "/opt/n8n/compose.yaml", "compose": f.files["compose"], "env": f.files["env"]})
	})
	s.Handle("PUT /v1/compose/projects/{name}/files", func(w http.ResponseWriter, r *http.Request, body []byte) {
		var in map[string]any
		json.Unmarshal(body, &in)
		f.record("PUT files")
		f.mu.Lock()
		f.files["compose"], _ = in["compose"].(string)
		f.files["env"], _ = in["env"].(string)
		f.mu.Unlock()
		agent.WriteJSON(w, 200, map[string]string{"dir": "/opt/n8n"})
	})
	s.Handle("POST /v1/compose/projects/{name}/validate", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		agent.WriteJSON(w, 200, map[string]bool{"ok": true})
	})
	s.Handle("GET /v1/metrics/host", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		agent.WriteJSON(w, 200, map[string]int64{"mem_avail": 6 << 30})
	})
	s.Handle("POST /v1/pty", func(w http.ResponseWriter, r *http.Request, body []byte) {
		var in map[string]any
		json.Unmarshal(body, &in)
		f.record("POST /v1/pty user=" + in["user"].(string))
		f.mu.Lock()
		f.ptyOwner = in["owner"].(string)
		f.mu.Unlock()
		agent.WriteJSON(w, 200, map[string]any{"id": "pty_1", "owner": in["owner"], "user": in["user"]})
	})
	s.Handle("GET /v1/pty", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		f.mu.Lock()
		defer f.mu.Unlock()
		agent.WriteJSON(w, 200, []map[string]any{{"id": "pty_1", "owner": f.ptyOwner}})
	})
	s.Handle("GET /v1/edge/status", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		agent.WriteJSON(w, 200, map[string]any{"installed": true, "running": true, "image": "berth/caddy:test"})
	})
	s.Handle("POST /v1/edge/validate", func(w http.ResponseWriter, r *http.Request, body []byte) {
		f.record("validate")
		if strings.Contains(string(body), "rejected.example.com") {
			agent.WriteErr(w, errs.New(400, "edge.config_rejected", "detail", "nope"))
			return
		}
		agent.WriteJSON(w, 200, map[string]bool{"ok": true})
	})
	s.Handle("POST /v1/edge/load", func(w http.ResponseWriter, r *http.Request, body []byte) {
		f.record("load")
		f.mu.Lock()
		f.edgeConfig = string(body)
		f.mu.Unlock()
		agent.WriteJSON(w, 200, map[string]bool{"ok": true})
	})
	s.Handle("PUT /v1/edge/certs/{id}", func(w http.ResponseWriter, r *http.Request, body []byte) {
		f.record("PUT cert " + r.PathValue("id"))
		agent.WriteJSON(w, 200, map[string]bool{"ok": true})
	})
	s.Handle("GET /v1/edge/acme-certs", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		agent.WriteJSON(w, 200, []map[string]any{{"domain": "shop.example.com", "issuer": "acme-v02.api.letsencrypt.org-directory"}})
	})
	// the agent's real public-IP handler asks these instead of the internet
	ipSrc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		w.Write([]byte(f.publicIP))
	}))
	c.t.Cleanup(ipSrc.Close)
	oldSrc := agent.PublicIPSources
	agent.PublicIPSources = map[string][]string{"4": {ipSrc.URL}, "6": {}}
	c.t.Cleanup(func() { agent.PublicIPSources = oldSrc })
	f.diag = fakeDiag{dns: map[string][]string{}, upstream: 200,
		listen:  []map[string]any{{"port": 5678, "proto": "tcp", "loopback": false}},
		inspect: map[string]any{"name": "n8n", "state": "running", "restart_count": 0, "oom_killed": false},
		https:   map[string]any{"status": 200, "ms": 12, "cert": map[string]any{"names": []string{"n8n.example.com"}, "issuer": "R11", "not_after": time.Now().Add(60 * 24 * time.Hour), "verified": true}}}
	s.Handle("POST /v1/probe/dns", func(w http.ResponseWriter, r *http.Request, body []byte) {
		var in struct {
			Name      string
			Resolvers []string
		}
		json.Unmarshal(body, &in)
		f.mu.Lock()
		defer f.mu.Unlock()
		var out []map[string]any
		for _, rs := range in.Resolvers {
			ips, ok := f.diag.dns[in.Name]
			if !ok {
				out = append(out, map[string]any{"resolver": rs, "ips": []string{}, "error": "NXDOMAIN"})
				continue
			}
			out = append(out, map[string]any{"resolver": rs, "ips": ips})
		}
		agent.WriteJSON(w, 200, out)
	})
	s.Handle("POST /v1/probe/tcp", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		agent.WriteJSON(w, 200, map[string]any{"ok": true})
	})
	s.Handle("POST /v1/probe/https", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		f.mu.Lock()
		defer f.mu.Unlock()
		agent.WriteJSON(w, 200, f.diag.https)
	})
	s.Handle("POST /v1/probe/edge-http", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.diag.upstream == 0 {
			agent.WriteJSON(w, 200, map[string]any{"status": 0, "error": "connection refused"})
			return
		}
		agent.WriteJSON(w, 200, map[string]any{"status": f.diag.upstream, "ms": 3})
	})
	s.Handle("GET /v1/edge/config", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		f.mu.Lock()
		defer f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(f.edgeConfig))
	})
	s.Handle("GET /v1/edge/errors", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		f.mu.Lock()
		defer f.mu.Unlock()
		agent.WriteJSON(w, 200, append([]string{}, f.diag.errors...))
	})
	s.Handle("GET /v1/docker/containers/{id}/listening", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		f.mu.Lock()
		defer f.mu.Unlock()
		agent.WriteJSON(w, 200, f.diag.listen)
	})
	s.Handle("GET /v1/docker/containers/{id}", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		f.mu.Lock()
		defer f.mu.Unlock()
		agent.WriteJSON(w, 200, f.diag.inspect)
	})
	f.logsDir = c.t.TempDir()
	f.collector = accesslog.NewCollector(f.logsDir, filepath.Join(c.t.TempDir(), "state.json"), nil)
	s.Handle("GET /v1/edge/stats", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		ack, _ := strconv.ParseInt(r.URL.Query().Get("ack"), 10, 64)
		agent.WriteJSON(w, 200, f.collector.Take(r.URL.Query().Get("boot"), ack))
	})
	f.images = map[string]string{"web": "192.0.2.10:5000/shop/web:r0"}
	s.Handle("POST /v1/pipeline/run", func(w http.ResponseWriter, r *http.Request, body []byte) {
		var req pipeline.RunRequest
		json.Unmarshal(body, &req)
		f.record("run " + req.RunID + " " + req.Source.Type + " " + req.Registry)
		st := agent.NewStream(w)
		st.Send(map[string]string{"l": "building " + req.Version})
		res := pipeline.Result{Commit: "abc123", Steps: []pipeline.StepResult{{Name: "build", Status: "ok"}}}
		f.mu.Lock()
		fail := f.buildFail
		f.mu.Unlock()
		if fail {
			res.Steps[0].Status = "failed"
			b, _ := json.Marshal(res)
			st.Send(map[string]string{"l": "::result " + string(b)})
			st.End(errs.New(500, "internal", "detail", "exit status 1"))
			return
		}
		res.Image = pipeline.RewriteRegistry(req.Spec.Image(), req.Registry) + ":" + req.Version
		res.Digest = "sha256:" + req.Version
		b, _ := json.Marshal(res)
		st.Send(map[string]string{"l": "::result " + string(b)})
		st.End(nil)
	})
	s.Handle("POST /v1/deploy/compose", func(w http.ResponseWriter, r *http.Request, body []byte) {
		var req pipeline.DeployRequest
		json.Unmarshal(body, &req)
		st := agent.NewStream(w)
		f.mu.Lock()
		old := f.images[req.Service]
		fail := f.deployFail
		if !fail {
			f.images[req.Service] = req.Image
		}
		f.mu.Unlock()
		res := pipeline.DeployResult{OldImage: old, NewImage: req.Image, RolledBack: fail}
		b, _ := json.Marshal(res)
		st.Send(map[string]string{"l": "::result " + string(b)})
		if fail {
			st.End(errs.New(500, "deploy.rolled_back", "service", req.Service, "image", old))
			return
		}
		st.End(nil)
	})
	s.Handle("GET /v1/deploy/current", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		f.mu.Lock()
		defer f.mu.Unlock()
		agent.WriteJSON(w, 200, map[string]string{"image": f.images[r.URL.Query().Get("service")]})
	})
	s.Handle("GET /v1/net/listening", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		f.mu.Lock()
		defer f.mu.Unlock()
		agent.WriteJSON(w, 200, append([]int{}, f.listening...))
	})
	for _, op := range []string{"up", "pull", "remove"} {
		s.Handle("POST /v1/compose/projects/{name}/"+op, func(w http.ResponseWriter, r *http.Request, _ []byte) {
			f.record("compose " + op + " " + r.PathValue("name"))
			st := agent.NewStream(w)
			st.Send(map[string]string{"l": op + " ok"})
			st.End(nil)
		})
	}
	s.Handle("POST /v1/edge/connect", func(w http.ResponseWriter, r *http.Request, body []byte) {
		f.record("connect " + string(body))
		agent.WriteJSON(w, 200, map[string]bool{"ok": true})
	})
	s.Handle("POST /v1/firewall/bans", func(w http.ResponseWriter, r *http.Request, body []byte) {
		f.record("bans " + string(body))
		agent.WriteJSON(w, 200, map[string]bool{"ok": true})
	})
	go s.ListenAndServe()
	s.WaitReady()
	c.t.Cleanup(func() { s.Close() })
	c.app.agents["local"] = agentproto.NewClient(sock, "k", secret)
	return f
}

func (c *tc) loginAndGet(user, pw, path string) (*http.Response, map[string]any) {
	c.login(user, pw)
	return c.do("GET", path, nil)
}

var _ = testing.T{}
