package panel

// Native projects with releases (R1): the panel keeps each project's settings and secrets,
// asks the agent to build and start a release, then moves the project's sites to the new
// port and only after that lets the agent stop the old one, so visitors see no gap.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/wangsirco/berth/internal/agent/system"
	"github.com/wangsirco/berth/internal/change"
	"github.com/wangsirco/berth/internal/errs"
)

type nativeProject struct {
	Mode string `json:"mode"` // always "release"
	system.NativeSpec
	EnvEnc string `json:"env_enc,omitempty"` // sealed JSON object of environment variables
	Port   int    `json:"port,omitempty"`    // where it serves now
	// Current is the release that serves. (It was stored as "release" before R3, which hid the
	// spec's own "release" command — the one run before start — so that command was lost.)
	Current  string `json:"current,omitempty"`
	Deployed int64  `json:"deployed,omitempty"`
	HookEnc  string `json:"hook_enc,omitempty"` // the push webhook's secret (sealed)
}

var releaseIDRe = regexp.MustCompile(`^[0-9]{8}-[0-9]{6}$`)

// fixOld moves a release id stored under the old key to Current.
func (p *nativeProject) fixOld() {
	if p.Current == "" && releaseIDRe.MatchString(p.NativeSpec.Release) {
		p.Current, p.NativeSpec.Release = p.NativeSpec.Release, ""
	}
	if p.Shared == nil {
		p.Shared = []string{}
	}
}

func (a *App) nativeProjects(host string) []nativeProject {
	out := []nativeProject{}
	rows, err := a.db.Query(`SELECT spec FROM projects WHERE host=? ORDER BY name`, host)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var s string
		rows.Scan(&s)
		var p nativeProject
		if json.Unmarshal([]byte(s), &p) == nil && p.Mode == "release" {
			p.fixOld()
			out = append(out, p)
		}
	}
	return out
}

func (a *App) nativeProject(host, name string) (nativeProject, bool) {
	var s string
	if a.db.QueryRow(`SELECT spec FROM projects WHERE host=? AND name=?`, host, name).Scan(&s) != nil {
		return nativeProject{}, false
	}
	var p nativeProject
	if json.Unmarshal([]byte(s), &p) != nil || p.Mode != "release" {
		return nativeProject{}, false
	}
	p.fixOld()
	return p, true
}

func (a *App) saveNative(host string, p nativeProject, by string) error {
	p.Mode = "release"
	b, _ := json.Marshal(p)
	_, err := a.db.Exec(`INSERT INTO projects(host, name, spec, created_by, created_at) VALUES(?,?,?,?,?) ON CONFLICT(host, name) DO UPDATE SET spec=excluded.spec`,
		host, p.Name, string(b), by, time.Now().Unix())
	return err
}

func (a *App) nativeEnv(p nativeProject) map[string]string {
	env := map[string]string{}
	if p.EnvEnc == "" {
		return env
	}
	raw, err := base64.StdEncoding.DecodeString(p.EnvEnc)
	if err != nil {
		return env
	}
	plain, err := a.open(raw)
	if err != nil {
		return env
	}
	json.Unmarshal(plain, &env)
	return env
}

func (a *App) sealEnv(env map[string]string) (string, error) {
	b, _ := json.Marshal(env)
	enc, err := a.seal(b)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(enc), nil
}

// projectPorts feeds the Caddy renderer.
func (a *App) projectPorts(host string) map[string]int {
	m := map[string]int{}
	for _, p := range a.nativeProjects(host) {
		if p.Port > 0 {
			m[p.Name] = p.Port
		}
	}
	return m
}

func (a *App) sitesOfProject(host, name string) []int64 {
	out := []int64{}
	sites, _ := a.sites(host)
	for _, s := range sites {
		if s.Target.Type == "project" && s.Target.Project == name {
			out = append(out, s.ID)
		}
	}
	return out
}

// switchTo records where the project now serves, moves its sites there and stops the old port.
func (a *App) switchTo(env change.Env, p nativeProject, res system.DeployResult) error {
	p.Port, p.Current, p.Deployed = res.Port, res.Release, time.Now().Unix()
	if err := a.saveNative(env.Host, p, env.ActorName); err != nil {
		return err
	}
	if ids := a.sitesOfProject(env.Host, p.Name); len(ids) > 0 {
		env.Log("moving %d site(s) to port %d", len(ids), res.Port)
		if err := a.applyEdge(env); err != nil {
			return err
		}
	}
	if res.OldPort > 0 {
		env.Log("stopping the previous release on port %d", res.OldPort)
		a.call(env.Ctx, env.Host, "POST", "/v1/native/retire", map[string]any{"name": p.Name, "port": res.OldPort}, nil)
	}
	return nil
}

type nativeParams struct {
	system.NativeSpec
	Env      map[string]string `json:"env,omitempty"`
	EnvMerge bool              `json:"env_merge,omitempty"` // change only the keys given; an empty value removes one
}

func init() {
	routeHooks = append(routeHooks, func(a *App, mux *http.ServeMux) {
		mux.HandleFunc("GET /api/v1/hosts/{host}/native", a.authed("sites.view", func(w http.ResponseWriter, r *http.Request) {
			host := r.PathValue("host")
			type view struct {
				nativeProject
				EnvKeys []string `json:"env_keys"`
				Sites   []int64  `json:"sites"`
				Active  bool     `json:"active"`
			}
			out := []view{}
			for _, p := range a.nativeProjects(host) {
				keys := []string{}
				for k := range a.nativeEnv(p) {
					keys = append(keys, k)
				}
				sort.Strings(keys)
				v := view{nativeProject: p, EnvKeys: keys, Sites: a.sitesOfProject(host, p.Name)}
				v.EnvEnc, v.HookEnc = "", ""
				var st system.NativeStatus
				if a.call(r.Context(), host, "GET", "/v1/native/"+p.Name, nil, &st) == nil {
					v.Active = st.Active
				}
				out = append(out, v)
			}
			writeJSON(w, 200, map[string]any{"items": out})
		}))
		mux.HandleFunc("GET /api/v1/hosts/{host}/native/{name}", a.authed("sites.view", func(w http.ResponseWriter, r *http.Request) {
			host, name := r.PathValue("host"), r.PathValue("name")
			p, ok := a.nativeProject(host, name)
			if !ok {
				a.fail(w, r, errs.New(404, "project.not_found"))
				return
			}
			var st system.NativeStatus
			if err := a.call(r.Context(), host, "GET", "/v1/native/"+name, nil, &st); err != nil {
				a.fail(w, r, err)
				return
			}
			keys := []string{}
			for k := range a.nativeEnv(p) {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			p.EnvEnc, p.HookEnc = "", ""
			writeJSON(w, 200, map[string]any{"project": p, "status": st, "env_keys": keys, "sites": a.sitesOfProject(host, name)})
		}))
		// the new-project wizard: what the source needs and the commands Berth suggests
		mux.HandleFunc("POST /api/v1/hosts/{host}/native/check", a.authed("sites.manage", func(w http.ResponseWriter, r *http.Request) {
			var in struct {
				Spec system.NativeSpec `json:"spec"`
			}
			if err := readJSON(r, &in); err != nil {
				a.fail(w, r, err)
				return
			}
			var rep system.NodeReport
			if err := a.call(r.Context(), r.PathValue("host"), "POST", "/v1/native/check", map[string]any{"spec": in.Spec}, &rep); err != nil {
				a.fail(w, r, err)
				return
			}
			writeJSON(w, 200, rep)
		}))
	})

	reg := func(k change.Kind) {
		k.Perm, k.Scope, k.Title = "sites.manage", "sites:write", "kind."+k.Name
		// every native change is about one project: name it, so the audit log and "AI is working here" find it
		plan := k.Plan
		k.Plan = func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			p, err := plan(env, raw)
			if err == nil && len(p.Affected) == 0 {
				var x struct{ Name string }
				json.Unmarshal(raw, &x)
				if x.Name != "" {
					p.Affected = []change.Object{{Type: "project", ID: x.Name, Name: x.Name, Host: env.Host}}
				}
			}
			return p, err
		}
		change.Register(k)
	}
	specProps := map[string]change.Prop{
		"name": {Type: "string"}, "kind": {Type: "string", Enum: []string{"node", "java", "go", "python"}, Desc: "language, default node"}, "version": {Type: "string", Desc: "runtime version: Node 24, Java 21 (Temurin), Go 1.24, Python 3.12"},
		"source":  {Type: "object", Desc: `{"type":"dir","path":"/srv/code/web"} or {"type":"git","url":"https://…","branch":"main"}`},
		"install": {Type: "string"}, "build": {Type: "string"}, "release": {Type: "string", Desc: "runs before the new release starts, e.g. migrations"},
		"start": {Type: "string"}, "health": {Type: "string", Desc: "path checked before switching, default /"}, "shared": {Type: "array", Desc: "paths kept across releases"},
		"mem_mb": {Type: "integer"}, "cpus": {Type: "number"}, "build_mem_mb": {Type: "integer"}, "wait_secs": {Type: "integer"}, "workers": {Type: "array", Desc: `long-running commands beside the web process, e.g. [{"name":"celery","command":"celery -A app worker"}]`},
		"env":       {Type: "object", Desc: "environment variables (stored encrypted); omit to keep the current ones"},
		"env_merge": {Type: "boolean", Desc: "with env: change only these keys (an empty value removes one)"},
	}
	reg(change.Kind{Name: "native.set", CLI: "berth native set {{.name}}", Redact: []string{"env"},
		Params: change.ParamSchema{Props: specProps, Required: []string{"name", "source", "start"}},
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			var p nativeParams
			json.Unmarshal(raw, &p)
			if p.Kind == "" {
				p.Kind = "node"
			}
			if err := p.NativeSpec.Validate(); err != nil {
				return change.Plan{}, err
			}
			a := svc(env)
			_, exists := a.nativeProject(env.Host, p.Name)
			if !exists {
				if _, legacy := a.appProject(env.Host, p.Name); legacy {
					return change.Plan{}, errs.New(409, "native.name_taken", "name", p.Name)
				}
			}
			sum := "plan.native.create"
			if exists {
				sum = "plan.native.update"
			}
			cmds := []string{"source: " + p.Source.Type + " " + p.Source.Path + p.Source.URL, "install: " + p.Install, "build: " + p.Build, "start: " + p.Start}
			if p.Release != "" {
				cmds = append(cmds, "before start: "+p.Release)
			}
			if p.Env != nil {
				keys := []string{}
				for k := range p.Env {
					keys = append(keys, k)
				}
				sort.Strings(keys)
				cmds = append(cmds, "environment: "+strings.Join(keys, ", ")+" (encrypted)")
			}
			return change.Plan{Summary: sum, SummaryP: map[string]any{"name": p.Name}, Risk: change.RiskLow, Undoable: true, Commands: cmds}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			a := svc(env)
			var p nativeParams
			json.Unmarshal(raw, &p)
			if p.Kind == "" {
				p.Kind = "node"
			}
			if err := p.NativeSpec.Validate(); err != nil {
				return nil, nil, err
			}
			old, exists := a.nativeProject(env.Host, p.Name)
			rec := nativeProject{NativeSpec: p.NativeSpec, EnvEnc: old.EnvEnc, Port: old.Port, Current: old.Current, Deployed: old.Deployed, HookEnc: old.HookEnc}
			if p.Env != nil {
				if p.EnvMerge {
					merged := a.nativeEnv(old)
					for k, v := range p.Env {
						if v == "" {
							delete(merged, k)
						} else {
							merged[k] = v
						}
					}
					p.Env = merged
				}
				enc, err := a.sealEnv(p.Env)
				if err != nil {
					return nil, nil, err
				}
				rec.EnvEnc = enc
			}
			if err := a.saveNative(env.Host, rec, env.ActorName); err != nil {
				return nil, nil, err
			}
			// workers changed on a deployed project: apply them now
			if exists && old.Current != "" && !sameWorkers(old.Workers, p.Workers) {
				if err := a.streamAgent(env, env.Host, "/v1/native/workers", map[string]any{"spec": p.NativeSpec}, nil); err != nil {
					return nil, nil, err
				}
			}
			if exists {
				prev, _ := json.Marshal(nativeParams{NativeSpec: old.NativeSpec, Env: a.nativeEnv(old)})
				return nil, &change.UndoRecipe{Kind: "native.set", Params: prev}, nil
			}
			u, _ := json.Marshal(map[string]string{"name": p.Name})
			return nil, &change.UndoRecipe{Kind: "native.delete", Params: u}, nil
		}})

	reg(change.Kind{Name: "native.deploy", CLI: "berth native deploy {{.name}}", Heavy: true, MemMB: 400,
		Params: change.ParamSchema{Props: map[string]change.Prop{"name": {Type: "string"}}, Required: []string{"name"}},
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			var in struct{ Name string }
			json.Unmarshal(raw, &in)
			p, ok := svc(env).nativeProject(env.Host, in.Name)
			if !ok {
				return change.Plan{}, errs.New(404, "project.not_found")
			}
			cmds := []string{"fetch the code (" + p.Source.Type + ") into a new release folder", "check compatibility; stop here if something blocks"}
			if p.Install != "" {
				cmds = append(cmds, "$ "+p.Install+"   (as site-"+p.Name+", in a memory-limited scope)")
			}
			if p.Build != "" {
				cmds = append(cmds, "$ "+p.Build)
			}
			if p.NativeSpec.Release != "" {
				cmds = append(cmds, "$ "+p.NativeSpec.Release+"   (before the new release starts)")
			}
			cmds = append(cmds, "start the new release on the free port, wait for "+p.Health+" to answer, then switch; the current release keeps serving until then")
			return change.Plan{Summary: "plan.native.deploy", SummaryP: map[string]any{"name": p.Name}, Risk: change.RiskMedium, Undoable: p.Current != "", Commands: cmds}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			a := svc(env)
			var in struct{ Name string }
			json.Unmarshal(raw, &in)
			p, ok := a.nativeProject(env.Host, in.Name)
			if !ok {
				return nil, nil, errs.New(404, "project.not_found")
			}
			prev := p.Current
			var res system.DeployResult
			if err := a.streamAgent(env, env.Host, "/v1/native/deploy", map[string]any{"spec": p.NativeSpec, "env": a.nativeEnv(p)}, &res); err != nil {
				return nil, nil, err
			}
			if err := a.switchTo(env, p, res); err != nil {
				return nil, nil, err
			}
			if prev == "" {
				return res, nil, nil
			}
			u, _ := json.Marshal(map[string]string{"name": p.Name, "release": prev})
			return res, &change.UndoRecipe{Kind: "native.activate", Params: u}, nil
		}})

	reg(change.Kind{Name: "native.activate", CLI: "berth native activate {{.name}} {{.release}}", Heavy: true, MemMB: 100,
		Params: change.ParamSchema{Props: map[string]change.Prop{"name": {Type: "string"}, "release": {Type: "string"}}, Required: []string{"name", "release"}},
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			var in struct{ Name, Release string }
			json.Unmarshal(raw, &in)
			p, ok := svc(env).nativeProject(env.Host, in.Name)
			if !ok {
				return change.Plan{}, errs.New(404, "project.not_found")
			}
			return change.Plan{Summary: "plan.native.activate", SummaryP: map[string]any{"name": p.Name, "release": in.Release, "current": p.Current}, Risk: change.RiskMedium, Undoable: true,
				Commands: []string{"start release " + in.Release + " on the free port, wait for " + p.Health + ", then switch (no rebuild)"}}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			a := svc(env)
			var in struct{ Name, Release string }
			json.Unmarshal(raw, &in)
			p, ok := a.nativeProject(env.Host, in.Name)
			if !ok {
				return nil, nil, errs.New(404, "project.not_found")
			}
			prev := p.Current
			var res system.DeployResult
			if err := a.streamAgent(env, env.Host, "/v1/native/activate", map[string]any{"spec": p.NativeSpec, "release": in.Release}, &res); err != nil {
				return nil, nil, err
			}
			if err := a.switchTo(env, p, res); err != nil {
				return nil, nil, err
			}
			u, _ := json.Marshal(map[string]string{"name": p.Name, "release": prev})
			return res, &change.UndoRecipe{Kind: "native.activate", Params: u}, nil
		}})

	reg(change.Kind{Name: "native.delete", CLI: "berth native delete {{.name}}",
		Params: change.ParamSchema{Props: map[string]change.Prop{"name": {Type: "string"}}, Required: []string{"name"}},
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			var in struct{ Name string }
			json.Unmarshal(raw, &in)
			a := svc(env)
			if _, ok := a.nativeProject(env.Host, in.Name); !ok {
				return change.Plan{}, errs.New(404, "project.not_found")
			}
			if ids := a.sitesOfProject(env.Host, in.Name); len(ids) > 0 {
				return change.Plan{}, errs.New(409, "native.has_sites", "n", len(ids))
			}
			return change.Plan{Summary: "plan.native.delete", SummaryP: map[string]any{"name": in.Name}, Risk: change.RiskMedium, Undoable: true,
				Commands: []string{"stop berth-proj-" + in.Name + "@*.service and remove it", "remove /etc/berth/projects/" + in.Name + ".env",
					system.NativeRoot + "/" + in.Name + " stays (releases and data); delete it in Files if you no longer need it"}}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			a := svc(env)
			var in struct{ Name string }
			json.Unmarshal(raw, &in)
			old, _ := a.nativeProject(env.Host, in.Name)
			if err := a.call(env.Ctx, env.Host, "POST", "/v1/native/remove", map[string]string{"name": in.Name}, nil); err != nil {
				return nil, nil, err
			}
			a.db.Exec(`DELETE FROM projects WHERE host=? AND name=?`, env.Host, in.Name)
			u, _ := json.Marshal(nativeParams{NativeSpec: old.NativeSpec, Env: a.nativeEnv(old)})
			return nil, &change.UndoRecipe{Kind: "native.set", Params: u}, nil
		}})
}

func sameWorkers(a, b []system.NativeWorker) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}

// ---- push to deploy: a Git webhook (GitHub, Gitea, GitLab) deploys the pushed branch ----

func init() {
	routeHooks = append(routeHooks, func(a *App, mux *http.ServeMux) {
		mux.HandleFunc("GET /api/v1/hosts/{host}/native/{name}/webhook", a.authed("sites.manage", func(w http.ResponseWriter, r *http.Request) {
			host, name := r.PathValue("host"), r.PathValue("name")
			p, ok := a.nativeProject(host, name)
			if !ok {
				a.fail(w, r, errs.New(404, "project.not_found"))
				return
			}
			if p.Source.Type != "git" {
				a.fail(w, r, errs.New(409, "native.hook_git_only"))
				return
			}
			secret, err := a.openHook(p)
			if p.HookEnc == "" || err != nil {
				secret = []byte(randomPassword(32))
				enc, err := a.seal(secret)
				if err != nil {
					a.fail(w, r, err)
					return
				}
				p.HookEnc = base64.StdEncoding.EncodeToString(enc)
				if err := a.saveNative(host, p, "webhook"); err != nil {
					a.fail(w, r, err)
					return
				}
			}
			writeJSON(w, 200, map[string]string{"url": panelBase(r) + "/hooks/native/" + host + "/" + name, "secret": string(secret), "content_type": "application/json",
				"branch": firstNonEmpty(p.Source.Branch, "main")})
		}))
		mux.HandleFunc("POST /hooks/native/{host}/{name}", func(w http.ResponseWriter, r *http.Request) {
			host, name := r.PathValue("host"), r.PathValue("name")
			p, ok := a.nativeProject(host, name)
			if !ok || p.HookEnc == "" || p.Source.Type != "git" {
				blank404(w)
				return
			}
			body, err := io.ReadAll(io.LimitReader(r.Body, 5<<20))
			if err != nil {
				blank404(w)
				return
			}
			secret, err := a.openHook(p)
			if err != nil || !verifyHook(r, body, secret) {
				http.Error(w, "bad signature", http.StatusUnauthorized)
				return
			}
			if r.Header.Get("X-GitHub-Event") == "ping" {
				writeJSON(w, 200, map[string]string{"ok": "pong"})
				return
			}
			var payload struct {
				Ref string `json:"ref"`
			}
			json.Unmarshal(body, &payload)
			if !refMatches(payload.Ref, firstNonEmpty(p.Source.Branch, "main")) {
				writeJSON(w, 200, map[string]string{"ignored": payload.Ref})
				return
			}
			params, _ := json.Marshal(map[string]string{"name": name})
			if _, err := a.pipe.Run(context.Background(), change.Actor{Type: "system", ID: "webhook", Name: "webhook", Via: "api"}, host, "native.deploy", params); err != nil {
				a.fail(w, r, err)
				return
			}
			writeJSON(w, 202, map[string]string{"ok": "queued"})
		})
	})
}

func (a *App) openHook(p nativeProject) ([]byte, error) {
	b, err := base64.StdEncoding.DecodeString(p.HookEnc)
	if err != nil {
		return nil, err
	}
	return a.open(b)
}
