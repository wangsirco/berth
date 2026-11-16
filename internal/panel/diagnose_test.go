package panel

import (
	"context"
	"testing"
	"time"
)

func layerOf(d diagnosis, name string) diagLayer {
	for _, l := range d.Layers {
		if l.Layer == name {
			return l
		}
	}
	return diagLayer{}
}

func TestDiagnoseLayers(t *testing.T) {
	c := newTC(t)
	fa := c.withFakeAgent()
	c.mkUser("root", "correct horse 9", "super")
	c.login("root", "correct horse 9")
	fa.mu.Lock()
	fa.publicIP = "203.0.113.5"
	fa.containers[0]["networks"] = []string{"n8n_default", "berth-edge"}
	fa.mu.Unlock()
	_, out := c.do("POST", "/api/v1/hosts/local/changes/site.create", map[string]any{"domains": []string{"n8n.example.com"}, "target": map[string]any{"type": "container", "container": "n8n", "port": 5678}})
	if out["status"] != "done" {
		t.Fatal(out)
	}
	s, _ := c.app.site("local", 1)
	run := func() diagnosis {
		d := c.app.diagnose(context.Background(), "local", s)
		c.app.translateDiag("zh-CN", &d)
		return d
	}
	set := func(f func(d *fakeDiag)) {
		fa.mu.Lock()
		f(&fa.diag)
		fa.mu.Unlock()
	}

	// healthy except DNS, which has no record yet
	d := run()
	if l := layerOf(d, "dns"); l.Code != "diag.dns_missing" || l.Status != "fail" {
		t.Fatalf("dns: %+v", l)
	}
	set(func(d *fakeDiag) { d.dns["n8n.example.com"] = []string{"203.0.113.5"} })
	d = run()
	if d.Status != "ok" {
		t.Fatalf("healthy site: %s %+v", d.Status, d.Layers)
	}
	if l := layerOf(d, "request"); l.Message == "" || l.Code != "diag.request_ok" {
		t.Fatalf("request: %+v", l)
	}

	// DNS points elsewhere
	set(func(d *fakeDiag) { d.dns["n8n.example.com"] = []string{"198.51.100.99"} })
	if l := layerOf(run(), "dns"); l.Code != "diag.dns_elsewhere" {
		t.Fatalf("elsewhere: %+v", l)
	}
	// behind Cloudflare is fine
	set(func(d *fakeDiag) { d.dns["n8n.example.com"] = []string{"104.16.1.1"} })
	if l := layerOf(run(), "dns"); l.Code != "diag.dns_cloudflare" || l.Status != "ok" {
		t.Fatalf("cloudflare: %+v", l)
	}

	// container stopped: fix = start it
	fa.mu.Lock()
	fa.containers[0]["state"] = "exited"
	fa.mu.Unlock()
	l := layerOf(run(), "upstream")
	if l.Code != "diag.upstream_stopped" || l.Fix == nil || l.Fix.Kind != "container.start" {
		t.Fatalf("stopped: %+v", l)
	}
	fa.mu.Lock()
	fa.containers[0]["state"] = "running"
	fa.containers[0]["networks"] = []string{"n8n_default"}
	fa.mu.Unlock()
	if l := layerOf(run(), "upstream"); l.Code != "diag.upstream_not_on_edge" || l.Fix.Kind != "edge.connect" {
		t.Fatalf("not on edge: %+v", l)
	}
	fa.mu.Lock()
	fa.containers[0]["networks"] = []string{"n8n_default", "berth-edge"}
	fa.mu.Unlock()

	// wrong port, loopback-only port
	set(func(d *fakeDiag) { d.listen = []map[string]any{{"port": 8080, "proto": "tcp", "loopback": false}} })
	if l := layerOf(run(), "upstream"); l.Code != "diag.upstream_port" || l.Params["ports"] != "8080" {
		t.Fatalf("port: %+v", l)
	}
	set(func(d *fakeDiag) { d.listen = []map[string]any{{"port": 5678, "proto": "tcp", "loopback": true}} })
	if l := layerOf(run(), "upstream"); l.Code != "diag.upstream_loopback" {
		t.Fatalf("loopback: %+v", l)
	}
	set(func(d *fakeDiag) { d.listen = []map[string]any{{"port": 5678, "proto": "tcp"}}; d.upstream = 0 })
	if l := layerOf(run(), "upstream"); l.Code != "diag.upstream_unreachable" {
		t.Fatalf("unreachable: %+v", l)
	}
	set(func(d *fakeDiag) { d.upstream = 200 })

	// expired certificate, 502 from Caddy
	set(func(d *fakeDiag) {
		d.https = map[string]any{"status": 502, "cert": map[string]any{"issuer": "R11", "not_after": time.Now().Add(-time.Hour), "verified": false}}
	})
	d = run()
	if l := layerOf(d, "cert"); l.Code != "diag.cert_expired" {
		t.Fatalf("expired: %+v", l)
	}
	if l := layerOf(d, "request"); l.Code != "diag.request_gateway" || d.Status != "fail" {
		t.Fatalf("502: %+v", l)
	}

	// OOM
	set(func(d *fakeDiag) { d.inspect["oom_killed"] = true })
	if l := layerOf(run(), "app"); l.Code != "diag.app_oom" || l.Fix.Kind != "container.restart" {
		t.Fatalf("oom: %+v", l)
	}

	// the API and the MCP tool give the same thing
	_, api := c.do("POST", "/api/v1/hosts/local/sites/n8n.example.com/diagnose", nil)
	if api["status"] != "fail" || len(api["layers"].([]any)) != 7 {
		t.Fatalf("api: %v", api)
	}
}
