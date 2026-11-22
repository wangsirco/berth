package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"github.com/wangsirco/berth/internal/agent/drift/driftapi"
	"log"
	"net"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/wangsirco/berth/internal/agent"
	"github.com/wangsirco/berth/internal/agent/accesslog"
	"github.com/wangsirco/berth/internal/agent/backup"
	"github.com/wangsirco/berth/internal/agent/caddy"
	"github.com/wangsirco/berth/internal/agent/dbconn"
	"github.com/wangsirco/berth/internal/agent/desktop"
	"github.com/wangsirco/berth/internal/agent/devenv"
	"github.com/wangsirco/berth/internal/agent/docker"
	"github.com/wangsirco/berth/internal/agent/files"
	"github.com/wangsirco/berth/internal/agent/firewall"
	"github.com/wangsirco/berth/internal/agent/flows"
	"github.com/wangsirco/berth/internal/agent/incus"
	"github.com/wangsirco/berth/internal/agent/metrics"
	"github.com/wangsirco/berth/internal/agent/migrate"
	"github.com/wangsirco/berth/internal/agent/proxy"
	"github.com/wangsirco/berth/internal/agent/pty"
	"github.com/wangsirco/berth/internal/agent/system"
	"github.com/wangsirco/berth/internal/agent/traffic"
	"github.com/wangsirco/berth/internal/pipeline"
	"github.com/wangsirco/berth/internal/sign"
)

type agentConfig struct {
	Socket string            `json:"socket"`
	Group  string            `json:"group"`
	Keys   map[string]string `json:"keys"` // keyID -> base64 secret
	// Remote is set on hosts joined to a panel on another machine: the agent dials it.
	Remote *remoteConfig `json:"remote,omitempty"`
}

type remoteConfig struct {
	PanelURL string `json:"panel_url"`
	PanelFP  string `json:"panel_fp"`
	Name     string `json:"name"`
	KeyID    string `json:"key_id"` // its secret is in Keys
}

// panelKey is what the panel reads from /etc/berth/panel-agent.key to sign its requests.
type panelKey struct {
	KeyID  string `json:"key_id"`
	Secret string `json:"secret"`
	Socket string `json:"socket"`
}

func runAgent(args []string) error {
	if len(args) > 0 && args[0] == "enroll" {
		return runEnroll(args[1:])
	}
	fs := flag.NewFlagSet("agent", flag.ContinueOnError)
	etc := fs.String("etc", "/etc/berth", "config directory")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := loadOrInitAgentConfig(*etc)
	if err != nil {
		return err
	}
	keys := map[string][]byte{}
	for id, b64 := range cfg.Keys {
		k, err := base64.StdEncoding.DecodeString(b64)
		if err != nil {
			return fmt.Errorf("agent.json: key %s: %w", id, err)
		}
		keys[id] = k
	}
	if err := os.MkdirAll(filepath.Dir(cfg.Socket), 0o750); err != nil {
		return err
	}
	// systemd's RuntimeDirectory is root:root; the panel (group berth) must traverse it.
	if g, err := user.LookupGroup(cfg.Group); err == nil {
		gid, _ := strconv.Atoi(g.Gid)
		_ = os.Chown(filepath.Dir(cfg.Socket), 0, gid)
	}
	mk, err := agent.LoadOrCreateMasterKey(filepath.Join(*etc, "master.key"))
	if err != nil {
		return err
	}
	s := agent.NewServer(agent.Config{Socket: cfg.Socket, Keys: keys, Group: cfg.Group})
	agent.RegisterMasterKey(s, mk)
	registerModules(s)
	if r := cfg.Remote; r != nil {
		rc := agent.RemoteConfig{PanelURL: r.PanelURL, PanelFP: r.PanelFP, KeyID: r.KeyID, Secret: keys[r.KeyID], Name: r.Name}
		go agent.RunRemote(context.Background(), rc, s, log.Printf)
	}
	return s.ListenAndServe()
}

// runEnroll: berth agent enroll --panel URL --code CODE --fp SHA256 --name NAME
// (run by the panel's join script).
func runEnroll(args []string) error {
	fs := flag.NewFlagSet("agent enroll", flag.ContinueOnError)
	etc := fs.String("etc", "/etc/berth", "config directory")
	panelURL := fs.String("panel", "", "panel URL, https://host:7090")
	code := fs.String("code", "", "one-time join code")
	fp := fs.String("fp", "", "SHA-256 fingerprint of the panel's CA certificate")
	name := fs.String("name", "", "host name in the panel")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *panelURL == "" || *code == "" || *fp == "" {
		return errors.New("usage: berth agent enroll --panel URL --code CODE --fp SHA256 [--name NAME]")
	}
	cfg, err := loadOrInitAgentConfig(*etc)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := agent.CheckPanel(ctx, agent.RemoteConfig{PanelURL: *panelURL, PanelFP: *fp}); err != nil {
		return fmt.Errorf("panel %s: %w", *panelURL, err)
	}
	keyID, secret, err := agent.Enroll(ctx, *panelURL, *fp, *code, *name)
	if err != nil {
		return err
	}
	if cfg.Remote != nil {
		delete(cfg.Keys, cfg.Remote.KeyID) // re-joining replaces the old key
	}
	cfg.Keys[keyID] = base64.StdEncoding.EncodeToString(secret)
	cfg.Remote = &remoteConfig{PanelURL: *panelURL, PanelFP: *fp, Name: *name, KeyID: keyID}
	if err := writeJSONFile(filepath.Join(*etc, "agent.json"), cfg, 0o600, -1); err != nil {
		return err
	}
	fmt.Printf("Joined %s as %q.\n", *panelURL, *name)
	return nil
}

// registerModules wires feature handlers (docker, metrics, pty, files...) into the agent.
func registerModules(s *agent.Server) {
	dc := docker.New("/var/run/docker.sock")
	docker.Register(s, dc)
	smp := metrics.NewSampler()
	go smp.Run(time.Second, nil)
	vmc := incus.New()
	metrics.VMs = func(ctx context.Context) []metrics.Berth {
		list, err := vmc.Instances(ctx)
		if err != nil {
			return nil
		}
		var out []metrics.Berth
		for _, in := range list {
			if strings.EqualFold(in.Status, "running") && in.MemUsed > 0 {
				out = append(out, metrics.Berth{Type: "vm", ID: in.Name, Name: in.Name, MemBytes: in.MemUsed, Project: in.Type})
			}
		}
		return out
	}
	metrics.Register(s, smp, dc)
	pty.Register(s, pty.NewManager("/var/lib/berth/recordings"))
	files.Register(s, files.New("/var/lib/berth/trash"))
	caddy.Register(s, caddy.New(dc))
	accesslog.Register(s, caddy.Root+"/logs", "/var/lib/berth")
	pipeline.Register(s, pipeline.NewExecutor("/srv/berth/workspaces"))
	dbconn.Register(s)
	backup.Register(s)
	firewall.CountryNets = func(ctx context.Context, code string) ([]string, error) {
		if accesslog.Shared == nil {
			return nil, fmt.Errorf("country database unavailable")
		}
		return accesslog.Shared.CountryNets(ctx, code)
	}
	firewall.Register(s)
	proxy.Country = func(ip string) string {
		if accesslog.Shared == nil {
			return ""
		}
		return accesslog.Shared.Country(net.ParseIP(ip))
	}
	proxy.Register(s)
	traffic.Register(s, dc)
	incus.Register(s)
	migrate.Register(s)
	desktop.Register(s)
	devenv.Register(s)
	fr := flows.New(dc, incus.New())
	fr.Geo = func(ip net.IP) string {
		if accesslog.Shared == nil {
			return ""
		}
		return accesslog.Shared.Country(ip)
	}
	flows.Register(s, fr)
	system.Register(s, dc)
	driftapi.Register(s)
}

func loadOrInitAgentConfig(etc string) (*agentConfig, error) {
	path := filepath.Join(etc, "agent.json")
	b, err := os.ReadFile(path)
	if err == nil {
		var c agentConfig
		if err := json.Unmarshal(b, &c); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		return &c, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return nil, err
	}
	id := "local-" + sign.RandomHex(4)
	c := &agentConfig{Socket: "/run/berth/agent.sock", Group: "berth", Keys: map[string]string{id: base64.StdEncoding.EncodeToString(secret)}}
	if err := os.MkdirAll(etc, 0o750); err != nil {
		return nil, err
	}
	if err := writeJSONFile(path, c, 0o600, -1); err != nil {
		return nil, err
	}
	gid := -1
	if g, err := user.LookupGroup(c.Group); err == nil {
		gid, _ = strconv.Atoi(g.Gid)
		_ = os.Chown(etc, 0, gid)
	}
	pk := panelKey{KeyID: id, Secret: c.Keys[id], Socket: c.Socket}
	if err := writeJSONFile(filepath.Join(etc, "panel-agent.key"), pk, 0o640, gid); err != nil {
		return nil, err
	}
	return c, nil
}

func writeJSONFile(path string, v any, mode os.FileMode, gid int) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, mode); err != nil {
		return err
	}
	if gid >= 0 {
		if err := os.Chown(tmp, 0, gid); err != nil {
			return err
		}
	}
	return os.Rename(tmp, path)
}
