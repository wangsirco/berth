package panel

import (
	"context"
	"testing"
	"time"
)

func TestBruteForceLadder(t *testing.T) {
	c := newTC(t)
	a := c.app
	ctx := context.Background()
	fail := func(ip string, n int) {
		a.recordFailures("local", "ssh", []authFailure{{TS: time.Now().Unix(), IP: ip, Count: n}})
	}
	active := func(ip string) []ban {
		var out []ban
		for _, b := range a.activeBans("local") {
			if b.Addr == ip {
				out = append(out, b)
			}
		}
		return out
	}
	fail("203.0.113.9", 4)
	a.judge(ctx, "local")
	if len(active("203.0.113.9")) != 0 {
		t.Fatal("banned under the threshold")
	}
	fail("203.0.113.9", 1)
	a.judge(ctx, "local")
	b := active("203.0.113.9")
	if len(b) != 1 || b[0].Source != "bruteforce" || b[0].ExpiresAt-b[0].CreatedAt != 600 {
		t.Fatalf("%+v", b)
	}
	a.judge(ctx, "local") // already banned: no second ban
	if len(active("203.0.113.9")) != 1 {
		t.Fatal("double ban")
	}
	// it comes back after the ban: longer
	a.db.Exec(`UPDATE bans SET expires_at=? WHERE addr=?`, time.Now().Unix()-1, "203.0.113.9")
	fail("203.0.113.9", 5)
	a.judge(ctx, "local")
	if b = active("203.0.113.9"); len(b) != 1 || b[0].ExpiresAt-b[0].CreatedAt != 3600 || b[0].Strike != 2 {
		t.Fatalf("second strike: %+v", b)
	}
	// loopback and the whitelist are never banned
	fail("127.0.0.1", 50)
	a.setSetting("security.bf_whitelist", "10.0.0.0/8\n192.0.2.7")
	fail("10.1.2.3", 50)
	fail("192.0.2.7", 50)
	a.judge(ctx, "local")
	for _, ip := range []string{"127.0.0.1", "10.1.2.3", "192.0.2.7"} {
		if len(active(ip)) != 0 {
			t.Errorf("%s banned", ip)
		}
	}
	// switched off
	a.setSetting("security.bf_enabled", "0")
	fail("198.51.100.1", 50)
	a.judge(ctx, "local")
	if len(active("198.51.100.1")) != 0 {
		t.Error("banned while disabled")
	}
}

func TestPanelLoginFailuresCounted(t *testing.T) {
	c := newTC(t)
	a := c.app
	a.collectLoginFailures() // first run starts from now
	for i := 0; i < 3; i++ {
		a.db.Exec(`INSERT INTO logins(ts, username, ok, method, ip, user_agent, reason) VALUES(?,?,0,'password','203.0.113.50','x','bad_credentials')`, time.Now().Unix(), "admin")
	}
	a.db.Exec(`INSERT INTO logins(ts, username, ok, method, ip, user_agent, reason) VALUES(?,?,0,'password','203.0.113.50','x','rate_limited')`, time.Now().Unix(), "admin")
	// a right password waiting for the second step is not a failure
	a.db.Exec(`INSERT INTO logins(ts, username, ok, method, ip, user_agent, reason) VALUES(?,?,0,'password','203.0.113.50','x','second_step')`, time.Now().Unix(), "admin")
	a.collectLoginFailures()
	var n int
	a.db.QueryRow(`SELECT SUM(count) FROM auth_failures WHERE ip='203.0.113.50' AND kind='panel'`).Scan(&n)
	if n != 3 {
		t.Fatalf("counted %d", n)
	}
	a.collectLoginFailures()
	a.db.QueryRow(`SELECT SUM(count) FROM auth_failures WHERE ip='203.0.113.50'`).Scan(&n)
	if n != 3 {
		t.Fatalf("counted twice: %d", n)
	}
}

func TestNormAddr(t *testing.T) {
	for in, want := range map[string]string{"203.0.113.9": "203.0.113.9", " 203.0.113.0/24 ": "203.0.113.0/24", "203.0.113.9/24": "203.0.113.0/24", "2001:db8::1": "2001:db8::1", "203.0.113.9/32": "203.0.113.9", "2001:db8::5/128": "2001:db8::5"} {
		if got, err := normAddr(in); err != nil || got != want {
			t.Errorf("%q: %q %v", in, got, err)
		}
	}
	for _, bad := range []string{"0.0.0.0/0", "10.0.0.0/4", "::/0", "x", "1.2.3.4; flush"} {
		if _, err := normAddr(bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}
