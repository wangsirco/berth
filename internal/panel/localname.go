package panel

// The panel's local name (for Passkey, which refuses IP addresses): name.local answered by
// mDNS on the LAN (the host's own name, or one the person picks), signed into the panel's
// certificate. Other local suffixes (.lan, .home.arpa, .internal) are resolved by the router
// (Wharf registers them once it can; until then the guide shows how).

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/wangsirco/berth/internal/agent/system"
	"github.com/wangsirco/berth/internal/change"
	"github.com/wangsirco/berth/internal/errs"
	"github.com/wangsirco/berth/internal/panel/wharfapi"
)

// wharfDNS: the router can register this name (Wharf v2 with the dns scope and a suffix it
// serves); nil when it cannot
func (a *App) wharfDNS(ctx context.Context) (*wharfapi.Client, *wharfLink, []string) {
	c, l, err := a.wharfClient("dns")
	if err != nil {
		return nil, nil, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	n, err := c.DNSNames(ctx)
	if err != nil {
		return nil, nil, nil
	}
	if len(n.Suffixes) == 0 {
		n.Suffixes = []string{"lan"}
	}
	return c, l, n.Suffixes
}

func suffixIn(name string, suffixes []string) bool {
	for _, s := range suffixes {
		if s = strings.Trim(strings.ToLower(s), "."); s != "" && strings.HasSuffix(name, "."+s) {
			return true
		}
	}
	return false
}

// lanIP: this machine's address on the LAN (the one its default route leaves by)
func (a *App) lanIP() string {
	for gw := range defaultGateways() {
		c, err := net.Dial("udp", net.JoinHostPort(gw, "80"))
		if err != nil {
			continue
		}
		ip := c.LocalAddr().(*net.UDPAddr).IP.String()
		c.Close()
		return ip
	}
	return ""
}

func init() {
	routeHooks = append(routeHooks, func(a *App, mux *http.ServeMux) {
		mux.HandleFunc("GET /api/v1/local-name", a.authed("settings.manage", func(w http.ResponseWriter, r *http.Request) {
			ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
			defer cancel()
			var m system.MDNS
			a.call(ctx, "local", "GET", "/v1/system/mdns", nil, &m)
			_, werr := a.wharf()
			cur := a.setting("panel.lan_host")
			out := map[string]any{"hostname": m.Hostname, "suggested": firstNonEmpty(m.Hostname, "berth") + ".local", "current": cur,
				"port": a.panelPort(), "ip": a.lanIP(), "mdns": m, "wharf_paired": werr == nil, "opened_as": hostOnly(r.Host),
				"entry": a.setting("access.entry")} // the new name needs the entry path once, like any new address
			served := []string{} // names the edge already serves on 443 (no port)
			for _, n := range append([]string{cur, m.Hostname + ".local", m.Hostname + ".lan"}, m.Aliases...) {
				if n != "" && a.entryServes(n) {
					served = append(served, n)
				}
			}
			out["no_port"] = served
			if c, l, sfx := a.wharfDNS(ctx); c != nil { // the router registers .lan names itself
				out["wharf_dns"] = map[string]any{"suffixes": sfx, "registered": l.DNSName}
			}
			if cur != "" {
				out["url"] = "https://" + net.JoinHostPort(cur, a.panelPort())
			}
			writeJSON(w, 200, out)
		}))
	})

	type nameParams struct {
		Name string `json:"name"`
	}
	read := func(raw json.RawMessage) (nameParams, error) {
		var p nameParams
		json.Unmarshal(raw, &p)
		p.Name = strings.ToLower(strings.TrimSpace(strings.TrimSuffix(p.Name, ".")))
		if p.Name != "" && net.ParseIP(p.Name) == nil {
			if err := validLanHost(p.Name); err != nil {
				return p, err
			}
		}
		return p, nil
	}
	change.Register(change.Kind{
		Name: "panel.local_name", Perm: "settings.manage", Scope: "settings:write", Title: "kind.panel.local_name",
		Params: change.ParamSchema{Props: map[string]change.Prop{"name": {Type: "string", Desc: "e.g. berth.local, nas.lan; empty: none"}}}, // empty is allowed: back to no name (undo needs it)
		CLI:    "berth panel local-name {{.name}}",
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			a := svc(env)
			p, err := read(raw)
			if err != nil {
				return change.Plan{}, err
			}
			cmds := []string{}
			if strings.HasSuffix(p.Name, ".local") {
				var m system.MDNS
				a.call(env.Ctx, "local", "GET", "/v1/system/mdns", nil, &m)
				if !m.Installed {
					cmds = append(cmds, "dnf install -y avahi   # (apt: avahi-daemon) answers name.local on the LAN")
				}
				if !m.FirewallOpen {
					cmds = append(cmds, "firewall-cmd --add-service=mdns --permanent")
				}
				if p.Name != m.Hostname+".local" {
					cmds = append(cmds, "/etc/avahi/hosts += "+a.lanIP()+" "+p.Name)
				}
				cmds = append(cmds, "systemctl enable --now avahi-daemon")
			}
			if c, l, sfx := a.wharfDNS(env.Ctx); c != nil {
				if l.DNSName != "" && l.DNSName != p.Name {
					cmds = append(cmds, "Wharf: DELETE dns/names/"+l.DNSName)
				}
				if p.Name != "" && suffixIn(p.Name, sfx) && l.DNSName != p.Name {
					cmds = append(cmds, "Wharf: PUT dns/names/"+p.Name+" → "+a.lanIP()+"   # the router answers this name on the LAN")
				}
			}
			cmds = append(cmds, "panel.lan_host = "+firstNonEmpty(p.Name, "—")+"   # signed into the panel certificate")
			summary := "plan.panel.local_name"
			if p.Name == "" {
				summary = "plan.panel.local_name_clear"
			}
			return change.Plan{Summary: summary, SummaryP: map[string]any{"name": p.Name}, Risk: change.RiskLow, Undoable: true, Commands: cmds}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			a := svc(env)
			p, err := read(raw)
			if err != nil {
				return nil, nil, err
			}
			prev := a.setting("panel.lan_host")
			aliases := []string{}
			if strings.HasSuffix(p.Name, ".local") {
				aliases = append(aliases, p.Name)
			}
			// .local names are published by mDNS; a name that is not (or none) leaves no alias behind
			if strings.HasSuffix(p.Name, ".local") || strings.HasSuffix(prev, ".local") {
				if err := a.streamAgent(env, "local", "/v1/system/mdns", map[string]any{"aliases": aliases, "ip": a.lanIP()}, nil); err != nil {
					return nil, nil, err
				}
			}
			// names the router answers (Wharf v2, scope dns): register the new one, drop the old
			if c, l, sfx := a.wharfDNS(env.Ctx); c != nil {
				actor := actorOf(env, "本地域名 "+firstNonEmpty(p.Name, l.DNSName))
				reg := ""
				if p.Name != "" && suffixIn(p.Name, sfx) {
					if err := c.SetDNSName(env.Ctx, actor, p.Name, a.lanIP()); err != nil {
						return nil, nil, a.wharfErr(err)
					}
					reg = p.Name
				}
				if l.DNSName != "" && l.DNSName != reg {
					if err := c.DeleteDNSName(env.Ctx, actor, l.DNSName); err != nil {
						return nil, nil, a.wharfErr(err)
					}
				}
				a.db.Exec(`UPDATE wharf_link SET dns_name=? WHERE id=1`, reg)
			}
			if err := a.setSetting("panel.lan_host", p.Name); err != nil {
				return nil, nil, err
			}
			if a.certRefresh != nil {
				go a.certRefresh()
			}
			go func() { a.entrySyncSoon(); a.notifyWharfNames() }() // the new name opens the panel on 443 too; Wharf learns it
			u, _ := json.Marshal(nameParams{Name: prev})
			return map[string]string{"name": p.Name}, &change.UndoRecipe{Kind: "panel.local_name", Params: u}, nil
		},
	})
}

func hostOnly(h string) string {
	if x, _, err := net.SplitHostPort(h); err == nil {
		return strings.Trim(x, "[]")
	}
	return h
}

var _ = errs.New
