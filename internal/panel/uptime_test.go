package panel

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestProbe(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/bad" {
			w.WriteHeader(503)
		}
		io.WriteString(w, "hello berth")
	}))
	defer srv.Close()
	ctx := context.Background()
	if ok, _, e := probe(ctx, uptimeCheck{Kind: "http", Target: srv.URL, Keyword: "berth"}); !ok {
		t.Fatal(e)
	}
	if ok, _, e := probe(ctx, uptimeCheck{Kind: "http", Target: srv.URL + "/bad"}); ok || e != "HTTP 503" {
		t.Fatal(ok, e)
	}
	if ok, _, _ := probe(ctx, uptimeCheck{Kind: "http", Target: srv.URL, Keyword: "nope"}); ok {
		t.Fatal("keyword")
	}
	if ok, _, _ := probe(ctx, uptimeCheck{Kind: "http", Target: srv.URL + "/bad", Expect: 503}); !ok {
		t.Fatal("expect 503")
	}
	if ok, _, e := probe(ctx, uptimeCheck{Kind: "tcp", Target: strings.TrimPrefix(srv.URL, "http://")}); !ok {
		t.Fatal(e)
	}
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := l.Addr().String()
	l.Close()
	if ok, _, _ := probe(ctx, uptimeCheck{Kind: "tcp", Target: addr}); ok {
		t.Fatal("closed port up")
	}
	for _, bad := range []uptimeCheck{{Name: "x", Kind: "http", Target: "ftp://x"}, {Name: "x", Kind: "tcp", Target: "nohost"}, {Kind: "http", Target: "http://x"}, {Name: "x", Kind: "http", Target: "http://x", Interval: 5}} {
		if validCheck(bad) == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
}

func TestUptimeAlertAndStatusPage(t *testing.T) {
	c := newTC(t)
	a := c.app
	res, _ := a.db.Exec(`INSERT INTO uptime_checks(spec, created_at) VALUES(?,?)`, `{"name":"Blog","kind":"http","target":"http://10.9.9.9/secret-path","interval":60}`, time.Now().Unix())
	id, _ := res.LastInsertId()
	ck := uptimeCheck{ID: id, Name: "Blog", Kind: "http", Target: "http://10.9.9.9/secret-path"}
	now := time.Now()
	a.recordProbe(ck, now)(false, 10, "timeout")
	var n int
	a.db.QueryRow(`SELECT COUNT(*) FROM alert_events WHERE rule='site_down' AND state='firing'`).Scan(&n)
	if n != 0 {
		t.Fatal("fired after one miss")
	}
	a.recordProbe(ck, now.Add(time.Minute))(false, 10, "timeout")
	a.db.QueryRow(`SELECT COUNT(*) FROM alert_events WHERE rule='site_down' AND state='firing'`).Scan(&n)
	if n != 1 {
		t.Fatal("no alert after two misses")
	}
	a.recordProbe(ck, now.Add(2*time.Minute))(true, 12, "")
	a.db.QueryRow(`SELECT COUNT(*) FROM alert_events WHERE rule='site_down' AND state='firing'`).Scan(&n)
	if n != 0 {
		t.Fatal("not resolved")
	}
	a.db.Exec(`INSERT INTO status_pages(slug, spec) VALUES('main', ?)`, `{"title":"Our services","checks":[`+itoa(id)+`]}`)
	resp, err := c.hc.Get(c.srv.URL + "/status/main")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(b), "Our services") || !strings.Contains(string(b), "Blog") || strings.Contains(string(b), "secret-path") {
		t.Fatalf("%d %s", resp.StatusCode, b)
	}
	if !strings.Contains(string(b), "33.33%") {
		t.Errorf("uptime not shown: %s", b)
	}
	resp, _ = c.hc.Get(c.srv.URL + "/status/nope")
	if resp.StatusCode != 404 {
		t.Fatal("unknown page", resp.StatusCode)
	}
}
