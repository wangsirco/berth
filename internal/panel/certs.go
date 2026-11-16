package panel

// Certificate center (方案 §5.2.4). Certificates Caddy obtains by ACME stay in Caddy's
// storage and are listed read-only. Self-signed, local-CA and imported certificates
// are kept here (key sealed with the master key), pushed to the hosts whose sites use
// them, and renewed by the panel 30 days before they expire.

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/wangsirco/berth/internal/agent/caddy"
	"github.com/wangsirco/berth/internal/change"
	"github.com/wangsirco/berth/internal/errs"
	"github.com/wangsirco/berth/internal/panel/caddycfg"
	"github.com/wangsirco/berth/internal/panel/certkit"
)

const renewBefore = 30 * 24 * time.Hour

type certRow struct {
	ID        int64           `json:"id"`
	Name      string          `json:"name"`
	Source    string          `json:"source"` // self | internal | import
	Info      certkit.Info    `json:"info"`
	Fields    *certkit.Fields `json:"fields,omitempty"`
	AutoRenew bool            `json:"auto_renew"`
	History   []certVersion   `json:"history"`
	CreatedAt int64           `json:"created_at"`
	UpdatedAt int64           `json:"updated_at"`
	UsedBy    []string        `json:"used_by,omitempty"`

	certPEM string
	keyEnc  []byte
}

// certVersion is an earlier certificate kept for switching back.
type certVersion struct {
	Fingerprint string    `json:"fingerprint"`
	NotAfter    time.Time `json:"not_after"`
	ReplacedAt  int64     `json:"replaced_at"`
	CertPEM     string    `json:"cert_pem"`
	KeySealed   string    `json:"key_sealed"` // base64, sealed with the master key
}

const maxCertHistory = 5

// SetLocalSigner lets the certificate center sign with the panel's local CA.
func (a *App) SetLocalSigner(s certkit.Signer) { a.localSigner = &s }

func (a *App) certs() ([]certRow, error) {
	rows, err := a.db.Query(`SELECT id, name, source, info, fields, auto_renew, history, cert_pem, key_enc, created_at, updated_at FROM certs ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []certRow{}
	for rows.Next() {
		c, err := scanCert(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func scanCert(row interface{ Scan(...any) error }) (certRow, error) {
	var c certRow
	var info, fields, hist string
	var ar int
	if err := row.Scan(&c.ID, &c.Name, &c.Source, &info, &fields, &ar, &hist, &c.certPEM, &c.keyEnc, &c.CreatedAt, &c.UpdatedAt); err != nil {
		return c, err
	}
	c.AutoRenew = ar == 1
	json.Unmarshal([]byte(info), &c.Info)
	if fields != "" && fields != "null" {
		c.Fields = &certkit.Fields{}
		json.Unmarshal([]byte(fields), c.Fields)
	}
	json.Unmarshal([]byte(hist), &c.History)
	if c.History == nil {
		c.History = []certVersion{}
	}
	return c, nil
}

func (a *App) cert(id int64) (certRow, error) {
	c, err := scanCert(a.db.QueryRow(`SELECT id, name, source, info, fields, auto_renew, history, cert_pem, key_enc, created_at, updated_at FROM certs WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return c, errs.New(404, "cert.not_found", "id", id)
	}
	return c, err
}

// certUsers lists "host/domain" of sites using a certificate.
func (a *App) certUsers(id int64) []string {
	rows, err := a.db.Query(`SELECT host, primary_domain, tls FROM sites`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var host, dom, tls string
		rows.Scan(&host, &dom, &tls)
		var t caddycfg.TLS
		json.Unmarshal([]byte(tls), &t)
		if t.CertID == id {
			out = append(out, host+"/"+dom)
		}
	}
	return out
}

func certProblem(err error) error {
	var p *certkit.Problem
	if errors.As(err, &p) {
		return errs.New(400, p.Code)
	}
	if err != nil && strings.Contains(err.Error(), "local CA only signs") {
		return errs.New(400, "cert.not_local")
	}
	return err
}

// issue makes a bundle for a self/internal certificate from its fields.
func (a *App) issue(source string, f certkit.Fields) (certkit.Bundle, error) {
	switch source {
	case "self":
		b, err := certkit.SelfSigned(f)
		return b, certProblem(err)
	case "internal":
		if a.localSigner == nil {
			return certkit.Bundle{}, errs.New(500, "cert.no_local_ca")
		}
		b, err := a.localSigner.Issue(f)
		return b, certProblem(err)
	}
	return certkit.Bundle{}, errs.New(400, "params.enum", "name", "source", "allowed", "self, internal, import")
}

// checkLocalNames is the preview-time check for the local CA (no key is made).
func (a *App) checkLocalNames(f certkit.Fields) error {
	if a.localSigner == nil || a.localSigner.Allow == nil {
		return nil
	}
	var ips []net.IP
	for _, s := range f.IPs {
		ips = append(ips, net.ParseIP(strings.TrimSpace(s)))
	}
	if err := a.localSigner.Allow(f.DNS, ips); err != nil {
		return errs.New(400, "cert.not_local")
	}
	return nil
}

func infoOf(b certkit.Bundle) certkit.Info {
	chain, err := certkit.Read(b.CertPEM)
	if err != nil {
		return certkit.Info{}
	}
	return certkit.Describe(chain)
}

func (a *App) storeCert(c *certRow, b certkit.Bundle) error {
	enc, err := a.seal(b.KeyPEM)
	if err != nil {
		return err
	}
	c.certPEM, c.keyEnc, c.Info = string(b.CertPEM), enc, infoOf(b)
	info, _ := json.Marshal(c.Info)
	fields, _ := json.Marshal(c.Fields)
	hist, _ := json.Marshal(c.History)
	now := time.Now().Unix()
	ar := 0
	if c.AutoRenew {
		ar = 1
	}
	if c.ID == 0 {
		res, err := a.db.Exec(`INSERT INTO certs(name, source, info, fields, auto_renew, history, cert_pem, key_enc, not_after, created_at, updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
			c.Name, c.Source, string(info), string(fields), ar, string(hist), c.certPEM, enc, c.Info.NotAfter.Unix(), now, now)
		if err != nil {
			return err
		}
		c.ID, _ = res.LastInsertId()
		c.CreatedAt, c.UpdatedAt = now, now
		return nil
	}
	res, err := a.db.Exec(`UPDATE certs SET name=?, info=?, fields=?, auto_renew=?, history=?, cert_pem=?, key_enc=?, not_after=?, updated_at=? WHERE id=?`,
		c.Name, string(info), string(fields), ar, string(hist), c.certPEM, enc, c.Info.NotAfter.Unix(), now, c.ID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 { // restoring a deleted certificate keeps its id
		_, err = a.db.Exec(`INSERT INTO certs(id, name, source, info, fields, auto_renew, history, cert_pem, key_enc, not_after, created_at, updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`,
			c.ID, c.Name, c.Source, string(info), string(fields), ar, string(hist), c.certPEM, enc, c.Info.NotAfter.Unix(), now, now)
	}
	return err
}

// pushVersion moves the current certificate into the history.
func (c *certRow) pushVersion() {
	v := certVersion{Fingerprint: c.Info.Fingerprint, NotAfter: c.Info.NotAfter, ReplacedAt: time.Now().Unix(),
		CertPEM: c.certPEM, KeySealed: base64.StdEncoding.EncodeToString(c.keyEnc)}
	c.History = append([]certVersion{v}, c.History...)
	if len(c.History) > maxCertHistory {
		c.History = c.History[:maxCertHistory]
	}
}

// ---- deployment to hosts ----

// certFilesFor: every stored certificate, by its path inside the Caddy container.
func certFilesFor(a *App, host string) map[int64]caddycfg.CertFiles {
	rows, err := a.db.Query(`SELECT id, info FROM certs`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := map[int64]caddycfg.CertFiles{}
	for rows.Next() {
		var id int64
		var info string
		rows.Scan(&id, &info)
		v := certVersionOf(info)
		c, k := caddy.CertPaths(strconv.FormatInt(id, 10), v)
		out[id] = caddycfg.CertFiles{Cert: c, Key: k, Version: v}
	}
	return out
}

// certVersionOf: the first 12 hex digits of the certificate's fingerprint.
func certVersionOf(infoJSON string) string {
	var in certkit.Info
	json.Unmarshal([]byte(infoJSON), &in)
	if len(in.Fingerprint) >= 12 {
		return in.Fingerprint[:12]
	}
	return "000000000000"
}

// pushCerts writes the certificates used by host's sites (or extra) to the host.
func (a *App) pushCerts(ctx context.Context, host string, extra ...Site) error {
	sites, err := a.sites(host)
	if err != nil {
		return err
	}
	ids := map[int64]bool{}
	for _, s := range append(sites, extra...) {
		if s.TLS.CertID > 0 {
			ids[s.TLS.CertID] = true
		}
	}
	for id := range ids {
		c, err := a.cert(id)
		if err != nil {
			return err
		}
		key, err := a.open(c.keyEnc)
		if err != nil {
			return err
		}
		info, _ := json.Marshal(c.Info)
		if err := a.call(ctx, host, "PUT", "/v1/edge/certs/"+strconv.FormatInt(id, 10), map[string]string{"version": certVersionOf(string(info)), "cert": c.certPEM, "key": string(key)}, nil); err != nil {
			return err
		}
	}
	return nil
}

// redeployCert pushes a changed certificate to every host using it and reloads them.
func (a *App) redeployCert(ctx context.Context, id int64) error {
	hosts := map[string]bool{}
	for _, u := range a.certUsers(id) {
		hosts[strings.SplitN(u, "/", 2)[0]] = true
	}
	var firstErr error
	for h := range hosts {
		err := a.pushCerts(ctx, h)
		if err == nil {
			err = a.applyEdge(change.Env{Ctx: ctx, Host: h})
		}
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// ---- renewal ----

func (a *App) renewLoop(ctx context.Context) {
	t := time.NewTicker(6 * time.Hour)
	defer t.Stop()
	for {
		a.renewDue(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (a *App) renewDue(ctx context.Context) {
	rows, err := a.db.Query(`SELECT id FROM certs WHERE auto_renew=1 AND source IN ('self','internal') AND not_after < ?`, time.Now().Add(renewBefore).Unix())
	if err != nil {
		return
	}
	var ids []int64
	for rows.Next() {
		var id int64
		rows.Scan(&id)
		ids = append(ids, id)
	}
	rows.Close()
	for _, id := range ids {
		c, err := a.cert(id)
		if err != nil || c.Fields == nil {
			continue
		}
		b, err := a.issue(c.Source, *c.Fields)
		if err == nil {
			c.pushVersion()
			err = a.storeCert(&c, b)
		}
		if err == nil {
			err = a.redeployCert(ctx, id)
		}
		status := "done"
		if err != nil {
			status = "failed"
			log.Printf("panel: renew certificate %d: %v", id, err)
		}
		_, _ = a.pipe.Record(change.Actor{Type: "system", ID: "renew", Name: "berth", Via: "api"}, "local", "cert.renew",
			map[string]any{"id": id, "name": c.Name, "status": status}, change.Plan{
				Summary: "plan.cert.renew", SummaryP: map[string]any{"name": c.Name, "until": c.Info.NotAfter.Format("2006-01-02")},
				Risk: change.RiskLow, Affected: []change.Object{certObj(c)}})
	}
}

func certObj(c certRow) change.Object {
	return change.Object{Type: "cert", ID: strconv.FormatInt(c.ID, 10), Name: c.Name}
}

// ---- kinds ----

type certParams struct {
	ID        int64           `json:"id,omitempty"`
	Name      string          `json:"name"`
	Source    string          `json:"source"` // self | internal | import | restore (undo only)
	Fields    *certkit.Fields `json:"fields,omitempty"`
	AutoRenew *bool           `json:"auto_renew,omitempty"`
	CertPEM   string          `json:"cert_pem,omitempty"`
	KeyPEM    string          `json:"key_pem,omitempty"`
	PFX       string          `json:"pfx,omitempty"` // base64
	Password  string          `json:"password,omitempty"`
	// restore (undo of delete): the sealed key and the old history
	KeySealed  string        `json:"key_sealed,omitempty"`
	OrigSource string        `json:"orig_source,omitempty"`
	History    []certVersion `json:"history,omitempty"`
}

func (p *certParams) bundle(a *App) (certkit.Bundle, certkit.Info, error) {
	switch p.Source {
	case "import":
		var pfx []byte
		if p.PFX != "" {
			var err error
			if pfx, err = base64.StdEncoding.DecodeString(p.PFX); err != nil {
				return certkit.Bundle{}, certkit.Info{}, errs.New(400, "cert.bad_pfx")
			}
		}
		b, info, err := certkit.Parse([]byte(p.CertPEM), []byte(p.KeyPEM), pfx, p.Password)
		return b, info, certProblem(err)
	case "restore":
		enc, err := base64.StdEncoding.DecodeString(p.KeySealed)
		if err != nil {
			return certkit.Bundle{}, certkit.Info{}, errs.New(400, "params.invalid_json")
		}
		key, err := a.open(enc)
		if err != nil {
			return certkit.Bundle{}, certkit.Info{}, err
		}
		b := certkit.Bundle{CertPEM: []byte(p.CertPEM), KeyPEM: key}
		return b, infoOf(b), nil
	}
	return certkit.Bundle{}, certkit.Info{}, errs.New(400, "params.enum", "name", "source", "allowed", "self, internal, import")
}

func init() {
	certFilesHook = certFilesFor
	routeHooks = append(routeHooks, func(a *App, mux *http.ServeMux) {
		mux.HandleFunc("GET /api/v1/certs", a.authed("sites.view", a.handleCerts))
		mux.HandleFunc("GET /api/v1/certs/{id}", a.authed("sites.view", a.handleCert))
		mux.HandleFunc("POST /api/v1/certs/{id}/export", a.authed("certs.manage", a.handleCertExport))
	})

	props := map[string]change.Prop{
		"id": {Type: "integer"}, "name": {Type: "string"},
		"source":     {Type: "string", Enum: []string{"self", "internal", "import"}},
		"fields":     {Type: "object", Desc: "{cn, o, ou, c, dns[], ips[], days, key_type: ec256|ec384|rsa2048|rsa4096, usage[]: server|client, is_ca}"},
		"auto_renew": {Type: "boolean"},
		"cert_pem":   {Type: "string"}, "key_pem": {Type: "string"}, "pfx": {Type: "string", Desc: "base64 PKCS#12"}, "password": {Type: "string"},
	}

	change.Register(change.Kind{
		Name: "cert.create", Perm: "certs.manage", Scope: "sites:write", Title: "kind.cert.create",
		Params: change.ParamSchema{Props: props, Required: []string{"source"}}, Redact: []string{"key_pem", "pfx", "password", "key_sealed"},
		CLI: "berth cert create --source {{.source}}",
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			a := svc(env)
			var p certParams
			json.Unmarshal(raw, &p)
			var summary map[string]any
			switch p.Source {
			case "self", "internal":
				if p.Fields == nil {
					return change.Plan{}, errs.New(400, "cert.no_name")
				}
				f := *p.Fields
				// validate the fields without making a key
				if _, err := certkit.SelfSigned(certkit.Fields{CN: f.CN, DNS: f.DNS, IPs: f.IPs, Days: f.Days, KeyType: "ec256", C: f.C}); err != nil {
					return change.Plan{}, certProblem(err)
				}
				if p.Source == "internal" {
					if err := a.checkLocalNames(f); err != nil {
						return change.Plan{}, err
					}
				}
				summary = map[string]any{"name": firstNonEmpty(p.Name, f.CN, strings.Join(f.DNS, ", ")), "source": p.Source}
			case "import", "restore":
				_, info, err := p.bundle(a)
				if err != nil {
					return change.Plan{}, err
				}
				summary = map[string]any{"name": firstNonEmpty(p.Name, strings.Join(info.Domains, ", ")), "source": p.Source,
					"until": info.NotAfter.Format("2006-01-02")}
			default:
				return change.Plan{}, errs.New(400, "params.enum", "name", "source", "allowed", "self, internal, import")
			}
			return change.Plan{Summary: "plan.cert.create", SummaryP: summary, Risk: change.RiskLow, Undoable: true}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			a := svc(env)
			var p certParams
			json.Unmarshal(raw, &p)
			c := certRow{ID: p.ID, Name: p.Name, Source: p.Source, History: p.History}
			var b certkit.Bundle
			var err error
			switch p.Source {
			case "self", "internal":
				if p.Fields == nil {
					return nil, nil, errs.New(400, "cert.no_name")
				}
				b, err = a.issue(p.Source, *p.Fields)
				c.Fields = p.Fields
				c.AutoRenew = p.AutoRenew == nil || *p.AutoRenew
			default:
				b, _, err = p.bundle(a)
				if p.Source == "restore" {
					c.Source = firstNonEmpty(p.OrigSource, "import")
					c.Fields = p.Fields
					c.AutoRenew = p.AutoRenew != nil && *p.AutoRenew
				}
			}
			if err != nil {
				return nil, nil, err
			}
			if c.Name == "" {
				c.Name = strings.Join(infoOf(b).Domains, ", ")
			}
			if err := a.storeCert(&c, b); err != nil {
				return nil, nil, err
			}
			u, _ := json.Marshal(map[string]int64{"id": c.ID})
			return map[string]any{"id": c.ID, "info": c.Info}, &change.UndoRecipe{Kind: "cert.delete", Params: u}, nil
		},
	})

	change.Register(change.Kind{
		Name: "cert.reissue", Perm: "certs.manage", Scope: "sites:write", Title: "kind.cert.reissue",
		Params: change.ParamSchema{Props: props, Required: []string{"id"}},
		CLI:    "berth cert reissue {{.id}}",
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			a := svc(env)
			var p certParams
			json.Unmarshal(raw, &p)
			c, err := a.cert(p.ID)
			if err != nil {
				return change.Plan{}, err
			}
			if c.Source != "self" && c.Source != "internal" {
				return change.Plan{}, errs.New(400, "cert.not_reissuable")
			}
			f := c.Fields
			if p.Fields != nil {
				f = p.Fields
			}
			if c.Source == "internal" {
				if err := a.checkLocalNames(*f); err != nil {
					return change.Plan{}, err
				}
			}
			plan := change.Plan{Summary: "plan.cert.reissue", SummaryP: map[string]any{"name": c.Name}, Risk: change.RiskMedium,
				Affected: []change.Object{certObj(c)}, Undoable: true}
			for _, u := range a.certUsers(c.ID) {
				h, d, _ := strings.Cut(u, "/")
				plan.Affected = append(plan.Affected, change.Object{Type: "site", ID: d, Name: d, Host: h})
			}
			return plan, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			a := svc(env)
			var p certParams
			json.Unmarshal(raw, &p)
			c, err := a.cert(p.ID)
			if err != nil {
				return nil, nil, err
			}
			if p.Fields != nil {
				c.Fields = p.Fields
			}
			if p.Name != "" {
				c.Name = p.Name
			}
			if p.AutoRenew != nil {
				c.AutoRenew = *p.AutoRenew
			}
			b, err := a.issue(c.Source, *c.Fields)
			if err != nil {
				return nil, nil, err
			}
			oldFP := c.Info.Fingerprint
			c.pushVersion()
			if err := a.storeCert(&c, b); err != nil {
				return nil, nil, err
			}
			if err := a.redeployCert(env.Ctx, c.ID); err != nil {
				env.Log("redeploy: %v", err)
			}
			u, _ := json.Marshal(map[string]any{"id": c.ID, "fingerprint": oldFP})
			return map[string]any{"id": c.ID, "info": c.Info}, &change.UndoRecipe{Kind: "cert.rollback", Params: u}, nil
		},
	})

	change.Register(change.Kind{
		Name: "cert.rollback", Perm: "certs.manage", Scope: "sites:write", Title: "kind.cert.rollback",
		Params: change.ParamSchema{Props: map[string]change.Prop{"id": {Type: "integer"}, "fingerprint": {Type: "string"}}, Required: []string{"id", "fingerprint"}},
		CLI:    "berth cert rollback {{.id}} {{.fingerprint}}",
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			a := svc(env)
			var p struct {
				ID          int64
				Fingerprint string
			}
			json.Unmarshal(raw, &p)
			c, err := a.cert(p.ID)
			if err != nil {
				return change.Plan{}, err
			}
			for _, v := range c.History {
				if v.Fingerprint == p.Fingerprint {
					return change.Plan{Summary: "plan.cert.rollback", SummaryP: map[string]any{"name": c.Name, "until": v.NotAfter.Format("2006-01-02")},
						Risk: change.RiskMedium, Affected: []change.Object{certObj(c)}, Undoable: true}, nil
				}
			}
			return change.Plan{}, errs.New(404, "cert.version_not_found")
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			a := svc(env)
			var p struct {
				ID          int64
				Fingerprint string
			}
			json.Unmarshal(raw, &p)
			c, err := a.cert(p.ID)
			if err != nil {
				return nil, nil, err
			}
			idx := -1
			for i, v := range c.History {
				if v.Fingerprint == p.Fingerprint {
					idx = i
				}
			}
			if idx < 0 {
				return nil, nil, errs.New(404, "cert.version_not_found")
			}
			v := c.History[idx]
			enc, _ := base64.StdEncoding.DecodeString(v.KeySealed)
			key, err := a.open(enc)
			if err != nil {
				return nil, nil, err
			}
			oldFP := c.Info.Fingerprint
			c.History = append(c.History[:idx], c.History[idx+1:]...)
			c.pushVersion()
			if err := a.storeCert(&c, certkit.Bundle{CertPEM: []byte(v.CertPEM), KeyPEM: key}); err != nil {
				return nil, nil, err
			}
			if err := a.redeployCert(env.Ctx, c.ID); err != nil {
				env.Log("redeploy: %v", err)
			}
			u, _ := json.Marshal(map[string]any{"id": c.ID, "fingerprint": oldFP})
			return map[string]any{"id": c.ID}, &change.UndoRecipe{Kind: "cert.rollback", Params: u}, nil
		},
	})

	change.Register(change.Kind{
		Name: "cert.delete", Perm: "certs.manage", Scope: "sites:write", Title: "kind.cert.delete",
		Params: change.ParamSchema{Props: map[string]change.Prop{"id": {Type: "integer"}}, Required: []string{"id"}},
		CLI:    "berth cert rm {{.id}}",
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			a := svc(env)
			var p struct{ ID int64 }
			json.Unmarshal(raw, &p)
			c, err := a.cert(p.ID)
			if err != nil {
				return change.Plan{}, err
			}
			if used := a.certUsers(c.ID); len(used) > 0 {
				return change.Plan{}, errs.New(409, "cert.in_use", "sites", strings.Join(used, ", "))
			}
			return change.Plan{Summary: "plan.cert.delete", SummaryP: map[string]any{"name": c.Name}, Risk: change.RiskMedium,
				Affected: []change.Object{certObj(c)}, Undoable: true}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			a := svc(env)
			var p struct{ ID int64 }
			json.Unmarshal(raw, &p)
			c, err := a.cert(p.ID)
			if err != nil {
				return nil, nil, err
			}
			if used := a.certUsers(c.ID); len(used) > 0 {
				return nil, nil, errs.New(409, "cert.in_use", "sites", strings.Join(used, ", "))
			}
			if _, err := a.db.Exec(`DELETE FROM certs WHERE id=?`, c.ID); err != nil {
				return nil, nil, err
			}
			ar := c.AutoRenew
			u, _ := json.Marshal(map[string]any{"id": c.ID, "name": c.Name, "source": "restore", "orig_source": c.Source, "fields": c.Fields,
				"auto_renew": ar, "cert_pem": c.certPEM, "key_sealed": base64.StdEncoding.EncodeToString(c.keyEnc), "history": c.History})
			return nil, &change.UndoRecipe{Kind: "cert.create", Params: u}, nil
		},
	})
}

// ---- API ----

type acmeView struct {
	Host        string    `json:"host"`
	Domain      string    `json:"domain"`
	Issuer      string    `json:"issuer"`
	Names       []string  `json:"names"`
	NotBefore   time.Time `json:"not_before"`
	NotAfter    time.Time `json:"not_after"`
	Fingerprint string    `json:"fingerprint"`
}

func (a *App) handleCerts(w http.ResponseWriter, r *http.Request) {
	list, err := a.certs()
	if err != nil {
		a.fail(w, r, err)
		return
	}
	for i := range list {
		list[i].UsedBy = a.certUsers(list[i].ID)
		for j := range list[i].History {
			list[i].History[j].CertPEM, list[i].History[j].KeySealed = "", ""
		}
	}
	acme := []acmeView{}
	for _, h := range a.connectedHosts() {
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		var got []acmeView
		if a.call(ctx, h, "GET", "/v1/edge/acme-certs", nil, &got) == nil {
			for _, c := range got {
				c.Host = h
				acme = append(acme, c)
			}
		}
		cancel()
	}
	sort.Slice(acme, func(i, j int) bool { return acme[i].Domain < acme[j].Domain })
	writeJSON(w, 200, map[string]any{"items": list, "acme": acme, "renew_days": int(renewBefore.Hours() / 24)})
}

func (a *App) handleCert(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	c, err := a.cert(id)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	c.UsedBy = a.certUsers(id)
	for j := range c.History {
		c.History[j].KeySealed = ""
	}
	writeJSON(w, 200, map[string]any{"cert": c, "cert_pem": c.certPEM})
}

// POST /api/v1/certs/{id}/export {format: pem|pfx, password}: needs a fresh sign-in
// confirmation, and is recorded.
func (a *App) handleCertExport(w http.ResponseWriter, r *http.Request) {
	if err := a.requireReauth(r); err != nil {
		a.fail(w, r, err)
		return
	}
	var in struct {
		Format   string `json:"format"`
		Password string `json:"password"`
	}
	if err := readJSON(r, &in); err != nil {
		a.fail(w, r, err)
		return
	}
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	c, err := a.cert(id)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	key, err := a.open(c.keyEnc)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	_, _ = a.pipe.Record(currentActor(r), "local", "cert.export", map[string]any{"id": id, "name": c.Name, "format": in.Format}, change.Plan{
		Summary: "plan.cert.export", SummaryP: map[string]any{"name": c.Name}, Risk: change.RiskHigh, Affected: []change.Object{certObj(c)}})
	switch in.Format {
	case "pfx":
		b, err := certkit.PFX(certkit.Bundle{CertPEM: []byte(c.certPEM), KeyPEM: key}, in.Password)
		if err != nil {
			a.fail(w, r, certProblem(err))
			return
		}
		writeJSON(w, 200, map[string]string{"pfx": base64.StdEncoding.EncodeToString(b), "filename": safeFile(c.Name) + ".pfx"})
	default:
		writeJSON(w, 200, map[string]string{"cert_pem": c.certPEM, "key_pem": string(key), "filename": safeFile(c.Name)})
	}
}

func safeFile(s string) string {
	s = strings.Map(func(r rune) rune {
		if r == '.' || r == '-' || r == '_' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			return r
		}
		return '_'
	}, s)
	if s == "" {
		return "certificate"
	}
	return s
}
