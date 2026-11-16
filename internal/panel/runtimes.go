package panel

// Runtimes (运行环境): the Node, Python and Java versions mise has installed for projects,
// what each takes on disk and which projects use it. Versions nobody uses can go.

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/wangsirco/berth/internal/change"
	"github.com/wangsirco/berth/internal/errs"
)

type runtimeView struct {
	Tool    string   `json:"tool"`
	Version string   `json:"version"`
	Bytes   int64    `json:"bytes"`
	UsedBy  []string `json:"used_by"`
}

// versionUses: does a project's version ("22", "3.12", "lts") pick this installed one?
func versionUses(want, installed string, newest bool) bool {
	if want == "lts" || want == "latest" {
		return newest
	}
	return want == installed || strings.HasPrefix(installed, want+".")
}

func (a *App) runtimes(env change.Env) ([]runtimeView, bool, error) {
	var out struct {
		Items []struct {
			Tool, Version string
			Bytes         int64
		} `json:"items"`
		Mise bool `json:"mise"`
	}
	if err := a.call(env.Ctx, env.Host, "GET", "/v1/system/runtimes", nil, &out); err != nil {
		return nil, false, err
	}
	projects := a.appProjects(env.Host)
	for _, n := range a.nativeProjects(env.Host) {
		projects = append(projects, projectSpec{Name: n.Name, Kind: n.Kind, Version: n.Version})
	}
	seen := map[string]bool{}
	list := []runtimeView{}
	for _, r := range out.Items { // newest first per tool
		v := runtimeView{Tool: r.Tool, Version: r.Version, Bytes: r.Bytes, UsedBy: []string{}}
		for _, p := range projects {
			if p.Kind == r.Tool && p.Version != "" && versionUses(p.Version, r.Version, !seen[r.Tool]) {
				v.UsedBy = append(v.UsedBy, p.Name)
			}
		}
		seen[r.Tool] = true
		list = append(list, v)
	}
	return list, out.Mise, nil
}

func init() {
	routeHooks = append(routeHooks, func(a *App, mux *http.ServeMux) {
		mux.HandleFunc("GET /api/v1/hosts/{host}/runtimes", a.authed("sites.view", func(w http.ResponseWriter, r *http.Request) {
			list, mise, err := a.runtimes(change.Env{Ctx: r.Context(), Host: r.PathValue("host")})
			if err != nil {
				a.fail(w, r, err)
				return
			}
			writeJSON(w, 200, map[string]any{"items": list, "mise": mise})
		}))
	})
	props := change.ParamSchema{Props: map[string]change.Prop{"tool": {Type: "string", Enum: []string{"node", "python", "java"}},
		"version": {Type: "string", Desc: "e.g. 22, 20.18.1, 3.12, 21"}}, Required: []string{"tool", "version"}}
	type rp struct{ Tool, Version string }
	change.Register(change.Kind{
		Name: "runtime.install", Perm: "sites.manage", Scope: "sites:write", Title: "kind.runtime.install", CLI: "berth runtime install {{.tool}}@{{.version}}",
		Heavy: true, MemMB: 200, Params: props,
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			var p rp
			json.Unmarshal(raw, &p)
			return change.Plan{Summary: "plan.runtime.install", SummaryP: map[string]any{"tool": p.Tool, "version": p.Version}, Risk: change.RiskLow, Undoable: true,
				Commands: []string{"mise install " + p.Tool + "@" + p.Version + "  (into /opt/berth/mise; the system's own versions are not touched)"}}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			var p rp
			json.Unmarshal(raw, &p)
			before, _, _ := svc(env).runtimes(env)
			if err := agentOp(env, "/v1/system/runtimes", map[string]any{"tool": p.Tool, "version": p.Version}); err != nil {
				return nil, nil, err
			}
			after, _, _ := svc(env).runtimes(env)
			// undo removes what this install added (the exact version mise picked)
			for _, x := range after {
				fresh := x.Tool == p.Tool
				for _, y := range before {
					if y.Tool == x.Tool && y.Version == x.Version {
						fresh = false
					}
				}
				if fresh {
					u, _ := json.Marshal(map[string]string{"tool": x.Tool, "version": x.Version})
					return map[string]string{"version": x.Version}, &change.UndoRecipe{Kind: "runtime.remove", Params: u}, nil
				}
			}
			return nil, nil, nil
		},
	})
	change.Register(change.Kind{
		Name: "runtime.remove", Perm: "sites.manage", Scope: "sites:write", Title: "kind.runtime.remove", CLI: "berth runtime remove {{.tool}}@{{.version}}",
		Params: props,
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			var p rp
			json.Unmarshal(raw, &p)
			list, _, err := svc(env).runtimes(env)
			if err != nil {
				return change.Plan{}, err
			}
			for _, x := range list {
				if x.Tool == p.Tool && x.Version == p.Version {
					if len(x.UsedBy) > 0 {
						return change.Plan{}, errs.New(409, "runtime.in_use", "list", strings.Join(x.UsedBy, ", "))
					}
					return change.Plan{Summary: "plan.runtime.remove", SummaryP: map[string]any{"tool": p.Tool, "version": p.Version, "mb": x.Bytes >> 20},
						Risk: change.RiskLow, Undoable: true, Commands: []string{"mise uninstall " + p.Tool + "@" + p.Version}}, nil
				}
			}
			return change.Plan{}, errs.New(404, "runtime.not_found", "name", p.Tool+"@"+p.Version)
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			var p rp
			json.Unmarshal(raw, &p)
			if err := agentOp(env, "/v1/system/runtimes", map[string]any{"tool": p.Tool, "version": p.Version, "remove": true}); err != nil {
				return nil, nil, err
			}
			u, _ := json.Marshal(map[string]string{"tool": p.Tool, "version": p.Version})
			return nil, &change.UndoRecipe{Kind: "runtime.install", Params: u}, nil
		},
	})
}
