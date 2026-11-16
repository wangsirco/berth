package system

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

type Addr struct {
	IP      string `json:"ip"`
	Prefix  int    `json:"prefix"`
	Family  string `json:"family"` // 4 | 6
	Scope   string `json:"scope"`  // global | link | host
	Dynamic bool   `json:"dynamic"`
	Public  bool   `json:"public"` // a global address outside private ranges
}

type NIC struct {
	Name      string   `json:"name"`
	Kind      string   `json:"kind"`     // ethernet | wifi | bridge | vpn | docker | incus | veth | loopback | virtual
	Physical  bool     `json:"physical"` // a real card
	Owner     string   `json:"owner"`    // who made it: system | docker | incus | tailscale | wireguard
	State     string   `json:"state"`    // UP | DOWN | UNKNOWN …
	Carrier   bool     `json:"carrier"`  // cable in / associated
	MAC       string   `json:"mac,omitempty"`
	MTU       int      `json:"mtu"`
	SpeedMbps int      `json:"speed_mbps,omitempty"`
	Driver    string   `json:"driver,omitempty"`
	Master    string   `json:"master,omitempty"` // bridge it belongs to
	Addrs     []Addr   `json:"addrs"`
	Default   bool     `json:"default"` // carries a default route
	Gateway   string   `json:"gateway,omitempty"`
	RXBytes   int64    `json:"rx_bytes"`
	TXBytes   int64    `json:"tx_bytes"`
	RXErrors  int64    `json:"rx_errors"`
	TXErrors  int64    `json:"tx_errors"`
	RXDropped int64    `json:"rx_dropped"`
	TXDropped int64    `json:"tx_dropped"`
	Conn      *NMConn  `json:"conn,omitempty"` // NetworkManager profile on it
	SSID      string   `json:"ssid,omitempty"`
	Flags     []string `json:"flags,omitempty"`
}

// NMConn: the NetworkManager profile settings the panel can edit.
type NMConn struct {
	Name      string   `json:"name"`
	UUID      string   `json:"uuid"`
	Method4   string   `json:"method4"` // auto | manual | disabled | link-local | shared
	Addrs4    []string `json:"addrs4"`  // "ip/prefix"
	Gateway4  string   `json:"gateway4,omitempty"`
	DNS4      []string `json:"dns4"`
	Method6   string   `json:"method6"`
	AutoStart bool     `json:"autoconnect"`
}

type ipAddrJSON struct {
	Ifname    string   `json:"ifname"`
	Flags     []string `json:"flags"`
	MTU       int      `json:"mtu"`
	Operstate string   `json:"operstate"`
	Address   string   `json:"address"`
	Master    string   `json:"master"`
	AddrInfo  []struct {
		Family    string `json:"family"`
		Local     string `json:"local"`
		Prefixlen int    `json:"prefixlen"`
		Scope     string `json:"scope"`
		Dynamic   bool   `json:"dynamic"`
	} `json:"addr_info"`
	LinkInfo *struct {
		InfoKind string `json:"info_kind"`
	} `json:"linkinfo"`
}

type routeJSON struct {
	Dst      string `json:"dst"`
	Gateway  string `json:"gateway"`
	Dev      string `json:"dev"`
	Protocol string `json:"protocol"`
	Metric   int    `json:"metric"`
	Prefsrc  string `json:"prefsrc"`
	Scope    string `json:"scope"`
	Table    string `json:"table"`
}

var privateRe = regexp.MustCompile(`^(10\.|192\.168\.|172\.(1[6-9]|2\d|3[01])\.|100\.(6[4-9]|[7-9]\d|1[01]\d|12[0-7])\.|169\.254\.|f[cd]|fe80:)`)

func readInt(path string) int64 {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	n, _ := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
	return n
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

// NICs lists every interface with its addresses, counters and NetworkManager profile.
func NICs(ctx context.Context) ([]NIC, error) {
	out, err := run(ctx, "ip", "-j", "-d", "addr")
	if err != nil {
		return nil, err
	}
	var raw []ipAddrJSON
	if err := json.Unmarshal([]byte(out), &raw); err != nil {
		return nil, err
	}
	defaults := map[string]string{}
	for _, fam := range []string{"-4", "-6"} {
		if out, err := run(ctx, "ip", "-j", fam, "route", "show", "default"); err == nil {
			var rs []routeJSON
			json.Unmarshal([]byte(out), &rs)
			for _, r := range rs {
				if _, ok := defaults[r.Dev]; !ok {
					defaults[r.Dev] = r.Gateway
				}
			}
		}
	}
	conns := nmDevices(ctx)
	list := make([]NIC, 0, len(raw))
	for _, r := range raw {
		sys := "/sys/class/net/" + r.Ifname
		n := NIC{Name: r.Ifname, State: r.Operstate, MAC: r.Address, MTU: r.MTU, Master: r.Master, Flags: r.Flags, Addrs: []Addr{}}
		n.Physical = exists(sys + "/device")
		n.Carrier = readInt(sys+"/carrier") == 1
		if s := readInt(sys + "/speed"); s > 0 && s < 1000000 {
			n.SpeedMbps = int(s)
		}
		if l, err := os.Readlink(sys + "/device/driver"); err == nil {
			n.Driver = filepath.Base(l)
		}
		st := sys + "/statistics/"
		n.RXBytes, n.TXBytes = readInt(st+"rx_bytes"), readInt(st+"tx_bytes")
		n.RXErrors, n.TXErrors = readInt(st+"rx_errors"), readInt(st+"tx_errors")
		n.RXDropped, n.TXDropped = readInt(st+"rx_dropped"), readInt(st+"tx_dropped")
		kind := ""
		if r.LinkInfo != nil {
			kind = r.LinkInfo.InfoKind
		}
		n.Kind, n.Owner = classify(r.Ifname, kind, n.Physical, exists(sys+"/wireless") || exists(sys+"/phy80211"), exists(sys+"/bridge"))
		for _, a := range r.AddrInfo {
			fam := "4"
			if a.Family == "inet6" {
				fam = "6"
			}
			n.Addrs = append(n.Addrs, Addr{IP: a.Local, Prefix: a.Prefixlen, Family: fam, Scope: a.Scope, Dynamic: a.Dynamic,
				Public: a.Scope == "global" && !privateRe.MatchString(a.Local)})
		}
		if gw, ok := defaults[r.Ifname]; ok {
			n.Default, n.Gateway = true, gw
		}
		if c, ok := conns[r.Ifname]; ok {
			n.Conn = nmConn(ctx, c)
			if n.Kind == "wifi" {
				n.SSID, _ = run(ctx, "nmcli", "-t", "-g", "802-11-wireless.ssid", "connection", "show", c)
			}
		}
		list = append(list, n)
	}
	order := map[string]int{"ethernet": 0, "wifi": 1, "vpn": 2, "bridge": 3, "incus": 4, "docker": 5, "virtual": 6, "veth": 7, "loopback": 8}
	sort.SliceStable(list, func(i, j int) bool {
		if list[i].Default != list[j].Default {
			return list[i].Default
		}
		return order[list[i].Kind] < order[list[j].Kind]
	})
	return list, nil
}

func classify(name, kind string, physical, wifi, bridge bool) (string, string) {
	switch {
	case name == "lo":
		return "loopback", "system"
	case wifi:
		return "wifi", "system"
	case strings.HasPrefix(name, "tailscale"):
		return "vpn", "tailscale"
	case kind == "wireguard" || strings.HasPrefix(name, "wg"):
		return "vpn", "wireguard"
	case kind == "tun" || strings.HasPrefix(name, "tun"):
		return "vpn", "system"
	case name == "docker0" || strings.HasPrefix(name, "br-"):
		return "docker", "docker"
	case strings.HasPrefix(name, "incusbr") || strings.HasPrefix(name, "tap"):
		return "incus", "incus"
	case kind == "veth" || strings.HasPrefix(name, "veth"):
		return "veth", "docker"
	case bridge || kind == "bridge":
		return "bridge", "system"
	case physical:
		return "ethernet", "system"
	}
	return "virtual", "system"
}

// nmDevices: device -> active NetworkManager connection name.
func nmDevices(ctx context.Context) map[string]string {
	out := map[string]string{}
	if !has("nmcli") {
		return out
	}
	s, err := run(ctx, "nmcli", "-t", "-f", "DEVICE,CONNECTION", "device")
	if err != nil {
		return out
	}
	for _, l := range strings.Split(s, "\n") {
		dev, conn, ok := strings.Cut(l, ":")
		if ok && conn != "" && conn != "--" {
			out[dev] = strings.ReplaceAll(conn, `\:`, ":")
		}
	}
	return out
}

func nmConn(ctx context.Context, name string) *NMConn {
	s, err := run(ctx, "nmcli", "-t", "-f", "connection.id,connection.uuid,connection.autoconnect,ipv4.method,ipv4.addresses,ipv4.gateway,ipv4.dns,ipv6.method", "connection", "show", name)
	if err != nil {
		return nil
	}
	c := &NMConn{Name: name, Addrs4: []string{}, DNS4: []string{}}
	for _, l := range strings.Split(s, "\n") {
		k, v, _ := strings.Cut(l, ":")
		v = strings.ReplaceAll(v, `\:`, ":")
		switch k {
		case "connection.uuid":
			c.UUID = v
		case "connection.autoconnect":
			c.AutoStart = v == "yes"
		case "ipv4.method":
			c.Method4 = v
		case "ipv4.addresses":
			c.Addrs4 = splitList(v)
		case "ipv4.gateway":
			if v != "--" {
				c.Gateway4 = v
			}
		case "ipv4.dns":
			c.DNS4 = splitList(v)
		case "ipv6.method":
			c.Method6 = v
		}
	}
	return c
}

func splitList(v string) []string {
	out := []string{}
	for _, x := range strings.Split(v, ",") {
		if x = strings.TrimSpace(x); x != "" && x != "--" {
			out = append(out, x)
		}
	}
	return out
}

// ---- routes and resolvers ----

type Route struct {
	Dst     string `json:"dst"`
	Gateway string `json:"gateway,omitempty"`
	Dev     string `json:"dev"`
	Proto   string `json:"proto,omitempty"`
	Metric  int    `json:"metric,omitempty"`
	Src     string `json:"src,omitempty"`
	Family  string `json:"family"`
}

func Routes(ctx context.Context) []Route {
	out := []Route{}
	for _, fam := range []string{"4", "6"} {
		s, err := run(ctx, "ip", "-j", "-"+fam, "route")
		if err != nil {
			continue
		}
		var rs []routeJSON
		json.Unmarshal([]byte(s), &rs)
		for _, r := range rs {
			out = append(out, Route{Dst: r.Dst, Gateway: r.Gateway, Dev: r.Dev, Proto: r.Protocol, Metric: r.Metric, Src: r.Prefsrc, Family: fam})
		}
	}
	return out
}

type Resolver struct {
	Servers []string            `json:"servers"`
	Search  []string            `json:"search"`
	Via     string              `json:"via"` // systemd-resolved | resolv.conf
	PerLink map[string][]string `json:"per_link,omitempty"`
}

func Resolvers(ctx context.Context) Resolver {
	r := Resolver{Servers: []string{}, Search: []string{}, Via: "resolv.conf", PerLink: map[string][]string{}}
	if out, err := run(ctx, "resolvectl", "dns"); err == nil {
		r.Via = "systemd-resolved"
		for _, l := range strings.Split(out, "\n") {
			k, v, ok := strings.Cut(l, ":")
			if !ok {
				continue
			}
			servers := strings.Fields(v)
			if strings.HasPrefix(k, "Global") {
				r.Servers = append(r.Servers, servers...)
			} else if m := regexp.MustCompile(`\(([^)]+)\)`).FindStringSubmatch(k); m != nil && len(servers) > 0 {
				r.PerLink[m[1]] = servers
			}
		}
		if out, err := run(ctx, "resolvectl", "domain"); err == nil {
			for _, l := range strings.Split(out, "\n") {
				if _, v, ok := strings.Cut(l, ":"); ok {
					for _, d := range strings.Fields(v) {
						if !contains(r.Search, d) {
							r.Search = append(r.Search, d)
						}
					}
				}
			}
		}
		return r
	}
	if b, err := os.ReadFile("/etc/resolv.conf"); err == nil {
		for _, l := range strings.Split(string(b), "\n") {
			f := strings.Fields(l)
			if len(f) >= 2 && f[0] == "nameserver" {
				r.Servers = append(r.Servers, f[1])
			}
			if len(f) >= 2 && f[0] == "search" {
				r.Search = append(r.Search, f[1:]...)
			}
		}
	}
	return r
}
