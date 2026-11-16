package panel

// Databases (方案 §5.7): registered instances (containers found here or external
// addresses), browsing and read-only queries, and admin changes through the pipeline.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/wangsirco/berth/internal/agent/dbconn"
	"github.com/wangsirco/berth/internal/change"
	"github.com/wangsirco/berth/internal/errs"
)

var engineDefaultPort = map[string]string{"postgres": "5432", "mysql": "3306", "mariadb": "3306", "redis": "6379", "sqlite": "-"}

// engineOf guesses the engine from an image name.
func engineOf(image string) string {
	i := strings.ToLower(image)
	switch {
	case strings.Contains(i, "postgres") || strings.Contains(i, "pgvecto") || strings.Contains(i, "timescale"):
		return "postgres"
	case strings.Contains(i, "mariadb"):
		return "mariadb"
	case strings.Contains(i, "mysql") || strings.Contains(i, "percona"):
		return "mysql"
	case strings.Contains(i, "redis") || strings.Contains(i, "valkey") || strings.Contains(i, "keydb"):
		return "redis"
	}
	return ""
}

type dbInstance struct {
	ID        int64  `json:"id"`
	Host      string `json:"host"`
	Name      string `json:"name"`
	Engine    string `json:"engine"`
	Container string `json:"container,omitempty"`
	Addr      string `json:"addr,omitempty"`   // host:port, or the file's path for SQLite
	Native    string `json:"native,omitempty"` // a server Berth installed here: "mariadb 11.8"
	User      string `json:"user"`
	CreatedAt int64  `json:"created_at"`
	Status    any    `json:"status,omitempty"`
	passEnc   []byte
}

const dbCols = `id, host, name, engine, container, addr, admin_user, admin_pass_enc, created_at, native`

func scanDB(row interface{ Scan(...any) error }) (dbInstance, error) {
	var d dbInstance
	err := row.Scan(&d.ID, &d.Host, &d.Name, &d.Engine, &d.Container, &d.Addr, &d.User, &d.passEnc, &d.CreatedAt, &d.Native)
	return d, err
}

func (a *App) dbInstances(host string) ([]dbInstance, error) {
	rows, err := a.db.Query(`SELECT `+dbCols+` FROM databases WHERE host=? ORDER BY name`, host)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []dbInstance{}
	for rows.Next() {
		d, err := scanDB(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (a *App) dbInstance(host string, id int64) (dbInstance, error) {
	d, err := scanDB(a.db.QueryRow(`SELECT `+dbCols+` FROM databases WHERE host=? AND id=?`, host, id))
	if errors.Is(err, sql.ErrNoRows) {
		return d, errs.New(404, "db.not_found", "id", id)
	}
	return d, err
}

// target resolves how the agent reaches an instance: a container's address on its
// network, or the address given.
func (a *App) dbTarget(ctx context.Context, d dbInstance, database string) (dbconn.Target, error) {
	t := dbconn.Target{Engine: d.Engine, User: d.User, Database: database}
	if len(d.passEnc) > 0 {
		p, err := a.open(d.passEnc)
		if err != nil {
			return t, err
		}
		t.Password = string(p)
	}
	if d.Addr != "" {
		t.Addr = d.Addr
		return t, nil
	}
	cs, err := a.containers(ctx, d.Host)
	if err != nil {
		return t, err
	}
	for _, c := range cs {
		if c.Name == d.Container {
			for _, ip := range c.IPs {
				t.Addr = net.JoinHostPort(ip, engineDefaultPort[d.Engine])
				return t, nil
			}
			return t, errs.New(409, "db.no_address", "container", d.Container)
		}
	}
	return t, errs.New(404, "container.not_found", "detail", d.Container)
}

// storeCredentials: what the store recorded for a database app, to prefill a form.
func (a *App) storeCredentials(host, container string) (user, pass string) {
	rows, err := a.db.Query(`SELECT name FROM apps WHERE host=?`, host)
	if err != nil {
		return
	}
	var names []string
	for rows.Next() {
		var n string
		rows.Scan(&n)
		names = append(names, n)
	}
	rows.Close()
	for _, n := range names {
		if !strings.HasPrefix(container, n+"-") {
			continue
		}
		in, err := a.appInstance(host, n)
		if err != nil {
			continue
		}
		all, err := a.appSecrets(in)
		if err != nil {
			continue
		}
		switch {
		case all["ROOT_PASSWORD"] != "":
			return "root", all["ROOT_PASSWORD"]
		case all["DB_PASSWORD"] != "":
			return all["DB_USER"], all["DB_PASSWORD"]
		case all["REDIS_PASSWORD"] != "":
			return "", all["REDIS_PASSWORD"]
		}
	}
	return
}

var dbNameRe = regexp.MustCompile(`^[A-Za-z0-9_]{1,63}$`)

type dbParams struct {
	Instance int64  `json:"instance"`
	Name     string `json:"name,omitempty"`     // database
	User     string `json:"user,omitempty"`     // database user
	Password string `json:"password,omitempty"` // for new users and password changes
	SQL      string `json:"sql,omitempty"`
	Database string `json:"database,omitempty"`
}

func (a *App) dbAdmin(env change.Env, p dbParams, action string) error {
	d, err := a.dbInstance(env.Host, p.Instance)
	if err != nil {
		return err
	}
	t, err := a.dbTarget(env.Ctx, d, "")
	if err != nil {
		return err
	}
	return a.call(env.Ctx, env.Host, "POST", "/v1/db/admin", map[string]any{"engine": t.Engine, "addr": t.Addr, "user": t.User, "password": t.Password,
		"database": t.Database, "Action": action, "Name": p.Name, "User": p.User, "NewPassword": p.Password}, nil)
}

func dbObj(d dbInstance, sub string) change.Object {
	o := change.Object{Type: "database", ID: strconv.FormatInt(d.ID, 10), Name: d.Name, Host: d.Host}
	if sub != "" {
		o.ID += "/" + sub
		o.Name += "/" + sub
	}
	return o
}

func init() {
	routeHooks = append(routeHooks, func(a *App, mux *http.ServeMux) {
		mux.HandleFunc("GET /api/v1/hosts/{host}/databases", a.authed("db.view", a.handleDatabases))
		mux.HandleFunc("POST /api/v1/hosts/{host}/databases/{id}/meta", a.authed("db.view", a.handleDBMeta))
		mux.HandleFunc("POST /api/v1/hosts/{host}/databases/{id}/query", a.authed("db.query", a.handleDBQuery))
		mux.HandleFunc("POST /api/v1/hosts/{host}/databases/{id}/redis", a.authed("db.query", a.handleDBRedis))
	})

	idProps := map[string]change.Prop{"instance": {Type: "integer"}, "name": {Type: "string"}}
	change.Register(change.Kind{
		Name: "db.instance.add", Perm: "db.manage", Scope: "system:write", Title: "kind.db.instance.add",
		Params: change.ParamSchema{Props: map[string]change.Prop{
			"name": {Type: "string"}, "engine": {Type: "string", Enum: []string{"postgres", "mysql", "mariadb", "redis", "sqlite"}},
			"container": {Type: "string"}, "addr": {Type: "string", Desc: "host:port for an external instance; the file's path for sqlite"},
			"user": {Type: "string"}, "password": {Type: "string"}}, Required: []string{"name", "engine"}},
		Redact: []string{"password"}, CLI: "berth db add {{.name}} --engine {{.engine}}",
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			var p struct{ Name, Engine, Container, Addr, User, Password string }
			json.Unmarshal(raw, &p)
			if !dbNameRe.MatchString(strings.ReplaceAll(p.Name, "-", "_")) || engineDefaultPort[p.Engine] == "" || (p.Container == "") == (p.Addr == "") {
				return change.Plan{}, errs.New(400, "db.bad_request")
			}
			a := svc(env)
			if p.Password == "" && p.Container != "" { // installed from the store: use what it recorded
				if u, pw := a.storeCredentials(env.Host, p.Container); pw != "" {
					p.User, p.Password = firstNonEmpty(p.User, u), pw
				}
			}
			d := dbInstance{Host: env.Host, Name: p.Name, Engine: p.Engine, Container: p.Container, Addr: p.Addr, User: p.User}
			if p.Password != "" {
				d.passEnc, _ = a.seal([]byte(p.Password))
			}
			if err := a.dbPing(env.Ctx, d); err != nil {
				return change.Plan{}, err
			}
			return change.Plan{Summary: "plan.db.instance.add", SummaryP: map[string]any{"name": p.Name, "engine": p.Engine}, Risk: change.RiskLow, Undoable: true,
				Affected: []change.Object{{Type: "database", ID: p.Name, Name: p.Name, Host: env.Host}}}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			var p struct{ Name, Engine, Container, Addr, User, Password string }
			json.Unmarshal(raw, &p)
			a := svc(env)
			if p.Password == "" && p.Container != "" {
				if u, pw := a.storeCredentials(env.Host, p.Container); pw != "" {
					p.User, p.Password = firstNonEmpty(p.User, u), pw
				}
			}
			enc, err := a.seal([]byte(p.Password))
			if err != nil {
				return nil, nil, err
			}
			res, err := a.db.Exec(`INSERT INTO databases(host, name, engine, container, addr, admin_user, admin_pass_enc, created_at) VALUES(?,?,?,?,?,?,?,?)`,
				env.Host, p.Name, p.Engine, p.Container, p.Addr, p.User, enc, time.Now().Unix())
			if err != nil {
				return nil, nil, errs.New(409, "db.exists", "name", p.Name)
			}
			id, _ := res.LastInsertId()
			if p.Container != "" {
				a.relate(env.Host, change.Object{Type: "database", ID: strconv.FormatInt(id, 10), Name: p.Name}, change.Object{Type: "container", ID: p.Container, Name: p.Container}, "runs_in", "module")
			}
			u, _ := json.Marshal(map[string]int64{"instance": id})
			return map[string]int64{"id": id}, &change.UndoRecipe{Kind: "db.instance.remove", Params: u}, nil
		},
	})
	change.Register(change.Kind{
		Name: "db.instance.remove", Perm: "db.manage", Scope: "system:write", Title: "kind.db.instance.remove",
		Params: change.ParamSchema{Props: idProps, Required: []string{"instance"}}, CLI: "berth db forget {{.instance}}",
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			var p dbParams
			json.Unmarshal(raw, &p)
			d, err := svc(env).dbInstance(env.Host, p.Instance)
			if err != nil {
				return change.Plan{}, err
			}
			return change.Plan{Summary: "plan.db.instance.remove", SummaryP: map[string]any{"name": d.Name}, Risk: change.RiskLow, Affected: []change.Object{dbObj(d, "")}}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			var p dbParams
			json.Unmarshal(raw, &p)
			_, err := svc(env).db.Exec(`DELETE FROM databases WHERE host=? AND id=?`, env.Host, p.Instance)
			return nil, nil, err
		},
	})
	change.Register(change.Kind{
		Name: "db.create", Perm: "db.manage", Scope: "system:write", Title: "kind.db.create",
		Params: change.ParamSchema{Props: map[string]change.Prop{"instance": {Type: "integer"}, "name": {Type: "string"}, "user": {Type: "string", Desc: "owner (PostgreSQL)"}}, Required: []string{"instance", "name"}},
		CLI:    "berth db create {{.instance}} {{.name}}",
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			var p dbParams
			json.Unmarshal(raw, &p)
			d, err := svc(env).dbInstance(env.Host, p.Instance)
			if err != nil {
				return change.Plan{}, err
			}
			if !dbNameRe.MatchString(p.Name) {
				return change.Plan{}, errs.New(400, "db.bad_name")
			}
			return change.Plan{Summary: "plan.db.create", SummaryP: map[string]any{"name": p.Name, "instance": d.Name}, Risk: change.RiskLow, Undoable: true,
				Commands: []string{"CREATE DATABASE " + p.Name}, Affected: []change.Object{dbObj(d, p.Name)}}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			var p dbParams
			json.Unmarshal(raw, &p)
			if err := svc(env).dbAdmin(env, p, "create_db"); err != nil {
				return nil, nil, err
			}
			u, _ := json.Marshal(dbParams{Instance: p.Instance, Name: p.Name})
			return nil, &change.UndoRecipe{Kind: "db.drop", Params: u}, nil
		},
	})
	change.Register(change.Kind{
		Name: "db.drop", Perm: "db.manage", Scope: "system:write", Title: "kind.db.drop",
		Params: change.ParamSchema{Props: idProps, Required: []string{"instance", "name"}}, CLI: "berth db drop {{.instance}} {{.name}}",
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			var p dbParams
			json.Unmarshal(raw, &p)
			d, err := svc(env).dbInstance(env.Host, p.Instance)
			if err != nil {
				return change.Plan{}, err
			}
			plan := change.Plan{Summary: "plan.db.drop", SummaryP: map[string]any{"name": p.Name, "instance": d.Name}, Risk: change.RiskDestructive,
				Commands: []string{"DROP DATABASE " + p.Name}, Affected: []change.Object{{Type: "database", ID: p.Name, Name: p.Name, Host: env.Host}}}
			if d.Container != "" && d.Engine != "redis" {
				plan.Commands = append([]string{"# " + dropDumpPath(d, p.Name, "<time>")}, plan.Commands...)
			}
			return plan, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			var p dbParams
			json.Unmarshal(raw, &p)
			a := svc(env)
			d, err := a.dbInstance(env.Host, p.Instance)
			if err != nil {
				return nil, nil, err
			}
			res := map[string]any{}
			if d.Container != "" && d.Engine != "redis" { // a copy first: dropping is otherwise final
				t, err := a.dbTarget(env.Ctx, d, p.Name)
				if err != nil {
					return nil, nil, err
				}
				path := dropDumpPath(d, p.Name, time.Now().Format("20060102-150405"))
				var out map[string]any
				if err := a.call(env.Ctx, env.Host, "POST", "/v1/db/dump", map[string]any{"engine": t.Engine, "addr": t.Addr, "user": t.User, "password": t.Password,
					"database": p.Name, "container": d.Container, "path": path}, &out); err != nil {
					return nil, nil, err
				}
				env.Log("saved a copy to %s", path)
				res["dump"] = path
			}
			if err := a.dbAdmin(env, p, "drop_db"); err != nil {
				return nil, nil, err
			}
			return res, nil, nil
		},
	})
	userProps := map[string]change.Prop{"instance": {Type: "integer"}, "user": {Type: "string"}, "password": {Type: "string"}, "name": {Type: "string", Desc: "database to grant"}}
	change.Register(change.Kind{
		Name: "db.user.create", Perm: "db.manage", Scope: "system:write", Title: "kind.db.user.create",
		Params: change.ParamSchema{Props: userProps, Required: []string{"instance", "user", "password"}}, Redact: []string{"password"},
		CLI: "berth db user add {{.instance}} {{.user}}",
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			var p dbParams
			json.Unmarshal(raw, &p)
			d, err := svc(env).dbInstance(env.Host, p.Instance)
			if err != nil {
				return change.Plan{}, err
			}
			if !dbNameRe.MatchString(p.User) || len(p.Password) < 8 {
				return change.Plan{}, errs.New(400, "db.bad_user")
			}
			return change.Plan{Summary: "plan.db.user.create", SummaryP: map[string]any{"user": p.User, "db": firstNonEmpty(p.Name, "—"), "instance": d.Name},
				Risk: change.RiskMedium, Undoable: true, Affected: []change.Object{dbObj(d, "user:"+p.User)}}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			var p dbParams
			json.Unmarshal(raw, &p)
			if err := svc(env).dbAdmin(env, p, "create_user"); err != nil {
				return nil, nil, err
			}
			u, _ := json.Marshal(dbParams{Instance: p.Instance, User: p.User})
			return nil, &change.UndoRecipe{Kind: "db.user.drop", Params: u}, nil
		},
	})
	change.Register(change.Kind{
		Name: "db.user.drop", Perm: "db.manage", Scope: "system:write", Title: "kind.db.user.drop",
		Params: change.ParamSchema{Props: userProps, Required: []string{"instance", "user"}}, CLI: "berth db user rm {{.instance}} {{.user}}",
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			var p dbParams
			json.Unmarshal(raw, &p)
			d, err := svc(env).dbInstance(env.Host, p.Instance)
			if err != nil {
				return change.Plan{}, err
			}
			return change.Plan{Summary: "plan.db.user.drop", SummaryP: map[string]any{"user": p.User, "instance": d.Name}, Risk: change.RiskHigh, Affected: []change.Object{dbObj(d, "user:"+p.User)}}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			var p dbParams
			json.Unmarshal(raw, &p)
			return nil, nil, svc(env).dbAdmin(env, p, "drop_user")
		},
	})
	change.Register(change.Kind{
		Name: "db.user.password", Perm: "db.manage", Scope: "system:write", Title: "kind.db.user.password",
		Params: change.ParamSchema{Props: userProps, Required: []string{"instance", "user", "password"}}, Redact: []string{"password"},
		CLI: "berth db user password {{.instance}} {{.user}}",
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			var p dbParams
			json.Unmarshal(raw, &p)
			d, err := svc(env).dbInstance(env.Host, p.Instance)
			if err != nil {
				return change.Plan{}, err
			}
			if len(p.Password) < 8 {
				return change.Plan{}, errs.New(400, "db.bad_user")
			}
			return change.Plan{Summary: "plan.db.user.password", SummaryP: map[string]any{"user": p.User, "instance": d.Name}, Risk: change.RiskMedium, Affected: []change.Object{dbObj(d, "user:"+p.User)}}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			var p dbParams
			json.Unmarshal(raw, &p)
			return nil, nil, svc(env).dbAdmin(env, p, "password")
		},
	})
	change.Register(change.Kind{
		Name: "db.exec", Perm: "db.manage", Scope: "system:write", Title: "kind.db.exec",
		Params: change.ParamSchema{Props: map[string]change.Prop{"instance": {Type: "integer"}, "database": {Type: "string"}, "sql": {Type: "string"}}, Required: []string{"instance", "sql"}},
		CLI:    "berth db exec {{.instance}} {{.database}}",
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			var p dbParams
			json.Unmarshal(raw, &p)
			d, err := svc(env).dbInstance(env.Host, p.Instance)
			if err != nil {
				return change.Plan{}, err
			}
			plan := change.Plan{Summary: "plan.db.exec", SummaryP: map[string]any{"instance": d.Name, "db": firstNonEmpty(p.Database, "—")}, Risk: change.RiskHigh,
				Commands: []string{strings.TrimSpace(p.SQL)}, Affected: []change.Object{dbObj(d, p.Database)}}
			if k := dbconn.Danger(p.SQL); k != "" {
				plan.Risk = change.RiskDestructive
				plan.Summary = "plan.db.exec_danger_" + k
			}
			if d.Engine == "sqlite" {
				plan.Undoable = true
				plan.Commands = append([]string{"VACUUM INTO " + dbconn.SQLiteBackups + "/" + strconv.FormatInt(d.ID, 10) + "/…   (a copy to undo from)"}, plan.Commands...)
			}
			return plan, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			var p dbParams
			json.Unmarshal(raw, &p)
			a := svc(env)
			d, err := a.dbInstance(env.Host, p.Instance)
			if err != nil {
				return nil, nil, err
			}
			t, err := a.dbTarget(env.Ctx, d, p.Database)
			if err != nil {
				return nil, nil, err
			}
			// SQLite: a copy first, so the change can be undone
			var undo *change.UndoRecipe
			if d.Engine == "sqlite" {
				var b dbconn.SQLiteBackup
				if err := a.call(env.Ctx, env.Host, "POST", "/v1/sqlite/backup", map[string]any{"path": d.Addr, "key": strconv.FormatInt(d.ID, 10)}, &b); err != nil {
					return nil, nil, err
				}
				env.Log("backed up as %s", b.Name)
				u, _ := json.Marshal(map[string]any{"instance": d.ID, "backup": b.Name})
				undo = &change.UndoRecipe{Kind: "db.sqlite.restore", Params: u}
			}
			var res dbconn.Result
			err = a.call(env.Ctx, env.Host, "POST", "/v1/db/query", map[string]any{"engine": t.Engine, "addr": t.Addr, "user": t.User, "password": t.Password,
				"database": t.Database, "sql": p.SQL, "readonly": false, "limit": 200}, &res)
			if err != nil {
				return nil, nil, err
			}
			return res, undo, nil
		},
	})
}

func dropDumpPath(d dbInstance, name, ts string) string {
	return "/srv/berth/backups/db-drops/" + d.Name + "-" + name + "-" + ts + ".sql.gz"
}

func (a *App) dbPing(ctx context.Context, d dbInstance) error {
	t, err := a.dbTarget(ctx, d, "")
	if err != nil {
		return err
	}
	if d.Engine == "redis" {
		return a.call(ctx, d.Host, "POST", "/v1/db/redis", map[string]any{"engine": "redis", "addr": t.Addr, "user": t.User, "password": t.Password, "args": []string{"dbsize"}}, nil)
	}
	return a.call(ctx, d.Host, "POST", "/v1/db/meta", map[string]any{"engine": t.Engine, "addr": t.Addr, "user": t.User, "password": t.Password, "what": "status"}, nil)
}

// ---- API ----

func (a *App) handleDatabases(w http.ResponseWriter, r *http.Request) {
	host := r.PathValue("host")
	list, err := a.dbInstances(host)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	known := map[string]bool{}
	for i := range list {
		known[list[i].Container] = true
		ctx, cancel := context.WithTimeout(r.Context(), 4*time.Second)
		if t, err := a.dbTarget(ctx, list[i], ""); err == nil && list[i].Engine != "redis" {
			var st dbconn.Result
			if a.call(ctx, host, "POST", "/v1/db/meta", map[string]any{"engine": t.Engine, "addr": t.Addr, "user": t.User, "password": t.Password, "what": "status"}, &st) == nil && len(st.Rows) > 0 {
				list[i].Status = map[string]any{"version": st.Rows[0][0], "connections": st.Rows[0][1]}
			} else {
				list[i].Status = map[string]any{"error": true}
			}
		}
		cancel()
	}
	type cand struct {
		Container string `json:"container"`
		Image     string `json:"image"`
		Engine    string `json:"engine"`
		User      string `json:"user,omitempty"`
		HasPass   bool   `json:"has_password"`
	}
	cands := []cand{}
	if cs, err := a.containers(r.Context(), host); err == nil {
		for _, c := range cs {
			if e := engineOf(c.Image); e != "" && c.State == "running" && !known[c.Name] {
				u, p := a.storeCredentials(host, c.Name)
				cands = append(cands, cand{Container: c.Name, Image: c.Image, Engine: e, User: u, HasPass: p != ""})
			}
		}
	}
	writeJSON(w, 200, map[string]any{"items": list, "candidates": cands})
}

func (a *App) dbFromRequest(r *http.Request, database string) (dbInstance, dbconn.Target, error) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	d, err := a.dbInstance(r.PathValue("host"), id)
	if err != nil {
		return d, dbconn.Target{}, err
	}
	t, err := a.dbTarget(r.Context(), d, database)
	return d, t, err
}

func targetBody(t dbconn.Target) map[string]any {
	return map[string]any{"engine": t.Engine, "addr": t.Addr, "user": t.User, "password": t.Password, "database": t.Database}
}

func (a *App) handleDBMeta(w http.ResponseWriter, r *http.Request) {
	var in struct{ Database, What, Table string }
	readJSON(r, &in)
	d, t, err := a.dbFromRequest(r, in.Database)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	body := targetBody(t)
	body["what"], body["table"] = in.What, in.Table
	var res dbconn.Result
	if err := a.call(r.Context(), d.Host, "POST", "/v1/db/meta", body, &res); err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, 200, res)
}

// handleDBQuery runs a read-only query; changes go through the db.exec kind.
func (a *App) handleDBQuery(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Database, SQL string
		Limit         int
	}
	readJSON(r, &in)
	d, t, err := a.dbFromRequest(r, in.Database)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	body := targetBody(t)
	body["sql"], body["readonly"], body["limit"] = in.SQL, true, in.Limit
	var res dbconn.Result
	if err := a.call(r.Context(), d.Host, "POST", "/v1/db/query", body, &res); err != nil {
		e := errs.From(err)
		if det, _ := e.Params["detail"].(string); strings.Contains(strings.ToLower(det), "read-only") || strings.Contains(strings.ToLower(det), "read only") || strings.Contains(strings.ToLower(det), "readonly") {
			a.fail(w, r, errs.New(409, "db.readonly"))
			return
		}
		a.fail(w, r, err)
		return
	}
	writeJSON(w, 200, res)
}

func (a *App) handleDBRedis(w http.ResponseWriter, r *http.Request) {
	var in struct{ Args []string }
	readJSON(r, &in)
	d, t, err := a.dbFromRequest(r, "")
	if err != nil {
		a.fail(w, r, err)
		return
	}
	body := targetBody(t)
	body["args"] = in.Args
	var out any
	if err := a.call(r.Context(), d.Host, "POST", "/v1/db/redis", body, &out); err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, 200, out)
}
