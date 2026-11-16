package panel

import (
	"net/http"
	"strings"
	"testing"
)

func TestInviteFlow(t *testing.T) {
	c := newTC(t)
	c.mkUser("iwsheng", "correct horse 9", "super")
	c.login("iwsheng", "correct horse 9")
	if r, _ := c.do("POST", "/api/v1/invites", map[string]any{"preset": "custom", "role": "super"}); r.StatusCode != 400 {
		t.Fatal("a super admin cannot be invited")
	}
	_, out := c.do("POST", "/api/v1/invites", map[string]any{"preset": "vms", "days": 3, "max_uses": 1, "note": "给小王"})
	url, _ := out["url"].(string)
	i := strings.Index(url, "/invite/")
	if i < 0 {
		t.Fatalf("no link: %v", out)
	}
	code := url[i+len("/invite/"):]
	c.do("POST", "/api/v1/auth/logout", nil)
	// the link gets past a security entry and on to the page
	c.app.setSetting("access.entry", "/b-7f3k2q")
	c.hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	r0, _ := c.do("GET", "/invite/"+code, nil)
	c.hc.CheckRedirect = nil
	if r := r0; r.StatusCode != 302 || !strings.HasSuffix(r.Header.Get("Location"), "/accept-invite#"+code) {
		t.Fatalf("link: %d %s", r.StatusCode, r.Header.Get("Location"))
	}
	_, info := c.do("GET", "/api/v1/invite/"+code, nil)
	if info["preset"] != "vms" || info["note"] != "给小王" || info["by"] == "" {
		t.Fatalf("info: %v", info)
	}
	if r, _ := c.do("POST", "/api/v1/invite/"+code+"/accept", map[string]string{"username": "wang", "password": "short"}); r.StatusCode != 400 {
		t.Fatal("weak password accepted")
	}
	r, acc := c.do("POST", "/api/v1/invite/"+code+"/accept", map[string]string{"username": "wang", "display_name": "小王", "password": "correct horse 10"})
	if r.StatusCode != 200 {
		t.Fatalf("accept: %d %v", r.StatusCode, acc)
	}
	u, _, _ := scanUser(c.app.db.QueryRow(`SELECT ` + userCols + ` FROM users WHERE username='wang'`))
	if u == nil || u.Role != RoleAdmin || !u.Can("vms.manage") || u.Can("sites.manage") || c.app.quotaOf(u.ID).VMs != 2 {
		t.Fatalf("user: %+v", u)
	}
	if r, _ := c.do("POST", "/api/v1/invite/"+code+"/accept", map[string]string{"username": "wang2", "password": "correct horse 10"}); r.StatusCode != 404 {
		t.Fatal("a single-use invite worked twice")
	}
}
