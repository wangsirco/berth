package panel

// Server migration (R6): where a pack comes from, the pages' endpoints and the change kinds.
//
// Sources:
//   - host: another host of this panel; nothing is sealed or stored, the source's stream goes
//     straight into the target.
//   - code: a migration code made on the old server's panel. The new panel pulls from it over
//     HTTPS, pinned to the old panel's own CA, and nothing is stored in between.
//   - url:  a pack someone put on object storage or a web server; restored while downloading.
//   - file: a pack on the panel's machine (uploaded, or copied there). It takes its own room.

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/wangsirco/berth/internal/change"
	"github.com/wangsirco/berth/internal/errs"
	"github.com/wangsirco/berth/internal/sign"
	"github.com/wangsirco/berth/internal/snapshot"
)

type migSource struct {
	Type string `json:"type"` // host | code | url | file
	From string `json:"from,omitempty"`
	URL  string `json:"url,omitempty"`
	Path string `json:"path,omitempty"`
}

type migParams struct {
	Source   migSource `json:"source"`
	Code     string    `json:"code,omitempty"`
	Password string    `json:"password,omitempty"`
	Items    []string  `json:"items,omitempty"`
}

// ---- migration codes ----

type migCode struct {
	Base, Token, FP string
}

const migCodePrefix = "berth-migrate:"

func parseMigCode(s string) (migCode, error) {
	s = strings.TrimSpace(s)
	rest, ok := strings.CutPrefix(s, migCodePrefix)
	if !ok {
		return migCode{}, errs.New(400, "migrate.bad_code")
	}
	base, frag, ok := strings.Cut(rest, "#")
	tok, fp, ok2 := strings.Cut(frag, ".")
	u, err := url.Parse(base)
	if !ok || !ok2 || err != nil || u.Scheme != "https" || u.Host == "" || len(tok) < 32 || len(fp) != 64 {
		return migCode{}, errs.New(400, "migrate.bad_code")
	}
	return migCode{Base: strings.TrimRight(base, "/"), Token: tok, FP: strings.ToLower(fp)}, nil
}

// migClient trusts a server whose chain carries the CA with the given fingerprint (a Berth
// panel's own CA), or one with a valid public certificate for the name in the code.
func migClient(base, fp string) *http.Client {
	u, _ := url.Parse(base)
	name := u.Hostname()
	return &http.Client{Transport: &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true, VerifyConnection: func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 {
				return errs.New(502, "migrate.cert_mismatch")
			}
			leaf := cs.PeerCertificates[0]
			for _, c := range cs.PeerCertificates[1:] {
				h := sha256.Sum256(c.Raw)
				if hex.EncodeToString(h[:]) == fp && leaf.CheckSignatureFrom(c) == nil {
					return nil
				}
			}
			if net.ParseIP(name) == nil { // a public certificate, for this exact name
				pool := x509.NewCertPool()
				for _, c := range cs.PeerCertificates[1:] {
					pool.AddCert(c)
				}
				if _, err := leaf.Verify(x509.VerifyOptions{DNSName: name, Intermediates: pool}); err == nil {
					return nil
				}
			}
			return errs.New(502, "migrate.cert_mismatch")
		}},
	}}
}

type migCodeRec struct {
	Hash    string `json:"hash"`
	Host    string `json:"host"`
	Expires int64  `json:"expires"`
	By      string `json:"by"`
}

func (a *App) migCodeRec() (migCodeRec, bool) {
	var c migCodeRec
	if json.Unmarshal([]byte(a.setting("migrate.code")), &c) != nil || c.Hash == "" || time.Now().Unix() > c.Expires {
		return c, false
	}
	return c, true
}

// migPullHost checks a pull request's token and says which host it may read.
func (a *App) migPullHost(r *http.Request) (string, error) {
	tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	c, live := a.migCodeRec()
	h := sha256.Sum256([]byte(tok))
	if !ok || !live || subtle.ConstantTimeCompare([]byte(hex.EncodeToString(h[:])), []byte(c.Hash)) != 1 {
		return "", errs.New(401, "migrate.bad_code")
	}
	return c.Host, nil
}

// ---- opening a source ----

// unseal decrypts a sealed pack as it is read.
func unseal(body io.ReadCloser, pass string) io.ReadCloser {
	pr, pw := io.Pipe()
	go func() {
		err := snapshot.Open(pw, body, pass)
		body.Close()
		if errors.Is(err, snapshot.ErrPassword) {
			err = errs.New(400, "migrate.bad_password")
		}
		pw.CloseWithError(err)
	}()
	return pr
}

func (a *App) migOpen(ctx context.Context, p migParams, log func(string, ...any)) (io.ReadCloser, error) {
	switch p.Source.Type {
	case "host":
		if _, err := a.agent(p.Source.From); err != nil {
			return nil, err
		}
		pr, pw := io.Pipe()
		go func() { pw.CloseWithError(a.migWritePack(ctx, p.Source.From, p.Items, pw, log)) }()
		return pr, nil
	case "code":
		c, err := parseMigCode(p.Code)
		if err != nil {
			return nil, err
		}
		b, _ := json.Marshal(map[string]any{"items": p.Items})
		req, _ := http.NewRequestWithContext(ctx, "POST", c.Base+"/migrate/pull/pack", bytes.NewReader(b))
		req.Header.Set("Authorization", "Bearer "+c.Token)
		req.Header.Set("Content-Type", "application/json")
		resp, err := migClient(c.Base, c.FP).Do(req)
		if err != nil {
			return nil, errs.New(502, "migrate.unreachable", "detail", err.Error())
		}
		if resp.StatusCode != 200 {
			resp.Body.Close()
			return nil, errs.New(502, "migrate.source_refused", "status", resp.StatusCode)
		}
		return unseal(resp.Body, c.Token), nil
	case "url":
		u, err := url.Parse(p.Source.URL)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") {
			return nil, errs.New(400, "migrate.bad_url")
		}
		req, _ := http.NewRequestWithContext(ctx, "GET", u.String(), nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return nil, errs.New(502, "migrate.unreachable", "detail", err.Error())
		}
		if resp.StatusCode != 200 {
			resp.Body.Close()
			return nil, errs.New(502, "migrate.source_refused", "status", resp.StatusCode)
		}
		return unseal(resp.Body, p.Password), nil
	case "file":
		path := filepath.Clean(p.Source.Path)
		if !filepath.IsAbs(path) {
			return nil, errs.New(400, "migrate.bad_path", "path", path)
		}
		fi, err := os.Stat(path)
		if err != nil || !fi.Mode().IsRegular() {
			return nil, errs.New(404, "files.not_found", "path", path)
		}
		f, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		return unseal(f, p.Password), nil
	}
	return nil, errs.New(400, "migrate.bad_source")
}

// migManifestOf reads what a source holds without moving any data.
func (a *App) migManifestOf(ctx context.Context, p migParams) (migManifest, error) {
	switch p.Source.Type {
	case "host":
		return a.migInventory(ctx, p.Source.From)
	case "code":
		c, err := parseMigCode(p.Code)
		if err != nil {
			return migManifest{}, err
		}
		req, _ := http.NewRequestWithContext(ctx, "GET", c.Base+"/migrate/pull/manifest", nil)
		req.Header.Set("Authorization", "Bearer "+c.Token)
		resp, err := migClient(c.Base, c.FP).Do(req)
		if err != nil {
			return migManifest{}, errs.New(502, "migrate.unreachable", "detail", err.Error())
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			return migManifest{}, errs.New(502, "migrate.source_refused", "status", resp.StatusCode)
		}
		var m migManifest
		err = json.NewDecoder(io.LimitReader(resp.Body, 32<<20)).Decode(&m)
		return m, err
	}
	cctx, cancel := context.WithCancel(ctx)
	defer cancel()
	rc, err := a.migOpen(cctx, p, func(string, ...any) {})
	if err != nil {
		return migManifest{}, err
	}
	defer rc.Close()
	tr := tar.NewReader(rc)
	h, err := tr.Next()
	if err != nil {
		var e *errs.E
		if errors.As(err, &e) {
			return migManifest{}, err
		}
		return migManifest{}, errs.New(400, "migrate.bad_pack")
	}
	if h.Name != "manifest.json" {
		return migManifest{}, errs.New(400, "migrate.bad_pack")
	}
	var m migManifest
	if err := json.NewDecoder(io.LimitReader(tr, 32<<20)).Decode(&m); err != nil || m.Format != migFormat {
		return migManifest{}, errs.New(400, "migrate.bad_pack")
	}
	return m, nil
}

// migChosen: the items of a manifest to restore (none listed: the ones ticked by default).
func migChosen(man migManifest, ids []string) []migItem {
	return migSelect(man, ids)
}

func init() {
	routeHooks = append(routeHooks, func(a *App, mux *http.ServeMux) {
		mux.HandleFunc("GET /api/v1/hosts/{host}/migrate/inventory", a.authed("system.manage", func(w http.ResponseWriter, r *http.Request) {
			man, err := a.migInventory(r.Context(), r.PathValue("host"))
			if err != nil {
				a.fail(w, r, err)
				return
			}
			writeJSON(w, 200, map[string]any{"manifest": man, "folders": a.migFolders(r.PathValue("host")), "code": a.migCodeView(r.PathValue("host"))})
		}))
		mux.HandleFunc("POST /api/v1/hosts/{host}/migrate/preflight", a.authed("system.manage", func(w http.ResponseWriter, r *http.Request) {
			var p migParams
			if err := readJSON(r, &p); err != nil {
				a.fail(w, r, err)
				return
			}
			host := r.PathValue("host")
			if p.Source.Type == "host" && p.Source.From == host {
				a.fail(w, r, errs.New(400, "migrate.same_host"))
				return
			}
			man, err := a.migManifestOf(r.Context(), p)
			if err != nil {
				a.fail(w, r, err)
				return
			}
			items := man.Items
			if len(p.Items) > 0 {
				items = migChosen(man, p.Items)
			}
			pf, err := a.migCheckTarget(r.Context(), host, man, items)
			if err != nil {
				a.fail(w, r, err)
				return
			}
			writeJSON(w, 200, pf)
		}))
		// the pack as a file, sealed with a password the person chose. A one-time link, so the
		// browser saves it straight to disk however large it is.
		mux.HandleFunc("POST /api/v1/hosts/{host}/migrate/pack", a.superOnly(func(w http.ResponseWriter, r *http.Request) {
			if err := a.requireReauth(r); err != nil {
				a.fail(w, r, err)
				return
			}
			var in struct {
				Items    []string `json:"items"`
				Password string   `json:"password"`
			}
			if err := readJSON(r, &in); err != nil {
				a.fail(w, r, err)
				return
			}
			if len(in.Password) < 8 {
				a.fail(w, r, errs.New(400, "snapshot.weak_password"))
				return
			}
			host := r.PathValue("host")
			tok := sign.RandomHex(20)
			migDownloads.Store(tok, migDownload{host: host, items: in.Items, pass: in.Password, actor: currentActor(r), until: time.Now().Add(10 * time.Minute)})
			writeJSON(w, 200, map[string]string{"url": "/migrate/download/" + tok})
		}))
		mux.HandleFunc("GET /migrate/download/{tok}", func(w http.ResponseWriter, r *http.Request) {
			v, ok := migDownloads.LoadAndDelete(r.PathValue("tok"))
			d, _ := v.(migDownload)
			if !ok || time.Now().After(d.until) {
				a.fail(w, r, errs.New(404, "migrate.link_expired"))
				return
			}
			a.pipe.Record(d.actor, d.host, "migrate.export", map[string]any{"items": d.items}, change.Plan{Summary: "plan.migrate.export", Risk: change.RiskHigh})
			w.Header().Set("Content-Type", "application/octet-stream")
			w.Header().Set("Content-Disposition", `attachment; filename="berth-`+d.host+`-`+time.Now().Format("20060102-1504")+`.berthpack"`)
			a.migServePack(r.Context(), w, d.host, d.items, d.pass)
		})
		mux.HandleFunc("POST /api/v1/hosts/{host}/migrate/code", a.superOnly(func(w http.ResponseWriter, r *http.Request) {
			if err := a.requireReauth(r); err != nil {
				a.fail(w, r, err)
				return
			}
			host := r.PathValue("host")
			if _, err := a.agent(host); err != nil {
				a.fail(w, r, err)
				return
			}
			tok := sign.RandomHex(24)
			h := sha256.Sum256([]byte(tok))
			rec := migCodeRec{Hash: hex.EncodeToString(h[:]), Host: host, Expires: time.Now().Add(24 * time.Hour).Unix(), By: currentActor(r).Name}
			b, _ := json.Marshal(rec)
			a.setSetting("migrate.code", string(b))
			base := "https://" + r.Host
			if a.caFingerprint == "" {
				a.fail(w, r, errs.New(500, "migrate.no_ca"))
				return
			}
			a.pipe.Record(currentActor(r), host, "migrate.code", map[string]any{}, change.Plan{Summary: "plan.migrate.code", Risk: change.RiskHigh})
			writeJSON(w, 200, map[string]any{"code": migCodePrefix + base + "#" + tok + "." + a.caFingerprint, "expires": rec.Expires})
		}))
		mux.HandleFunc("DELETE /api/v1/hosts/{host}/migrate/code", a.superOnly(func(w http.ResponseWriter, r *http.Request) {
			a.setSetting("migrate.code", "")
			writeJSON(w, 200, map[string]bool{"ok": true})
		}))

		// the other panel, with a migration code
		mux.HandleFunc("GET /migrate/pull/manifest", func(w http.ResponseWriter, r *http.Request) {
			host, err := a.migPullHost(r)
			if err != nil {
				a.fail(w, r, err)
				return
			}
			man, err := a.migInventory(r.Context(), host)
			if err != nil {
				a.fail(w, r, err)
				return
			}
			writeJSON(w, 200, man)
		})
		mux.HandleFunc("POST /migrate/pull/pack", func(w http.ResponseWriter, r *http.Request) {
			host, err := a.migPullHost(r)
			if err != nil {
				a.fail(w, r, err)
				return
			}
			var in struct {
				Items []string `json:"items"`
			}
			json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&in)
			tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			a.pipe.Record(change.Actor{Type: "system", ID: "migrate", Name: "migration code", Via: "api", IP: a.clientIP(r)}, host, "migrate.export",
				map[string]any{"items": in.Items}, change.Plan{Summary: "plan.migrate.export", Risk: change.RiskHigh})
			w.Header().Set("Content-Type", "application/octet-stream")
			a.migServePack(r.Context(), w, host, in.Items, tok)
		})
	})

	srcProps := map[string]change.Prop{
		"source":   {Type: "object", Desc: `{"type":"host","from":"<host>"} | {"type":"code"} | {"type":"url","url":"https://…"} | {"type":"file","path":"/…"}`},
		"code":     {Type: "string", Desc: "migration code from the old server's panel (type code)"},
		"password": {Type: "string", Desc: "the pack's password (types url and file)"},
		"items":    {Type: "array", Desc: "item ids to restore; empty = the defaults"},
	}
	change.Register(change.Kind{
		Name: "migrate.import", Perm: "system.manage", Scope: "system:write", Title: "kind.migrate.import", CLI: "berth migrate import",
		Heavy: true, MemMB: 300, Redact: []string{"password", "code"},
		Params: change.ParamSchema{Props: srcProps, Required: []string{"source"}},
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			a := svc(env)
			var p migParams
			json.Unmarshal(raw, &p)
			if p.Source.Type == "host" && p.Source.From == env.Host {
				return change.Plan{}, errs.New(400, "migrate.same_host")
			}
			man, err := a.migManifestOf(env.Ctx, p)
			if err != nil {
				return change.Plan{}, err
			}
			items := migChosen(man, p.Items)
			pf, err := a.migCheckTarget(env.Ctx, env.Host, man, items)
			if err != nil {
				return change.Plan{}, err
			}
			if !pf.Fits {
				for _, fs := range pf.Filesystems {
					if !fs.OK {
						return change.Plan{}, errs.New(507, "migrate.no_room", "mount", fs.Mount, "need", fmtBytes(fs.Room), "free", fmtBytes(fs.Free))
					}
				}
			}
			cmds := []string{}
			st := map[string]migCheck{}
			for _, c := range pf.Checks {
				st[c.ID] = c
			}
			var total int64
			for _, it := range items {
				line := it.Type + " " + it.Name + "  (" + fmtBytes(it.Bytes) + ")"
				switch st[it.ID].Status {
				case "exists":
					line += "  — already here, skipped"
				case "missing":
					line += "  — skipped: this host lacks docker compose"
				case "install":
					line += "  — installs what it needs first"
				case "merge":
					line += "  — added to what is here"
				}
				if s := st[it.ID].Status; s != "exists" && s != "missing" {
					total += it.Bytes
				}
				cmds = append(cmds, line)
			}
			for _, fs := range pf.Filesystems {
				cmds = append(cmds, "disk "+fs.Mount+": needs "+fmtBytes(fs.Room)+" (with 1 GB left free), has "+fmtBytes(fs.Free))
			}
			return change.Plan{Summary: "plan.migrate.import", SummaryP: map[string]any{"n": len(items), "from": man.Host, "size": fmtBytes(total)},
				Risk: change.RiskHigh, Commands: cmds}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			a := svc(env)
			var p migParams
			json.Unmarshal(raw, &p)
			ids := p.Items
			if len(ids) == 0 {
				man, err := a.migManifestOf(env.Ctx, p)
				if err != nil {
					return nil, nil, err
				}
				for _, it := range migChosen(man, nil) {
					ids = append(ids, it.ID)
				}
				p.Items = ids
			}
			want := map[string]bool{}
			for _, id := range ids {
				want[id] = true
			}
			ctx, cancel := context.WithCancel(env.Ctx)
			defer cancel()
			rc, err := a.migOpen(ctx, p, func(f string, v ...any) {})
			if err != nil {
				return nil, nil, err
			}
			defer rc.Close()
			rep, err := a.migRestore(env, rc, want)
			env.Log("restored %d, skipped %d, failed %d (%s written)", len(rep.Done), len(rep.Skipped), len(rep.Failed), fmtBytes(rep.Bytes))
			if err != nil {
				return rep, nil, err
			}
			if len(rep.Failed) > 0 {
				return rep, nil, errs.New(500, "migrate.partial", "done", len(rep.Done), "failed", len(rep.Failed))
			}
			return rep, nil, nil
		},
	})
	change.Register(change.Kind{
		Name: "migrate.folders", Perm: "system.manage", Scope: "system:write", Title: "kind.migrate.folders", CLI: "berth migrate folders",
		Params: change.ParamSchema{Props: map[string]change.Prop{"folders": {Type: "array"}}, Required: []string{"folders"}},
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			var p struct{ Folders []string }
			json.Unmarshal(raw, &p)
			for _, f := range p.Folders {
				if err := migFolderOK(f); err != nil {
					return change.Plan{}, err
				}
			}
			return change.Plan{Summary: "plan.migrate.folders", SummaryP: map[string]any{"n": len(p.Folders)}, Risk: change.RiskLow, Undoable: true}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			a := svc(env)
			var p struct{ Folders []string }
			json.Unmarshal(raw, &p)
			old := a.migFolders(env.Host)
			clean := []string{}
			for _, f := range p.Folders {
				clean = append(clean, filepath.Clean(f))
			}
			b, _ := json.Marshal(clean)
			a.setSetting("migrate.folders."+env.Host, string(b))
			u, _ := json.Marshal(map[string]any{"folders": old})
			return nil, &change.UndoRecipe{Kind: "migrate.folders", Params: u}, nil
		},
	})
}

var migDenied = []string{"/etc", "/usr", "/bin", "/sbin", "/lib", "/lib64", "/boot", "/proc", "/sys", "/dev", "/run", "/var/lib/berth", "/var/lib/docker", "/var/lib/incus", "/var/lib/containerd", "/var/lib/mysql", "/var/lib/pgsql", "/var/lib/postgresql", "/srv/berth"}

func migFolderOK(f string) error {
	c := filepath.Clean(f)
	if !filepath.IsAbs(c) || c == "/" || c != strings.TrimRight(f, "/") && c != f {
		return errs.New(400, "migrate.bad_path", "path", f)
	}
	for _, d := range migDenied {
		if c == d || strings.HasPrefix(c, d+"/") || strings.HasPrefix(d, c+"/") {
			return errs.New(400, "migrate.folder_denied", "path", f)
		}
	}
	return nil
}

type migDownload struct {
	host  string
	items []string
	pass  string
	actor change.Actor
	until time.Time
}

var migDownloads sync.Map // one-time pack links

func (a *App) migCodeView(host string) map[string]any {
	c, ok := a.migCodeRec()
	if !ok || c.Host != host {
		return nil
	}
	return map[string]any{"expires": c.Expires, "by": c.By}
}

// migServePack writes the sealed pack of host to w.
func (a *App) migServePack(ctx context.Context, w io.Writer, host string, items []string, pass string) {
	pr, pw := io.Pipe()
	go func() { pw.CloseWithError(a.migWritePack(ctx, host, items, pw, func(string, ...any) {})) }()
	if err := snapshot.Seal(w, pr, pass); err != nil {
		pr.CloseWithError(err) // the receiver sees a cut stream, which Open reports as damaged
	}
}
