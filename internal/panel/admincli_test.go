package panel

import (
	"bufio"
	"strings"
	"testing"
)

func TestAdminCLIEntryUserPassword(t *testing.T) {
	c := newTC(t)
	u := c.mkUser("boss", "correct horse 9", "super")
	c.app.db.Exec(`INSERT INTO sessions(token_hash, user_id, csrf, ip, user_agent, method, created_at, seen_at) VALUES('x', ?, 'c', '', '', 'password', 0, 0)`, u.ID)
	a := &adminCLI{db: c.app.db, cfg: c.app.cfg, unit: "berth-panel-test-none", in: bufio.NewReader(strings.NewReader(""))}
	if err := a.entry("/bad", true); err == nil {
		t.Fatal("short entry accepted")
	}
	if err := a.entry("/mypanel88", true); err != nil || a.setting("access.entry") != "/mypanel88" {
		t.Fatal(err, a.setting("access.entry"))
	}
	if err := a.entry("off", true); err != nil || a.setting("access.entry") != "" {
		t.Fatal(err)
	}
	if err := a.username("x"); err == nil {
		t.Fatal("bad username accepted")
	}
	if err := a.username("chief"); err != nil {
		t.Fatal(err)
	}
	if err := a.password(true); err != nil {
		t.Fatal(err)
	}
	var n int
	c.app.db.QueryRow(`SELECT COUNT(*) FROM sessions WHERE user_id=?`, u.ID).Scan(&n)
	var name, hash string
	c.app.db.QueryRow(`SELECT username, password_hash FROM users WHERE id=?`, u.ID).Scan(&name, &hash)
	if n != 0 || name != "chief" || checkPasswordHash(hash, "correct horse 9") {
		t.Fatalf("sessions %d name %s", n, name)
	}
}
