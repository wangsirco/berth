package panel

// ARM / Asahi compatibility (16K memory pages). See internal/agent/compat.go.

import (
	"context"
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/wangsirco/berth/internal/change"
	"github.com/wangsirco/berth/internal/errs"
)

type compatStatus struct {
	PageSize  int    `json:"page_size"`
	Needed    bool   `json:"needed"`
	KVM       bool   `json:"kvm"`
	Packages  bool   `json:"packages"`
	Runtime   bool   `json:"runtime"`
	Supported bool   `json:"supported"`
	OS        string `json:"os"`
}

func (a *App) compat(ctx context.Context, host string) (compatStatus, error) {
	var st compatStatus
	err := a.call(ctx, host, "GET", "/v1/compat", nil, &st)
	return st, err
}

// pageSizeCrash matches log lines of programs that fail on pages larger than 4K.
var pageSizeCrash = regexp.MustCompile(`(?i)(unsupported system page size|<jemalloc>.*page|page ?size.*(not supported|unsupported|mismatch|16384|65536)|tcmalloc.*page size|failed to map segment from shared object)`)

// pageSizeLogs looks for the signature in a container's recent log.
func (a *App) pageSizeLogs(ctx context.Context, host, container string) string {
	lines, err := a.tailLogs(ctx, host, container, 200)
	if err != nil {
		return ""
	}
	for _, l := range lines {
		if pageSizeCrash.MatchString(l.Line) {
			return strings.TrimSpace(l.Line)
		}
	}
	return ""
}

type runtimeParams struct {
	Name    string `json:"name"`    // compose project
	Service string `json:"service"` // service in it
	Runtime string `json:"runtime"` // krun | "" (default)
}

func init() {
	routeHooks = append(routeHooks, func(a *App, mux *http.ServeMux) {
		mux.HandleFunc("GET /api/v1/hosts/{host}/compat", a.authed("containers.view", func(w http.ResponseWriter, r *http.Request) {
			st, err := a.compat(r.Context(), r.PathValue("host"))
			if err != nil {
				a.fail(w, r, err)
				return
			}
			writeJSON(w, 200, st)
		}))
	})

	change.Register(change.Kind{
		Name: "compat.install", Perm: "system.manage", Scope: "system:write", Title: "kind.compat.install",
		Params: change.ParamSchema{Props: map[string]change.Prop{}}, CLI: "berth compat install", Heavy: true, MemMB: 64,
		Plan: func(env change.Env, _ json.RawMessage) (change.Plan, error) {
			st, err := svc(env).compat(env.Ctx, env.Host)
			if err != nil {
				return change.Plan{}, err
			}
			if st.Packages && st.Runtime {
				return change.Plan{}, errs.New(409, "compat.installed")
			}
			if !st.KVM {
				return change.Plan{}, errs.New(409, "compat.no_kvm")
			}
			if !st.Packages && !st.Supported {
				return change.Plan{}, errs.New(409, "compat.unsupported_os", "os", st.OS)
			}
			p := change.Plan{Summary: "plan.compat.install", SummaryP: map[string]any{"page": st.PageSize / 1024}, Risk: change.RiskMedium, Undoable: true,
				Files: []change.FileDiff{{Path: "/etc/docker/daemon.json", After: `"runtimes": { "krun": { "path": "/usr/bin/krun" } }`}}}
			if !st.Packages {
				p.Commands = append(p.Commands, "dnf install crun-krun libkrun")
			}
			p.Commands = append(p.Commands, "systemctl reload docker   # running containers keep running")
			return p, nil
		},
		Apply: func(env change.Env, _ json.RawMessage) (any, *change.UndoRecipe, error) {
			if err := agentOp(env, "/v1/compat/install", nil); err != nil {
				return nil, nil, err
			}
			return nil, &change.UndoRecipe{Kind: "compat.uninstall", Params: json.RawMessage(`{}`)}, nil
		},
	})
	change.Register(change.Kind{
		Name: "compat.uninstall", Perm: "system.manage", Scope: "system:write", Title: "kind.compat.uninstall",
		Params: change.ParamSchema{Props: map[string]change.Prop{}}, CLI: "berth compat uninstall",
		Plan: func(env change.Env, _ json.RawMessage) (change.Plan, error) {
			return change.Plan{Summary: "plan.compat.uninstall", Risk: change.RiskMedium, Undoable: true,
				Commands: []string{"remove runtimes.krun from /etc/docker/daemon.json", "systemctl reload docker"}}, nil
		},
		Apply: func(env change.Env, _ json.RawMessage) (any, *change.UndoRecipe, error) {
			if err := svc(env).call(env.Ctx, env.Host, "POST", "/v1/compat/uninstall", nil, nil); err != nil {
				return nil, nil, err
			}
			return nil, &change.UndoRecipe{Kind: "compat.install", Params: json.RawMessage(`{}`)}, nil
		},
	})
	change.Register(change.Kind{
		Name: "compose.runtime", Perm: "containers.manage", Scope: "containers:write", Title: "kind.compose.runtime",
		Params: change.ParamSchema{Props: map[string]change.Prop{
			"name": {Type: "string", Desc: "compose project"}, "service": {Type: "string"},
			"runtime": {Type: "string", Enum: []string{"", "krun"}, Desc: "krun = 4K-page compatibility VM; empty = normal"}}, Required: []string{"name", "service"}},
		CLI: "berth compose runtime {{.name}} {{.service}} {{.runtime}}", Heavy: true, MemMB: 128,
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			var p runtimeParams
			json.Unmarshal(raw, &p)
			a := svc(env)
			if p.Runtime == "krun" {
				st, err := a.compat(env.Ctx, env.Host)
				if err != nil {
					return change.Plan{}, err
				}
				if !st.Runtime {
					return change.Plan{}, errs.New(409, "compat.not_installed")
				}
			}
			var chk struct {
				File, Runtime, Before, After string
			}
			if err := a.call(env.Ctx, env.Host, "POST", "/v1/compose/projects/"+p.Name+"/runtime", map[string]any{"service": p.Service, "runtime": p.Runtime, "check": true}, &chk); err != nil {
				return change.Plan{}, err
			}
			summary := "plan.compose.runtime_on"
			if p.Runtime == "" {
				summary = "plan.compose.runtime_off"
			}
			return change.Plan{Summary: summary, SummaryP: map[string]any{"name": p.Name, "service": p.Service}, Risk: change.RiskMedium, Undoable: true,
				Files:    []change.FileDiff{{Path: chk.File, Before: chk.Before, After: chk.After}},
				Commands: []string{"docker compose -p " + p.Name + " up -d --no-deps --force-recreate " + p.Service},
				Affected: []change.Object{projObj(p.Name, env.Host)}}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			var p runtimeParams
			json.Unmarshal(raw, &p)
			c, err := svc(env).agent(env.Host)
			if err != nil {
				return nil, nil, err
			}
			rc, err := c.Stream(env.Ctx, "POST", "/v1/compose/projects/"+p.Name+"/runtime", map[string]any{"service": p.Service, "runtime": p.Runtime})
			if err != nil {
				return nil, nil, err
			}
			old := ""
			err = streamLines(rc, func(l string) {
				if strings.HasPrefix(l, "::result") {
					old = strings.TrimSpace(strings.TrimPrefix(l, "::result"))
					return
				}
				env.Log("%s", l)
			})
			if err != nil {
				return nil, nil, err
			}
			u, _ := json.Marshal(runtimeParams{Name: p.Name, Service: p.Service, Runtime: old})
			return nil, &change.UndoRecipe{Kind: "compose.runtime", Params: u}, nil
		},
	})
}

// compatFix: what diagnosis offers when a container crashed on 16K pages.
func (a *App) compatFix(ctx context.Context, host, container, project, service string) *diagFix {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	st, err := a.compat(ctx, host)
	if err != nil {
		return nil
	}
	if !st.Runtime {
		return &diagFix{Kind: "compat.install", Params: map[string]any{}, Label: "fix.compat_install"}
	}
	if project == "" || service == "" {
		return nil
	}
	return &diagFix{Kind: "compose.runtime", Params: runtimeParams{Name: project, Service: service, Runtime: "krun"}, Label: "fix.compat_run"}
}
