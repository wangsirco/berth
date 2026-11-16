package panel

import (
	"testing"

	"github.com/wangsirco/berth/internal/change"
)

func TestFirewallLockout(t *testing.T) {
	a := &App{cfg: Config{Listen: ":7090"}}
	env := change.Env{ActorIP: "10.0.2.50"}
	cases := []struct {
		rules []fwRule
		want  string
	}{
		{[]fwRule{{Action: "deny", Proto: "tcp", Ports: "41999", Source: "any", Enabled: true}}, ""},
		{[]fwRule{{Action: "deny", Proto: "tcp", Ports: "7090", Source: "any", Enabled: true}}, "panel 7090"},
		{[]fwRule{{Action: "deny", Proto: "tcp", Ports: "20-25", Source: "cidr", Value: "10.0.0.0/8", Enabled: true}}, "SSH 22"},
		{[]fwRule{{Action: "deny", Proto: "tcp", Ports: "22", Source: "cidr", Value: "192.168.0.0/16", Enabled: true}}, ""},
		{[]fwRule{{Action: "deny", Proto: "tcp", Ports: "22", Source: "any", Enabled: false}}, ""},
		// an earlier allow for your network keeps the door open
		{[]fwRule{{Action: "allow", Proto: "tcp", Ports: "22", Source: "cidr", Value: "10.0.2.0/24", Enabled: true},
			{Action: "deny", Proto: "tcp", Ports: "22", Source: "any", Enabled: true}}, ""},
		// only Cloudflare may reach 7090: you are not Cloudflare
		{[]fwRule{{Action: "deny", Proto: "tcp", Ports: "7090", Source: "cloudflare", Except: true, Enabled: true}}, "panel 7090"},
		{[]fwRule{{Action: "deny", Proto: "udp", Ports: "22", Source: "any", Enabled: true}}, ""},
	}
	for i, c := range cases {
		if got := a.lockout(env, c.rules); got != c.want {
			t.Errorf("case %d: got %q want %q", i, got, c.want)
		}
	}
	if a.lockout(change.Env{ActorIP: "127.0.0.1"}, cases[1].rules) != "" {
		t.Error("loopback (the local CLI) cannot be locked out")
	}
}

func TestWithRule(t *testing.T) {
	rules := []fwRule{{ID: 1, Action: "deny"}, {ID: 2, Action: "deny"}}
	out := withRule(rules, fwRuleParams{fwRule: fwRule{ID: 2, Action: "allow"}})
	if len(out) != 2 || out[1].Action != "allow" {
		t.Fatal(out)
	}
	out = withRule(rules, fwRuleParams{fwRule: fwRule{Action: "allow"}})
	if len(out) != 3 {
		t.Fatal(out)
	}
}
