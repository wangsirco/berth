package caddycfg

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func render(t *testing.T, env Env, sites ...Site) map[string]any {
	t.Helper()
	b, err := Render(sites, env)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

// at walks a decoded JSON value: keys for objects, numbers for arrays.
func at(v any, path ...any) any {
	for _, p := range path {
		switch k := p.(type) {
		case string:
			m, ok := v.(map[string]any)
			if !ok {
				return nil
			}
			v = m[k]
		case int:
			a, ok := v.([]any)
			if !ok || k >= len(a) {
				return nil
			}
			v = a[k]
		}
	}
	return v
}

func js(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func site(id int64, domain string, t Target) Site {
	return Site{ID: id, Domains: []string{domain}, Enabled: true, Target: t}
}

func innerRoutes(m map[string]any, server string, i int) []any {
	r, _ := at(m, "apps", "http", "servers", server, "routes", i, "handle", 0, "routes").([]any)
	return r
}

func TestRenderTargets(t *testing.T) {
	cases := []struct {
		name   string
		target Target
		want   []string // substrings of the site's subroute JSON
	}{
		{"container", Target{Type: "container", Container: "n8n", Port: 5678},
			[]string{`"dial":"n8n:5678"`, `"handler":"reverse_proxy"`}},
		{"address on host", Target{Type: "address", Upstreams: []string{"127.0.0.1:3000"}},
			[]string{`"dial":"host.docker.internal:3000"`}},
		{"address lb", Target{Type: "address", Upstreams: []string{"10.0.0.2:80", "10.0.0.3:80"}, LB: "least_conn", HealthPath: "/healthz"},
			[]string{`"policy":"least_conn"`, `"uri":"/healthz"`, `"dial":"10.0.0.3:80"`}},
		{"https upstream", Target{Type: "address", Upstreams: []string{"10.0.0.2:8443"}, Scheme: "https", InsecureTLS: true},
			[]string{`"insecure_skip_verify":true`}},
		{"grpc h2c", Target{Type: "container", Container: "api", Port: 50051, Scheme: "h2c"},
			[]string{`"versions":["h2c","2"]`}},
		{"static", Target{Type: "static"},
			[]string{`"handler":"file_server"`, `"root":"/srv/sites/a.example.com"`}},
		{"static spa", Target{Type: "static", SPA: true},
			[]string{`"/index.html"`, `{http.matchers.file.relative}`}},
		{"redirect", Target{Type: "redirect", To: "https://b.example.com/", Code: 301},
			[]string{`"status_code":301`, `https://b.example.com{http.request.uri}`}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := render(t, Env{}, site(1, "a.example.com", c.target))
			got := js(innerRoutes(m, "https", 0))
			for _, w := range c.want {
				if !strings.Contains(got, w) {
					t.Errorf("missing %s in %s", w, got)
				}
			}
			if h := js(at(m, "apps", "http", "servers", "https", "routes", 0, "match")); h != `[{"host":["a.example.com"]}]` {
				t.Errorf("match %s", h)
			}
		})
	}
}

func TestRenderOptions(t *testing.T) {
	base := Target{Type: "container", Container: "app", Port: 80}
	cases := []struct {
		name string
		opt  Options
		want []string
		not  []string
	}{
		{"hsts", Options{HSTS: true}, []string{`"Strict-Transport-Security":["max-age=31536000; includeSubDomains"]`, `"deferred":true`}, nil},
		{"compress", Options{Compress: true}, []string{`"handler":"encode"`, `"zstd":{}`}, nil},
		{"cache static", Options{CacheStatic: true}, []string{`"*.woff2"`, `public, max-age=604800`}, nil},
		{"headers", Options{ReqHeaders: []HeaderOp{{Op: "set", Name: "X-Env", Value: "prod"}}, RespHeaders: []HeaderOp{{Op: "del", Name: "Server"}, {Op: "add", Name: "X-A", Value: "1"}}},
			[]string{`"request":{"set":{"X-Env":["prod"]}}`, `"delete":["Server"]`, `"add":{"X-A":["1"]}`}, nil},
		{"cors", Options{CORS: &CORS{Origins: []string{"https://x.com", "https://y.com"}, Credentials: true}},
			[]string{`"Origin":["https://x.com","https://y.com"]`, `{http.request.header.Origin}`, `"Access-Control-Allow-Credentials":["true"]`, `"status_code":204`}, nil},
		{"basic auth", Options{BasicAuth: []BasicUser{{User: "ops", Hash: "$2a$14$abcdefghijklmnopqrstuuJ4n3tLcOTfQe0gD1b7sZ0hNw7yJc1q"}}},
			[]string{`"handler":"authentication"`, `"username":"ops"`, `"algorithm":"bcrypt"`}, nil},
		{"ip deny", Options{DenyIPs: []string{"1.2.3.4"}}, []string{`"client_ip":{"ranges":["1.2.3.4"]}`, `"status_code":403`}, nil},
		{"ip allow behind cf", Options{AllowIPs: []string{"10.0.0.0/8"}, RealIPCF: true}, []string{`"not":[{"client_ip":{"ranges":["10.0.0.0/8"]}}]`}, nil},
		{"timeouts", Options{Timeouts: Timeouts{Dial: 5, Read: 120}}, []string{`"dial_timeout":"5s"`, `"response_header_timeout":"120s"`}, nil},
		{"path proxy strip", Options{Paths: []PathRule{{Path: "/api/*", Action: "proxy", Upstream: "api:8080", Strip: true}}},
			[]string{`"path":["/api/*"]`, `"strip_path_prefix":"/api"`, `"dial":"api:8080"`}, nil},
		{"path rewrite and respond", Options{Paths: []PathRule{{Path: "/old", Action: "rewrite", To: "/new"}, {Path: "/robots.txt", Action: "respond", Body: "User-agent: *"}}},
			[]string{`"uri":"/new"`, `"body":"User-agent: *"`}, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := site(1, "a.example.com", base)
			s.Options = c.opt
			m := render(t, Env{}, s)
			got := js(innerRoutes(m, "https", 0))
			for _, w := range c.want {
				if !strings.Contains(got, w) {
					t.Errorf("missing %s in %s", w, got)
				}
			}
		})
	}
}

func TestRenderWWWAndHTTP(t *testing.T) {
	s := site(1, "example.com", Target{Type: "static"})
	s.Options.WWW = "to_www"
	m := render(t, Env{}, s)
	// to_www means: example.com is canonical? No: the site lists its canonical domain;
	// to_www adds www.example.com redirecting to it.
	if js(at(m, "apps", "http", "servers", "https", "routes", 0, "match")) != `[{"host":["www.example.com"]}]` {
		t.Fatalf("www route: %s", js(at(m, "apps", "http", "servers", "https", "routes")))
	}
	if !strings.Contains(js(at(m, "apps", "http", "servers", "https", "routes", 0)), `https://example.com{http.request.uri}`) {
		t.Fatal("www redirect target")
	}

	plain := site(2, "intranet.lan", Target{Type: "static"})
	plain.TLS.Mode = "off"
	m = render(t, Env{}, plain)
	// Only plain-HTTP sites: no :443 server at all.
	if at(m, "apps", "http", "servers", "http") == nil || at(m, "apps", "http", "servers", "https") != nil {
		t.Fatalf("tls off: %s", js(m))
	}

	noRedirect := site(3, "both.example.com", Target{Type: "static"})
	noRedirect.Options.NoHTTPSRedirect = true
	m = render(t, Env{}, noRedirect)
	if len(at(m, "apps", "http", "servers", "http", "routes").([]any)) != 1 || len(at(m, "apps", "http", "servers", "https", "routes").([]any)) != 1 {
		t.Fatalf("no redirect: %s", js(m))
	}
}

func TestRenderTLS(t *testing.T) {
	a := site(1, "a.lan", Target{Type: "static"})
	a.TLS.Mode = "internal"
	b := site(2, "*.example.com", Target{Type: "static"})
	b.TLS = TLS{Mode: "dns", DNS: "cf1"}
	c := site(3, "c.example.org", Target{Type: "static"})
	c.TLS = TLS{Mode: "custom", CertID: 7}
	env := Env{Email: "ops@example.com",
		DNS:   map[string]map[string]any{"cf1": {"name": "cloudflare", "api_token": "tok"}},
		Certs: map[int64]CertFiles{7: {Cert: "/data/berth-certs/7/v1/cert.pem", Key: "/data/berth-certs/7/v1/key.pem", Version: "v1"}}}
	m := render(t, env, a, b, c)
	pol := js(at(m, "apps", "tls", "automation", "policies"))
	for _, w := range []string{`{"issuers":[{"module":"internal"}],"subjects":["a.lan"]}`, `"provider":{"api_token":"tok","name":"cloudflare"}`, `"subjects":["*.example.com"]`, `"email":"ops@example.com"`, `acme.zerossl.com`} {
		if !strings.Contains(pol, w) {
			t.Errorf("missing %s in %s", w, pol)
		}
	}
	if !strings.Contains(js(at(m, "apps", "tls", "certificates")), `"tags":["berth-cert-7-v1"]`) {
		t.Error("custom cert not loaded")
	}
	if !strings.Contains(js(at(m, "apps", "http", "servers", "https", "tls_connection_policies")), `"any_tag":["berth-cert-7-v1"]`) {
		t.Error("custom cert not selected")
	}

	// missing references are errors, not silent fallbacks
	if _, err := Render([]Site{b}, Env{}); err == nil {
		t.Error("dns site rendered without its provider")
	}
	w := site(4, "*.x.com", Target{Type: "static"})
	if _, err := Render([]Site{w}, Env{}); err == nil || err.(*Problem).Code != "site.wildcard_needs_dns" {
		t.Errorf("wildcard with auto TLS: %v", err)
	}
}

func TestRenderLogsAndDisabled(t *testing.T) {
	a := site(1, "a.example.com", Target{Type: "static"})
	b := site(2, "b.example.com", Target{Type: "static"})
	b.Enabled = false
	m := render(t, Env{}, a, b)
	// a stopped site answers with the paused page (and keeps its certificate) instead of vanishing
	routes := at(m, "apps", "http", "servers", "https", "routes").([]any)
	if n := len(routes); n != 2 || !strings.Contains(js(routes[1]), `"error":"site stopped"`) || strings.Contains(js(routes[1]), "file_server") {
		t.Fatalf("stopped site: %s", js(routes))
	}
	if !strings.Contains(js(at(m, "apps", "http", "servers", "https", "errors")), "网站暂停访问") {
		t.Fatal("no error pages")
	}
	if at(m, "logging", "logs", "site1", "writer", "filename") != "/var/log/caddy/a.example.com.log" {
		t.Fatal(js(at(m, "logging")))
	}
	if js(at(m, "apps", "http", "servers", "https", "logs", "logger_names", "a.example.com")) != `["site1"]` {
		t.Fatal(js(at(m, "apps", "http", "servers", "https", "logs")))
	}
	// deterministic output
	b1, _ := Render([]Site{a, b}, Env{})
	b2, _ := Render([]Site{b, a}, Env{})
	if string(b1) != string(b2) {
		t.Fatal("render is not deterministic")
	}
}

func TestNormalize(t *testing.T) {
	bad := []Site{
		{Domains: nil, Target: Target{Type: "static"}},
		{Domains: []string{"bad domain"}, Target: Target{Type: "static"}},
		{Domains: []string{"a.com"}, Target: Target{Type: "container", Container: "x"}},
		{Domains: []string{"a.com"}, Target: Target{Type: "address", Upstreams: []string{"nope"}}},
		{Domains: []string{"a.com"}, Target: Target{Type: "redirect", To: "ftp://x"}},
		{Domains: []string{"a.com"}, Target: Target{Type: "static"}, Options: Options{ReqHeaders: []HeaderOp{{Op: "set", Name: "X", Value: "a\r\nb"}}}},
		{Domains: []string{"a.com"}, Target: Target{Type: "static"}, Options: Options{DenyIPs: []string{"nope"}}},
		{Domains: []string{"a.com"}, Target: Target{Type: "static"}, Options: Options{BasicAuth: []BasicUser{{User: "a", Hash: "plain"}}}},
		{Domains: []string{"a.com"}, Target: Target{Type: "static"}, TLS: TLS{Mode: "weird"}},
	}
	for i, s := range bad {
		if err := Normalize(&s); err == nil {
			t.Errorf("case %d accepted", i)
		}
	}
	s := Site{Domains: []string{" A.Example.COM. ", "a.example.com"}, Target: Target{Type: "static"}}
	if err := Normalize(&s); err != nil || len(s.Domains) != 1 || s.Domains[0] != "a.example.com" || s.TLS.Mode != "auto" || s.Options.LogDays != 14 {
		t.Fatalf("%v %+v", err, s)
	}
}

// TestWriteSample writes a config using every feature, for `caddy validate` on a real
// Caddy (see scripts/caddy-validate.sh). Skipped unless BERTH_CADDY_SAMPLE is set.
func TestWriteSample(t *testing.T) {
	out := os.Getenv("BERTH_CADDY_SAMPLE")
	if out == "" {
		t.Skip("BERTH_CADDY_SAMPLE not set")
	}
	full := site(1, "app.example.com", Target{Type: "address", Upstreams: []string{"127.0.0.1:3000", "10.0.0.5:3000"}, LB: "ip_hash", HealthPath: "/health"})
	full.Options = Options{WWW: "to_www", HSTS: true, Compress: true, CacheStatic: true, Timeouts: Timeouts{Dial: 5, Read: 60},
		ReqHeaders: []HeaderOp{{Op: "set", Name: "X-Env", Value: "prod"}}, RespHeaders: []HeaderOp{{Op: "del", Name: "Server"}},
		CORS:      &CORS{Origins: []string{"https://x.com"}, Credentials: true, MaxAge: 600},
		Paths:     []PathRule{{Path: "/api/*", Action: "proxy", Upstream: "api:8080", Strip: true}, {Path: "/old", Action: "redirect", To: "/new", Code: 301}, {Path: "/robots.txt", Action: "respond", Body: "ok"}, {Path: "/r", Action: "rewrite", To: "/x"}},
		BasicAuth: []BasicUser{{User: "ops", Hash: "$2a$14$Zkx19XLiW6VYouLHR5NmfOFU0z2GTNmpkT/5qqR7hx4IjWJPDhjvG"}},
		AllowIPs:  []string{"10.0.0.0/8"}, DenyIPs: []string{"10.9.9.9"}, RealIPCF: true}
	spa := site(2, "spa.lan", Target{Type: "static", SPA: true})
	spa.TLS.Mode = "internal"
	grpc := site(3, "grpc.example.com", Target{Type: "container", Container: "api", Port: 50051, Scheme: "h2c"})
	grpc.TLS = TLS{Mode: "dns", DNS: "cf"}
	plain := site(4, "plain.lan", Target{Type: "redirect", To: "https://example.com", Code: 308})
	plain.TLS.Mode = "off"
	both := site(5, "both.example.com", Target{Type: "container", Container: "web", Port: 443, Scheme: "https", InsecureTLS: true})
	both.Options.NoHTTPSRedirect = true
	b, err := Render([]Site{full, spa, grpc, plain, both}, Env{Email: "ops@example.com",
		DNS: map[string]map[string]any{"cf": {"name": "cloudflare", "api_token": "abcdefghijklmnopqrstuvwxyz0123456789ABCD"}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(out, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestRenderPHP(t *testing.T) {
	s := Site{ID: 9, Domains: []string{"blog.example.com"}, Enabled: true, Target: Target{Type: "php", PHP: "84"}, TLS: TLS{Mode: "internal"}}
	if err := Normalize(&s); err != nil {
		t.Fatal(err)
	}
	b, err := Render([]Site{s}, Env{})
	if err != nil {
		t.Fatal(err)
	}
	var cb bytes.Buffer
	json.Compact(&cb, b)
	j := cb.String()
	for _, want := range []string{`"protocol":"fastcgi"`, `"dial":"berth-php84:9000"`, `"split_path":[".php"]`, `"try_files":["{http.request.uri.path}","{http.request.uri.path}/index.php","index.php"]`, `"root":"/srv/sites/blog.example.com"`} {
		if !strings.Contains(j, want) {
			t.Errorf("missing %s", want)
		}
	}
	bad := Site{Domains: []string{"x.example.com"}, Target: Target{Type: "php", PHP: "8.4"}, TLS: TLS{Mode: "internal"}}
	if Normalize(&bad) == nil {
		t.Error("a dotted version should be refused")
	}
}

func TestRenderHotlink(t *testing.T) {
	s := Site{ID: 3, Domains: []string{"img.example.com"}, Enabled: true, Target: Target{Type: "static"}, TLS: TLS{Mode: "internal"},
		Options: Options{Hotlink: &Hotlink{Exts: []string{"jpg", "png"}, Allow: []string{"*.friend.org"}, AllowEmpty: true}}}
	if err := Normalize(&s); err != nil {
		t.Fatal(err)
	}
	b, _ := Render([]Site{s}, Env{})
	var cb bytes.Buffer
	json.Compact(&cb, b)
	j := cb.String()
	for _, want := range []string{`"path":["*.jpg","*.png"]`, `"https://img.example.com/*"`, `"*.friend.org/*"`, `{"header":{"Referer":null}}`} {
		if !strings.Contains(j, want) {
			t.Errorf("missing %s", want)
		}
	}
	bad := s
	bad.Options.Hotlink = &Hotlink{Exts: []string{"j/pg"}}
	if Normalize(&bad) == nil {
		t.Error("bad extension accepted")
	}
}

func TestRenderProjectTarget(t *testing.T) {
	m := render(t, Env{ProjectPorts: map[string]int{"web": 21234}}, site(1, "a.example", Target{Type: "project", Project: "web"}))
	if s := js(innerRoutes(m, "https", 0)); !strings.Contains(s, `"dial":"host.docker.internal:21234"`) {
		t.Fatal(s)
	}
	m = render(t, Env{}, site(2, "b.example", Target{Type: "project", Project: "web"}))
	if s := js(m); !strings.Contains(s, `"status_code":503`) {
		t.Fatal("undeployed project should answer 503")
	}
	if _, err := Render([]Site{site(3, "c.example", Target{Type: "project", Project: "Bad Name"})}, Env{}); err == nil {
		t.Fatal("bad project name accepted")
	}
}

func TestRenderNativePHP(t *testing.T) {
	m := render(t, Env{SitesRoot: "/srv/sites"}, site(12, "app.example", Target{Type: "php", PHP: "84", Native: true, RootSub: "public"}))
	s := js(innerRoutes(m, "https", 0))
	for _, want := range []string{`"dial":"host.docker.internal:25012"`, `"root":"/srv/berth/caddy/sites/app.example/public"`, `"root":"/srv/sites/app.example/public"`} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %s in %s", want, s)
		}
	}
	m = render(t, Env{}, site(13, "old.example", Target{Type: "php", PHP: "74"}))
	if s := js(innerRoutes(m, "https", 0)); !strings.Contains(s, `"dial":"berth-php74:9000"`) {
		t.Error("container PHP unchanged")
	}
	if _, err := Render([]Site{site(14, "x.example", Target{Type: "php", PHP: "74", Native: true})}, Env{}); err == nil {
		t.Error("7.4 cannot run natively")
	}
	if _, err := Render([]Site{site(15, "y.example", Target{Type: "php", PHP: "84", RootSub: "../etc"})}, Env{}); err == nil {
		t.Error("root_sub escape accepted")
	}
}

// Without the WAF extension the edge has no waf handler, so a site's WAF setting is skipped.
func TestRenderNoWAF(t *testing.T) {
	s := site(7, "waf.example.com", Target{Type: "container", Container: "app", Port: 80})
	s.Options.WAF = &WAF{Mode: "block", Paranoia: 1}
	on, err := Render([]Site{s}, Env{})
	if err != nil || !strings.Contains(string(on), `load_owasp_crs`) {
		t.Fatalf("with the WAF: %v", err)
	}
	off, err := Render([]Site{s}, Env{NoWAF: true})
	if err != nil || strings.Contains(string(off), `load_owasp_crs`) {
		t.Fatalf("without the WAF: %v", err)
	}
}

func TestRedirectHostsAndRealIP(t *testing.T) {
	s := Site{ID: 1, Domains: []string{"hwh.co.nz", "www.hwh.co.nz", "hwh.ihan.nz"}, Enabled: true,
		Target: Target{Type: "static"}, TLS: TLS{Mode: "internal"},
		Options: Options{Redirect: &RedirectHosts{Hosts: []string{"www.hwh.co.nz", "hwh.ihan.nz"}, Code: 302}}}
	if err := Normalize(&s); err != nil {
		t.Fatal(err)
	}
	b, _ := Render([]Site{s}, Env{LocalProxies: []string{"172.19.0.0/16"}})
	var buf bytes.Buffer
	json.Compact(&buf, b)
	j := strings.ReplaceAll(buf.String(), `\u0026`, "&")
	for _, want := range []string{
		`"Location":["https://hwh.co.nz{http.request.uri}"]`, `"status_code":302`,
		`"host":["www.hwh.co.nz","hwh.ihan.nz"]`, `"host":["hwh.co.nz"]`,
		`"172.19.0.0/16"`, `"client_ip_headers":["CF-Connecting-IP","X-Forwarded-For"]`,
	} {
		if !strings.Contains(j, want) {
			t.Errorf("missing %s", want)
		}
	}
	bad := s
	bad.Options.Redirect = &RedirectHosts{Hosts: []string{"hwh.co.nz"}}
	if Normalize(&bad) == nil {
		t.Error("the primary domain cannot redirect to itself")
	}
}

// The panel's own names open it on 443: proxied to the panel with the edge secret, the LAN
// names on the panel's certificate (picked by SNI, never sent to ACME), other sites untouched.
func TestPanelEntry(t *testing.T) {
	site := Site{ID: 1, Domains: []string{"blog.example.com"}, Enabled: true, Target: Target{Type: "static"}, TLS: TLS{Mode: "auto"}}
	b, err := Render([]Site{site}, Env{Panel: &PanelEntry{LANHosts: []string{"mini.lan"}, PublicHosts: []string{"panel.example.com"},
		Upstream: "host.docker.internal:7090", Secret: "s3cret", Cert: CertFiles{Cert: "/data/c.pem", Key: "/data/k.pem", Version: "abc123abc123"}}})
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, want := range []string{`"mini.lan"`, `"panel.example.com"`, `host.docker.internal:7090`, `"X-Berth-Edge"`, `"s3cret"`, `"berth-panel-abc123abc123"`, `"insecure_skip_verify": true`, `"sni"`} {
		if !strings.Contains(s, want) {
			t.Fatalf("missing %s in\n%s", want, s)
		}
	}
	var cfg map[string]any
	json.Unmarshal(b, &cfg)
	srv := cfg["apps"].(map[string]any)["http"].(map[string]any)["servers"].(map[string]any)["https"].(map[string]any)
	skip := srv["automatic_https"].(map[string]any)["skip"].([]any)
	if len(skip) != 1 || skip[0] != "mini.lan" {
		t.Fatalf("ACME skip: %v", skip)
	}
	pols := srv["tls_connection_policies"].([]any)
	if len(pols) != 2 || pols[0].(map[string]any)["match"] == nil || len(pols[1].(map[string]any)) != 0 {
		t.Fatalf("policies: %v", pols)
	}
	routes := srv["routes"].([]any)
	if h := routes[0].(map[string]any)["match"].([]any)[0].(map[string]any)["host"].([]any); h[0] != "mini.lan" {
		t.Fatalf("panel route not first: %v", routes[0])
	}
}

// An uploaded certificate is picked only for its own site's names; the other sites keep
// their automatic certificates (before, every site got the uploaded one).
func TestCustomCertOnlyForItsSite(t *testing.T) {
	a := Site{ID: 1, Domains: []string{"shop.lan"}, Enabled: true, Target: Target{Type: "static"}, TLS: TLS{Mode: "custom", CertID: 3}}
	b := Site{ID: 2, Domains: []string{"blog.example.com"}, Enabled: true, Target: Target{Type: "static"}, TLS: TLS{Mode: "auto"}}
	out, err := Render([]Site{a, b}, Env{Certs: map[int64]CertFiles{3: {Cert: "/c", Key: "/k", Version: "v1v1v1v1"}}})
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	json.Unmarshal(out, &cfg)
	pols := cfg["apps"].(map[string]any)["http"].(map[string]any)["servers"].(map[string]any)["https"].(map[string]any)["tls_connection_policies"].([]any)
	first := pols[0].(map[string]any)
	sni, _ := first["match"].(map[string]any)["sni"].([]any)
	if len(pols) != 2 || len(sni) == 0 || sni[0] != "shop.lan" {
		t.Fatalf("policies: %v", pols)
	}
	for _, n := range sni {
		if n == "blog.example.com" {
			t.Fatal("uploaded certificate offered to another site")
		}
	}
}
