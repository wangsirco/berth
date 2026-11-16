package panel

// Native databases (R4): install MariaDB/MySQL/PostgreSQL on the host and register it as an
// instance, tune it, choose who may reach it, copy databases between instances, and look after
// SQLite files.

import (
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/wangsirco/berth/internal/agent/dbconn"
	"github.com/wangsirco/berth/internal/agent/system"
	"github.com/wangsirco/berth/internal/change"
	"github.com/wangsirco/berth/internal/errs"
)

// what the install form offers (the first version of each is the default)
var dbNativeVersions = map[string][]string{"mariadb": {"11.8", "12"}, "mysql": {"8.4", "9"}, "postgres": {"18", "17"}, "redis": {"valkey"}}

// dbFam: MariaDB and MySQL share a package family and a port; the others stand alone.
func dbFam(e string) string {
	if e == "mariadb" || e == "mysql" {
		return "mysql"
	}
	return e
}

var dbNativePort = map[string]int{"mysql": 3306, "postgres": 5432, "redis": 6379}

func (a *App) dbNatives(env change.Env) []system.DBNative {
	var out struct{ Items []system.DBNative }
	a.call(env.Ctx, env.Host, "GET", "/v1/dbnative", nil, &out)
	return out.Items
}

// nativeOf: the agent's record for a native instance.
func (a *App) nativeOf(env change.Env, d dbInstance) (system.DBNative, error) {
	if d.Native == "" {
		return system.DBNative{}, errs.New(409, "dbn.not_native")
	}
	for _, n := range a.dbNatives(env) {
		if n.Engine == d.Engine {
			return n, nil
		}
	}
	return system.DBNative{}, errs.New(404, "dbn.not_installed", "engine", d.Engine)
}

func (a *App) hostMemMB(r *http.Request, host string) int {
	var i struct {
		MemTotal int64 `json:"mem_total"`
	}
	a.call(r.Context(), host, "GET", "/v1/system/info", nil, &i)
	return int(i.MemTotal >> 20)
}

func init() {
	routeHooks = append(routeHooks, func(a *App, mux *http.ServeMux) {
		// the install form: engines, versions, what is installed, and whether a port is taken by a container
		mux.HandleFunc("GET /api/v1/hosts/{host}/databases/native", a.authed("db.view", func(w http.ResponseWriter, r *http.Request) {
			host := r.PathValue("host")
			var out struct{ Items []system.DBNative }
			if err := a.call(r.Context(), host, "GET", "/v1/dbnative", nil, &out); err != nil {
				a.fail(w, r, err)
				return
			}
			busy := map[string]string{}
			if cs, err := a.containers(r.Context(), host); err == nil {
				for _, c := range cs {
					for _, p := range c.Ports {
						if (p.Public == 3306 || p.Public == 5432 || p.Public == 6379) && c.State == "running" {
							busy[strconv.Itoa(p.Public)] = c.Name
						}
					}
				}
			}
			writeJSON(w, 200, map[string]any{"items": out.Items, "versions": dbNativeVersions, "busy": busy, "mem_mb": a.hostMemMB(r, host)})
		}))
		// tuning: what Berth wrote, what runs, and what it recommends for a memory size
		mux.HandleFunc("GET /api/v1/hosts/{host}/databases/{id}/tune", a.authed("db.view", func(w http.ResponseWriter, r *http.Request) {
			host := r.PathValue("host")
			id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
			d, err := a.dbInstance(host, id)
			if err != nil {
				a.fail(w, r, err)
				return
			}
			env := change.Env{Ctx: r.Context(), Host: host}
			n, err := a.nativeOf(env, d)
			if err != nil {
				a.fail(w, r, err)
				return
			}
			total := a.hostMemMB(r, host)
			mem, _ := strconv.Atoi(r.URL.Query().Get("mem"))
			if mem <= 0 {
				mem = n.MemMB
			}
			if mem <= 0 {
				mem = min(max(total/4, 256), 8192)
			}
			var live struct{ Values map[string]string }
			a.call(r.Context(), host, "GET", "/v1/dbnative/"+d.Engine+"/live", nil, &live)
			writeJSON(w, 200, map[string]any{"native": n, "live": live.Values, "mem_mb": mem, "host_mem_mb": total,
				"recommend": system.RecommendDB(d.Engine, n.Version, mem)})
		}))
		mux.HandleFunc("GET /api/v1/hosts/{host}/databases/{id}/stats", a.authed("db.view", a.handleDBStats))
		mux.HandleFunc("GET /api/v1/hosts/{host}/databases/sqlite/discover", a.authed("db.view", func(w http.ResponseWriter, r *http.Request) {
			host := r.PathValue("host")
			var out struct{ Items []dbconn.Found }
			if err := a.call(r.Context(), host, "GET", "/v1/sqlite/discover", nil, &out); err != nil {
				a.fail(w, r, err)
				return
			}
			known := map[string]bool{}
			if list, err := a.dbInstances(host); err == nil {
				for _, d := range list {
					if d.Engine == "sqlite" {
						known[d.Addr] = true
					}
				}
			}
			type item struct {
				dbconn.Found
				Registered bool   `json:"registered"`
				Project    string `json:"project,omitempty"`
				Site       string `json:"site,omitempty"`
			}
			items := []item{}
			for _, f := range out.Items {
				it := item{Found: f, Registered: known[f.Path]}
				if rest, ok := strings.CutPrefix(f.Path, system.NativeRoot+"/"); ok {
					it.Project = strings.SplitN(rest, "/", 2)[0]
				}
				if rest, ok := strings.CutPrefix(f.Path, "/srv/berth/caddy/sites/"); ok {
					it.Site = strings.SplitN(rest, "/", 2)[0]
				}
				items = append(items, it)
			}
			writeJSON(w, 200, map[string]any{"items": items})
		}))
		mux.HandleFunc("GET /api/v1/hosts/{host}/databases/{id}/sqlite", a.authed("db.view", func(w http.ResponseWriter, r *http.Request) {
			host := r.PathValue("host")
			id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
			d, err := a.dbInstance(host, id)
			if err != nil || d.Engine != "sqlite" {
				a.fail(w, r, errs.New(404, "db.not_found", "id", id))
				return
			}
			var info dbconn.SQLiteInfo
			if err := a.call(r.Context(), host, "POST", "/v1/sqlite/info", map[string]any{"path": d.Addr, "key": strconv.FormatInt(d.ID, 10)}, &info); err != nil {
				a.fail(w, r, err)
				return
			}
			writeJSON(w, 200, info)
		}))
	})

	engines := []string{"mariadb", "mysql", "postgres", "redis"}
	change.Register(change.Kind{
		Name: "db.native.install", Perm: "db.manage", Scope: "system:write", Title: "kind.db.native.install", CLI: "berth db native install {{.engine}} {{.version}}",
		Heavy: true, MemMB: 300,
		Params: change.ParamSchema{Props: map[string]change.Prop{"engine": {Type: "string", Enum: engines}, "version": {Type: "string", Desc: "mariadb 11.8|12, mysql 8.4|9, postgres 18|17, redis valkey"},
			"name": {Type: "string", Desc: "instance name, default <engine>"}}, Required: []string{"engine"}},
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			p, err := dbNativeInstallParams(raw)
			if err != nil {
				return change.Plan{}, err
			}
			a := svc(env)
			for _, n := range a.dbNatives(env) {
				if dbFam(n.Engine) == dbFam(p.Engine) {
					return change.Plan{}, errs.New(409, "dbn.exists", "engine", n.Engine, "version", n.Version)
				}
			}
			port := dbNativePort[dbFam(p.Engine)]
			if cs, err := a.containers(env.Ctx, env.Host); err == nil {
				for _, c := range cs {
					for _, cp := range c.Ports {
						if cp.Public == port && c.State == "running" {
							return change.Plan{}, errs.New(409, "dbn.port_busy", "port", strconv.Itoa(port), "by", c.Name)
						}
					}
				}
			}
			return change.Plan{Summary: "plan.db.native.install", SummaryP: map[string]any{"engine": p.Engine, "version": p.Version}, Risk: change.RiskMedium, Undoable: true,
				Commands: []string{"install " + p.Engine + " " + p.Version + " from the system's packages and start it on port " + strconv.Itoa(port),
					map[bool]string{true: "set a random password (stored encrypted); it always needs one", false: "make a login for Berth (random password, stored encrypted); root stays on the local socket"}[p.Engine == "redis"],
					"keep port " + strconv.Itoa(port) + " to this server and its containers (nft table berth_db)",
					"register it as the instance \"" + p.Name + "\""},
				Affected: []change.Object{{Type: "database", ID: p.Name, Name: p.Name, Host: env.Host}}}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			p, err := dbNativeInstallParams(raw)
			if err != nil {
				return nil, nil, err
			}
			a := svc(env)
			var res system.DBInstallResult
			pw := randomPassword(28)
			if err := a.streamAgent(env, env.Host, "/v1/dbnative/install", map[string]any{"engine": p.Engine, "version": p.Version, "password": pw}, &res); err != nil {
				return nil, nil, err
			}
			enc, err := a.seal([]byte(pw))
			if err != nil {
				return nil, nil, err
			}
			ins, err := a.db.Exec(`INSERT INTO databases(host, name, engine, container, addr, admin_user, admin_pass_enc, created_at, native) VALUES(?,?,?,?,?,?,?,?,?)`,
				env.Host, p.Name, p.Engine, "", res.Addr, res.User, enc, time.Now().Unix(), p.Engine+" "+p.Version)
			if err != nil {
				return nil, nil, errs.New(409, "db.exists", "name", p.Name)
			}
			id, _ := ins.LastInsertId()
			u, _ := json.Marshal(map[string]int64{"instance": id})
			return map[string]any{"id": id, "addr": res.Addr}, &change.UndoRecipe{Kind: "db.native.remove", Params: u}, nil
		},
	})
	change.Register(change.Kind{
		Name: "db.native.remove", Perm: "db.manage", Scope: "system:write", Title: "kind.db.native.remove", CLI: "berth db native remove {{.instance}}",
		Heavy: true, Params: change.ParamSchema{Props: map[string]change.Prop{"instance": {Type: "integer"}}, Required: []string{"instance"}},
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			var p dbParams
			json.Unmarshal(raw, &p)
			a := svc(env)
			d, err := a.dbInstance(env.Host, p.Instance)
			if err != nil {
				return change.Plan{}, err
			}
			n, err := a.nativeOf(env, d)
			if err != nil {
				return change.Plan{}, err
			}
			return change.Plan{Summary: "plan.db.native.remove", SummaryP: map[string]any{"name": d.Name, "dir": n.DataDir}, Risk: change.RiskHigh,
				Commands: []string{"systemctl disable --now " + n.Service, "uninstall the server's packages", "forget the instance in Berth",
					n.DataDir + " stays (all its databases); delete it yourself if you no longer need it"},
				Affected: []change.Object{dbObj(d, "")}}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			var p dbParams
			json.Unmarshal(raw, &p)
			a := svc(env)
			d, err := a.dbInstance(env.Host, p.Instance)
			if err != nil {
				return nil, nil, err
			}
			if err := agentOp(env, "/v1/dbnative/remove", map[string]any{"engine": d.Engine}); err != nil {
				return nil, nil, err
			}
			_, err = a.db.Exec(`DELETE FROM databases WHERE host=? AND id=?`, env.Host, d.ID)
			return nil, nil, err
		},
	})
	change.Register(change.Kind{
		Name: "db.tune", Perm: "db.manage", Scope: "system:write", Title: "kind.db.tune", CLI: "berth db tune {{.instance}}",
		Heavy: true,
		Params: change.ParamSchema{Props: map[string]change.Prop{"instance": {Type: "integer"}, "mem_mb": {Type: "integer", Desc: "memory the server is sized for"},
			"values": {Type: "object", Desc: "settings, e.g. {\"innodb_buffer_pool_size\":\"1G\"}; omit for the recommended ones"}}, Required: []string{"instance"}},
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			a := svc(env)
			d, n, p, err := a.tuneParams(env, raw)
			if err != nil {
				return change.Plan{}, err
			}
			cmds := []string{}
			for _, k := range sortedKeys(p.Values) {
				was := n.Values[k]
				if was == p.Values[k] {
					continue
				}
				if was == "" {
					was = "default"
				}
				cmds = append(cmds, k+": "+was+" → "+p.Values[k])
			}
			cmds = append(cmds, "restart "+n.Service+" (the database is unavailable for a few seconds); if it does not come back, the previous settings go back")
			return change.Plan{Summary: "plan.db.tune", SummaryP: map[string]any{"name": d.Name}, Risk: change.RiskMedium, Undoable: true, Commands: cmds, Affected: []change.Object{dbObj(d, "")}}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			a := svc(env)
			d, n, p, err := a.tuneParams(env, raw)
			if err != nil {
				return nil, nil, err
			}
			if err := agentOp(env, "/v1/dbnative/tune", map[string]any{"engine": d.Engine, "mem_mb": p.MemMB, "values": p.Values}); err != nil {
				return nil, nil, err
			}
			u, _ := json.Marshal(map[string]any{"instance": d.ID, "mem_mb": n.MemMB, "values": n.Values})
			return nil, &change.UndoRecipe{Kind: "db.tune", Params: u}, nil
		},
	})
	change.Register(change.Kind{
		Name: "db.native.allow", Perm: "db.manage", Scope: "system:write", Title: "kind.db.native.allow", CLI: "berth db allow {{.instance}}",
		Params: change.ParamSchema{Props: map[string]change.Prop{"instance": {Type: "integer"}, "sources": {Type: "array", Desc: "IPs or networks that may connect from outside; [] = this server only"}}, Required: []string{"instance", "sources"}},
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			var p struct {
				Instance int64
				Sources  []string
			}
			json.Unmarshal(raw, &p)
			a := svc(env)
			d, err := a.dbInstance(env.Host, p.Instance)
			if err != nil {
				return change.Plan{}, err
			}
			n, err := a.nativeOf(env, d)
			if err != nil {
				return change.Plan{}, err
			}
			risk, sum := change.RiskLow, "plan.db.native.allow_none"
			if len(p.Sources) > 0 {
				risk, sum = change.RiskMedium, "plan.db.native.allow"
			}
			return change.Plan{Summary: sum, SummaryP: map[string]any{"name": d.Name, "port": n.Port, "sources": strings.Join(p.Sources, ", ")}, Risk: risk, Undoable: true,
				Commands: []string{"nft table berth_db: port " + strconv.Itoa(n.Port) + " from " + firstNonEmpty(strings.Join(p.Sources, ", "), "nowhere outside this server")},
				Affected: []change.Object{dbObj(d, "")}}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			var p struct {
				Instance int64
				Sources  []string
			}
			json.Unmarshal(raw, &p)
			a := svc(env)
			d, err := a.dbInstance(env.Host, p.Instance)
			if err != nil {
				return nil, nil, err
			}
			n, err := a.nativeOf(env, d)
			if err != nil {
				return nil, nil, err
			}
			if p.Sources == nil {
				p.Sources = []string{}
			}
			if err := a.call(env.Ctx, env.Host, "POST", "/v1/dbnative/allow", map[string]any{"engine": d.Engine, "sources": p.Sources}, nil); err != nil {
				return nil, nil, err
			}
			u, _ := json.Marshal(map[string]any{"instance": d.ID, "sources": n.Allow})
			return nil, &change.UndoRecipe{Kind: "db.native.allow", Params: u}, nil
		},
	})
	change.Register(change.Kind{
		Name: "db.copy", Perm: "db.manage", Scope: "system:write", Title: "kind.db.copy", CLI: "berth db copy {{.from}}/{{.database}} {{.to}}",
		Heavy: true, MemMB: 200,
		Params: change.ParamSchema{Props: map[string]change.Prop{"from": {Type: "integer"}, "to": {Type: "integer"}, "database": {Type: "string"},
			"as": {Type: "string", Desc: "name on the target, default the same"}}, Required: []string{"from", "to", "database"}},
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			from, to, p, err := svc(env).copyParams(env, raw)
			if err != nil {
				return change.Plan{}, err
			}
			return change.Plan{Summary: "plan.db.copy", SummaryP: map[string]any{"db": p.Database, "from": from.Name, "to": to.Name, "as": p.As}, Risk: change.RiskLow, Undoable: true,
				Commands: []string{"dump " + from.Name + "/" + p.Database + " into /srv/berth/backups/db-copy/", "create " + to.Name + "/" + p.As + " and load the dump into it",
					"logins are not copied: make one on " + to.Name + " and point the site at it"},
				Affected: []change.Object{dbObj(to, p.As)}}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			a := svc(env)
			from, to, p, err := a.copyParams(env, raw)
			if err != nil {
				return nil, nil, err
			}
			path := "/srv/berth/backups/db-copy/" + from.Name + "-" + p.Database + "-" + time.Now().Format("20060102-150405") + ".sql.gz"
			env.Log("dumping %s/%s", from.Name, p.Database)
			if err := a.dumpDB(env, from, p.Database, path); err != nil {
				return nil, nil, err
			}
			env.Log("creating %s/%s", to.Name, p.As)
			if err := a.dbAdmin(env, dbParams{Instance: to.ID, Name: p.As}, "create_db"); err != nil {
				return nil, nil, err
			}
			t, err := a.dbTarget(env.Ctx, to, p.As)
			if err != nil {
				return nil, nil, err
			}
			env.Log("loading the dump")
			if err := a.call(env.Ctx, env.Host, "POST", "/v1/db/import", map[string]any{"engine": t.Engine, "addr": t.Addr, "user": t.User, "password": t.Password,
				"database": p.As, "container": to.Container, "path": path}, nil); err != nil {
				return nil, nil, err
			}
			u, _ := json.Marshal(map[string]any{"instance": to.ID, "name": p.As})
			return map[string]string{"dump": path}, &change.UndoRecipe{Kind: "db.drop", Params: u}, nil
		},
	})
	change.Register(change.Kind{
		Name: "db.sqlite.maintain", Perm: "db.manage", Scope: "system:write", Title: "kind.db.sqlite.maintain", CLI: "berth db sqlite {{.action}} {{.instance}}",
		Params: change.ParamSchema{Props: map[string]change.Prop{"instance": {Type: "integer"}, "action": {Type: "string", Enum: []string{"check", "vacuum", "wal", "backup"}}}, Required: []string{"instance", "action"}},
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			var p struct {
				Instance int64
				Action   string
			}
			json.Unmarshal(raw, &p)
			d, err := svc(env).dbInstance(env.Host, p.Instance)
			if err != nil || d.Engine != "sqlite" {
				return change.Plan{}, errs.New(404, "db.not_found", "id", p.Instance)
			}
			risk := change.RiskLow
			cmds := map[string][]string{"check": {"PRAGMA integrity_check"}, "backup": {"VACUUM INTO " + dbconn.SQLiteBackups + "/" + strconv.FormatInt(d.ID, 10) + "/…"},
				"vacuum": {"back up first", "VACUUM; ANALYZE (the file is locked while it runs)"}, "wal": {"back up first", "PRAGMA journal_mode=WAL"}}[p.Action]
			if cmds == nil {
				return change.Plan{}, errs.New(400, "db.bad_request")
			}
			if p.Action == "vacuum" || p.Action == "wal" {
				risk = change.RiskMedium
			}
			return change.Plan{Summary: "plan.db.sqlite." + p.Action, SummaryP: map[string]any{"name": d.Name}, Risk: risk, Undoable: p.Action == "vacuum" || p.Action == "wal",
				Commands: cmds, Affected: []change.Object{dbObj(d, "")}}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			var p struct {
				Instance int64
				Action   string
			}
			json.Unmarshal(raw, &p)
			a := svc(env)
			d, err := a.dbInstance(env.Host, p.Instance)
			if err != nil {
				return nil, nil, err
			}
			key := strconv.FormatInt(d.ID, 10)
			var undo *change.UndoRecipe
			if p.Action != "check" {
				var b dbconn.SQLiteBackup
				if err := a.call(env.Ctx, env.Host, "POST", "/v1/sqlite/backup", map[string]any{"path": d.Addr, "key": key}, &b); err != nil {
					return nil, nil, err
				}
				env.Log("backed up as %s", b.Name)
				if p.Action == "backup" {
					return b, nil, nil
				}
				u, _ := json.Marshal(map[string]any{"instance": d.ID, "backup": b.Name})
				undo = &change.UndoRecipe{Kind: "db.sqlite.restore", Params: u}
			}
			var out struct{ Result string }
			if err := a.call(env.Ctx, env.Host, "POST", "/v1/sqlite/maintain", map[string]any{"path": d.Addr, "action": p.Action}, &out); err != nil {
				return nil, nil, err
			}
			return map[string]string{"result": out.Result}, undo, nil
		},
	})
	change.Register(change.Kind{
		Name: "db.sqlite.restore", Perm: "db.manage", Scope: "system:write", Title: "kind.db.sqlite.restore", CLI: "berth db sqlite restore {{.instance}} {{.backup}}",
		Params: change.ParamSchema{Props: map[string]change.Prop{"instance": {Type: "integer"}, "backup": {Type: "string"}}, Required: []string{"instance", "backup"}},
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			var p struct {
				Instance int64
				Backup   string
			}
			json.Unmarshal(raw, &p)
			d, err := svc(env).dbInstance(env.Host, p.Instance)
			if err != nil || d.Engine != "sqlite" {
				return change.Plan{}, errs.New(404, "db.not_found", "id", p.Instance)
			}
			return change.Plan{Summary: "plan.db.sqlite.restore", SummaryP: map[string]any{"name": d.Name, "backup": p.Backup}, Risk: change.RiskHigh, Undoable: true,
				Commands: []string{"back up the current file first", "copy " + p.Backup + " over " + d.Addr + " (owner and mode kept; best with the site stopped)"},
				Affected: []change.Object{dbObj(d, "")}}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			var p struct {
				Instance int64
				Backup   string
			}
			json.Unmarshal(raw, &p)
			a := svc(env)
			d, err := a.dbInstance(env.Host, p.Instance)
			if err != nil {
				return nil, nil, err
			}
			key := strconv.FormatInt(d.ID, 10)
			var b dbconn.SQLiteBackup
			if err := a.call(env.Ctx, env.Host, "POST", "/v1/sqlite/backup", map[string]any{"path": d.Addr, "key": key}, &b); err != nil {
				return nil, nil, err
			}
			if err := a.call(env.Ctx, env.Host, "POST", "/v1/sqlite/restore", map[string]any{"path": d.Addr, "key": key, "name": p.Backup}, nil); err != nil {
				return nil, nil, err
			}
			u, _ := json.Marshal(map[string]any{"instance": d.ID, "backup": b.Name})
			return nil, &change.UndoRecipe{Kind: "db.sqlite.restore", Params: u}, nil
		},
	})
}

type nativeInstallParams struct{ Engine, Version, Name string }

func dbNativeInstallParams(raw json.RawMessage) (nativeInstallParams, error) {
	var p nativeInstallParams
	json.Unmarshal(raw, &p)
	vs := dbNativeVersions[p.Engine]
	if vs == nil {
		return p, errs.New(400, "db.bad_engine", "engine", p.Engine)
	}
	if p.Version == "" {
		p.Version = vs[0]
	}
	if !contains(vs, p.Version) {
		return p, errs.New(400, "dbn.bad_version", "version", p.Version)
	}
	if p.Name == "" {
		p.Name = p.Engine
	}
	if !dbNameRe.MatchString(strings.ReplaceAll(p.Name, "-", "_")) {
		return p, errs.New(400, "db.bad_request")
	}
	return p, nil
}

type tuneIn struct {
	Instance int64
	MemMB    int `json:"mem_mb"`
	Values   map[string]string
}

func (a *App) tuneParams(env change.Env, raw json.RawMessage) (dbInstance, system.DBNative, tuneIn, error) {
	var p tuneIn
	json.Unmarshal(raw, &p)
	d, err := a.dbInstance(env.Host, p.Instance)
	if err != nil {
		return d, system.DBNative{}, p, err
	}
	n, err := a.nativeOf(env, d)
	if err != nil {
		return d, n, p, err
	}
	if p.MemMB < 0 || p.MemMB > 1<<20 {
		return d, n, p, errs.New(400, "dbn.bad_value", "key", "mem_mb", "value", strconv.Itoa(p.MemMB))
	}
	if p.Values == nil {
		if p.MemMB == 0 {
			return d, n, p, errs.New(400, "dbn.bad_value", "key", "mem_mb", "value", "0")
		}
		p.Values = system.RecommendDB(d.Engine, n.Version, p.MemMB)
	}
	return d, n, p, system.ValidateDBValues(d.Engine, p.Values)
}

type copyIn struct {
	From, To     int64
	Database, As string
}

func (a *App) copyParams(env change.Env, raw json.RawMessage) (dbInstance, dbInstance, copyIn, error) {
	var p copyIn
	json.Unmarshal(raw, &p)
	from, err := a.dbInstance(env.Host, p.From)
	if err != nil {
		return from, from, p, err
	}
	to, err := a.dbInstance(env.Host, p.To)
	if err != nil {
		return from, to, p, err
	}
	if p.As == "" {
		p.As = p.Database
	}
	fam := func(e string) string {
		if e == "mariadb" {
			return "mysql"
		}
		return e
	}
	if !dbNameRe.MatchString(p.Database) || !dbNameRe.MatchString(p.As) || from.ID == to.ID {
		return from, to, p, errs.New(400, "db.bad_request")
	}
	if fam(from.Engine) != fam(to.Engine) || from.Engine == "redis" || from.Engine == "sqlite" {
		return from, to, p, errs.New(400, "db.copy_engine", "from", from.Engine, "to", to.Engine)
	}
	if from.Container == "" && from.Native == "" || to.Container == "" && to.Native == "" {
		return from, to, p, errs.New(409, "db.import_container_only")
	}
	return from, to, p, nil
}

// handleDBStats: what the server is doing — uptime, connections, queries per second, cache hit
// rate and the size of each database.
func (a *App) handleDBStats(w http.ResponseWriter, r *http.Request) {
	d, t, err := a.dbFromRequest(r, "")
	if err != nil {
		a.fail(w, r, err)
		return
	}
	q := map[string]string{
		"postgres": `SELECT extract(epoch FROM now() - pg_postmaster_start_time())::bigint AS uptime, (SELECT count(*) FROM pg_stat_activity) AS connections,
			(SELECT sum(xact_commit + xact_rollback) FROM pg_stat_database) AS queries,
			(SELECT round(100.0 * sum(blks_hit) / nullif(sum(blks_hit) + sum(blks_read), 0), 2) FROM pg_stat_database) AS hit_pct,
			(SELECT setting FROM pg_settings WHERE name = 'max_connections') AS max_connections`,
		"mysql": `SELECT (SELECT VARIABLE_VALUE FROM performance_schema.global_status WHERE VARIABLE_NAME='Uptime') AS uptime,
			(SELECT VARIABLE_VALUE FROM performance_schema.global_status WHERE VARIABLE_NAME='Threads_connected') AS connections,
			(SELECT VARIABLE_VALUE FROM performance_schema.global_status WHERE VARIABLE_NAME='Questions') AS queries,
			(SELECT greatest(0, round(100 * (1 - r.VARIABLE_VALUE / nullif(q.VARIABLE_VALUE, 0)), 2)) FROM performance_schema.global_status r, performance_schema.global_status q
				WHERE r.VARIABLE_NAME='Innodb_buffer_pool_reads' AND q.VARIABLE_NAME='Innodb_buffer_pool_read_requests') AS hit_pct,
			@@max_connections AS max_connections`,
	}
	q["mariadb"] = strings.ReplaceAll(strings.ReplaceAll(q["mysql"], "performance_schema.global_status", "information_schema.GLOBAL_STATUS"), "VARIABLE_NAME='", "VARIABLE_NAME='")
	if d.Engine == "redis" {
		var info struct{ Value string }
		body := targetBody(t)
		body["args"] = []string{"INFO"}
		if err := a.call(r.Context(), d.Host, "POST", "/v1/db/redis", body, &info); err != nil {
			a.fail(w, r, err)
			return
		}
		kv := map[string]string{}
		for _, l := range strings.Split(info.Value, "\n") {
			if k, v, ok := strings.Cut(strings.TrimSpace(l), ":"); ok {
				kv[k] = v
			}
		}
		out := map[string]any{"uptime": kv["uptime_in_seconds"], "connections": kv["connected_clients"], "max_connections": kv["maxclients"], "qps": kv["instantaneous_ops_per_sec"],
			"memory": kv["used_memory_human"], "keys": kv["db0"]}
		if h, err1 := strconv.ParseFloat(kv["keyspace_hits"], 64); err1 == nil {
			if m, err2 := strconv.ParseFloat(kv["keyspace_misses"], 64); err2 == nil && h+m > 0 {
				out["hit_pct"] = float64(int(h/(h+m)*10000)) / 100
			}
		}
		writeJSON(w, 200, out)
		return
	}
	sqlText := q[d.Engine]
	if sqlText == "" {
		a.fail(w, r, errs.New(400, "db.bad_engine", "engine", d.Engine))
		return
	}
	body := targetBody(t)
	body["sql"], body["readonly"], body["limit"] = sqlText, true, 1
	var res dbconn.Result
	if err := a.call(r.Context(), d.Host, "POST", "/v1/db/query", body, &res); err != nil {
		a.fail(w, r, err)
		return
	}
	out := map[string]any{}
	if len(res.Rows) > 0 {
		for i, c := range res.Columns {
			out[c] = res.Rows[0][i]
		}
	}
	if up, _ := strconv.ParseFloat(toStr(out["uptime"]), 64); up > 0 {
		qs, _ := strconv.ParseFloat(toStr(out["queries"]), 64)
		out["qps"] = float64(int(qs/up*100)) / 100
	}
	body = targetBody(t)
	body["what"] = "databases"
	var dbs dbconn.Result
	if a.call(r.Context(), d.Host, "POST", "/v1/db/meta", body, &dbs) == nil {
		out["databases"] = dbs
	}
	writeJSON(w, 200, out)
}

func toStr(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case nil:
		return ""
	default:
		b, _ := json.Marshal(x)
		return strings.Trim(string(b), `"`)
	}
}

func sortedKeys(m map[string]string) []string {
	k := make([]string, 0, len(m))
	for x := range m {
		k = append(k, x)
	}
	sort.Strings(k)
	return k
}
