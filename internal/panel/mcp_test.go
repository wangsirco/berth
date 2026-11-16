package panel

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func mcpCall(c *tc, tok, method string, params any) map[string]any {
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	req, _ := http.NewRequest("POST", c.srv.URL+"/mcp", strings.NewReader(string(body)))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	resp, err := c.srv.Client().Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer resp.Body.Close()
	out := map[string]any{}
	json.NewDecoder(resp.Body).Decode(&out)
	return out
}

func TestMCPListAndCallRestart(t *testing.T) {
	c := newTC(t)
	fa := c.withFakeAgent() // 来自 containers_test.go 的辅助：返回记录调用的假 agent
	u := c.mkUser("iwsheng", "correct horse 9", "super")
	tok, _ := c.app.issueToken(u, tokenSpec{Kind: "agt", Name: "claude", Scopes: []string{"containers:read", "containers:write"}, DangerPolicy: "confirm"})

	init := mcpCall(c, tok, "initialize", map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "t", "version": "1"}})
	if init["result"] == nil {
		t.Fatal(init)
	}
	list := mcpCall(c, tok, "tools/list", map[string]any{})
	names := map[string]bool{}
	for _, tl := range list["result"].(map[string]any)["tools"].([]any) {
		names[tl.(map[string]any)["name"].(string)] = true
	}
	if !names["list_containers"] || !names["container_restart"] || names["user_create"] {
		t.Fatalf("tools: %v", names)
	}
	res := mcpCall(c, tok, "tools/call", map[string]any{"name": "container_restart", "arguments": map[string]any{"name": "n8n"}})
	if res["result"].(map[string]any)["isError"] == true {
		t.Fatal(res)
	}
	if !fa.called("POST /v1/docker/containers/abc123def456/restart") {
		t.Fatal("agent not called")
	}
	_, out := c.loginAndGet("iwsheng", "correct horse 9", "/api/v1/changes")
	first := out["items"].([]any)[0].(map[string]any)
	actor := first["actor"].(map[string]any)
	if first["kind"] != "container.restart" || actor["via"] != "mcp" || actor["name"] != "claude" {
		t.Fatalf("%v", first)
	}
}

func TestMCPDestructiveNeedsConfirm(t *testing.T) {
	c := newTC(t)
	fa := c.withFakeAgent()
	u := c.mkUser("iwsheng", "correct horse 9", "super")
	tok, _ := c.app.issueToken(u, tokenSpec{Kind: "agt", Name: "claude", Scopes: []string{"containers:write", "containers:read"}, DangerPolicy: "confirm"})
	res := mcpCall(c, tok, "tools/call", map[string]any{"name": "container_remove", "arguments": map[string]any{"name": "n8n"}})
	sc := res["result"].(map[string]any)["structuredContent"].(map[string]any)
	if sc["status"] != "pending_approval" || fa.called("POST /v1/docker/containers/abc123def456/remove") {
		t.Fatalf("%v", sc)
	}
}

func TestMCPRejectsPAT(t *testing.T) {
	c := newTC(t)
	u := c.mkUser("iwsheng", "correct horse 9", "super")
	tok, _ := c.app.issueToken(u, tokenSpec{Kind: "pat", Name: "x", Scopes: []string{"containers:read"}})
	out := mcpCall(c, tok, "tools/list", map[string]any{})
	if out["error"] == nil {
		t.Fatal("PAT must not be accepted on /mcp")
	}
}
