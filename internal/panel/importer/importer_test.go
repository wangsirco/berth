package importer

import (
	"database/sql"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

func find(items []Item, key string) Item {
	for _, it := range items {
		if it.Key == key {
			return it
		}
	}
	return Item{}
}

func TestCaddyfile(t *testing.T) {
	items := Caddyfile(`{
	email me@example.com
}
(common) {
	encode gzip
}
app.example.com, www.app.example.com {
	import common
	reverse_proxy localhost:3000 127.0.0.1:3001
}
old.example.com {
	redir https://new.example.com{uri} permanent
}
files.example.com {
	root * /srv/files
	file_server
}
:8080 {
	respond "hi"
}
lan.home {
	tls internal
	reverse_proxy 192.168.1.20:8123 {
		header_up Host {host}
	}
}
`)
	if len(items) != 5 {
		t.Fatalf("%d items: %+v", len(items), items)
	}
	var p struct {
		Domains []string
		Target  struct {
			Type      string
			Upstreams []string
			To        string
			Code      int
		}
		TLS struct{ Mode string }
	}
	json.Unmarshal(items[0].Params, &p)
	if !items[0].OK || strings.Join(p.Domains, ",") != "app.example.com,www.app.example.com" || strings.Join(p.Target.Upstreams, ",") != "127.0.0.1:3000,127.0.0.1:3001" {
		t.Fatalf("proxy: %+v %s", items[0], items[0].Params)
	}
	json.Unmarshal(items[1].Params, &p)
	if p.Target.Type != "redirect" || p.Target.To != "https://new.example.com" || p.Target.Code != 301 {
		t.Fatalf("redirect: %s", items[1].Params)
	}
	if items[2].Reason != "import.copy_files" {
		t.Fatalf("static: %+v", items[2])
	}
	if items[3].OK || items[3].Reason != "import.no_domain" {
		t.Fatalf(":8080: %+v", items[3])
	}
	json.Unmarshal(items[4].Params, &p)
	if p.TLS.Mode != "internal" || p.Target.Upstreams[0] != "192.168.1.20:8123" {
		t.Fatalf("lan: %s", items[4].Params)
	}
}

func TestNPMAndOnePanel(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "npm.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`CREATE TABLE proxy_host (id INTEGER, domain_names TEXT, forward_scheme TEXT, forward_host TEXT, forward_port INTEGER, enabled INTEGER, ssl_forced INTEGER, access_list_id INTEGER, is_deleted INTEGER)`,
		`INSERT INTO proxy_host VALUES (1, '["photos.example.com"]', 'http', 'immich', 2283, 1, 1, 0, 0), (2, '["gone.example.com"]', 'http', 'x', 1, 1, 0, 0, 1), (3, '["nas.example.com"]', 'https', '10.0.0.5', 5001, 1, 1, 4, 0)`,
		`CREATE TABLE redirection_host (id INTEGER, domain_names TEXT, forward_domain_name TEXT, forward_scheme TEXT, forward_http_code INTEGER, preserve_path INTEGER, is_deleted INTEGER)`,
		`INSERT INTO redirection_host VALUES (1, '["a.example.com"]', 'b.example.com', 'auto', 301, 1, 0)`,
		`CREATE TABLE access_list (id INTEGER, name TEXT, is_deleted INTEGER)`, `INSERT INTO access_list VALUES (4, 'family', 0)`,
		`CREATE TABLE certificate (id INTEGER, nice_name TEXT, provider TEXT, is_deleted INTEGER)`, `INSERT INTO certificate VALUES (1, 'example', 'letsencrypt', 0)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	items, err := NPM(db)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 5 || !find(items, "npm-proxy-1").OK || find(items, "npm-proxy-3").Reason != "import.npm_acl" || !strings.Contains(string(find(items, "npm-proxy-3").Params), `"scheme":"https"`) {
		t.Fatalf("npm: %+v", items)
	}
	if !strings.Contains(string(find(items, "npm-redir-1").Params), `"to":"https://b.example.com"`) {
		t.Fatalf("redir: %s", find(items, "npm-redir-1").Params)
	}
	if _, err := OnePanel(db); err == nil {
		t.Fatal("an NPM database is not 1Panel's")
	}

	db2, _ := sql.Open("sqlite", filepath.Join(t.TempDir(), "1panel.db"))
	for _, q := range []string{
		`CREATE TABLE websites (id INTEGER, primary_domain TEXT, type TEXT, proxy TEXT, status TEXT)`,
		`INSERT INTO websites VALUES (1, 'blog.example.com', 'proxy', 'http://127.0.0.1:8080', 'Running'), (2, 'shop.example.com', 'runtime', '', ''), (3, 'docs.example.com', 'static', '', '')`,
		`CREATE TABLE website_domains (website_id INTEGER, domain TEXT)`, `INSERT INTO website_domains VALUES (1, 'blog.example.com'), (1, 'www.blog.example.com')`,
		`CREATE TABLE cronjobs (id INTEGER, name TEXT, type TEXT, spec TEXT, script TEXT)`,
		`INSERT INTO cronjobs VALUES (1, 'clean', 'shell', '0 3 * * *', 'rm -rf /tmp/x'), (2, 'db', 'database', '0 4 * * *', '')`,
		`CREATE TABLE app_installs (id INTEGER, name TEXT)`, `INSERT INTO app_installs VALUES (1, 'mysql')`,
	} {
		if _, err := db2.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	items, err = OnePanel(db2)
	if err != nil {
		t.Fatal(err)
	}
	if s := find(items, "1panel-site-1"); !s.OK || !strings.Contains(string(s.Params), `"www.blog.example.com"`) || !strings.Contains(string(s.Params), `127.0.0.1:8080`) {
		t.Fatalf("site: %+v %s", s, s.Params)
	}
	if find(items, "1panel-site-2").Reason != "import.runtime_site" || find(items, "1panel-site-3").Reason != "import.copy_files" {
		t.Fatalf("sites: %+v", items)
	}
	if c := find(items, "1panel-cron-1"); !c.OK || c.Kind != "cron.set" || find(items, "1panel-cron-2").OK {
		t.Fatalf("cron: %+v", items)
	}
	if find(items, "1panel-app-1").Reason != "import.adopt_compose" {
		t.Fatalf("app: %+v", items)
	}
}
