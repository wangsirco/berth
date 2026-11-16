package panel

import "testing"

func TestLoginMeLogout(t *testing.T) {
	c := newTC(t)
	c.mkUser("iwsheng", "correct horse 9", "super")
	if r, _ := c.do("GET", "/api/v1/me", nil); r.StatusCode != 401 {
		t.Fatalf("anon me = %d", r.StatusCode)
	}
	c.login("iwsheng", "correct horse 9")
	r, me := c.do("GET", "/api/v1/me", nil)
	if r.StatusCode != 200 || me["username"] != "iwsheng" {
		t.Fatalf("%d %v", r.StatusCode, me)
	}
	if r, _ := c.do("POST", "/api/v1/auth/logout", nil); r.StatusCode != 200 {
		t.Fatal("logout")
	}
	if r, _ := c.do("GET", "/api/v1/me", nil); r.StatusCode != 401 {
		t.Fatal("still logged in")
	}
}

func TestCSRFRequired(t *testing.T) {
	c := newTC(t)
	c.mkUser("iwsheng", "correct horse 9", "super")
	c.login("iwsheng", "correct horse 9")
	c.csrf = ""
	if r, _ := c.do("POST", "/api/v1/auth/logout", nil); r.StatusCode != 403 {
		t.Fatalf("want 403, got %d", r.StatusCode)
	}
}

func TestLoginRateLimit(t *testing.T) {
	c := newTC(t)
	c.mkUser("iwsheng", "correct horse 9", "super")
	for i := 0; i < 10; i++ {
		c.do("POST", "/api/v1/auth/login", map[string]string{"username": "iwsheng", "password": "wrong"})
	}
	r, out := c.do("POST", "/api/v1/auth/login", map[string]string{"username": "iwsheng", "password": "correct horse 9"})
	if r.StatusCode != 429 {
		t.Fatalf("want 429, got %d %v", r.StatusCode, out)
	}
	if out["error"].(map[string]any)["code"] != "auth.rate_limited" {
		t.Fatal(out)
	}
}

func TestErrorTranslated(t *testing.T) {
	c := newTC(t)
	c.mkUser("iwsheng", "correct horse 9", "super")
	_, out := c.do("POST", "/api/v1/auth/login", map[string]string{"username": "iwsheng", "password": "x"})
	msg := out["error"].(map[string]any)["message"].(string)
	if msg == "" || msg == "auth.bad_credentials" {
		t.Fatalf("not translated: %q", msg)
	}
}

func TestOutsideAllowedNetworkIs404(t *testing.T) {
	c := newTC(t)
	c.app.cfg.AllowCIDRs = []string{"10.0.0.0/8"}
	if r, _ := c.do("GET", "/api/v1/me", nil); r.StatusCode != 404 {
		t.Fatalf("want 404, got %d", r.StatusCode)
	}
}
