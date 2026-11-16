package panel

import (
	"net/http"
	"strings"
	"testing"
)

func TestTerminalPermissionsAndAudit(t *testing.T) {
	c := newTC(t)
	fa := c.withFakeAgent()
	ops := c.mkUser("ops", "correct horse 9", "admin")
	c.login("ops", "correct horse 9")
	if r, _ := c.do("POST", "/api/v1/hosts/local/terminals", map[string]any{"target": "host"}); r.StatusCode != 403 {
		t.Fatalf("no perm: %d", r.StatusCode)
	}
	ops.Perms = []string{"terminal.use"}
	c.app.saveUser(ops)
	r, out := c.do("POST", "/api/v1/hosts/local/terminals", map[string]any{"target": "host"})
	if r.StatusCode != 409 || out["error"].(map[string]any)["code"] != "terminal.no_default_user" {
		t.Fatalf("admin without default user: %d %v", r.StatusCode, out)
	}
	c.app.setSetting("terminal.default_user", "iwsheng")
	if r, out := c.do("POST", "/api/v1/hosts/local/terminals", map[string]any{"target": "host"}); r.StatusCode != 200 || out["id"] != "pty_1" {
		t.Fatalf("%d %v", r.StatusCode, out)
	}
	if !fa.called("POST /v1/pty user=iwsheng") {
		t.Fatal("admin must not get root")
	}
	var risk string
	c.app.db.QueryRow(`SELECT risk FROM changes WHERE kind='terminal.open'`).Scan(&risk)
	if risk != "high" {
		t.Fatalf("audit risk %q", risk)
	}
}

func TestTerminalWebSocketRejectsForeignOrigin(t *testing.T) {
	c := newTC(t)
	c.withFakeAgent()
	c.mkUser("iwsheng", "correct horse 9", "super")
	c.login("iwsheng", "correct horse 9")
	c.do("POST", "/api/v1/hosts/local/terminals", map[string]any{"target": "host"})
	req, _ := http.NewRequest("GET", c.srv.URL+"/api/v1/hosts/local/terminals/pty_1/ws", nil)
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Sec-WebSocket-Version", "13")
	req.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
	req.Header.Set("Origin", "https://evil.example")
	resp, err := c.hc.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Fatalf("foreign origin got %d", resp.StatusCode)
	}
	_ = strings.TrimSpace
}
