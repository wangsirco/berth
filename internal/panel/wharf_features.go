package panel

// What Berth does at a paired Wharf: publish sites (port mapping, or 443 SNI registration
// when Wharf's proxy-front owns 443), sync bans, claim DDNS names, show the router's view,
// hand alerts over, and unpair.

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/wangsirco/berth/internal/change"
	"github.com/wangsirco/berth/internal/errs"
	"github.com/wangsirco/berth/internal/panel/wharfapi"
)

type wharfPublish struct {
	SiteID    int64    `json:"site_id"`
	Mode      string   `json:"mode"` // forward | sni
	WANPort   string   `json:"wan_port,omitempty"`
	TargetIP  string   `json:"target_ip"`
	ForwardID string   `json:"forward_id,omitempty"`
	Countries []string `json:"allow_countries,omitempty"`
}

func (a *App) wharfPublished(host string) []wharfPublish {
	rows, err := a.db.Query(`SELECT spec FROM wharf_publish WHERE host=? ORDER BY site_id`, host)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := []wharfPublish{}
	for rows.Next() {
		var spec string
		rows.Scan(&spec)
		var p wharfPublish
		json.Unmarshal([]byte(spec), &p)
		out = append(out, p)
	}
	return out
}

func actorOf(env change.Env, what string) string {
	return strings.TrimSpace(env.ActorName + " · " + what)
}

// pushSNI rewrites Berth's 443 registration from every SNI-published site.
func (a *App) pushSNI(ctx context.Context, c *wharfapi.Client, actor, target string) error {
	var domains []string
	for _, h := range append([]string{"local"}, a.remoteHostNames()...) {
		for _, p := range a.wharfPublished(h) {
			if p.Mode != "sni" {
				continue
			}
			if s, err := a.site(h, p.SiteID); err == nil {
				domains = append(domains, s.Domains...)
			}
		}
	}
	if domains == nil {
		domains = []string{}
	}
	return c.SetSNI(ctx, actor, wharfapi.SNI{Target: target + ":443", Domains: domains, ProxyProtocol: false})
}

// unpublishSite takes a site off Wharf (mapping deleted or SNI list rewritten).
func (a *App) unpublishSite(ctx context.Context, host string, siteID int64, actor string) (*wharfPublish, error) {
	var spec string
	if a.db.QueryRow(`SELECT spec FROM wharf_publish WHERE host=? AND site_id=?`, host, siteID).Scan(&spec) != nil {
		return nil, nil
	}
	var p wharfPublish
	json.Unmarshal([]byte(spec), &p)
	scope := "forwards"
	if p.Mode == "sni" {
		scope = "sni"
	}
	c, _, err := a.wharfClient(scope)
	if err != nil {
		return nil, err
	}
	a.db.Exec(`DELETE FROM wharf_publish WHERE host=? AND site_id=?`, host, siteID)
	if p.Mode == "forward" {
		err = c.DeleteForward(ctx, actor, p.ForwardID)
	} else {
		err = a.pushSNI(ctx, c, actor, p.TargetIP)
	}
	if err != nil {
		b, _ := json.Marshal(p)
		a.db.Exec(`INSERT OR REPLACE INTO wharf_publish(host, site_id, spec) VALUES(?,?,?)`, host, siteID, string(b))
		return nil, a.wharfErr(err)
	}
	return &p, nil
}

// syncBanWharf pushes a ban to Wharf or withdraws it.
func (a *App) syncBanWharf(ctx context.Context, banID int64, on bool, actor string) error {
	var addr, ref, reason, source string
	var exp int64
	if err := a.db.QueryRow(`SELECT addr, wharf_ban, reason, source, expires_at FROM bans WHERE id=?`, banID).Scan(&addr, &ref, &reason, &source, &exp); err != nil {
		return errs.New(404, "ban.not_found")
	}
	if strings.HasPrefix(ref, "in:") {
		return errs.New(409, "wharf.ban_from_wharf") // came from Wharf: nothing to push back
	}
	c, _, err := a.wharfClient("bans")
	if err != nil {
		return err
	}
	if on && ref == "" {
		b := wharfapi.Ban{CIDR: addr, Reason: reason, Source: "berth:" + firstNonEmpty(a.setting("panel.name"), "berth") + ":" + source}
		if !strings.Contains(addr, "/") {
			if strings.Contains(addr, ":") {
				b.CIDR += "/128"
			} else {
				b.CIDR += "/32"
			}
		}
		if exp > 0 {
			b.ExpiresAt = time.Unix(exp, 0).UTC().Format(time.RFC3339)
		}
		id, err := c.PushBan(ctx, actor, b)
		if err != nil {
			return a.wharfErr(err)
		}
		a.db.Exec(`UPDATE bans SET wharf_ban=?, layers=json_insert(layers, '$[#]', 'wharf') WHERE id=?`, "out:"+id, banID)
	}
	if !on && strings.HasPrefix(ref, "out:") {
		if err := c.WithdrawBan(ctx, actor, strings.TrimPrefix(ref, "out:")); err != nil {
			return a.wharfErr(err)
		}
		a.db.Exec(`UPDATE bans SET wharf_ban='', layers=(SELECT json_group_array(value) FROM json_each(layers) WHERE value != 'wharf') WHERE id=?`, banID)
	}
	return nil
}

// wharfBanSweep withdraws Wharf bans of bans that ended (and syncs automatic ones when asked).
func (a *App) wharfBanSweep(ctx context.Context) {
	if _, err := a.wharf(); err != nil {
		return
	}
	now := time.Now().Unix()
	ids := func(q string, args ...any) []int64 {
		var out []int64
		rows, err := a.db.Query(q, args...)
		if err != nil {
			return nil
		}
		for rows.Next() {
			var id int64
			rows.Scan(&id)
			out = append(out, id)
		}
		rows.Close()
		return out
	}
	for _, id := range ids(`SELECT id FROM bans WHERE wharf_ban LIKE 'out:%' AND (revoked_at != 0 OR (expires_at != 0 AND expires_at <= ?)) LIMIT 50`, now) {
		a.syncBanWharf(ctx, id, false, "Berth · 封禁结束")
	}
	if a.setting("wharf.sync_auto_bans") == "1" {
		for _, id := range ids(`SELECT id FROM bans WHERE wharf_ban = '' AND source IN ('bruteforce','waf') AND revoked_at = 0 AND (expires_at = 0 OR expires_at > ?) LIMIT 50`, now) {
			a.syncBanWharf(ctx, id, true, "Berth · 自动封禁")
		}
	}
}

// sendToWharf hands an alert to Wharf when Wharf is the main notifier; false means
// Berth should deliver it itself.
func (a *App) sendToWharf(ev alertEvent) bool {
	l, err := a.wharf()
	if err != nil || l.AlertsPrimary != "wharf" || !l.has("alerts") || ev.Rule == "wharf_alert" {
		return false
	}
	title, body := a.eventText(ev, "zh-CN")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	err = wharfapi.New(l.URL, l.FP, l.token).Alert(ctx, wharfapi.Alert{Level: ev.Severity, Title: "[Berth · " + firstNonEmpty(a.setting("panel.name"), "Berth") + "] " + title,
		Body: body, Link: a.publicURL() + "/" + firstNonEmpty(ev.Host, "local") + "/monitor", DedupeKey: ev.Key + ":" + ev.State})
	return err == nil
}

func init() {
	routeHooks = append(routeHooks, func(a *App, mux *http.ServeMux) {
		// the router's view: devices, DDNS, this host's traffic and inbound connections
		mux.HandleFunc("GET /api/v1/hosts/{host}/wharf", a.authed("network.view", func(w http.ResponseWriter, r *http.Request) {
			host := r.PathValue("host")
			c, l, err := a.wharfClient("")
			if err != nil {
				writeJSON(w, 200, map[string]any{"paired": false})
				return
			}
			out := map[string]any{"paired": true, "link": l, "published": a.wharfPublished(host), "sync_auto_bans": a.setting("wharf.sync_auto_bans") == "1"}
			ip := ""
			if len(l.TargetIPs) > 0 {
				ip = l.TargetIPs[0]
			}
			if l.has("devices.read") {
				if d, err := c.Devices(r.Context()); err == nil {
					out["devices"] = d
				}
			}
			if l.has("ddns") {
				if d, err := c.DDNS(r.Context()); err == nil {
					out["ddns"] = d
				}
			}
			if l.has("traffic.read") && ip != "" {
				if t, err := c.HostView(r.Context(), ip, "traffic", "24h"); err == nil {
					out["traffic"] = t
				}
				if t, err := c.HostView(r.Context(), ip, "inbound", "24h"); err == nil {
					out["inbound"] = t
				}
			}
			if l.has("sni") {
				if s, err := c.GetSNI(r.Context()); err == nil {
					out["sni"] = s
				}
			}
			writeJSON(w, 200, out)
		}))
	})

	pubProps := map[string]change.Prop{"site_id": {Type: "integer"}, "mode": {Type: "string", Enum: []string{"forward", "sni"}},
		"wan_port": {Type: "string"}, "target_ip": {Type: "string"}, "allow_countries": {Type: "array"}}
	change.Register(change.Kind{
		Name: "wharf.publish", Perm: "sites.manage", Scope: "sites:write", Title: "kind.wharf.publish",
		Params: change.ParamSchema{Props: pubProps, Required: []string{"site_id", "mode"}}, CLI: "berth wharf publish {{.site_id}}",
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			a := svc(env)
			var p wharfPublish
			json.Unmarshal(raw, &p)
			s, err := a.site(env.Host, p.SiteID)
			if err != nil {
				return change.Plan{}, err
			}
			scope := map[string]string{"forward": "forwards", "sni": "sni"}[p.Mode]
			if scope == "" {
				return change.Plan{}, errs.New(400, "wharf.bad_publish")
			}
			c, l, err := a.wharfClient(scope)
			if err != nil {
				return change.Plan{}, err
			}
			if p.TargetIP == "" && len(l.TargetIPs) > 0 {
				p.TargetIP = l.TargetIPs[0]
			}
			plan := change.Plan{Risk: change.RiskMedium, Undoable: true, SummaryP: map[string]any{"domain": s.Primary(), "port": p.WANPort, "wharf": l.Name}}
			if p.Mode == "forward" {
				if n, err := strconv.Atoi(p.WANPort); err != nil || n < 1 || n > 65535 {
					return change.Plan{}, errs.New(400, "wharf.bad_publish")
				}
				f := wharfapi.Forward{Name: "berth: " + s.Primary(), Proto: "tcp", WANPort: p.WANPort, TargetIP: p.TargetIP, TargetPort: sitePort(s),
					BerthRef: "site:" + s.Primary(), BerthHost: env.Host}
				prev, err := c.PlanForward(env.Ctx, f)
				if err != nil {
					return change.Plan{}, a.wharfErr(err)
				}
				plan.Summary = "plan.wharf.publish_forward"
				plan.Commands = []string{"Wharf: WAN " + p.WANPort + "/tcp → " + p.TargetIP + ":" + f.TargetPort, "# " + string(prev)}
			} else {
				plan.Summary = "plan.wharf.publish_sni"
				plan.Commands = []string{"Wharf proxy-front: " + strings.Join(s.Domains, ", ") + " → " + p.TargetIP + ":443 (TLS passes through)"}
			}
			return plan, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			a := svc(env)
			var p wharfPublish
			json.Unmarshal(raw, &p)
			s, err := a.site(env.Host, p.SiteID)
			if err != nil {
				return nil, nil, err
			}
			scope := map[string]string{"forward": "forwards", "sni": "sni"}[p.Mode]
			c, l, err := a.wharfClient(scope)
			if err != nil {
				return nil, nil, err
			}
			if p.TargetIP == "" && len(l.TargetIPs) > 0 {
				p.TargetIP = l.TargetIPs[0]
			}
			actor := actorOf(env, "公开站点 "+s.Primary())
			if old, _ := a.unpublishSite(env.Ctx, env.Host, p.SiteID, actor); old != nil { // switching mode or port
				env.Log("removed earlier publication (%s)", old.Mode)
			}
			if p.Mode == "forward" {
				f, err := c.CreateForward(env.Ctx, actor, wharfapi.Forward{Name: "berth: " + s.Primary(), Proto: "tcp", WANPort: p.WANPort, TargetIP: p.TargetIP,
					TargetPort: sitePort(s), Mode: map[bool]string{true: "restrict"}[len(p.Countries) > 0], AllowCountries: p.Countries,
					BerthRef: "site:" + s.Primary(), BerthHost: env.Host, Note: "由 Berth 新建站点时创建"})
				if err != nil {
					return nil, nil, a.wharfErr(err)
				}
				p.ForwardID = f.ID
			}
			spec, _ := json.Marshal(p)
			a.db.Exec(`INSERT OR REPLACE INTO wharf_publish(host, site_id, spec) VALUES(?,?,?)`, env.Host, p.SiteID, string(spec))
			if p.Mode == "sni" {
				if err := a.pushSNI(env.Ctx, c, actor, p.TargetIP); err != nil {
					a.db.Exec(`DELETE FROM wharf_publish WHERE host=? AND site_id=?`, env.Host, p.SiteID)
					return nil, nil, a.wharfErr(err)
				}
			}
			a.relate(env.Host, siteObj(s, env.Host), change.Object{Type: "wharf_forward", ID: firstNonEmpty(p.ForwardID, "sni:"+s.Primary()), Name: l.Name}, "published_by", "module")
			u, _ := json.Marshal(map[string]int64{"site_id": p.SiteID})
			return p, &change.UndoRecipe{Kind: "wharf.unpublish", Params: u}, nil
		},
	})
	change.Register(change.Kind{
		Name: "wharf.unpublish", Perm: "sites.manage", Scope: "sites:write", Title: "kind.wharf.unpublish",
		Params: change.ParamSchema{Props: map[string]change.Prop{"site_id": {Type: "integer"}}, Required: []string{"site_id"}}, CLI: "berth wharf unpublish {{.site_id}}",
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			var p struct {
				SiteID int64 `json:"site_id"`
			}
			json.Unmarshal(raw, &p)
			var spec string
			if svc(env).db.QueryRow(`SELECT spec FROM wharf_publish WHERE host=? AND site_id=?`, env.Host, p.SiteID).Scan(&spec) != nil {
				return change.Plan{}, errs.New(404, "wharf.not_published")
			}
			return change.Plan{Summary: "plan.wharf.unpublish", SummaryP: map[string]any{"site": p.SiteID}, Risk: change.RiskMedium, Undoable: true}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			a := svc(env)
			var p struct {
				SiteID int64 `json:"site_id"`
			}
			json.Unmarshal(raw, &p)
			old, err := a.unpublishSite(env.Ctx, env.Host, p.SiteID, actorOf(env, "撤销公开"))
			if err != nil {
				return nil, nil, err
			}
			if old == nil {
				return nil, nil, errs.New(404, "wharf.not_published")
			}
			old.ForwardID = ""
			u, _ := json.Marshal(old)
			return nil, &change.UndoRecipe{Kind: "wharf.publish", Params: u}, nil
		},
	})
	change.Register(change.Kind{
		Name: "wharf.ddns.claim", Perm: "sites.manage", Scope: "sites:write", Title: "kind.wharf.ddns.claim",
		Params: change.ParamSchema{Props: map[string]change.Prop{"domain": {Type: "string"}, "owner": {Type: "string", Enum: []string{"wharf", "berth"}}}, Required: []string{"domain", "owner"}},
		CLI:    "berth wharf ddns {{.domain}} {{.owner}}",
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			var p struct{ Domain, Owner string }
			json.Unmarshal(raw, &p)
			if !domainLike.MatchString(strings.ToLower(p.Domain)) || (p.Owner != "wharf" && p.Owner != "berth") {
				return change.Plan{}, errs.New(400, "wharf.bad_publish")
			}
			return change.Plan{Summary: "plan.wharf.ddns.claim_" + p.Owner, SummaryP: map[string]any{"domain": p.Domain}, Risk: change.RiskLow}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			a := svc(env)
			var p struct{ Domain, Owner string }
			json.Unmarshal(raw, &p)
			c, _, err := a.wharfClient("ddns")
			if err != nil {
				return nil, nil, err
			}
			return nil, nil, a.wharfErr(c.ClaimDDNS(env.Ctx, actorOf(env, "DDNS "+p.Domain), strings.ToLower(p.Domain), p.Owner))
		},
	})
	change.Register(change.Kind{
		Name: "wharf.unpair", Perm: "settings.manage", Scope: "settings:write", Title: "kind.wharf.unpair",
		Params: change.ParamSchema{Props: map[string]change.Prop{"keep": {Type: "boolean", Desc: "keep Berth-made mappings at Wharf as ordinary ones"}}},
		CLI:    "berth wharf unpair",
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			a := svc(env)
			l, err := a.wharf()
			if err != nil {
				return change.Plan{}, err
			}
			var p struct{ Keep bool }
			json.Unmarshal(raw, &p)
			n := 0
			for _, h := range append([]string{"local"}, a.remoteHostNames()...) {
				n += len(a.wharfPublished(h))
			}
			sum := "plan.wharf.unpair_delete"
			if p.Keep {
				sum = "plan.wharf.unpair_keep"
			}
			return change.Plan{Summary: sum, SummaryP: map[string]any{"name": l.Name, "count": n}, Risk: change.RiskHigh}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			a := svc(env)
			var p struct{ Keep bool }
			json.Unmarshal(raw, &p)
			l, err := a.wharf()
			if err != nil {
				return nil, nil, err
			}
			c := wharfapi.New(l.URL, l.FP, l.token)
			if !p.Keep {
				for _, h := range append([]string{"local"}, a.remoteHostNames()...) {
					for _, x := range a.wharfPublished(h) {
						a.unpublishSite(env.Ctx, h, x.SiteID, actorOf(env, "解除配对"))
					}
				}
			}
			// tell Wharf; if it cannot be reached the link is dropped here anyway
			if err := c.Unpair(env.Ctx, p.Keep); err != nil {
				env.Log("Wharf did not answer: %v", err)
			}
			a.dropWharfLink()
			return nil, nil, nil
		},
	})
	change.Register(change.Kind{
		Name: "wharf.alerts_primary", Perm: "settings.manage", Scope: "settings:write", Title: "kind.wharf.alerts_primary",
		Params: change.ParamSchema{Props: map[string]change.Prop{"primary": {Type: "string", Enum: []string{"", "wharf", "berth"}}}},
		CLI:    "berth wharf alerts {{.primary}}",
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			var p struct{ Primary string }
			json.Unmarshal(raw, &p)
			if _, err := svc(env).wharf(); err != nil {
				return change.Plan{}, err
			}
			return change.Plan{Summary: "plan.wharf.alerts_" + firstNonEmpty(p.Primary, "both"), Risk: change.RiskLow, Undoable: true}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			a := svc(env)
			var p struct{ Primary string }
			json.Unmarshal(raw, &p)
			l, err := a.wharf()
			if err != nil {
				return nil, nil, err
			}
			a.db.Exec(`UPDATE wharf_link SET alerts_primary=? WHERE id=1`, p.Primary)
			u, _ := json.Marshal(map[string]string{"primary": l.AlertsPrimary})
			return nil, &change.UndoRecipe{Kind: "wharf.alerts_primary", Params: u}, nil
		},
	})
}

// sitePort: what a mapping points at on the Berth host.
func sitePort(s Site) string {
	if s.TLS.Mode == "off" {
		return "80"
	}
	return "443"
}
