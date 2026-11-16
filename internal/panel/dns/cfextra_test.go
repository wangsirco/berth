package dns_test

import (
	"context"
	"testing"

	"github.com/wangsirco/berth/internal/panel/dns"
	"github.com/wangsirco/berth/internal/panel/dns/dnstest"
)

func TestCloudflareExtras(t *testing.T) {
	f := dnstest.New("tok", "example.com")
	defer f.Close()
	c := dns.NewCloudflare("tok")
	c.Base = f.URL
	ctx := context.Background()
	zones, err := c.Zones(ctx)
	if err != nil || zones[0].AccountID != "acc1" {
		t.Fatal(zones, err)
	}
	z := zones[0]
	feat := c.CheckFeatures(ctx, z)
	if !(feat.DNS && feat.Tunnel && feat.Access && feat.Firewall && feat.Settings && feat.Analytics) {
		t.Fatalf("all allowed: %+v", feat)
	}
	f.Deny["tunnel"], f.Deny["analytics"] = true, true
	feat = c.CheckFeatures(ctx, z)
	if feat.Tunnel || feat.Analytics || !feat.Access {
		t.Fatalf("missing tunnel/analytics: %+v", feat)
	}
	delete(f.Deny, "tunnel")
	delete(f.Deny, "analytics")

	tun, err := c.CreateTunnel(ctx, "acc1", "berth-mini")
	if err != nil || tun.ID == "" {
		t.Fatal(tun, err)
	}
	if tok, err := c.TunnelToken(ctx, "acc1", tun.ID); err != nil || tok == "" {
		t.Fatal(tok, err)
	}
	if err := c.SetIngress(ctx, "acc1", tun.ID, []dns.Ingress{{Hostname: "shop.example.com", Service: "https://berth-caddy:443"}}); err != nil {
		t.Fatal(err)
	}
	ing := f.Ingress[tun.ID]
	if len(ing) != 2 || ing[0].(map[string]any)["hostname"] != "shop.example.com" || ing[1].(map[string]any)["service"] != "http_status:404" {
		t.Fatalf("%+v", ing)
	}

	app, err := c.CreateAccessApp(ctx, "acc1", "admin.example.com", dns.AccessPolicy{Emails: []string{"me@example.com"}, EmailDomains: []string{"example.com"}})
	if err != nil || len(f.Policies[app.ID]) != 1 {
		t.Fatal(app, err)
	}
	f.Deny["access"] = true
	if _, err := c.CreateAccessApp(ctx, "acc1", "x.example.com", dns.AccessPolicy{Emails: []string{"a@b.c"}}); err == nil {
		t.Fatal("access without permission")
	}

	id, err := c.BlockIP(ctx, "acc1", "203.0.113.0/24", "berth")
	if err != nil || f.AccessRules[id]["configuration"].(map[string]any)["target"] != "ip_range" {
		t.Fatal(id, err)
	}
	if err := c.UnblockIP(ctx, "acc1", id); err != nil || len(f.AccessRules) != 0 {
		t.Fatal(err)
	}
	if err := c.UnblockIP(ctx, "acc1", id); err != nil { // already gone is fine
		t.Fatal(err)
	}

	if lvl, _ := c.SecurityLevel(ctx, z.ID); lvl != "medium" {
		t.Fatal(lvl)
	}
	c.SetSecurityLevel(ctx, z.ID, "under_attack")
	if lvl, _ := c.SecurityLevel(ctx, z.ID); lvl != "under_attack" {
		t.Fatal(lvl)
	}
	if err := c.PurgeCache(ctx, z.ID, nil); err != nil || f.Purges[0]["purge_everything"] != true {
		t.Fatal(err, f.Purges)
	}
	days, err := c.ZoneDays(ctx, z.ID, 7)
	if err != nil || len(days) != 2 || days[1].Requests != 1500 || days[1].Threats != 7 {
		t.Fatal(days, err)
	}
}
