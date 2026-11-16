// Package importer turns other panels' configuration into Berth changes (方案 §14):
// a Caddyfile, Nginx Proxy Manager's SQLite database, and 1Panel's database. It only reads
// and proposes; every item becomes an ordinary change the person previews and confirms,
// and the source is left as it was.
package importer

import (
	"bufio"
	"database/sql"
	"encoding/json"
	"fmt"
	"net"
	"strconv"
	"strings"
)

// Item is one thing found in the source.
type Item struct {
	Key    string          `json:"key"`
	Type   string          `json:"type"` // site | cron | app | access_list | cert | other
	Title  string          `json:"title"`
	Detail string          `json:"detail,omitempty"`
	OK     bool            `json:"ok"`               // Berth can create it
	Reason string          `json:"reason,omitempty"` // message key when not (or partly) convertible
	Kind   string          `json:"kind,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
}

func mustJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

func site(key, title string, domains []string, target map[string]any, tls string) Item {
	if tls == "" {
		tls = "auto"
	}
	return Item{Key: key, Type: "site", Title: title, OK: true, Kind: "site.create",
		Params: mustJSON(map[string]any{"domains": domains, "target": target, "tls": map[string]any{"mode": tls}})}
}

// upstream normalizes a proxy target to host:port.
func upstream(s string) (string, bool) {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(strings.TrimPrefix(s, "http://"), "https://")
	s = strings.TrimSuffix(s, "/")
	if strings.HasPrefix(s, "localhost") {
		s = "127.0.0.1" + strings.TrimPrefix(s, "localhost")
	}
	if strings.HasPrefix(s, ":") {
		s = "127.0.0.1" + s
	}
	h, p, err := net.SplitHostPort(s)
	if err != nil || h == "" {
		return "", false
	}
	if n, err := strconv.Atoi(p); err != nil || n < 1 || n > 65535 {
		return "", false
	}
	return s, true
}

// ---- Caddyfile ----

type block struct {
	addrs []string
	lines [][]string // directives, nested blocks flattened with their first word kept
}

// tokenize splits a Caddyfile into site blocks (global options and snippets are skipped).
func parseCaddyfile(text string) []block {
	var out []block
	sc := bufio.NewScanner(strings.NewReader(text))
	depth := 0
	var cur *block
	for sc.Scan() {
		l := strings.TrimSpace(sc.Text())
		if i := strings.Index(l, " #"); i >= 0 {
			l = strings.TrimSpace(l[:i])
		}
		if l == "" || strings.HasPrefix(l, "#") {
			continue
		}
		opens := strings.HasSuffix(l, "{")
		closes := l == "}"
		if depth == 0 {
			if opens {
				head := strings.TrimSpace(strings.TrimSuffix(l, "{"))
				depth = 1
				if head == "" || strings.HasPrefix(head, "(") { // global options or a snippet
					cur = nil
					continue
				}
				b := block{}
				for _, a := range strings.FieldsFunc(head, func(r rune) bool { return r == ',' || r == ' ' }) {
					b.addrs = append(b.addrs, a)
				}
				out = append(out, b)
				cur = &out[len(out)-1]
			}
			continue
		}
		if closes {
			depth--
			if depth == 0 {
				cur = nil
			}
			continue
		}
		if cur != nil {
			f := strings.Fields(strings.TrimSuffix(l, "{"))
			if len(f) > 0 {
				cur.lines = append(cur.lines, f)
			}
		}
		if opens {
			depth++
		}
	}
	return out
}

func hostOf(addr string) string {
	a := strings.TrimPrefix(strings.TrimPrefix(addr, "https://"), "http://")
	if h, _, err := net.SplitHostPort(a); err == nil {
		return h
	}
	return strings.TrimSuffix(a, "/")
}

// Caddyfile proposes one site per site block: reverse_proxy, redir, or file_server.
func Caddyfile(text string) []Item {
	var out []Item
	for i, b := range parseCaddyfile(text) {
		var domains []string
		for _, a := range b.addrs {
			if h := hostOf(a); h != "" && h != "localhost" && !strings.HasPrefix(a, ":") {
				domains = append(domains, h)
			}
		}
		key := fmt.Sprintf("caddy-%d", i)
		title := strings.Join(b.addrs, ", ")
		if len(domains) == 0 {
			out = append(out, Item{Key: key, Type: "site", Title: title, Reason: "import.no_domain"})
			continue
		}
		var ups []string
		var redirTo, root string
		code := 302
		extra := 0
		tls := ""
		for _, l := range b.lines {
			switch l[0] {
			case "reverse_proxy":
				for _, u := range l[1:] {
					if strings.HasPrefix(u, "/") || strings.HasPrefix(u, "@") || u == "*" {
						continue
					}
					if up, ok := upstream(u); ok {
						ups = append(ups, up)
					}
				}
			case "to":
				for _, u := range l[1:] {
					if up, ok := upstream(u); ok {
						ups = append(ups, up)
					}
				}
			case "redir":
				args := l[1:]
				if len(args) > 0 && (strings.HasPrefix(args[0], "/") || strings.HasPrefix(args[0], "@")) && len(args) > 1 && !strings.Contains(args[0], "://") {
					args = args[1:]
				}
				if len(args) > 0 {
					redirTo = args[0]
				}
				if len(args) > 1 && (args[1] == "permanent" || args[1] == "301") {
					code = 301
				}
			case "root":
				root = l[len(l)-1]
			case "file_server", "encode", "header", "log":
			case "tls":
				if len(l) > 1 && l[1] == "internal" {
					tls = "internal"
				}
			default:
				extra++
			}
		}
		var it Item
		switch {
		case len(ups) > 0:
			it = site(key, title, domains, map[string]any{"type": "address", "upstreams": ups}, tls)
			it.Detail = "→ " + strings.Join(ups, ", ")
		case redirTo != "":
			it = site(key, title, domains, map[string]any{"type": "redirect", "to": strings.ReplaceAll(redirTo, "{uri}", ""), "code": code}, tls)
			it.Detail = "⇢ " + redirTo
		case root != "":
			it = site(key, title, domains, map[string]any{"type": "static"}, tls)
			it.Detail = root
			it.Reason = "import.copy_files" // the files stay where they are
		default:
			it = Item{Key: key, Type: "site", Title: title, Reason: "import.unknown_site"}
		}
		if it.OK && extra > 0 && it.Reason == "" {
			it.Reason = "import.partial"
		}
		out = append(out, it)
	}
	return out
}

// ---- Nginx Proxy Manager ----

// NPM reads proxy hosts, redirection hosts, access lists and certificates.
func NPM(db *sql.DB) ([]Item, error) {
	var out []Item
	rows, err := db.Query(`SELECT id, domain_names, forward_scheme, forward_host, forward_port, enabled, ssl_forced, access_list_id FROM proxy_host WHERE is_deleted=0`)
	if err != nil {
		return nil, fmt.Errorf("not an Nginx Proxy Manager database: %w", err)
	}
	for rows.Next() {
		var id, port, enabled, ssl, acl int
		var names, scheme, host string
		rows.Scan(&id, &names, &scheme, &host, &port, &enabled, &ssl, &acl)
		var domains []string
		json.Unmarshal([]byte(names), &domains)
		key := fmt.Sprintf("npm-proxy-%d", id)
		up, ok := upstream(fmt.Sprintf("%s:%d", host, port))
		if !ok {
			out = append(out, Item{Key: key, Type: "site", Title: strings.Join(domains, ", "), Reason: "import.bad_target"})
			continue
		}
		target := map[string]any{"type": "address", "upstreams": []string{up}}
		if scheme == "https" {
			target["scheme"] = "https"
			target["insecure_tls"] = true
		}
		it := site(key, strings.Join(domains, ", "), domains, target, "")
		it.Detail = scheme + "://" + up
		if acl != 0 {
			it.Reason = "import.npm_acl" // access lists are noted, not converted
		}
		if enabled == 0 {
			it.Reason = "import.disabled"
		}
		out = append(out, it)
	}
	rows.Close()
	if rows, err := db.Query(`SELECT id, domain_names, forward_domain_name, forward_scheme, forward_http_code, preserve_path FROM redirection_host WHERE is_deleted=0`); err == nil {
		for rows.Next() {
			var id, code, keep int
			var names, to, scheme string
			rows.Scan(&id, &names, &to, &scheme, &code, &keep)
			var domains []string
			json.Unmarshal([]byte(names), &domains)
			if scheme == "" || scheme == "auto" || scheme == "$scheme" {
				scheme = "https"
			}
			if code != 301 && code != 308 && code != 307 {
				code = 302
			}
			it := site(fmt.Sprintf("npm-redir-%d", id), strings.Join(domains, ", "), domains, map[string]any{"type": "redirect", "to": scheme + "://" + to, "code": code}, "")
			it.Detail = "⇢ " + to
			out = append(out, it)
		}
		rows.Close()
	}
	if rows, err := db.Query(`SELECT id, name FROM access_list WHERE is_deleted=0`); err == nil {
		for rows.Next() {
			var id int
			var name string
			rows.Scan(&id, &name)
			out = append(out, Item{Key: fmt.Sprintf("npm-acl-%d", id), Type: "access_list", Title: name, Reason: "import.npm_acl"})
		}
		rows.Close()
	}
	if rows, err := db.Query(`SELECT id, nice_name, provider FROM certificate WHERE is_deleted=0`); err == nil {
		for rows.Next() {
			var id int
			var name, provider string
			rows.Scan(&id, &name, &provider)
			out = append(out, Item{Key: fmt.Sprintf("npm-cert-%d", id), Type: "cert", Title: name, Detail: provider, Reason: "import.cert_reissue"})
		}
		rows.Close()
	}
	return out, nil
}

// ---- 1Panel ----

func colSet(db *sql.DB, table string) map[string]bool {
	out := map[string]bool{}
	rows, err := db.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var cid, notnull, pk int
		var name, typ string
		var def any
		rows.Scan(&cid, &name, &typ, &notnull, &def, &pk)
		out[name] = true
	}
	return out
}

// OnePanel reads websites, scheduled jobs and installed apps from 1Panel's database.
// Columns are checked first: 1Panel's schema changes between versions.
func OnePanel(db *sql.DB) ([]Item, error) {
	var out []Item
	ws := colSet(db, "websites")
	if len(ws) == 0 {
		return nil, fmt.Errorf("not a 1Panel database (no websites table)")
	}
	if ws["primary_domain"] {
		q := `SELECT id, primary_domain, type, ` + map[bool]string{true: "proxy", false: "''"}[ws["proxy"]] + `, ` + map[bool]string{true: "status", false: "''"}[ws["status"]] + ` FROM websites`
		rows, err := db.Query(q)
		if err == nil {
			for rows.Next() {
				var id int
				var domain, typ, proxy, status string
				rows.Scan(&id, &domain, &typ, &proxy, &status)
				domains := []string{domain}
				if dr, err := db.Query(`SELECT domain FROM website_domains WHERE website_id=?`, id); err == nil {
					for dr.Next() {
						var d string
						dr.Scan(&d)
						if d != domain && d != "" {
							domains = append(domains, d)
						}
					}
					dr.Close()
				}
				key := fmt.Sprintf("1panel-site-%d", id)
				switch typ {
				case "proxy":
					if up, ok := upstream(proxy); ok {
						it := site(key, domain, domains, map[string]any{"type": "address", "upstreams": []string{up}}, "")
						it.Detail = "→ " + up
						out = append(out, it)
					} else {
						out = append(out, Item{Key: key, Type: "site", Title: domain, Detail: proxy, Reason: "import.bad_target"})
					}
				case "deployment":
					out = append(out, Item{Key: key, Type: "site", Title: domain, Detail: "app", Reason: "import.1panel_app_site"})
				case "static":
					it := site(key, domain, domains, map[string]any{"type": "static"}, "")
					it.Reason = "import.copy_files"
					out = append(out, it)
				default:
					out = append(out, Item{Key: key, Type: "site", Title: domain, Detail: typ, Reason: "import.runtime_site"})
				}
			}
			rows.Close()
		}
	}
	cj := colSet(db, "cronjobs")
	if cj["spec"] && cj["name"] {
		script := map[bool]string{true: "script", false: "''"}[cj["script"]]
		typ := map[bool]string{true: "type", false: "'shell'"}[cj["type"]]
		rows, err := db.Query(`SELECT id, name, ` + typ + `, spec, ` + script + ` FROM cronjobs`)
		if err == nil {
			for rows.Next() {
				var id int
				var name, t, spec, sc string
				rows.Scan(&id, &name, &t, &spec, &sc)
				key := fmt.Sprintf("1panel-cron-%d", id)
				if t != "shell" || strings.TrimSpace(sc) == "" || len(strings.Fields(spec)) != 5 {
					out = append(out, Item{Key: key, Type: "cron", Title: name, Detail: t, Reason: "import.cron_kind"})
					continue
				}
				out = append(out, Item{Key: key, Type: "cron", Title: name, Detail: spec, OK: true, Kind: "cron.set",
					Params: mustJSON(map[string]any{"name": name, "cron": spec, "action": map[string]any{"type": "script", "script": sc}})})
			}
			rows.Close()
		}
	}
	ai := colSet(db, "app_installs")
	if ai["name"] {
		rows, err := db.Query(`SELECT id, name FROM app_installs`)
		if err == nil {
			for rows.Next() {
				var id int
				var name string
				rows.Scan(&id, &name)
				// 1Panel apps are compose projects: Berth already lists them under Containers to adopt
				out = append(out, Item{Key: fmt.Sprintf("1panel-app-%d", id), Type: "app", Title: name, Reason: "import.adopt_compose"})
			}
			rows.Close()
		}
	}
	return out, nil
}
