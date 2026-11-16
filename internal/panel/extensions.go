package panel

// Third-party extensions (方案 §9). An extension is a container described by
// berth-extension.yaml: what it is, how to build or pull it, which Berth API scopes it
// asks for, which events it wants and where its page listens. Installing lists the scopes;
// the installer grants all or some, and Berth issues a token limited to them — the
// extension can do only that, and the audit log names it as the actor. Its page is shown
// inside the panel through /ext/<id>/<key>/, in a sandboxed frame (its own opaque origin:
// it cannot use the viewer's session), and events reach it as webhooks.

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/wangsirco/berth/internal/change"
	"github.com/wangsirco/berth/internal/errs"
)

type extManifest struct {
	ID          string            `yaml:"id" json:"id"`
	Name        map[string]string `yaml:"name" json:"name"`
	Version     string            `yaml:"version" json:"version"`
	Description map[string]string `yaml:"description" json:"description,omitempty"`
	Image       string            `yaml:"image" json:"image,omitempty"`
	Build       string            `yaml:"build" json:"build,omitempty"`
	Port        int               `yaml:"port" json:"port"`
	Permissions []string          `yaml:"permissions" json:"permissions"`
	Events      []string          `yaml:"events" json:"events,omitempty"`
	Webhook     string            `yaml:"webhook" json:"webhook,omitempty"`
}

type extension struct {
	extManifest
	Source      string   `json:"source"`
	Scopes      []string `json:"scopes"` // granted
	Port        int      `json:"host_port"`
	InstalledBy string   `json:"installed_by"`
	InstalledAt int64    `json:"installed_at"`
	Running     bool     `json:"running"`
	tokenID     int64
}

var (
	extIDRe    = regexp.MustCompile(`^[a-z][a-z0-9-]{1,30}$`)
	extImageRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._/:@-]{0,200}$`)
	extHookRe  = regexp.MustCompile(`^/[A-Za-z0-9/_.-]{0,100}$`)
	extEvents  = map[string]bool{"change.done": true, "alert": true, "uptime": true, "drift": true, "host.online": true, "host.offline": true, "backup.run": true}
)

func (m extManifest) validate() error {
	if !extIDRe.MatchString(m.ID) {
		return errs.New(400, "ext.bad_manifest", "detail", "id")
	}
	if (m.Image == "") == (m.Build == "") {
		return errs.New(400, "ext.bad_manifest", "detail", "image or build")
	}
	if m.Image != "" && !extImageRe.MatchString(m.Image) {
		return errs.New(400, "ext.bad_manifest", "detail", "image")
	}
	if m.Build != "" && (strings.Contains(m.Build, "..") || strings.HasPrefix(m.Build, "/")) {
		return errs.New(400, "ext.bad_manifest", "detail", "build")
	}
	if m.Port < 1 || m.Port > 65535 {
		return errs.New(400, "ext.bad_manifest", "detail", "port")
	}
	for _, s := range m.Permissions {
		if !validScope(s) {
			return errs.New(400, "ext.bad_manifest", "detail", "permission "+s)
		}
	}
	for _, e := range m.Events {
		if !extEvents[e] {
			return errs.New(400, "ext.bad_manifest", "detail", "event "+e)
		}
	}
	if m.Webhook != "" && !extHookRe.MatchString(m.Webhook) {
		return errs.New(400, "ext.bad_manifest", "detail", "webhook")
	}
	return nil
}

func (m extManifest) title(locale string) string {
	if n := m.Name[locale]; n != "" {
		return n
	}
	if n := m.Name["en"]; n != "" {
		return n
	}
	return m.ID
}

// readManifest reads berth-extension.yaml from a directory on the host.
func (a *App) readManifest(ctx context.Context, host, dir string) (extManifest, error) {
	var m extManifest
	if !strings.HasPrefix(dir, "/") || strings.Contains(dir, "..") {
		return m, errs.New(400, "ext.bad_source")
	}
	var f struct {
		Content []byte `json:"content"`
	}
	if err := a.call(ctx, host, "POST", "/v1/files/read", map[string]any{"root": "/", "path": strings.TrimSuffix(dir, "/") + "/berth-extension.yaml", "max": 65536}, &f); err != nil {
		return m, errs.New(404, "ext.no_manifest", "path", dir)
	}
	if err := yaml.Unmarshal(f.Content, &m); err != nil {
		return m, errs.New(400, "ext.bad_manifest", "detail", err.Error())
	}
	return m, m.validate()
}

func (a *App) extensions() []extension {
	out := []extension{}
	rows, err := a.db.Query(`SELECT id, manifest, source, scopes, token_id, port, installed_by, installed_at FROM extensions ORDER BY id`)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var e extension
		var id, man, scopes string
		rows.Scan(&id, &man, &e.Source, &scopes, &e.tokenID, &e.Port, &e.InstalledBy, &e.InstalledAt)
		json.Unmarshal([]byte(man), &e.extManifest)
		json.Unmarshal([]byte(scopes), &e.Scopes)
		out = append(out, e)
	}
	return out
}

func (a *App) extension(id string) (extension, bool) {
	for _, e := range a.extensions() {
		if e.ID == id {
			return e, true
		}
	}
	return extension{}, false
}

// freeExtPort picks a loopback port for the extension's page.
func (a *App) freeExtPort() int {
	used := map[int]bool{}
	for _, e := range a.extensions() {
		used[e.Port] = true
	}
	for p := 17200; p < 17400; p++ {
		if used[p] {
			continue
		}
		if l, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(p)); err == nil {
			l.Close()
			return p
		}
	}
	return 0
}

func (a *App) panelPort() string {
	_, p, _ := net.SplitHostPort(a.cfg.Listen)
	return firstNonEmpty(p, "7090")
}

func extCompose(m extManifest, source string, port int) string {
	var b strings.Builder
	b.WriteString("# Berth extension " + m.ID + " " + m.Version + " (managed by Berth; reinstall to change)\nservices:\n  app:\n")
	if m.Image != "" {
		fmt.Fprintf(&b, "    image: %s\n", m.Image)
	} else {
		fmt.Fprintf(&b, "    build: %s\n    image: berth-ext-%s:%s\n", strings.TrimSuffix(source, "/")+"/"+strings.TrimPrefix(m.Build, "./"), m.ID, firstNonEmpty(m.Version, "latest"))
	}
	fmt.Fprintf(&b, "    restart: unless-stopped\n    ports: [\"127.0.0.1:%d:%d\"]\n", port, m.Port)
	b.WriteString("    environment:\n      BERTH_URL: ${BERTH_URL}\n      BERTH_TOKEN: ${BERTH_TOKEN}\n      BERTH_EXTENSION: " + m.ID + "\n      BERTH_CA_FILE: /etc/berth/ca.crt\n")
	b.WriteString("    volumes: [\"./berth-ca.crt:/etc/berth/ca.crt:ro\"]\n    extra_hosts: [\"host.docker.internal:host-gateway\"]\n")
	b.WriteString("    security_opt: [\"no-new-privileges:true\"]\n    mem_limit: 256m\n")
	return b.String()
}

// ---- the page ----

func (a *App) extKey(uid int64, id string, exp int64) string {
	buf := make([]byte, 16)
	binary.BigEndian.PutUint64(buf, uint64(uid))
	binary.BigEndian.PutUint64(buf[8:], uint64(exp))
	m := hmac.New(sha256.New, a.approvalSecret())
	m.Write([]byte("ext:" + id))
	m.Write(buf)
	return base64.RawURLEncoding.EncodeToString(append(buf, m.Sum(nil)[:16]...))
}

func (a *App) checkExtKey(id, key string) (int64, bool) {
	b, err := base64.RawURLEncoding.DecodeString(key)
	if err != nil || len(b) != 32 {
		return 0, false
	}
	uid := int64(binary.BigEndian.Uint64(b))
	exp := int64(binary.BigEndian.Uint64(b[8:]))
	if time.Now().Unix() > exp || !hmac.Equal([]byte(key), []byte(a.extKey(uid, id, exp))) {
		return 0, false
	}
	return uid, true
}

func (a *App) handleExtPage(w http.ResponseWriter, r *http.Request) {
	id, key := r.PathValue("id"), r.PathValue("key")
	e, ok := a.extension(id)
	uid, valid := a.checkExtKey(id, key)
	if !ok || !valid {
		http.Error(w, "this link has expired; open the extension from the panel again", 403)
		return
	}
	u, err := a.userByID(uid)
	if err != nil || u.Disabled || !(u.Role == RoleSuper || u.Can("extensions.use")) {
		http.Error(w, "forbidden", 403)
		return
	}
	prefix := "/ext/" + id + "/" + key
	w.Header().Del("Content-Security-Policy") // the panel's own (no framing, no inline scripts) is replaced below
	// the page runs in a sandboxed (opaque, "null") origin: let it call its own paths here.
	// No credentials are involved — the key in the path is what admits it.
	if r.Header.Get("Origin") == "null" {
		if r.Method == "OPTIONS" {
			w.Header().Set("Access-Control-Allow-Origin", "null")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
			w.Header().Set("Access-Control-Max-Age", "600")
			w.WriteHeader(204)
			return
		}
	}
	target, _ := url.Parse("http://127.0.0.1:" + strconv.Itoa(e.Port))
	rp := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			pr.Out.URL.Path = "/" + strings.TrimPrefix(strings.TrimPrefix(pr.In.URL.Path, prefix), "/")
			pr.Out.URL.RawPath = ""
			pr.Out.Header.Del("Cookie") // never the viewer's session
			pr.Out.Header.Del("Authorization")
			pr.Out.Header.Del("X-CSRF-Token")
			pr.Out.Header.Set("X-Berth-User", u.Username)
		},
		ModifyResponse: func(resp *http.Response) error {
			resp.Header.Del("Set-Cookie")
			if resp.Request != nil && resp.Request.Header.Get("Origin") == "null" {
				resp.Header.Set("Access-Control-Allow-Origin", "null")
			}
			// its own opaque origin: scripts run, but cannot reach the panel as the viewer
			resp.Header.Set("Content-Security-Policy", "sandbox allow-scripts allow-forms allow-popups allow-downloads; frame-ancestors 'self'")
			resp.Header.Set("X-Content-Type-Options", "nosniff")
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			http.Error(w, "the extension is not running", 502)
		},
	}
	rp.ServeHTTP(w, r)
}

// ---- events ----

var extHookClient = &http.Client{Timeout: 10 * time.Second}

func (a *App) extEventLoop(ctx context.Context) {
	ch, stop := a.hub.subscribe()
	defer stop()
	var mu sync.Mutex
	busy := map[string]bool{}
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-ch:
			if !ok {
				return
			}
			for _, e := range a.extensions() {
				if e.Webhook == "" || !contains(e.Events, ev.Type) {
					continue
				}
				body, _ := json.Marshal(map[string]any{"type": ev.Type, "data": ev.Data})
				mu.Lock()
				if busy[e.ID] { // one at a time per extension; a slow one drops events rather than piling up
					mu.Unlock()
					continue
				}
				busy[e.ID] = true
				mu.Unlock()
				go func(port int, path, id string) {
					defer func() { mu.Lock(); delete(busy, id); mu.Unlock() }()
					req, _ := http.NewRequestWithContext(ctx, "POST", "http://127.0.0.1:"+strconv.Itoa(port)+path, strings.NewReader(string(body)))
					req.Header.Set("Content-Type", "application/json")
					if resp, err := extHookClient.Do(req); err == nil {
						resp.Body.Close()
					}
				}(e.Port, e.Webhook, e.ID)
			}
		}
	}
}

// ---- API and kinds ----

func init() {
	startHooks = append(startHooks, func(a *App, ctx context.Context) { go a.extEventLoop(ctx) })
	routeHooks = append(routeHooks, func(a *App, mux *http.ServeMux) {
		mux.HandleFunc("GET /api/v1/extensions", a.authed("", func(w http.ResponseWriter, r *http.Request) {
			u := currentUser(r)
			list := a.extensions()
			if u.Role != RoleSuper && !u.Can("extensions.use") {
				list = []extension{}
			}
			running := a.extRunning(r.Context())
			for i := range list {
				list[i].Running = running["ext-"+list[i].ID]
			}
			writeJSON(w, 200, map[string]any{"items": list})
		}))
		mux.HandleFunc("POST /api/v1/extensions/manifest", a.superOnly(func(w http.ResponseWriter, r *http.Request) {
			var in struct{ Source string }
			if err := readJSON(r, &in); err != nil {
				a.fail(w, r, err)
				return
			}
			m, err := a.readManifest(r.Context(), "local", in.Source)
			if err != nil {
				a.fail(w, r, err)
				return
			}
			writeJSON(w, 200, m)
		}))
		mux.HandleFunc("GET /api/v1/extensions/{id}/open", a.authed("", func(w http.ResponseWriter, r *http.Request) {
			u := currentUser(r)
			id := r.PathValue("id")
			if _, ok := a.extension(id); !ok || !(u.Role == RoleSuper || u.Can("extensions.use")) {
				a.fail(w, r, errs.New(404, "ext.not_found"))
				return
			}
			exp := time.Now().Add(12 * time.Hour).Unix()
			writeJSON(w, 200, map[string]string{"url": "/ext/" + id + "/" + a.extKey(u.ID, id, exp) + "/"})
		}))
		mux.HandleFunc("/ext/{id}/{key}/", a.handleExtPage)
	})

	change.Register(change.Kind{
		Name: "ext.install", Perm: "extensions.manage", Scope: "settings:write", Title: "kind.ext.install", CLI: "berth ext install {{.source}}", Heavy: true, MemMB: 200,
		Params: change.ParamSchema{Props: map[string]change.Prop{"source": {Type: "string", Desc: "directory on the panel's host holding berth-extension.yaml"},
			"grant": {Type: "array", Desc: "scopes to grant (a subset of what it asks for); omitted = all"}}, Required: []string{"source"}},
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			a := svc(env)
			var p struct {
				Source string
				Grant  []string
			}
			json.Unmarshal(raw, &p)
			if env.Host != "local" {
				return change.Plan{}, errs.New(400, "ext.local_only")
			}
			m, err := a.readManifest(env.Ctx, env.Host, p.Source)
			if err != nil {
				return change.Plan{}, err
			}
			if _, ok := a.extension(m.ID); ok {
				return change.Plan{}, errs.New(409, "ext.exists", "id", m.ID)
			}
			grant := p.Grant
			if grant == nil {
				grant = m.Permissions
			}
			lines := []string{}
			for _, s := range m.Permissions {
				mark := "✗ "
				if contains(grant, s) {
					mark = "✓ "
				}
				lines = append(lines, mark+s)
			}
			for _, s := range grant {
				if !contains(m.Permissions, s) {
					return change.Plan{}, errs.New(400, "ext.grant_extra", "scope", s)
				}
			}
			if len(m.Events) > 0 {
				lines = append(lines, "events: "+strings.Join(m.Events, ", "))
			}
			return change.Plan{Summary: "plan.ext.install", SummaryP: map[string]any{"name": m.title("en"), "id": m.ID, "n": len(grant)}, Risk: change.RiskHigh, Undoable: true,
				Commands: lines, Affected: []change.Object{{Type: "extension", ID: m.ID, Name: m.title("en")}}}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			a := svc(env)
			var p struct {
				Source string
				Grant  []string
			}
			json.Unmarshal(raw, &p)
			m, err := a.readManifest(env.Ctx, env.Host, p.Source)
			if err != nil {
				return nil, nil, err
			}
			grant := p.Grant
			if grant == nil {
				grant = m.Permissions
			}
			uid, _ := strconv.ParseInt(env.ActorID, 10, 64)
			owner, err := a.userByID(uid)
			if err != nil || env.ActorType != "user" {
				return nil, nil, errs.New(403, "ext.person_only")
			}
			port := a.freeExtPort()
			if port == 0 {
				return nil, nil, errs.New(503, "ext.no_port")
			}
			tok, tid, err := a.createToken(owner, tokenSpec{Kind: "ext", Name: m.title("en"), Scopes: orEmpty(grant), Note: "extension " + m.ID})
			if err != nil {
				return nil, nil, err
			}
			project := "ext-" + m.ID
			envFile := "BERTH_URL=https://host.docker.internal:" + a.panelPort() + "\nBERTH_TOKEN=" + tok + "\n"
			fail := func(err error) (any, *change.UndoRecipe, error) {
				a.revokeToken(tid)
				return nil, nil, err
			}
			if err := a.call(env.Ctx, env.Host, "PUT", "/v1/compose/projects/"+project+"/files", map[string]any{"compose": extCompose(m, p.Source, port), "env": envFile, "create": true}, nil); err != nil {
				return fail(err)
			}
			if err := a.call(env.Ctx, env.Host, "POST", "/v1/files/write", map[string]any{"root": "/", "path": "/srv/berth/stacks/" + project + "/berth-ca.crt", "content": a.caPEM}, nil); err != nil {
				return fail(err)
			}
			env.Log("building and starting %s", project)
			if err := agentOp(env, "/v1/compose/projects/"+project+"/up", nil); err != nil {
				return fail(err)
			}
			mj, _ := json.Marshal(m)
			sj, _ := json.Marshal(orEmpty(grant))
			if _, err := a.db.Exec(`INSERT INTO extensions(id, manifest, source, scopes, token_id, port, installed_by, installed_at) VALUES(?,?,?,?,?,?,?,?)`,
				m.ID, string(mj), p.Source, string(sj), tid, port, env.ActorName, time.Now().Unix()); err != nil {
				return fail(err)
			}
			u, _ := json.Marshal(map[string]string{"id": m.ID})
			return map[string]any{"id": m.ID, "port": port}, &change.UndoRecipe{Kind: "ext.remove", Params: u}, nil
		},
	})
	change.Register(change.Kind{
		Name: "ext.remove", Perm: "extensions.manage", Scope: "settings:write", Title: "kind.ext.remove", CLI: "berth ext remove {{.id}}", Heavy: true,
		Params: change.ParamSchema{Props: map[string]change.Prop{"id": {Type: "string"}}, Required: []string{"id"}},
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			var p struct{ ID string }
			json.Unmarshal(raw, &p)
			e, ok := svc(env).extension(p.ID)
			if !ok {
				return change.Plan{}, errs.New(404, "ext.not_found")
			}
			return change.Plan{Summary: "plan.ext.remove", SummaryP: map[string]any{"name": e.title("en")}, Risk: change.RiskMedium, Undoable: true,
				Commands: []string{"revoke its token", "docker compose -p ext-" + e.ID + " down"}}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			a := svc(env)
			var p struct{ ID string }
			json.Unmarshal(raw, &p)
			e, ok := a.extension(p.ID)
			if !ok {
				return nil, nil, errs.New(404, "ext.not_found")
			}
			a.revokeToken(e.tokenID)
			if err := agentOp(env, "/v1/compose/projects/ext-"+e.ID+"/remove", nil); err != nil {
				env.Log("removing the container: %v", err)
			}
			a.db.Exec(`DELETE FROM extensions WHERE id=?`, e.ID)
			u, _ := json.Marshal(map[string]any{"source": e.Source, "grant": e.Scopes})
			return nil, &change.UndoRecipe{Kind: "ext.install", Params: u}, nil
		},
	})
}

// extRunning: which ext-* compose projects have running containers.
func (a *App) extRunning(ctx context.Context) map[string]bool {
	out := map[string]bool{}
	var list []struct {
		Name    string `json:"name"`
		Running int    `json:"running"`
	}
	if a.call(ctx, "local", "GET", "/v1/compose/projects", nil, &list) == nil {
		for _, p := range list {
			if strings.HasPrefix(p.Name, "ext-") && p.Running > 0 {
				out[p.Name] = true
			}
		}
	}
	return out
}
