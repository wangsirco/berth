package panel

import (
	"testing"
)

func TestObjFromArgs(t *testing.T) {
	cases := []struct {
		tool    string
		args    map[string]any
		typ, id string
	}{
		{"compose_up", map[string]any{"name": "n8n"}, "compose_project", "n8n"},
		{"get_container", map[string]any{"name": "web"}, "container", "web"},
		{"tail_logs", map[string]any{"container": "web", "lines": 50.0}, "container", "web"},
		{"native_deploy", map[string]any{"project": "acme"}, "project", "acme"},
		{"site_update", map[string]any{"site_id": 12.0}, "site", "12"},
		{"read_file", map[string]any{"path": "/etc/hosts"}, "file", "/etc/hosts"},
		{"list_hosts", map[string]any{}, "host", ""},
		{"list_changes", map[string]any{}, "", ""},
	}
	for _, c := range cases {
		typ, id := objFromArgs(c.tool, c.args)
		if typ != c.typ || id != c.id {
			t.Errorf("%s %v: got %q %q, want %q %q", c.tool, c.args, typ, id, c.typ, c.id)
		}
	}
}

func TestMCPActivityRecorded(t *testing.T) {
	c := newTC(t)
	c.withFakeAgent()
	u := c.mkUser("iwsheng", "correct horse 9", "super")
	tok, _ := c.app.issueToken(u, tokenSpec{Kind: "agt", Name: "claude", Scopes: []string{"containers:read", "containers:write"}, DangerPolicy: "run"})
	mcpCall(c, tok, "tools/call", map[string]any{"name": "get_container", "arguments": map[string]any{"name": "n8n"}})
	mcpCall(c, tok, "tools/call", map[string]any{"name": "container_restart", "arguments": map[string]any{"name": "n8n"}})

	_, out := c.loginAndGet("iwsheng", "correct horse 9", "/api/v1/tokens/activity")
	items := out["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("%v", out)
	}
	x := items[0].(map[string]any)
	if x["name"] != "claude" || x["working"] != true || x["count_24h"].(float64) != 2 {
		t.Fatalf("%v", x)
	}
	recent := x["recent"].([]any)
	last := recent[0].(map[string]any)
	if last["method"] != "container_restart" || last["kind"] != "container.restart" || last["obj_type"] != "container" || last["change_id"] == "" {
		t.Fatalf("%v", last)
	}
	_, obj := c.loginAndGet("iwsheng", "correct horse 9", "/api/v1/hosts/local/activity?type=container&id="+last["obj_id"].(string))
	if n := len(obj["items"].([]any)); n < 1 {
		t.Fatalf("object activity: %v", obj)
	}
}
