package panel

// Sites (方案 §5.2): domains served by the host's edge (Caddy). The database is the
// source of truth; every change re-renders the whole Caddy config, validates it with
// the real Caddy, and loads it. A preview shows the config diff.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/wangsirco/berth/internal/agent/caddy"
	"github.com/wangsirco/berth/internal/change"
	"github.com/wangsirco/berth/internal/errs"
	"github.com/wangsirco/berth/internal/panel/caddycfg"
	"github.com/wangsirco/berth/internal/panel/certkit"
	"github.com/wangsirco/berth/internal/panel/dns"
)

type Site = caddycfg.Site

func siteObj(s Site, host string) change.Object {
	return change.Object{Type: "site", ID: strconv.FormatInt(s.ID, 10), Name: s.Primary(), Host: host}
}

func scanSite(row interface{ Scan(...any) error }) (Site, error) {
	var s Site
	var domains, target, tls, opts string
	var enabled int
	if err := row.Scan(&s.ID, &domains, &enabled, &target, &tls, &opts); err != nil {
		return s, err
	}
	s.Enabled = enabled == 1
	json.Unmarshal([]byte(domains), &s.Domains)
	json.Unmarshal([]byte(target), &s.Target)
	json.Unmarshal([]byte(tls), &s.TLS)
	json.Unmarshal([]byte(opts), &s.Options)
	return s, nil
}

const siteCols = `id, domains, enabled, target, tls, options`

func (a *App) sites(host string) ([]Site, error) {
	rows, err := a.db.Query(`SELECT `+siteCols+` FROM sites WHERE host=? ORDER BY id`, host)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Site{}
	for rows.Next() {
		s, err := scanSite(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func (a *App) site(host string, id int64) (Site, error) {
	s, err := scanSite(a.db.QueryRow(`SELECT `+siteCols+` FROM sites WHERE host=? AND id=?`, host, id))
	if errors.Is(err, sql.ErrNoRows) {
		return s, errs.New(404, "site.not_found", "id", id)
	}
	return s, err
}

// edgeEnv collects what rendering needs besides the sites (certificates, DNS accounts).
func (a *App) edgeEnv(host string) (caddycfg.Env, error) {
	env := caddycfg.Env{Email: a.setting("certs.email")}
	var err error
	if env.DNS, err = a.dnsProvidersForCaddy(); err != nil {
		return env, err
	}
	env.Certs = a.certFilesFor(host)
	env.ProjectPorts = a.projectPorts(host)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	env.NoWAF = !a.wafInstalled(ctx, host)
	env.LocalProxies = a.edgeSubnets(ctx, host)
	return env, nil
}

// Hooks filled in by the certificate and DNS modules.
var (
	dnsProvidersHook func(a *App) (map[string]map[string]any, error)
	certFilesHook    func(a *App, host string) map[int64]caddycfg.CertFiles
)

func (a *App) dnsProvidersForCaddy() (map[string]map[string]any, error) {
	if dnsProvidersHook == nil {
		return nil, nil
	}
	return dnsProvidersHook(a)
}

func (a *App) certFilesFor(host string) map[int64]caddycfg.CertFiles {
	if certFilesHook == nil {
		return nil
	}
	return certFilesHook(a, host)
}

// renderWith renders host's sites with one site replaced (or added, or removed when del).
func (a *App) renderWith(host string, s *Site, del bool) (before, after []byte, err error) {
	cur, err := a.sites(host)
	if err != nil {
		return nil, nil, err
	}
	env, err := a.edgeEnv(host)
	if err != nil {
		return nil, nil, err
	}
	before, err = caddycfg.Render(append([]Site{}, cur...), env)
	if err != nil {
		before = nil // the current state may reference something since deleted; show no diff base
	}
	// turning the WAF on needs the WAF extension (sites that already had it keep their setting)
	if s != nil && !del && s.Options.WAF != nil && env.NoWAF {
		had := false
		for _, c := range cur {
			if c.ID == s.ID && c.Options.WAF != nil {
				had = true
			}
		}
		if !had {
			return nil, nil, errs.New(409, "waf.not_installed")
		}
	}
	next := make([]Site, 0, len(cur)+1)
	replaced := false
	for _, c := range cur {
		if s != nil && c.ID == s.ID {
			replaced = true
			if del {
				continue
			}
			next = append(next, *s)
			continue
		}
		next = append(next, c)
	}
	if s != nil && !replaced && !del {
		next = append(next, *s)
	}
	after, err = caddycfg.Render(next, env)
	if err != nil {
		var p *caddycfg.Problem
		if errors.As(err, &p) {
			return nil, nil, errs.New(400, p.Code, flatten(p.Params)...)
		}
		return nil, nil, err
	}
	return before, after, nil
}

func flatten(m map[string]any) []any {
	var out []any
	for k, v := range m {
		out = append(out, k, v)
	}
	return out
}

// checkDomains rejects a domain already used by another site on any host.
func (a *App) checkDomains(s Site) error {
	rows, err := a.db.Query(`SELECT id, host, domains FROM sites WHERE id<>?`, s.ID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var host, ds string
		rows.Scan(&id, &host, &ds)
		var list []string
		json.Unmarshal([]byte(ds), &list)
		for _, d := range list {
			for _, n := range s.Domains {
				if d == n {
					return errs.New(409, "site.domain_taken", "domain", n, "host", host)
				}
			}
		}
	}
	return nil
}

// sitePlan is shared by create/update/delete/toggle: it renders, validates with the real
// Caddy and describes the change.
func (a *App) sitePlan(env change.Env, s *Site, del bool, summary string) (change.Plan, error) {
	st, err := a.edgeStatus(env.Ctx, env.Host)
	if err != nil {
		return change.Plan{}, err
	}
	if !st.Running {
		return change.Plan{}, errs.New(409, "edge.not_installed")
	}
	before, after, err := a.renderWith(env.Host, s, del)
	if err != nil {
		return change.Plan{}, err
	}
	if err := a.pushCerts(env.Ctx, env.Host, *s); err != nil {
		return change.Plan{}, err
	}
	if err := a.call(env.Ctx, env.Host, "POST", "/v1/edge/validate", json.RawMessage(after), nil); err != nil {
		return change.Plan{}, err
	}
	p := change.Plan{Summary: summary, SummaryP: map[string]any{"domain": s.Primary(), "domains": strings.Join(s.Domains, ", ")},
		Risk:     change.RiskMedium,
		Files:    []change.FileDiff{{Path: caddy.Root + "/config/config.json", Before: string(before), After: string(after)}},
		Affected: []change.Object{siteObj(*s, env.Host)}, Undoable: true}
	if !del && s.Target.Type == "container" {
		on, err := a.onEdgeNetwork(env.Ctx, env.Host, s.Target.Container)
		if err != nil {
			return change.Plan{}, err
		}
		if !on {
			p.Commands = append(p.Commands, "docker network connect "+caddy.Network+" "+s.Target.Container)
		}
		p.Affected = append(p.Affected, change.Object{Type: "container", ID: s.Target.Container, Name: s.Target.Container, Host: env.Host})
	}
	if del {
		p.Risk = change.RiskHigh
	}
	return p, nil
}

func (a *App) onEdgeNetwork(ctx context.Context, host, container string) (bool, error) {
	cs, err := a.containers(ctx, host)
	if err != nil {
		return false, err
	}
	for _, c := range cs {
		if c.Name == container {
			return contains(c.Networks, caddy.Network), nil
		}
	}
	return false, errs.New(404, "container.not_found", "detail", container)
}

// applyEdge re-renders from the database and loads the result.
func (a *App) applyEdge(env change.Env) error {
	_, after, err := a.renderWith(env.Host, nil, false)
	if err != nil {
		return err
	}
	if err := a.pushCerts(env.Ctx, env.Host); err != nil {
		return err
	}
	return a.call(env.Ctx, env.Host, "POST", "/v1/edge/load", json.RawMessage(after), nil)
}

func (a *App) saveSite(host string, s *Site, by string) error {
	d, _ := json.Marshal(s.Domains)
	t, _ := json.Marshal(s.Target)
	tl, _ := json.Marshal(s.TLS)
	o, _ := json.Marshal(s.Options)
	now := time.Now().Unix()
	en := 0
	if s.Enabled {
		en = 1
	}
	if s.ID == 0 {
		res, err := a.db.Exec(`INSERT INTO sites(host, primary_domain, domains, enabled, target, tls, options, created_by, created_at, updated_at) VALUES(?,?,?,?,?,?,?,?,?,?)`,
			host, s.Primary(), string(d), en, string(t), string(tl), string(o), by, now, now)
		if err != nil {
			return err
		}
		s.ID, _ = res.LastInsertId()
		return nil
	}
	res, err := a.db.Exec(`UPDATE sites SET primary_domain=?, domains=?, enabled=?, target=?, tls=?, options=?, updated_at=? WHERE id=? AND host=?`,
		s.Primary(), string(d), en, string(t), string(tl), string(o), now, s.ID, host)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 { // restoring a deleted site keeps its id
		_, err = a.db.Exec(`INSERT INTO sites(id, host, primary_domain, domains, enabled, target, tls, options, created_by, created_at, updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
			s.ID, host, s.Primary(), string(d), en, string(t), string(tl), string(o), by, now, now)
	}
	return err
}

func (a *App) relateSite(host string, s Site) {
	so := siteObj(s, host)
	if s.Target.Type == "container" {
		a.relate(host, so, change.Object{Type: "container", ID: s.Target.Container, Name: s.Target.Container, Host: host}, "proxies", "module")
	}
}

func (a *App) unrelateSite(host string, s Site) {
	if s.Target.Type == "container" {
		a.unrelate(host, siteObj(s, host), change.Object{Type: "container", ID: s.Target.Container}, "proxies")
	}
}

// ensureSiteCert gives an "internal" site a certificate from the panel's own CA (which
// the user's devices already trust), stored in the certificate center and renewed there.
// Names the CA may not sign (public domains) keep Caddy's internal issuer.
func (a *App) ensureSiteCert(s *Site) error {
	if s.TLS.Mode != "internal" || s.TLS.CertID != 0 || a.localSigner == nil {
		return nil
	}
	f := certkit.Fields{DNS: s.Domains, Days: 397}
	if a.checkLocalNames(f) != nil {
		return nil
	}
	b, err := a.issue("internal", f)
	if err != nil {
		return err
	}
	c := certRow{Name: "site " + s.Primary(), Source: "internal", Fields: &f, AutoRenew: true, History: []certVersion{}}
	if err := a.storeCert(&c, b); err != nil {
		return err
	}
	s.TLS.CertID = c.ID
	return nil
}

func parseSite(raw json.RawMessage) (Site, error) {
	var s Site
	if err := json.Unmarshal(raw, &s); err != nil {
		return s, errs.New(400, "params.invalid_json")
	}
	if err := caddycfg.Normalize(&s); err != nil {
		var p *caddycfg.Problem
		errors.As(err, &p)
		return s, errs.New(400, p.Code, flatten(p.Params)...)
	}
	return s, nil
}

// siteDNS: records published with a site (create) or to put back (undo of delete).
type siteDNS struct {
	DNS *struct {
		Integration string `json:"integration"`
		ZoneID      string `json:"zone_id"`
		Type        string `json:"type"` // A | AAAA | CNAME
		Content     string `json:"content"`
		Proxied     bool   `json:"proxied"`
	} `json:"dns,omitempty"`
	RestoreDNS []recordParams `json:"restore_dns,omitempty"`
	WithDNS    bool           `json:"with_dns,omitempty"`
}

func (d siteDNS) records(s Site) []recordParams {
	if len(d.RestoreDNS) > 0 {
		return d.RestoreDNS
	}
	if d.DNS == nil {
		return nil
	}
	var out []recordParams
	for _, name := range s.Domains {
		out = append(out, recordParams{Integration: d.DNS.Integration, ZoneID: d.DNS.ZoneID,
			Record: dns.Record{Type: d.DNS.Type, Name: name, Content: d.DNS.Content, TTL: 1, Proxied: d.DNS.Proxied, Comment: "Berth site"}})
	}
	return out
}

func dnsLine(r recordParams) string {
	l := "DNS " + r.Record.Type + " " + r.Record.Name + " → " + r.Record.Content
	if r.Record.Proxied {
		l += " (proxied)"
	}
	return l
}

// publishDNS creates the records (an identical existing record is adopted) and
// relates them to the site.
func (a *App) publishDNS(env change.Env, s Site, recs []recordParams) error {
	for _, r := range recs {
		prov, err := a.provider(r.Integration)
		if err != nil {
			return err
		}
		existing, err := prov.Records(env.Ctx, r.ZoneID)
		if err != nil {
			return dnsErr(err)
		}
		var id string
		for _, e := range existing {
			if e.Name == r.Record.Name && e.Type == r.Record.Type && e.Content == r.Record.Content {
				id = e.ID
			}
		}
		if id == "" {
			r.Record.ID = ""
			rec, err := prov.Create(env.Ctx, r.ZoneID, r.Record)
			if err != nil {
				return dnsErr(err)
			}
			id = rec.ID
			env.Log("created %s", dnsLine(r))
		}
		a.relate(env.Host, siteObj(s, env.Host), change.Object{Type: "dns_record", ID: r.Integration + "/" + r.ZoneID + "/" + id, Name: r.Record.Name}, "publishes", "module")
	}
	return nil
}

// unpublishDNS deletes the site's records and returns them for undo.
func (a *App) unpublishDNS(env change.Env, s Site) ([]recordParams, error) {
	var removed []recordParams
	for _, rel := range a.related(env.Host, "site", strconv.FormatInt(s.ID, 10)) {
		if rel.Kind != "publishes" || rel.To.Type != "dns_record" {
			continue
		}
		parts := strings.SplitN(rel.To.ID, "/", 3)
		if len(parts) != 3 {
			continue
		}
		prov, err := a.provider(parts[0])
		if err != nil {
			return removed, err
		}
		cur, err := a.findRecord(env.Ctx, prov, parts[1], parts[2])
		if err != nil {
			continue // already gone
		}
		if err := prov.Delete(env.Ctx, parts[1], parts[2]); err != nil {
			return removed, dnsErr(err)
		}
		cur.ID = ""
		removed = append(removed, recordParams{Integration: parts[0], ZoneID: parts[1], Record: cur})
		a.unrelate(env.Host, siteObj(s, env.Host), rel.To, "publishes")
		env.Log("deleted DNS %s %s", cur.Type, cur.Name)
	}
	return removed, nil
}

func siteSnapshot(s Site) json.RawMessage {
	b, _ := json.Marshal(s)
	return b
}

func init() {
	routeHooks = append(routeHooks, func(a *App, mux *http.ServeMux) {
		mux.HandleFunc("GET /api/v1/hosts/{host}/sites", a.authed("sites.view", a.handleSites))
		mux.HandleFunc("GET /api/v1/hosts/{host}/sites/{id}", a.authed("sites.view", a.handleSite))
		mux.HandleFunc("GET /api/v1/hosts/{host}/sites/targets", a.authed("sites.view", a.handleSiteTargets))
	})

	siteProps := map[string]change.Prop{
		"id":      {Type: "integer"},
		"domains": {Type: "array", Desc: "domain names; the first is the primary"},
		"enabled": {Type: "boolean"},
		"target":  {Type: "object", Desc: "{type: container|address|static|redirect, container, port, upstreams, scheme, spa, to, code}"},
		"tls":     {Type: "object", Desc: "{mode: auto|internal|dns|custom|off, dns, cert_id}"},
		"options": {Type: "object", Desc: "HSTS, www redirect, headers, CORS, path rules, basic auth, IP rules..."},
	}
	siteProps["dns"] = change.Prop{Type: "object", Desc: "also publish DNS records: {integration, zone_id, type: A|AAAA|CNAME, content, proxied}"}
	byID := change.ParamSchema{Props: map[string]change.Prop{"id": {Type: "integer"}, "with_dns": {Type: "boolean", Desc: "also delete the DNS records the site published"}}, Required: []string{"id"}}

	change.Register(change.Kind{
		Name: "site.create", Perm: "sites.manage", Scope: "sites:write", Title: "kind.site.create",
		Params: change.ParamSchema{Props: siteProps, Required: []string{"domains", "target"}},
		CLI:    "berth site create {{.domains}}",
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			a := svc(env)
			s, err := parseSite(raw)
			if err != nil {
				return change.Plan{}, err
			}
			if s.ID != 0 { // restore of a deleted site (undo)
				if _, err := a.site(env.Host, s.ID); err == nil {
					return change.Plan{}, errs.New(409, "site.exists")
				}
			} else {
				s.ID = -1 // placeholder so the renderer can tell it apart
			}
			s.Enabled = true
			if err := a.checkDomains(s); err != nil {
				return change.Plan{}, err
			}
			plan, err := a.sitePlan(env, &s, false, "plan.site.create")
			if err != nil {
				return plan, err
			}
			var d siteDNS
			json.Unmarshal(raw, &d)
			for _, r := range d.records(s) {
				if err := validRecord(r.Record); err != nil {
					return plan, err
				}
				if _, err := a.provider(r.Integration); err != nil {
					return plan, err
				}
				plan.Commands = append(plan.Commands, dnsLine(r))
				plan.Affected = append(plan.Affected, change.Object{Type: "dns_record", ID: r.Record.Name, Name: r.Record.Name})
			}
			return plan, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			a := svc(env)
			s, err := parseSite(raw)
			if err != nil {
				return nil, nil, err
			}
			s.Enabled = true
			if err := a.ensureSiteCert(&s); err != nil {
				return nil, nil, err
			}
			if s.Target.Type == "container" {
				if err := a.call(env.Ctx, env.Host, "POST", "/v1/edge/connect", map[string]string{"container": s.Target.Container}, nil); err != nil {
					return nil, nil, err
				}
			}
			if err := a.saveSite(env.Host, &s, ""); err != nil {
				return nil, nil, err
			}
			if err := a.syncPHPSite(env, &s, nil); err != nil {
				if isNativePHP(&s) { // take down what the failed start left behind
					a.call(env.Ctx, env.Host, "POST", "/v1/phpsite/remove", map[string]any{"id": s.ID, "domain": s.Primary(), "owner": a.setting(ownerKey(s.ID))}, nil)
					a.setSetting(ownerKey(s.ID), "")
				}
				a.db.Exec(`DELETE FROM sites WHERE id=?`, s.ID)
				return nil, nil, err
			}
			if err := a.applyEdge(env); err != nil {
				a.syncPHPSite(env, &Site{}, &s)
				a.db.Exec(`DELETE FROM sites WHERE id=?`, s.ID)
				return nil, nil, err
			}
			a.relateSite(env.Host, s)
			var d siteDNS
			json.Unmarshal(raw, &d)
			undo, _ := json.Marshal(map[string]any{"id": s.ID, "with_dns": true})
			if err := a.publishDNS(env, s, d.records(s)); err != nil {
				// the site is up; the DNS part failed: keep the site and say so
				env.Log("DNS: %v", err)
				return map[string]any{"id": s.ID, "domains": s.Domains, "dns_error": errs.From(err).Code}, &change.UndoRecipe{Kind: "site.delete", Params: undo}, nil
			}
			return map[string]any{"id": s.ID, "domains": s.Domains}, &change.UndoRecipe{Kind: "site.delete", Params: undo}, nil
		},
	})

	change.Register(change.Kind{
		Name: "site.update", Perm: "sites.manage", Scope: "sites:write", Title: "kind.site.update",
		Params: change.ParamSchema{Props: siteProps, Required: []string{"id", "domains", "target"}},
		CLI:    "berth site edit {{.id}}",
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			a := svc(env)
			s, err := parseSite(raw)
			if err != nil {
				return change.Plan{}, err
			}
			old, err := a.site(env.Host, s.ID)
			if err != nil {
				return change.Plan{}, err
			}
			s.Enabled = old.Enabled
			if err := a.checkDomains(s); err != nil {
				return change.Plan{}, err
			}
			return a.sitePlan(env, &s, false, "plan.site.update")
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			a := svc(env)
			s, err := parseSite(raw)
			if err != nil {
				return nil, nil, err
			}
			old, err := a.site(env.Host, s.ID)
			if err != nil {
				return nil, nil, err
			}
			s.Enabled = old.Enabled
			if s.TLS.Mode == "internal" && s.TLS.CertID == 0 && old.TLS.Mode == "internal" {
				s.TLS.CertID = old.TLS.CertID // keep the certificate it already has
			}
			if err := a.ensureSiteCert(&s); err != nil {
				return nil, nil, err
			}
			if s.Target.Type == "container" {
				if err := a.call(env.Ctx, env.Host, "POST", "/v1/edge/connect", map[string]string{"container": s.Target.Container}, nil); err != nil {
					return nil, nil, err
				}
			}
			// a static or PHP site lives in a folder named after its primary domain: a new
			// primary takes the folder along (unless one with the new name already exists)
			if (s.Target.Type == "static" || s.Target.Type == "php") && (old.Target.Type == "static" || old.Target.Type == "php") && s.Primary() != old.Primary() {
				from, to := "/srv/berth/caddy/sites/"+old.Primary(), "/srv/berth/caddy/sites/"+s.Primary()
				if err := a.call(env.Ctx, env.Host, "POST", "/v1/files/rename", map[string]string{"root": "/", "path": from, "to": to}, nil); err == nil {
					env.Log("moved %s → %s", from, to)
				} else {
					env.Log("kept the folders as they are (%v)", err)
				}
			}
			if err := a.saveSite(env.Host, &s, ""); err != nil {
				return nil, nil, err
			}
			if isNativePHP(&s) { // the site's own FPM comes up before Caddy is pointed at it
				if err := a.syncPHPSite(env, &s, &old); err != nil {
					a.saveSite(env.Host, &old, "")
					return nil, nil, err
				}
			}
			if err := a.applyEdge(env); err != nil {
				a.saveSite(env.Host, &old, "")
				return nil, nil, err
			}
			if !isNativePHP(&s) && isNativePHP(&old) { // and goes away only after Caddy has left it
				if err := a.syncPHPSite(env, &s, &old); err != nil {
					env.Log("stopping the site's own PHP: %v", err)
				}
			}
			a.unrelateSite(env.Host, old)
			a.relateSite(env.Host, s)
			return map[string]any{"id": s.ID}, &change.UndoRecipe{Kind: "site.update", Params: siteSnapshot(old)}, nil
		},
	})

	change.Register(change.Kind{
		Name: "site.delete", Perm: "sites.manage", Scope: "sites:write", Title: "kind.site.delete", Params: byID,
		CLI: "berth site rm {{.id}}",
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			var p struct{ ID int64 }
			json.Unmarshal(raw, &p)
			a := svc(env)
			s, err := a.site(env.Host, p.ID)
			if err != nil {
				return change.Plan{}, err
			}
			plan, err := a.sitePlan(env, &s, true, "plan.site.delete")
			if err != nil {
				return plan, err
			}
			var d siteDNS
			json.Unmarshal(raw, &d)
			for _, r := range a.related(env.Host, "site", strconv.FormatInt(s.ID, 10)) {
				plan.Affected = append(plan.Affected, r.To)
				if r.Kind == "publishes" && d.WithDNS {
					plan.Commands = append(plan.Commands, "delete DNS record "+r.To.Name)
				}
			}
			return plan, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			var p struct{ ID int64 }
			json.Unmarshal(raw, &p)
			a := svc(env)
			s, err := a.site(env.Host, p.ID)
			if err != nil {
				return nil, nil, err
			}
			if _, err := a.db.Exec(`DELETE FROM sites WHERE id=?`, s.ID); err != nil {
				return nil, nil, err
			}
			if err := a.applyEdge(env); err != nil {
				a.saveSite(env.Host, &s, "")
				return nil, nil, err
			}
			if isNativePHP(&s) {
				if err := a.syncPHPSite(env, &Site{}, &s); err != nil {
					env.Log("stopping the site's own PHP: %v", err)
				}
			}
			var d siteDNS
			json.Unmarshal(raw, &d)
			var removed []recordParams
			if d.WithDNS {
				removed, err = a.unpublishDNS(env, s)
				if err != nil {
					env.Log("DNS: %v", err)
				}
			}
			a.unrelateSite(env.Host, s)
			snap := map[string]any{}
			json.Unmarshal(siteSnapshot(s), &snap)
			if len(removed) > 0 {
				snap["restore_dns"] = removed
			}
			u, _ := json.Marshal(snap)
			return map[string]any{"id": s.ID}, &change.UndoRecipe{Kind: "site.create", Params: u}, nil
		},
	})

	change.Register(change.Kind{
		Name: "site.toggle", Perm: "sites.manage", Scope: "sites:write", Title: "kind.site.toggle",
		Params: change.ParamSchema{Props: map[string]change.Prop{"id": {Type: "integer"}, "enabled": {Type: "boolean"}}, Required: []string{"id", "enabled"}},
		CLI:    "berth site {{if .enabled}}enable{{else}}disable{{end}} {{.id}}",
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			var p struct {
				ID      int64
				Enabled bool
			}
			json.Unmarshal(raw, &p)
			a := svc(env)
			s, err := a.site(env.Host, p.ID)
			if err != nil {
				return change.Plan{}, err
			}
			s.Enabled = p.Enabled
			summary := "plan.site.disable"
			if p.Enabled {
				summary = "plan.site.enable"
			}
			return a.sitePlan(env, &s, false, summary)
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			var p struct {
				ID      int64
				Enabled bool
			}
			json.Unmarshal(raw, &p)
			a := svc(env)
			s, err := a.site(env.Host, p.ID)
			if err != nil {
				return nil, nil, err
			}
			was := s.Enabled
			s.Enabled = p.Enabled
			if err := a.saveSite(env.Host, &s, ""); err != nil {
				return nil, nil, err
			}
			if err := a.applyEdge(env); err != nil {
				s.Enabled = was
				a.saveSite(env.Host, &s, "")
				return nil, nil, err
			}
			u, _ := json.Marshal(map[string]any{"id": s.ID, "enabled": was})
			return nil, &change.UndoRecipe{Kind: "site.toggle", Params: u}, nil
		},
	})
}

func (a *App) handleSites(w http.ResponseWriter, r *http.Request) {
	list, err := a.sites(r.PathValue("host"))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"items": list})
}

func (a *App) handleSite(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	host := r.PathValue("host")
	s, err := a.site(host, id)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"site": s, "relations": a.related(host, "site", strconv.FormatInt(id, 10))})
}

type targetCandidate struct {
	Container string   `json:"container"`
	Project   string   `json:"project,omitempty"`
	Image     string   `json:"image"`
	Ports     []int    `json:"ports"`
	Loopback  []int    `json:"loopback,omitempty"` // only reachable inside the container
	OnEdge    bool     `json:"on_edge"`
	UsedBy    []string `json:"used_by,omitempty"`
}

func (a *App) handleSiteTargets(w http.ResponseWriter, r *http.Request) {
	host := r.PathValue("host")
	cs, err := a.containers(r.Context(), host)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	sites, _ := a.sites(host)
	used := map[string][]string{}
	for _, s := range sites {
		if s.Target.Type == "container" {
			used[s.Target.Container] = append(used[s.Target.Container], s.Primary())
		}
	}
	out := []targetCandidate{}
	for _, c := range cs {
		if c.State != "running" || c.Name == caddy.Container {
			continue
		}
		tc := targetCandidate{Container: c.Name, Project: c.Project, Image: c.Image, Ports: []int{}, OnEdge: contains(c.Networks, caddy.Network), UsedBy: used[c.Name]}
		var ls []struct {
			Port     int    `json:"port"`
			Proto    string `json:"proto"`
			Loopback bool   `json:"loopback"`
		}
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		_ = a.call(ctx, host, "GET", "/v1/docker/containers/"+c.ID+"/listening", nil, &ls)
		cancel()
		for _, l := range ls {
			if l.Proto != "tcp" && l.Proto != "tcp6" {
				continue
			}
			if l.Loopback {
				tc.Loopback = appendUniq(tc.Loopback, l.Port)
			} else {
				tc.Ports = appendUniq(tc.Ports, l.Port)
			}
		}
		out = append(out, tc)
	}
	writeJSON(w, 200, map[string]any{"items": out})
}

func appendUniq(l []int, v int) []int {
	for _, x := range l {
		if x == v {
			return l
		}
	}
	return append(l, v)
}

// edgeSubnets: the edge network's subnets (cloudflared and the edge sit on it).
func (a *App) edgeSubnets(ctx context.Context, host string) []string {
	var nets []struct {
		Name   string `json:"name"`
		Subnet string `json:"subnet"`
	}
	if a.call(ctx, host, "GET", "/v1/docker/networks", nil, &nets) != nil {
		return nil
	}
	for _, n := range nets {
		if n.Name == "berth-edge" && n.Subnet != "" {
			return strings.Fields(strings.ReplaceAll(n.Subnet, ",", " "))
		}
	}
	return nil
}
