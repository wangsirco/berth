package panel

// SFTP sub-accounts (宝塔/1Panel 的 FTP 账号): a login that sees one directory only, usually
// a site's folder handed to whoever maintains it.

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/wangsirco/berth/internal/change"
	"github.com/wangsirco/berth/internal/errs"
)

type sftpAccount struct {
	Name     string   `json:"name"`
	Dir      string   `json:"dir"`
	ReadOnly bool     `json:"readonly,omitempty"`
	Password string   `json:"password,omitempty"`
	Keys     []string `json:"keys,omitempty"`
	HasKeys  int      `json:"has_keys,omitempty"`
	Mounted  bool     `json:"mounted"`
}

func (a *App) sftpAccounts(env change.Env) map[string]sftpAccount {
	var out struct {
		Items []sftpAccount `json:"items"`
	}
	a.call(env.Ctx, env.Host, "GET", "/v1/system/sftp", nil, &out)
	m := map[string]sftpAccount{}
	for _, x := range out.Items {
		m[x.Name] = x
	}
	return m
}

func init() {
	routeHooks = append(routeHooks, func(a *App, mux *http.ServeMux) {
		mux.HandleFunc("GET /api/v1/hosts/{host}/sftp", a.authed("system.view", a.agentGet("/v1/system/sftp")))
	})
	reg := func(k change.Kind) {
		k.Perm, k.Scope, k.Title = "system.manage", "system:write", "kind."+k.Name
		change.Register(k)
	}
	reg(change.Kind{Name: "sftp.set", CLI: "berth sftp set {{.name}} {{.dir}}", Redact: []string{"password"},
		Params: change.ParamSchema{Props: map[string]change.Prop{
			"name": {Type: "string", Desc: "login name"}, "dir": {Type: "string", Desc: "the only directory it sees, e.g. a site's folder"},
			"readonly": {Type: "boolean"}, "password": {Type: "string", Desc: "set or change the password (\"\" keeps it)"},
			"keys": {Type: "array", Desc: "authorized public keys (replaces the list)"}}, Required: []string{"name", "dir"}},
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			var p sftpAccount
			json.Unmarshal(raw, &p)
			old, exists := svc(env).sftpAccounts(env)[p.Name]
			if !exists && p.Password == "" && len(p.Keys) == 0 {
				return change.Plan{}, errs.New(400, "system.sftp_no_login")
			}
			mode := map[bool]string{true: "read-only", false: "read-write"}[p.ReadOnly]
			cmds := []string{}
			if !exists {
				cmds = append(cmds, "useradd -M -s /sbin/nologin -G berth-sftp "+p.Name+"  (no shell, SFTP only)")
			}
			cmds = append(cmds, "setfacl -R -m u:"+p.Name+" "+p.Dir+"  ("+mode+")",
				"mount --bind "+p.Dir+" /srv/berth/sftp/"+p.Name+"/files  (systemd mount unit, kept across reboots)",
				"sshd: Match Group berth-sftp → ChrootDirectory, internal-sftp  (root's own settings are checked unchanged)",
				"SELinux: module berth_sftp lets SFTP sessions use shared data directories (installed once)")
			if exists && old.Dir != p.Dir {
				cmds = append(cmds, "# "+old.Dir+" is no longer shared")
			}
			sum := map[bool]string{true: "plan.sftp.update", false: "plan.sftp.create"}[exists]
			if p.ReadOnly {
				sum += "_ro"
			}
			return change.Plan{Summary: sum,
				SummaryP: map[string]any{"name": p.Name, "dir": p.Dir}, Risk: change.RiskMedium, Undoable: true, Commands: cmds}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			a := svc(env)
			var p sftpAccount
			json.Unmarshal(raw, &p)
			p.Name = strings.TrimSpace(p.Name)
			old, exists := a.sftpAccounts(env)[p.Name]
			if err := agentOp(env, "/v1/system/sftp", p); err != nil {
				return nil, nil, err
			}
			if exists { // the password cannot come back; where and how it was shared can
				u, _ := json.Marshal(map[string]any{"name": old.Name, "dir": old.Dir, "readonly": old.ReadOnly})
				return nil, &change.UndoRecipe{Kind: "sftp.set", Params: u}, nil
			}
			u, _ := json.Marshal(map[string]string{"name": p.Name})
			return nil, &change.UndoRecipe{Kind: "sftp.delete", Params: u}, nil
		}})
	reg(change.Kind{Name: "sftp.delete", CLI: "berth sftp delete {{.name}}",
		Params: change.ParamSchema{Props: map[string]change.Prop{"name": {Type: "string"}}, Required: []string{"name"}},
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			var p struct{ Name string }
			json.Unmarshal(raw, &p)
			acc, ok := svc(env).sftpAccounts(env)[p.Name]
			if !ok {
				return change.Plan{}, errs.New(404, "system.sftp_not_found")
			}
			return change.Plan{Summary: "plan.sftp.delete", SummaryP: map[string]any{"name": p.Name, "dir": acc.Dir}, Risk: change.RiskMedium,
				Commands: []string{"umount /srv/berth/sftp/" + p.Name + "/files", "userdel " + p.Name, "# the files in " + acc.Dir + " stay"}}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			var p struct{ Name string }
			json.Unmarshal(raw, &p)
			return nil, nil, agentOp(env, "/v1/system/sftp/remove", p)
		}})
}
