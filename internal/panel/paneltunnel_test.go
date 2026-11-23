package panel

import (
	"net/http/httptest"
	"testing"
	"time"
)

func TestPanelThroughTunnel(t *testing.T) {
	c := newTC(t)
	c.withFakeAgent()
	cf := withCloudflare(t, "example.com")
	c.mkUser("root", "correct horse 9", "super")
	c.login("root", "correct horse 9")
	if code, out := c.addCF("cf-good-token-1234"); code != 200 {
		t.Fatal(out)
	}
	run := func(kind string, p any) map[string]any {
		t.Helper()
		r, out := c.do("POST", "/api/v1/hosts/local/changes/"+kind, p)
		if r.StatusCode != 200 {
			t.Fatalf("%s: %d %v", kind, r.StatusCode, out)
		}
		id, _ := out["id"].(string)
		for i := 0; i < 80 && out["status"] != "done" && out["status"] != "failed"; i++ {
			time.Sleep(50 * time.Millisecond)
			_, out = c.do("GET", "/api/v1/changes/"+id, nil)
		}
		if out["status"] != "done" {
			t.Fatalf("%s: %v %v", kind, out["status"], out["log"])
		}
		return out
	}
	c.app.setSetting("access.entry", "/door")

	// one change: tunnel, record, route, public name, Access
	ch := run("cf.tunnel.panel", map[string]any{"integration": "cf", "hostname": "Panel.example.com", "on": true, "emails": []string{"me@example.com"}})
	if len(cf.Tunnels) != 1 {
		t.Fatal("no tunnel")
	}
	var tid string
	for id := range cf.Tunnels {
		tid = id
	}
	recs := cf.Records("z1")
	if len(recs) != 1 || recs[0]["name"] != "panel.example.com" || recs[0]["content"] != tid+".cfargotunnel.com" {
		t.Fatalf("%v", recs)
	}
	if ing := cf.Ingress[tid]; len(ing) != 2 || ing[0].(map[string]any)["hostname"] != "panel.example.com" {
		t.Fatalf("%v", ing)
	}
	if len(cf.AccessApps) != 1 {
		t.Fatalf("access: %v", cf.AccessApps)
	}
	if c.app.setting("panel.public_host") != "panel.example.com" || c.app.setting("access.tunnel_access") != "panel.example.com" || !c.app.exposed() {
		t.Fatal("settings")
	}
	_, chk := c.do("GET", "/api/v1/access/check", nil)
	if chk["tunnel_host"] != "panel.example.com" || chk["tunnel_entry"] != true || chk["tunnel_access"] != true {
		t.Fatalf("%v", chk)
	}
	if e := c.app.panelEntry(); e != nil && e.PublicDNS != "cf" {
		t.Fatalf("certificate by DNS challenge: %+v", e)
	}

	// who counts as coming through the tunnel
	req := func(host, peer string, edge bool) bool {
		r := httptest.NewRequest("GET", "https://"+host+"/", nil)
		r.RemoteAddr = "127.0.0.1:5000"
		r.Header.Set("X-Forwarded-For", "203.0.113.9")
		if edge {
			r.Header.Set("X-Berth-Edge", c.app.edgeSecret())
		}
		r.Header.Set("X-Berth-Peer", peer)
		return c.app.allowed(r)
	}
	if !req("panel.example.com", "172.19.0.5", true) {
		t.Fatal("tunnel visitor refused")
	}
	if req("panel.example.com", "198.51.100.4", true) || req("panel.example.com", "172.19.0.5", false) || req("other.example.com", "172.19.0.5", true) {
		t.Fatal("non-tunnel visitor allowed")
	}

	// off again by undo: record, route, Access and the names go
	if r, out := c.do("POST", "/api/v1/changes/"+ch["id"].(string)+"/revert", nil); r.StatusCode != 200 {
		t.Fatalf("revert: %v", out)
	}
	for i := 0; i < 60 && c.app.setting("access.tunnel_host") != ""; i++ {
		time.Sleep(50 * time.Millisecond)
	}
	if c.app.setting("access.tunnel_host") != "" || c.app.setting("panel.public_host") != "" || len(cf.Records("z1")) != 0 || len(cf.Ingress[tid]) != 1 {
		t.Fatalf("after undo: %v %v", cf.Records("z1"), cf.Ingress[tid])
	}
	if c.app.exposed() || len(cf.AccessApps) != 0 {
		t.Fatal("still exposed")
	}
}
