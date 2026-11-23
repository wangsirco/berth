package system

// mDNS names for the panel (name.local): avahi answers them on the LAN, so phones and laptops
// find the panel by name without any DNS setup — what Passkey needs (it refuses IP addresses).
// The host's own name is published by avahi itself; extra names go into a block of
// /etc/avahi/hosts that Berth owns.

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
)

const (
	avahiHosts = "/etc/avahi/hosts"
	mdnsBegin  = "# >>> berth: panel names (managed by Berth)"
	mdnsEnd    = "# <<< berth"
)

type MDNS struct {
	Installed    bool     `json:"installed"`
	Active       bool     `json:"active"`
	Hostname     string   `json:"hostname"` // published as hostname.local
	Aliases      []string `json:"aliases"`  // extra names Berth publishes
	FirewallOpen bool     `json:"firewall_open"`
}

var mdnsNameRe = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]*[a-z0-9])?\.)+local$`)

func avahiBin() bool {
	for _, p := range []string{"/usr/sbin/avahi-daemon", "/usr/bin/avahi-daemon"} {
		if _, err := os.Stat(p); err == nil {
			return true
		}
	}
	return false
}

func MDNSStatus(ctx context.Context) MDNS {
	m := MDNS{Installed: avahiBin(), Aliases: []string{}, FirewallOpen: true}
	h, _ := os.Hostname()
	m.Hostname = strings.ToLower(strings.SplitN(h, ".", 2)[0])
	if out, err := exec.CommandContext(ctx, "systemctl", "is-active", "avahi-daemon").Output(); err == nil {
		m.Active = strings.TrimSpace(string(out)) == "active"
	}
	if b, err := os.ReadFile(avahiHosts); err == nil {
		in := false
		for _, l := range strings.Split(string(b), "\n") {
			switch {
			case strings.HasPrefix(l, mdnsBegin):
				in = true
			case strings.HasPrefix(l, mdnsEnd):
				in = false
			case in:
				if f := strings.Fields(l); len(f) == 2 {
					m.Aliases = append(m.Aliases, f[1])
				}
			}
		}
	}
	if firewalldActive(ctx) {
		out, _ := exec.CommandContext(ctx, "firewall-cmd", "--query-service=mdns").Output()
		m.FirewallOpen = strings.TrimSpace(string(out)) == "yes"
	}
	return m
}

func firewalldActive(ctx context.Context) bool {
	out, err := exec.CommandContext(ctx, "systemctl", "is-active", "firewalld").Output()
	return err == nil && strings.TrimSpace(string(out)) == "active"
}

// MDNSApply makes name.local work: avahi installed and running, mDNS let through the
// firewall, and aliases (other than the host's own name) published for ip.
func MDNSApply(ctx context.Context, aliases []string, ip string, log func(string)) error {
	for _, a := range aliases {
		if !mdnsNameRe.MatchString(a) {
			return fmt.Errorf("not a .local name: %q", a)
		}
	}
	if !avahiBin() {
		pkg := "avahi"
		if has("apt-get") {
			pkg = "avahi-daemon"
		}
		if _, err := ApplyPackages(ctx, []string{pkg}, false, log); err != nil {
			return err
		}
	}
	if firewalldActive(ctx) {
		log("$ firewall-cmd --add-service=mdns (--permanent)")
		exec.CommandContext(ctx, "firewall-cmd", "--add-service=mdns").Run()
		exec.CommandContext(ctx, "firewall-cmd", "--permanent", "--add-service=mdns").Run()
	}
	h, _ := os.Hostname()
	own := strings.ToLower(strings.SplitN(h, ".", 2)[0]) + ".local"
	var lines []string
	for _, a := range aliases {
		if a != own && ip != "" {
			lines = append(lines, ip+" "+a)
		}
	}
	if err := writeManagedBlock(avahiHosts, lines); err != nil {
		return err
	}
	log("$ systemctl enable --now avahi-daemon; systemctl restart avahi-daemon")
	if out, err := exec.CommandContext(ctx, "systemctl", "enable", "--now", "avahi-daemon").CombinedOutput(); err != nil {
		return fmt.Errorf("avahi-daemon: %s", strings.TrimSpace(string(out)))
	}
	if out, err := exec.CommandContext(ctx, "systemctl", "restart", "avahi-daemon").CombinedOutput(); err != nil {
		return fmt.Errorf("avahi-daemon: %s", strings.TrimSpace(string(out)))
	}
	return nil
}

// writeManagedBlock replaces Berth's block in a file, keeping everything else as it was.
func writeManagedBlock(path string, lines []string) error {
	old, _ := os.ReadFile(path)
	var keep []string
	in := false
	for _, l := range strings.Split(strings.TrimRight(string(old), "\n"), "\n") {
		switch {
		case strings.HasPrefix(l, mdnsBegin):
			in = true
		case strings.HasPrefix(l, mdnsEnd):
			in = false
		case !in:
			keep = append(keep, l)
		}
	}
	for len(keep) > 0 && keep[len(keep)-1] == "" {
		keep = keep[:len(keep)-1]
	}
	if len(lines) > 0 {
		if len(keep) > 0 {
			keep = append(keep, "")
		}
		keep = append(keep, mdnsBegin)
		keep = append(keep, lines...)
		keep = append(keep, mdnsEnd)
	}
	if len(keep) == 0 {
		return os.WriteFile(path, nil, 0o644)
	}
	return os.WriteFile(path, []byte(strings.Join(keep, "\n")+"\n"), 0o644)
}
