package panel

// Single-database import and export (宝塔/1Panel 的“导入”“备份”): load an .sql/.sql.gz file
// from the server into a database (after saving a copy of it), or dump one to a file you
// can download from Files.

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/wangsirco/berth/internal/change"
	"github.com/wangsirco/berth/internal/errs"
)

func (a *App) dumpDB(env change.Env, d dbInstance, name, path string) error {
	t, err := a.dbTarget(env.Ctx, d, name)
	if err != nil {
		return err
	}
	return a.call(env.Ctx, env.Host, "POST", "/v1/db/dump", map[string]any{"engine": t.Engine, "addr": t.Addr, "user": t.User, "password": t.Password,
		"database": name, "container": d.Container, "path": path}, nil)
}

func init() {
	props := map[string]change.Prop{"instance": {Type: "integer"}, "database": {Type: "string"}, "path": {Type: "string", Desc: ".sql or .sql.gz on the server"},
		"replace": {Type: "boolean", Desc: "empty the database first (putting a saved copy back)"}}
	change.Register(change.Kind{
		Name: "db.import", Perm: "db.manage", Scope: "system:write", Title: "kind.db.import", CLI: "berth db import {{.instance}} {{.database}} {{.path}}", Heavy: true,
		Params: change.ParamSchema{Props: props, Required: []string{"instance", "database", "path"}},
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			var p struct {
				Instance       int64
				Database, Path string
			}
			json.Unmarshal(raw, &p)
			d, err := svc(env).dbInstance(env.Host, p.Instance)
			if err != nil {
				return change.Plan{}, err
			}
			if d.Container == "" && d.Native == "" || d.Engine == "redis" || d.Engine == "sqlite" {
				return change.Plan{}, errs.New(409, "db.import_container_only")
			}
			if !strings.HasSuffix(p.Path, ".sql") && !strings.HasSuffix(p.Path, ".sql.gz") {
				return change.Plan{}, errs.New(400, "db.bad_import_file")
			}
			return change.Plan{Summary: "plan.db.import", SummaryP: map[string]any{"path": p.Path, "db": p.Database, "instance": d.Name}, Risk: change.RiskHigh, Undoable: true,
				Commands: []string{"# a copy of " + p.Database + " is saved first", loadCmd(d) + " … " + p.Database + " < " + p.Path},
				Affected: []change.Object{dbObj(d, p.Database)}}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			a := svc(env)
			var p struct {
				Instance       int64
				Database, Path string
				Replace        bool
			}
			json.Unmarshal(raw, &p)
			d, err := a.dbInstance(env.Host, p.Instance)
			if err != nil {
				return nil, nil, err
			}
			before := dropDumpPath(d, p.Database, time.Now().Format("20060102-150405")+"-before-import")
			if err := a.dumpDB(env, d, p.Database, before); err != nil {
				return nil, nil, err
			}
			env.Log("saved the current %s to %s", p.Database, before)
			t, err := a.dbTarget(env.Ctx, d, p.Database)
			if err != nil {
				return nil, nil, err
			}
			if err := a.call(env.Ctx, env.Host, "POST", "/v1/db/import", map[string]any{"engine": t.Engine, "addr": t.Addr, "user": t.User, "password": t.Password,
				"database": p.Database, "container": d.Container, "path": p.Path, "replace": p.Replace}, nil); err != nil {
				return nil, nil, err
			}
			u, _ := json.Marshal(map[string]any{"instance": p.Instance, "database": p.Database, "path": before, "replace": true})
			return map[string]string{"before": before}, &change.UndoRecipe{Kind: "db.import", Params: u}, nil
		},
	})
	change.Register(change.Kind{
		Name: "db.export", Perm: "db.manage", Scope: "system:write", Title: "kind.db.export", CLI: "berth db export {{.instance}} {{.database}}", Heavy: true,
		Params: change.ParamSchema{Props: map[string]change.Prop{"instance": {Type: "integer"}, "database": {Type: "string"}}, Required: []string{"instance", "database"}},
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			var p struct {
				Instance int64
				Database string
			}
			json.Unmarshal(raw, &p)
			d, err := svc(env).dbInstance(env.Host, p.Instance)
			if err != nil {
				return change.Plan{}, err
			}
			if d.Container == "" && d.Native == "" || d.Engine == "redis" || d.Engine == "sqlite" {
				return change.Plan{}, errs.New(409, "db.import_container_only")
			}
			return change.Plan{Summary: "plan.db.export", SummaryP: map[string]any{"db": p.Database, "instance": d.Name}, Risk: change.RiskLow}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			a := svc(env)
			var p struct {
				Instance int64
				Database string
			}
			json.Unmarshal(raw, &p)
			d, err := a.dbInstance(env.Host, p.Instance)
			if err != nil {
				return nil, nil, err
			}
			path := dropDumpPath(d, p.Database, time.Now().Format("20060102-150405")+"-export")
			if err := a.dumpDB(env, d, p.Database, path); err != nil {
				return nil, nil, err
			}
			env.Log("saved to %s", path)
			return map[string]string{"path": path}, nil, nil
		},
	})
}

// loadCmd is how the plan describes where a dump is loaded.
func loadCmd(d dbInstance) string {
	if d.Container == "" {
		return d.Engine + " client on this host (" + d.Addr + ")"
	}
	return "docker exec -i " + d.Container
}
