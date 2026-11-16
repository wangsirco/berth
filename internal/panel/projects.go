package panel

// Projects: Node, Python, Go, Java or any command, kept running by systemd (开机自启、崩溃
// 自动重启), with its runtime version from mise. One click makes a site for its port.

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/wangsirco/berth/internal/change"
	"github.com/wangsirco/berth/internal/errs"
)

type projectSpec struct {
	Name    string            `json:"name"`
	Kind    string            `json:"kind"`
	Version string            `json:"version,omitempty"`
	Dir     string            `json:"dir"`
	Command string            `json:"command"`
	Install string            `json:"install,omitempty"`
	User    string            `json:"user"`
	Env     map[string]string `json:"env,omitempty"`
	Port    int               `json:"port,omitempty"`
}

type appProjectView struct {
	projectSpec
	Unit   string  `json:"unit"`
	Active string  `json:"active"`
	MemB   int64   `json:"mem_bytes,omitempty"`
	Sites  []int64 `json:"sites"`
}

// defaults for the form: version and start command by kind
var projectDefaults = map[string][2]string{
	"node":   {"22", "npm start"},
	"python": {"3.12", "python app.py"},
	"java":   {"21", "java -jar app.jar"},
	"go":     {"", "./app"},
	"custom": {"", ""},
}

func (a *App) appProjects(host string) []projectSpec {
	out := []projectSpec{}
	rows, err := a.db.Query(`SELECT spec FROM projects WHERE host=? ORDER BY name`, host)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var s string
		rows.Scan(&s)
		if strings.Contains(s, `"mode":"release"`) {
			continue // native projects with releases are listed separately
		}
		var p projectSpec
		json.Unmarshal([]byte(s), &p)
		out = append(out, p)
	}
	return out
}

func (a *App) appProject(host, name string) (projectSpec, bool) {
	var s string
	if a.db.QueryRow(`SELECT spec FROM projects WHERE host=? AND name=?`, host, name).Scan(&s) != nil {
		return projectSpec{}, false
	}
	if strings.Contains(s, `"mode":"release"`) {
		return projectSpec{}, false
	}
	var p projectSpec
	json.Unmarshal([]byte(s), &p)
	return p, true
}

func init() {
	routeHooks = append(routeHooks, func(a *App, mux *http.ServeMux) {
		mux.HandleFunc("GET /api/v1/hosts/{host}/projects", a.authed("sites.view", func(w http.ResponseWriter, r *http.Request) {
			host := r.PathValue("host")
			var svc struct {
				Items []struct {
					Name     string `json:"name"`
					Active   string `json:"active"`
					MemBytes int64  `json:"mem_bytes"`
				} `json:"items"`
			}
			a.call(r.Context(), host, "GET", "/v1/system/services", nil, &svc)
			sites, _ := a.sites(host)
			out := []appProjectView{}
			for _, p := range a.appProjects(host) {
				v := appProjectView{projectSpec: p, Unit: "berth-app-" + p.Name + ".service", Active: "inactive", Sites: []int64{}}
				for _, s := range svc.Items {
					if s.Name == v.Unit {
						v.Active, v.MemB = s.Active, s.MemBytes
					}
				}
				for _, s := range sites {
					if p.Port > 0 && s.Target.Type == "address" {
						for _, u := range s.Target.Upstreams {
							if strings.HasSuffix(u, ":"+strconv.Itoa(p.Port)) && (strings.HasPrefix(u, "127.0.0.1:") || strings.HasPrefix(u, "localhost:")) {
								v.Sites = append(v.Sites, s.ID)
							}
						}
					}
				}
				out = append(out, v)
			}
			var users []struct {
				Name string `json:"name"`
			}
			a.call(r.Context(), host, "GET", "/v1/system/users", nil, &users)
			writeJSON(w, 200, map[string]any{"items": out, "defaults": projectDefaults, "users": users})
		}))
	})
	props := map[string]change.Prop{"name": {Type: "string"}, "kind": {Type: "string", Enum: []string{"node", "python", "go", "java", "custom"}},
		"version": {Type: "string", Desc: "runtime version for node/python/java, e.g. 22, 3.12, 21"}, "dir": {Type: "string"}, "command": {Type: "string"},
		"install": {Type: "string", Desc: "run once before starting, e.g. npm ci"}, "user": {Type: "string", Desc: "system user to run as (not root)"},
		"env": {Type: "object"}, "port": {Type: "integer"}}
	change.Register(change.Kind{
		Name: "project.set", Perm: "sites.manage", Scope: "sites:write", Title: "kind.project.set", CLI: "berth project set {{.name}}", Heavy: true, MemMB: 200,
		Params: change.ParamSchema{Props: props, Required: []string{"name", "kind", "dir", "command", "user"}},
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			var p projectSpec
			json.Unmarshal(raw, &p)
			if p.User == "root" {
				return change.Plan{}, errs.New(400, "system.project_root")
			}
			if _, native := svc(env).nativeProject(env.Host, p.Name); native {
				return change.Plan{}, errs.New(409, "native.name_taken", "name", p.Name)
			}
			_, exists := svc(env).appProject(env.Host, p.Name)
			cmds := []string{}
			if p.Version != "" {
				cmds = append(cmds, "mise install "+p.Kind+"@"+p.Version)
			}
			if p.Install != "" {
				cmds = append(cmds, "cd "+p.Dir+" && "+p.Install)
			}
			cmds = append(cmds, "systemd: berth-app-"+p.Name+".service → "+p.Command+" (as "+p.User+", restart on crash, start at boot)")
			return change.Plan{Summary: map[bool]string{true: "plan.project.update", false: "plan.project.create"}[exists], SummaryP: map[string]any{"name": p.Name, "kind": p.Kind},
				Risk: change.RiskMedium, Undoable: true, Commands: cmds}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			a := svc(env)
			var p projectSpec
			json.Unmarshal(raw, &p)
			old, existed := a.appProject(env.Host, p.Name)
			if err := agentOp(env, "/v1/system/projects", p); err != nil {
				return nil, nil, err
			}
			spec, _ := json.Marshal(p)
			a.db.Exec(`INSERT INTO projects(host, name, spec, created_by, created_at) VALUES(?,?,?,?,?) ON CONFLICT(host, name) DO UPDATE SET spec=excluded.spec`,
				env.Host, p.Name, string(spec), env.ActorName, time.Now().Unix())
			if existed {
				u, _ := json.Marshal(old)
				return nil, &change.UndoRecipe{Kind: "project.set", Params: u}, nil
			}
			u, _ := json.Marshal(map[string]string{"name": p.Name})
			return nil, &change.UndoRecipe{Kind: "project.delete", Params: u}, nil
		},
	})
	change.Register(change.Kind{
		Name: "project.delete", Perm: "sites.manage", Scope: "sites:write", Title: "kind.project.delete", CLI: "berth project delete {{.name}}",
		Params: change.ParamSchema{Props: map[string]change.Prop{"name": {Type: "string"}}, Required: []string{"name"}},
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			var p struct{ Name string }
			json.Unmarshal(raw, &p)
			if _, ok := svc(env).appProject(env.Host, p.Name); !ok {
				return change.Plan{}, errs.New(404, "project.not_found")
			}
			return change.Plan{Summary: "plan.project.delete", SummaryP: map[string]any{"name": p.Name}, Risk: change.RiskMedium, Undoable: true,
				Commands: []string{"systemctl disable --now berth-app-" + p.Name + ".service  (the project's files stay)"}}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			a := svc(env)
			var p struct{ Name string }
			json.Unmarshal(raw, &p)
			old, _ := a.appProject(env.Host, p.Name)
			if err := a.call(env.Ctx, env.Host, "POST", "/v1/system/projects/remove", p, nil); err != nil {
				return nil, nil, err
			}
			a.db.Exec(`DELETE FROM projects WHERE host=? AND name=?`, env.Host, p.Name)
			u, _ := json.Marshal(old)
			return nil, &change.UndoRecipe{Kind: "project.set", Params: u}, nil
		},
	})
}
