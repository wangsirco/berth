package panel

import (
	"context"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/wangsirco/berth/internal/agent/metrics"
	"github.com/wangsirco/berth/internal/agentproto"
	"github.com/wangsirco/berth/internal/change"
	"github.com/wangsirco/berth/internal/errs"
	"github.com/wangsirco/berth/internal/version"
)

func init() {
	routeHooks = append(routeHooks, func(a *App, mux *http.ServeMux) {
		mux.HandleFunc("GET /api/v1/hosts", a.authed("", a.handleHosts))
		mux.HandleFunc("GET /api/v1/hosts/{host}/overview", a.authed("overview.view", a.handleOverview))
		mux.HandleFunc("GET /api/v1/hosts/{host}/metrics", a.authed("overview.view", a.handleMetrics))
		mux.HandleFunc("GET /api/v1/hosts/{host}/disk-berths", a.authed("overview.view", a.handleDiskBerths))
		mux.HandleFunc("GET /api/v1/statusbar", a.authed("", a.handleStatusbar))
	})
}

type hostView struct {
	Name     string           `json:"name"`
	Online   bool             `json:"online"`
	LastSeen int64            `json:"last_seen"`
	Caps     *agentproto.Caps `json:"caps,omitempty"`
	Addrs    []string         `json:"addrs,omitempty"`
	Remote   bool             `json:"remote,omitempty"`
	Version  string           `json:"version,omitempty"`
	Arch     string           `json:"arch,omitempty"`
	Addr     string           `json:"addr,omitempty"` // where a remote agent connects from
	Upgrade  string           `json:"upgrade,omitempty"` // a newer agent this panel can hand to a remote host
}

// online: a sample in the last 5 seconds.
func (a *App) online(host string) (bool, time.Time) {
	_, at := a.metrics.latestOf(host)
	return time.Since(at) < 5*time.Second, at
}

func (a *App) handleHosts(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	known := map[string]remoteHost{}
	for _, rh := range a.remoteHosts() {
		known[rh.Name] = rh
	}
	all := a.connectedHosts()
	for n := range known {
		if !contains(all, n) {
			all = append(all, n)
		}
	}
	sort.Strings(all)
	names := make([]string, 0, len(all))
	for _, h := range all {
		if len(u.Scope.Hosts) == 0 || u.Role == RoleSuper || contains(u.Scope.Hosts, h) {
			names = append(names, h)
		}
	}
	out := []hostView{}
	for _, h := range names {
		on, at := a.online(h)
		v := hostView{Name: h, Online: on, LastSeen: at.Unix()}
		if h == "local" {
			v.Version = version.Version
		}
		if rh, ok := known[h]; ok {
			v.Remote, v.Version, v.Arch, v.Addr = true, rh.Version, rh.Arch, rh.Addr
			if b := a.agentBinary(rh.Arch); b != "" && rh.Version != "" {
				if nv := binVersion(b); nv != rh.Version {
					v.Upgrade = nv
				}
			}
			if at.IsZero() {
				v.LastSeen = rh.LastSeen
			}
		}
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		var caps agentproto.Caps
		if a.call(ctx, h, "GET", "/v1/caps", nil, &caps) == nil {
			v.Caps = &caps
			v.Online = true
		}
		cancel()
		out = append(out, v)
	}
	writeJSON(w, 200, map[string]any{"items": out, "name": a.setting("panel.name")})
}

type berthView struct {
	metrics.Berth
	Adopted bool `json:"adopted,omitempty"`
}

func (a *App) handleOverview(w http.ResponseWriter, r *http.Request) {
	host := r.PathValue("host")
	h, at := a.metrics.latestOf(host)
	if at.IsZero() { // collector not running yet (or a fresh start): ask once
		_ = a.call(r.Context(), host, "GET", "/v1/metrics/host", nil, &h)
	}
	var berths []metrics.Berth
	berthErr := a.call(r.Context(), host, "GET", "/v1/metrics/berths", nil, &berths)
	bv := make([]berthView, 0, len(berths))
	for _, b := range berths {
		bv = append(bv, berthView{Berth: b, Adopted: b.Project != "" && a.adopted(host, b.Project)})
	}
	tasks, _ := a.queue.List(host, 10)
	out := map[string]any{"host": h, "berths": bv, "tasks": tasks, "version": version.Version}
	if berthErr != nil {
		e := errs.From(berthErr)
		out["berths_error"] = T(a.locale(r), e.Code, e.Params)
	}
	writeJSON(w, 200, out)
}

// GET .../metrics?series=cpu,mem_used&range=24h&points=300
func (a *App) handleMetrics(w http.ResponseWriter, r *http.Request) {
	host := r.PathValue("host")
	q := r.URL.Query()
	n, _ := strconv.Atoi(q.Get("points"))
	if n <= 0 || n > 2000 {
		n = 300
	}
	out := map[string][]point{}
	now := time.Now()
	for _, s := range strings.Split(q.Get("series"), ",") {
		if s = strings.TrimSpace(s); s != "" {
			pts := a.metrics.query(host, s, q.Get("range"), now, n)
			if pts == nil {
				pts = []point{}
			}
			out[s] = pts
		}
	}
	writeJSON(w, 200, map[string]any{"range": q.Get("range"), "series": out})
}

func (a *App) handleDiskBerths(w http.ResponseWriter, r *http.Request) {
	var out map[string]any
	if err := a.call(r.Context(), r.PathValue("host"), "GET", "/v1/metrics/disk-berths", nil, &out); err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, 200, out)
}

// GET /api/v1/statusbar — the bar under every page. Wharf, public IP and backups come in
// later phases and stay null until then.
func (a *App) handleStatusbar(w http.ResponseWriter, r *http.Request) {
	host := r.URL.Query().Get("host")
	if host == "" {
		host = "local"
	}
	on, _ := a.online(host)
	running, waiting := a.queue.Counts()
	pending := 0
	if list, err := a.pipe.List(change.ListFilter{Status: "pending_approval", Limit: 100}); err == nil {
		rr := r.WithContext(contextWith(r, appKey, a))
		for _, c := range list {
			if canSee(rr, c) {
				pending++
			}
		}
	}
	var lastBackup any
	var at int64
	var st string
	if a.db.QueryRow(`SELECT r.started, r.status FROM backup_runs r JOIN backup_jobs j ON j.id = r.job_id
		WHERE j.host=? AND r.kind='backup' ORDER BY r.id DESC LIMIT 1`, host).Scan(&at, &st) == nil {
		lastBackup = map[string]any{"at": at, "status": st}
	}
	writeJSON(w, 200, map[string]any{
		"online": on, "queue": map[string]int{"running": running, "waiting": waiting}, "pending": pending,
		"wharf": a.wharfStatus(), "public_ip": nil, "last_backup": lastBackup, "version": version.Version,
	})
}

// wharfStatus for the status bar: nil when not paired.
func (a *App) wharfStatus() any {
	l, err := a.wharf()
	if err != nil {
		return nil
	}
	return map[string]any{"name": l.Name, "state": l.State, "last_seen": l.LastSeen}
}
