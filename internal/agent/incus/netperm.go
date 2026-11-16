package incus

// What a machine may reach and do. Networks: NAT behind this server (the default; others reach
// it through port forwards), its own address on the LAN (macvlan on the wired port, or the
// host's own bridge when there is one), or no network. In NAT mode a per-machine firewall
// (an Incus network ACL on incusbr0) limits where it may connect. Permissions: start on boot,
// containers that run Docker inside (nesting) or as root on the host (privileged), and host
// folders shared into the machine.

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"os"
	"path"
	"regexp"
	"sort"
	"strings"
)

// NetState: how a machine is connected, read from its eth0.
type NetState struct {
	Mode   string `json:"mode"`             // nat | lan | isolated | custom
	Parent string `json:"parent,omitempty"` // lan: the host interface
	Egress string `json:"egress"`           // all | lan | none (nat only)
	MAC    string `json:"mac,omitempty"`
}

// Share is a host folder seen inside the machine.
type Share struct {
	ID       string `json:"id"`
	Source   string `json:"source"`
	Path     string `json:"path"`
	ReadOnly bool   `json:"readonly"`
}

type Perms struct {
	Autostart  bool    `json:"autostart"`
	Nesting    bool    `json:"nesting"`
	Privileged bool    `json:"privileged"`
	Shares     []Share `json:"shares"`
}

func readNet(r rawInstance) NetState {
	n := NetState{Mode: "isolated", Egress: firstNonEmpty(r.Config["user.berth.egress"], "all"), MAC: r.Config["volatile.eth0.hwaddr"]}
	d, ok := r.ExpandedDevices["eth0"]
	switch {
	case !ok || d["type"] == "none":
	case d["nictype"] == "macvlan" || d["nictype"] == "bridged":
		n.Mode, n.Parent, n.Egress = "lan", d["parent"], "all"
	case d["network"] == defaultNetwork:
		n.Mode = "nat"
	default:
		n.Mode = "custom"
	}
	return n
}

func readPerms(r rawInstance) Perms {
	p := Perms{Autostart: r.Config["boot.autostart"] == "true", Nesting: r.Config["security.nesting"] == "true", Privileged: r.Config["security.privileged"] == "true", Shares: []Share{}}
	for name, d := range r.ExpandedDevices {
		if d["type"] == "disk" && strings.HasPrefix(name, "berth-share-") {
			p.Shares = append(p.Shares, Share{ID: strings.TrimPrefix(name, "berth-share-"), Source: d["source"], Path: d["path"], ReadOnly: d["readonly"] == "true"})
		}
	}
	sort.Slice(p.Shares, func(i, j int) bool { return p.Shares[i].Path < p.Shares[j].Path })
	return p
}

const defaultNetwork = "incusbr0"

// LANPort is a host interface a machine can sit on directly.
type LANPort struct {
	Name     string `json:"name"`
	Wireless bool   `json:"wireless"` // macvlan does not work over Wi-Fi
	Bridge   bool   `json:"bridge"`   // a host bridge: the host and the machine can talk
	Up       bool   `json:"up"`
	Address  string `json:"address,omitempty"`
	Default  bool   `json:"default"` // carries the default route
}

var skipIface = regexp.MustCompile(`^(lo|incusbr\d*|docker\d*|br-[0-9a-f]+|veth.*|tap.*|tun.*|tailscale.*|wg.*|virbr.*|p2p-.*|macvlan.*|mac[0-9a-f]+)$`)

// LANPorts: wired interfaces and host bridges, the one with the default route first.
func LANPorts() []LANPort {
	def := ""
	if b, err := os.ReadFile("/proc/net/route"); err == nil {
		for _, line := range strings.Split(string(b), "\n")[1:] {
			if f := strings.Fields(line); len(f) > 2 && f[1] == "00000000" {
				def = f[0]
				break
			}
		}
	}
	ents, _ := os.ReadDir("/sys/class/net")
	out := []LANPort{}
	for _, e := range ents {
		n := e.Name()
		if skipIface.MatchString(n) {
			continue
		}
		base := "/sys/class/net/" + n
		_, wl := os.Stat(base + "/wireless")
		_, br := os.Stat(base + "/bridge")
		_, dev := os.Stat(base + "/device")
		if dev != nil && br != nil { // neither hardware nor a bridge (dummy, bond slaves…)
			continue
		}
		p := LANPort{Name: n, Wireless: wl == nil, Bridge: br == nil, Default: n == def}
		state, _ := os.ReadFile(base + "/operstate")
		p.Up = strings.TrimSpace(string(state)) == "up"
		if ifc, err := net.InterfaceByName(n); err == nil {
			addrs, _ := ifc.Addrs()
			for _, a := range addrs {
				if ip, _, err := net.ParseCIDR(a.String()); err == nil && ip.To4() != nil {
					p.Address = ip.String()
					break
				}
			}
		}
		out = append(out, p)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Default && !out[j].Default })
	return out
}

func (c *Client) raw(ctx context.Context, name string) (map[string]any, error) {
	var cur map[string]any
	_, err := c.do(ctx, "GET", "/1.0/instances/"+url.PathEscape(name), nil, &cur)
	return cur, err
}

// put writes the instance back with fn applied to its own config and devices.
func (c *Client) put(ctx context.Context, name string, fn func(cfg map[string]any, devs map[string]any)) error {
	cur, err := c.raw(ctx, name)
	if err != nil {
		return err
	}
	cfg, _ := cur["config"].(map[string]any)
	if cfg == nil {
		cfg = map[string]any{}
	}
	devs, _ := cur["devices"].(map[string]any)
	if devs == nil {
		devs = map[string]any{}
	}
	fn(cfg, devs)
	cur["config"], cur["devices"] = cfg, devs
	body := map[string]any{}
	for _, k := range []string{"architecture", "config", "devices", "ephemeral", "profiles", "stateful", "description"} {
		if v, ok := cur[k]; ok {
			body[k] = v
		}
	}
	e, err := c.do(ctx, "PUT", "/1.0/instances/"+url.PathEscape(name), body, nil)
	if err != nil {
		return err
	}
	return c.wait(ctx, e)
}

func aclName(inst string) string { return "berth-" + inst }

// gateway: incusbr0's own address (DNS and DHCP for the machines behind it).
func (c *Client) gateway(ctx context.Context) string {
	var n struct {
		Config map[string]string `json:"config"`
	}
	if _, err := c.do(ctx, "GET", "/1.0/networks/"+defaultNetwork, nil, &n); err != nil {
		return ""
	}
	ip, _, _ := strings.Cut(n.Config["ipv4.address"], "/")
	return ip
}

// setACL writes the machine's outbound rules (lan: private ranges only; none: just the gateway).
func (c *Client) setACL(ctx context.Context, inst, egress string) error {
	name := aclName(inst)
	gw := c.gateway(ctx)
	var rules []map[string]string
	switch egress {
	case "lan":
		for _, cidr := range []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "169.254.0.0/16", "100.64.0.0/10"} {
			rules = append(rules, map[string]string{"action": "allow", "state": "enabled", "destination": cidr, "description": "LAN"})
		}
	case "none":
		if gw != "" {
			rules = append(rules, map[string]string{"action": "allow", "state": "enabled", "destination": gw + "/32", "description": "DNS and DHCP"})
		}
	}
	body := map[string]any{"description": "Berth: what " + inst + " may reach", "egress": rules, "ingress": []any{}, "config": map[string]string{}}
	if _, err := c.do(ctx, "GET", "/1.0/network-acls/"+name, nil, nil); err != nil {
		body["name"] = name
		_, err := c.do(ctx, "POST", "/1.0/network-acls", body, nil)
		return err
	}
	_, err := c.do(ctx, "PUT", "/1.0/network-acls/"+name, body, nil)
	return err
}

func (c *Client) dropACL(ctx context.Context, inst string) {
	c.do(ctx, "DELETE", "/1.0/network-acls/"+aclName(inst), nil, nil)
}

var forwardRe = regexp.MustCompile(`^berth-fwd-`)

// SetNetwork connects a machine as asked and restarts it when it runs. In LAN and isolated
// modes the port forwards cannot reach it: they are removed and returned (for undo).
func (c *Client) SetNetwork(ctx context.Context, name, mode, parent, egress string) ([]Forward, error) {
	inst, err := c.Instance(ctx, name)
	if err != nil {
		return nil, err
	}
	var removed []Forward
	switch mode {
	case "nat":
		if egress != "all" && egress != "lan" && egress != "none" {
			return nil, &Error{Status: 400, Message: "egress: all, lan or none"}
		}
		if egress != "all" {
			if err := c.setACL(ctx, name, egress); err != nil {
				return nil, err
			}
		}
	case "lan":
		ok := false
		for _, p := range LANPorts() {
			if p.Name == parent {
				if p.Wireless && !p.Bridge {
					return nil, &Error{Status: 400, Message: "macvlan does not work over Wi-Fi; use a wired port or a bridge"}
				}
				ok = true
			}
		}
		if !ok {
			return nil, &Error{Status: 400, Message: "no such LAN port: " + parent}
		}
		removed = inst.Forwards
	case "isolated":
		removed = inst.Forwards
	default:
		return nil, &Error{Status: 400, Message: "mode: nat, lan or isolated"}
	}
	bridge := false
	if _, err := os.Stat("/sys/class/net/" + parent + "/bridge"); err == nil {
		bridge = true
	}
	err = c.put(ctx, name, func(cfg map[string]any, devs map[string]any) {
		for k := range devs {
			if forwardRe.MatchString(k) && mode != "nat" {
				delete(devs, k)
			}
		}
		switch mode {
		case "nat":
			d := map[string]any{"type": "nic", "network": defaultNetwork, "name": "eth0"}
			if egress != "all" {
				d["security.acls"] = aclName(name)
				d["security.acls.default.egress.action"] = "reject"
				d["security.acls.default.ingress.action"] = "allow"
			}
			devs["eth0"] = d
			cfg["user.berth.egress"] = egress
		case "lan":
			nictype := "macvlan"
			if bridge {
				nictype = "bridged"
			}
			devs["eth0"] = map[string]any{"type": "nic", "nictype": nictype, "parent": parent, "name": "eth0"}
			delete(cfg, "user.berth.egress")
		case "isolated":
			devs["eth0"] = map[string]any{"type": "none"}
			delete(cfg, "user.berth.egress")
		}
	})
	if err != nil {
		return nil, err
	}
	if mode != "nat" || egress == "all" {
		c.dropACL(ctx, name)
	}
	if strings.EqualFold(inst.Status, "running") {
		if err := c.SetState(ctx, name, "restart", true); err != nil {
			return removed, err
		}
	}
	return removed, nil
}

// SetPerms: start on boot always; nesting and privileged only mean something for containers.
// A running container restarts when either of those changes.
func (c *Client) SetPerms(ctx context.Context, name string, autostart, nesting, privileged bool) error {
	inst, err := c.Instance(ctx, name)
	if err != nil {
		return err
	}
	b := func(v bool) string {
		if v {
			return "true"
		}
		return "false"
	}
	ct := inst.Type == "container"
	restart := ct && (inst.Perms.Nesting != nesting || inst.Perms.Privileged != privileged)
	cfg := map[string]any{"boot.autostart": b(autostart)}
	if ct {
		cfg["security.nesting"], cfg["security.privileged"] = b(nesting), b(privileged)
		if nesting {
			cfg["security.syscalls.intercept.mknod"], cfg["security.syscalls.intercept.setxattr"] = "true", "true" // what Docker inside needs
		}
	}
	if err := c.patch(ctx, name, map[string]any{"config": cfg}); err != nil {
		return err
	}
	if restart && strings.EqualFold(inst.Status, "running") {
		return c.SetState(ctx, name, "restart", true)
	}
	return nil
}

var shareIDRe = regexp.MustCompile(`^[a-z0-9]{1,16}$`)

// forbiddenShares: host folders never handed to a machine.
var forbiddenShares = []string{"/", "/etc", "/root", "/boot", "/usr", "/bin", "/sbin", "/lib", "/proc", "/sys", "/dev", "/var/lib/berth", "/etc/berth", "/var/lib/incus"}

// SetShare adds (source set) or removes (source empty) a shared folder.
func (c *Client) SetShare(ctx context.Context, name, id, source, target string, readonly bool) error {
	if !shareIDRe.MatchString(id) {
		return &Error{Status: 400, Message: "bad share id"}
	}
	dev := "berth-share-" + id
	if source == "" {
		return c.put(ctx, name, func(_ map[string]any, devs map[string]any) { delete(devs, dev) })
	}
	src, dst := path.Clean(source), path.Clean(target)
	for _, f := range forbiddenShares {
		if src == f {
			return &Error{Status: 400, Message: src + " cannot be shared"}
		}
	}
	if !strings.HasPrefix(src, "/") || !strings.HasPrefix(dst, "/") || dst == "/" {
		return &Error{Status: 400, Message: "absolute paths, and not / inside"}
	}
	if fi, err := os.Stat(src); err != nil || !fi.IsDir() {
		return &Error{Status: 400, Message: src + " is not a folder on this server"}
	}
	inst, err := c.Instance(ctx, name)
	if err != nil {
		return err
	}
	d := map[string]string{"type": "disk", "source": src, "path": dst, "readonly": fmt.Sprint(readonly)}
	if inst.Type == "container" && !inst.Perms.Privileged {
		d["shift"] = "true" // files keep their owners inside an unprivileged container
	}
	return c.patch(ctx, name, map[string]any{"devices": map[string]any{dev: d}})
}
