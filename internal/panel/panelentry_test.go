package panel

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// The edge passes the visitor's address on; only requests carrying the edge's secret are
// believed (another container on the edge network cannot claim to be on the LAN).
func TestEdgeClientIP(t *testing.T) {
	c := newTC(t)
	sec := c.app.edgeSecret()
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "172.18.0.5:41000"
	r.Header.Set("X-Forwarded-For", "203.0.113.7")
	if ip := c.app.clientIP(r); ip != "172.18.0.5" {
		t.Fatalf("believed X-Forwarded-For without the secret: %s", ip)
	}
	r.Header.Set("X-Berth-Edge", "wrong")
	if ip := c.app.clientIP(r); ip != "172.18.0.5" {
		t.Fatalf("believed a wrong secret: %s", ip)
	}
	r.Header.Set("X-Berth-Edge", sec)
	if ip := c.app.clientIP(r); ip != "203.0.113.7" {
		t.Fatalf("edge visitor: %s", ip)
	}
}

// Links drop the port once the edge serves the name.
func TestPanelURLWithoutPort(t *testing.T) {
	c := newTC(t)
	c.app.setSetting("panel.lan_host", "nas.lan")
	if u := c.app.panelURL("panel.lan_host"); u != "https://nas.lan:"+c.app.panelPort() {
		t.Fatalf("before the edge serves it: %s", u)
	}
	c.app.setSetting("access.entry_served", "nas.lan,nas.local||abc")
	if u := c.app.panelURL("panel.lan_host"); u != "https://nas.lan" {
		t.Fatalf("served on 443: %s", u)
	}
}

// The hop link moves the browser to the main name keeping the entry and the sign-in; it
// works once, only on that name.
func TestHopToMainName(t *testing.T) {
	c := newTC(t)
	c.mkUser("root", "correct horse 9", "super")
	c.login("root", "correct horse 9")
	c.app.setSetting("panel.lan_host", "nas.lan")
	_, where := c.do("GET", "/api/v1/access/where", nil)
	if where["lan_name"] != "nas.lan" || where["is_main"] != false {
		t.Fatalf("where: %v", where)
	}
	_, h := c.do("GET", "/api/v1/access/hop?to=lan&path=/local/sites", nil)
	u, _ := url.Parse(h["url"].(string))
	if u.Hostname() != "nas.lan" || !strings.HasPrefix(u.Path, "/hop/") {
		t.Fatalf("hop: %v", h)
	}
	fresh := &http.Client{Transport: c.srv.Client().Transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	get := func(host string) *http.Response {
		req, _ := http.NewRequest("GET", c.srv.URL+u.Path, nil)
		req.Host = host
		resp, err := fresh.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp
	}
	if r := get("evil.example"); r.StatusCode != 404 { // another name: refused (and the link is spent)
		t.Fatalf("other host: %d", r.StatusCode)
	}
	_, h = c.do("GET", "/api/v1/access/hop?to=lan&path=/local/sites", nil)
	u, _ = url.Parse(h["url"].(string))
	r := get("nas.lan")
	if r.StatusCode != 302 || r.Header.Get("Location") != "/local/sites" {
		t.Fatalf("hop: %d %s", r.StatusCode, r.Header.Get("Location"))
	}
	signed := false
	for _, ck := range r.Cookies() {
		signed = signed || ck.Name == sessionCookie
	}
	if !signed {
		t.Fatal("sign-in not carried over")
	}
	if r := get("nas.lan"); r.StatusCode != 404 {
		t.Fatal("hop used twice")
	}
}

// Behind the security entry, someone on the LAN opening the panel's own name gets told what
// to do (never the entry itself); other names and API calls still get the bare 404.
func TestFriendlyGate(t *testing.T) {
	c := newTC(t)
	c.mkUser("root", "correct horse 9", "super")
	c.app.setSetting("access.entry", "/s3cret-way")
	c.app.setSetting("panel.lan_host", "nas.lan")
	get := func(host, accept string) (int, string) {
		req, _ := http.NewRequest("GET", c.srv.URL+"/", nil)
		req.Host = host
		req.Header.Set("Accept", accept)
		resp, err := (&http.Client{Transport: c.srv.Client().Transport}).Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b := new(strings.Builder)
		io.Copy(b, resp.Body)
		return resp.StatusCode, b.String()
	}
	code, body := get(hostBase()+".local", "text/html") // the machine's own name, before the rename
	if code != 404 || !strings.Contains(body, "https://nas.lan") || !strings.Contains(body, "安全入口") || strings.Contains(body, "s3cret") {
		t.Fatalf("old name: %d %s", code, body)
	}
	if _, body := get("nas.lan", "text/html"); !strings.Contains(body, "安全入口") || strings.Contains(body, "新地址") {
		t.Fatalf("main name: %s", body)
	}
	if _, body := get("elsewhere.example", "text/html"); body != "404 page not found\n" {
		t.Fatalf("another name: %q", body)
	}
	if _, body := get("nas.lan", "application/json"); body != "404 page not found\n" {
		t.Fatalf("api: %q", body)
	}
}
