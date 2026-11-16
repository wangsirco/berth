// Package traffic counts traffic per container, summarizes live connections (who talks
// to which port) and limits container bandwidth with tc (方案 §5.12.2).
package traffic

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// ProcRoot and SysRoot are variables for tests.
var (
	ProcRoot = "/proc"
	SysRoot  = "/sys"
)

// Counters are cumulative bytes as the container sees them: RX is what it downloaded,
// TX what it sent. They restart from zero when the container restarts.
type Counters struct {
	Name string `json:"name"`
	ID   string `json:"id"`
	RX   uint64 `json:"rx"`
	TX   uint64 `json:"tx"`
}

// netDev sums /proc/<pid>/net/dev over all interfaces but lo.
func netDev(pid int) (rx, tx uint64, err error) {
	f, err := os.Open(filepath.Join(ProcRoot, strconv.Itoa(pid), "net", "dev"))
	if err != nil {
		return 0, 0, err
	}
	defer f.Close()
	return parseNetDev(f)
}

func parseNetDev(r io.Reader) (rx, tx uint64, err error) {
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		name, rest, ok := strings.Cut(sc.Text(), ":")
		if !ok || strings.TrimSpace(name) == "lo" {
			continue
		}
		f := strings.Fields(rest)
		if len(f) < 9 {
			continue
		}
		a, _ := strconv.ParseUint(f[0], 10, 64)
		b, _ := strconv.ParseUint(f[8], 10, 64)
		rx, tx = rx+a, tx+b
	}
	return rx, tx, sc.Err()
}

// hostVeth finds the host side of a container's eth0 (its peer ifindex → host interface).
func hostVeth(pid int) (string, error) {
	b, err := os.ReadFile(filepath.Join(ProcRoot, strconv.Itoa(pid), "root", "sys", "class", "net", "eth0", "iflink"))
	if err != nil {
		return "", err
	}
	want := strings.TrimSpace(string(b))
	ents, err := os.ReadDir(filepath.Join(SysRoot, "class", "net"))
	if err != nil {
		return "", err
	}
	for _, e := range ents {
		if idx, err := os.ReadFile(filepath.Join(SysRoot, "class", "net", e.Name(), "ifindex")); err == nil && strings.TrimSpace(string(idx)) == want {
			return e.Name(), nil
		}
	}
	return "", fmt.Errorf("host side of eth0 not found")
}

// ---- connections ----

// Conn is one conntrack entry.
type Conn struct {
	Proto      string
	Src, Dst   string // original direction
	Sport      int
	Dport      int
	ReplySrc   string // differs from Dst when DNAT sent it to a container
	BytesOrig  uint64
	BytesReply uint64
}

// ParseConntrack reads /proc/net/nf_conntrack lines.
func ParseConntrack(r io.Reader) []Conn {
	var out []Conn
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 6 {
			continue
		}
		c := Conn{Proto: f[2]}
		seen := map[string]int{}
		for _, kv := range f[3:] {
			k, v, ok := strings.Cut(kv, "=")
			if !ok {
				continue
			}
			n := seen[k]
			seen[k]++
			switch {
			case k == "src" && n == 0:
				c.Src = v
			case k == "dst" && n == 0:
				c.Dst = v
			case k == "sport" && n == 0:
				c.Sport, _ = strconv.Atoi(v)
			case k == "dport" && n == 0:
				c.Dport, _ = strconv.Atoi(v)
			case k == "src" && n == 1:
				c.ReplySrc = v
			case k == "bytes" && n == 0:
				c.BytesOrig, _ = strconv.ParseUint(v, 10, 64)
			case k == "bytes" && n == 1:
				c.BytesReply, _ = strconv.ParseUint(v, 10, 64)
			}
		}
		if c.Src != "" && c.Dport > 0 {
			out = append(out, c)
		}
	}
	return out
}

// Summary is the live view: connections coming in, grouped by port and by peer.
type Summary struct {
	Acct    bool      `json:"acct"` // byte counters enabled (nf_conntrack_acct)
	Inbound []PortUse `json:"inbound"`
	Peers   []PeerUse `json:"peers"`
	Total   int       `json:"total"`
}

type PortUse struct {
	Proto string `json:"proto"`
	Port  int    `json:"port"`
	ToIP  string `json:"to_ip,omitempty"` // container address when Docker forwarded it
	Conns int    `json:"conns"`
	Peers int    `json:"peers"`
	Bytes uint64 `json:"bytes"`
}

type PeerUse struct {
	IP    string `json:"ip"`
	Conns int    `json:"conns"`
	Bytes uint64 `json:"bytes"`
	Ports []int  `json:"ports"`
}

func localAddrs() map[string]bool {
	out := map[string]bool{}
	addrs, _ := net.InterfaceAddrs()
	for _, a := range addrs {
		if ip, _, err := net.ParseCIDR(a.String()); err == nil {
			out[ip.String()] = true
		}
	}
	return out
}

// Summarize keeps connections that came from outside to this host (or through it to a
// container), skipping the host's own outgoing traffic and container-to-container chatter.
func Summarize(conns []Conn, local map[string]bool, acct bool) Summary {
	s := Summary{Acct: acct, Inbound: []PortUse{}, Peers: []PeerUse{}}
	ports := map[string]*PortUse{}
	peerSets := map[string]map[string]bool{}
	peers := map[string]*PeerUse{}
	for _, c := range conns {
		src := net.ParseIP(c.Src)
		if !local[c.Dst] || local[c.Src] || src == nil || src.IsLoopback() || src.IsMulticast() {
			continue
		}
		s.Total++
		b := c.BytesOrig + c.BytesReply
		key := c.Proto + "/" + strconv.Itoa(c.Dport)
		p := ports[key]
		if p == nil {
			p = &PortUse{Proto: c.Proto, Port: c.Dport}
			ports[key] = p
			peerSets[key] = map[string]bool{}
		}
		if c.ReplySrc != "" && c.ReplySrc != c.Dst {
			p.ToIP = c.ReplySrc
		}
		p.Conns++
		p.Bytes += b
		peerSets[key][c.Src] = true
		pe := peers[c.Src]
		if pe == nil {
			pe = &PeerUse{IP: c.Src}
			peers[c.Src] = pe
		}
		pe.Conns++
		pe.Bytes += b
		if !containsInt(pe.Ports, c.Dport) {
			pe.Ports = append(pe.Ports, c.Dport)
		}
	}
	for k, p := range ports {
		p.Peers = len(peerSets[k])
		s.Inbound = append(s.Inbound, *p)
	}
	for _, p := range peers {
		sort.Ints(p.Ports)
		s.Peers = append(s.Peers, *p)
	}
	sort.Slice(s.Inbound, func(i, j int) bool {
		if s.Inbound[i].Conns != s.Inbound[j].Conns {
			return s.Inbound[i].Conns > s.Inbound[j].Conns
		}
		return s.Inbound[i].Port < s.Inbound[j].Port
	})
	sort.Slice(s.Peers, func(i, j int) bool {
		if s.Peers[i].Bytes != s.Peers[j].Bytes {
			return s.Peers[i].Bytes > s.Peers[j].Bytes
		}
		return s.Peers[i].Conns > s.Peers[j].Conns
	})
	if len(s.Peers) > 50 {
		s.Peers = s.Peers[:50]
	}
	return s
}

func containsInt(l []int, v int) bool {
	for _, x := range l {
		if x == v {
			return true
		}
	}
	return false
}

// Live reads conntrack now.
func Live() (Summary, error) {
	f, err := os.Open(filepath.Join(ProcRoot, "net", "nf_conntrack"))
	if err != nil {
		return Summary{}, err
	}
	defer f.Close()
	return Summarize(ParseConntrack(io.LimitReader(f, 32<<20)), localAddrs(), acctOn()), nil
}

func acctOn() bool {
	b, err := os.ReadFile(filepath.Join(ProcRoot, "sys", "net", "netfilter", "nf_conntrack_acct"))
	return err == nil && strings.TrimSpace(string(b)) == "1"
}

// ---- limits (tc) ----

// Limit caps one container; 0 means no cap in that direction.
type Limit struct {
	Container string `json:"container"`
	UpKbit    int    `json:"up_kbit"`   // what the container sends
	DownKbit  int    `json:"down_kbit"` // what it receives
}

var run = func(ctx context.Context, name string, args ...string) (string, error) {
	var out bytes.Buffer
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	return strings.TrimSpace(out.String()), err
}

// TCCommands are the tc invocations that put l on dev (also what the plan shows).
func TCCommands(dev string, l Limit) [][]string {
	var cmds [][]string
	// downloads leave the host through the veth: shape them (HTB + fq_codel)
	if l.DownKbit > 0 {
		r := strconv.Itoa(l.DownKbit) + "kbit"
		cmds = append(cmds,
			[]string{"qdisc", "replace", "dev", dev, "root", "handle", "1:", "htb", "default", "10"},
			[]string{"class", "replace", "dev", dev, "parent", "1:", "classid", "1:10", "htb", "rate", r, "ceil", r},
			[]string{"qdisc", "replace", "dev", dev, "parent", "1:10", "handle", "10:", "fq_codel"})
	} else {
		cmds = append(cmds, []string{"qdisc", "del", "dev", dev, "root"})
	}
	// uploads arrive on the veth: police them
	cmds = append(cmds, []string{"qdisc", "del", "dev", dev, "ingress"})
	if l.UpKbit > 0 {
		burst := max(l.UpKbit/8, 16) // ~1 s worth, at least 16 KB
		cmds = append(cmds,
			[]string{"qdisc", "add", "dev", dev, "handle", "ffff:", "ingress"},
			[]string{"filter", "add", "dev", dev, "parent", "ffff:", "protocol", "all", "prio", "1", "matchall",
				"action", "police", "rate", strconv.Itoa(l.UpKbit) + "kbit", "burst", strconv.Itoa(burst) + "kb", "drop"})
	}
	return cmds
}

var (
	mu      sync.Mutex
	applied = map[string]string{} // container -> veth it was put on
)

// StateFile remembers which containers carry limits (to clear them when dropped).
var StateFile = "/var/lib/berth/traffic-limits.json"

// Apply puts exactly these limits in place (containers no longer listed are cleared).
// Veths change when containers restart, so the panel calls this every minute.
func Apply(ctx context.Context, limits []Limit, pidOf func(ctx context.Context, name string) (int, error)) map[string]string {
	mu.Lock()
	defer mu.Unlock()
	if len(applied) == 0 {
		if b, err := os.ReadFile(StateFile); err == nil {
			json.Unmarshal(b, &applied)
		}
	}
	errs := map[string]string{}
	want := map[string]bool{}
	for _, l := range limits {
		want[l.Container] = true
		pid, err := pidOf(ctx, l.Container)
		if err != nil || pid == 0 {
			errs[l.Container] = "not running"
			continue
		}
		dev, err := hostVeth(pid)
		if err != nil {
			errs[l.Container] = err.Error()
			continue
		}
		for _, c := range TCCommands(dev, l) {
			out, err := run(ctx, "tc", c...)
			if err != nil && c[1] != "del" { // deleting what is not there is fine
				errs[l.Container] = "tc " + strings.Join(c, " ") + ": " + out
				break
			}
		}
		applied[l.Container] = dev
	}
	for name, dev := range applied {
		if !want[name] {
			run(ctx, "tc", "qdisc", "del", "dev", dev, "root")
			run(ctx, "tc", "qdisc", "del", "dev", dev, "ingress")
			delete(applied, name)
		}
	}
	if b, err := json.Marshal(applied); err == nil {
		os.WriteFile(StateFile, b, 0o600)
	}
	return errs
}
