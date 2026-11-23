// Package panel is berth-panel: HTTP API, sessions, permissions, audit and the
// embedded web UI. It never changes the system itself; it asks berth agent.
package panel

import (
	"context"
	"database/sql"
	"io"
	"io/fs"
	"net"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/wangsirco/berth/internal/agentproto"
	"github.com/wangsirco/berth/internal/change"
	"github.com/wangsirco/berth/internal/errs"
	"github.com/wangsirco/berth/internal/panel/certkit"
	"github.com/wangsirco/berth/internal/tasks"
)

// Config is /etc/berth/panel.json.
type Config struct {
	Listen     string   `json:"listen"`      // ":7090"
	DataDir    string   `json:"data_dir"`    // /var/lib/berth
	EtcDir     string   `json:"etc_dir"`     // /etc/berth
	AllowCIDRs []string `json:"allow_cidrs"` // who may reach the panel at all
	Public     bool     `json:"public"`      // allow any source address (forces two-step sign-in)
}

var DefaultAllowCIDRs = []string{
	"127.0.0.0/8", "::1/128", "10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16",
	"fc00::/7", "fe80::/10", "100.64.0.0/10",
}

type App struct {
	cfg         Config
	db          *sql.DB
	agents      map[string]*agentproto.Client // guarded by agentsMu: remote hosts come and go
	agentsMu    sync.RWMutex
	remotes     map[string]io.Closer // live remote sessions by host name
	web         fs.FS
	apiMux      *http.ServeMux
	links_      *linkStates // panel links: what the probes found
	wharfAsk_   *wharfAsk   // a LAN request to the router's Wharf, waiting for its admin
	discovery_  *discovery  // LAN discovery: the open window and requests waiting // the routes themselves (a linked panel's requests are dispatched here)
	builtinWeb  fs.FS       // the UI compiled in, for going back from a downloaded one
	certRefresh func()      // re-issue the panel certificate (set by main)
	pipe        *change.Pipeline
	masterKey   []byte
	limiter     *loginLimiter
	queue       *tasks.Queue
	metrics     *metricStore
	hub         hub

	mu          sync.RWMutex
	allow       []*net.IPNet
	settings    map[string]string
	lastScan    map[string]time.Time // relation scans per host
	listenCache map[string]listenEntry

	caPEM         []byte
	caFingerprint string
	localSigner   *certkit.Signer
}

func New(cfg Config, db *sql.DB, agents map[string]*agentproto.Client, web fs.FS) *App {
	if len(cfg.AllowCIDRs) == 0 {
		cfg.AllowCIDRs = DefaultAllowCIDRs
	}
	if agents == nil {
		agents = map[string]*agentproto.Client{}
	}
	a := &App{cfg: cfg, db: db, agents: agents, remotes: map[string]io.Closer{}, web: web, builtinWeb: web, limiter: newLimiter(10, loginWindow),
		lastScan: map[string]time.Time{}, listenCache: map[string]listenEntry{}}
	a.pipe = change.NewPipeline(db, gate{app: a})
	a.pipe.Svc = a
	a.pipe.OnDone = func(c *change.Change) {
		a.publishChange("change.done", c)
		a.afterChange(c)
		a.changeActivity(c)
	}
	a.wireApprovals()
	a.pipe.Quota = a.checkQuota
	a.loadSettings()
	a.metrics = newMetricStore(db)
	a.setupQueue()
	a.claudeRecover()
	return a
}

// SetMasterKey installs the 32-byte key used to seal third-party secrets.
func (a *App) SetMasterKey(k []byte) { a.masterKey = k }

// Pipeline exposes the change pipeline (task queue wiring in main).
func (a *App) Pipeline() *change.Pipeline { return a.pipe }

func (a *App) agent(host string) (*agentproto.Client, error) {
	a.agentsMu.RLock()
	c, ok := a.agents[host]
	a.agentsMu.RUnlock()
	if !ok {
		return nil, errs.New(404, "host.not_found", "host", host)
	}
	return c, nil
}

// connectedHosts lists hosts with a live agent connection, sorted.
func (a *App) connectedHosts() []string {
	a.agentsMu.RLock()
	out := make([]string, 0, len(a.agents))
	for h := range a.agents {
		out = append(out, h)
	}
	a.agentsMu.RUnlock()
	sort.Strings(out)
	return out
}

func (a *App) allowNets() []*net.IPNet {
	a.mu.RLock()
	nets := a.allow
	a.mu.RUnlock()
	if nets != nil {
		return nets
	}
	cidrs := a.cfg.AllowCIDRs
	if v := a.setting("access.allow_cidrs"); v != "" {
		cidrs = splitList(v)
	}
	for _, c := range cidrs {
		if _, n, err := net.ParseCIDR(c); err == nil {
			nets = append(nets, n)
		}
	}
	a.mu.Lock()
	a.allow = nets
	a.mu.Unlock()
	return nets
}

// clientIP is the direct peer. X-Forwarded-For is honoured only from loopback
// (a local reverse proxy such as Caddy in front of the panel).
func (a *App) clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			parts := splitList(xff)
			if v := net.ParseIP(parts[len(parts)-1]); v != nil {
				return v.String()
			}
		}
	}
	return host
}

func (a *App) allowed(r *http.Request) bool {
	if a.isPublic() {
		return true
	}
	ip := net.ParseIP(a.clientIP(r))
	if ip == nil {
		return false
	}
	for _, n := range a.allowNets() {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// Start runs background work (metrics collection) until ctx ends.
func (a *App) Start(ctx context.Context) {
	a.loadWebOverride()
	a.reportUpdateResult()
	go a.collect(ctx)
	go a.autoUpdate(ctx)
	go a.renewLoop(ctx)
	go a.ddnsLoop(ctx)
	go a.statsLoop(ctx)
	go a.cronLoop(ctx)
	go a.edgeLoop(ctx)
	go a.backupLoop(ctx)
	go a.alertLoop(ctx)
	go a.uptimeLoop(ctx)
	go a.securityLoop(ctx)
	go a.trafficLoop(ctx)
	go a.flowsLoop(ctx)
	go func() { // hosts may have been reinstalled since the registry was set up
		time.Sleep(5 * time.Second)
		a.trustRegistryEverywhere(ctx)
	}()
	for _, h := range startHooks {
		h(a, ctx)
	}
}

// startHooks run once when the panel starts its background work (modules add themselves).
var startHooks []func(a *App, ctx context.Context)

// cronHooks run every minute, at the minute (modules add themselves).
var cronHooks []func(a *App, at time.Time)

// SetCA publishes the local CA certificate (PEM) and its fingerprint.
func (a *App) SetCA(pemBytes []byte, fingerprint string) {
	a.caPEM, a.caFingerprint = pemBytes, fingerprint
}
