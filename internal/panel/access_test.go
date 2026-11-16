package panel

import (
	"context"
	"testing"
	"time"
)

func TestDeviceOf(t *testing.T) {
	for ua, want := range map[string]string{
		"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0 Safari/537.36":                       "Chrome · macOS",
		"Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.0 Mobile/15E148 Safari/604.1": "Safari · iOS",
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0 Safari/537.36 Edg/131.0":                   "Edge · Windows",
		"Mozilla/5.0 (X11; Linux aarch64; rv:133.0) Gecko/20100101 Firefox/133.0":                                                                 "Firefox · Linux",
		"curl/8.9.1": "curl",
	} {
		if got := deviceOf(ua); got != want {
			t.Errorf("%q: %q", ua, got)
		}
	}
}

func TestLoginFlags(t *testing.T) {
	c := newTC(t)
	a := c.app
	u := c.mkUser("ann", "pw-ann-1234567", RoleSuper)
	now := time.Now().Unix()
	ins := func(ok bool, ip string, ts int64) {
		a.db.Exec(`INSERT INTO logins(ts, username, user_id, ok, method, ip, user_agent, reason) VALUES(?,?,?,?,?,?,?,?)`, ts, "ann", u.ID, ok, "password", ip, "curl/8", "")
	}
	ins(true, "10.0.0.5", now-7200)
	ins(true, "10.0.0.5", now-3600) // known address
	for i := 0; i < 3; i++ {
		ins(false, "203.0.113.9", now-60)
	}
	ins(true, "203.0.113.9", now) // new address, right after failures
	rs := a.loginRecords(context.Background(), "ann", false, 50)
	if len(rs) != 6 {
		t.Fatalf("%d records", len(rs))
	}
	has := func(r loginRecord, f string) bool {
		for _, x := range r.Flags {
			if x == f {
				return true
			}
		}
		return false
	}
	if !has(rs[0], "new_ip") || !has(rs[0], "after_failures") {
		t.Errorf("latest: %+v", rs[0])
	}
	if has(rs[4], "new_ip") {
		t.Errorf("known address flagged: %+v", rs[4])
	}
	if !has(rs[5], "new_ip") { // the very first sign-in is new, too
		t.Errorf("first: %+v", rs[5])
	}
}
