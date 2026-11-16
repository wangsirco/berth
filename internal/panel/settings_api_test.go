package panel

import "testing"

func TestSettingsRefuseLockOutAndUndo(t *testing.T) {
	c := newTC(t)
	c.mkUser("iwsheng", "correct horse 9", "super")
	c.login("iwsheng", "correct horse 9")
	r, out := c.do("PUT", "/api/v1/settings", map[string]any{"values": map[string]string{"access.allow_cidrs": "10.0.0.0/8"}})
	if r.StatusCode != 400 || out["error"].(map[string]any)["code"] != "settings.would_lock_out" {
		t.Fatalf("%d %v", r.StatusCode, out)
	}
	_, out = c.do("PUT", "/api/v1/settings", map[string]any{"values": map[string]string{"panel.name": "mini 面板", "access.entry": "/b-7f3k2q"}})
	if out["status"] != "done" || c.app.setting("panel.name") != "mini 面板" {
		t.Fatalf("%v", out)
	}
	if out["plan"].(map[string]any)["risk"] != "high" {
		t.Fatal("access changes are high risk")
	}
	c.do("POST", "/api/v1/changes/"+out["id"].(string)+"/revert", nil)
	if c.app.setting("panel.name") != "" || c.app.setting("access.entry") != "" {
		t.Fatal("undo did not restore")
	}
	if r, _ := c.do("PUT", "/api/v1/settings", map[string]any{"values": map[string]string{"access.entry": "/api-x"}}); r.StatusCode != 400 {
		t.Fatal("bad entry accepted")
	}
}

func TestSettingEntryKeepsThisBrowserIn(t *testing.T) {
	c := newTC(t)
	c.mkUser("iwsheng", "correct horse 9", "super")
	c.login("iwsheng", "correct horse 9")
	c.do("PUT", "/api/v1/settings", map[string]any{"values": map[string]string{"access.entry": "/b-7f3k2q"}})
	c.do("POST", "/api/v1/auth/logout", nil)
	if r, _ := c.do("POST", "/api/v1/auth/login", map[string]string{"username": "iwsheng", "password": "correct horse 9"}); r.StatusCode != 200 {
		t.Fatalf("locked out after setting the entry: %d", r.StatusCode)
	}
}
