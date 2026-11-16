package panel

// DNS accounts (方案 §5.2.5): API credentials for DNS providers, sealed with the master
// key. Cloudflare gets full record management; every provider can be used by Caddy for
// DNS-01 certificates (wildcards, hosts without a public port 80).

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/wangsirco/berth/internal/change"
	"github.com/wangsirco/berth/internal/errs"
	"github.com/wangsirco/berth/internal/panel/dns"
)

// cloudflareBase is the API root (tests point it at a fake).
var cloudflareBase = "https://api.cloudflare.com/client/v4"

// dnsKinds: credential fields per provider, named as Caddy's libdns modules expect them.
var dnsKinds = map[string][]string{
	"cloudflare":   {"api_token"},
	"alidns":       {"access_key_id", "access_key_secret"},
	"tencentcloud": {"secret_id", "secret_key"},
	"route53":      {"access_key_id", "secret_access_key", "region"},
	// DDNS-only services: they can point one name at an address, nothing more
	"duckdns":   {"token"},
	"dyndns2":   {"server", "username", "password"},
	"customurl": {"url"},
}

// what each kind can do: issue certificates (DNS-01 via Caddy), manage records, DDNS
var dnsCaps = map[string][]string{
	"cloudflare": {"certs", "records", "ddns"}, "alidns": {"certs", "records", "ddns"}, "tencentcloud": {"certs", "records", "ddns"},
	"route53": {"certs"}, "duckdns": {"ddns"}, "dyndns2": {"ddns"}, "customurl": {"ddns"},
}

func dnsCan(kind, what string) bool { return contains(dnsCaps[kind], what) }

var (
	aliBase    = "https://alidns.aliyuncs.com"
	dnspodBase = "https://dnspod.tencentcloudapi.com"
)

var integrationIDRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)

type integration struct {
	ID        string     `json:"id"`
	Kind      string     `json:"kind"`
	Name      string     `json:"name"`
	Check     *dns.Check `json:"check,omitempty"`
	Hint      string     `json:"hint,omitempty"` // last characters of the token
	CreatedAt int64      `json:"created_at"`
	UpdatedAt int64      `json:"updated_at"`
	UsedBy    []string   `json:"used_by,omitempty"`
	secret    map[string]string
	secretEnc []byte
}

func (a *App) integrations() ([]integration, error) {
	rows, err := a.db.Query(`SELECT id, kind, name, meta, secret_enc, created_at, updated_at FROM integrations ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []integration{}
	for rows.Next() {
		in, err := scanIntegration(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, in)
	}
	return out, rows.Err()
}

func scanIntegration(row interface{ Scan(...any) error }) (integration, error) {
	var in integration
	var meta string
	if err := row.Scan(&in.ID, &in.Kind, &in.Name, &meta, &in.secretEnc, &in.CreatedAt, &in.UpdatedAt); err != nil {
		return in, err
	}
	var m struct {
		Check *dns.Check `json:"check"`
		Hint  string     `json:"hint"`
	}
	json.Unmarshal([]byte(meta), &m)
	in.Check, in.Hint = m.Check, m.Hint
	return in, nil
}

func (a *App) integration(id string) (integration, error) {
	in, err := scanIntegration(a.db.QueryRow(`SELECT id, kind, name, meta, secret_enc, created_at, updated_at FROM integrations WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return in, errs.New(404, "dns.account_not_found", "id", id)
	}
	if err != nil {
		return in, err
	}
	plain, err := a.open(in.secretEnc)
	if err != nil {
		return in, err
	}
	json.Unmarshal(plain, &in.secret)
	return in, nil
}

// provider returns the record-management client for an account.
func (a *App) provider(id string) (dns.Provider, error) {
	in, err := a.integration(id)
	if err != nil {
		return nil, err
	}
	return providerFor(in.Kind, in.secret)
}

func providerFor(kind string, sec map[string]string) (dns.Provider, error) {
	switch kind {
	case "cloudflare":
		cf := dns.NewCloudflare(sec["api_token"])
		cf.Base = cloudflareBase
		return cf, nil
	case "alidns":
		p := dns.NewAliDNS(sec["access_key_id"], sec["access_key_secret"])
		p.Base = aliBase
		return p, nil
	case "tencentcloud":
		p := dns.NewDNSPod(sec["secret_id"], sec["secret_key"])
		p.Base = dnspodBase
		return p, nil
	}
	return nil, errs.New(400, "dns.records_unsupported", "kind", kind)
}

// updater: the DDNS client of a DDNS-only account.
func (a *App) updater(id string) (dns.Updater, error) {
	in, err := a.integration(id)
	if err != nil {
		return nil, err
	}
	switch in.Kind {
	case "duckdns":
		return dns.DuckDNS{Token: in.secret["token"]}, nil
	case "dyndns2":
		return dns.DynDNS2{Server: in.secret["server"], User: in.secret["username"], Pass: in.secret["password"]}, nil
	case "customurl":
		return dns.CustomURL{URL: in.secret["url"]}, nil
	}
	return nil, errs.New(400, "dns.records_unsupported", "kind", in.Kind)
}

// dnsErr turns a provider error into a translatable one.
func dnsErr(err error) error {
	var de *dns.Error
	if errors.As(err, &de) {
		status := 502
		switch de.Code {
		case "dns.auth", "dns.forbidden":
			status = 403
		case "dns.not_found":
			status = 404
		case "dns.conflict":
			status = 409
		}
		return errs.New(status, de.Code, "detail", de.Detail)
	}
	return err
}

func dnsProvidersForCaddy(a *App) (map[string]map[string]any, error) {
	list, err := a.integrations()
	if err != nil {
		return nil, err
	}
	out := map[string]map[string]any{}
	for _, l := range list {
		in, err := a.integration(l.ID)
		if err != nil {
			continue
		}
		if !dnsCan(in.Kind, "certs") {
			continue // DDNS-only services cannot answer certificate challenges
		}
		p := map[string]any{"name": in.Kind}
		for k, v := range in.secret {
			p[k] = v
		}
		out[in.ID] = p
	}
	return out, nil
}

// integrationUsers: sites (DNS-01) and DDNS entries using an account.
func (a *App) integrationUsers(id string) []string {
	var out []string
	rows, err := a.db.Query(`SELECT host, primary_domain, tls FROM sites`)
	if err == nil {
		for rows.Next() {
			var host, dom, tls string
			rows.Scan(&host, &dom, &tls)
			if strings.Contains(tls, `"dns":"`+id+`"`) {
				out = append(out, host+"/"+dom)
			}
		}
		rows.Close()
	}
	rows, err = a.db.Query(`SELECT name FROM ddns WHERE integration=?`, id)
	if err == nil {
		for rows.Next() {
			var n string
			rows.Scan(&n)
			out = append(out, "ddns:"+n)
		}
		rows.Close()
	}
	return out
}

type integrationParams struct {
	ID          string            `json:"id"`
	Kind        string            `json:"kind"`
	Name        string            `json:"name"`
	Credentials map[string]string `json:"credentials,omitempty"`
	Sealed      string            `json:"sealed,omitempty"` // undo: the previous credentials, sealed
}

func (a *App) credentialsOf(p integrationParams) (map[string]string, error) {
	if p.Sealed != "" {
		enc, err := base64.StdEncoding.DecodeString(p.Sealed)
		if err != nil {
			return nil, errs.New(400, "params.invalid_json")
		}
		plain, err := a.open(enc)
		if err != nil {
			return nil, err
		}
		var m map[string]string
		json.Unmarshal(plain, &m)
		return m, nil
	}
	fields, ok := dnsKinds[p.Kind]
	if !ok {
		return nil, errs.New(400, "params.enum", "name", "kind", "allowed", "cloudflare, alidns, tencentcloud, route53, duckdns, dyndns2, customurl")
	}
	out := map[string]string{}
	for _, f := range fields {
		v := strings.TrimSpace(p.Credentials[f])
		if v == "" && f != "region" {
			return nil, errs.New(400, "dns.missing_field", "field", f)
		}
		if v != "" {
			out[f] = v
		}
	}
	return out, nil
}

func (a *App) verifyCredentials(ctx context.Context, kind string, cred map[string]string) (*dns.Check, error) {
	if kind == "alidns" || kind == "tencentcloud" {
		p, _ := providerFor(kind, cred)
		ch, err := p.Verify(ctx)
		if err != nil {
			return nil, dnsErr(err)
		}
		return &ch, nil
	}
	if kind != "cloudflare" {
		return nil, nil // other providers are checked when first used
	}
	cf := dns.NewCloudflare(cred["api_token"])
	cf.Base = cloudflareBase
	ch, err := cf.Verify(ctx)
	if err != nil {
		return nil, dnsErr(err)
	}
	if !ch.Active {
		return nil, errs.New(403, "dns.token_inactive", "status", ch.Message)
	}
	return &ch, nil
}

func hintOf(cred map[string]string) string {
	for _, k := range []string{"api_token", "access_key_id", "secret_id"} {
		if v := cred[k]; len(v) > 8 {
			return "…" + v[len(v)-4:]
		}
	}
	return ""
}

func init() {
	dnsProvidersHook = dnsProvidersForCaddy
	routeHooks = append(routeHooks, func(a *App, mux *http.ServeMux) {
		mux.HandleFunc("GET /api/v1/integrations", a.authed("sites.view", a.handleIntegrations))
		mux.HandleFunc("GET /api/v1/integrations/{id}/zones", a.authed("sites.view", a.handleZones))
		mux.HandleFunc("GET /api/v1/integrations/{id}/zones/{zone}/records", a.authed("sites.view", a.handleRecords))
	})

	props := map[string]change.Prop{
		"id":          {Type: "string", Desc: "short id, e.g. cf-main"},
		"kind":        {Type: "string", Enum: []string{"cloudflare", "alidns", "tencentcloud", "route53", "duckdns", "dyndns2", "customurl"}},
		"name":        {Type: "string"},
		"credentials": {Type: "object", Desc: "cloudflare: {api_token}; alidns: {access_key_id, access_key_secret}; tencentcloud: {secret_id, secret_key}; route53: {access_key_id, secret_access_key, region}"},
	}
	change.Register(change.Kind{
		Name: "integration.set", Perm: "settings.manage", Scope: "settings:write", Title: "kind.integration.set",
		Params: change.ParamSchema{Props: props, Required: []string{"id", "kind"}}, Redact: []string{"credentials", "sealed"},
		CLI: "berth dns account set {{.id}} --kind {{.kind}}",
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			a := svc(env)
			var p integrationParams
			json.Unmarshal(raw, &p)
			if !integrationIDRe.MatchString(p.ID) {
				return change.Plan{}, errs.New(400, "dns.bad_account_id")
			}
			cred, err := a.credentialsOf(p)
			if err != nil {
				return change.Plan{}, err
			}
			ch, err := a.verifyCredentials(env.Ctx, p.Kind, cred)
			if err != nil {
				return change.Plan{}, err
			}
			sp := map[string]any{"id": p.ID, "kind": p.Kind}
			if ch != nil {
				sp["zones"] = ch.ZoneCount
			}
			return change.Plan{Summary: "plan.integration.set", SummaryP: sp, Risk: change.RiskMedium,
				Affected: []change.Object{{Type: "integration", ID: p.ID, Name: firstNonEmpty(p.Name, p.ID)}}, Undoable: true}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			a := svc(env)
			var p integrationParams
			json.Unmarshal(raw, &p)
			cred, err := a.credentialsOf(p)
			if err != nil {
				return nil, nil, err
			}
			ch, err := a.verifyCredentials(env.Ctx, p.Kind, cred)
			if err != nil {
				return nil, nil, err
			}
			plain, _ := json.Marshal(cred)
			enc, err := a.seal(plain)
			if err != nil {
				return nil, nil, err
			}
			meta, _ := json.Marshal(map[string]any{"check": ch, "hint": hintOf(cred)})
			now := time.Now().Unix()
			old, oldErr := a.integration(p.ID)
			if _, err := a.db.Exec(`INSERT INTO integrations(id, kind, name, secret_enc, meta, created_at, updated_at) VALUES(?,?,?,?,?,?,?)
				ON CONFLICT(id) DO UPDATE SET kind=excluded.kind, name=excluded.name, secret_enc=excluded.secret_enc, meta=excluded.meta, updated_at=excluded.updated_at`,
				p.ID, p.Kind, firstNonEmpty(p.Name, p.ID), enc, string(meta), now, now); err != nil {
				return nil, nil, err
			}
			var undo *change.UndoRecipe
			if oldErr == nil {
				u, _ := json.Marshal(integrationParams{ID: old.ID, Kind: old.Kind, Name: old.Name, Sealed: base64.StdEncoding.EncodeToString(old.secretEnc)})
				undo = &change.UndoRecipe{Kind: "integration.set", Params: u}
			} else {
				u, _ := json.Marshal(map[string]string{"id": p.ID})
				undo = &change.UndoRecipe{Kind: "integration.delete", Params: u}
			}
			return map[string]any{"id": p.ID, "check": ch}, undo, nil
		},
	})
	change.Register(change.Kind{
		Name: "integration.delete", Perm: "settings.manage", Scope: "settings:write", Title: "kind.integration.delete",
		Params: change.ParamSchema{Props: map[string]change.Prop{"id": {Type: "string"}}, Required: []string{"id"}},
		CLI:    "berth dns account rm {{.id}}",
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			a := svc(env)
			var p struct{ ID string }
			json.Unmarshal(raw, &p)
			in, err := a.integration(p.ID)
			if err != nil {
				return change.Plan{}, err
			}
			if used := a.integrationUsers(p.ID); len(used) > 0 {
				return change.Plan{}, errs.New(409, "dns.account_in_use", "users", strings.Join(used, ", "))
			}
			return change.Plan{Summary: "plan.integration.delete", SummaryP: map[string]any{"id": in.ID}, Risk: change.RiskMedium,
				Affected: []change.Object{{Type: "integration", ID: in.ID, Name: in.Name}}, Undoable: true}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			a := svc(env)
			var p struct{ ID string }
			json.Unmarshal(raw, &p)
			in, err := a.integration(p.ID)
			if err != nil {
				return nil, nil, err
			}
			if used := a.integrationUsers(p.ID); len(used) > 0 {
				return nil, nil, errs.New(409, "dns.account_in_use", "users", strings.Join(used, ", "))
			}
			if _, err := a.db.Exec(`DELETE FROM integrations WHERE id=?`, p.ID); err != nil {
				return nil, nil, err
			}
			u, _ := json.Marshal(integrationParams{ID: in.ID, Kind: in.Kind, Name: in.Name, Sealed: base64.StdEncoding.EncodeToString(in.secretEnc)})
			return nil, &change.UndoRecipe{Kind: "integration.set", Params: u}, nil
		},
	})
}

func (a *App) handleIntegrations(w http.ResponseWriter, r *http.Request) {
	list, err := a.integrations()
	if err != nil {
		a.fail(w, r, err)
		return
	}
	for i := range list {
		list[i].UsedBy = a.integrationUsers(list[i].ID)
	}
	writeJSON(w, 200, map[string]any{"items": list, "kinds": dnsKinds, "caps": dnsCaps})
}

func (a *App) handleZones(w http.ResponseWriter, r *http.Request) {
	p, err := a.provider(r.PathValue("id"))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	zones, err := p.Zones(r.Context())
	if err != nil {
		a.fail(w, r, dnsErr(err))
		return
	}
	writeJSON(w, 200, map[string]any{"items": zones})
}

func (a *App) handleRecords(w http.ResponseWriter, r *http.Request) {
	p, err := a.provider(r.PathValue("id"))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	recs, err := p.Records(r.Context(), r.PathValue("zone"))
	if err != nil {
		a.fail(w, r, dnsErr(err))
		return
	}
	writeJSON(w, 200, map[string]any{"items": recs})
}
