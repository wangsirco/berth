package caddycfg

import (
	"encoding/json"
	"fmt"
	"net"
	"slices"
	"sort"
	"strconv"
	"strings"
)

// Env is what the renderer needs besides the sites.
type Env struct {
	Email string // ACME account email
	// DNS providers by account id: the Caddy dns provider object, e.g.
	// {"name":"cloudflare","api_token":"..."}.
	DNS map[string]map[string]any
	// Uploaded certificates by id: paths inside the container.
	Certs map[int64]CertFiles
	// Panel exposes the panel itself through the edge (optional): its names open it on
	// 443, without a port.
	Panel *PanelEntry
	SitesRoot string // inside the container, default /srv/sites
	LogsRoot  string // inside the container, default /var/log/caddy
	// ProjectPorts: the port each native project currently serves on (0 or missing: not deployed)
	ProjectPorts map[string]int
	// NoWAF: the edge has no WAF (the WAF extension is not installed); WAF settings are skipped
	NoWAF bool
	// LocalProxies: subnets of local proxies in front of the edge whose CF-Connecting-IP /
	// X-Forwarded-For carry the visitor (the edge network, where cloudflared runs)
	LocalProxies []string
}

// PanelEntry: the panel's own names, proxied to the panel (which still listens on its port).
type PanelEntry struct {
	LANHosts    []string  // local names (mini.lan, mini.local): the certificate below, signed by the panel's CA
	PublicHosts []string  // public names: certificates from ACME like any site
	Upstream    string    // host:port of the panel as the edge reaches it
	Cert        CertFiles // for LANHosts
	Secret      string    // X-Berth-Edge: the panel trusts X-Forwarded-For only with it
}

type CertFiles struct {
	Cert, Key string
	Version   string // changes whenever the certificate does, so Caddy loads the new one
}

// CloudflareRanges are trusted when a site restores the client IP from Cloudflare.
var CloudflareRanges = []string{
	"173.245.48.0/20", "103.21.244.0/22", "103.22.200.0/22", "103.31.4.0/22", "141.101.64.0/18",
	"108.162.192.0/18", "190.93.240.0/20", "188.114.96.0/20", "197.234.240.0/22", "198.41.128.0/17",
	"162.158.0.0/15", "104.16.0.0/13", "104.24.0.0/14", "172.64.0.0/13", "131.0.72.0/22",
	"2400:cb00::/32", "2606:4700::/32", "2803:f800::/32", "2405:b500::/32", "2405:8100::/32",
	"2a06:98c0::/29", "2c0f:f248::/32",
}

type obj = map[string]any

// Render builds the full Caddy config. Disabled sites are left out.
func Render(sites []Site, env Env) ([]byte, error) {
	if env.SitesRoot == "" {
		env.SitesRoot = "/srv/sites"
	}
	if env.LogsRoot == "" {
		env.LogsRoot = "/var/log/caddy"
	}
	sort.Slice(sites, func(i, j int) bool { return sites[i].ID < sites[j].ID })

	var httpsRoutes, httpRoutes []any
	var skip []string
	var policies []any
	var loadFiles []any
	loadSNI := map[string][]string{} // tag → the names that certificate is for
	logs := obj{"default": obj{"level": "WARN", "exclude": []string{}}}
	loggerNames := obj{}
	httpLoggerNames := obj{}
	var defaultExclude []string

	for i := range sites {
		s := sites[i]
		if err := Normalize(&s); err != nil {
			if !s.Enabled {
				continue
			}
			return nil, err
		}
		routes := siteRoutes(s, env)
		if !s.Enabled { // a stopped site says so, with its own certificate, instead of vanishing
			if (s.TLS.Mode == "dns" && env.DNS[s.TLS.DNS] == nil) || (s.TLS.Mode == "custom" && env.Certs[s.TLS.CertID].Cert == "") {
				continue
			}
			routes = []any{obj{"match": []any{obj{"host": append(append([]string{}, s.Domains...), wwwAlias(s)...)}}, "handle": []any{refuse(503, StoppedMessage)}, "terminal": true}}
		}
		hosts := s.Domains
		if s.TLS.Mode == "off" {
			httpRoutes = append(httpRoutes, routes...)
			skip = append(skip, hosts...)
		} else {
			httpsRoutes = append(httpsRoutes, routes...)
			if s.Options.NoHTTPSRedirect {
				httpRoutes = append(httpRoutes, routes...)
			}
		}
		// TLS policy
		subjects := append([]string{}, hosts...)
		subjects = append(subjects, wwwAlias(s)...)
		mode := s.TLS.Mode
		if mode == "internal" && s.TLS.CertID != 0 {
			mode = "custom" // signed by the panel's CA
		}
		switch mode {
		case "internal":
			policies = append(policies, obj{"subjects": subjects, "issuers": []any{obj{"module": "internal"}}})
		case "dns":
			prov := env.DNS[s.TLS.DNS]
			if prov == nil {
				return nil, bad("site.tls_dns_missing")
			}
			issuer := obj{"module": "acme", "challenges": obj{"dns": obj{"provider": prov}}}
			if env.Email != "" {
				issuer["email"] = env.Email
			}
			policies = append(policies, obj{"subjects": subjects, "issuers": []any{issuer}})
		case "custom":
			cf, ok := env.Certs[s.TLS.CertID]
			if !ok {
				return nil, bad("site.tls_cert_missing")
			}
			tag := certTag(s.TLS.CertID, cf.Version)
			if _, seen := loadSNI[tag]; !seen {
				loadFiles = append(loadFiles, obj{"certificate": cf.Cert, "key": cf.Key, "tags": []string{tag}})
			}
			loadSNI[tag] = append(loadSNI[tag], subjects...)
			skip = append(skip, subjects...) // no ACME for these names
		}
		// access log, one file per site
		name := "site" + strconv.FormatInt(s.ID, 10)
		logs[name] = obj{
			"writer": obj{"output": "file", "filename": env.LogsRoot + "/" + s.Primary() + ".log",
				"roll_size_mb": 100, "roll_keep": 5, "roll_keep_days": s.Options.LogDays},
			"encoder": obj{"format": "json"},
			"include": []string{"http.log.access." + name},
		}
		defaultExclude = append(defaultExclude, "http.log.access."+name)
		for _, h := range append(append([]string{}, hosts...), wwwAlias(s)...) {
			loggerNames[h] = []string{name}
			if s.TLS.Mode == "off" || s.Options.NoHTTPSRedirect {
				httpLoggerNames[h] = []string{name}
			}
		}
	}
	if p := env.Panel; p != nil && p.Upstream != "" && len(p.LANHosts)+len(p.PublicHosts) > 0 {
		hosts := append(append([]string{}, p.LANHosts...), p.PublicHosts...)
		up := obj{"handler": "reverse_proxy", "upstreams": []any{obj{"dial": p.Upstream}},
			// the panel's own certificate on its own port: same machine, nothing to verify
			"transport":      obj{"protocol": "http", "tls": obj{"insecure_skip_verify": true}},
			"flush_interval": -1, // live events and terminals stream
			"headers": obj{"request": obj{"set": obj{
				// the name the browser used, not the upstream's: the panel picks the main name,
				// the hop links and Passkey's relying party by it
				"Host":              []string{"{http.request.host}"},
				"X-Berth-Edge":      []string{p.Secret},
				"X-Forwarded-Proto": []string{"https"},
			}}}}
		httpsRoutes = append([]any{obj{"match": []any{obj{"host": hosts}}, "handle": []any{up}, "terminal": true}}, httpsRoutes...)
		if len(p.LANHosts) > 0 && p.Cert.Cert != "" {
			loadFiles = append(loadFiles, obj{"certificate": p.Cert.Cert, "key": p.Cert.Key, "tags": []string{"berth-panel-" + p.Cert.Version}})
			skip = append(skip, p.LANHosts...) // never ask ACME for local names
		}
	}
	// WAF matches: one file the agent reads (rule hits and blocks, per site via the tag)
	logs["waf"] = obj{
		"writer":  obj{"output": "file", "filename": env.LogsRoot + "/waf/waf.log", "roll_size_mb": 50, "roll_keep": 3},
		"encoder": obj{"format": "json"},
		"include": []string{"http.handlers.waf"},
	}
	defaultExclude = append(defaultExclude, "http.handlers.waf")
	logs["default"].(obj)["exclude"] = defaultExclude
	if len(defaultExclude) == 0 {
		delete(logs["default"].(obj), "exclude")
	}

	servers := obj{}
	if len(httpsRoutes) > 0 || len(httpRoutes) == 0 {
		srv := obj{"listen": []string{":443"}, "routes": nonNil(httpsRoutes), "protocols": []string{"h1", "h2", "h3"}, "errors": obj{"routes": errorRoutes()}}
		if len(loggerNames) > 0 {
			srv["logs"] = obj{"logger_names": loggerNames}
		}
		// the visitor's address, not the proxy's: Cloudflare (orange cloud) and a tunnel's
		// cloudflared on the edge network both pass it on; nobody else can send from these ranges
		srv["trusted_proxies"] = obj{"source": "static", "ranges": append(append([]string{}, CloudflareRanges...), env.LocalProxies...)}
		srv["client_ip_headers"] = []string{"CF-Connecting-IP", "X-Forwarded-For"}
		if len(skip) > 0 {
			srv["automatic_https"] = obj{"skip": skip}
		}
		if len(loadFiles) > 0 {
			var pols []any
			for _, lf := range loadFiles {
				tag := lf.(obj)["tags"].([]string)[0]
				if strings.HasPrefix(tag, "berth-panel-") {
					continue // matched by SNI below
				}
				// only for its own names: a policy without a match takes every connection,
				// and every site would get this certificate
				pols = append(pols, obj{"match": obj{"sni": loadSNI[tag]}, "certificate_selection": obj{"any_tag": []string{tag}}})
			}
			if p := env.Panel; p != nil && len(p.LANHosts) > 0 && p.Cert.Cert != "" { // the panel's names: by SNI, first
				pols = append([]any{obj{"match": obj{"sni": p.LANHosts}, "certificate_selection": obj{"any_tag": []string{"berth-panel-" + p.Cert.Version}}}}, pols...)
			}
			// a catch-all policy keeps automatic certificates working for the other names
			srv["tls_connection_policies"] = append(pols, obj{})
		}
		servers["https"] = srv
	}
	if len(httpRoutes) > 0 {
		srv := obj{"listen": []string{":80"}, "routes": httpRoutes, "errors": obj{"routes": errorRoutes()}}
		if len(httpLoggerNames) > 0 {
			srv["logs"] = obj{"logger_names": httpLoggerNames}
		}
		// the visitor's address, not the proxy's: Cloudflare (orange cloud) and a tunnel's
		// cloudflared on the edge network both pass it on; nobody else can send from these ranges
		srv["trusted_proxies"] = obj{"source": "static", "ranges": append(append([]string{}, CloudflareRanges...), env.LocalProxies...)}
		srv["client_ip_headers"] = []string{"CF-Connecting-IP", "X-Forwarded-For"}
		servers["http"] = srv
	}

	cfg := obj{
		"admin":   obj{"listen": "unix//run/caddy/admin.sock"},
		"logging": obj{"logs": logs},
		"apps":    obj{"http": obj{"servers": servers}},
	}
	tlsApp := obj{}
	if len(policies) > 0 || env.Email != "" {
		if env.Email != "" {
			// default policy (no subjects) carries the account email for everything else
			policies = append(policies, obj{"issuers": []any{
				obj{"module": "acme", "email": env.Email},
				obj{"module": "acme", "email": env.Email, "ca": "https://acme.zerossl.com/v2/DV90"},
			}})
		}
		tlsApp["automation"] = obj{"policies": policies}
	}
	if len(loadFiles) > 0 {
		tlsApp["certificates"] = obj{"load_files": loadFiles}
	}
	if len(tlsApp) > 0 {
		cfg["apps"].(obj)["tls"] = tlsApp
	}
	return json.MarshalIndent(cfg, "", "  ")
}

func nonNil(r []any) []any {
	if r == nil {
		return []any{}
	}
	return r
}

func certTag(id int64, version string) string {
	t := "berth-cert-" + strconv.FormatInt(id, 10)
	if version != "" {
		t += "-" + version
	}
	return t
}

// wwwAlias: the extra names a www redirect answers on.
func wwwAlias(s Site) []string {
	var out []string
	for _, d := range s.Domains {
		if strings.HasPrefix(d, "*.") || net.ParseIP(d) != nil {
			continue
		}
		switch s.Options.WWW {
		case "to_www":
			if !strings.HasPrefix(d, "www.") {
				out = append(out, "www."+d)
			}
		case "to_apex":
			if strings.HasPrefix(d, "www.") {
				out = append(out, strings.TrimPrefix(d, "www."))
			}
		}
	}
	return out
}

// siteRoutes: the www redirect route (if any) and the site's own route.
func siteRoutes(s Site, env Env) []any {
	var out []any
	switch s.Options.WWW {
	case "to_www", "to_apex":
		for _, d := range s.Domains {
			if strings.HasPrefix(d, "*.") || net.ParseIP(d) != nil {
				continue
			}
			var from, to string
			if s.Options.WWW == "to_www" && !strings.HasPrefix(d, "www.") {
				from, to = "www."+d, d
			} else if s.Options.WWW == "to_apex" && strings.HasPrefix(d, "www.") {
				from, to = strings.TrimPrefix(d, "www."), d
			} else {
				continue
			}
			out = append(out, obj{
				"match":    []any{obj{"host": []string{from}}},
				"handle":   []any{redirect("https://"+to+"{http.request.uri}", 308)},
				"terminal": true,
			})
		}
	}
	// other names that send visitors on to the primary one, path and query kept
	serve := s.Domains
	if rd := s.Options.Redirect; rd != nil && len(rd.Hosts) > 0 && len(s.Domains) > 0 {
		code := rd.Code
		if code == 0 {
			code = 301
		}
		scheme := "https://"
		if s.TLS.Mode == "off" {
			scheme = "http://"
		}
		out = append(out, obj{
			"match":    []any{obj{"host": rd.Hosts}},
			"handle":   []any{redirect(scheme+s.Domains[0]+"{http.request.uri}", code)},
			"terminal": true,
		})
		serve = nil
		for _, d := range s.Domains {
			if !slices.Contains(rd.Hosts, d) {
				serve = append(serve, d)
			}
		}
	}
	out = append(out, obj{
		"match":    []any{obj{"host": serve}},
		"handle":   []any{obj{"handler": "subroute", "routes": inner(s, env)}},
		"terminal": true,
	})
	return out
}

func redirect(loc string, code int) obj {
	return obj{"handler": "static_response", "status_code": code, "headers": obj{"Location": []string{loc}}}
}

func inner(s Site, env Env) []any {
	o := s.Options
	var r []any
	// the visitor's address (through Cloudflare or a tunnel too; see trusted_proxies)
	ipMatcher := "client_ip"
	// 1. IP rules
	if len(o.DenyIPs) > 0 {
		r = append(r, obj{"match": []any{obj{ipMatcher: obj{"ranges": o.DenyIPs}}},
			"handle": []any{refuse(403, "blocked")}, "terminal": true})
	}
	if len(o.AllowIPs) > 0 {
		r = append(r, obj{"match": []any{obj{"not": []any{obj{ipMatcher: obj{"ranges": o.AllowIPs}}}}},
			"handle": []any{refuse(403, "blocked")}, "terminal": true})
	}
	// 1-. hotlink protection: embedded files only from this site and the allowed domains
	if h := o.Hotlink; h != nil && len(h.Exts) > 0 {
		var paths []string
		for _, e := range h.Exts {
			paths = append(paths, "*."+e)
		}
		var refs []string
		for _, d := range append(append([]string{}, s.Domains...), h.Allow...) {
			if strings.HasPrefix(d, "*.") {
				refs = append(refs, "*"+d[1:]+"/*")
				continue
			}
			refs = append(refs, "http://"+d+"/*", "https://"+d+"/*")
		}
		nots := []any{obj{"header": obj{"Referer": refs}}}
		if h.AllowEmpty {
			nots = append(nots, obj{"header": obj{"Referer": nil}})
		}
		r = append(r, obj{"match": []any{obj{"path": paths, "not": nots}},
			"handle": []any{refuse(403, "blocked")}, "terminal": true})
	}
	// 1a. per-client rate limit
	if rl := o.RateLimit; rl != nil {
		key := "{http.request.remote.host}"
		if o.RealIPCF {
			key = "{http.vars.client_ip}"
		}
		r = append(r, obj{"handle": []any{obj{"handler": "rate_limit", "rate_limits": obj{
			"site" + strconv.FormatInt(s.ID, 10): obj{"key": key, "window": strconv.Itoa(rl.Window) + "s", "max_events": rl.Requests}}}}})
	}
	// 1b. WAF (Coraza + OWASP CRS) before anything reaches the application
	if o.WAF != nil && !env.NoWAF {
		r = append(r, obj{"handle": []any{obj{"handler": "waf", "load_owasp_crs": true, "directives": WAFDirectives(s.ID, *o.WAF)}}})
	}
	// 2. response headers (and HSTS) apply to everything below, including errors
	resp := headerOps(o.RespHeaders)
	if o.HSTS && s.TLS.Mode != "off" {
		set, _ := resp["set"].(obj)
		if set == nil {
			set = obj{}
		}
		set["Strict-Transport-Security"] = []string{"max-age=31536000; includeSubDomains"}
		resp["set"] = set
	}
	if len(resp) > 0 {
		resp["deferred"] = true
		r = append(r, obj{"handle": []any{obj{"handler": "headers", "response": resp}}})
	}
	// 3. CORS: only for allowed origins; the allowed origin is echoed back
	if o.CORS != nil && len(o.CORS.Origins) > 0 {
		originMatch := obj{"header": obj{"Origin": o.CORS.Origins}}
		if contains(o.CORS.Origins, "*") {
			originMatch = obj{"header": obj{"Origin": []string{"*"}}}
		}
		r = append(r, obj{"match": []any{originMatch},
			"handle": []any{obj{"handler": "headers", "response": obj{"set": corsHeaders(o.CORS), "deferred": true}}}})
		pre := obj{"method": []string{"OPTIONS"}, "header": obj{"Origin": originMatch["header"].(obj)["Origin"], "Access-Control-Request-Method": []string{"*"}}}
		r = append(r, obj{"match": []any{pre},
			"handle": []any{obj{"handler": "static_response", "status_code": 204}}, "terminal": true})
	}
	// 4. basic auth
	if len(o.BasicAuth) > 0 {
		var accts []any
		for _, u := range o.BasicAuth {
			accts = append(accts, obj{"username": u.User, "password": u.Hash})
		}
		r = append(r, obj{"handle": []any{obj{"handler": "authentication",
			"providers": obj{"http_basic": obj{"accounts": accts, "hash": obj{"algorithm": "bcrypt"}, "realm": "restricted"}}}}})
	}
	// 5. compression and static caching
	if o.Compress {
		r = append(r, obj{"handle": []any{obj{"handler": "encode", "encodings": obj{"zstd": obj{}, "gzip": obj{}}, "prefer": []string{"zstd", "gzip"}}}})
	}
	if o.CacheStatic {
		r = append(r, obj{"match": []any{obj{"path": []string{"*.css", "*.js", "*.mjs", "*.woff2", "*.woff", "*.png", "*.jpg", "*.jpeg", "*.gif", "*.svg", "*.webp", "*.avif", "*.ico"}}},
			"handle": []any{obj{"handler": "headers", "response": obj{"set": obj{"Cache-Control": []string{"public, max-age=604800"}}}}}})
	}
	// 6. path rules, in order
	for _, p := range o.Paths {
		m := []any{obj{"path": []string{p.Path}}}
		switch p.Action {
		case "proxy":
			var hs []any
			if p.Strip {
				prefix := strings.TrimSuffix(strings.TrimSuffix(p.Path, "*"), "/")
				hs = append(hs, obj{"handler": "rewrite", "strip_path_prefix": prefix})
			}
			hs = append(hs, proxy([]string{dialAddr(p.Upstream)}, Target{}, o))
			r = append(r, obj{"match": m, "handle": hs, "terminal": true})
		case "rewrite":
			r = append(r, obj{"match": m, "handle": []any{obj{"handler": "rewrite", "uri": p.To}}})
		case "redirect":
			code := p.Code
			if code == 0 {
				code = 302
			}
			r = append(r, obj{"match": m, "handle": []any{redirect(p.To, code)}, "terminal": true})
		case "respond":
			code := p.Code
			if code == 0 {
				code = 200
			}
			r = append(r, obj{"match": m, "handle": []any{obj{"handler": "static_response", "status_code": code, "body": p.Body}}, "terminal": true})
		}
	}
	// 7. request headers and the target
	var final []any
	if req := headerOps(o.ReqHeaders); len(req) > 0 {
		final = append(final, obj{"handler": "headers", "request": req})
	}
	t := s.Target
	switch t.Type {
	case "container":
		final = append(final, proxy([]string{net.JoinHostPort(t.Container, strconv.Itoa(t.Port))}, t, o))
	case "address":
		var ups []string
		for _, u := range t.Upstreams {
			ups = append(ups, dialAddr(u))
		}
		final = append(final, proxy(ups, t, o))
	case "project":
		if p := env.ProjectPorts[t.Project]; p > 0 {
			final = append(final, proxy([]string{net.JoinHostPort("host.docker.internal", strconv.Itoa(p))}, t, o))
		} else {
			final = append(final, refuse(503, "project not deployed"))
		}
	case "static":
		root := env.SitesRoot + "/" + s.Primary()
		if t.SPA {
			r = append(r, obj{"match": []any{obj{"file": obj{"root": root, "try_files": []string{"{http.request.uri.path}", "{http.request.uri.path}/", "/index.html"}}}},
				"handle": []any{obj{"handler": "rewrite", "uri": "{http.matchers.file.relative}"}}})
		}
		final = append(final, obj{"handler": "file_server", "root": root})
	case "php":
		// what Caddy's php_fastcgi does: directories get a slash, anything that is not a file
		// goes to index.php (pretty URLs for WordPress, Laravel, Typecho… need no rewrite
		// rules), *.php runs in the PHP runtime, the rest is served as files
		root := env.SitesRoot + "/" + s.Primary()
		fcgiRoot, dial := "/srv/sites/"+s.Primary(), "berth-php"+t.PHP+":9000"
		if t.Native { // the site's own FPM on the host sees the host's paths
			fcgiRoot, dial = "/srv/berth/caddy/sites/"+s.Primary(), net.JoinHostPort("host.docker.internal", strconv.Itoa(25000+int(s.ID)))
		}
		if t.RootSub != "" {
			root += "/" + t.RootSub
			fcgiRoot += "/" + t.RootSub
		}
		r = append(r, obj{"handle": []any{obj{"handler": "vars", "root": root}}})
		r = append(r, obj{"match": []any{obj{"file": obj{"try_files": []string{"{http.request.uri.path}/index.php"}}, "not": []any{obj{"path": []string{"*/"}}}}},
			"handle": []any{obj{"handler": "static_response", "status_code": 308, "headers": obj{"Location": []string{"{http.request.orig_uri.path}/"}}}}, "terminal": true})
		r = append(r, obj{"match": []any{obj{"file": obj{"try_files": []string{"{http.request.uri.path}", "{http.request.uri.path}/index.php", "index.php"}, "split_path": []string{".php"}}}},
			"handle": []any{obj{"handler": "rewrite", "uri": "{http.matchers.file.relative}"}}})
		r = append(r, obj{"match": []any{obj{"path": []string{"*.php"}}},
			"handle": []any{obj{"handler": "reverse_proxy", "transport": obj{"protocol": "fastcgi", "root": fcgiRoot, "split_path": []string{".php"}},
				"upstreams": []any{obj{"dial": dial}}}}, "terminal": true})
		final = append(final, obj{"handler": "file_server", "root": root, "hide": []string{".user.ini", ".htaccess", "wp-config.php"}})
	case "redirect":
		final = append(final, redirect(strings.TrimSuffix(t.To, "/")+"{http.request.uri}", t.Code))
	}
	r = append(r, obj{"handle": final, "terminal": true})
	return r
}

// dialAddr: services on the host itself are reached from the Caddy container through
// the Docker host gateway.
func dialAddr(u string) string {
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

func proxy(dials []string, t Target, o Options) obj {
	var ups []any
	for _, d := range dials {
		ups = append(ups, obj{"dial": d})
	}
	p := obj{"handler": "reverse_proxy", "upstreams": ups}
	tr := obj{"protocol": "http"}
	switch t.Scheme {
	case "https":
		tls := obj{}
		if t.InsecureTLS {
			tls["insecure_skip_verify"] = true
		}
		tr["tls"] = tls
	case "h2c":
		tr["versions"] = []string{"h2c", "2"}
	}
	if o.Timeouts.Dial > 0 {
		tr["dial_timeout"] = strconv.Itoa(o.Timeouts.Dial) + "s"
	}
	if o.Timeouts.Read > 0 {
		tr["response_header_timeout"] = strconv.Itoa(o.Timeouts.Read) + "s"
	}
	p["transport"] = tr
	if len(dials) > 1 || t.LB != "" {
		policy := t.LB
		if policy == "" {
			policy = "round_robin"
		}
		p["load_balancing"] = obj{"selection_policy": obj{"policy": policy}}
	}
	if t.HealthPath != "" {
		p["health_checks"] = obj{"active": obj{"uri": t.HealthPath, "interval": "10s", "timeout": "5s"}}
	}
	if t.Scheme == "https" {
		// keep the original Host for HTTPS upstreams that check it
		p["headers"] = obj{"request": obj{"set": obj{"Host": []string{"{http.reverse_proxy.upstream.hostport}"}}}}
	}
	return p
}

func headerOps(ops []HeaderOp) obj {
	out := obj{}
	set, add := obj{}, obj{}
	var del []string
	for _, h := range ops {
		switch h.Op {
		case "set":
			set[h.Name] = []string{h.Value}
		case "add":
			if v, ok := add[h.Name].([]string); ok {
				add[h.Name] = append(v, h.Value)
			} else {
				add[h.Name] = []string{h.Value}
			}
		case "del":
			del = append(del, h.Name)
		}
	}
	if len(set) > 0 {
		out["set"] = set
	}
	if len(add) > 0 {
		out["add"] = add
	}
	if len(del) > 0 {
		out["delete"] = del
	}
	return out
}

func corsHeaders(c *CORS) obj {
	h := obj{"Access-Control-Allow-Origin": []string{"{http.request.header.Origin}"}, "Vary": []string{"Origin"}}
	if contains(c.Origins, "*") && !c.Credentials {
		h["Access-Control-Allow-Origin"] = []string{"*"}
	}
	methods := c.Methods
	if len(methods) == 0 {
		methods = []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"}
	}
	h["Access-Control-Allow-Methods"] = []string{strings.Join(methods, ", ")}
	if len(c.Headers) > 0 {
		h["Access-Control-Allow-Headers"] = []string{strings.Join(c.Headers, ", ")}
	} else {
		h["Access-Control-Allow-Headers"] = []string{"*"}
	}
	if c.Credentials {
		h["Access-Control-Allow-Credentials"] = []string{"true"}
	}
	if c.MaxAge > 0 {
		h["Access-Control-Max-Age"] = []string{strconv.Itoa(c.MaxAge)}
	}
	return h
}

func contains(l []string, v string) bool {
	for _, x := range l {
		if x == v {
			return true
		}
	}
	return false
}

// WAFDirectives builds the Coraza configuration for one site (CRS in anomaly-scoring mode;
// the agent tells sites apart by the request host in the log).
func WAFDirectives(siteID int64, w WAF) string {
	engine := "DetectionOnly"
	if w.Mode == "block" {
		engine = "On"
	}
	var b strings.Builder
	b.WriteString("Include @coraza.conf-recommended\n")
	b.WriteString("Include @crs-setup.conf.example\n")
	fmt.Fprintf(&b, "SecAction \"id:900000,phase:1,pass,t:none,nolog,setvar:tx.blocking_paranoia_level=%d\"\n", w.Paranoia)
	b.WriteString("Include @owasp_crs/*.conf\n")
	fmt.Fprintf(&b, "SecRuleEngine %s\n", engine)
	b.WriteString("SecRequestBodyAccess On\nSecResponseBodyAccess Off\nSecAuditEngine Off\n")
	// marks which site a flagged request belonged to (the rule hits do not carry the host)
	fmt.Fprintf(&b, "SecRule TX:BLOCKING_INBOUND_ANOMALY_SCORE \"@gt 0\" \"id:1999,phase:5,pass,log,msg:'berth-site-%d'\"\n", siteID)
	for i, p := range w.ExcludePaths {
		fmt.Fprintf(&b, "SecRule REQUEST_FILENAME \"@beginsWith %s\" \"id:%d,phase:1,pass,nolog,ctl:ruleEngine=Off\"\n", p, 1000+i)
	}
	if len(w.ExcludeRules) > 0 {
		ids := make([]string, len(w.ExcludeRules))
		for i, id := range w.ExcludeRules {
			ids[i] = strconv.Itoa(id)
		}
		fmt.Fprintf(&b, "SecRuleRemoveById %s\n", strings.Join(ids, " "))
	}
	return b.String()
}
