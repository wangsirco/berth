package panel

// DDNS: keep A/AAAA records pointing at a host's public address. The host's agent
// detects the address (majority of several sources); the panel updates the record when
// it changes and records each update.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/wangsirco/berth/internal/change"
	"github.com/wangsirco/berth/internal/errs"
	"github.com/wangsirco/berth/internal/panel/dns"
)

const ddnsEvery = 5 * time.Minute

type ddnsEntry struct {
	ID          int64  `json:"id"`
	Host        string `json:"host"`
	Integration string `json:"integration"`
	ZoneID      string `json:"zone_id"`
	Name        string `json:"name"`
	Type        string `json:"type"` // A | AAAA
	Proxied     bool   `json:"proxied"`
	Enabled     bool   `json:"enabled"`
	RecordID    string `json:"record_id,omitempty"`
	LastIP      string `json:"last_ip,omitempty"`
	LastCheck   int64  `json:"last_check,omitempty"`
	LastError   string `json:"last_error,omitempty"`
	// where the address comes from: auto (asked from the internet), iface:NAME (what leaves
	// through that card), wharf (the router's WAN address), fixed:IP
	Source string `json:"source"`
}

const ddnsCols = `id, host, integration, zone_id, name, type, proxied, enabled, record_id, last_ip, last_check, last_error, source`

func scanDDNS(row interface{ Scan(...any) error }) (ddnsEntry, error) {
	var d ddnsEntry
	var px, en int
	err := row.Scan(&d.ID, &d.Host, &d.Integration, &d.ZoneID, &d.Name, &d.Type, &px, &en, &d.RecordID, &d.LastIP, &d.LastCheck, &d.LastError, &d.Source)
	d.Proxied, d.Enabled = px == 1, en == 1
	return d, err
}

func (a *App) ddnsList() ([]ddnsEntry, error) {
	rows, err := a.db.Query(`SELECT ` + ddnsCols + ` FROM ddns ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ddnsEntry{}
	for rows.Next() {
		d, err := scanDDNS(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (a *App) ddnsEntry(id int64) (ddnsEntry, error) {
	d, err := scanDDNS(a.db.QueryRow(`SELECT `+ddnsCols+` FROM ddns WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return d, errs.New(404, "ddns.not_found", "id", id)
	}
	return d, err
}

func (a *App) publicIP(ctx context.Context, host, typ string) (string, error) {
	fam := "4"
	if typ == "AAAA" {
		fam = "6"
	}
	var out struct {
		IP string `json:"ip"`
	}
	err := a.call(ctx, host, "GET", "/v1/net/public-ip?family="+fam, nil, &out)
	return out.IP, err
}

// ddnsIP: the address a DDNS name should carry, from the entry's source.
func (a *App) ddnsIP(ctx context.Context, host, typ, source string) (string, error) {
	fam := "4"
	if typ == "AAAA" {
		fam = "6"
	}
	switch {
	case source == "" || source == "auto":
		return a.publicIP(ctx, host, typ)
	case strings.HasPrefix(source, "iface:"):
		var out struct {
			IP string `json:"ip"`
		}
		err := a.call(ctx, host, "GET", "/v1/net/public-ip?family="+fam+"&iface="+url.QueryEscape(strings.TrimPrefix(source, "iface:")), nil, &out)
		return out.IP, err
	case strings.HasPrefix(source, "fixed:"):
		ip := net.ParseIP(strings.TrimPrefix(source, "fixed:"))
		if ip == nil || (fam == "4") != (ip.To4() != nil) {
			return "", errs.New(400, "ddns.bad_source")
		}
		return ip.String(), nil
	case source == "wharf":
		c, _, err := a.wharfClient("ddns")
		if err != nil {
			return "", err
		}
		list, err := c.DDNS(ctx)
		if err != nil {
			return "", a.wharfErr(err)
		}
		for _, d := range list { // the router reports its WAN address on the names it keeps
			if ip := net.ParseIP(d.IP); ip != nil && (fam == "4") == (ip.To4() != nil) {
				return ip.String(), nil
			}
		}
		return "", errs.New(502, "ddns.wharf_no_ip")
	}
	return "", errs.New(400, "ddns.bad_source")
}

// syncDDNS makes one entry's record match the host's current address.
func (a *App) syncDDNS(ctx context.Context, d ddnsEntry) (changed bool, ip string, err error) {
	ip, err = a.ddnsIP(ctx, d.Host, d.Type, d.Source)
	if err == nil {
		in, ierr := a.integration(d.Integration)
		switch {
		case ierr != nil:
			err = ierr
		case dnsCan(in.Kind, "records"):
			var prov dns.Provider
			if prov, err = a.provider(d.Integration); err == nil {
				changed, err = a.upsertAddress(ctx, prov, &d, ip)
			}
		default: // DDNS-only service: send when the address moved (or it never got one)
			if ip != d.LastIP || d.LastError != "" {
				var up dns.Updater
				if up, err = a.updater(d.Integration); err == nil {
					if err = up.SetAddress(ctx, d.Name, ip); err != nil {
						err = dnsErr(err)
					} else {
						changed = true
					}
				}
			}
		}
	}
	msg := ""
	if err != nil {
		msg = errs.From(err).Code
		if de := errs.From(err); de.Params["detail"] != nil {
			msg += ": " + toString(de.Params["detail"])
		}
	}
	a.db.Exec(`UPDATE ddns SET last_ip=CASE WHEN ?<>'' THEN ? ELSE last_ip END, last_check=?, last_error=?, record_id=? WHERE id=?`,
		ip, ip, time.Now().Unix(), msg, d.RecordID, d.ID)
	return changed, ip, err
}

func toString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	b, _ := json.Marshal(v)
	return string(b)
}

func (a *App) upsertAddress(ctx context.Context, prov dns.Provider, d *ddnsEntry, ip string) (bool, error) {
	recs, err := prov.Records(ctx, d.ZoneID)
	if err != nil {
		return false, dnsErr(err)
	}
	for _, r := range recs {
		if (d.RecordID != "" && r.ID == d.RecordID) || (r.Name == d.Name && r.Type == d.Type) {
			d.RecordID = r.ID
			if r.Content == ip && r.Proxied == d.Proxied {
				return false, nil
			}
			r.Content, r.Proxied = ip, d.Proxied
			if _, err := prov.Update(ctx, d.ZoneID, r); err != nil {
				return false, dnsErr(err)
			}
			return true, nil
		}
	}
	rec, err := prov.Create(ctx, d.ZoneID, dns.Record{Type: d.Type, Name: d.Name, Content: ip, TTL: 60, Proxied: d.Proxied, Comment: "Berth DDNS"})
	if err != nil {
		return false, dnsErr(err)
	}
	d.RecordID = rec.ID
	return true, nil
}

func (a *App) ddnsLoop(ctx context.Context) {
	t := time.NewTicker(ddnsEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			a.ddnsTick(ctx)
		}
	}
}

func (a *App) ddnsTick(ctx context.Context) {
	list, err := a.ddnsList()
	if err != nil {
		return
	}
	for _, d := range list {
		if !d.Enabled {
			continue
		}
		if _, err := a.agent(d.Host); err != nil {
			continue // offline host: try next time
		}
		c, cancel := context.WithTimeout(ctx, time.Minute)
		old := d.LastIP
		changed, ip, err := a.syncDDNS(c, d)
		cancel()
		if err != nil {
			log.Printf("panel: ddns %s: %v", d.Name, err)
			continue
		}
		if changed {
			_, _ = a.pipe.Record(change.Actor{Type: "system", ID: "ddns", Name: "berth", Via: "api"}, d.Host, "ddns.update",
				map[string]any{"id": d.ID, "name": d.Name, "ip": ip, "before": old}, change.Plan{
					Summary: "plan.ddns.update", SummaryP: map[string]any{"name": d.Name, "ip": ip, "before": old}, Risk: change.RiskLow,
					Affected: []change.Object{{Type: "dns_record", ID: d.Integration + "/" + d.ZoneID + "/" + d.RecordID, Name: d.Name}}})
		}
	}
}

type ddnsParams struct {
	ID          int64  `json:"id,omitempty"`
	Integration string `json:"integration"`
	ZoneID      string `json:"zone_id"`
	Name        string `json:"name"`
	Type        string `json:"type"`
	Proxied     bool   `json:"proxied"`
	Enabled     *bool  `json:"enabled,omitempty"`
	Source      string `json:"source,omitempty"`
}

func init() {
	routeHooks = append(routeHooks, func(a *App, mux *http.ServeMux) {
		mux.HandleFunc("GET /api/v1/ddns", a.authed("sites.view", func(w http.ResponseWriter, r *http.Request) {
			list, err := a.ddnsList()
			if err != nil {
				a.fail(w, r, err)
				return
			}
			out := map[string]any{"items": list, "every_seconds": int(ddnsEvery.Seconds())}
			// names the router (Wharf) keeps: shown alongside, maintained there
			if c, l, err := a.wharfClient("ddns"); err == nil {
				ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
				if d, err := c.DDNS(ctx); err == nil {
					out["wharf"] = map[string]any{"name": l.Name, "items": d}
				}
				cancel()
			}
			writeJSON(w, 200, out)
		}))
		mux.HandleFunc("GET /api/v1/hosts/{host}/public-ip", a.authed("sites.view", func(w http.ResponseWriter, r *http.Request) {
			out := map[string]any{}
			for _, t := range []string{"A", "AAAA"} {
				if ip, err := a.publicIP(r.Context(), r.PathValue("host"), t); err == nil {
					out[map[string]string{"A": "ipv4", "AAAA": "ipv6"}[t]] = ip
				}
			}
			var lan struct {
				IP string `json:"ip"`
			}
			if a.call(r.Context(), r.PathValue("host"), "GET", "/v1/net/lan-ip", nil, &lan) == nil && lan.IP != "" {
				out["lan"] = lan.IP
			}
			writeJSON(w, 200, out)
		}))
	})

	props := map[string]change.Prop{
		"source": {Type: "string", Desc: "auto | iface:NAME | wharf | fixed:IP"},
		"id":     {Type: "integer"}, "integration": {Type: "string"}, "zone_id": {Type: "string"},
		"name": {Type: "string", Desc: "full record name, e.g. home.example.com"}, "type": {Type: "string", Enum: []string{"A", "AAAA"}},
		"proxied": {Type: "boolean"}, "enabled": {Type: "boolean"},
	}
	change.Register(change.Kind{
		Name: "ddns.set", Perm: "sites.manage", Scope: "sites:write", Title: "kind.ddns.set",
		Params: change.ParamSchema{Props: props, Required: []string{"integration", "name", "type"}},
		CLI:    "berth ddns set {{.name}} {{.type}}",
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			var p ddnsParams
			json.Unmarshal(raw, &p)
			if p.Type != "A" && p.Type != "AAAA" || p.Name == "" || !strings.Contains(p.Name, ".") {
				return change.Plan{}, errs.New(400, "dns.bad_record")
			}
			a := svc(env)
			in, err := a.integration(p.Integration)
			if err != nil {
				return change.Plan{}, err
			}
			if !dnsCan(in.Kind, "ddns") {
				return change.Plan{}, errs.New(400, "dns.records_unsupported", "kind", in.Kind)
			}
			if dnsCan(in.Kind, "records") && p.ZoneID == "" {
				return change.Plan{}, errs.New(400, "dns.bad_record")
			}
			ip, err := a.ddnsIP(env.Ctx, env.Host, p.Type, p.Source)
			if err != nil {
				return change.Plan{}, err
			}
			return change.Plan{Summary: "plan.ddns.set", SummaryP: map[string]any{"name": p.Name, "type": p.Type, "ip": ip, "source": sourceText(p.Source)}, Risk: change.RiskMedium,
				Affected: []change.Object{{Type: "ddns", ID: strconv.FormatInt(p.ID, 10), Name: p.Name, Host: env.Host}}, Undoable: true}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			var p ddnsParams
			json.Unmarshal(raw, &p)
			a := svc(env)
			en := p.Enabled == nil || *p.Enabled
			var undo *change.UndoRecipe
			if p.ID != 0 {
				old, err := a.ddnsEntry(p.ID)
				if err != nil {
					return nil, nil, err
				}
				_, err = a.db.Exec(`UPDATE ddns SET integration=?, zone_id=?, name=?, type=?, proxied=?, enabled=?, source=?, last_error='' WHERE id=?`,
					p.Integration, p.ZoneID, p.Name, p.Type, b2i(p.Proxied), b2i(en), sourceOr(p.Source), p.ID)
				if err != nil {
					return nil, nil, err
				}
				u, _ := json.Marshal(ddnsParams{ID: old.ID, Integration: old.Integration, ZoneID: old.ZoneID, Name: old.Name, Type: old.Type, Proxied: old.Proxied, Enabled: &old.Enabled, Source: old.Source})
				undo = &change.UndoRecipe{Kind: "ddns.set", Params: u}
			} else {
				res, err := a.db.Exec(`INSERT INTO ddns(host, integration, zone_id, name, type, proxied, enabled, source, created_at) VALUES(?,?,?,?,?,?,?,?,?)`,
					env.Host, p.Integration, p.ZoneID, p.Name, p.Type, b2i(p.Proxied), b2i(en), sourceOr(p.Source), time.Now().Unix())
				if err != nil {
					return nil, nil, err
				}
				p.ID, _ = res.LastInsertId()
				u, _ := json.Marshal(map[string]int64{"id": p.ID})
				undo = &change.UndoRecipe{Kind: "ddns.delete", Params: u}
			}
			d, _ := a.ddnsEntry(p.ID)
			var ip string
			if d.Enabled {
				var err error
				if _, ip, err = a.syncDDNS(env.Ctx, d); err != nil {
					env.Log("first update failed: %v", err)
				}
			}
			return map[string]any{"id": p.ID, "ip": ip}, undo, nil
		},
	})
	change.Register(change.Kind{
		Name: "ddns.delete", Perm: "sites.manage", Scope: "sites:write", Title: "kind.ddns.delete",
		Params: change.ParamSchema{Props: map[string]change.Prop{"id": {Type: "integer"}}, Required: []string{"id"}},
		CLI:    "berth ddns rm {{.id}}",
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			var p struct{ ID int64 }
			json.Unmarshal(raw, &p)
			d, err := svc(env).ddnsEntry(p.ID)
			if err != nil {
				return change.Plan{}, err
			}
			return change.Plan{Summary: "plan.ddns.delete", SummaryP: map[string]any{"name": d.Name}, Risk: change.RiskLow,
				Affected: []change.Object{{Type: "ddns", ID: strconv.FormatInt(d.ID, 10), Name: d.Name, Host: d.Host}}, Undoable: true}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			var p struct{ ID int64 }
			json.Unmarshal(raw, &p)
			a := svc(env)
			d, err := a.ddnsEntry(p.ID)
			if err != nil {
				return nil, nil, err
			}
			if _, err := a.db.Exec(`DELETE FROM ddns WHERE id=?`, p.ID); err != nil {
				return nil, nil, err
			}
			u, _ := json.Marshal(ddnsParams{Integration: d.Integration, ZoneID: d.ZoneID, Name: d.Name, Type: d.Type, Proxied: d.Proxied, Enabled: &d.Enabled, Source: d.Source})
			return nil, &change.UndoRecipe{Kind: "ddns.set", Params: u}, nil
		},
	})
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

func sourceOr(s string) string {
	if s == "" {
		return "auto"
	}
	return s
}

// sourceText: the source for the preview line, readable.
func sourceText(s string) string {
	switch {
	case s == "" || s == "auto":
		return "auto"
	case s == "wharf":
		return "Wharf WAN"
	}
	return strings.TrimPrefix(strings.TrimPrefix(s, "iface:"), "fixed:")
}
