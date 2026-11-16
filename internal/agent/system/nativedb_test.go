package system

import (
	"strings"
	"testing"
)

func TestRecommendDB(t *testing.T) {
	m := RecommendDB("mariadb", "11.8", 2048)
	if m["innodb_buffer_pool_size"] != "1228M" || m["max_connections"] != "200" || m["innodb_log_file_size"] != "307M" || m["innodb_redo_log_capacity"] != "" {
		t.Errorf("mariadb 2G: %v", m)
	}
	if RecommendDB("mysql", "8.4", 2048)["innodb_redo_log_capacity"] != "307M" {
		t.Error("mysql uses the redo log capacity")
	}
	if ValidateDBValues("mariadb", m) != nil || ValidateDBValues("postgres", RecommendDB("postgres", "18", 4096)) != nil {
		t.Error("recommended values must validate")
	}
	p := RecommendDB("postgres", "18", 4096)
	if p["shared_buffers"] != "1024MB" || p["effective_cache_size"] != "3072MB" || p["max_connections"] != "200" || p["work_mem"] != "5MB" {
		t.Errorf("postgres 4G: %v", p)
	}
	for _, bad := range []map[string]string{{"datadir": "/tmp"}, {"max_connections": "1; DROP"}, {"innodb_buffer_pool_size": "1G\nskip-grant-tables"}} {
		if ValidateDBValues("mariadb", bad) == nil {
			t.Errorf("accepted %v", bad)
		}
	}
	if ValidateDBValues("postgres", map[string]string{"shared_buffers": "1GB' ; x"}) == nil {
		t.Error("postgres quote")
	}
}

func TestDBConfigText(t *testing.T) {
	d := DBNative{Engine: "mariadb", Port: 3306, Values: map[string]string{"max_connections": "200"}}
	c := mysqlConfText(d)
	for _, w := range []string{"[mysqld]\n", "bind-address = 0.0.0.0\n", "collation-server = utf8mb4_unicode_ci\n", "max_connections = 200\n"} {
		if !strings.Contains(c, w) {
			t.Errorf("lacks %q:\n%s", w, c)
		}
	}
	pg := DBNative{Engine: "postgres", Port: 5432, Allow: []string{"10.0.2.5", "192.168.1.0/24"}, Values: map[string]string{"work_mem": "8MB"}}
	if !strings.Contains(pgConfText(pg), "work_mem = '8MB'\n") {
		t.Error(pgConfText(pg))
	}
	h := pgHBAText(pg)
	for _, w := range []string{"127.0.0.1/32", "172.16.0.0/12", "10.0.2.5/32", "192.168.1.0/24"} {
		if !strings.Contains(h, w) {
			t.Errorf("hba lacks %s", w)
		}
	}
	n := dbNftBody([]DBNative{d, pg})
	for _, w := range []string{"iifname \"docker0\" accept", "ip saddr 10.0.2.5 tcp dport 5432 accept", "tcp dport { 3306, 5432 } drop"} {
		if !strings.Contains(n, w) {
			t.Errorf("nft lacks %q:\n%s", w, n)
		}
	}
}

func TestRedisNative(t *testing.T) {
	r := RecommendDB("redis", "valkey", 1000)
	if r["maxmemory"] != "800mb" || ValidateDBValues("redis", r) != nil {
		t.Errorf("redis recommend: %v", r)
	}
	if ValidateDBValues("redis", map[string]string{"requirepass": "x"}) == nil || ValidateDBValues("redis", map[string]string{"maxmemory": "1gb\nrequirepass x"}) == nil {
		t.Error("redis accepted a bad setting")
	}
	c := redisConfText(DBNative{Engine: "redis", Pass: "pw123", Values: map[string]string{"maxmemory": "800mb"}})
	for _, w := range []string{"bind 0.0.0.0 -::\n", "requirepass pw123\n", "maxmemory 800mb\n"} {
		if !strings.Contains(c, w) {
			t.Errorf("redis conf lacks %q", w)
		}
	}
	if dbFamily("redis") != "redis" || dbFamily("mariadb") != "mysql" {
		t.Error("families")
	}
}
