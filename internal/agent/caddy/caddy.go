// Package caddy manages the berth-caddy edge container: its files under
// /srv/berth/caddy, the compose stack, and the admin API on a unix socket.
package caddy

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/wangsirco/berth/internal/agent"
	"github.com/wangsirco/berth/internal/agent/docker"
	"github.com/wangsirco/berth/internal/errs"
)

//go:embed Dockerfile
var Dockerfile string

const (
	Container = "berth-caddy"
	Network   = "berth-edge"
	Project   = "berth-edge"
	Image     = "berth/caddy:2.11-3" // bump the suffix when the Dockerfile changes
	ImageWAF  = Image + "-waf"       // the same with the Coraza WAF (the WAF extension)
)

// ImageFor is the edge image with or without the WAF.
func ImageFor(waf bool) string {
	if waf {
		return ImageWAF
	}
	return Image
}

// HasWAF says whether an edge image carries Coraza: the -waf builds, and every build from
// before the WAF became optional (they all had it).
func HasWAF(img string) bool {
	if img == "" {
		return false
	}
	if strings.HasSuffix(img, "-waf") {
		return true
	}
	n, err := strconv.Atoi(strings.TrimPrefix(img, "berth/caddy:2.11-"))
	return strings.HasPrefix(img, "berth/caddy:2.11-") && err == nil && n < 3
}

// Root is where the edge keeps its files (tests point it elsewhere).
var Root = "/srv/berth/caddy"

func adminSock() string  { return filepath.Join(Root, "run", "admin.sock") }
func configPath() string { return filepath.Join(Root, "config", "config.json") }

// Compose is the berth-edge stack (without the WAF). The image is built on the host from the
// Dockerfile.
func Compose() string { return ComposeFor(false) }

// ComposeFor is the stack with or without the WAF.
func ComposeFor(waf bool) string {
	arg := "0"
	if waf {
		arg = "1"
	}
	return fmt.Sprintf(`# Managed by Berth: the edge proxy for every site on this host.
name: %[1]s
services:
  caddy:
    image: %[2]s
    build:
      context: .
      args:
        WAF: "%[6]s"
    container_name: %[3]s
    restart: unless-stopped
    command: ["caddy", "run", "--config", "/etc/caddy/config.json", "--resume=false"]
    ports:
      - "80:80"
      - "443:443"
      - "443:443/udp"
    extra_hosts:
      - "host.docker.internal:host-gateway"
    volumes:
      - %[4]s/config:/etc/caddy
      - %[4]s/data:/data
      - %[4]s/logs:/var/log/caddy
      - %[4]s/sites:/srv/sites:ro
      - %[4]s/run:/run/caddy
    networks: [edge]
    mem_limit: 256m
networks:
  edge:
    name: %[5]s
    external: true
`, Project, ImageFor(waf), Container, Root, Network, arg)
}

// Initial is the config the edge starts with before any site exists.
func Initial() []byte {
	b, _ := json.MarshalIndent(map[string]any{
		"admin":   map[string]any{"listen": "unix//run/caddy/admin.sock"},
		"logging": map[string]any{"logs": map[string]any{"default": map[string]any{"level": "WARN"}}},
		"apps":    map[string]any{"http": map[string]any{"servers": map[string]any{}}},
	}, "", "  ")
	return b
}

type Admin struct {
	hc *http.Client
	mu sync.Mutex // one config change at a time
	dc *docker.Client
}

func New(dc *docker.Client) *Admin {
	tr := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "unix", adminSock())
	}}
	return &Admin{hc: &http.Client{Transport: tr, Timeout: 60 * time.Second}, dc: dc}
}

func (a *Admin) call(ctx context.Context, method, path string, body []byte) ([]byte, error) {
	req, _ := http.NewRequestWithContext(ctx, method, "http://caddy"+path, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := a.hc.Do(req)
	if err != nil {
		return nil, errs.New(502, "edge.unreachable", "detail", err.Error())
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if resp.StatusCode >= 400 {
		return nil, errs.New(400, "edge.config_rejected", "detail", caddyError(b))
	}
	return b, nil
}

func caddyError(b []byte) string {
	var e struct{ Error string }
	if json.Unmarshal(b, &e) == nil && e.Error != "" {
		return e.Error
	}
	return strings.TrimSpace(string(b))
}

// Prepare creates the directories, the network, the initial config and the stack files.
func (a *Admin) Prepare(ctx context.Context, waf bool) (map[string]any, error) {
	for _, d := range []string{"config", "data", "logs", "sites", "run"} {
		if err := os.MkdirAll(filepath.Join(Root, d), 0o755); err != nil {
			return nil, err
		}
	}
	if _, err := os.Stat(configPath()); os.IsNotExist(err) {
		if err := os.WriteFile(configPath(), Initial(), 0o644); err != nil {
			return nil, err
		}
	}
	created, err := a.dc.EnsureNetwork(ctx, Network)
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(docker.StacksDir, Project)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte(Dockerfile), 0o644); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte(ComposeFor(waf)), 0o644); err != nil {
		return nil, err
	}
	return map[string]any{"network_created": created, "dir": dir}, nil
}

// Validate checks a config with `caddy validate` inside the running container.
func (a *Admin) Validate(ctx context.Context, cfg []byte) error {
	tmp := filepath.Join(Root, "config", ".validate.json")
	if err := os.WriteFile(tmp, cfg, 0o600); err != nil {
		return err
	}
	defer os.Remove(tmp)
	out, code, err := a.dc.Exec(ctx, Container, []string{"caddy", "validate", "--config", "/etc/caddy/.validate.json"})
	if err != nil {
		return errs.New(502, "edge.not_running", "detail", err.Error())
	}
	if code != 0 {
		return errs.New(400, "edge.config_rejected", "detail", lastLines(out, 6))
	}
	return nil
}

func lastLines(s string, n int) string {
	l := strings.Split(strings.TrimSpace(s), "\n")
	if len(l) > n {
		l = l[len(l)-n:]
	}
	return strings.Join(l, "\n")
}

// Load swaps the running config (Caddy rolls back by itself if the new one fails to
// start) and then saves it, keeping the previous file as config.prev.json.
func (a *Admin) Load(ctx context.Context, cfg []byte) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !json.Valid(cfg) {
		return errs.New(400, "edge.config_rejected", "detail", "not JSON")
	}
	if _, err := a.call(ctx, "POST", "/load", cfg); err != nil {
		return err
	}
	if old, err := os.ReadFile(configPath()); err == nil {
		_ = os.WriteFile(filepath.Join(Root, "config", "config.prev.json"), old, 0o600)
	}
	tmp := configPath() + ".tmp"
	if err := os.WriteFile(tmp, cfg, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, configPath())
}

// Config is the config on disk (what the edge runs after Load).
func (a *Admin) Config() ([]byte, error) { return os.ReadFile(configPath()) }

// Register adds the edge routes to the agent.
func Register(s *agent.Server, a *Admin) {
	registerTamper(s)
	registerCerts(s)
	registerProbes(s, a)
	s.Handle("POST /v1/edge/prepare", func(w http.ResponseWriter, r *http.Request, body []byte) {
		var in struct {
			WAF bool `json:"waf"`
		}
		json.Unmarshal(body, &in)
		out, err := a.Prepare(r.Context(), in.WAF)
		reply(w, out, err)
	})
	s.Handle("GET /v1/edge/status", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		st := map[string]any{"installed": false, "running": false, "image": Image}
		if _, err := os.Stat(filepath.Join(docker.StacksDir, Project, "compose.yaml")); err == nil {
			st["installed"] = true
		}
		if d, err := a.dc.Inspect(r.Context(), Container); err == nil {
			st["running"] = d.State == "running"
			st["state"] = d.State
			st["running_image"] = d.Image
			st["waf"] = HasWAF(d.Image)
		}
		agent.WriteJSON(w, 200, st)
	})
	s.Handle("GET /v1/edge/config", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		b, err := a.Config()
		if err != nil {
			reply(w, nil, err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(b)
	})
	s.Handle("POST /v1/edge/validate", func(w http.ResponseWriter, r *http.Request, body []byte) {
		reply(w, map[string]bool{"ok": true}, a.Validate(r.Context(), body))
	})
	s.Handle("POST /v1/edge/load", func(w http.ResponseWriter, r *http.Request, body []byte) {
		reply(w, map[string]bool{"ok": true}, a.Load(r.Context(), body))
	})
	s.Handle("POST /v1/edge/connect", func(w http.ResponseWriter, r *http.Request, body []byte) {
		var in struct {
			Container string `json:"container"`
		}
		json.Unmarshal(body, &in)
		reply(w, map[string]bool{"ok": true}, a.dc.Connect(r.Context(), Network, in.Container))
	})
}

func reply(w http.ResponseWriter, v any, err error) {
	if err != nil {
		agent.WriteErr(w, err)
		return
	}
	agent.WriteJSON(w, 200, v)
}
