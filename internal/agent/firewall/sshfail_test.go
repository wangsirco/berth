package firewall

import "testing"

func TestParseSSH(t *testing.T) {
	e := func(pid, msg string) journalEntry {
		return journalEntry{PID: pid, Message: msg, Realtime: "1794400000000000"}
	}
	got := ParseSSH([]journalEntry{
		// one bot session: invalid user, then two password tries
		e("10", "Invalid user admin from 203.0.113.9 port 51022"),
		e("10", "Failed password for invalid user admin from 203.0.113.9 port 51022 ssh2"),
		e("10", "Failed password for invalid user admin from 203.0.113.9 port 51022 ssh2"),
		// a key-only probe: no "Failed" line
		e("11", "Connection closed by invalid user baduser 172.17.0.2 port 40000 [preauth]"),
		e("12", "Invalid user baduser from 172.17.0.2 port 40002"),
		// real user, wrong password, IPv6
		e("13", "Failed password for root from 2001:db8::7 port 22 ssh2"),
		// sshd refusing the source itself: one attempt per refused connection
		e("1", "drop connection #0 from [172.17.0.2]:34318 on [192.0.2.10]:22 penalty: attempted authentication by invalid user"),
		journalEntry{PID: "1", Message: "drop connection #0 from [172.17.0.2]:34324 on [192.0.2.10]:22 penalty: attempted authentication by invalid user", Realtime: "1794400000000001"},
		e("1", "drop connection #0 from [198.51.100.3]:1 on [192.0.2.10]:22 penalty: connections without attempting authentication"),
		// noise
		e("14", "Accepted publickey for root from 10.0.2.231 port 56016 ssh2: ED25519 SHA256:x"),
		e("15", "Failed password for root from not-an-ip port 1 ssh2"),
	})
	if len(got) != 6 {
		t.Fatalf("%+v", got)
	}
	if got[0].IP != "203.0.113.9" || got[0].Count != 2 || got[0].User != "admin" || got[0].TS != 1794400000 {
		t.Errorf("%+v", got[0])
	}
	if got[1].IP != "172.17.0.2" || got[1].Count != 1 || got[2].Count != 1 {
		t.Errorf("%+v %+v", got[1], got[2])
	}
	if got[3].IP != "2001:db8::7" || got[3].User != "root" {
		t.Errorf("%+v", got[3])
	}
	if got[4].IP != "172.17.0.2" || got[5].IP != "172.17.0.2" || got[4].Count != 1 {
		t.Errorf("penalty drops: %+v %+v", got[4], got[5])
	}
}
