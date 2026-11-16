package devenv

// The "internet only" guard: a workspace (an autonomous Claude Code above all) may reach the
// internet but not this LAN, other private networks, or the server itself. Its compose network
// gets a fixed bridge name; two iptables rules hang off that bridge:
//
//	DOCKER-USER -i <bridge> -j BERTH-DEVNET   (private destinations: REJECT, unless a reply)
//	INPUT       -i <bridge> NEW -j REJECT     (nothing new towards the server's own addresses)
//
// Replies stay allowed, so SSH and the web editor (published ports) keep working. The rules only
// match traffic from that bridge: they cannot lock anyone out of the server. A marker file in the
// workspace state makes the agent put them back after a reboot or a firewall reload.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/wangsirco/berth/internal/errs"
)

const guardChain = "BERTH-DEVNET"

var privateNets = []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "169.254.0.0/16", "100.64.0.0/10"}

// BridgeName is the workspace network's Linux bridge (15 characters at most).
func BridgeName(name string) string {
	h := sha256.Sum256([]byte(name))
	return "bdev" + hex.EncodeToString(h[:])[:10]
}

func ipt(ctx context.Context, args ...string) error {
	out, err := exec.CommandContext(ctx, "iptables", append([]string{"-w", "5"}, args...)...).CombinedOutput()
	if err != nil {
		return errs.New(500, "dev.guard_failed", "detail", strings.TrimSpace(string(out)))
	}
	return nil
}

func guardRules(br string) [][]string {
	return [][]string{
		{"DOCKER-USER", "-i", br, "-j", guardChain},
		{"INPUT", "-i", br, "-m", "conntrack", "--ctstate", "NEW", "-j", "REJECT"},
	}
}

func ensureGuardChain(ctx context.Context) error {
	if ipt(ctx, "-n", "-L", guardChain) != nil {
		if err := ipt(ctx, "-N", guardChain); err != nil {
			return err
		}
	}
	if err := ipt(ctx, "-F", guardChain); err != nil {
		return err
	}
	if err := ipt(ctx, "-A", guardChain, "-m", "conntrack", "--ctstate", "RELATED,ESTABLISHED", "-j", "RETURN"); err != nil {
		return err
	}
	for _, n := range privateNets {
		if err := ipt(ctx, "-A", guardChain, "-d", n, "-j", "REJECT", "--reject-with", "icmp-net-prohibited"); err != nil {
			return err
		}
	}
	return ipt(ctx, "-A", guardChain, "-j", "RETURN")
}

// Guard turns the rules for one workspace on or off (and remembers the choice).
func Guard(ctx context.Context, name string, on bool) error {
	if !ValidName(name) {
		return errs.New(400, "dev.bad_name")
	}
	if _, err := exec.LookPath("iptables"); err != nil {
		if on {
			return errs.New(500, "dev.guard_failed", "detail", "iptables not found")
		}
		return nil
	}
	br := BridgeName(name)
	marker := filepath.Join(StateDir, name, "egress-guard")
	if !on {
		os.Remove(marker)
		for _, r := range guardRules(br) {
			for i := 0; i < 5 && ipt(ctx, append([]string{"-D"}, r...)...) == nil; i++ {
			}
		}
		return nil
	}
	if ipt(ctx, "-n", "-L", "DOCKER-USER") != nil {
		return errs.New(500, "dev.guard_failed", "detail", "Docker's DOCKER-USER chain is missing")
	}
	// the marker first: a sweep running meanwhile must not take the new rules for leftovers
	os.MkdirAll(filepath.Dir(marker), 0o700)
	if err := os.WriteFile(marker, nil, 0o600); err != nil {
		return err
	}
	if err := ensureGuardChain(ctx); err != nil {
		return err
	}
	for _, r := range guardRules(br) {
		if ipt(ctx, append([]string{"-C"}, r...)...) != nil {
			if err := ipt(ctx, append([]string{"-I"}, r...)...); err != nil {
				return err
			}
		}
	}
	return nil
}

// GuardAll puts back the rules of every guarded workspace (after a reboot or a reload) and
// drops rules left by workspaces that are gone.
func GuardAll(ctx context.Context) {
	keep := map[string]bool{}
	ents, _ := os.ReadDir(StateDir)
	for _, e := range ents {
		if _, err := os.Stat(filepath.Join(StateDir, e.Name(), "egress-guard")); err == nil && ValidName(e.Name()) {
			keep[BridgeName(e.Name())] = true
			Guard(ctx, e.Name(), true)
		}
	}
	for _, chain := range []string{"DOCKER-USER", "INPUT"} {
		out, err := exec.CommandContext(ctx, "iptables", "-w", "5", "-S", chain).Output()
		if err != nil {
			continue
		}
		for _, l := range strings.Split(string(out), "\n") {
			f := strings.Fields(l)
			if len(f) < 4 || f[0] != "-A" {
				continue
			}
			for i := 0; i+1 < len(f); i++ {
				if f[i] == "-i" && strings.HasPrefix(f[i+1], "bdev") && !keep[f[i+1]] {
					ipt(ctx, append([]string{"-D"}, f[1:]...)...)
				}
			}
		}
	}
}

// KeepGuards re-checks the rules every two minutes (firewalld reloads drop them).
func KeepGuards() {
	go func() {
		for {
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			GuardAll(ctx)
			cancel()
			time.Sleep(2 * time.Minute)
		}
	}()
}
