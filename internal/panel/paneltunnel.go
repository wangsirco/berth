package panel

// The panel through a Cloudflare tunnel: panel.example.com opens the panel from anywhere
// while no port is open. One change does all of it — the tunnel (when there is none yet),
// the DNS record, the tunnel route to the edge, the panel's public name, and Cloudflare
// Access (an email code before anything of the panel shows). Visitors through the tunnel
// are let past the address allowlist (they come from the internet), and still need the
// security entry unless the person turned that off for the tunnel name.

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/wangsirco/berth/internal/agent/caddy"
	"github.com/wangsirco/berth/internal/change"
	"github.com/wangsirco/berth/internal/errs"
	"github.com/wangsirco/berth/internal/panel/dns"
)

const panelRouteID = 0 // cf_tunnel_routes: the panel itself (sites start at 1)

type panelTunnelParams struct {
	Integration string   `json:"integration"`
	Hostname    string   `json:"hostname"`
	On          bool     `json:"on"`
	Entry       *bool    `json:"entry,omitempty"`  // the security entry on the tunnel name (default: yes)
	Emails      []string `json:"emails,omitempty"` // Cloudflare Access: who may open it (empty: no Access)
}

func (p *panelTunnelParams) norm() {
	p.Hostname = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(p.Hostname), "."))
	var em []string
	for _, e := range p.Emails {
		if e = strings.TrimSpace(e); e != "" {
			em = append(em, strings.ToLower(e))
		}
	}
	p.Emails = em
}

// exposed: the panel answers people on the internet (public access, or through the tunnel)
func (a *App) exposed() bool {
	return a.isPublic() || a.setting("access.tunnel_host") != ""
}

// viaTunnel: this request came through the panel's tunnel name — sent on by the edge, for
// that name, from a neighbour on the edge's own network (cloudflared), not from the internet
func (a *App) viaTunnel(r *http.Request) bool {
	th := a.setting("access.tunnel_host")
	if th == "" || !a.fromEdge(r) {
		return false
	}
	h := r.Host
	if x, _, err := net.SplitHostPort(h); err == nil {
		h = x
	}
	if !strings.EqualFold(h, th) {
		return false
	}
	peer := net.ParseIP(r.Header.Get("X-Berth-Peer"))
	return peer != nil && (privateIP(peer) || peer.IsLoopback())
}

func (a *App) clearPanelTunnel() {
	th := a.setting("access.tunnel_host")
	if th == "" {
		return
	}
	for _, k := range []string{"access.tunnel_host", "access.tunnel_dns", "access.tunnel_entry", "access.tunnel_access"} {
		a.setSetting(k, "")
	}
	if strings.EqualFold(a.setting("panel.public_host"), th) {
		a.setSetting("panel.public_host", "")
	}
	go a.entrySync(context.Background())
}

func init() {
	change.Register(change.Kind{
		Name: "cf.tunnel.panel", Perm: "settings.manage", Scope: "system:write", Title: "kind.cf.tunnel.panel",
		Params: change.ParamSchema{Props: map[string]change.Prop{"integration": {Type: "string"}, "hostname": {Type: "string"}, "on": {Type: "boolean"},
			"entry": {Type: "boolean"}, "emails": {Type: "array"}}, Required: []string{"on"}},
		CLI: "berth cloudflare tunnel panel {{.hostname}}", Heavy: true,
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			a := svc(env)
			if env.Host != "local" {
				return change.Plan{}, errs.New(400, "cf.panel_local_only")
			}
			var p panelTunnelParams
			json.Unmarshal(raw, &p)
			p.norm()
			if !p.On {
				th := a.setting("access.tunnel_host")
				if th == "" {
					return change.Plan{}, errs.New(404, "cf.no_panel_tunnel")
				}
				return change.Plan{Summary: "plan.cf.tunnel.panel_off", SummaryP: map[string]any{"name": th}, Risk: change.RiskMedium, Undoable: true,
					Commands: []string{"remove tunnel route and DNS record of " + th}}, nil
			}
			if !domainLike.MatchString(p.Hostname) {
				return change.Plan{}, errs.New(400, "cf.bad_panel_name")
			}
			if a.domainUsedBySite(p.Hostname) {
				return change.Plan{}, errs.New(409, "cf.panel_name_used", "name", p.Hostname)
			}
			for _, e := range p.Emails {
				if !emailRe.MatchString(e) {
					return change.Plan{}, errs.New(400, "cf.bad_access")
				}
			}
			integ := p.Integration
			var cmds []string
			if t, _, ok := a.tunnelOf(env.Host); ok {
				integ = t.Integration // the tunnel there is uses its own account
			} else {
				cmds = append(cmds, "POST /accounts/…/cfd_tunnel {name: berth-"+env.Host+"}", "docker compose -p "+tunnelProject+" up -d")
			}
			if _, err := a.cloudflare(integ); err != nil {
				return change.Plan{}, err
			}
			cmds = append(cmds, "DNS "+p.Hostname+" CNAME <tunnel>.cfargotunnel.com (proxied)", "tunnel route "+p.Hostname+" → https://"+caddy.Container+":443",
				"panel.public_host = "+p.Hostname)
			if len(p.Emails) > 0 {
				cmds = append(cmds, "Cloudflare Access "+p.Hostname+": "+strings.Join(p.Emails, ", "))
			}
			risk := change.RiskMedium
			if p.Entry != nil && !*p.Entry && len(p.Emails) == 0 {
				risk = change.RiskHigh // the login page itself is open to the internet
			}
			return change.Plan{Summary: "plan.cf.tunnel.panel_on", SummaryP: map[string]any{"name": p.Hostname}, Risk: risk, Undoable: true, Commands: cmds}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			a := svc(env)
			var p panelTunnelParams
			json.Unmarshal(raw, &p)
			p.norm()
			if !p.On {
				return a.panelTunnelOff(env)
			}
			t, _, ok := a.tunnelOf(env.Host)
			if !ok {
				if _, err := a.createTunnel(env, p.Integration); err != nil {
					return nil, nil, err
				}
				t, _, _ = a.tunnelOf(env.Host)
			}
			cf, err := a.cloudflare(t.Integration)
			if err != nil {
				return nil, nil, err
			}
			if old := a.setting("access.tunnel_host"); old != "" && old != p.Hostname {
				if _, _, err := a.panelTunnelOff(env); err != nil {
					return nil, nil, err
				}
			}
			env.Log("::step 2 dns")
			z, err := a.zoneOf(env.Ctx, cf, p.Hostname)
			if err != nil {
				return nil, nil, err
			}
			route := tunnelRoute{SiteID: panelRouteID, Domains: []string{p.Hostname}}
			if _, _, had := a.panelRoute(); !had {
				rec, err := cf.Create(env.Ctx, z.ID, dns.Record{Type: "CNAME", Name: p.Hostname, Content: dns.TunnelTarget(t.TunnelID), TTL: 1, Proxied: true, Comment: "Berth panel tunnel"})
				if err != nil {
					return nil, nil, dnsErr(err)
				}
				route.Records = append(route.Records, struct {
					Zone string `json:"zone"`
					ID   string `json:"id"`
				}{z.ID, rec.ID})
			} else {
				route, _, _ = a.panelRoute()
			}
			spec, _ := json.Marshal(route)
			a.db.Exec(`INSERT INTO cf_tunnel_routes(host, site_id, spec) VALUES(?,?,?) ON CONFLICT(host, site_id) DO UPDATE SET spec=excluded.spec`, env.Host, panelRouteID, string(spec))
			if err := a.pushIngress(env.Ctx, cf, t); err != nil {
				return nil, nil, err
			}
			entry := ""
			if p.Entry != nil && !*p.Entry {
				entry = "off"
			}
			a.setSetting("panel.public_host", p.Hostname)
			a.setSetting("access.tunnel_host", p.Hostname)
			a.setSetting("access.tunnel_dns", t.Integration)
			a.setSetting("access.tunnel_entry", entry)
			if len(p.Emails) > 0 && a.setting("access.tunnel_access") == "" {
				env.Log("::step 3 access")
				var n int
				a.db.QueryRow(`SELECT COUNT(*) FROM cf_access WHERE domain=?`, p.Hostname).Scan(&n)
				if n == 0 {
					acc, err := a.cfAccount(env.Ctx, cf)
					if err != nil {
						return nil, nil, err
					}
					app, err := cf.CreateAccessApp(env.Ctx, acc, p.Hostname, dns.AccessPolicy{Emails: p.Emails})
					if err != nil {
						return nil, nil, dnsErr(err)
					}
					c := cfAccess{Domain: p.Hostname, Integration: t.Integration, Account: acc, AppID: app.ID, Emails: p.Emails, CreatedAt: time.Now().Unix()}
					s, _ := json.Marshal(c)
					a.db.Exec(`INSERT INTO cf_access(domain, spec) VALUES(?,?)`, p.Hostname, string(s))
					a.setSetting("access.tunnel_access", p.Hostname)
				}
			}
			env.Log("::step 4 edge")
			a.entrySync(env.Ctx)
			a.notifyWharfNames()
			undo, _ := json.Marshal(map[string]any{"on": false})
			return map[string]string{"url": "https://" + p.Hostname}, &change.UndoRecipe{Kind: "cf.tunnel.panel", Params: undo}, nil
		},
	})
}

// panelRoute: the panel's own tunnel route, if any
func (a *App) panelRoute() (tunnelRoute, string, bool) {
	var spec string
	if a.db.QueryRow(`SELECT spec FROM cf_tunnel_routes WHERE host='local' AND site_id=?`, panelRouteID).Scan(&spec) != nil {
		return tunnelRoute{}, "", false
	}
	var r tunnelRoute
	json.Unmarshal([]byte(spec), &r)
	return r, spec, true
}

func (a *App) panelTunnelOff(env change.Env) (any, *change.UndoRecipe, error) {
	th := a.setting("access.tunnel_host")
	t, _, _ := a.tunnelOf(env.Host)
	undo, _ := json.Marshal(map[string]any{"on": true, "integration": t.Integration, "hostname": th, "entry": a.setting("access.tunnel_entry") != "off"})
	if ad := a.setting("access.tunnel_access"); ad != "" {
		var spec string
		if a.db.QueryRow(`SELECT spec FROM cf_access WHERE domain=?`, ad).Scan(&spec) == nil {
			var c cfAccess
			json.Unmarshal([]byte(spec), &c)
			if cf, err := a.cloudflare(c.Integration); err == nil {
				if err := cf.DeleteAccessApp(env.Ctx, c.Account, c.AppID); err != nil {
					if e, ok := err.(*dns.Error); !ok || e.Code != "dns.not_found" {
						return nil, nil, dnsErr(err)
					}
				}
			}
			a.db.Exec(`DELETE FROM cf_access WHERE domain=?`, ad)
			undo, _ = json.Marshal(map[string]any{"on": true, "integration": t.Integration, "hostname": th, "entry": a.setting("access.tunnel_entry") != "off", "emails": c.Emails})
		}
	}
	if _, err := a.unrouteSite(env.Ctx, env.Host, panelRouteID); err != nil {
		return nil, nil, err
	}
	a.clearPanelTunnel()
	return nil, &change.UndoRecipe{Kind: "cf.tunnel.panel", Params: undo}, nil
}
