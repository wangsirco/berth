package panel

import "testing"

func TestSetupOnceOnly(t *testing.T) {
	c := newTC(t)
	code := c.app.issueSetupCode()
	r, _ := c.do("POST", "/api/v1/setup", map[string]string{"code": code, "username": "iwsheng", "display_name": "王", "password": "correct horse 9"})
	if r.StatusCode != 200 {
		t.Fatalf("%d", r.StatusCode)
	}
	if r, _ := c.do("GET", "/api/v1/me", nil); r.StatusCode != 200 {
		t.Fatal("should be logged in")
	}
	r, _ = c.do("POST", "/api/v1/setup", map[string]string{"code": code, "username": "evil", "password": "correct horse 9"})
	if r.StatusCode != 410 {
		t.Fatalf("reuse must be 410, got %d", r.StatusCode)
	}
}

func TestEntryPathHidesPanel(t *testing.T) {
	c := newTC(t)
	c.mkUser("iwsheng", "correct horse 9", "super")
	c.app.setSetting("access.entry", "/b-7f3k2q")
	if r, _ := c.do("POST", "/api/v1/auth/login", map[string]string{"username": "iwsheng", "password": "correct horse 9"}); r.StatusCode != 404 {
		t.Fatalf("login without entry cookie must 404, got %d", r.StatusCode)
	}
	c.do("GET", "/b-7f3k2q", nil)
	if r, _ := c.do("POST", "/api/v1/auth/login", map[string]string{"username": "iwsheng", "password": "correct horse 9"}); r.StatusCode != 200 {
		t.Fatalf("after entry: %d", r.StatusCode)
	}
}
