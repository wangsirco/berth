package panel

// Container upkeep (方案 §5.3): image updates with automatic rollback, per-service CPU and
// memory limits, registry mirrors and registry logins.

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"

	"github.com/wangsirco/berth/internal/change"
	"github.com/wangsirco/berth/internal/errs"
)

func init() {
	routeHooks = append(routeHooks, func(a *App, mux *http.ServeMux) {
		mux.HandleFunc("GET /api/v1/hosts/{host}/image-updates", a.authed("containers.view", func(w http.ResponseWriter, r *http.Request) {
			q := ""
			if r.URL.Query().Get("refresh") == "1" {
				q = "?refresh=1"
			}
			var out json.RawMessage
			if err := a.call(r.Context(), r.PathValue("host"), "GET", "/v1/docker/updates"+q, nil, &out); err != nil {
				a.fail(w, r, err)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.Write(out)
		}))
		mux.HandleFunc("GET /api/v1/hosts/{host}/registries", a.authed("containers.view", a.agentGet("/v1/docker/registries")))
	})
	reg := func(k change.Kind) {
		k.Perm, k.Scope, k.Title = "containers.manage", "containers:write", "kind."+k.Name
		change.Register(k)
	}
	reg(change.Kind{Name: "compose.upgrade", CLI: "berth compose upgrade {{.name}}", Heavy: true, MemMB: 256,
		Params: change.ParamSchema{Props: map[string]change.Prop{"name": {Type: "string", Desc: "compose project"}}, Required: []string{"name"}},
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			var p struct{ Name string }
			json.Unmarshal(raw, &p)
			var u struct {
				Items []struct {
					Project, Image, Container string
					Update                    bool
				} `json:"items"`
			}
			svc(env).call(env.Ctx, env.Host, "GET", "/v1/docker/updates", nil, &u)
			lines := []string{"docker compose pull", "docker compose up -d", "# not running 20 s later → the previous images come back"}
			for _, i := range u.Items {
				if i.Project == p.Name && i.Update {
					lines = append(lines, "↑ "+i.Image+" ("+i.Container+")")
				}
			}
			return change.Plan{Summary: "plan.compose.upgrade", SummaryP: map[string]any{"name": p.Name}, Risk: change.RiskMedium, Commands: lines,
				Affected: []change.Object{projObj(p.Name, env.Host)}}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			var p struct{ Name string }
			json.Unmarshal(raw, &p)
			return nil, nil, agentOp(env, "/v1/compose/projects/"+url.PathEscape(p.Name)+"/upgrade", nil)
		}})
	reg(change.Kind{Name: "compose.limits", CLI: "berth compose limits {{.name}} {{.service}}",
		Params: change.ParamSchema{Props: map[string]change.Prop{"name": {Type: "string"}, "service": {Type: "string"},
			"cpus": {Type: "string", Desc: `"1.5" ("" = no limit)`}, "memory": {Type: "string", Desc: `"512m", "2g" ("" = no limit)`}}, Required: []string{"name", "service"}},
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			var p struct{ Name, Service, CPUs, Memory string }
			json.Unmarshal(raw, &p)
			var cur struct {
				File, Before, After string
			}
			if err := svc(env).call(env.Ctx, env.Host, "POST", "/v1/compose/projects/"+url.PathEscape(p.Name)+"/limits",
				map[string]any{"service": p.Service, "cpus": p.CPUs, "memory": p.Memory, "check": true}, &cur); err != nil {
				return change.Plan{}, err
			}
			return change.Plan{Summary: "plan.compose.limits", SummaryP: map[string]any{"name": p.Name, "service": p.Service, "cpus": firstNonEmpty(p.CPUs, "—"), "mem": firstNonEmpty(p.Memory, "—")},
				Risk: change.RiskMedium, Undoable: true, Files: []change.FileDiff{{Path: cur.File, Before: cur.Before, After: cur.After}},
				Commands: []string{"docker compose up -d --no-deps " + p.Service}}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			a := svc(env)
			var p struct{ Name, Service, CPUs, Memory string }
			json.Unmarshal(raw, &p)
			var cur struct {
				CPUs   string `json:"cpus"`
				Memory string `json:"memory"`
			}
			if err := a.call(env.Ctx, env.Host, "POST", "/v1/compose/projects/"+url.PathEscape(p.Name)+"/limits",
				map[string]any{"service": p.Service, "cpus": p.CPUs, "memory": p.Memory, "check": true}, &cur); err != nil {
				return nil, nil, err
			}
			if err := agentOp(env, "/v1/compose/projects/"+url.PathEscape(p.Name)+"/limits", map[string]any{"service": p.Service, "cpus": p.CPUs, "memory": p.Memory}); err != nil {
				return nil, nil, err
			}
			u, _ := json.Marshal(map[string]string{"name": p.Name, "service": p.Service, "cpus": cur.CPUs, "memory": cur.Memory})
			return nil, &change.UndoRecipe{Kind: "compose.limits", Params: u}, nil
		}})
	reg(change.Kind{Name: "docker.mirrors", CLI: "berth docker mirrors",
		Params: change.ParamSchema{Props: map[string]change.Prop{"mirrors": {Type: "array", Desc: `["https://mirror.example.com"]; [] = none`}}, Required: []string{"mirrors"}},
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			var p struct{ Mirrors []string }
			json.Unmarshal(raw, &p)
			for _, m := range p.Mirrors {
				if !strings.HasPrefix(m, "https://") && !strings.HasPrefix(m, "http://") {
					return change.Plan{}, errs.New(400, "docker.bad_mirror", "url", m)
				}
			}
			return change.Plan{Summary: "plan.docker.mirrors", SummaryP: map[string]any{"list": firstNonEmpty(strings.Join(p.Mirrors, ", "), "—")}, Risk: change.RiskMedium, Undoable: true,
				Commands: []string{"/etc/docker/daemon.json: registry-mirrors", "systemctl reload docker  (running containers are not restarted)"}}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			a := svc(env)
			var cur struct {
				Mirrors []string `json:"mirrors"`
			}
			a.call(env.Ctx, env.Host, "GET", "/v1/docker/registries", nil, &cur)
			var p struct{ Mirrors []string }
			json.Unmarshal(raw, &p)
			if err := agentOp(env, "/v1/docker/mirrors", map[string]any{"mirrors": orEmpty(p.Mirrors)}); err != nil {
				return nil, nil, err
			}
			u, _ := json.Marshal(map[string]any{"mirrors": orEmpty(cur.Mirrors)})
			return nil, &change.UndoRecipe{Kind: "docker.mirrors", Params: u}, nil
		}})
	reg(change.Kind{Name: "docker.logs", CLI: "berth docker log-size {{.max_size}}",
		Params: change.ParamSchema{Props: map[string]change.Prop{"max_size": {Type: "string", Desc: `"50m" per file; "" = no cap`}, "max_file": {Type: "string", Desc: "files kept (default 3)"}}},
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			var p struct {
				MaxSize string `json:"max_size"`
				MaxFile string `json:"max_file"`
			}
			json.Unmarshal(raw, &p)
			return change.Plan{Summary: map[bool]string{true: "plan.docker.logs_off", false: "plan.docker.logs"}[p.MaxSize == ""], SummaryP: map[string]any{"size": p.MaxSize, "files": firstNonEmpty(p.MaxFile, "3")},
				Risk: change.RiskLow, Undoable: true, Commands: []string{"/etc/docker/daemon.json: log-opts", "systemctl reload docker"}}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			a := svc(env)
			var cur struct {
				LogOpts map[string]string `json:"log_opts"`
			}
			a.call(env.Ctx, env.Host, "GET", "/v1/docker/registries", nil, &cur)
			if err := agentOp(env, "/v1/docker/logopts", raw); err != nil {
				return nil, nil, err
			}
			u, _ := json.Marshal(map[string]string{"max_size": cur.LogOpts["max-size"], "max_file": cur.LogOpts["max-file"]})
			return nil, &change.UndoRecipe{Kind: "docker.logs", Params: u}, nil
		}})
	reg(change.Kind{Name: "docker.login", CLI: "berth docker login {{.registry}}", Redact: []string{"password"},
		Params: change.ParamSchema{Props: map[string]change.Prop{"registry": {Type: "string", Desc: "e.g. ghcr.io, registry.cn-hangzhou.aliyuncs.com"}, "user": {Type: "string"}, "password": {Type: "string"}},
			Required: []string{"registry", "user", "password"}},
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			var p struct{ Registry, User string }
			json.Unmarshal(raw, &p)
			return change.Plan{Summary: "plan.docker.login", SummaryP: map[string]any{"registry": p.Registry, "user": p.User}, Risk: change.RiskLow, Undoable: true}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			var p struct{ Registry, User, Password string }
			json.Unmarshal(raw, &p)
			if err := svc(env).call(env.Ctx, env.Host, "POST", "/v1/docker/login", p, nil); err != nil {
				return nil, nil, err
			}
			u, _ := json.Marshal(map[string]string{"registry": p.Registry})
			return nil, &change.UndoRecipe{Kind: "docker.logout", Params: u}, nil
		}})
	reg(change.Kind{Name: "docker.logout", CLI: "berth docker logout {{.registry}}",
		Params: change.ParamSchema{Props: map[string]change.Prop{"registry": {Type: "string"}}, Required: []string{"registry"}},
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			var p struct{ Registry string }
			json.Unmarshal(raw, &p)
			return change.Plan{Summary: "plan.docker.logout", SummaryP: map[string]any{"registry": p.Registry}, Risk: change.RiskLow}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			var p struct{ Registry string }
			json.Unmarshal(raw, &p)
			return nil, nil, svc(env).call(env.Ctx, env.Host, "POST", "/v1/docker/logout", p, nil)
		}})
}
