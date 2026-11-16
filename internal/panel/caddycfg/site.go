// Package caddycfg renders Berth sites into one Caddy JSON config. It is pure: the
// same sites always give the same bytes, so a preview diff is exactly what gets loaded.
package caddycfg

import (
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// PHP runtimes are named by version without the dot: "84" is PHP 8.4.
var phpVerRe = regexp.MustCompile(`^[0-9]{2}$`)

type Site struct {
	ID      int64    `json:"id"`
	Domains []string `json:"domains"`
	Enabled bool     `json:"enabled"`
	Target  Target   `json:"target"`
	TLS     TLS      `json:"tls"`
	Options Options  `json:"options"`
}

// Target is where requests go.
var projectRe = regexp.MustCompile(`^[a-z][a-z0-9-]{0,25}$`)

type PHPWorker struct {
	Name    string `json:"name"`
	Command string `json:"command"`
}

var (
	rootSubR    = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_./-]{0,60}$`)
	workerNameR = regexp.MustCompile(`^[a-z][a-z0-9-]{0,20}$`)
	nativePHPR  = regexp.MustCompile(`^8[2-5]$`)
)

type Target struct {
	Type string `json:"type"` // container | address | static | php | redirect | project

	// project: a Berth native project; Caddy reaches whichever port it currently runs on
	Project string `json:"project,omitempty"`

	// container
	Container string `json:"container,omitempty"`
	Port      int    `json:"port,omitempty"`
	// address: one or more host:port (several = load balancing)
	Upstreams []string `json:"upstreams,omitempty"`
	// container and address
	Scheme      string `json:"scheme,omitempty"` // http (default) | https | h2c
	InsecureTLS bool   `json:"insecure_tls,omitempty"`
	LB          string `json:"lb,omitempty"`          // round_robin | least_conn | ip_hash | first
	HealthPath  string `json:"health_path,omitempty"` // active health check URI
	// static: files under the site's directory
	SPA bool `json:"spa,omitempty"`
	// php: files under the site's directory, *.php through a Berth PHP runtime ("84" = 8.4)
	PHP string `json:"php,omitempty"`
	// php, run natively (R2): the site's own sandboxed PHP-FPM instead of the shared container
	Native     bool              `json:"native,omitempty"`
	RootSub    string            `json:"root_sub,omitempty"` // serve from this subfolder (Laravel: public)
	PHPIni     map[string]string `json:"php_ini,omitempty"`
	PHPMemMB   int               `json:"php_mem_mb,omitempty"`
	PHPWorkers []PHPWorker       `json:"php_workers,omitempty"`
	Schedule   string            `json:"schedule,omitempty"` // e.g. php artisan schedule:run, every minute
	// redirect
	To   string `json:"to,omitempty"`
	Code int    `json:"code,omitempty"`
}

type TLS struct {
	Mode   string `json:"mode"`              // auto | internal | dns | custom | off
	DNS    string `json:"dns,omitempty"`     // DNS provider account id (mode dns)
	CertID int64  `json:"cert_id,omitempty"` // uploaded certificate (mode custom)
}

type HeaderOp struct {
	Op    string `json:"op"` // set | add | del
	Name  string `json:"name"`
	Value string `json:"value,omitempty"`
}

type CORS struct {
	Origins     []string `json:"origins"`
	Methods     []string `json:"methods,omitempty"`
	Headers     []string `json:"headers,omitempty"`
	Credentials bool     `json:"credentials,omitempty"`
	MaxAge      int      `json:"max_age,omitempty"`
}

type PathRule struct {
	Path   string `json:"path"`   // /api/*
	Action string `json:"action"` // proxy | rewrite | redirect | respond
	// proxy
	Upstream string `json:"upstream,omitempty"` // host:port or container:port
	Strip    bool   `json:"strip,omitempty"`    // strip the path prefix before proxying
	// rewrite / redirect
	To   string `json:"to,omitempty"`
	Code int    `json:"code,omitempty"`
	// respond
	Body string `json:"body,omitempty"`
}

type BasicUser struct {
	User string `json:"user"`
	Hash string `json:"hash"` // bcrypt
}

type Timeouts struct {
	Dial  int `json:"dial,omitempty"`  // seconds
	Read  int `json:"read,omitempty"`  // response header timeout
	Write int `json:"write,omitempty"` // unused by Caddy per route; kept for the UI
}

type Options struct {
	Note            string      `json:"note,omitempty"` // 备注: shown in lists in place of the domains
	NoHTTPSRedirect bool        `json:"no_https_redirect,omitempty"`
	WWW             string      `json:"www,omitempty"` // "" | to_www | to_apex
	HSTS            bool        `json:"hsts,omitempty"`
	Compress        bool        `json:"compress,omitempty"`
	CacheStatic     bool        `json:"cache_static,omitempty"`
	Timeouts        Timeouts    `json:"timeouts,omitempty"`
	ReqHeaders      []HeaderOp  `json:"req_headers,omitempty"`
	RespHeaders     []HeaderOp  `json:"resp_headers,omitempty"`
	CORS            *CORS       `json:"cors,omitempty"`
	Paths           []PathRule  `json:"paths,omitempty"`
	BasicAuth       []BasicUser `json:"basic_auth,omitempty"`
	AllowIPs        []string    `json:"allow_ips,omitempty"`
	DenyIPs         []string    `json:"deny_ips,omitempty"`
	RealIPCF        bool        `json:"real_ip_cloudflare,omitempty"`
	LogDays         int         `json:"log_days,omitempty"`
	WAF             *WAF        `json:"waf,omitempty"`
	RateLimit       *RateLimit  `json:"rate_limit,omitempty"`
	Hotlink         *Hotlink    `json:"hotlink,omitempty"`
}

// Hotlink (防盗链): files of these types may only be embedded by the site itself and the
// listed domains; other pages get 403.
type Hotlink struct {
	Exts       []string `json:"exts"`        // jpg png gif webp mp4 …
	Allow      []string `json:"allow"`       // other domains allowed to embed (*.example.com)
	AllowEmpty bool     `json:"allow_empty"` // requests with no Referer (direct visits, some apps)
}

// RateLimit caps requests per client address (Caddy rate_limit, sliding window).
type RateLimit struct {
	Requests int `json:"requests"` // at most this many…
	Window   int `json:"window"`   // …per this many seconds; more get 429
}

// WAF is Coraza with the OWASP Core Rule Set.
type WAF struct {
	Mode         string   `json:"mode"`                    // detect (log only) | block
	Paranoia     int      `json:"paranoia,omitempty"`      // CRS paranoia level 1-4 (default 1)
	ExcludeRules []int    `json:"exclude_rules,omitempty"` // CRS rule ids switched off for this site
	ExcludePaths []string `json:"exclude_paths,omitempty"` // path prefixes the WAF skips (e.g. /api/upload)
}

var (
	domainRe     = regexp.MustCompile(`^(\*\.)?([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)*[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
	headerRe     = regexp.MustCompile(`^[A-Za-z0-9-]{1,64}$`)
	hotlinkExtRe = regexp.MustCompile(`^[a-z0-9]{1,8}$`)
	containerR   = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,127}$`)
)

// Problem is a validation error: an i18n code plus params.
type Problem struct {
	Code   string
	Params map[string]any
}

func (p *Problem) Error() string { return p.Code }

func bad(code string, kv ...any) *Problem {
	p := &Problem{Code: code, Params: map[string]any{}}
	for i := 0; i+1 < len(kv); i += 2 {
		p.Params[kv[i].(string)] = kv[i+1]
	}
	return p
}

// Normalize lowercases domains, fills defaults and validates. It changes s in place.
func Normalize(s *Site) error {
	if len(s.Domains) == 0 {
		return bad("site.no_domain")
	}
	seen := map[string]bool{}
	var ds []string
	for _, d := range s.Domains {
		d = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(d)), ".")
		if d == "" || seen[d] {
			continue
		}
		if !domainRe.MatchString(d) && net.ParseIP(d) == nil {
			return bad("site.bad_domain", "domain", d)
		}
		seen[d] = true
		ds = append(ds, d)
	}
	if len(ds) == 0 {
		return bad("site.no_domain")
	}
	s.Domains = ds
	if s.TLS.Mode == "" {
		s.TLS.Mode = "auto"
	}
	switch s.TLS.Mode {
	case "auto", "internal", "off": // internal may carry the cert_id the panel's CA issued
	case "dns":
		if s.TLS.DNS == "" {
			return bad("site.tls_dns_missing")
		}
	case "custom":
		if s.TLS.CertID == 0 {
			return bad("site.tls_cert_missing")
		}
	default:
		return bad("site.bad_tls_mode", "mode", s.TLS.Mode)
	}
	for _, d := range s.Domains {
		if strings.HasPrefix(d, "*.") && s.TLS.Mode == "auto" {
			return bad("site.wildcard_needs_dns", "domain", d)
		}
	}
	t := &s.Target
	switch t.Type {
	case "container":
		if !containerR.MatchString(t.Container) || t.Port < 1 || t.Port > 65535 {
			return bad("site.bad_target")
		}
	case "address":
		if len(t.Upstreams) == 0 {
			return bad("site.bad_target")
		}
		for _, u := range t.Upstreams {
			if _, err := splitHostPort(u); err != nil {
				return bad("site.bad_upstream", "upstream", u)
			}
		}
	case "static":
	case "project":
		if !projectRe.MatchString(t.Project) {
			return bad("site.bad_target")
		}
	case "php":
		if !phpVerRe.MatchString(t.PHP) {
			return bad("site.bad_php")
		}
		if t.Native && !nativePHPR.MatchString(t.PHP) {
			return bad("site.php_not_native", "version", t.PHP)
		}
		if t.RootSub != "" && (!rootSubR.MatchString(t.RootSub) || strings.Contains(t.RootSub, "..")) {
			return bad("site.bad_root_sub")
		}
		for _, w := range t.PHPWorkers {
			if !t.Native || !workerNameR.MatchString(w.Name) || w.Name == "schedule" || strings.TrimSpace(w.Command) == "" || strings.ContainsAny(w.Command, "\n\r") {
				return bad("site.bad_worker")
			}
		}
		if strings.ContainsAny(t.Schedule, "\n\r") || (t.Schedule != "" && !t.Native) {
			return bad("site.bad_worker")
		}
	case "redirect":
		if !strings.HasPrefix(t.To, "http://") && !strings.HasPrefix(t.To, "https://") {
			return bad("site.bad_redirect")
		}
		if t.Code == 0 {
			t.Code = 302
		}
		if t.Code != 301 && t.Code != 302 && t.Code != 307 && t.Code != 308 {
			return bad("site.bad_redirect")
		}
	default:
		return bad("site.bad_target")
	}
	switch t.Scheme {
	case "", "http", "https", "h2c":
	default:
		return bad("site.bad_target")
	}
	switch t.LB {
	case "", "round_robin", "least_conn", "ip_hash", "first":
	default:
		return bad("site.bad_target")
	}
	o := &s.Options
	switch o.WWW {
	case "", "to_www", "to_apex":
	default:
		return bad("site.bad_option", "option", "www")
	}
	for _, h := range append(append([]HeaderOp{}, o.ReqHeaders...), o.RespHeaders...) {
		if !headerRe.MatchString(h.Name) || (h.Op != "set" && h.Op != "add" && h.Op != "del") || strings.ContainsAny(h.Value, "\r\n") {
			return bad("site.bad_header", "name", h.Name)
		}
	}
	for _, p := range o.Paths {
		if !strings.HasPrefix(p.Path, "/") {
			return bad("site.bad_path_rule", "path", p.Path)
		}
		switch p.Action {
		case "proxy":
			if _, err := splitHostPort(p.Upstream); err != nil {
				return bad("site.bad_upstream", "upstream", p.Upstream)
			}
		case "rewrite":
			if !strings.HasPrefix(p.To, "/") {
				return bad("site.bad_path_rule", "path", p.Path)
			}
		case "redirect":
			if p.To == "" {
				return bad("site.bad_path_rule", "path", p.Path)
			}
		case "respond":
		default:
			return bad("site.bad_path_rule", "path", p.Path)
		}
	}
	for _, ip := range append(append([]string{}, o.AllowIPs...), o.DenyIPs...) {
		if _, _, err := net.ParseCIDR(ip); err != nil && net.ParseIP(ip) == nil {
			return bad("site.bad_ip", "ip", ip)
		}
	}
	for _, u := range o.BasicAuth {
		if u.User == "" || !strings.HasPrefix(u.Hash, "$2") {
			return bad("site.bad_basic_auth")
		}
	}
	o.Note = strings.TrimSpace(o.Note)
	if utf8.RuneCountInString(o.Note) > 60 || strings.ContainsAny(o.Note, "\n\r") {
		return bad("site.bad_note")
	}
	if o.LogDays == 0 {
		o.LogDays = 14
	}
	if h := o.Hotlink; h != nil {
		if len(h.Exts) == 0 {
			o.Hotlink = nil
		}
		for _, e := range h.Exts {
			if !hotlinkExtRe.MatchString(e) {
				return bad("site.bad_hotlink", "value", e)
			}
		}
		for _, d := range h.Allow {
			if !domainRe.MatchString(d) {
				return bad("site.bad_hotlink", "value", d)
			}
		}
	}
	if rl := o.RateLimit; rl != nil {
		if rl.Requests == 0 {
			o.RateLimit = nil
		} else if rl.Requests < 1 || rl.Requests > 1_000_000 || rl.Window < 1 || rl.Window > 86400 {
			return bad("site.bad_option", "option", "rate_limit")
		}
	}
	if w := o.WAF; w != nil {
		if w.Mode == "" || w.Mode == "off" {
			o.WAF = nil
		} else {
			if w.Mode != "detect" && w.Mode != "block" {
				return bad("site.bad_option", "option", "waf")
			}
			if w.Paranoia == 0 {
				w.Paranoia = 1
			}
			if w.Paranoia < 1 || w.Paranoia > 4 {
				return bad("site.bad_option", "option", "waf.paranoia")
			}
			for _, id := range w.ExcludeRules {
				if id < 1 || id > 9999999 {
					return bad("site.bad_option", "option", "waf.exclude_rules")
				}
			}
			for _, p := range w.ExcludePaths {
				if !wafPathRe.MatchString(p) {
					return bad("site.bad_path_rule", "path", p)
				}
			}
		}
	}
	return nil
}

var wafPathRe = regexp.MustCompile(`^/[A-Za-z0-9._~/%-]{0,200}$`)

func splitHostPort(u string) (string, error) {
	h, p, err := net.SplitHostPort(u)
	if err != nil {
		return "", err
	}
	n, err := strconv.Atoi(p)
	if err != nil || n < 1 || n > 65535 || h == "" {
		return "", fmt.Errorf("bad port")
	}
	return h, nil
}

// Primary is the first domain: the name used for files and logs.
func (s *Site) Primary() string { return s.Domains[0] }
