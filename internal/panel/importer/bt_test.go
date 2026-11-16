package importer

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

// a 宝塔 server as its default.db and Nginx files describe it
func TestBT(t *testing.T) {
	db, _ := sql.Open("sqlite", filepath.Join(t.TempDir(), "default.db"))
	for _, q := range []string{
		`CREATE TABLE sites (id INTEGER, name TEXT, path TEXT, status TEXT, ps TEXT, project_type TEXT, project_config TEXT)`,
		`INSERT INTO sites VALUES (1, 'blog.example.com', '/www/wwwroot/blog.example.com', '1', '', 'PHP', ''),
			(2, 'static.example.com', '/www/wwwroot/static.example.com/', '1', '', 'PHP', ''),
			(3, 'api.example.com', '/www/wwwroot/api.example.com', '1', '', 'PHP', ''),
			(4, 'node.example.com', '/www/wwwroot/node', '1', '', 'Node', '{"port":3000}')`,
		`CREATE TABLE domain (id INTEGER, pid INTEGER, name TEXT, port INTEGER)`,
		`INSERT INTO domain VALUES (1, 1, 'blog.example.com', 80), (2, 1, 'www.blog.example.com', 80), (3, 1, 'blog.example.com:8080', 8080)`,
		`CREATE TABLE databases (id INTEGER, pid INTEGER, name TEXT, username TEXT, password TEXT, type TEXT, sid INTEGER)`,
		`INSERT INTO databases VALUES (1, 1, 'blog', 'blog', 'p''w', 'MySQL', 0), (2, 0, 'far', 'far', 'x', 'MySQL', 3), (3, 0, 'mg', 'mg', 'x', 'MongoDB', 0)`,
		`CREATE TABLE config (id INTEGER, mysql_root TEXT)`, `INSERT INTO config VALUES (1, 'rootpw')`,
		`CREATE TABLE crontab (id INTEGER, name TEXT, type TEXT, where1 TEXT, where_hour INTEGER, where_minute INTEGER, sType TEXT, sBody TEXT)`,
		`INSERT INTO crontab VALUES (1, 'clean', 'day', '', 3, 30, 'toShell', 'rm -rf /tmp/x'),
			(2, 'every 5', 'minute-n', '5', 0, 0, 'toShell', 'echo hi'),
			(3, 'backup site', 'day', '', 1, 0, 'site', ''),
			(4, 'weekly', 'week', '1', 2, 15, 'toShell', 'echo w')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	files := map[string]string{
		"/www/server/panel/vhost/nginx/blog.example.com.conf":   "server {\n  listen 80;\n  listen 443 ssl http2;\n  root /www/wwwroot/blog.example.com/public;\n  include enable-php-74.conf;\n}\n",
		"/www/server/panel/vhost/rewrite/blog.example.com.conf": "location / { try_files $uri $uri/ /index.php?$query_string; }",
		"/www/server/panel/vhost/nginx/static.example.com.conf": "server {\n  root /www/wwwroot/static.example.com;\n  include enable-php-00.conf;\n}\n",
		"/www/server/panel/vhost/nginx/api.example.com.conf":    "server {\n  root /www/wwwroot/api.example.com;\n  include /www/server/panel/vhost/nginx/proxy/api.example.com/*.conf;\n}\n",
		"/www/server/panel/vhost/nginx/proxy/api.example.com/x.conf": "location / {\n    proxy_pass http://127.0.0.1:8080;\n}\n",
		"/www/server/panel/vhost/nginx/node_node.example.com.conf":   "server { proxy_pass http://127.0.0.1:3000; }",
	}
	src := BTSource{DB: db, Read: func(p string) string { return files[p] }, List: func(dir string) []string {
		var out []string
		for p := range files {
			if filepath.Dir(p) == dir {
				out = append(out, filepath.Base(p))
			}
		}
		return out
	}}
	d, err := BT(src)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Sites) != 4 {
		t.Fatalf("sites: %+v", d.Sites)
	}
	b := d.Sites[0]
	if b.Kind != "php" || b.PHP != "7.4" || b.RootSub != "public" || !b.SSL || !b.Rewrite || strings.Join(b.Domains, ",") != "blog.example.com,www.blog.example.com" {
		t.Fatalf("blog: %+v", b)
	}
	if s := d.Sites[1]; s.Kind != "static" || s.Path != "/www/wwwroot/static.example.com" {
		t.Fatalf("static: %+v", s)
	}
	if s := d.Sites[2]; s.Kind != "proxy" || s.Upstream != "127.0.0.1:8080" {
		t.Fatalf("proxy: %+v", s)
	}
	if s := d.Sites[3]; s.Kind != "project" || s.Upstream != "127.0.0.1:3000" {
		t.Fatalf("node: %+v", s)
	}
	if len(d.Databases) != 1 || d.Databases[0].Name != "blog" || d.RootPass != "rootpw" {
		t.Fatalf("dbs: %+v %q", d.Databases, d.RootPass)
	}
	if !strings.Contains(BTUsersSQL(d.Databases), `IDENTIFIED BY 'p\'w'`) {
		t.Fatal("password quoting: " + BTUsersSQL(d.Databases))
	}
	want := map[int]string{1: "30 3 * * *", 2: "*/5 * * * *", 4: "15 2 * * 1"}
	for _, c := range d.Crons {
		if w, ok := want[c.ID]; ok && (!c.OK || c.Spec != w) {
			t.Fatalf("cron %d: %+v", c.ID, c)
		}
		if c.ID == 3 && c.OK {
			t.Fatal("a site backup job is not a script")
		}
	}
}
