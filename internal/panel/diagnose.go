package panel

// One-click site diagnosis (方案 §5.2.6): DNS → edge → certificate → Caddy → upstream
// → application → the request end to end. Each layer says ok / warn / fail with a
// reason; fixes are ordinary change kinds the UI (or an AI) can run in one click.

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/wangsirco/berth/internal/agent/caddy"
	"github.com/wangsirco/berth/internal/agent/docker"
	"github.com/wangsirco/berth/internal/change"
	"github.com/wangsirco/berth/internal/errs"
	"github.com/wangsirco/berth/internal/panel/caddycfg"
	"github.com/wangsirco/berth/internal/panel/dns"
	"github.com/wangsirco/berth/internal/tlsutil"
)

type diagFix struct {
	Kind   string `json:"kind"`
	Params any    `json:"params"`
	Label  string `json:"label"` // i18n key
}

type diagLayer struct {
	Layer   string         `json:"layer"`  // dns | edge | cert | caddy | upstream | app | request
	Status  string         `json:"status"` // ok | warn | fail | skip
	Code    string         `json:"code"`
	Params  map[string]any `json:"params,omitempty"`
	Message string         `json:"message"`
	Detail  []string       `json:"detail,omitempty"`
	Fix     *diagFix       `json:"fix,omitempty"`
}

type diagnosis struct {
	Site   Site        `json:"site"`
	Host   string      `json:"host"`
	Status string      `json:"status"` // worst layer
	Layers []diagLayer `json:"layers"`
	At     int64       `json:"at"`
}

func lay(layer, status, code string, kv ...any) diagLayer {
	l := diagLayer{Layer: layer, Status: status, Code: code, Params: map[string]any{}}
	for i := 0; i+1 < len(kv); i += 2 {
		l.Params[kv[i].(string)] = kv[i+1]
	}
	return l
}

func inRanges(ip string, ranges []string) bool {
	p := net.ParseIP(ip)
	for _, r := range ranges {
		if _, n, err := net.ParseCIDR(r); err == nil && p != nil && n.Contains(p) {
			return true
		}
	}
	return false
}

func localSuffix(d string) bool {
	for _, z := range []string{".lan", ".local", ".home.arpa", ".internal", ".localhost"} {
		if strings.HasSuffix(d, z) {
			return true
		}
	}
	return false
}

func (a *App) diagnose(ctx context.Context, host string, s Site) diagnosis {
	d := diagnosis{Site: s, Host: host, At: time.Now().Unix()}
	add := func(l diagLayer) { d.Layers = append(d.Layers, l) }

	add(a.diagDNS(ctx, host, s))
	edge := a.diagEdge(ctx, host, s)
	add(edge)
	if edge.Status == "fail" {
		d.finish()
		return d
	}
	certL, reqL := a.diagCert(ctx, host, s)
	add(certL)
	add(a.diagCaddy(ctx, host, s))
	add(a.diagUpstream(ctx, host, s))
	add(a.diagApp(ctx, host, s))
	add(reqL)
	d.finish()
	return d
}

func (d *diagnosis) finish() {
	rank := map[string]int{"skip": 0, "ok": 1, "warn": 2, "fail": 3}
	d.Status = "ok"
	for _, l := range d.Layers {
		if rank[l.Status] > rank[d.Status] {
			d.Status = l.Status
		}
	}
}

// ---- DNS ----

type dnsRes struct {
	Resolver string   `json:"resolver"`
	IPs      []string `json:"ips"`
	Error    string   `json:"error"`
}

func (a *App) diagDNS(ctx context.Context, host string, s Site) diagLayer {
	name := s.Primary()
	if strings.HasPrefix(name, "*.") {
		name = "berth-check" + name[1:]
	}
	if net.ParseIP(name) != nil {
		return lay("dns", "skip", "diag.dns_ip")
	}
	local := localSuffix(name)
	resolvers := []string{"1.1.1.1", "8.8.8.8", "system"}
	if local {
		resolvers = []string{"system"}
	}
	var res []dnsRes
	if err := a.call(ctx, host, "POST", "/v1/probe/dns", map[string]any{"name": name, "resolvers": resolvers}, &res); err != nil {
		return lay("dns", "warn", "diag.probe_failed", "detail", errs.From(err).Code)
	}
	var detail []string
	all := map[string]bool{}
	missing := 0
	for _, r := range res {
		line := r.Resolver + ": " + strings.Join(r.IPs, ", ")
		if r.Error != "" {
			line = r.Resolver + ": " + r.Error
			missing++
		}
		detail = append(detail, line)
		for _, ip := range r.IPs {
			all[ip] = true
		}
	}
	ips := make([]string, 0, len(all))
	for ip := range all {
		ips = append(ips, ip)
	}
	if len(ips) == 0 && local {
		l := lay("dns", "warn", "diag.dns_local_missing", "name", name)
		l.Detail = detail
		return l
	}
	if len(ips) == 0 {
		l := lay("dns", "fail", "diag.dns_missing", "name", name)
		l.Detail = detail
		if pub, err := a.publicIP(ctx, host, "A"); err == nil && !local {
			l.Fix = a.dnsFix(ctx, name, pub)
		}
		return l
	}
	pub, _ := a.publicIP(ctx, host, "A")
	var l diagLayer
	switch {
	case pub != "" && all[pub]:
		l = lay("dns", "ok", "diag.dns_ok", "name", name, "ip", pub)
	case allIn(ips, func(ip string) bool { return inRanges(ip, caddycfg.CloudflareRanges) }):
		l = lay("dns", "ok", "diag.dns_cloudflare", "name", name)
	case allIn(ips, func(ip string) bool {
		p := net.ParseIP(ip)
		return p != nil && (tlsutil.PrivateIP(p) || p.IsLoopback())
	}):
		if local {
			l = lay("dns", "ok", "diag.dns_local", "name", name, "ips", strings.Join(ips, ", "))
		} else {
			l = lay("dns", "warn", "diag.dns_private", "name", name, "ips", strings.Join(ips, ", "))
		}
	default:
		l = lay("dns", "fail", "diag.dns_elsewhere", "name", name, "ips", strings.Join(ips, ", "), "expected", pub)
		if pub != "" {
			l.Fix = a.dnsFix(ctx, name, pub)
		}
	}
	if missing > 0 && l.Status == "ok" {
		l.Status, l.Code = "warn", "diag.dns_propagating"
	}
	l.Detail = detail
	return l
}

func allIn(l []string, f func(string) bool) bool {
	for _, v := range l {
		if !f(v) {
			return false
		}
	}
	return len(l) > 0
}

// dnsFix: create or point the A record at ip, through a Cloudflare account that has the zone.
func (a *App) dnsFix(ctx context.Context, name, ip string) *diagFix {
	list, _ := a.integrations()
	for _, in := range list {
		if in.Kind != "cloudflare" {
			continue
		}
		prov, err := a.provider(in.ID)
		if err != nil {
			continue
		}
		zones, err := prov.Zones(ctx)
		if err != nil {
			continue
		}
		z, ok := dns.ZoneFor(zones, name)
		if !ok {
			continue
		}
		recs, _ := prov.Records(ctx, z.ID)
		for _, r := range recs {
			if r.Name == name && (r.Type == "A" || r.Type == "CNAME") {
				if r.Type == "CNAME" {
					return nil // a CNAME is someone's deliberate choice; leave it
				}
				r.Content = ip
				return &diagFix{Kind: "dns.record.update", Label: "fix.dns_point",
					Params: recordParams{Integration: in.ID, ZoneID: z.ID, Record: r}}
			}
		}
		return &diagFix{Kind: "dns.record.create", Label: "fix.dns_create",
			Params: recordParams{Integration: in.ID, ZoneID: z.ID, Record: dns.Record{Type: "A", Name: name, Content: ip, TTL: 1}}}
	}
	return nil
}

// ---- edge ----

func (a *App) diagEdge(ctx context.Context, host string, s Site) diagLayer {
	st, err := a.edgeStatus(ctx, host)
	if err != nil {
		return lay("edge", "fail", "diag.host_offline", "host", host)
	}
	if !st.Running {
		l := lay("edge", "fail", "diag.edge_down")
		l.Fix = &diagFix{Kind: "edge.install", Params: map[string]any{}, Label: "fix.edge_install"}
		return l
	}
	var detail []string
	for _, port := range []int{80, 443} {
		var t struct {
			OK    bool   `json:"ok"`
			Error string `json:"error"`
		}
		a.call(ctx, host, "POST", "/v1/probe/tcp", map[string]string{"addr": "127.0.0.1:" + strconv.Itoa(port)}, &t)
		if !t.OK {
			return lay("edge", "fail", "diag.edge_port", "port", port, "detail", t.Error)
		}
		detail = append(detail, strconv.Itoa(port)+": ok")
	}
	if !s.Enabled {
		l := lay("edge", "warn", "diag.site_disabled")
		l.Fix = &diagFix{Kind: "site.toggle", Params: map[string]any{"id": s.ID, "enabled": true}, Label: "fix.site_enable"}
		return l
	}
	cfg, err := a.agentRaw(ctx, host, "/v1/edge/config")
	if err == nil && !strings.Contains(cfg, `"`+s.Primary()+`"`) {
		l := lay("edge", "fail", "diag.site_not_loaded")
		l.Fix = &diagFix{Kind: "site.update", Params: s, Label: "fix.site_reapply"}
		return l
	}
	l := lay("edge", "ok", "diag.edge_ok")
	l.Detail = detail
	return l
}

func (a *App) agentRaw(ctx context.Context, host, path string) (string, error) {
	var raw json.RawMessage
	if err := a.call(ctx, host, "GET", path, nil, &raw); err != nil {
		return "", err
	}
	return string(raw), nil
}

// ---- certificate and the request itself ----

type httpsRes struct {
	Status int    `json:"status"`
	MS     int64  `json:"ms"`
	Error  string `json:"error"`
	Cert   *struct {
		Names       []string  `json:"names"`
		Issuer      string    `json:"issuer"`
		NotAfter    time.Time `json:"not_after"`
		Verified    bool      `json:"verified"`
		VerifyError string    `json:"verify_error"`
	} `json:"cert"`
}

func (a *App) diagCert(ctx context.Context, host string, s Site) (diagLayer, diagLayer) {
	name := s.Primary()
	if strings.HasPrefix(name, "*.") {
		name = "berth-check" + name[1:]
	}
	if s.TLS.Mode == "off" {
		return lay("cert", "skip", "diag.cert_off"), lay("request", "skip", "diag.request_http")
	}
	var r httpsRes
	if err := a.call(ctx, host, "POST", "/v1/probe/https", map[string]any{"host": name, "ca_pem": string(a.caPEM)}, &r); err != nil {
		return lay("cert", "warn", "diag.probe_failed", "detail", errs.From(err).Code), lay("request", "skip", "diag.request_skipped")
	}
	var cert diagLayer
	switch {
	case r.Cert == nil:
		cert = lay("cert", "fail", "diag.cert_none")
		if s.TLS.Mode == "auto" || s.TLS.Mode == "dns" {
			cert.Code = "diag.cert_pending"
		}
		cert.Detail = []string{r.Error}
	case time.Now().After(r.Cert.NotAfter):
		cert = lay("cert", "fail", "diag.cert_expired", "date", r.Cert.NotAfter.Format("2006-01-02"))
	case !r.Cert.Verified:
		status := "fail"
		if s.TLS.Mode == "internal" || s.TLS.Mode == "custom" {
			status = "warn" // devices that trust the CA are fine
		}
		cert = lay("cert", status, "diag.cert_untrusted", "issuer", r.Cert.Issuer)
		cert.Detail = []string{r.Cert.VerifyError}
		if s.TLS.CertID > 0 {
			if c, err := a.cert(s.TLS.CertID); err == nil && (c.Source == "self" || c.Source == "internal") {
				cert.Fix = &diagFix{Kind: "cert.reissue", Params: map[string]any{"id": c.ID}, Label: "fix.cert_reissue"}
			}
		}
	default:
		days := int(time.Until(r.Cert.NotAfter).Hours() / 24)
		cert = lay("cert", "ok", "diag.cert_ok", "issuer", r.Cert.Issuer, "days", days)
		if days < 14 {
			cert.Status, cert.Code = "warn", "diag.cert_expiring"
		}
	}
	var req diagLayer
	switch {
	case r.Status == 0:
		req = lay("request", "fail", "diag.request_failed")
		req.Detail = []string{r.Error}
	case r.Status == 502 || r.Status == 503 || r.Status == 504:
		req = lay("request", "fail", "diag.request_gateway", "status", r.Status)
	case r.Status >= 500:
		req = lay("request", "warn", "diag.request_5xx", "status", r.Status)
	case r.Status == 401 && len(s.Options.BasicAuth) > 0:
		req = lay("request", "ok", "diag.request_auth", "ms", r.MS)
	default:
		req = lay("request", "ok", "diag.request_ok", "status", r.Status, "ms", r.MS)
	}
	return cert, req
}

// ---- Caddy's own log ----

func (a *App) diagCaddy(ctx context.Context, host string, s Site) diagLayer {
	var lines []string
	if err := a.call(ctx, host, "GET", "/v1/edge/errors?domain="+s.Primary(), nil, &lines); err != nil {
		return lay("caddy", "skip", "diag.probe_failed", "detail", errs.From(err).Code)
	}
	if len(lines) == 0 {
		return lay("caddy", "ok", "diag.caddy_ok")
	}
	l := lay("caddy", "warn", "diag.caddy_errors", "count", len(lines))
	if len(lines) > 5 {
		lines = lines[len(lines)-5:]
	}
	l.Detail = lines
	return l
}

// ---- upstream ----

type listenPort struct {
	Port     int    `json:"port"`
	Proto    string `json:"proto"`
	Loopback bool   `json:"loopback"`
}

func (a *App) diagUpstream(ctx context.Context, host string, s Site) diagLayer {
	t := s.Target
	switch t.Type {
	case "redirect":
		return lay("upstream", "skip", "diag.upstream_redirect")
	case "static":
		var st struct {
			Dir    string `json:"dir"`
			Exists bool   `json:"exists"`
			Index  bool   `json:"index"`
		}
		if err := a.call(ctx, host, "GET", "/v1/edge/sitedir?domain="+s.Primary(), nil, &st); err != nil {
			return lay("upstream", "skip", "diag.probe_failed", "detail", errs.From(err).Code)
		}
		switch {
		case !st.Exists:
			return lay("upstream", "fail", "diag.static_missing", "dir", st.Dir)
		case !st.Index:
			return lay("upstream", "warn", "diag.static_no_index", "dir", st.Dir)
		}
		return lay("upstream", "ok", "diag.static_ok", "dir", st.Dir)
	case "container":
		cs, err := a.containers(ctx, host)
		if err != nil {
			return lay("upstream", "skip", "diag.probe_failed", "detail", errs.From(err).Code)
		}
		var c *docker.Container
		for i := range cs {
			if cs[i].Name == t.Container {
				c = &cs[i]
			}
		}
		if c == nil {
			return lay("upstream", "fail", "diag.upstream_missing", "container", t.Container)
		}
		if c.State != "running" {
			l := lay("upstream", "fail", "diag.upstream_stopped", "container", t.Container, "state", c.State)
			l.Fix = &diagFix{Kind: "container.start", Params: map[string]string{"name": t.Container}, Label: "fix.container_start"}
			return l
		}
		if !contains(c.Networks, caddy.Network) {
			l := lay("upstream", "fail", "diag.upstream_not_on_edge", "container", t.Container)
			l.Fix = &diagFix{Kind: "edge.connect", Params: map[string]string{"container": t.Container}, Label: "fix.edge_connect"}
			return l
		}
		var ls []listenPort
		if a.call(ctx, host, "GET", "/v1/docker/containers/"+c.ID+"/listening", nil, &ls) == nil && len(ls) > 0 {
			found, loop := false, false
			var ports []string
			for _, p := range ls {
				if p.Port == t.Port {
					found = found || !p.Loopback
					loop = loop || p.Loopback
				}
				if !p.Loopback && !contains(ports, strconv.Itoa(p.Port)) {
					ports = append(ports, strconv.Itoa(p.Port))
				}
			}
			if !found && loop {
				return lay("upstream", "fail", "diag.upstream_loopback", "container", t.Container, "port", t.Port)
			}
			if !found {
				return lay("upstream", "fail", "diag.upstream_port", "container", t.Container, "port", t.Port, "ports", strings.Join(ports, ", "))
			}
		}
		return a.edgeFetch(ctx, host, t.Scheme, net.JoinHostPort(t.Container, strconv.Itoa(t.Port)))
	case "address":
		var worst diagLayer
		for i, u := range t.Upstreams {
			l := a.edgeFetch(ctx, host, t.Scheme, caddycfgDial(u))
			if i == 0 || l.Status == "fail" {
				worst = l
			}
		}
		return worst
	}
	return lay("upstream", "skip", "diag.upstream_redirect")
}

func caddycfgDial(u string) string {
	h, p, err := net.SplitHostPort(u)
	if err != nil {
		return u
	}
	switch h {
	case "127.0.0.1", "localhost", "::1", "0.0.0.0":
		h = "host.docker.internal"
	}
	return net.JoinHostPort(h, p)
}

func (a *App) edgeFetch(ctx context.Context, host, scheme, addr string) diagLayer {
	if scheme == "" || scheme == "h2c" {
		scheme = "http"
	}
	var r httpsRes
	if err := a.call(ctx, host, "POST", "/v1/probe/edge-http", map[string]string{"url": scheme + "://" + addr + "/"}, &r); err != nil {
		return lay("upstream", "skip", "diag.probe_failed", "detail", errs.From(err).Code)
	}
	switch {
	case r.Status == 0:
		l := lay("upstream", "fail", "diag.upstream_unreachable", "addr", addr)
		l.Detail = []string{r.Error}
		if strings.HasPrefix(addr, "host.docker.internal:") {
			l.Code = "diag.upstream_host_unreachable"
		}
		return l
	case r.Status >= 500:
		return lay("upstream", "warn", "diag.upstream_5xx", "addr", addr, "status", r.Status)
	}
	return lay("upstream", "ok", "diag.upstream_ok", "addr", addr, "status", r.Status, "ms", r.MS)
}

// ---- the application in the container ----

func (a *App) diagApp(ctx context.Context, host string, s Site) diagLayer {
	if s.Target.Type != "container" {
		return lay("app", "skip", "diag.app_none")
	}
	var d docker.ContainerDetail
	if err := a.call(ctx, host, "GET", "/v1/docker/containers/"+s.Target.Container, nil, &d); err != nil {
		return lay("app", "skip", "diag.probe_failed", "detail", errs.From(err).Code)
	}
	// 16K memory pages (Asahi): the program refuses to run; the 4K compatibility VM helps
	if line := a.pageSizeLogs(ctx, host, s.Target.Container); line != "" {
		l := lay("app", "fail", "diag.app_pagesize", "container", s.Target.Container)
		l.Detail = []string{line}
		l.Fix = a.compatFix(ctx, host, s.Target.Container, d.Project, d.Service)
		return l
	}
	switch {
	case d.OOMKilled:
		l := lay("app", "fail", "diag.app_oom", "container", s.Target.Container)
		l.Fix = &diagFix{Kind: "container.restart", Params: map[string]string{"name": s.Target.Container}, Label: "fix.container_restart"}
		return l
	case d.Health == "unhealthy":
		return lay("app", "warn", "diag.app_unhealthy", "container", s.Target.Container)
	case d.RestartCount > 0:
		return lay("app", "warn", "diag.app_restarts", "container", s.Target.Container, "count", d.RestartCount)
	}
	return lay("app", "ok", "diag.app_ok", "container", s.Target.Container)
}

// ---- API, kind, MCP ----

func (a *App) siteByRef(host, ref string) (Site, error) {
	if id, err := strconv.ParseInt(ref, 10, 64); err == nil {
		return a.site(host, id)
	}
	list, err := a.sites(host)
	if err != nil {
		return Site{}, err
	}
	for _, s := range list {
		if contains(s.Domains, strings.ToLower(ref)) {
			return s, nil
		}
	}
	return Site{}, errs.New(404, "site.not_found", "id", ref)
}

func (a *App) translateDiag(locale string, d *diagnosis) {
	for i := range d.Layers {
		d.Layers[i].Message = T(locale, d.Layers[i].Code, d.Layers[i].Params)
		if f := d.Layers[i].Fix; f != nil {
			f.Label = T(locale, f.Label, nil)
		}
	}
}

func init() {
	routeHooks = append(routeHooks, func(a *App, mux *http.ServeMux) {
		// live DNS check while typing a domain in the new-site dialog
		mux.HandleFunc("GET /api/v1/hosts/{host}/dns-check", a.authed("sites.view", func(w http.ResponseWriter, r *http.Request) {
			name := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("name")))
			if name == "" {
				a.fail(w, r, errs.New(400, "site.no_domain"))
				return
			}
			ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
			defer cancel()
			l := a.diagDNS(ctx, r.PathValue("host"), Site{Domains: []string{name}})
			l.Message = T(a.locale(r), l.Code, l.Params)
			if l.Fix != nil {
				l.Fix.Label = T(a.locale(r), l.Fix.Label, nil)
			}
			writeJSON(w, 200, l)
		}))
		mux.HandleFunc("POST /api/v1/hosts/{host}/sites/{id}/diagnose", a.authed("sites.view", func(w http.ResponseWriter, r *http.Request) {
			host := r.PathValue("host")
			s, err := a.siteByRef(host, r.PathValue("id"))
			if err != nil {
				a.fail(w, r, err)
				return
			}
			ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
			defer cancel()
			d := a.diagnose(ctx, host, s)
			a.translateDiag(a.locale(r), &d)
			writeJSON(w, 200, d)
		}))
	})

	readTools["diagnose_site"] = readTool{scope: "sites:read",
		desc:     "Diagnose a site layer by layer (DNS, edge, certificate, Caddy log, upstream, app, request). Each layer has a status, a reason and, when there is one, a fix: a change kind with params you can run.",
		props:    map[string]any{"host": hostProp, "site": map[string]any{"type": "string", "description": "site id or one of its domains"}},
		required: []string{"site"},
		run: func(a *App, r *http.Request, host string, args map[string]any) (any, error) {
			s, err := a.siteByRef(host, str(args, "site"))
			if err != nil {
				return nil, err
			}
			ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
			defer cancel()
			d := a.diagnose(ctx, host, s)
			a.translateDiag(a.locale(r), &d)
			return d, nil
		}}

	change.Register(change.Kind{
		Name: "edge.connect", Perm: "sites.manage", Scope: "sites:write", Title: "kind.edge.connect",
		Params: change.ParamSchema{Props: map[string]change.Prop{"container": {Type: "string"}}, Required: []string{"container"}},
		CLI:    "docker network connect berth-edge {{.container}}",
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			var p struct{ Container string }
			json.Unmarshal(raw, &p)
			return change.Plan{Summary: "plan.edge.connect", SummaryP: map[string]any{"container": p.Container}, Risk: change.RiskLow,
				Commands: []string{"docker network connect " + caddy.Network + " " + p.Container},
				Affected: []change.Object{{Type: "container", ID: p.Container, Name: p.Container, Host: env.Host}}}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			var p struct{ Container string }
			json.Unmarshal(raw, &p)
			return nil, nil, svc(env).call(env.Ctx, env.Host, "POST", "/v1/edge/connect", map[string]string{"container": p.Container}, nil)
		},
	})
}
