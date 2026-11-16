package panel

// Simple mode (方案 §10.4 简要模式): an app wall for family members — the sites' icons and
// names, one tap opens them, nothing else. A super administrator turns it on per user and
// picks which sites the wall shows. Icons come from the store app behind a site, or the
// site's own favicon fetched (and cached) by the panel.
//
// Share links (方案 §5.6): a temporary public download link for one file, with an expiry,
// a download limit and an optional password, served at /s/{id}.

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/base32"
	"encoding/json"
	"html/template"
	"io"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/wangsirco/berth/internal/change"
	"github.com/wangsirco/berth/internal/errs"
)

type wallItem struct {
	Host  string            `json:"host"`
	ID    int64             `json:"id"`
	Name  string            `json:"name"`
	URL   string            `json:"url"`
	AppID string            `json:"app_id,omitempty"`
	Names map[string]string `json:"names,omitempty"` // the store app's name by locale
}

// appOfSite finds the store app whose containers a site points at.
func (a *App) appOfSite(host string, s Site) string {
	if s.Target.Type != "container" || s.Target.Container == "" {
		return ""
	}
	ins, _ := a.appInstances(host)
	for _, in := range ins {
		if s.Target.Container == in.Name || strings.HasPrefix(s.Target.Container, in.Name+"-") {
			return in.AppID
		}
	}
	return ""
}

func (a *App) wall() []wallItem {
	pick := map[string]bool{}
	for _, k := range splitList(a.setting("simple.sites")) {
		pick[k] = true
	}
	hosts := []string{"local"}
	for _, h := range a.remoteHosts() {
		hosts = append(hosts, h.Name)
	}
	out := []wallItem{}
	for _, h := range hosts {
		list, _ := a.sites(h)
		for _, s := range list {
			key := h + ":" + strconv.FormatInt(s.ID, 10)
			if !s.Enabled || len(s.Domains) == 0 || len(pick) > 0 && !pick[key] {
				continue
			}
			it := wallItem{Host: h, ID: s.ID, Name: strings.SplitN(s.Domains[0], ".", 2)[0], URL: "https://" + s.Domains[0], AppID: a.appOfSite(h, s)}
			if it.AppID != "" {
				if def, err := findApp(it.AppID); err == nil {
					it.Names = def.Name
				}
			}
			out = append(out, it)
		}
	}
	return out
}

var (
	iconMu    sync.Mutex
	iconCache = map[string]struct {
		at   time.Time
		ct   string
		body []byte
	}{}
)

var iconClient = &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}, // LAN sites use the panel's own CA
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) > 3 {
			return http.ErrUseLastResponse
		}
		return nil
	}}

// siteIcon: the store app's SVG, else the site's favicon (cached for a day).
func (a *App) siteIcon(ctx context.Context, it wallItem) (string, []byte) {
	if it.AppID != "" {
		if app, err := findApp(it.AppID); err == nil && app.Icon != "" {
			return "image/svg+xml", []byte(app.Icon)
		}
	}
	key := it.URL
	iconMu.Lock()
	c, ok := iconCache[key]
	iconMu.Unlock()
	if ok && time.Since(c.at) < 24*time.Hour {
		return c.ct, c.body
	}
	ct, body := "", []byte(nil)
	for _, p := range []string{"/favicon.ico", "/apple-touch-icon.png", "/favicon.png"} {
		req, _ := http.NewRequestWithContext(ctx, "GET", it.URL+p, nil)
		resp, err := iconClient.Do(req)
		if err != nil {
			break
		}
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 256<<10))
		resp.Body.Close()
		t := resp.Header.Get("Content-Type")
		if resp.StatusCode == 200 && len(b) > 0 && (strings.HasPrefix(t, "image/") && !strings.Contains(t, "svg")) {
			ct, body = t, b
			break
		}
	}
	iconMu.Lock()
	iconCache[key] = struct {
		at   time.Time
		ct   string
		body []byte
	}{time.Now(), ct, body}
	iconMu.Unlock()
	return ct, body
}

// ---- share links ----

type share struct {
	ID        string `json:"id"`
	Host      string `json:"host"`
	Path      string `json:"path"`
	CreatedBy string `json:"created_by"`
	CreatedAt int64  `json:"created_at"`
	ExpiresAt int64  `json:"expires_at"`
	Max       int    `json:"max_downloads"`
	Downloads int    `json:"downloads"`
	Password  bool   `json:"password"`
	root      string
	pwHash    string
	byID      int64
}

func (a *App) shareByID(id string) (share, error) {
	var s share
	err := a.db.QueryRow(`SELECT id, host, path, root, created_by, created_by_id, created_at, expires_at, max_downloads, downloads, pw_hash FROM shares WHERE id=?`, id).
		Scan(&s.ID, &s.Host, &s.Path, &s.root, &s.CreatedBy, &s.byID, &s.CreatedAt, &s.ExpiresAt, &s.Max, &s.Downloads, &s.pwHash)
	s.Password = s.pwHash != ""
	return s, err
}

func (a *App) shares(host string) []share {
	out := []share{}
	rows, err := a.db.Query(`SELECT id FROM shares WHERE host=? ORDER BY created_at DESC`, host)
	if err != nil {
		return out
	}
	var ids []string
	for rows.Next() {
		var id string
		rows.Scan(&id)
		ids = append(ids, id)
	}
	rows.Close()
	for _, id := range ids {
		if s, err := a.shareByID(id); err == nil {
			out = append(out, s)
		}
	}
	return out
}

func (s share) usable() bool {
	return time.Now().Unix() < s.ExpiresAt && (s.Max == 0 || s.Downloads < s.Max)
}

var sharePage = template.Must(template.New("s").Parse(`<!doctype html><html lang="{{.Lang}}"><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<meta name="robots" content="noindex"><title>{{.Name}}</title>
<style>body{font:15px system-ui,-apple-system,"PingFang SC",sans-serif;margin:0;display:grid;place-items:center;min-height:100vh;background:#f6f8fa;color:#1f2328}
@media(prefers-color-scheme:dark){body{background:#0d1117;color:#e6edf3}.c{background:#161b22!important;border-color:#30363d!important}input{background:#0d1117;color:#e6edf3;border-color:#30363d}}
.c{background:#fff;border:1px solid #d1d9e0;border-radius:12px;padding:28px;max-width:360px;width:calc(100% - 48px)}h1{font-size:17px;margin:0 0 4px;word-break:break-all}p{color:#656d76;font-size:13px;margin:0 0 18px}
input{width:100%;box-sizing:border-box;padding:8px 10px;border:1px solid #d1d9e0;border-radius:6px;font:inherit;margin-bottom:10px}
button,a.b{display:block;text-align:center;width:100%;box-sizing:border-box;padding:9px;border:0;border-radius:6px;background:#0b6e66;color:#fff;font:inherit;text-decoration:none;cursor:pointer}.e{color:#cf222e}</style>
<div class="c">{{if .Gone}}<h1>{{.T.gone}}</h1><p>{{.T.gone_hint}}</p>{{else}}<h1>{{.Name}}</h1><p>{{.T.expires}} {{.Expires}}{{if .Left}} · {{.Left}}{{end}}</p>
{{if .Password}}<form method="post">{{if .Wrong}}<p class="e">{{.T.wrong}}</p>{{end}}<input type="password" name="password" placeholder="{{.T.password}}" autofocus required><button>{{.T.download}}</button></form>
{{else}}<form method="post"><button>{{.T.download}}</button></form>{{end}}{{end}}</div></html>`))

func (a *App) handleShare(w http.ResponseWriter, r *http.Request) {
	lang := pickLocale(r.Header.Get("Accept-Language"))
	tr := func(k string) string { return T(lang, "share."+k, nil) }
	texts := map[string]string{}
	for _, k := range []string{"gone", "gone_hint", "expires", "wrong", "password", "download"} {
		texts[k] = tr(k)
	}
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; form-action 'self'")
	w.Header().Set("Referrer-Policy", "no-referrer")
	s, err := a.shareByID(r.PathValue("id"))
	if err != nil || !s.usable() {
		w.WriteHeader(404)
		sharePage.Execute(w, map[string]any{"Lang": lang, "Gone": true, "T": texts})
		return
	}
	view := map[string]any{"Lang": lang, "Name": path.Base(s.Path), "Expires": time.Unix(s.ExpiresAt, 0).Format("2006-01-02 15:04"), "Password": s.Password, "T": texts}
	if s.Max > 0 {
		view["Left"] = T(lang, "share.left", map[string]any{"n": s.Max - s.Downloads})
	}
	if r.Method != "POST" {
		sharePage.Execute(w, view)
		return
	}
	if s.Password {
		key := "share:" + a.clientIP(r)
		if a.limiter.blocked(key) || !checkPasswordHash(s.pwHash, r.FormValue("password")) {
			a.limiter.record(key)
			view["Wrong"] = true
			w.WriteHeader(403)
			sharePage.Execute(w, view)
			return
		}
	}
	// count first, so a limit of one cannot be raced
	res, err := a.db.Exec(`UPDATE shares SET downloads=downloads+1 WHERE id=? AND (max_downloads=0 OR downloads<max_downloads) AND expires_at>?`, s.ID, time.Now().Unix())
	if n, _ := res.RowsAffected(); err != nil || n == 0 {
		w.WriteHeader(404)
		sharePage.Execute(w, map[string]any{"Lang": lang, "Gone": true, "T": texts})
		return
	}
	ag, err := a.agent(s.Host)
	if err != nil {
		http.Error(w, "host offline", 502)
		return
	}
	rc, err := ag.Stream(r.Context(), "POST", "/v1/files/download", map[string]any{"root": s.root, "path": s.Path})
	if err != nil {
		http.Error(w, "file unavailable", 404)
		return
	}
	defer rc.Close()
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", "attachment; filename*=UTF-8''"+url.PathEscape(path.Base(s.Path)))
	io.Copy(w, rc)
}

var shareIDEnc = base32.NewEncoding("abcdefghijkmnpqrstuvwxyz23456789").WithPadding(base32.NoPadding)

func init() {
	settingDefs["simple.sites"] = settingDef{validate: func(v string) error { return nil }}
	routeHooks = append(routeHooks, func(a *App, mux *http.ServeMux) {
		mux.HandleFunc("GET /api/v1/wall", a.authed("", func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, 200, map[string]any{"items": a.wall(), "name": a.setting("panel.name")})
		}))
		mux.HandleFunc("GET /api/v1/wall/icon/{host}/{id}", a.authed("", func(w http.ResponseWriter, r *http.Request) {
			id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
			for _, it := range a.wall() {
				if it.Host == r.PathValue("host") && it.ID == id {
					ct, body := a.siteIcon(r.Context(), it)
					if body == nil {
						w.WriteHeader(404)
						return
					}
					w.Header().Set("Content-Type", ct)
					w.Header().Set("Cache-Control", "private, max-age=86400")
					w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'")
					w.Write(body)
					return
				}
			}
			w.WriteHeader(404)
		}))
		mux.HandleFunc("GET /api/v1/hosts/{host}/shares", a.authed("files.read", func(w http.ResponseWriter, r *http.Request) {
			u := currentUser(r)
			out := []share{}
			for _, s := range a.shares(r.PathValue("host")) {
				if u.Role == RoleSuper || s.byID == u.ID {
					out = append(out, s)
				}
			}
			writeJSON(w, 200, map[string]any{"items": out})
		}))
		mux.HandleFunc("GET /s/{id}", a.handleShare)
		mux.HandleFunc("POST /s/{id}", a.handleShare)
	})

	change.Register(change.Kind{
		Name: "file.share", Perm: "files.read", Scope: "files:write", Title: "kind.file.share", CLI: "berth file share {{.path}}", Redact: []string{"password"},
		Params: change.ParamSchema{Props: map[string]change.Prop{"path": {Type: "string"}, "hours": {Type: "integer", Desc: "valid for (1-720, default 24)"},
			"max_downloads": {Type: "integer", Desc: "0 = no limit"}, "password": {Type: "string", Desc: "optional"}}, Required: []string{"path"}},
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			var p struct {
				Path     string
				Hours    int
				Max      int `json:"max_downloads"`
				Password string
			}
			json.Unmarshal(raw, &p)
			if p.Hours == 0 {
				p.Hours = 24
			}
			if p.Hours < 1 || p.Hours > 720 || p.Max < 0 {
				return change.Plan{}, errs.New(400, "share.bad")
			}
			key := "plan.file.share"
			if p.Password != "" {
				key = "plan.file.share_pw"
			}
			return change.Plan{Summary: key, SummaryP: map[string]any{"path": p.Path, "hours": p.Hours, "max": p.Max}, Risk: change.RiskMedium, Undoable: true}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			a := svc(env)
			var p struct {
				Path     string
				Hours    int
				Max      int `json:"max_downloads"`
				Password string
			}
			json.Unmarshal(raw, &p)
			if p.Hours == 0 {
				p.Hours = 24
			}
			uid, _ := strconv.ParseInt(env.ActorID, 10, 64)
			u, err := a.userByID(uid)
			if err != nil || env.ActorType != "user" {
				return nil, nil, errs.New(403, "share.person_only")
			}
			root, err := rootFor(u, p.Path)
			if err != nil {
				return nil, nil, err
			}
			var st struct {
				Dir bool `json:"dir"`
			}
			if err := a.call(env.Ctx, env.Host, "POST", "/v1/files/stat", map[string]string{"root": root, "path": p.Path}, &st); err != nil {
				return nil, nil, err
			}
			if st.Dir {
				return nil, nil, errs.New(400, "share.not_file")
			}
			b := make([]byte, 15)
			rand.Read(b)
			id := shareIDEnc.EncodeToString(b)
			h := ""
			if p.Password != "" {
				if h, err = hashPassword(p.Password); err != nil {
					return nil, nil, err
				}
			}
			now := time.Now()
			if _, err := a.db.Exec(`INSERT INTO shares(id, host, path, root, created_by, created_by_id, created_at, expires_at, max_downloads, pw_hash) VALUES(?,?,?,?,?,?,?,?,?,?)`,
				id, env.Host, p.Path, root, env.ActorName, uid, now.Unix(), now.Add(time.Duration(p.Hours)*time.Hour).Unix(), p.Max, h); err != nil {
				return nil, nil, err
			}
			u2, _ := json.Marshal(map[string]string{"id": id})
			return map[string]string{"id": id, "url": a.publicURL() + "/s/" + id}, &change.UndoRecipe{Kind: "file.unshare", Params: u2}, nil
		},
	})
	change.Register(change.Kind{
		Name: "file.unshare", Perm: "files.read", Scope: "files:write", Title: "kind.file.unshare", CLI: "berth file unshare {{.id}}",
		Params: change.ParamSchema{Props: map[string]change.Prop{"id": {Type: "string"}}, Required: []string{"id"}},
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			var p struct{ ID string }
			json.Unmarshal(raw, &p)
			s, err := svc(env).shareByID(p.ID)
			if err != nil {
				return change.Plan{}, errs.New(404, "share.not_found")
			}
			return change.Plan{Summary: "plan.file.unshare", SummaryP: map[string]any{"path": s.Path}, Risk: change.RiskLow}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			a := svc(env)
			var p struct{ ID string }
			json.Unmarshal(raw, &p)
			s, err := a.shareByID(p.ID)
			if err != nil {
				return nil, nil, errs.New(404, "share.not_found")
			}
			uid, _ := strconv.ParseInt(env.ActorID, 10, 64)
			if u, err := a.userByID(uid); err != nil || u.Role != RoleSuper && s.byID != uid {
				return nil, nil, errs.New(403, "share.not_yours")
			}
			a.db.Exec(`DELETE FROM shares WHERE id=?`, p.ID)
			return nil, nil, nil
		},
	})
	// expired links are removed once a day after a week
	cronHooks = append(cronHooks, func(a *App, at time.Time) {
		if at.Hour() == 4 && at.Minute() == 7 {
			a.db.Exec(`DELETE FROM shares WHERE expires_at < ?`, at.AddDate(0, 0, -7).Unix())
		}
	})
}
