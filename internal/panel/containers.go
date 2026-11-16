package panel

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/wangsirco/berth/internal/agent/docker"
	"github.com/wangsirco/berth/internal/change"
	"github.com/wangsirco/berth/internal/errs"
)

const relationScanEvery = 5 * time.Minute

type nameParams struct {
	Name    string `json:"name"`
	Volumes bool   `json:"volumes,omitempty"`
}

func parseName(raw json.RawMessage) nameParams {
	var p nameParams
	_ = json.Unmarshal(raw, &p)
	return p
}

func (a *App) containers(ctx context.Context, host string) ([]docker.Container, error) {
	var list []docker.Container
	if err := a.call(ctx, host, "GET", "/v1/docker/containers", nil, &list); err != nil {
		return nil, err
	}
	return list, nil
}

// findContainer matches a container by name, full id or id prefix (≥ 6 chars).
func (a *App) findContainer(ctx context.Context, host, name string) (*docker.Container, error) {
	list, err := a.containers(ctx, host)
	if err != nil {
		return nil, err
	}
	name = strings.TrimPrefix(name, "/")
	for _, c := range list {
		if c.Name == name || c.ID == name || (len(name) >= 6 && strings.HasPrefix(c.ID, name)) {
			return &c, nil
		}
	}
	return nil, errs.New(404, "container.not_found", "name", name)
}

func (a *App) projects(ctx context.Context, host string) ([]docker.Project, error) {
	var list []docker.Project
	err := a.call(ctx, host, "GET", "/v1/compose/projects", nil, &list)
	return list, err
}

func (a *App) findProject(ctx context.Context, host, name string) (*docker.Project, error) {
	list, err := a.projects(ctx, host)
	if err != nil {
		return nil, err
	}
	for _, p := range list {
		if p.Name == name {
			return &p, nil
		}
	}
	return nil, errs.New(404, "compose.not_found", "name", name)
}

func ctrObj(c *docker.Container, host string) change.Object {
	return change.Object{Type: "container", ID: c.Name, Name: c.Name, Host: host}
}

func projObj(name, host string) change.Object {
	return change.Object{Type: "compose_project", ID: name, Name: name, Host: host}
}

func (a *App) adopted(host, project string) bool {
	if project == "berth-edge" || project == "berth-registry" { // Berth's own stacks
		return true
	}
	var n int
	a.db.QueryRow(`SELECT COUNT(*) FROM relations WHERE host=? AND from_type='host' AND to_type='compose_project' AND to_id=? AND kind='manages'`, host, project).Scan(&n)
	return n > 0
}

// syncRelations rebuilds scan-sourced edges (uses volume, joins network, project contains).
func (a *App) syncRelations(host string, list []docker.Container) {
	var edges []Relation
	for _, c := range list {
		from := change.Object{Type: "container", ID: c.Name}
		for _, m := range c.Mounts {
			if m.Type == "volume" && m.Name != "" {
				edges = append(edges, Relation{From: from, To: change.Object{Type: "volume", ID: m.Name}, Kind: "uses"})
			}
		}
		for _, n := range c.Networks {
			edges = append(edges, Relation{From: from, To: change.Object{Type: "network", ID: n}, Kind: "joins"})
		}
		if c.Project != "" {
			edges = append(edges, Relation{From: change.Object{Type: "compose_project", ID: c.Project}, To: from, Kind: "contains"})
		}
	}
	a.replaceScanned(host, []string{"uses", "joins", "contains"}, edges)
}

func (a *App) maybeSyncRelations(ctx context.Context, host string, list []docker.Container) {
	a.mu.RLock()
	last := a.lastScan[host]
	a.mu.RUnlock()
	if list == nil {
		if time.Since(last) < relationScanEvery {
			return
		}
		var err error
		if list, err = a.containers(ctx, host); err != nil {
			return
		}
	}
	a.syncRelations(host, list)
}

// ---- kinds ----

func containerKind(name, risk string, undo string, cli string, plan func(env change.Env, c *docker.Container, p nameParams) (change.Plan, error), apply func(env change.Env, c *docker.Container, p nameParams) error) change.Kind {
	return change.Kind{
		Name: name, Perm: "containers.manage", Scope: "containers:write", Title: "kind." + name, CLI: cli,
		Params: change.ParamSchema{Props: map[string]change.Prop{"name": {Type: "string", Desc: "container name or id"}, "volumes": {Type: "boolean"}}, Required: []string{"name"}},
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			p := parseName(raw)
			c, err := svc(env).findContainer(env.Ctx, env.Host, p.Name)
			if err != nil {
				return change.Plan{}, err
			}
			pl := change.Plan{Summary: "plan." + name, SummaryP: map[string]any{"name": c.Name, "image": c.Image}, Risk: change.Risk(risk),
				Affected: []change.Object{ctrObj(c, env.Host)}, Undoable: undo != ""}
			if plan != nil {
				return plan(env, c, p)
			}
			return pl, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			p := parseName(raw)
			c, err := svc(env).findContainer(env.Ctx, env.Host, p.Name)
			if err != nil {
				return nil, nil, err
			}
			if err := apply(env, c, p); err != nil {
				return nil, nil, err
			}
			env.Log("%s %s: ok", name, c.Name)
			if undo == "" {
				return map[string]string{"name": c.Name}, nil, nil
			}
			u, _ := json.Marshal(nameParams{Name: c.Name})
			return map[string]string{"name": c.Name}, &change.UndoRecipe{Kind: undo, Params: u}, nil
		},
	}
}

func agentAction(action string) func(env change.Env, c *docker.Container, p nameParams) error {
	return func(env change.Env, c *docker.Container, p nameParams) error {
		return svc(env).call(env.Ctx, env.Host, "POST", "/v1/docker/containers/"+url.PathEscape(c.ID)+"/"+action, map[string]bool{"volumes": p.Volumes, "force": true}, nil)
	}
}

func basicPlan(name, risk string, undoable bool, cmd string) func(env change.Env, c *docker.Container, p nameParams) (change.Plan, error) {
	return func(env change.Env, c *docker.Container, p nameParams) (change.Plan, error) {
		switch {
		case name == "container.start" && c.State == "running":
			return change.Plan{}, errs.New(409, "container.already_running", "name", c.Name)
		case (name == "container.stop" || name == "container.restart") && c.State != "running":
			return change.Plan{}, errs.New(409, "container.not_running", "name", c.Name)
		}
		return change.Plan{Summary: "plan." + name, SummaryP: map[string]any{"name": c.Name, "image": c.Image}, Risk: change.Risk(risk),
			Affected: []change.Object{ctrObj(c, env.Host)}, Undoable: undoable, Commands: []string{fmt.Sprintf(cmd, c.Name)}}, nil
	}
}

func init() {
	change.Register(containerKind("container.start", "low", "container.stop", "berth container start {{.name}}",
		basicPlan("container.start", "low", true, "docker start %s"), agentAction("start")))
	change.Register(containerKind("container.stop", "medium", "container.start", "berth container stop {{.name}}",
		basicPlan("container.stop", "medium", true, "docker stop %s"), agentAction("stop")))
	change.Register(containerKind("container.restart", "low", "", "berth container restart {{.name}}",
		basicPlan("container.restart", "low", false, "docker restart %s"), agentAction("restart")))
	change.Register(containerKind("container.remove", "destructive", "", "berth container rm {{.name}}{{if .volumes}} --volumes{{end}}",
		func(env change.Env, c *docker.Container, p nameParams) (change.Plan, error) {
			a := svc(env)
			a.maybeSyncRelations(env.Ctx, env.Host, nil)
			affected := append([]change.Object{ctrObj(c, env.Host)}, a.impact(env.Host, "container", c.Name)...)
			cmd := "docker rm -f " + c.Name
			if p.Volumes {
				cmd = "docker rm -f -v " + c.Name
			}
			return change.Plan{Summary: "plan.container.remove", SummaryP: map[string]any{"name": c.Name, "image": c.Image},
				Risk: change.RiskDestructive, Affected: affected, Commands: []string{cmd}}, nil
		}, agentAction("remove")))

	// ---- compose ----
	composeProps := map[string]change.Prop{"name": {Type: "string", Desc: "compose project name"}, "compose": {Type: "string"}, "env": {Type: "string"}, "env_sealed": {Type: "string"}}
	nameOnly := change.ParamSchema{Props: composeProps, Required: []string{"name"}}

	change.Register(change.Kind{
		Name: "compose.create", Perm: "containers.manage", Scope: "containers:write", Title: "kind.compose.create",
		Params: change.ParamSchema{Props: composeProps, Required: []string{"name", "compose"}}, Redact: []string{"env"},
		CLI: "berth compose create {{.name}} --file compose.yaml",
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			var p composeParams
			json.Unmarshal(raw, &p)
			if !docker.ValidProjectName(p.Name) {
				return change.Plan{}, errs.New(400, "compose.bad_name")
			}
			a := svc(env)
			if _, err := a.findProject(env.Ctx, env.Host, p.Name); err == nil {
				return change.Plan{}, errs.New(409, "compose.exists", "name", p.Name)
			}
			if err := a.call(env.Ctx, env.Host, "POST", "/v1/compose/projects/"+p.Name+"/validate", map[string]string{"compose": p.Compose, "env": p.Env}, nil); err != nil {
				return change.Plan{}, err
			}
			dir := docker.StacksDir + "/" + p.Name
			files := []change.FileDiff{{Path: dir + "/compose.yaml", After: p.Compose}}
			if p.Env != "" {
				files = append(files, change.FileDiff{Path: dir + "/.env", After: maskEnvText(p.Env)})
			}
			return change.Plan{Summary: "plan.compose.create", SummaryP: map[string]any{"name": p.Name, "dir": dir}, Risk: change.RiskMedium,
				Files: files, Affected: []change.Object{projObj(p.Name, env.Host)}, Undoable: true}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			var p composeParams
			json.Unmarshal(raw, &p)
			a := svc(env)
			if err := a.call(env.Ctx, env.Host, "PUT", "/v1/compose/projects/"+p.Name+"/files", map[string]any{"compose": p.Compose, "env": p.Env, "create": true}, nil); err != nil {
				return nil, nil, err
			}
			a.relate(env.Host, change.Object{Type: "host", ID: env.Host}, projObj(p.Name, env.Host), "manages", "module")
			u, _ := json.Marshal(nameParams{Name: p.Name})
			return map[string]string{"name": p.Name}, &change.UndoRecipe{Kind: "compose.remove", Params: u}, nil
		},
	})

	change.Register(change.Kind{
		Name: "compose.update", Perm: "containers.manage", Scope: "containers:write", Title: "kind.compose.update",
		Params: change.ParamSchema{Props: composeProps, Required: []string{"name"}}, Redact: []string{"env"},
		CLI: "berth compose edit {{.name}}",
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			a := svc(env)
			p, err := a.composeParamsOf(raw)
			if err != nil {
				return change.Plan{}, err
			}
			cur, err := a.composeFiles(env.Ctx, env.Host, p.Name)
			if err != nil {
				return change.Plan{}, err
			}
			if err := a.call(env.Ctx, env.Host, "POST", "/v1/compose/projects/"+p.Name+"/validate", map[string]string{"compose": p.Compose, "env": p.Env}, nil); err != nil {
				return change.Plan{}, err
			}
			files := []change.FileDiff{{Path: cur.File, Before: cur.Compose, After: p.Compose}}
			if cur.Env != p.Env {
				files = append(files, change.FileDiff{Path: cur.Dir + "/.env", Before: maskEnvText(cur.Env), After: maskEnvText(p.Env)})
			}
			return change.Plan{Summary: "plan.compose.update", SummaryP: map[string]any{"name": p.Name}, Risk: change.RiskMedium,
				Files: files, Affected: []change.Object{projObj(p.Name, env.Host)}, Undoable: true}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			a := svc(env)
			p, err := a.composeParamsOf(raw)
			if err != nil {
				return nil, nil, err
			}
			cur, err := a.composeFiles(env.Ctx, env.Host, p.Name)
			if err != nil {
				return nil, nil, err
			}
			if err := a.call(env.Ctx, env.Host, "PUT", "/v1/compose/projects/"+p.Name+"/files", map[string]any{"compose": p.Compose, "env": p.Env}, nil); err != nil {
				return nil, nil, err
			}
			env.Log("saved %s (not applied to running containers until compose up)", cur.File)
			// the previous .env goes into the undo recipe sealed with the master key
			sealed, err := a.seal([]byte(cur.Env))
			if err != nil {
				return nil, nil, err
			}
			u, _ := json.Marshal(composeParams{Name: p.Name, Compose: cur.Compose, EnvSealed: base64.StdEncoding.EncodeToString(sealed)})
			return map[string]string{"name": p.Name}, &change.UndoRecipe{Kind: "compose.update", Params: u}, nil
		},
	})

	composeCmd := func(name, cmd, risk, undo string, heavy bool) change.Kind {
		return change.Kind{
			Name: name, Perm: "containers.manage", Scope: "containers:write", Title: "kind." + name, Params: nameOnly,
			CLI: "berth compose " + cmd + " {{.name}}", Heavy: heavy, MemMB: map[bool]int{true: 256}[heavy],
			Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
				p := parseName(raw)
				pr, err := svc(env).findProject(env.Ctx, env.Host, p.Name)
				if err != nil {
					return change.Plan{}, err
				}
				return change.Plan{Summary: "plan." + name, SummaryP: map[string]any{"name": pr.Name, "services": strings.Join(pr.Services, ", ")},
					Risk: change.Risk(risk), Affected: []change.Object{projObj(pr.Name, env.Host)}, Undoable: undo != "",
					Commands: []string{"docker compose -p " + pr.Name + " " + composeArgs[cmd]}}, nil
			},
			Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
				p := parseName(raw)
				env.Log("::step 0 %s", cmd)
				if err := agentOp(env, "/v1/compose/projects/"+url.PathEscape(p.Name)+"/"+cmd, nil); err != nil {
					return nil, nil, err
				}
				if undo == "" {
					return nil, nil, nil
				}
				u, _ := json.Marshal(nameParams{Name: p.Name})
				return nil, &change.UndoRecipe{Kind: undo, Params: u}, nil
			},
		}
	}
	change.Register(composeCmd("compose.up", "up", "low", "compose.down", true))
	change.Register(composeCmd("compose.down", "down", "medium", "compose.up", true))
	change.Register(composeCmd("compose.pull", "pull", "low", "", true))
	change.Register(composeCmd("compose.restart", "restart", "low", "", true))

	change.Register(change.Kind{
		Name: "compose.remove", Perm: "containers.manage", Scope: "containers:write", Title: "kind.compose.remove", Params: nameOnly,
		CLI: "berth compose rm {{.name}}", Heavy: true,
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			a := svc(env)
			pr, err := a.findProject(env.Ctx, env.Host, parseName(raw).Name)
			if err != nil {
				return change.Plan{}, err
			}
			a.maybeSyncRelations(env.Ctx, env.Host, nil)
			affected := []change.Object{projObj(pr.Name, env.Host)}
			for _, o := range a.impact(env.Host, "compose_project", pr.Name) {
				affected = append(affected, o)
				if o.Type == "container" {
					affected = append(affected, a.impact(env.Host, "container", o.ID)...)
				}
			}
			cmds := []string{"docker compose -p " + pr.Name + " down"}
			if pr.InStacks {
				cmds = append(cmds, "mv "+pr.Dir+" "+docker.StacksDir+"/.removed/")
			}
			return change.Plan{Summary: "plan.compose.remove", SummaryP: map[string]any{"name": pr.Name, "dir": pr.Dir}, Risk: change.RiskDestructive,
				Affected: dedupe(affected), Commands: cmds}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			p := parseName(raw)
			if err := agentOp(env, "/v1/compose/projects/"+url.PathEscape(p.Name)+"/remove", nil); err != nil {
				return nil, nil, err
			}
			a := svc(env)
			a.unrelate(env.Host, change.Object{Type: "host", ID: env.Host}, projObj(p.Name, env.Host), "manages")
			return nil, nil, nil
		},
	})

	adopt := func(name, undo string, on bool) change.Kind {
		return change.Kind{
			Name: name, Perm: "containers.manage", Scope: "containers:write", Title: "kind." + name, Params: nameOnly,
			CLI: "berth compose " + map[bool]string{true: "adopt", false: "release"}[on] + " {{.name}}",
			Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
				a := svc(env)
				pr, err := a.findProject(env.Ctx, env.Host, parseName(raw).Name)
				if err != nil {
					return change.Plan{}, err
				}
				if a.adopted(env.Host, pr.Name) == on {
					return change.Plan{}, errs.New(409, map[bool]string{true: "compose.already_adopted", false: "compose.not_adopted"}[on], "name", pr.Name)
				}
				return change.Plan{Summary: "plan." + name, SummaryP: map[string]any{"name": pr.Name, "dir": pr.Dir}, Risk: change.RiskLow,
					Affected: []change.Object{projObj(pr.Name, env.Host)}, Undoable: true}, nil
			},
			Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
				a := svc(env)
				p := parseName(raw)
				host := change.Object{Type: "host", ID: env.Host}
				if on {
					a.relate(env.Host, host, projObj(p.Name, env.Host), "manages", "module")
					if list, err := a.containers(env.Ctx, env.Host); err == nil {
						a.syncRelations(env.Host, list)
					}
				} else {
					a.unrelate(env.Host, host, projObj(p.Name, env.Host), "manages")
				}
				u, _ := json.Marshal(nameParams{Name: p.Name})
				return nil, &change.UndoRecipe{Kind: undo, Params: u}, nil
			},
		}
	}
	change.Register(adopt("compose.adopt", "compose.release", true))
	change.Register(adopt("compose.release", "compose.adopt", false))

	// ---- images, volumes, networks ----
	refProps := change.ParamSchema{Props: map[string]change.Prop{"ref": {Type: "string", Desc: "image reference or id"}}, Required: []string{"ref"}}
	change.Register(change.Kind{
		Name: "image.pull", Perm: "containers.manage", Scope: "containers:write", Title: "kind.image.pull", Params: refProps,
		CLI: "berth image pull {{.ref}}", Heavy: true, MemMB: 128,
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			var p struct{ Ref string }
			json.Unmarshal(raw, &p)
			return change.Plan{Summary: "plan.image.pull", SummaryP: map[string]any{"ref": p.Ref}, Risk: change.RiskLow,
				Affected: []change.Object{{Type: "image", ID: p.Ref, Name: p.Ref, Host: env.Host}}, Commands: []string{"docker pull " + p.Ref}}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			var p struct{ Ref string }
			json.Unmarshal(raw, &p)
			return nil, nil, agentOp(env, "/v1/docker/images/pull", map[string]string{"ref": p.Ref})
		},
	})
	change.Register(simpleDockerKind("image.remove", "ref", "image", "berth image rm {{.ref}}", "docker rmi %s", "/v1/docker/images/remove"))
	change.Register(simpleDockerKind("volume.remove", "name", "volume", "berth volume rm {{.name}}", "docker volume rm %s", "/v1/docker/volumes/remove"))
	change.Register(simpleDockerKind("network.remove", "name", "network", "berth network rm {{.name}}", "docker network rm %s", "/v1/docker/networks/remove"))
	change.Register(change.Kind{
		Name: "image.prune", Perm: "containers.manage", Scope: "containers:write", Title: "kind.image.prune",
		Params: change.ParamSchema{Props: map[string]change.Prop{}}, CLI: "berth image prune",
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			var imgs []docker.Image
			if err := svc(env).call(env.Ctx, env.Host, "GET", "/v1/docker/images", nil, &imgs); err != nil {
				return change.Plan{}, err
			}
			var n, size int64
			var affected []change.Object
			for _, im := range imgs {
				if im.Dangling && len(im.UsedBy) == 0 {
					n++
					size += im.Size
					affected = append(affected, change.Object{Type: "image", ID: shortID(im.ID), Host: env.Host})
				}
			}
			return change.Plan{Summary: "plan.image.prune", SummaryP: map[string]any{"count": n, "size": humanBytes(size)}, Risk: change.RiskMedium,
				Affected: affected, Commands: []string{"docker image prune -f"}}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			var r struct{ Reclaimed int64 }
			if err := svc(env).call(env.Ctx, env.Host, "POST", "/v1/docker/images/prune", nil, &r); err != nil {
				return nil, nil, err
			}
			env.Log("reclaimed %s", humanBytes(r.Reclaimed))
			return r, nil, nil
		},
	})
}

var composeArgs = map[string]string{"up": "up -d --remove-orphans", "down": "down", "pull": "pull", "restart": "restart"}

// simpleDockerKind: delete-one-object kinds for images, volumes and networks.
func simpleDockerKind(name, param, objType, cli, cmd, path string) change.Kind {
	return change.Kind{
		Name: name, Perm: "containers.manage", Scope: "containers:write", Title: "kind." + name,
		Params: change.ParamSchema{Props: map[string]change.Prop{param: {Type: "string"}}, Required: []string{param}}, CLI: cli,
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			m := map[string]string{}
			json.Unmarshal(raw, &m)
			v := m[param]
			a := svc(env)
			a.maybeSyncRelations(env.Ctx, env.Host, nil)
			affected := append([]change.Object{{Type: objType, ID: v, Name: v, Host: env.Host}}, a.impact(env.Host, objType, v)...)
			risk := change.RiskMedium
			if objType == "volume" {
				risk = change.RiskDestructive // data lives there
			}
			return change.Plan{Summary: "plan." + name, SummaryP: map[string]any{"name": v}, Risk: risk, Affected: affected, Commands: []string{fmt.Sprintf(cmd, v)}}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			m := map[string]string{}
			json.Unmarshal(raw, &m)
			body := map[string]string{"ref": m[param], "name": m[param]}
			return nil, nil, svc(env).call(env.Ctx, env.Host, "POST", path, body, nil)
		},
	}
}

type composeParams struct {
	Name      string `json:"name"`
	Compose   string `json:"compose"`
	Env       string `json:"env,omitempty"`
	EnvSealed string `json:"env_sealed,omitempty"`
}

// composeParamsOf decodes params, opening a sealed .env (undo recipes carry it sealed).
func (a *App) composeParamsOf(raw json.RawMessage) (composeParams, error) {
	var p composeParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return p, errs.New(400, "params.invalid_json")
	}
	if p.EnvSealed != "" {
		b, err := base64.StdEncoding.DecodeString(p.EnvSealed)
		if err != nil {
			return p, err
		}
		plain, err := a.open(b)
		if err != nil {
			return p, err
		}
		p.Env, p.EnvSealed = string(plain), ""
	}
	return p, nil
}

func (a *App) composeFiles(ctx context.Context, host, name string) (*docker.ProjectFiles, error) {
	var f docker.ProjectFiles
	if err := a.call(ctx, host, "GET", "/v1/compose/projects/"+url.PathEscape(name)+"/files", nil, &f); err != nil {
		return nil, err
	}
	return &f, nil
}

func maskEnvText(s string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		t := strings.TrimSpace(l)
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		if k, _, ok := strings.Cut(l, "="); ok {
			lines[i] = k + "=••••"
		}
	}
	return strings.Join(lines, "\n")
}

func dedupe(objs []change.Object) []change.Object {
	seen := map[string]bool{}
	out := objs[:0]
	for _, o := range objs {
		if k := o.Type + "/" + o.ID; !seen[k] {
			seen[k] = true
			out = append(out, o)
		}
	}
	return out
}

func shortID(id string) string {
	id = strings.TrimPrefix(id, "sha256:")
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}

// ---- read API ----

type listenEntry struct {
	at   time.Time
	list []docker.Listen
}

type containerView struct {
	docker.Container
	Adopted bool `json:"adopted"` // its compose project is managed by Berth (or it is standalone)
	// who can reach it: public (a site points at it), open (a port published on every
	// address), local (published on 127.0.0.1 only) or internal (nothing published)
	Exposure string    `json:"exposure"`
	Sites    []siteRef `json:"sites,omitempty"`
	WAN      []string  `json:"wan,omitempty"` // Wharf forwards, "21116/udp"
}

func exposureOf(c docker.Container, sites []siteRef) string {
	if c.Project == "berth-edge" { // the panel's own web entry: open on 80/443 by design
		return "edge"
	}
	for _, s := range sites {
		if s.Enabled {
			return "public"
		}
	}
	exp := "internal"
	for _, p := range c.Ports {
		if p.Public == 0 {
			continue
		}
		if p.IP == "" || p.IP == "0.0.0.0" || p.IP == "::" {
			return "open"
		}
		exp = "local"
	}
	return exp
}

func init() {
	routeHooks = append(routeHooks, func(a *App, mux *http.ServeMux) {
		mux.HandleFunc("GET /api/v1/hosts/{host}/containers", a.authed("containers.view", a.handleContainers))
		mux.HandleFunc("GET /api/v1/hosts/{host}/containers/stats", a.authed("containers.view", a.handleContainerStats))
		mux.HandleFunc("GET /api/v1/hosts/{host}/containers/{name}", a.authed("containers.view", a.handleContainer))
		mux.HandleFunc("GET /api/v1/hosts/{host}/containers/{name}/listening", a.authed("containers.view", a.handleListening))
		mux.HandleFunc("GET /api/v1/hosts/{host}/containers/{name}/logs", a.authed("containers.view", a.handleContainerLogs))
		mux.HandleFunc("GET /api/v1/hosts/{host}/compose", a.authed("containers.view", a.handleProjects))
		mux.HandleFunc("GET /api/v1/hosts/{host}/compose/{name}/files", a.authed("containers.manage", a.handleProjectFiles))
		for _, what := range []string{"images", "networks", "volumes"} {
			mux.HandleFunc("GET /api/v1/hosts/{host}/"+what, a.authed("containers.view", a.proxyList("/v1/docker/"+what)))
		}
		for _, k := range []string{"start", "stop", "restart"} {
			mux.HandleFunc("POST /api/v1/hosts/{host}/containers/{name}/"+k, a.authed("", a.namedKind("container."+k)))
		}
	})
}

// namedKind is a REST shortcut that forwards {name} to a kind.
func (a *App) namedKind(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		raw, _ := json.Marshal(nameParams{Name: r.PathValue("name")})
		a.runChange(w, r, r.PathValue("host"), kind, raw)
	}
}

func (a *App) handleContainers(w http.ResponseWriter, r *http.Request) {
	host := r.PathValue("host")
	list, err := a.containers(r.Context(), host)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.maybeSyncRelations(r.Context(), host, list)
	bySite := map[string][]siteRef{}
	if all, err := a.sites(host); err == nil {
		for _, st := range all {
			if st.Target.Type == "container" {
				bySite[st.Target.Container] = append(bySite[st.Target.Container], siteRef{ID: st.ID, Domain: st.Primary(), Enabled: st.Enabled})
			}
		}
	}
	out := make([]containerView, 0, len(list))
	for _, c := range list {
		v := containerView{Container: c, Adopted: c.Project == "" || a.adopted(host, c.Project), Sites: bySite[c.Name]}
		v.Exposure = exposureOf(c, v.Sites)
		if c.Project != "" {
			for _, f := range a.appPublished(host, c.Project) {
				v.WAN = append(v.WAN, f.WANPort+"/"+f.Proto)
			}
		}
		out = append(out, v)
	}
	writeJSON(w, 200, map[string]any{"items": out})
}

func (a *App) handleContainerStats(w http.ResponseWriter, r *http.Request) {
	var st []docker.Stat
	if err := a.call(r.Context(), r.PathValue("host"), "GET", "/v1/docker/stats", nil, &st); err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"items": st})
}

func (a *App) handleContainer(w http.ResponseWriter, r *http.Request) {
	host := r.PathValue("host")
	c, err := a.findContainer(r.Context(), host, r.PathValue("name"))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	var d docker.ContainerDetail
	if err := a.call(r.Context(), host, "GET", "/v1/docker/containers/"+url.PathEscape(c.ID), nil, &d); err != nil {
		a.fail(w, r, err)
		return
	}
	var sites []siteRef
	if all, err := a.sites(host); err == nil {
		for _, st := range all {
			if st.Target.Type == "container" && st.Target.Container == c.Name {
				sites = append(sites, siteRef{ID: st.ID, Domain: st.Primary(), Enabled: st.Enabled})
			}
		}
	}
	var wan []string
	if c.Project != "" {
		for _, f := range a.appPublished(host, c.Project) {
			wan = append(wan, f.WANPort+"/"+f.Proto)
		}
	}
	writeJSON(w, 200, map[string]any{"container": d, "relations": a.related(host, "container", c.Name),
		"access": map[string]any{"exposure": exposureOf(*c, sites), "sites": sites, "wan": wan}})
}

func (a *App) listening(ctx context.Context, host, id string) ([]docker.Listen, error) {
	key := host + "/" + id
	a.mu.RLock()
	e, ok := a.listenCache[key]
	a.mu.RUnlock()
	if ok && time.Since(e.at) < time.Minute {
		return e.list, nil
	}
	var l []docker.Listen
	if err := a.call(ctx, host, "GET", "/v1/docker/containers/"+url.PathEscape(id)+"/listening", nil, &l); err != nil {
		return nil, err
	}
	a.mu.Lock()
	a.listenCache[key] = listenEntry{at: time.Now(), list: l}
	a.mu.Unlock()
	return l, nil
}

func (a *App) handleListening(w http.ResponseWriter, r *http.Request) {
	host := r.PathValue("host")
	c, err := a.findContainer(r.Context(), host, r.PathValue("name"))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	l, err := a.listening(r.Context(), host, c.ID)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"items": l})
}

// GET .../logs?tail=500&follow=1 — SSE: each event "log" carries {t,s,l}; "end" closes.
func (a *App) handleContainerLogs(w http.ResponseWriter, r *http.Request) {
	host := r.PathValue("host")
	c, err := a.findContainer(r.Context(), host, r.PathValue("name"))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	ag, err := a.agent(host)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	q := url.Values{"tail": {r.URL.Query().Get("tail")}, "follow": {r.URL.Query().Get("follow")}, "since": {r.URL.Query().Get("since")}}
	rc, err := ag.Stream(r.Context(), "GET", "/v1/docker/containers/"+url.PathEscape(c.ID)+"/logs?"+q.Encode(), nil)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	defer rc.Close()
	if r.URL.Query().Get("format") == "text" { // download
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="`+c.Name+`.log"`)
		_ = streamLogLines(rc, func(l docker.LogLine) bool {
			_, err := fmt.Fprintf(w, "%s %s %s\n", l.TS, l.Stream, l.Line)
			return err == nil
		})
		return
	}
	fl, _ := w.(http.Flusher)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(200)
	_ = streamLogLines(rc, func(l docker.LogLine) bool {
		b, _ := json.Marshal(l)
		_, err := fmt.Fprintf(w, "event: log\ndata: %s\n\n", b)
		if fl != nil {
			fl.Flush()
		}
		return err == nil
	})
	fmt.Fprint(w, "event: end\ndata: {}\n\n")
}

func streamLogLines(rc interface{ Read([]byte) (int, error) }, fn func(docker.LogLine) bool) error {
	dec := json.NewDecoder(rc)
	for {
		var raw map[string]json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return err
		}
		if _, done := raw["done"]; done {
			return nil
		}
		var l docker.LogLine
		b, _ := json.Marshal(raw)
		_ = json.Unmarshal(b, &l)
		if !fn(l) {
			return nil
		}
	}
}

type projectView struct {
	docker.Project
	Adopted bool `json:"adopted"`
}

func (a *App) handleProjects(w http.ResponseWriter, r *http.Request) {
	host := r.PathValue("host")
	list, err := a.projects(r.Context(), host)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	out := make([]projectView, 0, len(list))
	for _, p := range list {
		out = append(out, projectView{Project: p, Adopted: a.adopted(host, p.Name)})
	}
	writeJSON(w, 200, map[string]any{"items": out})
}

// handleProjectFiles returns the real .env (editing needs it); containers.manage only.
func (a *App) handleProjectFiles(w http.ResponseWriter, r *http.Request) {
	f, err := a.composeFiles(r.Context(), r.PathValue("host"), r.PathValue("name"))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, 200, f)
}

func (a *App) proxyList(path string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var out json.RawMessage
		if err := a.call(r.Context(), r.PathValue("host"), "GET", path, nil, &out); err != nil {
			a.fail(w, r, err)
			return
		}
		writeJSON(w, 200, map[string]any{"items": out})
	}
}

func init() {
	change.Register(change.Kind{
		Name: "network.create", Perm: "containers.manage", Scope: "containers:write", Title: "kind.network.create", CLI: "berth network create {{.name}}",
		Params: change.ParamSchema{Props: map[string]change.Prop{"name": {Type: "string"}, "subnet": {Type: "string", Desc: "optional, e.g. 172.30.0.0/24"},
			"internal": {Type: "boolean", Desc: "no access to the internet"}}, Required: []string{"name"}},
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			var p struct {
				Name, Subnet string
				Internal     bool
			}
			json.Unmarshal(raw, &p)
			cmd := "docker network create"
			if p.Subnet != "" {
				cmd += " --subnet " + p.Subnet
			}
			if p.Internal {
				cmd += " --internal"
			}
			return change.Plan{Summary: "plan.network.create", SummaryP: map[string]any{"name": p.Name}, Risk: change.RiskLow, Undoable: true,
				Commands: []string{cmd + " " + p.Name}, Affected: []change.Object{{Type: "network", ID: p.Name, Name: p.Name, Host: env.Host}}}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			var p struct{ Name string }
			json.Unmarshal(raw, &p)
			if err := svc(env).call(env.Ctx, env.Host, "POST", "/v1/docker/networks/create", raw, nil); err != nil {
				return nil, nil, err
			}
			u, _ := json.Marshal(map[string]string{"name": p.Name})
			return nil, &change.UndoRecipe{Kind: "network.remove", Params: u}, nil
		},
	})
	change.Register(change.Kind{
		Name: "volume.create", Perm: "containers.manage", Scope: "containers:write", Title: "kind.volume.create", CLI: "berth volume create {{.name}}",
		Params: change.ParamSchema{Props: map[string]change.Prop{"name": {Type: "string"}}, Required: []string{"name"}},
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			var p struct{ Name string }
			json.Unmarshal(raw, &p)
			return change.Plan{Summary: "plan.volume.create", SummaryP: map[string]any{"name": p.Name}, Risk: change.RiskLow, Undoable: true,
				Commands: []string{"docker volume create " + p.Name}, Affected: []change.Object{{Type: "volume", ID: p.Name, Name: p.Name, Host: env.Host}}}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			var p struct{ Name string }
			json.Unmarshal(raw, &p)
			if err := svc(env).call(env.Ctx, env.Host, "POST", "/v1/docker/volumes/create", raw, nil); err != nil {
				return nil, nil, err
			}
			u, _ := json.Marshal(map[string]string{"name": p.Name})
			return nil, &change.UndoRecipe{Kind: "volume.remove", Params: u}, nil
		},
	})
}
