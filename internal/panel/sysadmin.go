package panel

// The rest of 方案 §5.14: SSH server and keys, system users and groups, kernel parameters,
// swap and zram, the DNS resolver and automatic updates. Each write is a change; SSH
// settings additionally roll back by themselves unless confirmed.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/wangsirco/berth/internal/change"
	"github.com/wangsirco/berth/internal/errs"
)

type sshSettings struct {
	Port         int    `json:"port"`
	PasswordAuth bool   `json:"password_auth"`
	RootLogin    string `json:"root_login"`
	PubkeyAuth   bool   `json:"pubkey_auth"`
}

type sshState struct {
	Effective sshSettings `json:"effective"`
	Managed   bool        `json:"managed"`
	Pending   bool        `json:"pending"`
	Keys      []struct {
		User string `json:"user"`
		Keys []struct {
			Fingerprint string `json:"fingerprint"`
			Comment     string `json:"comment"`
		} `json:"keys"`
	} `json:"keys"`
}

type sysAccount struct {
	Name    string   `json:"name"`
	Shell   string   `json:"shell"`
	Groups  []string `json:"groups"`
	System  bool     `json:"system"`
	Locked  bool     `json:"locked"`
	Gecos   string   `json:"gecos"`
	Home    string   `json:"home"`
	UID     int      `json:"uid"`
	NoLogin bool     `json:"nologin"`
}

type sysAccounts struct {
	Users  []sysAccount `json:"users"`
	Groups []struct {
		Name   string `json:"name"`
		System bool   `json:"system"`
	} `json:"groups"`
	AdminGroup string `json:"admin_group"`
}

type tuningState struct {
	Sysctl struct {
		Values map[string]string `json:"values"`
		File   map[string]string `json:"file"`
	} `json:"sysctl"`
	Swap struct {
		ZramSize   string `json:"zram_size"`
		ZramCustom bool   `json:"zram_custom"`
		BerthMB    int    `json:"berth_swap_mb"`
	} `json:"swap"`
	Resolver struct {
		Resolved bool     `json:"resolved"`
		Servers  []string `json:"servers"`
	} `json:"resolver"`
	AutoUpdate struct {
		Family    string `json:"family"`
		Installed bool   `json:"installed"`
		Enabled   bool   `json:"enabled"`
		Type      string `json:"type"`
		Time      string `json:"time"`
	} `json:"auto_update"`
}

func (a *App) sshNow(env change.Env) (sshState, error) {
	var s sshState
	return s, a.call(env.Ctx, env.Host, "GET", "/v1/system/ssh", nil, &s)
}

func (a *App) accountsNow(env change.Env) (sysAccounts, error) {
	var s sysAccounts
	return s, a.call(env.Ctx, env.Host, "GET", "/v1/system/accounts", nil, &s)
}

func (s sysAccounts) user(name string) (sysAccount, bool) {
	for _, u := range s.Users {
		if u.Name == name {
			return u, true
		}
	}
	return sysAccount{}, false
}

func (a *App) tuningNow(env change.Env) (tuningState, error) {
	var s tuningState
	return s, a.call(env.Ctx, env.Host, "GET", "/v1/system/tuning", nil, &s)
}

func yesNo(b bool) string { return map[bool]string{true: "yes", false: "no"}[b] }

func init() {
	routeHooks = append(routeHooks, func(a *App, mux *http.ServeMux) {
		for _, p := range []string{"ssh", "accounts", "tuning"} {
			mux.HandleFunc("GET /api/v1/hosts/{host}/system/"+p, a.authed("system.view", a.agentGet("/v1/system/"+p)))
		}
	})
	str := func(d string) change.Prop { return change.Prop{Type: "string", Desc: d} }
	reg := func(k change.Kind) {
		if k.Perm == "" {
			k.Perm = "system.manage"
		}
		k.Scope, k.Title = "system:write", "kind."+k.Name
		change.Register(k)
	}

	// ---- SSH ----
	reg(change.Kind{Name: "ssh.set", CLI: "berth system ssh",
		Params: change.ParamSchema{Props: map[string]change.Prop{"port": {Type: "integer"}, "password_auth": {Type: "boolean"}, "pubkey_auth": {Type: "boolean"},
			"root_login": {Type: "string", Enum: []string{"yes", "prohibit-password", "no"}}, "seconds": {Type: "integer", Desc: "roll back after (30-600, default 60) unless confirmed"}},
			Required: []string{"password_auth", "root_login"}},
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			var p struct {
				sshSettings
				Seconds int `json:"seconds"`
			}
			p.PubkeyAuth = true
			json.Unmarshal(raw, &p)
			cur, err := svc(env).sshNow(env)
			if err != nil {
				return change.Plan{}, err
			}
			if p.Port == 0 {
				p.Port = cur.Effective.Port
			}
			if !p.PasswordAuth && !p.PubkeyAuth {
				return change.Plan{}, errs.New(409, "system.ssh_no_method")
			}
			lines := []string{}
			add := func(k, from, to string) {
				if from != to {
					lines = append(lines, fmt.Sprintf("%s: %s → %s", k, from, to))
				}
			}
			e := cur.Effective
			add("Port", fmt.Sprint(e.Port), fmt.Sprint(p.Port))
			add("PasswordAuthentication", yesNo(e.PasswordAuth), yesNo(p.PasswordAuth))
			add("PermitRootLogin", e.RootLogin, p.RootLogin)
			add("PubkeyAuthentication", yesNo(e.PubkeyAuth), yesNo(p.PubkeyAuth))
			secs := p.Seconds
			if secs == 0 {
				secs = 60
			}
			sum := "plan.ssh.set"
			if p.Port != e.Port {
				sum = "plan.ssh.set_port"
			}
			return change.Plan{Summary: sum, SummaryP: map[string]any{"secs": secs, "port": p.Port}, Risk: change.RiskHigh, Undoable: true, Commands: lines}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			a := svc(env)
			cur, err := a.sshNow(env)
			if err != nil {
				return nil, nil, err
			}
			var p map[string]any
			json.Unmarshal(raw, &p)
			if _, ok := p["pubkey_auth"]; !ok {
				p["pubkey_auth"] = true
			}
			if err := a.call(env.Ctx, env.Host, "POST", "/v1/system/ssh", p, nil); err != nil {
				return nil, nil, err
			}
			secs, _ := p["seconds"].(float64)
			if secs == 0 {
				secs = 60
			}
			a.hub.publish(event{Type: "ssh.pending", Data: map[string]any{"host": env.Host, "rollback_at": time.Now().Unix() + int64(secs)}})
			// undo goes back to the system's own file when Berth had none before
			undo := map[string]any{"port": cur.Effective.Port, "password_auth": cur.Effective.PasswordAuth, "root_login": cur.Effective.RootLogin, "pubkey_auth": cur.Effective.PubkeyAuth}
			if !cur.Managed {
				undo["remove"] = true
			}
			u, _ := json.Marshal(undo)
			return nil, &change.UndoRecipe{Kind: "ssh.set", Params: u}, nil
		}})
	for _, k := range []string{"confirm", "rollback"} {
		k := k
		reg(change.Kind{Name: "ssh." + k, CLI: "berth system ssh " + k, Params: change.ParamSchema{Props: map[string]change.Prop{}},
			Plan: func(env change.Env, _ json.RawMessage) (change.Plan, error) {
				return change.Plan{Summary: "plan.ssh." + k, Risk: change.RiskLow}, nil
			},
			Apply: func(env change.Env, _ json.RawMessage) (any, *change.UndoRecipe, error) {
				a := svc(env)
				err := a.call(env.Ctx, env.Host, "POST", "/v1/system/ssh/"+k, nil, nil)
				a.hub.publish(event{Type: "ssh.pending", Data: map[string]any{"host": env.Host, "rollback_at": 0}})
				return nil, nil, err
			}})
	}
	reg(change.Kind{Name: "ssh.key_add", CLI: "berth system ssh key add {{.user}}",
		Params: change.ParamSchema{Props: map[string]change.Prop{"user": str("system user"), "key": str("public key line")}, Required: []string{"user", "key"}},
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			var p struct{ User, Key string }
			json.Unmarshal(raw, &p)
			f := strings.Fields(p.Key)
			comment := ""
			if len(f) > 2 {
				comment = strings.Join(f[2:], " ")
			}
			return change.Plan{Summary: "plan.ssh.key_add", SummaryP: map[string]any{"user": p.User, "comment": firstNonEmpty(comment, "—")}, Risk: change.RiskMedium, Undoable: true,
				Commands: []string{"echo '" + truncate(p.Key, 60) + "' >> ~" + p.User + "/.ssh/authorized_keys"}}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			var p struct{ User, Key string }
			json.Unmarshal(raw, &p)
			var out struct{ Fingerprint string }
			if err := svc(env).call(env.Ctx, env.Host, "POST", "/v1/system/ssh/keys", map[string]string{"user": p.User, "add": p.Key}, &out); err != nil {
				return nil, nil, err
			}
			u, _ := json.Marshal(map[string]string{"user": p.User, "fingerprint": out.Fingerprint})
			return out, &change.UndoRecipe{Kind: "ssh.key_remove", Params: u}, nil
		}})
	reg(change.Kind{Name: "ssh.key_remove", CLI: "berth system ssh key remove {{.user}} {{.fingerprint}}",
		Params: change.ParamSchema{Props: map[string]change.Prop{"user": str("system user"), "fingerprint": str("SHA256:…")}, Required: []string{"user", "fingerprint"}},
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			var p struct{ User, Fingerprint string }
			json.Unmarshal(raw, &p)
			return change.Plan{Summary: "plan.ssh.key_remove", SummaryP: map[string]any{"user": p.User, "fp": p.Fingerprint}, Risk: change.RiskMedium, Undoable: true}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			var p struct{ User, Fingerprint string }
			json.Unmarshal(raw, &p)
			var out struct{ Removed string }
			if err := svc(env).call(env.Ctx, env.Host, "POST", "/v1/system/ssh/keys", map[string]string{"user": p.User, "remove": p.Fingerprint}, &out); err != nil {
				return nil, nil, err
			}
			u, _ := json.Marshal(map[string]string{"user": p.User, "key": out.Removed})
			return nil, &change.UndoRecipe{Kind: "ssh.key_add", Params: u}, nil
		}})

	// ---- users and groups ----
	acct := func(env change.Env, op map[string]any) error {
		return svc(env).call(env.Ctx, env.Host, "POST", "/v1/system/accounts", op, nil)
	}
	reg(change.Kind{Name: "sysuser.create", CLI: "berth system user add {{.name}}", Redact: []string{"password"},
		Params: change.ParamSchema{Props: map[string]change.Prop{"name": str("login name"), "gecos": str("display name"), "shell": str("default /bin/bash"),
			"groups": {Type: "array"}, "admin": {Type: "boolean", Desc: "add to wheel/sudo"}, "password": str("optional; key login otherwise")}, Required: []string{"name"}},
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			var p struct {
				Name   string
				Groups []string
				Admin  bool
			}
			json.Unmarshal(raw, &p)
			if !jobUserRe.MatchString(p.Name) {
				return change.Plan{}, errs.New(400, "system.bad_username")
			}
			risk := change.RiskLow
			if p.Admin {
				risk = change.RiskMedium
			}
			return change.Plan{Summary: map[bool]string{true: "plan.sysuser.create_admin", false: "plan.sysuser.create"}[p.Admin], SummaryP: map[string]any{"name": p.Name},
				Risk: risk, Undoable: true, Commands: []string{"useradd -m " + p.Name}}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			a := svc(env)
			var p struct {
				Name, Gecos, Shell, Password string
				Groups                       []string
				Admin                        bool
			}
			json.Unmarshal(raw, &p)
			if p.Admin {
				if st, err := a.accountsNow(env); err == nil && st.AdminGroup != "" && !contains(p.Groups, st.AdminGroup) {
					p.Groups = append(p.Groups, st.AdminGroup)
				}
			}
			if err := acct(env, map[string]any{"op": "create", "name": p.Name, "gecos": p.Gecos, "shell": p.Shell, "groups": p.Groups, "password": p.Password}); err != nil {
				return nil, nil, err
			}
			u, _ := json.Marshal(map[string]any{"name": p.Name, "remove_home": false})
			return nil, &change.UndoRecipe{Kind: "sysuser.delete", Params: u}, nil
		}})
	reg(change.Kind{Name: "sysuser.delete", CLI: "berth system user delete {{.name}}",
		Params: change.ParamSchema{Props: map[string]change.Prop{"name": str("login name"), "remove_home": {Type: "boolean"}}, Required: []string{"name"}},
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			var p struct {
				Name       string
				RemoveHome bool `json:"remove_home"`
			}
			json.Unmarshal(raw, &p)
			st, err := svc(env).accountsNow(env)
			if err != nil {
				return change.Plan{}, err
			}
			u, ok := st.user(p.Name)
			if !ok {
				return change.Plan{}, errs.New(404, "system.no_user", "name", p.Name)
			}
			if u.System {
				return change.Plan{}, errs.New(409, "system.account_system")
			}
			if p.RemoveHome {
				return change.Plan{Summary: "plan.sysuser.delete_home", SummaryP: map[string]any{"name": p.Name, "home": u.Home}, Risk: change.RiskDestructive,
					Commands: []string{"userdel -r " + p.Name}}, nil
			}
			return change.Plan{Summary: "plan.sysuser.delete", SummaryP: map[string]any{"name": p.Name, "home": u.Home}, Risk: change.RiskHigh, Undoable: true,
				Commands: []string{"userdel " + p.Name}}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			a := svc(env)
			var p struct {
				Name       string
				RemoveHome bool `json:"remove_home"`
			}
			json.Unmarshal(raw, &p)
			st, _ := a.accountsNow(env)
			u, _ := st.user(p.Name)
			if err := acct(env, map[string]any{"op": "delete", "name": p.Name, "remove_home": p.RemoveHome}); err != nil {
				return nil, nil, err
			}
			if p.RemoveHome {
				return nil, nil, nil
			}
			// back with the same shell and groups (the home stayed; the password did not)
			var groups []string
			for _, g := range u.Groups {
				if g != u.Name {
					groups = append(groups, g)
				}
			}
			b, _ := json.Marshal(map[string]any{"name": u.Name, "gecos": u.Gecos, "shell": u.Shell, "groups": groups})
			return nil, &change.UndoRecipe{Kind: "sysuser.create", Params: b}, nil
		}})
	reg(change.Kind{Name: "sysuser.update", CLI: "berth system user set {{.name}}",
		Params: change.ParamSchema{Props: map[string]change.Prop{"name": str("login name"), "groups": {Type: "array", Desc: "supplementary groups (whole list)"},
			"shell": str(""), "locked": {Type: "boolean"}}, Required: []string{"name"}},
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			var p struct {
				Name   string
				Groups []string
				Shell  string
				Locked *bool
			}
			json.Unmarshal(raw, &p)
			st, err := svc(env).accountsNow(env)
			if err != nil {
				return change.Plan{}, err
			}
			u, ok := st.user(p.Name)
			if !ok {
				return change.Plan{}, errs.New(404, "system.no_user", "name", p.Name)
			}
			lines := []string{}
			risk := change.RiskLow
			if p.Groups != nil {
				lines = append(lines, "groups: "+strings.Join(u.Groups, ", ")+" → "+strings.Join(p.Groups, ", "))
				if st.AdminGroup != "" && contains(p.Groups, st.AdminGroup) != contains(u.Groups, st.AdminGroup) {
					risk = change.RiskMedium
				}
			}
			if p.Shell != "" && p.Shell != u.Shell {
				lines = append(lines, "shell: "+u.Shell+" → "+p.Shell)
			}
			if p.Locked != nil && *p.Locked != u.Locked {
				lines = append(lines, map[bool]string{true: "usermod -L ", false: "usermod -U "}[*p.Locked]+p.Name)
			}
			return change.Plan{Summary: "plan.sysuser.update", SummaryP: map[string]any{"name": p.Name}, Risk: risk, Undoable: true, Commands: lines}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			a := svc(env)
			var p struct {
				Name   string
				Groups []string
				Shell  string
				Locked *bool
			}
			json.Unmarshal(raw, &p)
			st, err := a.accountsNow(env)
			if err != nil {
				return nil, nil, err
			}
			u, _ := st.user(p.Name)
			undo := map[string]any{"name": p.Name}
			if p.Groups != nil {
				if err := acct(env, map[string]any{"op": "groups", "name": p.Name, "groups": p.Groups}); err != nil {
					return nil, nil, err
				}
				var prev []string
				for _, g := range u.Groups {
					if g != u.Name {
						prev = append(prev, g)
					}
				}
				if prev == nil {
					prev = []string{}
				}
				undo["groups"] = prev
			}
			if p.Shell != "" && p.Shell != u.Shell {
				if err := acct(env, map[string]any{"op": "shell", "name": p.Name, "shell": p.Shell}); err != nil {
					return nil, nil, err
				}
				undo["shell"] = u.Shell
			}
			if p.Locked != nil && *p.Locked != u.Locked {
				if err := acct(env, map[string]any{"op": map[bool]string{true: "lock", false: "unlock"}[*p.Locked], "name": p.Name}); err != nil {
					return nil, nil, err
				}
				undo["locked"] = u.Locked
			}
			b, _ := json.Marshal(undo)
			return nil, &change.UndoRecipe{Kind: "sysuser.update", Params: b}, nil
		}})
	reg(change.Kind{Name: "sysuser.password", CLI: "berth system user passwd {{.name}}", Redact: []string{"password"},
		Params: change.ParamSchema{Props: map[string]change.Prop{"name": str("login name"), "password": str("empty = remove (key login only)")}, Required: []string{"name"}},
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			var p struct{ Name, Password string }
			json.Unmarshal(raw, &p)
			return change.Plan{Summary: map[bool]string{true: "plan.sysuser.password_clear", false: "plan.sysuser.password"}[p.Password == ""], SummaryP: map[string]any{"name": p.Name}, Risk: change.RiskMedium}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			var p struct{ Name, Password string }
			json.Unmarshal(raw, &p)
			return nil, nil, acct(env, map[string]any{"op": "password", "name": p.Name, "password": p.Password})
		}})
	for _, op := range []string{"create", "delete"} {
		op := op
		inv := map[string]string{"create": "delete", "delete": "create"}[op]
		reg(change.Kind{Name: "sysgroup." + op, CLI: "berth system group " + op + " {{.name}}",
			Params: change.ParamSchema{Props: map[string]change.Prop{"name": str("group name")}, Required: []string{"name"}},
			Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
				var p struct{ Name string }
				json.Unmarshal(raw, &p)
				if !jobUserRe.MatchString(p.Name) {
					return change.Plan{}, errs.New(400, "system.bad_username")
				}
				return change.Plan{Summary: "plan.sysgroup." + op, SummaryP: map[string]any{"name": p.Name}, Risk: change.RiskLow, Undoable: true,
					Commands: []string{"group" + map[string]string{"create": "add", "delete": "del"}[op] + " " + p.Name}}, nil
			},
			Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
				var p struct{ Name string }
				json.Unmarshal(raw, &p)
				if err := acct(env, map[string]any{"op": "group_" + op, "name": p.Name}); err != nil {
					return nil, nil, err
				}
				return nil, &change.UndoRecipe{Kind: "sysgroup." + inv, Params: raw}, nil
			}})
	}

	// ---- kernel parameters ----
	reg(change.Kind{Name: "sysctl.set", CLI: "berth system sysctl",
		Params: change.ParamSchema{Props: map[string]change.Prop{"values": {Type: "object", Desc: `{"vm.swappiness":"10"}; "" removes Berth's setting`},
			"runtime": {Type: "object", Desc: "values to apply now without keeping (used by undo)"}}, Required: []string{"values"}},
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			var p struct{ Values, Runtime map[string]string }
			json.Unmarshal(raw, &p)
			st, err := svc(env).tuningNow(env)
			if err != nil {
				return change.Plan{}, err
			}
			keys := make([]string, 0, len(p.Values))
			for k := range p.Values {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			lines := []string{}
			for _, k := range keys {
				if v := p.Values[k]; v != "" {
					lines = append(lines, fmt.Sprintf("%s: %s → %s", k, st.Sysctl.Values[k], v))
				} else {
					lines = append(lines, fmt.Sprintf("%s: %s (no longer set at boot)", k, st.Sysctl.Values[k]))
				}
			}
			return change.Plan{Summary: "plan.sysctl.set", SummaryP: map[string]any{"n": len(keys)}, Risk: change.RiskMedium, Undoable: true, Commands: lines}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			a := svc(env)
			var p struct{ Values, Runtime map[string]string }
			json.Unmarshal(raw, &p)
			st, err := a.tuningNow(env)
			if err != nil {
				return nil, nil, err
			}
			file := map[string]string{}
			for k, v := range st.Sysctl.File {
				file[k] = v
			}
			apply := map[string]string{}
			for k, v := range p.Runtime {
				apply[k] = v
			}
			undoVals, undoRun := map[string]string{}, map[string]string{}
			for k, v := range p.Values {
				undoVals[k] = st.Sysctl.File[k]
				if v == "" {
					delete(file, k)
					continue
				}
				file[k], apply[k] = v, v
				if cur, ok := st.Sysctl.Values[k]; ok {
					undoRun[k] = cur
				}
			}
			if err := a.call(env.Ctx, env.Host, "POST", "/v1/system/sysctl", map[string]any{"file": file, "apply": apply}, nil); err != nil {
				return nil, nil, err
			}
			u, _ := json.Marshal(map[string]any{"values": undoVals, "runtime": undoRun})
			return nil, &change.UndoRecipe{Kind: "sysctl.set", Params: u}, nil
		}})

	// ---- swap and zram ----
	reg(change.Kind{Name: "swap.set", CLI: "berth system swap", Heavy: true,
		Params: change.ParamSchema{Props: map[string]change.Prop{"zram": str(`zram size: "ram / 2", "ram", MiB, "0" = off, "" = default`), "file_mb": {Type: "integer", Desc: "Berth's swap file in MiB (0 = remove)"}}},
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			var p struct {
				Zram   *string `json:"zram"`
				FileMB *int    `json:"file_mb"`
			}
			json.Unmarshal(raw, &p)
			st, err := svc(env).tuningNow(env)
			if err != nil {
				return change.Plan{}, err
			}
			lines := []string{}
			if p.Zram != nil {
				lines = append(lines, "zram: "+firstNonEmpty(st.Swap.ZramSize, "—")+" → "+firstNonEmpty(*p.Zram, "default"))
			}
			if p.FileMB != nil {
				lines = append(lines, fmt.Sprintf("/var/swap/berth.swap: %d MiB → %d MiB", st.Swap.BerthMB, *p.FileMB))
			}
			return change.Plan{Summary: "plan.swap.set", Risk: change.RiskMedium, Undoable: true, Commands: lines}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			a := svc(env)
			var p struct {
				Zram   *string `json:"zram"`
				FileMB *int    `json:"file_mb"`
			}
			json.Unmarshal(raw, &p)
			st, err := a.tuningNow(env)
			if err != nil {
				return nil, nil, err
			}
			if err := agentOp(env, "/v1/system/swap", p); err != nil {
				return nil, nil, err
			}
			undo := map[string]any{}
			if p.Zram != nil {
				if st.Swap.ZramCustom {
					undo["zram"] = st.Swap.ZramSize
				} else {
					undo["zram"] = ""
				}
			}
			if p.FileMB != nil {
				undo["file_mb"] = st.Swap.BerthMB
			}
			u, _ := json.Marshal(undo)
			return nil, &change.UndoRecipe{Kind: "swap.set", Params: u}, nil
		}})

	// ---- resolver ----
	reg(change.Kind{Name: "dns.resolver", CLI: "berth system resolver",
		Params: change.ParamSchema{Props: map[string]change.Prop{"servers": {Type: "array", Desc: `["1.1.1.1","223.5.5.5"]; [] = back to what the network hands out`}}, Required: []string{"servers"}},
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			var p struct{ Servers []string }
			json.Unmarshal(raw, &p)
			st, err := svc(env).tuningNow(env)
			if err != nil {
				return change.Plan{}, err
			}
			if !st.Resolver.Resolved {
				return change.Plan{}, errs.New(409, "system.resolver_unmanaged")
			}
			return change.Plan{Summary: "plan.dns.resolver", SummaryP: map[string]any{"servers": firstNonEmpty(strings.Join(p.Servers, " "), "—")}, Risk: change.RiskMedium, Undoable: true,
				Commands: []string{"DNS=" + strings.Join(p.Servers, " "), "systemctl restart systemd-resolved"}}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			a := svc(env)
			var p struct{ Servers []string }
			json.Unmarshal(raw, &p)
			st, _ := a.tuningNow(env)
			if err := a.call(env.Ctx, env.Host, "POST", "/v1/system/resolver", map[string]any{"servers": p.Servers}, nil); err != nil {
				return nil, nil, err
			}
			prev := st.Resolver.Servers
			if prev == nil {
				prev = []string{}
			}
			u, _ := json.Marshal(map[string]any{"servers": prev})
			return nil, &change.UndoRecipe{Kind: "dns.resolver", Params: u}, nil
		}})

	// ---- automatic updates ----
	reg(change.Kind{Name: "system.auto_updates", CLI: "berth system auto-updates", Heavy: true,
		Params: change.ParamSchema{Props: map[string]change.Prop{"enabled": {Type: "boolean"}, "type": {Type: "string", Enum: []string{"security", "all"}}, "time": str("HH:MM window start")},
			Required: []string{"enabled"}},
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			var p struct {
				Enabled    bool
				Type, Time string
			}
			p.Type = "security"
			json.Unmarshal(raw, &p)
			if !p.Enabled {
				return change.Plan{Summary: "plan.system.auto_updates_off", Risk: change.RiskLow, Undoable: true}, nil
			}
			return change.Plan{Summary: "plan.system.auto_updates_" + p.Type, SummaryP: map[string]any{"time": firstNonEmpty(p.Time, "—")}, Risk: change.RiskMedium, Undoable: true}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			a := svc(env)
			var p map[string]any
			json.Unmarshal(raw, &p)
			if p["type"] == nil || p["type"] == "" {
				p["type"] = "security"
			}
			st, _ := a.tuningNow(env)
			if err := agentOp(env, "/v1/system/auto-update", p); err != nil {
				return nil, nil, err
			}
			au := st.AutoUpdate
			u, _ := json.Marshal(map[string]any{"enabled": au.Installed && au.Enabled, "type": firstNonEmpty(au.Type, "security"), "time": au.Time})
			return nil, &change.UndoRecipe{Kind: "system.auto_updates", Params: u}, nil
		}})
}
