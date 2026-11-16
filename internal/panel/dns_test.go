package panel

import (
	"context"
	"strings"
	"testing"

	"github.com/wangsirco/berth/internal/panel/dns/dnstest"
)

func withCloudflare(t *testing.T, zones ...string) *dnstest.Fake {
	f := dnstest.New("cf-good-token-1234", zones...)
	t.Cleanup(f.Close)
	old := cloudflareBase
	cloudflareBase = f.URL
	t.Cleanup(func() { cloudflareBase = old })
	return f
}

func (c *tc) addCF(token string) (int, map[string]any) {
	r, out := c.do("POST", "/api/v1/hosts/local/changes/integration.set", map[string]any{"id": "cf", "kind": "cloudflare", "name": "Main", "credentials": map[string]string{"api_token": token}})
	return r.StatusCode, out
}

func TestDNSAccountAndRecords(t *testing.T) {
	c := newTC(t)
	c.withFakeAgent()
	cf := withCloudflare(t, "example.com")
	c.mkUser("root", "correct horse 9", "super")
	c.login("root", "correct horse 9")

	if st, out := c.addCF("wrong"); st != 403 || !strings.Contains(toJSON(out), "dns.auth") {
		t.Fatalf("bad token: %d %v", st, out)
	}
	if _, out := c.addCF("cf-good-token-1234"); out["status"] != "done" {
		t.Fatalf("add: %v", out)
	}
	_, list := c.do("GET", "/api/v1/integrations", nil)
	js := toJSON(list)
	if !strings.Contains(js, `"zones_read":true`) || !strings.Contains(js, `"hint":"…1234"`) || strings.Contains(js, "cf-good-token") {
		t.Fatalf("list: %s", js)
	}
	_, audit := c.do("GET", "/api/v1/changes?limit=10", nil)
	if strings.Contains(toJSON(audit), "cf-good-token") {
		t.Fatal("token in the audit log")
	}
	_, zones := c.do("GET", "/api/v1/integrations/cf/zones", nil)
	if !strings.Contains(toJSON(zones), `"id":"z1"`) {
		t.Fatalf("zones: %v", zones)
	}

	rec := map[string]any{"type": "A", "name": "www.example.com", "content": "203.0.113.5", "proxied": true}
	_, out := c.do("POST", "/api/v1/hosts/local/changes/dns.record.create", map[string]any{"integration": "cf", "zone_id": "z1", "record": rec})
	if out["status"] != "done" || len(cf.Records("z1")) != 1 {
		t.Fatalf("create: %v", out)
	}
	createID := out["id"].(string)
	id := cf.Records("z1")[0]["id"]
	_, out = c.do("POST", "/api/v1/hosts/local/changes/dns.record.update", map[string]any{"integration": "cf", "zone_id": "z1",
		"record": map[string]any{"id": id, "type": "A", "name": "www.example.com", "content": "203.0.113.9"}})
	if out["status"] != "done" || cf.Records("z1")[0]["content"] != "203.0.113.9" {
		t.Fatalf("update: %v", out)
	}
	c.do("POST", "/api/v1/changes/"+out["id"].(string)+"/revert", nil)
	if cf.Records("z1")[0]["content"] != "203.0.113.5" {
		t.Fatal("undo of update")
	}
	c.do("POST", "/api/v1/changes/"+createID+"/revert", nil)
	if len(cf.Records("z1")) != 0 {
		t.Fatal("undo of create")
	}

	// a token without DNS:Edit
	cf.ReadOnly = true
	r, out := c.do("POST", "/api/v1/hosts/local/changes/dns.record.create", map[string]any{"integration": "cf", "zone_id": "z1", "record": rec})
	if r.StatusCode != 403 || !strings.Contains(toJSON(out), "dns.forbidden") {
		t.Fatalf("read-only: %d %v", r.StatusCode, out)
	}
	cf.ReadOnly = false

	// in use: cannot delete the account
	c.do("POST", "/api/v1/hosts/local/changes/site.create", map[string]any{"domains": []string{"*.example.com"}, "target": map[string]any{"type": "static"}, "tls": map[string]any{"mode": "dns", "dns": "cf"}})
	if r, _ := c.do("POST", "/api/v1/hosts/local/changes/integration.delete", map[string]any{"id": "cf"}); r.StatusCode != 409 {
		t.Fatalf("delete in use: %d", r.StatusCode)
	}
}

func TestSiteWithDNSAndDDNS(t *testing.T) {
	c := newTC(t)
	fa := c.withFakeAgent()
	cf := withCloudflare(t, "example.com")
	c.mkUser("root", "correct horse 9", "super")
	c.login("root", "correct horse 9")
	c.addCF("cf-good-token-1234")

	// a wildcard site with a DNS-01 certificate: Caddy gets the provider config
	_, out := c.do("POST", "/api/v1/hosts/local/changes/site.create?plan=1", map[string]any{"domains": []string{"shop.example.com"}, "target": map[string]any{"type": "static"},
		"tls": map[string]any{"mode": "dns", "dns": "cf"}, "dns": map[string]any{"integration": "cf", "zone_id": "z1", "type": "A", "content": "203.0.113.5", "proxied": true}})
	if !strings.Contains(toJSON(out), "DNS A shop.example.com → 203.0.113.5 (proxied)") {
		t.Fatalf("plan: %v", out)
	}
	_, out = c.do("POST", "/api/v1/hosts/local/changes/site.create", map[string]any{"domains": []string{"shop.example.com"}, "target": map[string]any{"type": "static"},
		"tls": map[string]any{"mode": "dns", "dns": "cf"}, "dns": map[string]any{"integration": "cf", "zone_id": "z1", "type": "A", "content": "203.0.113.5", "proxied": true}})
	if out["status"] != "done" || len(cf.Records("z1")) != 1 || !strings.Contains(fa.config(), `"api_token":"cf-good-token-1234"`) {
		t.Fatalf("create: %v", out)
	}
	_, out = c.do("POST", "/api/v1/hosts/local/changes/site.delete", map[string]any{"id": 1, "with_dns": true})
	if out["status"] != "done" || len(cf.Records("z1")) != 0 {
		t.Fatalf("delete with dns: %v %v", out, cf.Records("z1"))
	}
	c.do("POST", "/api/v1/changes/"+out["id"].(string)+"/revert", nil)
	if s, _ := c.app.sites("local"); len(s) != 1 || len(cf.Records("z1")) != 1 {
		t.Fatalf("undo delete: %v %v", s, cf.Records("z1"))
	}

	// DDNS follows the host's public address
	fa.mu.Lock()
	fa.publicIP = "198.51.100.10"
	fa.mu.Unlock()
	_, out = c.do("POST", "/api/v1/hosts/local/changes/ddns.set", map[string]any{"integration": "cf", "zone_id": "z1", "name": "home.example.com", "type": "A"})
	if out["status"] != "done" {
		t.Fatalf("ddns: %v", out)
	}
	find := func() string {
		for _, r := range cf.Records("z1") {
			if r["name"] == "home.example.com" {
				return r["content"].(string)
			}
		}
		return ""
	}
	if find() != "198.51.100.10" {
		t.Fatalf("first update: %v", cf.Records("z1"))
	}
	fa.mu.Lock()
	fa.publicIP = "198.51.100.11"
	fa.mu.Unlock()
	c.app.ddnsTick(context.Background())
	if find() != "198.51.100.11" {
		t.Fatal("not updated after the address changed")
	}
	_, audit := c.do("GET", "/api/v1/changes?limit=50", nil)
	if !strings.Contains(toJSON(audit), `"kind":"ddns.update"`) {
		t.Fatal("ddns update not recorded")
	}
}
