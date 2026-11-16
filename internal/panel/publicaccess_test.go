package panel

import "testing"

func TestPublicAccessNeedsEntryAndNags2FA(t *testing.T) {
	c := newTC(t)
	c.mkUser("iwsheng", "correct horse 9", "super")
	c.login("iwsheng", "correct horse 9")
	r, out := c.do("PUT", "/api/v1/settings", map[string]any{"values": map[string]string{"access.public": "1"}})
	if r.StatusCode != 400 || out["error"].(map[string]any)["code"] != "settings.public_needs_entry" {
		t.Fatalf("public without an entry: %d %v", r.StatusCode, out)
	}
	_, out = c.do("PUT", "/api/v1/settings", map[string]any{"values": map[string]string{"access.public": "1", "access.entry": "/b-7f3k2q"}})
	if out["status"] != "done" || !c.app.isPublic() {
		t.Fatalf("turning it on: %v", out)
	}
	if _, me := c.do("GET", "/api/v1/me", nil); me["needs_2fa"] != true {
		t.Fatalf("an account without a second factor must be nagged: %v", me)
	}
	// no second factor yet: the password still signs in (no lock-out of the only admin)
	c.do("POST", "/api/v1/auth/logout", nil)
	if r, out := c.do("POST", "/api/v1/auth/login", map[string]string{"username": "iwsheng", "password": "correct horse 9"}); r.StatusCode != 200 || out["stage"] == "second_step" {
		t.Fatalf("locked out: %d %v", r.StatusCode, out)
	}
	// closing it from an allowed address is fine
	if _, out := c.do("PUT", "/api/v1/settings", map[string]any{"values": map[string]string{"access.public": ""}}); out["status"] != "done" || c.app.isPublic() {
		t.Fatalf("turning it off: %v", out)
	}
}

func TestEntryGateLetsSetupThrough(t *testing.T) {
	c := newTC(t)
	c.app.setSetting("access.entry", "/b-7f3k2q")
	if r, _ := c.do("GET", "/api/v1/setup/status", nil); r.StatusCode != 200 {
		t.Fatalf("setup hidden behind the entry before anyone can sign in: %d", r.StatusCode)
	}
	c.mkUser("iwsheng", "correct horse 9", "super")
	if r, _ := c.do("GET", "/api/v1/setup/status", nil); r.StatusCode != 404 {
		t.Fatalf("entry not enforced once an admin exists: %d", r.StatusCode)
	}
}
