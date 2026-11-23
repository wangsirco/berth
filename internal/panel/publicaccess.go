package panel

import (
	"context"
	"net"
	"net/http"
	"slices"
	"time"
)

// Public access: whether the panel answers any source address, not just the allowlist.
// It is on when panel.json says so (old installs) or the access.public setting is "1"
// (the installer, `berth admin public on`, the welcome wizard and Settings set it).
// Turning it on requires a security entry; accounts with a second factor must use it,
// and those without one are asked to add one (meView.Needs2FA).
func (a *App) isPublic() bool {
	return a.cfg.Public || a.setting("access.public") == "1"
}

// privateIP: addresses that are not reachable from the internet (RFC 1918, ULA,
// link-local, loopback and the 100.64/10 range Tailscale and carriers use).
func privateIP(ip net.IP) bool {
	if ip == nil {
		return true
	}
	if ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() {
		return true
	}
	_, cgnat, _ := net.ParseCIDR("100.64.0.0/10")
	return cgnat.Contains(ip)
}

// localAddrs: every address on this machine's interfaces.
func localAddrs() []string {
	var out []string
	addrs, _ := net.InterfaceAddrs()
	for _, x := range addrs {
		if ip, _, err := net.ParseCIDR(x.String()); err == nil {
			out = append(out, ip.String())
		}
	}
	return out
}

// accessCheck is what the wizard and Settings show before asking about public access.
type accessCheck struct {
	Public       bool   `json:"public"`
	Entry        bool   `json:"entry"`                   // a security entry is set
	Port         string `json:"port"`                    // the panel's port
	PublicIP     string `json:"public_ip"`               // this server's address as the internet sees it ("" = none found)
	PublicIPv6   string `json:"public_ipv6"`             // same for IPv6
	OnInterface  bool   `json:"on_interface"`            // the public IPv4 sits on a NIC (no NAT in front)
	LanIP        string `json:"lan_ip"`                  // the address the default route leaves from
	ClientIP     string `json:"client_ip"`               // the browser asking
	ClientPublic bool   `json:"client_public"`           // ... arrived from the internet
	Firewalld    bool   `json:"firewalld"`               // firewalld is running (it blocks unknown ports)
	PortOpen     bool   `json:"port_open"`               // firewalld lets anyone reach the panel port
	Tailscale    string `json:"tailscale,omitempty"`     // this machine's Tailscale address, if it has one
	TunnelHost   string `json:"tunnel_host,omitempty"`   // the panel is published through the Cloudflare tunnel here
	TunnelEntry  bool   `json:"tunnel_entry,omitempty"`  // ...and asks for the security entry there
	TunnelAccess bool   `json:"tunnel_access,omitempty"` // ...behind Cloudflare Access
}

func (a *App) handleAccessCheck(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	out := accessCheck{Public: a.isPublic(), Entry: a.setting("access.entry") != "", Port: a.panelPort(), ClientIP: a.clientIP(r)}
	out.ClientPublic = !privateIP(net.ParseIP(out.ClientIP))
	out.TunnelHost = a.setting("access.tunnel_host")
	out.TunnelEntry = out.TunnelHost != "" && a.setting("access.tunnel_entry") != "off"
	out.TunnelAccess = a.setting("access.tunnel_access") != ""
	if ip, err := a.publicIP(ctx, "local", "A"); err == nil {
		out.PublicIP = ip
	}
	if ip, err := a.publicIP(ctx, "local", "AAAA"); err == nil {
		out.PublicIPv6 = ip
	}
	mine := localAddrs()
	out.OnInterface = out.PublicIP != "" && slices.Contains(mine, out.PublicIP)
	_, ts, _ := net.ParseCIDR("100.64.0.0/10")
	for _, s := range mine {
		if ip := net.ParseIP(s); ip != nil && ts.Contains(ip) {
			out.Tailscale = s
		}
	}
	var lan struct {
		IP string `json:"ip"`
	}
	if a.call(ctx, "local", "GET", "/v1/net/lan-ip", nil, &lan) == nil {
		out.LanIP = lan.IP
	}
	var fw struct {
		State struct {
			Firewalld bool     `json:"firewalld"`
			OpenPorts []string `json:"open_ports"`
		} `json:"state"`
	}
	if a.call(ctx, "local", "GET", "/v1/firewall", nil, &fw) == nil {
		out.Firewalld = fw.State.Firewalld
		out.PortOpen = slices.Contains(fw.State.OpenPorts, out.Port+"/tcp")
	}
	writeJSON(w, 200, out)
}

func init() {
	routeHooks = append(routeHooks, func(a *App, mux *http.ServeMux) {
		mux.HandleFunc("GET /api/v1/access/check", a.superOnly(a.handleAccessCheck))
	})
}
