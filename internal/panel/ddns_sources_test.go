package panel

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// A DDNS-only account (dyndns2 protocol) with a fixed address: the service gets
// hostname + myip with basic auth; the address is sent again only when it changes.
func TestDDNSDynDNS2Fixed(t *testing.T) {
	var mu sync.Mutex
	var calls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, p, _ := r.BasicAuth()
		mu.Lock()
		calls = append(calls, r.URL.Path+"?"+r.URL.RawQuery+" "+u+":"+p)
		mu.Unlock()
		if u != "me" || p != "pw" {
			w.Write([]byte("badauth"))
			return
		}
		w.Write([]byte("good " + r.URL.Query().Get("myip")))
	}))
	defer srv.Close()
	c := newTC(t)
	c.withFakeAgent()
	c.mkUser("root", "correct horse 9", "super")
	c.login("root", "correct horse 9")
	run := func(kind string, p any) map[string]any {
		t.Helper()
		r, out := c.do("POST", "/api/v1/hosts/local/changes/"+kind, p)
		id, _ := out["id"].(string)
		if id == "" || c.waitChange(id) != "done" {
			ch, _ := c.app.pipe.Get(id)
			t.Fatalf("%s: %d %v %s", kind, r.StatusCode, out, ch.Log)
		}
		_, out = c.do("GET", "/api/v1/changes/"+id, nil)
		return out
	}
	run("integration.set", map[string]any{"id": "noip", "kind": "dyndns2", "name": "No-IP", "credentials": map[string]string{"server": srv.URL, "username": "me", "password": "pw"}})
	if r, _ := c.do("POST", "/api/v1/hosts/local/changes/ddns.set?plan=1", map[string]any{"integration": "noip", "name": "home.ddns.net", "type": "A", "source": "fixed:2001:db8::1"}); r.StatusCode != 400 {
		t.Fatal("an IPv6 address accepted for an A record")
	}
	run("ddns.set", map[string]any{"integration": "noip", "name": "home.ddns.net", "type": "A", "source": "fixed:203.0.113.9"})
	mu.Lock()
	if len(calls) != 1 || !strings.Contains(calls[0], "/nic/update?hostname=home.ddns.net&myip=203.0.113.9 me:pw") {
		t.Fatalf("calls: %v", calls)
	}
	mu.Unlock()
	list, _ := c.app.ddnsList()
	if len(list) != 1 || list[0].LastIP != "203.0.113.9" || list[0].Source != "fixed:203.0.113.9" || list[0].LastError != "" {
		t.Fatalf("%+v", list)
	}
	c.app.ddnsTick(context.Background())
	mu.Lock()
	if len(calls) != 1 {
		t.Fatalf("unchanged address sent again: %v", calls)
	}
	mu.Unlock()
	// DDNS-only accounts never reach Caddy's certificate settings
	provs, _ := c.app.dnsProvidersForCaddy()
	if _, ok := provs["noip"]; ok {
		t.Fatal("a DDNS-only account offered for certificates")
	}
}
