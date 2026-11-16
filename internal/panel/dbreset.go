package panel

// Reset a database's admin password (忘记 root 密码): for instances in a container, without
// knowing the old one. Afterwards the panel's own copy, the store's record and the app's
// .env all carry the new password, and the panel connects with it once to prove it works.

import (
	"encoding/json"
	"net/url"
	"strings"
	"time"

	"github.com/wangsirco/berth/internal/change"
	"github.com/wangsirco/berth/internal/errs"
)

// syncAppPassword updates the store's record and the .env of the app that runs container.
func (a *App) syncAppPassword(env change.Env, container, user, pw string) {
	ins, err := a.appInstances(env.Host)
	if err != nil {
		return
	}
	for _, in := range ins {
		if !strings.HasPrefix(container, in.Name+"-") || len(in.secretsEnc) == 0 {
			continue
		}
		plain, err := a.open(in.secretsEnc)
		if err != nil {
			continue
		}
		var sec map[string]string
		json.Unmarshal(plain, &sec)
		key := ""
		switch {
		case user == "root" && sec["ROOT_PASSWORD"] != "":
			key = "ROOT_PASSWORD"
		case sec["DB_PASSWORD"] != "" && (in.Values["DB_USER"] == user || sec["DB_USER"] == user):
			key = "DB_PASSWORD"
		}
		if key == "" {
			continue
		}
		sec[key] = pw
		b, _ := json.Marshal(sec)
		if enc, err := a.seal(b); err == nil {
			a.db.Exec(`UPDATE apps SET secrets_enc=?, updated_at=? WHERE host=? AND name=?`, enc, time.Now().Unix(), env.Host, in.Name)
		}
		if f, err := a.composeFiles(env.Ctx, env.Host, in.Name); err == nil {
			lines := strings.Split(f.Env, "\n")
			for i, l := range lines {
				if strings.HasPrefix(l, key+"=") {
					lines[i] = key + "=" + pw
				}
			}
			if err := a.call(env.Ctx, env.Host, "PUT", "/v1/compose/projects/"+url.PathEscape(in.Name)+"/files", map[string]any{"compose": f.Compose, "env": strings.Join(lines, "\n")}, nil); err == nil {
				env.Log("updated %s in %s/.env", key, f.Dir)
			}
		}
	}
}

func init() {
	change.Register(change.Kind{
		Name: "db.root_reset", Perm: "db.manage", Scope: "system:write", Title: "kind.db.root_reset",
		Params: change.ParamSchema{Props: map[string]change.Prop{"instance": {Type: "integer"}, "password": {Type: "string", Desc: "8–64 letters, digits or !@#%^&*()_+=.,:;~-"}},
			Required: []string{"instance", "password"}},
		Redact: []string{"password"}, CLI: "berth db reset-admin {{.instance}}",
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			var p struct {
				Instance int64
				Password string
			}
			json.Unmarshal(raw, &p)
			d, err := svc(env).dbInstance(env.Host, p.Instance)
			if err != nil {
				return change.Plan{}, err
			}
			if d.Native != "" {
				return change.Plan{Summary: "plan.db.root_reset", SummaryP: map[string]any{"user": d.User, "instance": d.Name}, Risk: change.RiskMedium,
					Commands: []string{"as the system's root, over the local socket: ALTER USER " + d.User + " … (no restart)", "store the new password in the panel", "connect once with it"},
					Affected: []change.Object{dbObj(d, "user:"+d.User)}}, nil
			}
			if d.Container == "" {
				return change.Plan{}, errs.New(400, "db.reset_external")
			}
			if d.Engine != "mysql" && d.Engine != "mariadb" && d.Engine != "postgres" {
				return change.Plan{}, errs.New(400, "db.reset_unsupported", "engine", d.Engine)
			}
			cmds := map[string][]string{
				"postgres": {"docker exec " + d.Container + " psql -U " + d.User + ` -c "ALTER USER ` + d.User + ` PASSWORD '…'"   (local socket, no restart)`},
				"mariadb":  {"docker exec " + d.Container + " mariadb -uroot -e \"SET PASSWORD FOR '" + d.User + "'@… = PASSWORD('…')\"   (local socket, no restart)"},
				"mysql": {"docker stop " + d.Container + "   (the database is down for about half a minute)",
					"docker run berth-dbreset-" + d.Container + " --volumes-from " + d.Container + " --network none … --skip-grant-tables --skip-networking",
					"ALTER USER '" + d.User + "'@… IDENTIFIED BY '…'", "docker start " + d.Container},
			}[d.Engine]
			cmds = append(cmds, "store the new password in the panel, the store record and the app's .env", "connect once with it")
			risk := change.RiskMedium
			if d.Engine == "mysql" {
				risk = change.RiskHigh
			}
			return change.Plan{Summary: "plan.db.root_reset", SummaryP: map[string]any{"user": d.User, "instance": d.Name}, Risk: risk,
				Commands: cmds, Affected: []change.Object{dbObj(d, "user:"+d.User)}}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			var p struct {
				Instance int64
				Password string
			}
			json.Unmarshal(raw, &p)
			a := svc(env)
			d, err := a.dbInstance(env.Host, p.Instance)
			if err != nil {
				return nil, nil, err
			}
			if d.Native != "" {
				if err := a.call(env.Ctx, env.Host, "POST", "/v1/dbnative/password", map[string]string{"engine": d.Engine, "user": d.User, "password": p.Password}, nil); err != nil {
					return nil, nil, err
				}
			} else if err := agentOp(env, "/v1/db/root-reset", map[string]string{"container": d.Container, "engine": d.Engine, "user": d.User, "password": p.Password}); err != nil {
				return nil, nil, err
			}
			enc, err := a.seal([]byte(p.Password))
			if err != nil {
				return nil, nil, err
			}
			if _, err := a.db.Exec(`UPDATE databases SET admin_pass_enc=? WHERE host=? AND id=?`, enc, env.Host, d.ID); err != nil {
				return nil, nil, err
			}
			a.syncAppPassword(env, d.Container, d.User, p.Password)
			d.passEnc = enc
			var perr error
			for i := 0; i < 30; i++ { // MySQL is starting again
				if perr = a.dbPing(env.Ctx, d); perr == nil {
					env.Log("connected to %s as %s with the new password", d.Name, d.User)
					return map[string]string{"user": d.User}, nil, nil
				}
				time.Sleep(2 * time.Second)
			}
			return nil, nil, errs.New(500, "db.reset_failed", "detail", perr.Error())
		},
	})
}
