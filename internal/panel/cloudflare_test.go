package panel

import (
	"strings"
	"testing"
	"time"
)

func TestCloudflareTunnelAccessProtection(t *testing.T) {
	c := newTC(t)
	fa := c.withFakeAgent()
	_ = fa
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
		for i := 0; i < 50 && out["status"] != "done" && out["status"] != "failed"; i++ {
			time.Sleep(50 * time.Millisecond)
			_, out = c.do("GET", "/api/v1/changes/"+id, nil)
		}
		if out["status"] != "done" {
			t.Fatalf("%s: %v %v", kind, out["status"], out["log"])
		}
		return out
	}
	revert := func(id string) {
		t.Helper()
		if r, out := c.do("POST", "/api/v1/changes/"+id+"/revert", nil); r.StatusCode != 200 {
			t.Fatalf("revert: %v", out)
		}
		time.Sleep(200 * time.Millisecond)
	}

	// what the token can do
	_, info := c.do("GET", "/api/v1/hosts/local/cloudflare", nil)
	accts := info["accounts"].([]any)
	feat := accts[0].(map[string]any)["features"].(map[string]any)
	if feat["tunnel"] != true || feat["access"] != true {
		t.Fatalf("%v", feat)
	}

	// tunnel, and a site published through it
	run("cf.tunnel.create", map[string]any{"integration": "cf"})
	if len(cf.Tunnels) != 1 {
		t.Fatal("no tunnel at Cloudflare")
	}
	site := run("site.create", map[string]any{"domains": []string{"shop.example.com"}, "target": map[string]any{"type": "static"}, "tls": map[string]any{"mode": "internal"}})
	sid := int64(site["result"].(map[string]any)["id"].(float64))
	route := run("cf.tunnel.route", map[string]any{"site_id": sid, "on": true})
	var tid string
	for id := range cf.Tunnels {
		tid = id
	}
	recs := cf.Records("z1")
	if len(recs) != 1 || recs[0]["type"] != "CNAME" || recs[0]["content"] != tid+".cfargotunnel.com" || recs[0]["proxied"] != true {
		t.Fatalf("%v", recs)
	}
	if ing := cf.Ingress[tid]; len(ing) != 2 || ing[0].(map[string]any)["hostname"] != "shop.example.com" {
		t.Fatalf("%v", ing)
	}
	// undo takes it off again
	revert(route["id"].(string))
	if len(cf.Records("z1")) != 0 || len(cf.Ingress[tid]) != 1 {
		t.Fatalf("after undo: %v %v", cf.Records("z1"), cf.Ingress[tid])
	}
	// deleting the site takes it off the tunnel too
	run("cf.tunnel.route", map[string]any{"site_id": sid, "on": true})
	run("site.delete", map[string]any{"id": sid})
	for i := 0; i < 40 && len(cf.Records("z1")) > 0; i++ {
		time.Sleep(50 * time.Millisecond)
	}
	if len(cf.Records("z1")) != 0 {
		t.Fatal("record left after deleting the site")
	}
	run("cf.tunnel.delete", map[string]any{})
	if len(cf.Tunnels) != 0 {
		t.Fatal("tunnel left")
	}

	// under attack, and back
	am := run("cf.attack_mode", map[string]any{"integration": "cf", "zone": "z1", "zone_name": "example.com", "level": "under_attack"})
	if cf.SecurityLevel["z1"] != "under_attack" {
		t.Fatal("not under attack")
	}
	revert(am["id"].(string))
	if cf.SecurityLevel["z1"] != "medium" {
		t.Fatalf("undo: %q", cf.SecurityLevel["z1"])
	}

	// Access
	acc := run("cf.access.set", map[string]any{"integration": "cf", "domain": "admin.example.com", "emails": []string{"me@example.com"}})
	if len(cf.AccessApps) != 1 {
		t.Fatal("no Access app")
	}
	if r, _ := c.do("POST", "/api/v1/hosts/local/changes/cf.access.set?plan=1", map[string]any{"integration": "cf", "domain": "x.example.com", "emails": []string{"not-an-email"}}); r.StatusCode != 400 {
		t.Fatal("bad email accepted")
	}
	revert(acc["id"].(string))
	if len(cf.AccessApps) != 0 {
		t.Fatal("Access app left after undo")
	}

	// a ban synced to Cloudflare, withdrawn when it is lifted
	c.app.setSetting("cloudflare.ban_integration", "cf")
	b := run("ban.add", map[string]any{"addr": "203.0.113.66", "minutes": 60})
	bid := b["result"].(map[string]any)["id"].(float64)
	run("ban.sync", map[string]any{"id": bid, "cloudflare": true})
	if len(cf.AccessRules) != 1 {
		t.Fatal("not at Cloudflare")
	}
	for _, r := range cf.AccessRules {
		if !strings.Contains(r["notes"].(string), "Berth ban") {
			t.Fatal(r)
		}
	}
	run("ban.remove", map[string]any{"id": bid})
	c.app.cfBanSweep(t.Context())
	if len(cf.AccessRules) != 0 {
		t.Fatal("Cloudflare rule left after unban")
	}
}
