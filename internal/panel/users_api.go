package panel

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/wangsirco/berth/internal/change"
	"github.com/wangsirco/berth/internal/errs"
)

func svc(env change.Env) *App { return env.Svc.(*App) }

type userCreateParams struct {
	Username     string    `json:"username"`
	DisplayName  string    `json:"display_name"`
	Role         string    `json:"role"`
	Perms        []string  `json:"perms"`
	Scope        UserScope `json:"scope"`
	PasswordHash string    `json:"password_hash"`
	Simple       bool      `json:"simple,omitempty"`
}

type userUpdateParams struct {
	ID          int64      `json:"id"`
	DisplayName *string    `json:"display_name,omitempty"`
	Role        *string    `json:"role,omitempty"`
	Perms       []string   `json:"perms,omitempty"`
	Scope       *UserScope `json:"scope,omitempty"`
	Disabled    *bool      `json:"disabled,omitempty"`
	Locale      *string    `json:"locale,omitempty"`
	Simple      *bool      `json:"simple,omitempty"`
}

func userObj(u *User) change.Object {
	return change.Object{Type: "user", ID: strconv.FormatInt(u.ID, 10), Name: u.Username}
}

func validPerms(ps []string) error {
	for _, p := range ps {
		d, ok := permDef(p)
		if !ok || d.Super {
			return errs.New(400, "user.bad_perm", "perm", p)
		}
	}
	return nil
}

func init() {
	userProps := map[string]change.Prop{
		"id": {Type: "integer"}, "username": {Type: "string"}, "display_name": {Type: "string"},
		"role":  {Type: "string", Enum: []string{RoleSuper, RoleAdmin, RoleViewer}},
		"perms": {Type: "array"}, "scope": {Type: "object"}, "disabled": {Type: "boolean"},
		"locale": {Type: "string"}, "password_hash": {Type: "string"}, "simple": {Type: "boolean", Desc: "open the app wall instead of the panel"},
	}
	change.Register(change.Kind{
		Name: "user.create", Perm: "users.manage", Scope: "users:write", Title: "kind.user.create",
		Params: change.ParamSchema{Props: userProps, Required: []string{"username", "role", "password_hash"}},
		CLI:    "berth user add {{.username}} --role {{.role}}", Redact: []string{"password_hash"},
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			var p userCreateParams
			json.Unmarshal(raw, &p)
			if !usernameRe.MatchString(p.Username) {
				return change.Plan{}, errs.New(400, "user.bad_username")
			}
			if err := validPerms(p.Perms); err != nil {
				return change.Plan{}, err
			}
			risk := change.RiskMedium
			if p.Role == RoleSuper {
				risk = change.RiskHigh
			}
			return change.Plan{Summary: "plan.user.create", SummaryP: map[string]any{"username": p.Username, "role": p.Role},
				Affected: []change.Object{{Type: "user", ID: p.Username, Name: p.Username}}, Risk: risk, Undoable: true}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			var p userCreateParams
			json.Unmarshal(raw, &p)
			u, err := svc(env).insertUser(p.Username, p.DisplayName, p.PasswordHash, p.Role, p.Perms, p.Scope)
			if err != nil {
				return nil, nil, err
			}
			if p.Simple {
				u.Simple = true
				svc(env).saveUser(u)
			}
			disabled := true
			undo, _ := json.Marshal(userUpdateParams{ID: u.ID, Disabled: &disabled})
			return u, &change.UndoRecipe{Kind: "user.update", Params: undo}, nil
		},
	})
	change.Register(change.Kind{
		Name: "user.update", Perm: "users.manage", Scope: "users:write", Title: "kind.user.update",
		Params: change.ParamSchema{Props: userProps, Required: []string{"id"}},
		CLI:    "berth user edit {{.id}}",
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			var p userUpdateParams
			json.Unmarshal(raw, &p)
			a := svc(env)
			u, err := a.getUser(p.ID)
			if err != nil {
				return change.Plan{}, err
			}
			if p.Perms != nil {
				if err := validPerms(p.Perms); err != nil {
					return change.Plan{}, err
				}
			}
			if p.Role != nil && !validRole(*p.Role) {
				return change.Plan{}, errs.New(400, "user.bad_role")
			}
			losesSuper := u.Role == RoleSuper && !u.Disabled &&
				((p.Role != nil && *p.Role != RoleSuper) || (p.Disabled != nil && *p.Disabled))
			if losesSuper && a.superCount() <= 1 {
				return change.Plan{}, errs.New(409, "user.last_super")
			}
			risk := change.RiskMedium
			if (p.Role != nil && *p.Role == RoleSuper) || losesSuper {
				risk = change.RiskHigh
			}
			return change.Plan{Summary: "plan.user.update", SummaryP: map[string]any{"username": u.Username},
				Affected: []change.Object{userObj(u)}, Risk: risk, Undoable: true}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			var p userUpdateParams
			json.Unmarshal(raw, &p)
			a := svc(env)
			u, err := a.getUser(p.ID)
			if err != nil {
				return nil, nil, err
			}
			before := userUpdateParams{ID: u.ID, DisplayName: ptr(u.DisplayName), Role: ptr(u.Role), Perms: append([]string{}, u.Perms...),
				Scope: ptr(u.Scope), Disabled: ptr(u.Disabled), Locale: ptr(u.Locale), Simple: ptr(u.Simple)}
			if p.DisplayName != nil {
				u.DisplayName = *p.DisplayName
			}
			if p.Role != nil {
				u.Role = *p.Role
			}
			if p.Perms != nil {
				u.Perms = p.Perms
			}
			if p.Scope != nil {
				u.Scope = *p.Scope
			}
			if p.Disabled != nil {
				u.Disabled = *p.Disabled
			}
			if p.Locale != nil {
				u.Locale = *p.Locale
			}
			if p.Simple != nil {
				u.Simple = *p.Simple
			}
			if err := a.saveUser(u); err != nil {
				return nil, nil, err
			}
			undo, _ := json.Marshal(before)
			return u, &change.UndoRecipe{Kind: "user.update", Params: undo}, nil
		},
	})
	change.Register(change.Kind{
		Name: "user.password", Perm: "users.manage", Scope: "users:write", Title: "kind.user.password",
		Params: change.ParamSchema{Props: userProps, Required: []string{"id", "password_hash"}},
		CLI:    "berth user passwd {{.id}}", Redact: []string{"password_hash"},
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			var p struct{ ID int64 }
			json.Unmarshal(raw, &p)
			u, err := svc(env).getUser(p.ID)
			if err != nil {
				return change.Plan{}, err
			}
			return change.Plan{Summary: "plan.user.password", SummaryP: map[string]any{"username": u.Username},
				Affected: []change.Object{userObj(u)}, Risk: change.RiskHigh}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			var p struct {
				ID           int64  `json:"id"`
				PasswordHash string `json:"password_hash"`
			}
			json.Unmarshal(raw, &p)
			a := svc(env)
			if err := a.setPasswordHash(p.ID, p.PasswordHash); err != nil {
				return nil, nil, err
			}
			_, err := a.db.Exec(`DELETE FROM sessions WHERE user_id=?`, p.ID)
			return nil, nil, err
		},
	})
}

func ptr[T any](v T) *T { return &v }

func (a *App) handleUsers(w http.ResponseWriter, r *http.Request) {
	us, err := a.listUsers()
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"items": us})
}

// handleUserCreate hashes the password here so plaintext never enters the pipeline.
func (a *App) handleUserCreate(w http.ResponseWriter, r *http.Request) {
	var in struct {
		userCreateParams
		Password string `json:"password"`
	}
	if err := readJSON(r, &in); err != nil {
		a.fail(w, r, err)
		return
	}
	if err := validPassword(in.Password); err != nil {
		a.fail(w, r, err)
		return
	}
	h, err := hashPassword(in.Password)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	p := in.userCreateParams
	p.PasswordHash = h
	raw, _ := json.Marshal(p)
	a.runChange(w, r, "local", "user.create", raw)
}

func (a *App) handleUserUpdate(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	var p userUpdateParams
	if err := readJSON(r, &p); err != nil {
		a.fail(w, r, err)
		return
	}
	p.ID = id
	raw, _ := json.Marshal(p)
	a.runChange(w, r, "local", "user.update", raw)
}

func (a *App) handleUserPassword(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	var in struct{ Password string }
	if err := readJSON(r, &in); err != nil {
		a.fail(w, r, err)
		return
	}
	if err := validPassword(in.Password); err != nil {
		a.fail(w, r, err)
		return
	}
	h, _ := hashPassword(in.Password)
	raw, _ := json.Marshal(map[string]any{"id": id, "password_hash": h})
	a.runChange(w, r, "local", "user.password", raw)
}

func (a *App) handlePermissions(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{"items": Permissions})
}
