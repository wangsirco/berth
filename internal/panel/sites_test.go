package panel

import (
	"context"
	"strings"
	"testing"
)

func (f *fakeAgent) config() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.edgeConfig
}

func TestSiteCreatePreviewRunRevert(t *testing.T) {
	c := newTC(t)
	fa := c.withFakeAgent()
	c.mkUser("root", "correct horse 9", "super")
	c.login("root", "correct horse 9")
	site := map[string]any{"domains": []string{"N8N.example.com"}, "target": map[string]any{"type": "container", "container": "n8n", "port": 5678},
		"options": map[string]any{"hsts": true}}

	_, out := c.do("POST", "/api/v1/hosts/local/changes/site.create?plan=1", site)
	plan, _ := out["plan"].(map[string]any)
	if plan == nil {
		t.Fatalf("plan: %v", out)
	}
	files := toJSON(plan["files"])
	if !strings.Contains(files, `n8n:5678`) || !strings.Contains(files, "/srv/berth/caddy/config/config.json") {
		t.Fatalf("diff: %s", files)
	}
	if !strings.Contains(toJSON(plan["commands"]), "docker network connect berth-edge n8n") {
		t.Fatalf("commands: %v", plan["commands"])
	}
	if !fa.called("validate") || fa.called("load") {
		t.Fatal("preview must validate and must not load")
	}

	_, out = c.do("POST", "/api/v1/hosts/local/changes/site.create", site)
	if out["status"] != "done" {
		t.Fatalf("run: %v", out)
	}
	if !strings.Contains(fa.config(), `"n8n.example.com"`) || !fa.called(`connect {"container":"n8n"}`) {
		t.Fatalf("loaded: %s", fa.config())
	}
	_, list := c.do("GET", "/api/v1/hosts/local/sites", nil)
	if !strings.Contains(toJSON(list), `"domains":["n8n.example.com"]`) {
		t.Fatalf("list: %v", list)
	}
	// the site is related to its container
	if rels := c.app.related("local", "site", "1"); len(rels) != 1 || rels[0].To.ID != "n8n" {
		t.Fatalf("relations: %v", rels)
	}

	// the same domain cannot be used twice
	if r, out := c.do("POST", "/api/v1/hosts/local/changes/site.create", site); r.StatusCode != 409 {
		t.Fatalf("duplicate: %d %v", r.StatusCode, out)
	}

	// undo removes it from the database and from Caddy
	id := out["id"].(string)
	if r, _ := c.do("POST", "/api/v1/changes/"+id+"/revert", nil); r.StatusCode != 200 {
		t.Fatalf("revert %d", r.StatusCode)
	}
	if strings.Contains(fa.config(), "n8n.example.com") {
		t.Fatal("still in Caddy after revert")
	}
	if s, _ := c.app.sites("local"); len(s) != 0 {
		t.Fatalf("still in db: %v", s)
	}
}

func TestSiteUpdateToggleDeleteRestore(t *testing.T) {
	c := newTC(t)
	fa := c.withFakeAgent()
	c.mkUser("root", "correct horse 9", "super")
	c.login("root", "correct horse 9")
	_, out := c.do("POST", "/api/v1/hosts/local/changes/site.create", map[string]any{"domains": []string{"a.example.com"}, "target": map[string]any{"type": "static"}})
	if out["status"] != "done" {
		t.Fatal(out)
	}
	_, out = c.do("POST", "/api/v1/hosts/local/changes/site.update", map[string]any{"id": 1, "domains": []string{"a.example.com", "b.example.com"}, "target": map[string]any{"type": "static", "spa": true}})
	if out["status"] != "done" || !strings.Contains(fa.config(), "b.example.com") {
		t.Fatalf("update: %v", out)
	}
	_, out = c.do("POST", "/api/v1/hosts/local/changes/site.toggle", map[string]any{"id": 1, "enabled": false})
	// a stopped site stays on the edge, but only says it is paused
	if out["status"] != "done" || !strings.Contains(fa.config(), "site stopped") || strings.Contains(fa.config(), "\"file_server\"") {
		t.Fatalf("disable: %v", out)
	}
	c.do("POST", "/api/v1/changes/"+out["id"].(string)+"/revert", nil)
	if !strings.Contains(fa.config(), "a.example.com") {
		t.Fatal("re-enable by undo")
	}
	_, out = c.do("POST", "/api/v1/hosts/local/changes/site.delete", map[string]any{"id": 1})
	if out["status"] != "done" || strings.Contains(fa.config(), "a.example.com") {
		t.Fatalf("delete: %v", out)
	}
	c.do("POST", "/api/v1/changes/"+out["id"].(string)+"/revert", nil)
	s, _ := c.app.sites("local")
	if len(s) != 1 || s[0].ID != 1 || len(s[0].Domains) != 2 || !s[0].Target.SPA {
		t.Fatalf("restore: %+v", s)
	}

	// Caddy rejecting the config stops the change at preview
	r, out := c.do("POST", "/api/v1/hosts/local/changes/site.create", map[string]any{"domains": []string{"rejected.example.com"}, "target": map[string]any{"type": "static"}})
	if r.StatusCode != 400 || !strings.Contains(toJSON(out), "edge.config_rejected") {
		t.Fatalf("rejected: %d %v", r.StatusCode, out)
	}
	// bad input is a translated validation error
	r, out = c.do("POST", "/api/v1/hosts/local/changes/site.create", map[string]any{"domains": []string{"*.wild.com"}, "target": map[string]any{"type": "static"}})
	if r.StatusCode != 400 || !strings.Contains(toJSON(out), "site.wildcard_needs_dns") {
		t.Fatalf("wildcard: %d %v", r.StatusCode, out)
	}
}

func TestEnsureEdgeReconnects(t *testing.T) {
	c := newTC(t)
	fa := c.withFakeAgent()
	c.mkUser("root", "correct horse 9", "super")
	c.login("root", "correct horse 9")
	c.do("POST", "/api/v1/hosts/local/changes/site.create", map[string]any{"domains": []string{"n8n.example.com"}, "target": map[string]any{"type": "container", "container": "n8n", "port": 5678}})
	fa.mu.Lock()
	fa.calls = nil
	fa.containers[0]["networks"] = []string{"n8n_default"} // recreated by compose: berth-edge is gone
	fa.mu.Unlock()
	c.app.ensureEdge(context.Background(), "local")
	if !fa.called(`connect {"container":"n8n"}`) {
		t.Fatal("not reconnected")
	}
	fa.mu.Lock()
	fa.calls = nil
	fa.containers[0]["networks"] = []string{"n8n_default", "berth-edge"}
	fa.mu.Unlock()
	c.app.ensureEdge(context.Background(), "local")
	if fa.called(`connect {"container":"n8n"}`) {
		t.Fatal("connected again although already on the network")
	}
}
