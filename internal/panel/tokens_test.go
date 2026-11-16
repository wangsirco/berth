package panel

import (
	"net/http"
	"strings"
	"testing"
)

func bearer(c *tc, tok, method, path string) *http.Response {
	req, _ := http.NewRequest(method, c.srv.URL+path, strings.NewReader(`{"key":"x","value":"1"}`))
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := c.srv.Client().Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	resp.Body.Close()
	return resp
}

func TestPATScopeAndRevoke(t *testing.T) {
	c := newTC(t)
	u := c.mkUser("iwsheng", "correct horse 9", "super")
	tok, id := c.app.issueToken(u, tokenSpec{Kind: "pat", Name: "ci", Scopes: []string{"audit:read"}})
	if !strings.HasPrefix(tok, "berth_pat_") {
		t.Fatal(tok)
	}
	if r := bearer(c, tok, "GET", "/api/v1/changes"); r.StatusCode != 200 {
		t.Fatalf("read %d", r.StatusCode)
	}
	if r := bearer(c, tok, "GET", "/api/v1/tokens"); r.StatusCode != 403 {
		t.Fatalf("out of scope %d", r.StatusCode)
	}
	c.app.revokeToken(id)
	if r := bearer(c, tok, "GET", "/api/v1/changes"); r.StatusCode != 401 {
		t.Fatalf("revoked %d", r.StatusCode)
	}
}

func TestTokenDiesWithDisabledUser(t *testing.T) {
	c := newTC(t)
	u := c.mkUser("ops", "correct horse 9", "admin")
	tok, _ := c.app.issueToken(u, tokenSpec{Kind: "pat", Name: "x", Scopes: []string{"audit:read"}})
	u.Disabled = true
	c.app.saveUser(u)
	if r := bearer(c, tok, "GET", "/api/v1/changes"); r.StatusCode != 401 {
		t.Fatalf("%d", r.StatusCode)
	}
}

func TestExpiredAndIPAllow(t *testing.T) {
	c := newTC(t)
	u := c.mkUser("iwsheng", "correct horse 9", "super")
	tok, _ := c.app.issueToken(u, tokenSpec{Kind: "pat", Name: "x", Scopes: []string{"audit:read"}, ExpiresAt: 1})
	if r := bearer(c, tok, "GET", "/api/v1/changes"); r.StatusCode != 401 {
		t.Fatalf("expired %d", r.StatusCode)
	}
	tok2, _ := c.app.issueToken(u, tokenSpec{Kind: "pat", Name: "y", Scopes: []string{"audit:read"}, IPAllow: []string{"10.9.9.0/24"}})
	if r := bearer(c, tok2, "GET", "/api/v1/changes"); r.StatusCode != 403 {
		t.Fatalf("ip %d", r.StatusCode)
	}
}

func TestAgentTokenConfirmPolicy(t *testing.T) {
	c := newTC(t)
	u := c.mkUser("iwsheng", "correct horse 9", "super")
	g := tokenGate{app: c.app}
	actor := c.app.actorForToken(u, &tokenRow{Kind: "agt", DangerPolicy: "confirm"})
	need, err := g.check(actor, "high")
	if err != nil || !need {
		t.Fatalf("high must need confirm: %v %v", need, err)
	}
	need, _ = g.check(actor, "low")
	if need {
		t.Fatal("low runs directly")
	}
	ro := c.app.actorForToken(u, &tokenRow{Kind: "agt", DangerPolicy: "readonly"})
	if _, err := g.check(ro, "low"); err == nil {
		t.Fatal("readonly must refuse writes")
	}
}

func TestCannotRevokeOthersTokens(t *testing.T) {
	c := newTC(t)
	boss := c.mkUser("iwsheng", "correct horse 9", "super")
	c.mkUser("ops", "correct horse 9", "admin")
	_, id := c.app.issueToken(boss, tokenSpec{Kind: "pat", Name: "boss", Scopes: []string{"audit:read"}})
	c.login("ops", "correct horse 9")
	if r, _ := c.do("DELETE", "/api/v1/tokens/"+itoa(id), nil); r.StatusCode != 404 {
		t.Fatalf("own-token route: %d", r.StatusCode)
	}
	if r, _ := c.do("POST", "/api/v1/hosts/local/changes/token.revoke", map[string]any{"id": id}); r.StatusCode != 403 {
		t.Fatalf("generic route: %d", r.StatusCode)
	}
}

func TestOwnerRevokesOwnToken(t *testing.T) {
	c := newTC(t)
	ops := c.mkUser("ops", "correct horse 9", "admin")
	_, id := c.app.issueToken(ops, tokenSpec{Kind: "pat", Name: "mine", Scopes: []string{"overview:read"}})
	c.login("ops", "correct horse 9")
	if r, out := c.do("DELETE", "/api/v1/tokens/"+itoa(id), nil); r.StatusCode != 200 || out["status"] != "done" {
		t.Fatalf("%d %v", r.StatusCode, out)
	}
}
