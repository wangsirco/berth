package firewall

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidate(t *testing.T) {
	good := []Rule{
		{Action: "deny", Proto: "tcp", Ports: "22", Source: "any"},
		{Action: "allow", Proto: "tcp", Ports: "80,443,8000-8100", Source: "cidr", Value: "10.0.0.0/8"},
		{Action: "deny", Proto: "tcp", Ports: "80,443", Source: "cloudflare", Except: true},
		{Action: "deny", Proto: "any", Source: "country", Value: "KP"},
		{Action: "deny", Proto: "udp", Source: "ip", Value: "2001:db8::1"},
	}
	for _, r := range good {
		if err := r.Validate(); err != nil {
			t.Errorf("%+v: %v", r, err)
		}
	}
	bad := []Rule{
		{Action: "drop", Proto: "tcp", Source: "any"},
		{Action: "deny", Proto: "tcp", Ports: "0", Source: "any"},
		{Action: "deny", Proto: "tcp", Ports: "90-80", Source: "any"},
		{Action: "deny", Proto: "tcp", Ports: "22; flush ruleset", Source: "any"},
		{Action: "deny", Proto: "any", Ports: "22", Source: "any"},
		{Action: "deny", Proto: "tcp", Source: "ip", Value: "1.2.3.4 accept"},
		{Action: "deny", Proto: "tcp", Source: "country", Value: "cn"},
		{Action: "deny", Proto: "tcp", Source: "any", Except: true},
		{Action: "allow", Proto: "tcp", Ports: "22", Source: "cloudflare", Except: true},
	}
	for _, r := range bad {
		if r.Validate() == nil {
			t.Errorf("accepted %+v", r)
		}
	}
}

func TestRender(t *testing.T) {
	rules := []Rule{
		{ID: 1, Action: "allow", Proto: "tcp", Ports: "22", Source: "cidr", Value: "10.0.0.0/8", Enabled: true},
		{ID: 2, Action: "deny", Proto: "tcp", Ports: "22", Source: "any", Enabled: true},
		{ID: 3, Action: "deny", Proto: "tcp", Ports: "80,443", Source: "cloudflare", Except: true, Enabled: true},
		{ID: 4, Action: "deny", Proto: "tcp", Ports: "9999", Source: "any", Enabled: false},
	}
	s := Render(rules, Sets{"cloudflare": {"173.245.48.0/20", "2400:cb00::/32"}})
	for _, want := range []string{
		"set cloudflare_v4 {", "elements = { 173.245.48.0/20 }", "elements = { 2400:cb00::/32 }",
		"type filter hook input priority -10; policy accept;",
		`ip saddr 10.0.0.0/8 tcp dport { 22 } accept comment "berth:1"`,
		`ct state new tcp dport { 22 } drop comment "berth:2"`,
		`ct state new ip saddr != @cloudflare_v4 tcp dport { 80, 443 } drop comment "berth:3"`,
		`ct state new ip6 saddr != @cloudflare_v6 tcp dport { 80, 443 } drop comment "berth:3"`,
		// Docker-published ports: matched on the original destination in forward
		`ct state new meta l4proto tcp ct original proto-dst { 22 } drop comment "berth:2"`,
		"ct state established,related accept", "set ban_v4 {", "ip saddr @ban_v4 drop",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in\n%s", want, s)
		}
	}
	if strings.Contains(s, "9999") {
		t.Error("disabled rule rendered")
	}
	// allow comes before deny (order kept)
	if strings.Index(s, "berth:1") > strings.Index(s, "berth:2") {
		t.Error("order lost")
	}
}

func TestFirewalldPorts(t *testing.T) {
	ports, rich := FirewalldPorts([]Rule{
		{Action: "allow", Proto: "tcp", Ports: "8080,9000-9010", Source: "any", Enabled: true},
		{Action: "allow", Proto: "udp", Ports: "51820", Source: "cidr", Value: "192.168.1.0/24", Enabled: true},
		{Action: "deny", Proto: "tcp", Ports: "22", Source: "any", Enabled: true},
	})
	if strings.Join(ports, " ") != "8080/tcp 9000-9010/tcp" || len(rich) != 1 || !strings.Contains(rich[0], `source address="192.168.1.0/24" port port="51820" protocol="udp" accept`) {
		t.Fatal(ports, rich)
	}
}

func TestListening(t *testing.T) {
	root := t.TempDir()
	ProcRoot = root
	defer func() { ProcRoot = "/proc" }()
	os.MkdirAll(filepath.Join(root, "net"), 0o755)
	// 0.0.0.0:22 LISTEN inode 111; 127.0.0.1:7090 LISTEN inode 222; an established one
	tcp := `  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 00000000:0016 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 111 1 0 100 0 0 10 0
   1: 0100007F:1BB2 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 222 1 0 100 0 0 10 0
   2: 0100007F:1BB2 0100007F:D000 01 00000000:00000000 00:00000000 00000000     0        0 333 1 0 100 0 0 10 0
`
	os.WriteFile(filepath.Join(root, "net/tcp"), []byte(tcp), 0o644)
	os.MkdirAll(filepath.Join(root, "42/fd"), 0o755)
	os.WriteFile(filepath.Join(root, "42/comm"), []byte("sshd\n"), 0o644)
	os.Symlink("socket:[111]", filepath.Join(root, "42/fd/3"))
	ls := Listening()
	if len(ls) != 2 || ls[0].Port != 22 || !ls[0].Public || ls[0].Process != "sshd" || ls[1].Port != 7090 || ls[1].Public {
		t.Fatalf("%+v", ls)
	}
}

func TestParseListAll(t *testing.T) {
	out := "FedoraServer (default, active)\n  target: default\n  interfaces: end0\n  sources: \n  services: dhcpv6-client mdns ssh\n  ports: 8080/tcp 51820/udp\n  protocols: \n  rich rules: \n\trule family=\"ipv4\" source address=\"10.0.0.0/8\" port port=\"7090\" protocol=\"tcp\" accept\n\trule family=\"ipv4\" source address=\"192.168.0.0/16\" port port=\"7090\" protocol=\"tcp\" accept\n"
	z := parseListAll(out)
	if z.Zone != "FedoraServer" || strings.Join(z.Services, ",") != "dhcpv6-client,mdns,ssh" || strings.Join(z.Ports, ",") != "8080/tcp,51820/udp" || len(z.Rich) != 2 || !strings.HasPrefix(z.Rich[0], `rule family="ipv4" source address="10.0.0.0/8"`) {
		t.Fatalf("%+v", z)
	}
}
