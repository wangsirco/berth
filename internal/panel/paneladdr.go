package panel

import (
	"net"
	"regexp"
	"strings"

	"github.com/wangsirco/berth/internal/errs"
)

// The panel's own addresses (Settings → Access → Panel address):
//
//   - panel.lan_host: how machines on the LAN reach it, a private IP or a local name such
//     as mini.lan. Names go into the panel certificate (signed by the panel's CA).
//   - panel.public_host: how the internet reaches it, a domain or a public IP. A domain
//     can follow this server's address through DDNS.
//
// Commands that other machines run (adding a host) use them instead of whatever address
// the browser happened to use.

var dnsNameRe = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]*[a-z0-9])?\.)+[a-z][a-z0-9-]*[a-z0-9]$`)

func validLanHost(v string) error {
	if v == "" {
		return nil
	}
	if ip := net.ParseIP(v); ip != nil {
		if !privateIP(ip) || ip.IsLoopback() {
			return errs.New(400, "settings.lan_host_private")
		}
		return nil
	}
	if !localNameRe.MatchString(strings.ToLower(v)) {
		return errs.New(400, "settings.lan_host_local")
	}
	return nil
}

func validPublicHost(v string) error {
	if v == "" {
		return nil
	}
	if ip := net.ParseIP(v); ip != nil {
		if privateIP(ip) {
			return errs.New(400, "settings.public_host_bad")
		}
		return nil
	}
	if !dnsNameRe.MatchString(strings.ToLower(v)) || localNameRe.MatchString(strings.ToLower(v)) {
		return errs.New(400, "settings.public_host_bad")
	}
	return nil
}

// panelURL: https://host:port for a configured address ("" when not set).
func (a *App) panelURL(key string) string {
	h := a.setting(key)
	if h == "" {
		return ""
	}
	if a.entryServes(h) { // the edge serves this name on 443: no port
		return "https://" + h
	}
	return "https://" + net.JoinHostPort(h, a.panelPort())
}

// SetCertRefresh is called by main: re-issue the panel certificate now (after the
// names it should carry changed) instead of at the next five-minute check.
func (a *App) SetCertRefresh(f func()) { a.certRefresh = f }

// certNamesOf: access.names plus the LAN name, if that is a name.
func (a *App) certNamesOf() []string {
	out := splitList(a.setting("access.names"))
	if h := a.setting("panel.lan_host"); h != "" && net.ParseIP(h) == nil {
		out = append(out, strings.ToLower(h))
	}
	return out
}
