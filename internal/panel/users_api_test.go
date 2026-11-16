package panel

import (
	"strings"
	"testing"
)

func TestUserCreateViaPipelineAndUndo(t *testing.T) {
	c := newTC(t)
	c.mkUser("iwsheng", "correct horse 9", "super")
	c.login("iwsheng", "correct horse 9")
	r, out := c.do("POST", "/api/v1/users?plan=1", map[string]any{"username": "ops", "role": "admin", "password": "another pass 1", "perms": []string{"containers.view"}})
	if r.StatusCode != 200 || out["status"] != "planned" {
		t.Fatalf("plan: %d %v", r.StatusCode, out)
	}
	if n := c.app.userCount(); n != 1 {
		t.Fatal("preview created a user")
	}
	r, out = c.do("POST", "/api/v1/users", map[string]any{"username": "ops", "role": "admin", "password": "another pass 1", "perms": []string{"containers.view"}})
	if r.StatusCode != 200 || out["status"] != "done" {
		t.Fatalf("run: %d %v", r.StatusCode, out)
	}
	if !strings.HasPrefix(out["equivalent"].(string), "berth user add ops") {
		t.Fatalf("equivalent %v", out["equivalent"])
	}
	var stored string
	c.app.db.QueryRow(`SELECT params FROM changes WHERE id=?`, out["id"]).Scan(&stored)
	if strings.Contains(stored, "argon2") || !strings.Contains(stored, "[redacted]") {
		t.Fatalf("password hash leaked into audit: %s", stored)
	}
	ch, err := c.app.pipe.Revert(t.Context(), c.app.userActor(testRequest(), mustUser(c, "iwsheng")), out["id"].(string))
	if err != nil || ch.Status != "done" {
		t.Fatalf("undo: %v", err)
	}
	u, _, _ := scanUser(c.app.db.QueryRow(`SELECT ` + userCols + ` FROM users WHERE username='ops'`))
	if !u.Disabled {
		t.Fatal("undo of create should disable the user")
	}
}

func TestLastSuperProtected(t *testing.T) {
	c := newTC(t)
	u := c.mkUser("iwsheng", "correct horse 9", "super")
	c.login("iwsheng", "correct horse 9")
	r, out := c.do("PATCH", "/api/v1/users/"+itoa(u.ID), map[string]any{"role": "admin"})
	if r.StatusCode != 409 || out["error"].(map[string]any)["code"] != "user.last_super" {
		t.Fatalf("%d %v", r.StatusCode, out)
	}
}

func TestAdminCannotManageUsers(t *testing.T) {
	c := newTC(t)
	c.mkUser("ops", "correct horse 9", "admin")
	c.login("ops", "correct horse 9")
	if r, _ := c.do("GET", "/api/v1/users", nil); r.StatusCode != 403 {
		t.Fatalf("%d", r.StatusCode)
	}
}
