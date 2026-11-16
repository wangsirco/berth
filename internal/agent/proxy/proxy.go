// Package proxy is the agent side of Berth's Xray proxy extension: it writes the files
// the xray container reads (config, certificates), asks xray for traffic counters and
// reports the container's state. The panel builds the configuration.
package proxy

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/wangsirco/berth/internal/agent"
	"github.com/wangsirco/berth/internal/errs"
)

const (
	// Dir is the compose project folder; the container mounts it read-only at /etc/xray.
	Dir       = "/srv/berth/stacks/berth-proxy"
	Container = "berth-proxy-xray-1"
	APIAddr   = "127.0.0.1:20085"
	caddyCert = "/srv/berth/caddy/data/caddy/certificates"
	// xrayUID: the official image runs as this (non-root) user; its files belong to it,
	// mode 0600, so nobody else on the host reads the keys
	xrayUID = 65532
)

var domainRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9.-]*[a-z0-9])?$`)

type filesReq struct {
	Config string            `json:"config"`
	Certs  map[string]string `json:"certs"`      // name.crt / name.key → PEM
	Site   []string          `json:"site_certs"` // domains whose site certificate (Caddy's) to copy in
}

func run(ctx context.Context, name string, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// siteCert finds the certificate Caddy got for domain (any issuer).
func siteCert(domain string) (crt, key string, ok bool) {
	ms, _ := filepath.Glob(filepath.Join(caddyCert, "*", domain, domain+".crt"))
	for _, m := range ms {
		k := strings.TrimSuffix(m, ".crt") + ".key"
		if _, err := os.Stat(k); err == nil {
			return m, k, true
		}
	}
	return "", "", false
}

func writeFiles(in filesReq) error {
	if err := os.MkdirAll(filepath.Join(Dir, "certs"), 0o700); err != nil {
		return err
	}
	os.Chown(Dir, xrayUID, xrayUID)
	os.Chmod(Dir, 0o700)
	os.Chown(filepath.Join(Dir, "certs"), xrayUID, xrayUID)
	if !json.Valid([]byte(in.Config)) {
		return errs.New(400, "proxy.bad_config")
	}
	keep := map[string]bool{}
	for name, pem := range in.Certs {
		base := strings.TrimSuffix(strings.TrimSuffix(name, ".crt"), ".key")
		if !domainRe.MatchString(base) || (!strings.HasSuffix(name, ".crt") && !strings.HasSuffix(name, ".key")) {
			return errs.New(400, "proxy.bad_cert_name")
		}
		if err := writeOwned(filepath.Join(Dir, "certs", name), []byte(pem)); err != nil {
			return err
		}
		keep[name] = true
	}
	for _, d := range in.Site {
		if !domainRe.MatchString(d) {
			return errs.New(400, "proxy.bad_cert_name")
		}
		crt, key, ok := siteCert(d)
		if !ok {
			return errs.New(400, "proxy.no_site_cert", "domain", d)
		}
		for src, dst := range map[string]string{crt: d + ".crt", key: d + ".key"} {
			b, err := os.ReadFile(src)
			if err != nil {
				return err
			}
			if err := writeOwned(filepath.Join(Dir, "certs", dst), b); err != nil {
				return err
			}
			keep[dst] = true
		}
	}
	// certificates of nodes that are gone
	old, _ := os.ReadDir(filepath.Join(Dir, "certs"))
	for _, e := range old {
		if !keep[e.Name()] {
			os.Remove(filepath.Join(Dir, "certs", e.Name()))
		}
	}
	tmp := filepath.Join(Dir, "config.json.new")
	if err := writeOwned(tmp, []byte(in.Config)); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(Dir, "config.json"))
}

func writeOwned(path string, b []byte) error {
	if err := os.WriteFile(path, b, 0o600); err != nil {
		return err
	}
	os.Chmod(path, 0o600)
	return os.Chown(path, xrayUID, xrayUID)
}

// Stat is one xray counter: user>>>u3@n1>>>traffic>>>uplink, inbound>>>n1>>>traffic>>>downlink …
type Stat struct {
	Name  string `json:"name"`
	Value int64  `json:"value,string"`
}

func stats(ctx context.Context, reset bool) ([]Stat, error) {
	args := []string{"exec", Container, "xray", "api", "statsquery", "--server=" + APIAddr}
	if reset {
		args = append(args, "-reset")
	}
	out, err := run(ctx, "docker", args...)
	if err != nil {
		return nil, errs.New(502, "proxy.stats_failed", "detail", firstLine(out))
	}
	var r struct {
		Stat []json.RawMessage `json:"stat"`
	}
	if err := json.Unmarshal([]byte(out), &r); err != nil {
		return nil, errs.New(502, "proxy.stats_failed", "detail", firstLine(out))
	}
	var list []Stat
	for _, raw := range r.Stat {
		var s struct {
			Name  string          `json:"name"`
			Value json.RawMessage `json:"value"`
		}
		json.Unmarshal(raw, &s)
		v := strings.Trim(string(s.Value), `"`)
		var n int64
		json.Unmarshal([]byte(v), &n)
		list = append(list, Stat{Name: s.Name, Value: n})
	}
	return list, nil
}

func firstLine(s string) string {
	s, _, _ = strings.Cut(s, "\n")
	if len(s) > 300 {
		s = s[:300]
	}
	return s
}

// Status: is xray running, and its last log lines when it is not.
type Status struct {
	Exists  bool   `json:"exists"`
	Running bool   `json:"running"`
	State   string `json:"state"`
	Log     string `json:"log,omitempty"`
	Version string `json:"version,omitempty"`
}

func status(ctx context.Context) Status {
	var st Status
	out, err := run(ctx, "docker", "inspect", "-f", "{{.State.Status}}", Container)
	if err != nil {
		return st
	}
	st.Exists, st.State = true, out
	st.Running = out == "running"
	if v, err := run(ctx, "docker", "exec", Container, "xray", "version"); err == nil && st.Running {
		st.Version = firstLine(v)
	}
	if !st.Running || strings.Contains(out, "restarting") {
		l, _ := run(ctx, "docker", "logs", "--tail", "20", Container)
		st.Log = l
	}
	return st
}

// Register adds /v1/proxy/*.
func Register(s *agent.Server) {
	s.Handle("PUT /v1/proxy/files", func(w http.ResponseWriter, r *http.Request, body []byte) {
		var in filesReq
		if err := json.Unmarshal(body, &in); err != nil {
			agent.WriteErr(w, errs.New(400, "params.invalid"))
			return
		}
		if err := writeFiles(in); err != nil {
			agent.WriteErr(w, err)
			return
		}
		agent.WriteJSON(w, 200, map[string]bool{"ok": true})
	})
	s.Handle("POST /v1/proxy/restart", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		if out, err := run(ctx, "docker", "restart", Container); err != nil {
			agent.WriteErr(w, errs.New(502, "proxy.restart_failed", "detail", firstLine(out)))
			return
		}
		time.Sleep(1500 * time.Millisecond) // a bad config makes xray exit right away
		agent.WriteJSON(w, 200, status(ctx))
	})
	s.Handle("GET /v1/proxy/status", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		agent.WriteJSON(w, 200, status(r.Context()))
	})
	s.Handle("GET /v1/proxy/stats", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		list, err := stats(ctx, r.URL.Query().Get("reset") == "1")
		if err != nil {
			agent.WriteErr(w, err)
			return
		}
		agent.WriteJSON(w, 200, map[string]any{"stats": list})
	})
	s.Handle("GET /v1/proxy/site-cert", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		_, _, ok := siteCert(r.URL.Query().Get("domain"))
		agent.WriteJSON(w, 200, map[string]bool{"ok": ok})
	})
}
