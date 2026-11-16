package panel

// App store (方案 §5.5): official catalog → install as a compose stack with checks
// (architecture, 16K pages, memory, ports, disk), generated passwords kept sealed,
// health check, then an offer to give it a site.

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/wangsirco/berth/internal/agent/docker"
	"github.com/wangsirco/berth/internal/agent/metrics"
	"github.com/wangsirco/berth/internal/agentproto"
	"github.com/wangsirco/berth/internal/appstore"
	"github.com/wangsirco/berth/internal/change"
	"github.com/wangsirco/berth/internal/errs"
	"go.yaml.in/yaml/v3"
)

const appsRoot = "/srv/berth/apps"

// dataSpec asks the agent to prepare data folders owned by the image's user.
func dataSpec(app *appstore.App, name string) any {
	if app.DataOwner == "" {
		return nil
	}
	return map[string]any{"root": appsRoot + "/" + name, "dirs": app.DataDirs(), "owner": app.DataOwner}
}

// appHealthWait: how long a freshly installed app gets to answer (tests shorten it).
var appHealthWait = 3 * time.Minute

var (
	catalogOnce sync.Once
	catalog     []appstore.App
	catalogErr  error
	instanceRe  = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,40}$`)
)

func appCatalog() ([]appstore.App, error) {
	catalogOnce.Do(func() { catalog, catalogErr = appstore.Load() })
	return catalog, catalogErr
}

func findApp(id string) (appstore.App, error) {
	list, err := appCatalog()
	if err != nil {
		return appstore.App{}, err
	}
	for _, a := range list {
		if a.ID == id {
			return a, nil
		}
	}
	return appstore.App{}, errs.New(404, "app.not_found", "id", id)
}

type appInstance struct {
	Name        string            `json:"name"`
	Host        string            `json:"host"`
	AppID       string            `json:"app_id"`
	Version     string            `json:"version"`
	Values      map[string]string `json:"values"` // without secrets
	Runtime     string            `json:"runtime,omitempty"`
	Access      string            `json:"access,omitempty"`
	Sites       []siteRef         `json:"sites,omitempty"` // sites pointing at it
	InstalledAt int64             `json:"installed_at"`
	UpdatedAt   int64             `json:"updated_at"`
	Latest      string            `json:"latest,omitempty"` // catalog version when newer
	Site        map[string]any    `json:"site,omitempty"`   // target for "give it a site"
	secretsEnc  []byte
}

func (a *App) appInstance(host, name string) (appInstance, error) {
	var in appInstance
	var vals string
	err := a.db.QueryRow(`SELECT name, host, app_id, version, vals, secrets_enc, runtime, access, installed_at, updated_at FROM apps WHERE host=? AND name=?`, host, name).
		Scan(&in.Name, &in.Host, &in.AppID, &in.Version, &vals, &in.secretsEnc, &in.Runtime, &in.Access, &in.InstalledAt, &in.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return in, errs.New(404, "app.instance_not_found", "name", name)
	}
	json.Unmarshal([]byte(vals), &in.Values)
	return in, err
}

func (a *App) appInstances(host string) ([]appInstance, error) {
	rows, err := a.db.Query(`SELECT name FROM apps WHERE host=? ORDER BY name`, host)
	if err != nil {
		return nil, err
	}
	var names []string
	for rows.Next() {
		var n string
		rows.Scan(&n)
		names = append(names, n)
	}
	rows.Close()
	out := []appInstance{}
	for _, n := range names {
		in, err := a.appInstance(host, n)
		if err != nil {
			continue
		}
		if c, err := findApp(in.AppID); err == nil {
			if c.Version != in.Version {
				in.Latest = c.Version
			}
			if c.Site.Service != "" {
				in.Site = map[string]any{"container": containerName(in.Name, c.Site.Service, c.Compose), "port": c.Site.Port}
			}
			for _, st := range a.sitesForProject(host, in.Name) {
				in.Sites = append(in.Sites, siteRef{ID: st.ID, Domain: st.Primary(), Enabled: st.Enabled})
			}
		}
		out = append(out, in)
	}
	return out, nil
}

// secrets returns all values of an instance, secrets included.
func (a *App) appSecrets(in appInstance) (map[string]string, error) {
	out := map[string]string{}
	for k, v := range in.Values {
		out[k] = v
	}
	if len(in.secretsEnc) > 0 {
		plain, err := a.open(in.secretsEnc)
		if err != nil {
			return nil, err
		}
		var sec map[string]string
		json.Unmarshal(plain, &sec)
		for k, v := range sec {
			out[k] = v
		}
	}
	return out, nil
}

// ---- checks ----

type appCheck struct {
	Code   string         `json:"code"`
	Params map[string]any `json:"params,omitempty"`
	Block  bool           `json:"block"`
}

// composeFor renders the compose file, adding the 4K runtime to every service if asked.
func composeFor(app appstore.App, runtime, access string) (string, error) {
	compose := app.Compose
	if access == "lan" { // the web port listens on every address instead of 127.0.0.1
		for _, p := range app.Ports {
			if p.HTTP {
				compose = strings.ReplaceAll(compose, "127.0.0.1:${"+p.Field+"}", "0.0.0.0:${"+p.Field+"}")
			}
		}
	}
	src := []byte(compose)
	if runtime == "" {
		return compose, nil
	}
	var doc struct {
		Services map[string]any `yaml:"services"`
	}
	if err := yamlUnmarshal(src, &doc); err != nil {
		return "", err
	}
	names := make([]string, 0, len(doc.Services))
	for n := range doc.Services {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		out, _, err := docker.SetRuntime(src, n, runtime)
		if err != nil {
			return "", err
		}
		src = out
	}
	return string(src), nil
}

// installChecks looks at the host before installing.
func (a *App) installChecks(ctx context.Context, host string, app appstore.App, values map[string]string, runtime *string) ([]appCheck, error) {
	var checks []appCheck
	var caps agentproto.Caps
	if err := a.call(ctx, host, "GET", "/v1/caps", nil, &caps); err != nil {
		return nil, err
	}
	if !contains(app.Arch, caps.Arch) {
		checks = append(checks, appCheck{Code: "app.check_arch", Params: map[string]any{"arch": caps.Arch, "supported": strings.Join(app.Arch, ", ")}, Block: true})
	}
	if caps.PageSize > 4096 {
		switch app.Verified.Arm64_16K {
		case "crash":
			st, err := a.compat(ctx, host)
			if err == nil && st.Runtime {
				*runtime = "krun"
				checks = append(checks, appCheck{Code: "app.check_16k_compat"})
			} else {
				checks = append(checks, appCheck{Code: "app.check_16k_crash", Block: true})
			}
		case "untested":
			checks = append(checks, appCheck{Code: "app.check_16k_untested"})
		}
	}
	var h metrics.Host
	if err := a.call(ctx, host, "GET", "/v1/metrics/host", nil, &h); err == nil {
		if need := int64(app.MemMB) << 20; need > 0 && h.MemAvail < need {
			checks = append(checks, appCheck{Code: "app.check_memory", Params: map[string]any{"need": app.MemMB, "avail": h.MemAvail >> 20}})
		}
		free := int64(-1)
		best := ""
		for _, d := range h.Disks {
			if strings.HasPrefix(appsRoot, d.Mount) && len(d.Mount) > len(best) {
				best, free = d.Mount, d.Total-d.Used
			}
		}
		if need := int64(app.DiskMB) << 20; free >= 0 && need > free {
			checks = append(checks, appCheck{Code: "app.check_disk", Params: map[string]any{"need": app.DiskMB, "free": free >> 20}, Block: true})
		}
	}
	var used []int
	if err := a.call(ctx, host, "GET", "/v1/net/listening", nil, &used); err == nil {
		for _, p := range app.Ports {
			n, _ := strconv.Atoi(values[p.Field])
			for _, u := range used {
				if u == n {
					checks = append(checks, appCheck{Code: "app.check_port", Params: map[string]any{"port": n, "name": p.Name}, Block: true})
				}
			}
		}
	}
	return checks, nil
}

// ---- kinds ----

type installParams struct {
	App     string            `json:"app"`
	Name    string            `json:"name"`
	Values  map[string]string `json:"values"`
	Sealed  string            `json:"sealed,omitempty"` // undo of remove: all values, sealed
	Runtime string            `json:"runtime,omitempty"`
	// how people reach it: "" / local (this server only), lan (web port on every address,
	// http://IP:port) or public (a site with a domain, made in the same step)
	Access string            `json:"access,omitempty"`
	Site   json.RawMessage   `json:"site,omitempty"`  // public: {domains, tls, options, …}; the target is filled in
	Sites  []json.RawMessage `json:"sites,omitempty"` // undo of remove: the sites that pointed at it
}

var accessModes = []string{"", "local", "lan", "public"}

func (p *installParams) normalize(a *App) (appstore.App, map[string]string, error) {
	app, err := findApp(p.App)
	if err != nil {
		return app, nil, err
	}
	if p.Name == "" {
		p.Name = app.ID
	}
	p.Name = strings.ToLower(p.Name)
	if !instanceRe.MatchString(p.Name) || !docker.ValidProjectName(p.Name) {
		return app, nil, errs.New(400, "app.bad_name")
	}
	in := p.Values
	if p.Sealed != "" {
		plain, err := a.open(unb64(p.Sealed))
		if err != nil {
			return app, nil, err
		}
		in = map[string]string{}
		json.Unmarshal(plain, &in)
	}
	vals, err := app.Values(in)
	if err != nil {
		var fe *appstore.FieldError
		if errors.As(err, &fe) {
			return app, nil, errs.New(400, fe.Code, "field", fe.Key)
		}
		return app, nil, err
	}
	return app, vals, nil
}

func maskedEnv(app appstore.App, env string) string {
	var out []string
	for _, l := range strings.Split(env, "\n") {
		k, _, ok := strings.Cut(l, "=")
		if ok && app.Secret(k) {
			l = k + "=••••••（自动生成或已填写，安装后可查看）"
		}
		out = append(out, l)
	}
	return strings.Join(out, "\n")
}

// splitSecrets separates password fields from the rest.
func splitSecrets(app appstore.App, vals map[string]string) (plain, secret map[string]string) {
	plain, secret = map[string]string{}, map[string]string{}
	for k, v := range vals {
		if app.Secret(k) {
			secret[k] = v
		} else {
			plain[k] = v
		}
	}
	return
}

func containerName(project, service, compose string) string {
	// an explicit container_name wins
	var doc struct {
		Services map[string]struct {
			ContainerName string `yaml:"container_name"`
		} `yaml:"services"`
	}
	if yamlUnmarshal([]byte(compose), &doc) == nil {
		if s, ok := doc.Services[service]; ok && s.ContainerName != "" {
			return strings.ReplaceAll(s.ContainerName, "${BERTH_APP}", project)
		}
	}
	return project + "-" + service + "-1"
}

// waitHealthy polls the app's health URL from the host.
func (a *App) waitHealthy(env change.Env, app appstore.App, vals map[string]string) bool {
	if app.Health.Port == 0 {
		return true
	}
	hostPort := ""
	for _, p := range app.Ports {
		if p.Port == app.Health.Port {
			hostPort = vals[p.Field]
		}
	}
	if hostPort == "" {
		return true
	}
	url := "http://127.0.0.1:" + hostPort + app.Health.Path
	deadline := time.Now().Add(appHealthWait)
	for time.Now().Before(deadline) && env.Ctx.Err() == nil {
		var r struct {
			Status int `json:"status"`
		}
		if a.call(env.Ctx, env.Host, "POST", "/v1/probe/http", map[string]string{"url": url}, &r) == nil && r.Status > 0 && r.Status < 500 {
			env.Log("health check %s: HTTP %d", url, r.Status)
			return true
		}
		time.Sleep(3 * time.Second)
	}
	return false
}

func init() {
	routeHooks = append(routeHooks, func(a *App, mux *http.ServeMux) {
		mux.HandleFunc("GET /api/v1/store/apps", a.authed("store.install", a.handleStoreApps))
		mux.HandleFunc("GET /api/v1/store/apps/{id}", a.authed("store.install", a.handleStoreApp))
		mux.HandleFunc("GET /api/v1/hosts/{host}/apps", a.authed("containers.view", a.handleInstalledApps))
		mux.HandleFunc("POST /api/v1/hosts/{host}/apps/{name}/secrets", a.authed("store.install", a.handleAppSecrets))
		mux.HandleFunc("POST /api/v1/hosts/{host}/store/check", a.authed("store.install", a.handleInstallCheck))
	})

	props := map[string]change.Prop{
		"app": {Type: "string", Desc: "catalog id"}, "name": {Type: "string", Desc: "instance name (default: the app id)"},
		"values": {Type: "object", Desc: "form fields; password fields are generated when empty"},
		"access": {Type: "string", Enum: accessModes, Desc: "local (default): this server only; lan: web port on every address; public: also make a site"},
		"site":   {Type: "object", Desc: "with access=public: {domains:[…], tls:{mode:auto}, options:{…}}; the target is the app"},
		"sites":  {Type: "array", Desc: "internal (undo of remove)"},
	}
	change.Register(change.Kind{
		Name: "app.install", Perm: "store.install", Scope: "containers:write", Title: "kind.app.install",
		Params: change.ParamSchema{Props: props, Required: []string{"app"}}, Redact: []string{"values", "sealed"},
		CLI: "berth app install {{.app}}", Heavy: true, MemMB: 256,
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			a := svc(env)
			var p installParams
			json.Unmarshal(raw, &p)
			app, vals, err := p.normalize(a)
			if err != nil {
				return change.Plan{}, err
			}
			if _, err := a.appInstance(env.Host, p.Name); err == nil {
				return change.Plan{}, errs.New(409, "app.exists", "name", p.Name)
			}
			if _, err := a.findProject(env.Ctx, env.Host, p.Name); err == nil {
				return change.Plan{}, errs.New(409, "compose.exists", "name", p.Name)
			}
			rt := p.Runtime
			checks, err := a.installChecks(env.Ctx, env.Host, app, vals, &rt)
			if err != nil {
				return change.Plan{}, err
			}
			for _, c := range checks {
				if c.Block {
					return change.Plan{}, errs.New(409, c.Code, flattenAny(c.Params)...)
				}
			}
			compose, err := composeFor(app, rt, p.Access)
			if err != nil {
				return change.Plan{}, err
			}
			dir := docker.StacksDir + "/" + p.Name
			envText := app.Env(p.Name, appsRoot+"/"+p.Name, vals)
			plan := change.Plan{Summary: "plan.app.install", SummaryP: map[string]any{"app": app.Name["zh-CN"], "name": p.Name, "version": app.Version},
				Risk: change.RiskLow, Undoable: true,
				Files:    []change.FileDiff{{Path: dir + "/compose.yaml", After: compose}, {Path: dir + "/.env", After: maskedEnv(app, envText)}},
				Commands: []string{"docker compose -p " + p.Name + " up -d"},
				Affected: []change.Object{projObj(p.Name, env.Host)}}
			// data left by an earlier install of the same name is reused as it is: a database
			// keeps its old passwords, so say so before the new ones silently do not work
			var left []json.RawMessage
			if a.call(env.Ctx, env.Host, "POST", "/v1/files/list", map[string]any{"root": appsRoot + "/" + p.Name, "path": appsRoot + "/" + p.Name, "hidden": true}, &left) == nil && len(left) > 0 {
				checks = append(checks, appCheck{Code: "app.check_data_left", Params: map[string]any{"dir": appsRoot + "/" + p.Name}})
			}
			for _, c := range checks { // warnings ride along as commands the preview shows
				plan.Commands = append(plan.Commands, "# "+T("zh-CN", c.Code, c.Params))
			}
			if !contains(accessModes, p.Access) {
				return change.Plan{}, errs.New(400, "app.bad_access")
			}
			if p.Access == "lan" {
				plan.Commands = append(plan.Commands, "# "+T("zh-CN", "app.access_lan_note", nil))
			}
			if p.Access == "public" && len(p.Sites) == 0 { // (an undo brings its sites back in Sites)
				siteRaw, err := appSite(app, p, compose)
				if err != nil {
					return change.Plan{}, err
				}
				// the container does not exist yet: check the domains now, build the site after
				st, err := parseSite(siteRaw)
				if err != nil {
					return change.Plan{}, err
				}
				st.ID = -1
				if err := a.checkDomains(st); err != nil {
					return change.Plan{}, err
				}
				plan.Summary = "plan.app.install_site"
				plan.SummaryP["domain"] = st.Primary()
				plan.Commands = append(plan.Commands, fmt.Sprintf("site %s → %s:%d (HTTPS %s)", strings.Join(st.Domains, ", "), st.Target.Container, st.Target.Port, st.TLS.Mode))
			}
			return plan, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			a := svc(env)
			var p installParams
			json.Unmarshal(raw, &p)
			app, vals, err := p.normalize(a)
			if err != nil {
				return nil, nil, err
			}
			rt := p.Runtime
			if _, err := a.installChecks(env.Ctx, env.Host, app, vals, &rt); err != nil {
				return nil, nil, err
			}
			compose, err := composeFor(app, rt, p.Access)
			if err != nil {
				return nil, nil, err
			}
			envText := app.Env(p.Name, appsRoot+"/"+p.Name, vals)
			env.Log("::step 0 files")
			if err := a.call(env.Ctx, env.Host, "PUT", "/v1/compose/projects/"+p.Name+"/files", map[string]any{"compose": compose, "env": envText, "create": true, "data": dataSpec(&app, p.Name)}, nil); err != nil {
				return nil, nil, err
			}
			plain, secret := splitSecrets(app, vals)
			pj, _ := json.Marshal(plain)
			sj, _ := json.Marshal(secret)
			senc, err := a.seal(sj)
			if err != nil {
				return nil, nil, err
			}
			now := time.Now().Unix()
			if _, err := a.db.Exec(`INSERT INTO apps(name, host, app_id, version, vals, secrets_enc, runtime, access, installed_at, updated_at) VALUES(?,?,?,?,?,?,?,?,?,?)`,
				p.Name, env.Host, app.ID, app.Version, string(pj), senc, rt, p.Access, now, now); err != nil {
				return nil, nil, err
			}
			a.relate(env.Host, change.Object{Type: "host", ID: env.Host}, projObj(p.Name, env.Host), "manages", "module")
			env.Log("::step 1 up")
			if err := agentOp(env, "/v1/compose/projects/"+p.Name+"/up", nil); err != nil {
				return nil, nil, err
			}
			env.Log("::step 2 health")
			healthy := a.waitHealthy(env, app, vals)
			if !healthy {
				env.Log("::note app.unhealthy")
			}
			res := map[string]any{"name": p.Name, "healthy": healthy}
			if app.Site.Service != "" {
				res["site"] = map[string]any{"container": containerName(p.Name, app.Site.Service, compose), "port": app.Site.Port}
			}
			k, _ := change.Get("site.create")
			if p.Access == "public" && len(p.Sites) == 0 {
				env.Log("::step 3 site")
				siteRaw, err := appSite(app, p, compose)
				if err == nil {
					var out any
					if out, _, err = k.Apply(env, siteRaw); err == nil {
						res["site_created"] = out
					}
				}
				if err != nil { // the app runs; say why the site is missing instead of failing it all
					env.Log("site: %v", err)
					res["site_error"] = errs.From(err).Code
				}
			}
			for _, sr := range p.Sites { // undo of remove: the sites come back too
				if _, _, err := k.Apply(env, sr); err != nil {
					env.Log("site: %v", err)
				}
			}
			u, _ := json.Marshal(map[string]any{"name": p.Name})
			return res, &change.UndoRecipe{Kind: "app.remove", Params: u}, nil
		},
	})

	change.Register(change.Kind{
		Name: "app.upgrade", Perm: "store.install", Scope: "containers:write", Title: "kind.app.upgrade",
		Params: change.ParamSchema{Props: map[string]change.Prop{"name": {Type: "string"}}, Required: []string{"name"}},
		CLI:    "berth app upgrade {{.name}}", Heavy: true, MemMB: 256,
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			a := svc(env)
			var p struct{ Name string }
			json.Unmarshal(raw, &p)
			in, err := a.appInstance(env.Host, p.Name)
			if err != nil {
				return change.Plan{}, err
			}
			app, err := findApp(in.AppID)
			if err != nil {
				return change.Plan{}, err
			}
			if app.Version == in.Version {
				return change.Plan{}, errs.New(409, "app.up_to_date", "version", in.Version)
			}
			cur, err := a.composeFiles(env.Ctx, env.Host, p.Name)
			if err != nil {
				return change.Plan{}, err
			}
			next, err := composeFor(app, in.Runtime, in.Access)
			if err != nil {
				return change.Plan{}, err
			}
			return change.Plan{Summary: "plan.app.upgrade", SummaryP: map[string]any{"name": p.Name, "from": in.Version, "to": app.Version}, Risk: change.RiskMedium, Undoable: true,
				Files:    []change.FileDiff{{Path: cur.File, Before: cur.Compose, After: next}},
				Commands: []string{"docker compose -p " + p.Name + " pull", "docker compose -p " + p.Name + " up -d"},
				Affected: []change.Object{projObj(p.Name, env.Host)}}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			a := svc(env)
			var p struct {
				Name    string
				Compose string // undo: the previous compose file
				Version string
			}
			json.Unmarshal(raw, &p)
			in, err := a.appInstance(env.Host, p.Name)
			if err != nil {
				return nil, nil, err
			}
			app, err := findApp(in.AppID)
			if err != nil {
				return nil, nil, err
			}
			cur, err := a.composeFiles(env.Ctx, env.Host, p.Name)
			if err != nil {
				return nil, nil, err
			}
			next, version := p.Compose, p.Version
			if next == "" {
				if next, err = composeFor(app, in.Runtime, in.Access); err != nil {
					return nil, nil, err
				}
				version = app.Version
			}
			if err := a.call(env.Ctx, env.Host, "PUT", "/v1/compose/projects/"+p.Name+"/files", map[string]any{"compose": next, "env": cur.Env, "data": dataSpec(&app, p.Name)}, nil); err != nil {
				return nil, nil, err
			}
			if err := agentOp(env, "/v1/compose/projects/"+p.Name+"/pull", nil); err != nil {
				return nil, nil, err
			}
			if err := agentOp(env, "/v1/compose/projects/"+p.Name+"/up", nil); err != nil {
				return nil, nil, err
			}
			a.db.Exec(`UPDATE apps SET version=?, updated_at=? WHERE host=? AND name=?`, version, time.Now().Unix(), env.Host, p.Name)
			u, _ := json.Marshal(map[string]string{"name": p.Name, "compose": cur.Compose, "version": in.Version})
			return map[string]string{"version": version}, &change.UndoRecipe{Kind: "app.upgrade", Params: u}, nil
		},
	})

	change.Register(change.Kind{
		Name: "app.remove", Perm: "store.install", Scope: "containers:write", Title: "kind.app.remove",
		Params: change.ParamSchema{Props: map[string]change.Prop{"name": {Type: "string"}}, Required: []string{"name"}},
		CLI:    "berth app rm {{.name}}", Heavy: true,
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			a := svc(env)
			var p struct{ Name string }
			json.Unmarshal(raw, &p)
			in, err := a.appInstance(env.Host, p.Name)
			if err != nil {
				return change.Plan{}, err
			}
			plan := change.Plan{Summary: "plan.app.remove", SummaryP: map[string]any{"name": p.Name, "data": appsRoot + "/" + p.Name}, Risk: change.RiskHigh, Undoable: true,
				Commands: []string{"docker compose -p " + p.Name + " down"}, Affected: []change.Object{projObj(p.Name, env.Host)}}
			for _, r := range a.related(env.Host, "compose_project", in.Name) {
				plan.Affected = append(plan.Affected, r.From)
			}
			for _, st := range a.sitesForProject(env.Host, in.Name) {
				plan.Commands = append(plan.Commands, "# "+T("zh-CN", "app.remove_site_note", map[string]any{"domain": st.Primary()}))
				plan.Affected = append(plan.Affected, siteObj(st, env.Host))
			}
			return plan, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			a := svc(env)
			var p struct{ Name string }
			json.Unmarshal(raw, &p)
			in, err := a.appInstance(env.Host, p.Name)
			if err != nil {
				return nil, nil, err
			}
			all, err := a.appSecrets(in)
			if err != nil {
				return nil, nil, err
			}
			// sites pointing at it would only show errors: they go, and come back with undo
			var sites []json.RawMessage
			for _, st := range a.sitesForProject(env.Host, in.Name) {
				k, _ := change.Get("site.delete")
				ref, _ := json.Marshal(map[string]int64{"id": st.ID})
				if _, undo, err := k.Apply(env, ref); err == nil && undo != nil {
					sites = append(sites, undo.Params)
				} else if err != nil {
					env.Log("site %s: %v", st.Primary(), err)
				}
			}
			if err := agentOp(env, "/v1/compose/projects/"+p.Name+"/remove", nil); err != nil {
				return nil, nil, err
			}
			a.db.Exec(`DELETE FROM apps WHERE host=? AND name=?`, env.Host, p.Name)
			a.unrelate(env.Host, change.Object{Type: "host", ID: env.Host}, projObj(p.Name, env.Host), "manages")
			aj, _ := json.Marshal(all)
			sealed, err := a.seal(aj)
			if err != nil {
				return nil, nil, err
			}
			u, _ := json.Marshal(installParams{App: in.AppID, Name: in.Name, Sealed: base64.StdEncoding.EncodeToString(sealed), Runtime: in.Runtime, Access: in.Access, Sites: sites})
			return nil, &change.UndoRecipe{Kind: "app.install", Params: u}, nil
		},
	})
}

func flattenAny(m map[string]any) []any {
	var out []any
	for k, v := range m {
		out = append(out, k, v)
	}
	return out
}

// ---- API ----

type appView struct {
	appstore.App
	Installed int `json:"installed"`
}

func (a *App) handleStoreApps(w http.ResponseWriter, r *http.Request) {
	list, err := appCatalog()
	if err != nil {
		a.fail(w, r, err)
		return
	}
	counts := map[string]int{}
	rows, _ := a.db.Query(`SELECT app_id, COUNT(*) FROM apps GROUP BY app_id`)
	if rows != nil {
		for rows.Next() {
			var id string
			var n int
			rows.Scan(&id, &n)
			counts[id] = n
		}
		rows.Close()
	}
	out := make([]appView, 0, len(list))
	for _, x := range list {
		x.Compose = ""
		out = append(out, appView{App: x, Installed: counts[x.ID]})
	}
	writeJSON(w, 200, map[string]any{"items": out, "categories": appstore.Categories})
}

func (a *App) handleStoreApp(w http.ResponseWriter, r *http.Request) {
	app, err := findApp(r.PathValue("id"))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"app": app, "compose": app.Compose})
}

// POST /api/v1/hosts/{host}/store/check {app, values}: the checks before the form is sent.
func (a *App) handleInstallCheck(w http.ResponseWriter, r *http.Request) {
	var p installParams
	if err := readJSON(r, &p); err != nil {
		a.fail(w, r, err)
		return
	}
	app, vals, err := p.normalize(a)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	rt := ""
	checks, err := a.installChecks(r.Context(), r.PathValue("host"), app, vals, &rt)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	type shown struct {
		appCheck
		Message string `json:"message"`
	}
	out := []shown{}
	for _, c := range checks {
		out = append(out, shown{c, T(a.locale(r), c.Code, c.Params)})
	}
	writeJSON(w, 200, map[string]any{"checks": out, "runtime": rt})
}

func (a *App) handleInstalledApps(w http.ResponseWriter, r *http.Request) {
	list, err := a.appInstances(r.PathValue("host"))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"items": list})
}

// POST …/apps/{name}/secrets: the generated passwords (needs a fresh sign-in, recorded).
func (a *App) handleAppSecrets(w http.ResponseWriter, r *http.Request) {
	if err := a.requireReauth(r); err != nil {
		a.fail(w, r, err)
		return
	}
	host := r.PathValue("host")
	in, err := a.appInstance(host, r.PathValue("name"))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	all, err := a.appSecrets(in)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	_, _ = a.pipe.Record(currentActor(r), host, "app.secrets_view", map[string]any{"name": in.Name}, change.Plan{
		Summary: "plan.app.secrets_view", SummaryP: map[string]any{"name": in.Name}, Risk: change.RiskMedium, Affected: []change.Object{projObj(in.Name, host)}})
	writeJSON(w, 200, map[string]any{"values": all})
}

func yamlUnmarshal(b []byte, v any) error { return yaml.Unmarshal(b, v) }

type siteRef struct {
	ID      int64  `json:"id"`
	Domain  string `json:"domain"`
	Enabled bool   `json:"enabled"`
}

// sitesForProject: sites whose target is one of a compose project's containers.
func (a *App) sitesForProject(host, project string) []Site {
	all, _ := a.sites(host)
	var out []Site
	for _, st := range all {
		c := st.Target.Container
		if st.Target.Type == "container" && (c == project || strings.HasPrefix(c, project+"-")) {
			out = append(out, st)
		}
	}
	return out
}

// appSite: the site the install wizard asked for, aimed at the app's web container.
func appSite(app appstore.App, p installParams, compose string) (json.RawMessage, error) {
	if app.Site.Service == "" {
		return nil, errs.New(400, "app.no_site")
	}
	var site map[string]any
	if len(p.Site) == 0 || json.Unmarshal(p.Site, &site) != nil {
		return nil, errs.New(400, "app.site_needs_domain")
	}
	if d, _ := site["domains"].([]any); len(d) == 0 {
		return nil, errs.New(400, "app.site_needs_domain")
	}
	site["target"] = map[string]any{"type": "container", "container": containerName(p.Name, app.Site.Service, compose), "port": app.Site.Port}
	if _, ok := site["tls"]; !ok {
		site["tls"] = map[string]any{"mode": "auto"}
	}
	return json.Marshal(site)
}
