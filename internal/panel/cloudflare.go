package panel

// Cloudflare beyond DNS (方案 §6): a tunnel per host (cloudflared next to the site gateway),
// sites published through it, Zero Trust Access, ban sync, under-attack mode, cache purge,
// edge analytics. Every write is a change (preview, audit, undo).

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/wangsirco/berth/internal/agent/caddy"
	"github.com/wangsirco/berth/internal/change"
	"github.com/wangsirco/berth/internal/errs"
	"github.com/wangsirco/berth/internal/panel/dns"
)

const (
	tunnelProject = "berth-tunnel"
	tunnelImage   = "cloudflare/cloudflared:2025.11.1"
)

func (a *App) cloudflare(id string) (*dns.Cloudflare, error) {
	p, err := a.provider(id)
	if err != nil {
		return nil, err
	}
	cf, ok := p.(*dns.Cloudflare)
	if !ok {
		return nil, errs.New(400, "dns.records_unsupported", "kind", "?")
	}
	return cf, nil
}

// zoneOf finds the integration's zone for a host name.
func (a *App) zoneOf(ctx context.Context, cf *dns.Cloudflare, name string) (dns.Zone, error) {
	zones, err := cf.Zones(ctx)
	if err != nil {
		return dns.Zone{}, dnsErr(err)
	}
	z, ok := dns.ZoneFor(zones, name)
	if !ok {
		return dns.Zone{}, errs.New(400, "cf.no_zone", "name", name)
	}
	return z, nil
}

// ---- tunnel ----

type cfTunnel struct {
	Host        string `json:"host"`
	Integration string `json:"integration"`
	Account     string `json:"account"`
	TunnelID    string `json:"tunnel_id"`
	Name        string `json:"name"`
	CreatedAt   int64  `json:"created_at"`
}

func (a *App) tunnelOf(host string) (cfTunnel, []byte, bool) {
	var t cfTunnel
	var tok []byte
	err := a.db.QueryRow(`SELECT host, integration, account, tunnel_id, name, token_enc, created_at FROM cf_tunnels WHERE host=?`, host).
		Scan(&t.Host, &t.Integration, &t.Account, &t.TunnelID, &t.Name, &tok, &t.CreatedAt)
	return t, tok, err == nil
}

func tunnelCompose() string {
	return fmt.Sprintf(`# Berth: Cloudflare Tunnel for this host (token in .env)
services:
  cloudflared:
    image: %s
    restart: unless-stopped
    command: ["tunnel", "--no-autoupdate", "run"]
    environment:
      TUNNEL_TOKEN: ${TUNNEL_TOKEN}
    networks: [edge]
    mem_limit: 128m
networks:
  edge:
    name: %s
    external: true
`, tunnelImage, caddy.Network)
}

// tunnelRoutes lists the hostnames published through the host's tunnel.
type tunnelRoute struct {
	SiteID  int64    `json:"site_id"`
	Domains []string `json:"domains"`
	Records []struct {
		Zone string `json:"zone"`
		ID   string `json:"id"`
	} `json:"records"`
}

func (a *App) tunnelRoutes(host string) []tunnelRoute {
	rows, err := a.db.Query(`SELECT spec FROM cf_tunnel_routes WHERE host=? ORDER BY site_id`, host)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := []tunnelRoute{}
	for rows.Next() {
		var spec string
		rows.Scan(&spec)
		var r tunnelRoute
		json.Unmarshal([]byte(spec), &r)
		out = append(out, r)
	}
	return out
}

// pushIngress rewrites the tunnel's routes from cf_tunnel_routes.
func (a *App) pushIngress(ctx context.Context, cf *dns.Cloudflare, t cfTunnel) error {
	var ing []dns.Ingress
	for _, r := range a.tunnelRoutes(t.Host) {
		for _, d := range r.Domains {
			ing = append(ing, dns.Ingress{Hostname: d, Service: "https://" + caddy.Container + ":443"})
		}
	}
	return dnsErr(cf.SetIngress(ctx, t.Account, t.TunnelID, ing))
}

// unrouteSite takes a site off the tunnel (DNS records too). Used when a site is deleted.
func (a *App) unrouteSite(ctx context.Context, host string, siteID int64) (*tunnelRoute, error) {
	t, _, ok := a.tunnelOf(host)
	if !ok {
		return nil, nil
	}
	var spec string
	if a.db.QueryRow(`SELECT spec FROM cf_tunnel_routes WHERE host=? AND site_id=?`, host, siteID).Scan(&spec) != nil {
		return nil, nil
	}
	var r tunnelRoute
	json.Unmarshal([]byte(spec), &r)
	cf, err := a.cloudflare(t.Integration)
	if err != nil {
		return nil, err
	}
	a.db.Exec(`DELETE FROM cf_tunnel_routes WHERE host=? AND site_id=?`, host, siteID)
	if err := a.pushIngress(ctx, cf, t); err != nil {
		return nil, err
	}
	for _, rec := range r.Records {
		cf.Delete(ctx, rec.Zone, rec.ID)
	}
	return &r, nil
}

// ---- access ----

type cfAccess struct {
	Domain       string   `json:"domain"`
	Integration  string   `json:"integration"`
	Account      string   `json:"account"`
	AppID        string   `json:"app_id"`
	Emails       []string `json:"emails,omitempty"`
	EmailDomains []string `json:"email_domains,omitempty"`
	CreatedAt    int64    `json:"created_at"`
}

var emailRe = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[^@\s]+$`)
var domainLike = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,}$`)

// ---- bans to Cloudflare ----

// syncBanCF adds or removes a ban's account-wide Cloudflare rule.
func (a *App) syncBanCF(ctx context.Context, banID int64, on bool) error {
	var addr, rule string
	if err := a.db.QueryRow(`SELECT addr, cf_rule FROM bans WHERE id=?`, banID).Scan(&addr, &rule); err != nil {
		return errs.New(404, "ban.not_found")
	}
	integ := a.setting("cloudflare.ban_integration")
	if integ == "" {
		return errs.New(409, "cf.no_ban_account")
	}
	cf, err := a.cloudflare(integ)
	if err != nil {
		return err
	}
	acc, err := a.cfAccount(ctx, cf)
	if err != nil {
		return err
	}
	if on && rule == "" {
		id, err := cf.BlockIP(ctx, acc, addr, "Berth ban #"+strconv.FormatInt(banID, 10))
		if err != nil {
			return dnsErr(err)
		}
		a.db.Exec(`UPDATE bans SET cf_rule=?, layers=json_insert(layers, '$[#]', 'cloudflare') WHERE id=?`, acc+"/"+id, banID)
	}
	if !on && rule != "" {
		accID, ruleID, _ := strings.Cut(rule, "/")
		if err := cf.UnblockIP(ctx, accID, ruleID); err != nil {
			return dnsErr(err)
		}
		a.db.Exec(`UPDATE bans SET cf_rule='', layers='["local"]' WHERE id=?`, banID)
	}
	return nil
}

func (a *App) cfAccount(ctx context.Context, cf *dns.Cloudflare) (string, error) {
	zones, err := cf.Zones(ctx)
	if err != nil {
		return "", dnsErr(err)
	}
	for _, z := range zones {
		if z.AccountID != "" {
			return z.AccountID, nil
		}
	}
	return "", errs.New(409, "cf.no_account")
}

// cfBanSweep withdraws Cloudflare rules of bans that ended, and syncs new automatic bans
// when the setting asks for it.
func (a *App) cfBanSweep(ctx context.Context) {
	if a.setting("cloudflare.ban_integration") == "" {
		return
	}
	now := time.Now().Unix()
	rows, err := a.db.Query(`SELECT id FROM bans WHERE cf_rule != '' AND (revoked_at != 0 OR (expires_at != 0 AND expires_at <= ?)) LIMIT 50`, now)
	if err == nil {
		var ids []int64
		for rows.Next() {
			var id int64
			rows.Scan(&id)
			ids = append(ids, id)
		}
		rows.Close()
		for _, id := range ids {
			a.syncBanCF(ctx, id, false)
		}
	}
	if a.setting("cloudflare.sync_auto_bans") == "1" {
		rows, err := a.db.Query(`SELECT id FROM bans WHERE cf_rule = '' AND source IN ('bruteforce','waf') AND revoked_at = 0 AND (expires_at = 0 OR expires_at > ?) LIMIT 50`, now)
		if err == nil {
			var ids []int64
			for rows.Next() {
				var id int64
				rows.Scan(&id)
				ids = append(ids, id)
			}
			rows.Close()
			for _, id := range ids {
				a.syncBanCF(ctx, id, true)
			}
		}
	}
}

// ---- API ----

func init() {
	routeHooks = append(routeHooks, func(a *App, mux *http.ServeMux) {
		// what each Cloudflare account can do, plus Berth's state there
		mux.HandleFunc("GET /api/v1/hosts/{host}/cloudflare", a.authed("network.view", func(w http.ResponseWriter, r *http.Request) {
			host := r.PathValue("host")
			ins, _ := a.integrations()
			type acct struct {
				ID       string            `json:"id"`
				Name     string            `json:"name"`
				Zones    []dns.Zone        `json:"zones"`
				Features *dns.Features     `json:"features,omitempty"`
				Levels   map[string]string `json:"levels,omitempty"`
				Error    string            `json:"error,omitempty"`
			}
			out := []acct{}
			for _, in := range ins {
				if in.Kind != "cloudflare" {
					continue
				}
				x := acct{ID: in.ID, Name: in.Name, Levels: map[string]string{}}
				cf, err := a.cloudflare(in.ID)
				if err == nil {
					x.Zones, err = cf.Zones(r.Context())
				}
				if err != nil {
					x.Error = errs.From(dnsErr(err)).Code
				} else if len(x.Zones) > 0 {
					f := cf.CheckFeatures(r.Context(), x.Zones[0])
					x.Features = &f
					if f.Settings {
						for _, z := range x.Zones {
							x.Levels[z.ID], _ = cf.SecurityLevel(r.Context(), z.ID)
						}
					}
				}
				out = append(out, x)
			}
			var tun any
			if t, _, ok := a.tunnelOf(host); ok {
				tun = t
			}
			access := []cfAccess{}
			rows, err := a.db.Query(`SELECT spec FROM cf_access ORDER BY domain`)
			if err == nil {
				for rows.Next() {
					var spec string
					rows.Scan(&spec)
					var c cfAccess
					json.Unmarshal([]byte(spec), &c)
					access = append(access, c)
				}
				rows.Close()
			}
			writeJSON(w, 200, map[string]any{"accounts": out, "tunnel": tun, "routes": a.tunnelRoutes(host), "access": access,
				"ban_integration": a.setting("cloudflare.ban_integration"), "sync_auto_bans": a.setting("cloudflare.sync_auto_bans") == "1"})
		}))
		mux.HandleFunc("GET /api/v1/integrations/{id}/zones/{zone}/analytics", a.authed("sites.view", func(w http.ResponseWriter, r *http.Request) {
			cf, err := a.cloudflare(r.PathValue("id"))
			if err != nil {
				a.fail(w, r, err)
				return
			}
			days, _ := strconv.Atoi(r.URL.Query().Get("days"))
			if days <= 0 || days > 30 {
				days = 7
			}
			out, err := cf.ZoneDays(r.Context(), r.PathValue("zone"), days)
			if err != nil {
				a.fail(w, r, dnsErr(err))
				return
			}
			writeJSON(w, 200, map[string]any{"days": out})
		}))
	})

	// ---- tunnel kinds ----
	change.Register(change.Kind{
		Name: "cf.tunnel.create", Perm: "network.manage", Scope: "system:write", Title: "kind.cf.tunnel.create",
		Params: change.ParamSchema{Props: map[string]change.Prop{"integration": {Type: "string"}}, Required: []string{"integration"}},
		CLI:    "berth cloudflare tunnel create", Heavy: true, MemMB: 128,
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			a := svc(env)
			var p struct{ Integration string }
			json.Unmarshal(raw, &p)
			if _, _, ok := a.tunnelOf(env.Host); ok {
				return change.Plan{}, errs.New(409, "cf.tunnel_exists")
			}
			if _, err := a.cloudflare(p.Integration); err != nil {
				return change.Plan{}, err
			}
			return change.Plan{Summary: "plan.cf.tunnel.create", SummaryP: map[string]any{"name": "berth-" + env.Host}, Risk: change.RiskLow, Undoable: true,
				Files:    []change.FileDiff{{Path: "/srv/berth/stacks/" + tunnelProject + "/compose.yaml", After: tunnelCompose()}},
				Commands: []string{"POST /accounts/…/cfd_tunnel {name: berth-" + env.Host + "}", "docker compose -p " + tunnelProject + " up -d"}}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			a := svc(env)
			var p struct{ Integration string }
			json.Unmarshal(raw, &p)
			cf, err := a.cloudflare(p.Integration)
			if err != nil {
				return nil, nil, err
			}
			acc, err := a.cfAccount(env.Ctx, cf)
			if err != nil {
				return nil, nil, err
			}
			env.Log("::step 0 create")
			name := "berth-" + env.Host + "-" + strconv.FormatInt(time.Now().Unix()%100000, 36)
			t, err := cf.CreateTunnel(env.Ctx, acc, name)
			if err != nil {
				return nil, nil, dnsErr(err)
			}
			tok, err := cf.TunnelToken(env.Ctx, acc, t.ID)
			if err != nil {
				cf.DeleteTunnel(env.Ctx, acc, t.ID)
				return nil, nil, dnsErr(err)
			}
			cf.SetIngress(env.Ctx, acc, t.ID, nil)
			enc, err := a.seal([]byte(tok))
			if err != nil {
				return nil, nil, err
			}
			a.db.Exec(`INSERT INTO cf_tunnels(host, integration, account, tunnel_id, name, token_enc, created_at) VALUES(?,?,?,?,?,?,?)`,
				env.Host, p.Integration, acc, t.ID, name, enc, time.Now().Unix())
			env.Log("::step 1 run cloudflared")
			envText := "TUNNEL_TOKEN=" + tok + "\n"
			if err := a.call(env.Ctx, env.Host, "PUT", "/v1/compose/projects/"+tunnelProject+"/files", map[string]any{"compose": tunnelCompose(), "env": envText, "create": true}, nil); err != nil {
				return nil, nil, err
			}
			if err := agentOp(env, "/v1/compose/projects/"+tunnelProject+"/up", nil); err != nil {
				return nil, nil, err
			}
			a.relate(env.Host, change.Object{Type: "host", ID: env.Host}, projObj(tunnelProject, env.Host), "manages", "module")
			return map[string]string{"tunnel": t.ID}, &change.UndoRecipe{Kind: "cf.tunnel.delete", Params: json.RawMessage(`{}`)}, nil
		},
	})
	change.Register(change.Kind{
		Name: "cf.tunnel.delete", Perm: "network.manage", Scope: "system:write", Title: "kind.cf.tunnel.delete",
		Params: change.ParamSchema{Props: map[string]change.Prop{}}, CLI: "berth cloudflare tunnel delete", Heavy: true,
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			a := svc(env)
			t, _, ok := a.tunnelOf(env.Host)
			if !ok {
				return change.Plan{}, errs.New(404, "cf.no_tunnel")
			}
			return change.Plan{Summary: "plan.cf.tunnel.delete", SummaryP: map[string]any{"name": t.Name, "count": len(a.tunnelRoutes(env.Host))}, Risk: change.RiskHigh,
				Commands: []string{"docker compose -p " + tunnelProject + " down", "DELETE /accounts/…/cfd_tunnel/" + t.TunnelID, "# and the DNS records of the sites published through it"}}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			a := svc(env)
			t, _, ok := a.tunnelOf(env.Host)
			if !ok {
				return nil, nil, errs.New(404, "cf.no_tunnel")
			}
			cf, err := a.cloudflare(t.Integration)
			if err != nil {
				return nil, nil, err
			}
			for _, r := range a.tunnelRoutes(env.Host) {
				for _, rec := range r.Records {
					cf.Delete(env.Ctx, rec.Zone, rec.ID)
				}
			}
			agentOp(env, "/v1/compose/projects/"+tunnelProject+"/remove", nil)
			if err := cf.DeleteTunnel(env.Ctx, t.Account, t.TunnelID); err != nil {
				return nil, nil, dnsErr(err)
			}
			a.db.Exec(`DELETE FROM cf_tunnel_routes WHERE host=?`, env.Host)
			a.db.Exec(`DELETE FROM cf_tunnels WHERE host=?`, env.Host)
			u, _ := json.Marshal(map[string]string{"integration": t.Integration})
			return nil, &change.UndoRecipe{Kind: "cf.tunnel.create", Params: u}, nil
		},
	})
	change.Register(change.Kind{
		Name: "cf.tunnel.route", Perm: "sites.manage", Scope: "sites:write", Title: "kind.cf.tunnel.route",
		Params: change.ParamSchema{Props: map[string]change.Prop{"site_id": {Type: "integer"}, "on": {Type: "boolean"}}, Required: []string{"site_id", "on"}},
		CLI:    "berth cloudflare tunnel route {{.site_id}}",
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			a := svc(env)
			var p struct {
				SiteID int64 `json:"site_id"`
				On     bool
			}
			json.Unmarshal(raw, &p)
			t, _, ok := a.tunnelOf(env.Host)
			if !ok {
				return change.Plan{}, errs.New(409, "cf.no_tunnel")
			}
			s, err := a.site(env.Host, p.SiteID)
			if err != nil {
				return change.Plan{}, err
			}
			var cmds []string
			for _, d := range s.Domains {
				if p.On {
					cmds = append(cmds, fmt.Sprintf("DNS %s CNAME %s (proxied)", d, dns.TunnelTarget(t.TunnelID)), "tunnel route "+d+" → https://"+caddy.Container+":443")
				} else {
					cmds = append(cmds, "remove tunnel route and DNS record of "+d)
				}
			}
			sum := "plan.cf.tunnel.route_on"
			if !p.On {
				sum = "plan.cf.tunnel.route_off"
			}
			return change.Plan{Summary: sum, SummaryP: map[string]any{"domain": s.Primary()}, Risk: change.RiskMedium, Undoable: true, Commands: cmds}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			a := svc(env)
			var p struct {
				SiteID int64 `json:"site_id"`
				On     bool
			}
			json.Unmarshal(raw, &p)
			undo, _ := json.Marshal(map[string]any{"site_id": p.SiteID, "on": !p.On})
			if !p.On {
				if _, err := a.unrouteSite(env.Ctx, env.Host, p.SiteID); err != nil {
					return nil, nil, err
				}
				return nil, &change.UndoRecipe{Kind: "cf.tunnel.route", Params: undo}, nil
			}
			t, _, ok := a.tunnelOf(env.Host)
			if !ok {
				return nil, nil, errs.New(409, "cf.no_tunnel")
			}
			s, err := a.site(env.Host, p.SiteID)
			if err != nil {
				return nil, nil, err
			}
			cf, err := a.cloudflare(t.Integration)
			if err != nil {
				return nil, nil, err
			}
			route := tunnelRoute{SiteID: s.ID, Domains: s.Domains}
			for _, d := range s.Domains {
				z, err := a.zoneOf(env.Ctx, cf, d)
				if err != nil {
					return nil, nil, err
				}
				rec, err := cf.Create(env.Ctx, z.ID, dns.Record{Type: "CNAME", Name: d, Content: dns.TunnelTarget(t.TunnelID), TTL: 1, Proxied: true, Comment: "Berth tunnel"})
				if err != nil {
					for _, r := range route.Records { // put back what was added
						cf.Delete(env.Ctx, r.Zone, r.ID)
					}
					return nil, nil, dnsErr(err)
				}
				route.Records = append(route.Records, struct {
					Zone string `json:"zone"`
					ID   string `json:"id"`
				}{z.ID, rec.ID})
			}
			spec, _ := json.Marshal(route)
			a.db.Exec(`INSERT INTO cf_tunnel_routes(host, site_id, spec) VALUES(?,?,?) ON CONFLICT(host, site_id) DO UPDATE SET spec=excluded.spec`, env.Host, s.ID, string(spec))
			if err := a.pushIngress(env.Ctx, cf, t); err != nil {
				return nil, nil, err
			}
			return route, &change.UndoRecipe{Kind: "cf.tunnel.route", Params: undo}, nil
		},
	})

	// ---- protection ----
	zoneProps := map[string]change.Prop{"integration": {Type: "string"}, "zone": {Type: "string"}, "zone_name": {Type: "string"}}
	change.Register(change.Kind{
		Name: "cf.attack_mode", Perm: "network.manage", Scope: "system:write", Title: "kind.cf.attack_mode",
		Params: change.ParamSchema{Props: map[string]change.Prop{"integration": {Type: "string"}, "zone": {Type: "string"}, "zone_name": {Type: "string"},
			"level": {Type: "string", Desc: "under_attack to switch on; the earlier level to switch off"}}, Required: []string{"integration", "zone", "level"}},
		CLI: "berth cloudflare attack-mode {{.zone_name}} {{.level}}",
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			var p struct{ Integration, Zone, ZoneName, Level string }
			json.Unmarshal(raw, &p)
			switch p.Level {
			case "under_attack", "high", "medium", "low", "essentially_off", "off":
			default:
				return change.Plan{}, errs.New(400, "cf.bad_level")
			}
			return change.Plan{Summary: "plan.cf.attack_mode", SummaryP: map[string]any{"zone": firstNonEmpty(p.ZoneName, p.Zone), "level": p.Level}, Risk: change.RiskMedium, Undoable: true,
				Commands: []string{"PATCH /zones/" + p.Zone + "/settings/security_level {value: " + p.Level + "}"}}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			a := svc(env)
			var p struct {
				Integration string
				Zone        string
				ZoneName    string `json:"zone_name"`
				Level       string
			}
			json.Unmarshal(raw, &p)
			cf, err := a.cloudflare(p.Integration)
			if err != nil {
				return nil, nil, err
			}
			before, err := cf.SecurityLevel(env.Ctx, p.Zone)
			if err != nil {
				return nil, nil, dnsErr(err)
			}
			if err := cf.SetSecurityLevel(env.Ctx, p.Zone, p.Level); err != nil {
				return nil, nil, dnsErr(err)
			}
			u, _ := json.Marshal(map[string]string{"integration": p.Integration, "zone": p.Zone, "zone_name": p.ZoneName, "level": before})
			return map[string]string{"before": before}, &change.UndoRecipe{Kind: "cf.attack_mode", Params: u}, nil
		},
	})
	change.Register(change.Kind{
		Name: "cf.purge", Perm: "sites.manage", Scope: "sites:write", Title: "kind.cf.purge",
		Params: change.ParamSchema{Props: zoneProps, Required: []string{"integration", "zone"}}, CLI: "berth cloudflare purge {{.zone_name}}",
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			var p struct{ Integration, Zone, ZoneName string }
			json.Unmarshal(raw, &p)
			return change.Plan{Summary: "plan.cf.purge", SummaryP: map[string]any{"zone": firstNonEmpty(p.ZoneName, p.Zone)}, Risk: change.RiskLow,
				Commands: []string{"POST /zones/" + p.Zone + "/purge_cache {purge_everything: true}"}}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			var p struct{ Integration, Zone string }
			json.Unmarshal(raw, &p)
			cf, err := svc(env).cloudflare(p.Integration)
			if err != nil {
				return nil, nil, err
			}
			return nil, nil, dnsErr(cf.PurgeCache(env.Ctx, p.Zone, nil))
		},
	})
	change.Register(change.Kind{
		Name: "ban.sync", Perm: "network.manage", Scope: "system:write", Title: "kind.ban.sync",
		Params: change.ParamSchema{Props: map[string]change.Prop{"id": {Type: "integer"}, "cloudflare": {Type: "boolean"}, "wharf": {Type: "boolean"}}, Required: []string{"id"}},
		CLI:    "berth ban sync {{.id}}",
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			var p struct {
				ID         int64
				Cloudflare *bool
				Wharf      *bool
			}
			json.Unmarshal(raw, &p)
			var addr string
			if svc(env).db.QueryRow(`SELECT addr FROM bans WHERE id=?`, p.ID).Scan(&addr) != nil {
				return change.Plan{}, errs.New(404, "ban.not_found")
			}
			sum := ""
			switch {
			case p.Cloudflare != nil && *p.Cloudflare:
				sum = "plan.ban.sync_cf_on"
			case p.Cloudflare != nil:
				sum = "plan.ban.sync_cf_off"
			case p.Wharf != nil && *p.Wharf:
				sum = "plan.ban.sync_wharf_on"
			case p.Wharf != nil:
				sum = "plan.ban.sync_wharf_off"
			default:
				return change.Plan{}, errs.New(400, "ban.bad_addr", "addr", "")
			}
			return change.Plan{Summary: sum, SummaryP: map[string]any{"addr": addr}, Risk: change.RiskLow, Undoable: true}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			a := svc(env)
			var p struct {
				ID         int64
				Cloudflare *bool
				Wharf      *bool
			}
			json.Unmarshal(raw, &p)
			undo := map[string]any{"id": p.ID}
			if p.Cloudflare != nil {
				if err := a.syncBanCF(env.Ctx, p.ID, *p.Cloudflare); err != nil {
					return nil, nil, err
				}
				undo["cloudflare"] = !*p.Cloudflare
			}
			if p.Wharf != nil {
				if err := a.syncBanWharf(env.Ctx, p.ID, *p.Wharf, actorOf(env, "封禁同步")); err != nil {
					return nil, nil, err
				}
				undo["wharf"] = !*p.Wharf
			}
			u, _ := json.Marshal(undo)
			return nil, &change.UndoRecipe{Kind: "ban.sync", Params: u}, nil
		},
	})

	// ---- access ----
	change.Register(change.Kind{
		Name: "cf.access.set", Perm: "network.manage", Scope: "system:write", Title: "kind.cf.access.set",
		Params: change.ParamSchema{Props: map[string]change.Prop{"integration": {Type: "string"}, "domain": {Type: "string"},
			"emails": {Type: "array"}, "email_domains": {Type: "array"}}, Required: []string{"integration", "domain"}},
		CLI: "berth cloudflare access {{.domain}}",
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			var p cfAccess
			json.Unmarshal(raw, &p)
			p.Domain = strings.ToLower(strings.TrimSpace(p.Domain))
			if !domainLike.MatchString(p.Domain) || len(p.Emails)+len(p.EmailDomains) == 0 {
				return change.Plan{}, errs.New(400, "cf.bad_access")
			}
			for _, e := range p.Emails {
				if !emailRe.MatchString(e) {
					return change.Plan{}, errs.New(400, "cf.bad_access")
				}
			}
			for _, d := range p.EmailDomains {
				if !domainLike.MatchString(d) {
					return change.Plan{}, errs.New(400, "cf.bad_access")
				}
			}
			var n int
			svc(env).db.QueryRow(`SELECT COUNT(*) FROM cf_access WHERE domain=?`, p.Domain).Scan(&n)
			if n > 0 {
				return change.Plan{}, errs.New(409, "cf.access_exists")
			}
			who := strings.Join(append(append([]string{}, p.Emails...), prefixAll("@", p.EmailDomains)...), ", ")
			return change.Plan{Summary: "plan.cf.access.set", SummaryP: map[string]any{"domain": p.Domain, "who": who}, Risk: change.RiskMedium, Undoable: true}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			a := svc(env)
			var p cfAccess
			json.Unmarshal(raw, &p)
			p.Domain = strings.ToLower(strings.TrimSpace(p.Domain))
			cf, err := a.cloudflare(p.Integration)
			if err != nil {
				return nil, nil, err
			}
			acc, err := a.cfAccount(env.Ctx, cf)
			if err != nil {
				return nil, nil, err
			}
			app, err := cf.CreateAccessApp(env.Ctx, acc, p.Domain, dns.AccessPolicy{Emails: p.Emails, EmailDomains: p.EmailDomains})
			if err != nil {
				return nil, nil, dnsErr(err)
			}
			p.Account, p.AppID, p.CreatedAt = acc, app.ID, time.Now().Unix()
			spec, _ := json.Marshal(p)
			a.db.Exec(`INSERT INTO cf_access(domain, spec) VALUES(?,?)`, p.Domain, string(spec))
			u, _ := json.Marshal(map[string]string{"domain": p.Domain})
			return map[string]string{"app": app.ID}, &change.UndoRecipe{Kind: "cf.access.delete", Params: u}, nil
		},
	})
	change.Register(change.Kind{
		Name: "cf.access.delete", Perm: "network.manage", Scope: "system:write", Title: "kind.cf.access.delete",
		Params: change.ParamSchema{Props: map[string]change.Prop{"domain": {Type: "string"}}, Required: []string{"domain"}}, CLI: "berth cloudflare access delete {{.domain}}",
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			var p struct{ Domain string }
			json.Unmarshal(raw, &p)
			return change.Plan{Summary: "plan.cf.access.delete", SummaryP: map[string]any{"domain": p.Domain}, Risk: change.RiskHigh, Undoable: true}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			a := svc(env)
			var p struct{ Domain string }
			json.Unmarshal(raw, &p)
			var spec string
			if a.db.QueryRow(`SELECT spec FROM cf_access WHERE domain=?`, p.Domain).Scan(&spec) != nil {
				return nil, nil, errs.New(404, "cf.no_access")
			}
			var c cfAccess
			json.Unmarshal([]byte(spec), &c)
			cf, err := a.cloudflare(c.Integration)
			if err != nil {
				return nil, nil, err
			}
			if err := cf.DeleteAccessApp(env.Ctx, c.Account, c.AppID); err != nil {
				if e, ok := err.(*dns.Error); !ok || e.Code != "dns.not_found" {
					return nil, nil, dnsErr(err)
				}
			}
			a.db.Exec(`DELETE FROM cf_access WHERE domain=?`, p.Domain)
			u, _ := json.Marshal(map[string]any{"integration": c.Integration, "domain": c.Domain, "emails": c.Emails, "email_domains": c.EmailDomains})
			return nil, &change.UndoRecipe{Kind: "cf.access.set", Params: u}, nil
		},
	})
}

func prefixAll(p string, l []string) []string {
	out := make([]string, len(l))
	for i, x := range l {
		out[i] = p + x
	}
	return out
}
