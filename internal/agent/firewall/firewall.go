// Package firewall keeps Berth's own nftables table (`inet berth`) and opens ports in
// firewalld when it runs (方案 §5.12.1).
//
// Every change is applied with a safety net: the state before is saved, and a systemd timer
// restores it after a grace period unless the change is confirmed. The timer is a separate
// unit, so it fires even if the agent dies or the panel becomes unreachable.
package firewall

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Rule is one line of the panel's rule list.
type Rule struct {
	ID      int64  `json:"id"`
	Action  string `json:"action"`  // allow | deny
	Proto   string `json:"proto"`   // tcp | udp | any
	Ports   string `json:"ports"`   // "22", "80,443", "8000-8100"; empty = every port
	Source  string `json:"source"`  // any | ip | cidr | cloudflare | country
	Value   string `json:"value"`   // address, network, or country code (for ip/cidr/country)
	Except  bool   `json:"except"`  // match sources NOT in Source (e.g. "everyone except Cloudflare")
	Comment string `json:"comment"` // shown in the panel
	Enabled bool   `json:"enabled"`
}

// Sets are address lists rules may refer to (Cloudflare ranges, countries).
type Sets map[string][]string // name -> CIDRs, e.g. "cloudflare", "country_cn"

var (
	portsRe   = regexp.MustCompile(`^\d{1,5}(-\d{1,5})?(,\d{1,5}(-\d{1,5})?)*$`)
	countryRe = regexp.MustCompile(`^[A-Z]{2}$`)
)

// Validate checks a rule; the panel calls it before saving.
func (r Rule) Validate() error {
	if r.Action != "allow" && r.Action != "deny" {
		return fmt.Errorf("action must be allow or deny")
	}
	if r.Proto != "tcp" && r.Proto != "udp" && r.Proto != "any" {
		return fmt.Errorf("proto must be tcp, udp or any")
	}
	if r.Ports != "" {
		if !portsRe.MatchString(r.Ports) {
			return fmt.Errorf("bad ports %q", r.Ports)
		}
		for _, p := range strings.Split(r.Ports, ",") {
			a, b, _ := strings.Cut(p, "-")
			x, _ := strconv.Atoi(a)
			y := x
			if b != "" {
				y, _ = strconv.Atoi(b)
			}
			if x < 1 || y > 65535 || y < x {
				return fmt.Errorf("bad ports %q", r.Ports)
			}
		}
		if r.Proto == "any" {
			return fmt.Errorf("ports need tcp or udp")
		}
	}
	switch r.Source {
	case "any", "cloudflare":
	case "ip":
		if net.ParseIP(r.Value) == nil {
			return fmt.Errorf("bad address %q", r.Value)
		}
	case "cidr":
		if _, _, err := net.ParseCIDR(r.Value); err != nil {
			return fmt.Errorf("bad network %q", r.Value)
		}
	case "country":
		if !countryRe.MatchString(r.Value) {
			return fmt.Errorf("bad country %q", r.Value)
		}
	default:
		return fmt.Errorf("bad source %q", r.Source)
	}
	if r.Except && r.Source == "any" {
		return fmt.Errorf("except needs a source")
	}
	if r.Action == "allow" && r.Except {
		return fmt.Errorf("allow rules cannot use except")
	}
	return nil
}

// Render builds the nft script for the table. Deny rules drop new connections in input
// (host ports) and forward (ports Docker publishes, matched on the original destination
// port); connections already open are never cut. Allow rules are carried out by firewalld
// when it runs (see FirewalldPorts); in this table they only accept early.
func Render(rules []Rule, sets Sets) string {
	var b strings.Builder
	b.WriteString("table inet berth {\n")
	used := map[string]bool{}
	for _, r := range rules {
		if r.Enabled {
			if n := setName(r); n != "" {
				used[n] = true
			}
		}
	}
	names := make([]string, 0, len(used))
	for n := range used {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		v4, v6 := splitFamilies(sets[n])
		writeSet(&b, n+"_v4", "ipv4_addr", v4)
		writeSet(&b, n+"_v6", "ipv6_addr", v6)
	}
	b.WriteString("\tset ban_v4 {\n\t\ttype ipv4_addr\n\t\tflags interval, timeout\n\t}\n")
	b.WriteString("\tset ban_v6 {\n\t\ttype ipv6_addr\n\t\tflags interval, timeout\n\t}\n")
	for _, ch := range []struct{ name, hook, port string }{
		{"input", "input", "dport"},
		{"forward", "forward", "ct original proto-dst"},
	} {
		fmt.Fprintf(&b, "\tchain %s {\n\t\ttype filter hook %s priority -10; policy accept;\n", ch.name, ch.hook)
		if ch.name == "input" {
			b.WriteString("\t\tiifname \"lo\" accept\n")
		}
		b.WriteString("\t\tct state established,related accept\n")
		b.WriteString("\t\tip saddr @ban_v4 drop\n\t\tip6 saddr @ban_v6 drop\n")
		for _, r := range rules {
			if !r.Enabled {
				continue
			}
			for _, line := range ruleLines(r, ch.port) {
				fmt.Fprintf(&b, "\t\t%s\n", line)
			}
		}
		b.WriteString("\t}\n")
	}
	b.WriteString("}\n")
	return b.String()
}

func setName(r Rule) string {
	switch r.Source {
	case "cloudflare":
		return "cloudflare"
	case "country":
		return "country_" + strings.ToLower(r.Value)
	}
	return ""
}

func writeSet(b *strings.Builder, name, typ string, elems []string) {
	fmt.Fprintf(b, "\tset %s {\n\t\ttype %s\n\t\tflags interval\n", name, typ)
	if len(elems) > 0 {
		fmt.Fprintf(b, "\t\telements = { %s }\n", strings.Join(elems, ", "))
	}
	b.WriteString("\t}\n")
}

func splitFamilies(cidrs []string) (v4, v6 []string) {
	for _, c := range cidrs {
		ip, n, err := net.ParseCIDR(c)
		if err != nil {
			if ip = net.ParseIP(c); ip == nil {
				continue
			}
			c = ip.String()
		} else {
			c = n.String()
		}
		if ip.To4() != nil {
			v4 = append(v4, c)
		} else {
			v6 = append(v6, c)
		}
	}
	return
}

// ruleLines: one nft line per address family the source needs.
func ruleLines(r Rule, port string) []string {
	verdict := "drop"
	if r.Action == "allow" {
		verdict = "accept"
	}
	tail := ""
	if r.Proto != "any" {
		if r.Ports != "" {
			ports := "{ " + strings.ReplaceAll(r.Ports, ",", ", ") + " }"
			if port == "dport" {
				tail = r.Proto + " dport " + ports
			} else {
				tail = "meta l4proto " + r.Proto + " " + port + " " + ports
			}
		} else {
			tail = "meta l4proto " + r.Proto
		}
	}
	neg := ""
	if r.Except {
		neg = "!= "
	}
	join := func(parts ...string) string {
		var out []string
		for _, p := range parts {
			if p != "" {
				out = append(out, p)
			}
		}
		return strings.Join(out, " ")
	}
	ct := ""
	if verdict == "drop" {
		ct = "ct state new"
	}
	comment := ""
	if r.ID > 0 {
		comment = fmt.Sprintf(`comment "berth:%d"`, r.ID)
	}
	switch r.Source {
	case "any":
		return []string{join(ct, tail, verdict, comment)}
	case "ip", "cidr":
		fam := "ip"
		if !strings.Contains(r.Value, ".") {
			fam = "ip6"
		}
		return []string{join(ct, fam+" saddr "+neg+r.Value, tail, verdict, comment)}
	default: // a set: both families
		n := setName(r)
		return []string{
			join(ct, "ip saddr "+neg+"@"+n+"_v4", tail, verdict, comment),
			join(ct, "ip6 saddr "+neg+"@"+n+"_v6", tail, verdict, comment),
		}
	}
}

// FirewalldPorts lists "port/proto" entries to open in firewalld for allow rules that apply
// to everyone, and rich rules for allow rules limited to a source.
func FirewalldPorts(rules []Rule) (ports, rich []string) {
	for _, r := range rules {
		if !r.Enabled || r.Action != "allow" || r.Proto == "any" || r.Ports == "" {
			continue
		}
		for _, p := range strings.Split(r.Ports, ",") {
			switch r.Source {
			case "any":
				ports = append(ports, p+"/"+r.Proto)
			case "ip", "cidr":
				fam := "ipv4"
				if !strings.Contains(r.Value, ".") {
					fam = "ipv6"
				}
				rich = append(rich, fmt.Sprintf(`rule family="%s" source address="%s" port port="%s" protocol="%s" accept`, fam, r.Value, p, r.Proto))
			}
		}
	}
	return
}

// ---- applying ----

// Dir holds the boot copy and the rollback snapshot.
var Dir = "/var/lib/berth/firewall"

const rollbackUnit = "berth-fw-rollback"

// State is what the panel shows.
type State struct {
	Backend      string   `json:"backend"`   // nftables
	Firewalld    bool     `json:"firewalld"` // running
	Zone         string   `json:"zone,omitempty"`
	OpenPorts    []string `json:"open_ports,omitempty"` // firewalld ports in the default zone (runtime)
	Services     []string `json:"services,omitempty"`
	RichRules    []string `json:"rich_rules,omitempty"`
	Docker       bool     `json:"docker"`
	Pending      bool     `json:"pending"`               // waiting for confirmation
	RollbackAt   int64    `json:"rollback_at,omitempty"` // unix seconds
	Table        string   `json:"table,omitempty"`       // current `inet berth`
	NftAvailable bool     `json:"nft_available"`
}

var run = func(ctx context.Context, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	return strings.TrimSpace(out.String()), err
}

var runStdin = func(ctx context.Context, stdin, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdin = strings.NewReader(stdin)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	return strings.TrimSpace(out.String()), err
}

func has(bin string) bool { _, err := exec.LookPath(bin); return err == nil }

func firewalldRunning(ctx context.Context) bool {
	if !has("firewall-cmd") {
		return false
	}
	out, err := run(ctx, "firewall-cmd", "--state")
	return err == nil && out == "running"
}

type zoneInfo struct {
	Zone     string
	Ports    []string
	Services []string
	Rich     []string
}

// firewalldZone reads the default zone with one firewall-cmd call (each call starts Python,
// so they are slow); ok is false when firewalld is not running.
func firewalldZone(ctx context.Context) (zoneInfo, bool) {
	if !has("firewall-cmd") {
		return zoneInfo{}, false
	}
	out, err := run(ctx, "firewall-cmd", "--list-all")
	if err != nil {
		return zoneInfo{}, false
	}
	return parseListAll(out), true
}

func parseListAll(out string) zoneInfo {
	var z zoneInfo
	lines := strings.Split(out, "\n")
	if len(lines) > 0 {
		z.Zone, _, _ = strings.Cut(strings.TrimSpace(lines[0]), " ")
	}
	inRich := false
	for _, l := range lines[1:] {
		if inRich && strings.HasPrefix(l, "\t") || inRich && strings.HasPrefix(strings.TrimLeft(l, " "), "rule ") {
			if r := strings.TrimSpace(l); r != "" {
				z.Rich = append(z.Rich, r)
			}
			continue
		}
		inRich = false
		k, v, ok := strings.Cut(strings.TrimSpace(l), ":")
		if !ok {
			continue
		}
		switch k {
		case "services":
			z.Services = strings.Fields(v)
		case "ports":
			z.Ports = strings.Fields(v)
		case "rich rules":
			inRich = true
		}
	}
	return z
}

// Status reads the current state.
func Status(ctx context.Context) State {
	st := State{Backend: "nftables", NftAvailable: has("nft"), Docker: has("docker")}
	if z, ok := firewalldZone(ctx); ok {
		st.Firewalld, st.Zone, st.OpenPorts, st.Services, st.RichRules = true, z.Zone, z.Ports, z.Services, z.Rich
	}
	if st.NftAvailable {
		st.Table, _ = run(ctx, "nft", "list", "table", "inet", "berth")
	}
	if b, err := os.ReadFile(filepath.Join(Dir, "rollback-at")); err == nil {
		if at, _ := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64); at > time.Now().Unix()-5 {
			if out, _ := run(ctx, "systemctl", "is-active", rollbackUnit+".timer"); out == "active" {
				st.Pending, st.RollbackAt = true, at
			}
		}
	}
	return st
}

// Plan is what Apply will do.
type Plan struct {
	Script    string   `json:"script"`
	AddPorts  []string `json:"add_ports,omitempty"`
	DelPorts  []string `json:"del_ports,omitempty"`
	AddRich   []string `json:"add_rich,omitempty"`
	DelRich   []string `json:"del_rich,omitempty"`
	Firewalld bool     `json:"firewalld"`
}

// Desired is the full wanted state: rules plus the firewalld entries Berth owns.
type Desired struct {
	Rules   []Rule   `json:"rules"`
	Sets    Sets     `json:"sets"`
	Managed []string `json:"managed"` // firewalld ports/rich rules Berth added earlier (so it may remove them)
}

func (d Desired) plan(ctx context.Context) (Plan, error) {
	for _, r := range d.Rules {
		if err := r.Validate(); err != nil {
			return Plan{}, err
		}
	}
	if err := d.fillSets(ctx); err != nil {
		return Plan{}, err
	}
	for _, r := range d.Rules {
		if err := r.Validate(); err != nil {
			return Plan{}, err
		}
	}
	z, fwd := firewalldZone(ctx)
	p := Plan{Script: Render(d.Rules, d.Sets), Firewalld: fwd}
	if p.Firewalld {
		ports, rich := FirewalldPorts(d.Rules)
		want := map[string]bool{}
		for _, x := range append(ports, rich...) {
			want[x] = true
		}
		cur := map[string]bool{}
		for _, x := range append(z.Ports, z.Rich...) {
			cur[x] = true
		}
		for _, x := range ports {
			if !cur[x] {
				p.AddPorts = append(p.AddPorts, x)
			}
		}
		for _, x := range rich {
			if !cur[x] {
				p.AddRich = append(p.AddRich, x)
			}
		}
		for _, x := range d.Managed { // only what Berth itself opened earlier
			if !want[x] && cur[x] {
				if strings.HasPrefix(x, "rule ") {
					p.DelRich = append(p.DelRich, x)
				} else {
					p.DelPorts = append(p.DelPorts, x)
				}
			}
		}
	}
	return p, nil
}

// Preview returns the plan without changing anything (and checks the script with nft -c).
func Preview(ctx context.Context, d Desired) (Plan, error) {
	p, err := d.plan(ctx)
	if err != nil {
		return p, err
	}
	if has("nft") {
		if out, err := runStdin(ctx, "table inet berth\ndelete table inet berth\n"+p.Script, "nft", "-c", "-f", "-"); err != nil {
			return p, fmt.Errorf("nft: %s", out)
		}
	}
	return p, nil
}

// Apply puts d in place with a rollback after grace (unless confirmed). When a rollback
// is already pending, the earlier snapshot stays: rolling back returns to the state before
// the first unconfirmed change.
func Apply(ctx context.Context, d Desired, grace time.Duration) (Plan, error) {
	p, err := Preview(ctx, d)
	if err != nil {
		return p, err
	}
	if err := os.MkdirAll(Dir, 0o700); err != nil {
		return p, err
	}
	if !Status(ctx).Pending {
		if err := snapshot(ctx); err != nil {
			return p, err
		}
	}
	// arm the rollback before touching anything
	at := time.Now().Add(grace)
	run(ctx, "systemctl", "stop", rollbackUnit+".timer")
	run(ctx, "systemctl", "reset-failed", rollbackUnit+".service", rollbackUnit+".timer")
	if out, err := run(ctx, "systemd-run", "--unit", rollbackUnit, "--on-active", strconv.Itoa(int(grace.Seconds()))+"s",
		"--description", "Berth firewall rollback", "/bin/sh", filepath.Join(Dir, "rollback.sh")); err != nil {
		return p, fmt.Errorf("arming rollback: %s", out)
	}
	os.WriteFile(filepath.Join(Dir, "rollback-at"), []byte(strconv.FormatInt(at.Unix(), 10)), 0o600)
	if out, err := runStdin(ctx, "table inet berth\ndelete table inet berth\n"+p.Script, "nft", "-f", "-"); err != nil {
		return p, fmt.Errorf("nft: %s", out)
	}
	if p.Firewalld {
		for _, x := range p.AddPorts {
			run(ctx, "firewall-cmd", "--add-port="+x)
		}
		for _, x := range p.DelPorts {
			run(ctx, "firewall-cmd", "--remove-port="+x)
		}
		for _, x := range p.AddRich {
			run(ctx, "firewall-cmd", "--add-rich-rule="+x)
		}
		for _, x := range p.DelRich {
			run(ctx, "firewall-cmd", "--remove-rich-rule="+x)
		}
	}
	// what Confirm will make permanent
	os.WriteFile(filepath.Join(Dir, "pending.nft"), []byte(p.Script), 0o600)
	appendLines(filepath.Join(Dir, "pending-firewalld"), firewalldOps(p))
	return p, nil
}

func firewalldOps(p Plan) []string {
	var ops []string
	for _, x := range p.AddPorts {
		ops = append(ops, "--add-port="+x)
	}
	for _, x := range p.DelPorts {
		ops = append(ops, "--remove-port="+x)
	}
	for _, x := range p.AddRich {
		ops = append(ops, "--add-rich-rule="+x)
	}
	for _, x := range p.DelRich {
		ops = append(ops, "--remove-rich-rule="+x)
	}
	return ops
}

func appendLines(path string, lines []string) {
	if len(lines) == 0 {
		return
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	for _, l := range lines {
		f.WriteString(l + "\n")
	}
}

// snapshot saves the current table and writes the rollback script. firewalld changes are
// made at runtime only, so `firewall-cmd --reload` undoes them.
func snapshot(ctx context.Context) error {
	cur, err := run(ctx, "nft", "list", "table", "inet", "berth")
	if err != nil {
		cur = "" // no table yet
	}
	if err := os.WriteFile(filepath.Join(Dir, "before.nft"), []byte(cur), 0o600); err != nil {
		return err
	}
	os.Remove(filepath.Join(Dir, "pending-firewalld"))
	script := `#!/bin/sh
# Written by Berth: restores the firewall as it was before an unconfirmed change.
D=` + Dir + `
nft delete table inet berth 2>/dev/null
[ -s "$D/before.nft" ] && nft -f "$D/before.nft"
if command -v firewall-cmd >/dev/null 2>&1 && [ "$(firewall-cmd --state 2>/dev/null)" = running ] && [ -s "$D/pending-firewalld" ]; then
  firewall-cmd --reload
fi
rm -f "$D/pending.nft" "$D/pending-firewalld" "$D/rollback-at"
echo "rolled back $(date -Is)" >> "$D/rollback.log"
`
	return os.WriteFile(filepath.Join(Dir, "rollback.sh"), []byte(script), 0o700)
}

// Confirm keeps the pending change: cancels the timer, saves the table for boot and makes
// the firewalld changes permanent.
func Confirm(ctx context.Context) error {
	run(ctx, "systemctl", "stop", rollbackUnit+".timer")
	if b, err := os.ReadFile(filepath.Join(Dir, "pending.nft")); err == nil {
		if err := os.WriteFile(filepath.Join(Dir, "boot.nft"), b, 0o600); err != nil {
			return err
		}
	}
	if b, err := os.ReadFile(filepath.Join(Dir, "pending-firewalld")); err == nil && firewalldRunning(ctx) {
		for _, op := range strings.Split(strings.TrimSpace(string(b)), "\n") {
			if op != "" {
				run(ctx, "firewall-cmd", "--permanent", op)
			}
		}
	}
	for _, f := range []string{"pending.nft", "pending-firewalld", "rollback-at"} {
		os.Remove(filepath.Join(Dir, f))
	}
	return nil
}

// Rollback restores now (the "undo now" button).
func Rollback(ctx context.Context) error {
	run(ctx, "systemctl", "stop", rollbackUnit+".timer")
	if _, err := os.Stat(filepath.Join(Dir, "rollback.sh")); err != nil {
		return nil
	}
	if out, err := run(ctx, "/bin/sh", filepath.Join(Dir, "rollback.sh")); err != nil {
		return fmt.Errorf("rollback: %s", out)
	}
	return nil
}

// Boot loads the confirmed table after a restart (nftables rules do not survive one).
func Boot(ctx context.Context) {
	if !has("nft") {
		return
	}
	if _, err := run(ctx, "nft", "list", "table", "inet", "berth"); err == nil {
		return // already there (agent restart, not a reboot)
	}
	if b, err := os.ReadFile(filepath.Join(Dir, "boot.nft")); err == nil && len(b) > 0 {
		runStdin(ctx, string(b), "nft", "-f", "-")
	}
}

// SetBans replaces the ban sets. Values are seconds left; a negative value means
// permanent (no timeout).
func SetBans(ctx context.Context, bans map[string]int64) error {
	if err := EnsureTable(ctx); err != nil {
		return err
	}
	var v4, v6 []string
	for addr, secs := range bans {
		if secs == 0 {
			continue
		}
		if _, _, err := net.ParseCIDR(addr); err != nil && net.ParseIP(addr) == nil {
			continue
		}
		e := addr
		if secs > 0 {
			e = fmt.Sprintf("%s timeout %ds", addr, secs)
		}
		if strings.Contains(addr, ":") {
			v6 = append(v6, e)
		} else {
			v4 = append(v4, e)
		}
	}
	sort.Strings(v4)
	sort.Strings(v6)
	var b strings.Builder
	b.WriteString("flush set inet berth ban_v4\nflush set inet berth ban_v6\n")
	if len(v4) > 0 {
		fmt.Fprintf(&b, "add element inet berth ban_v4 { %s }\n", strings.Join(v4, ", "))
	}
	if len(v6) > 0 {
		fmt.Fprintf(&b, "add element inet berth ban_v6 { %s }\n", strings.Join(v6, ", "))
	}
	if out, err := runStdin(ctx, b.String(), "nft", "-f", "-"); err != nil {
		return fmt.Errorf("nft: %s", out)
	}
	return nil
}
