package panel

// This server's own desktop (方案 §5.8): Xfce on a VNC display bound to 127.0.0.1, opened
// through the remote desktop page. Turning it on also keeps a "this server" connection in
// the library with a fresh password; nobody types or sees it.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/wangsirco/berth/internal/agent/desktop"
	"github.com/wangsirco/berth/internal/change"
	"github.com/wangsirco/berth/internal/errs"
)

func localOnly(env change.Env) error {
	if env.Host != "local" {
		return errs.New(400, "desktop.local_only")
	}
	return nil
}

// desktopConn: the library entry for this server's desktop, created or given a new password.
func (a *App) desktopConn(env change.Env, password string) (int64, error) {
	var id int64
	for _, c := range a.rdConns() {
		if c.System == "desktop" {
			id = c.ID
		}
	}
	c := rdConn{Name: "本机桌面", Group: "", Protocol: "vnc", Host: "127.0.0.1", Port: desktop.Port, System: "desktop", Clipboard: true}
	spec, _ := json.Marshal(c)
	b, _ := json.Marshal(map[string]string{"password": password})
	enc, err := a.seal(b)
	if err != nil {
		return 0, err
	}
	if id != 0 {
		_, err := a.db.Exec(`UPDATE rd_connections SET spec=?, name=?, secret_enc=? WHERE id=?`, string(spec), c.Name, enc, id)
		return id, err
	}
	res, err := a.db.Exec(`INSERT INTO rd_connections(name, grp, spec, secret_enc, created_by, created_at) VALUES(?,?,?,?,?,?)`, c.Name, "", string(spec), enc, env.ActorName, time.Now().Unix())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func init() {
	routeHooks = append(routeHooks, func(a *App, mux *http.ServeMux) {
		// GET /api/v1/desktop: state and the software list (welcome wizard, remote page)
		mux.HandleFunc("GET /api/v1/desktop", a.authed("settings.manage", func(w http.ResponseWriter, r *http.Request) {
			var out map[string]any
			if err := a.call(r.Context(), "local", "GET", "/v1/desktop?items=1", nil, &out); err != nil {
				a.fail(w, r, err)
				return
			}
			writeJSON(w, 200, out)
		}))
	})
	noParams := change.ParamSchema{Props: map[string]change.Prop{}}
	simple := func(name, path string, risk change.Risk, undo string, heavy bool, files func() []change.FileDiff) {
		change.Register(change.Kind{
			Name: name, Perm: "settings.manage", Scope: "system:write", Title: "kind." + name, Params: noParams,
			CLI: "berth desktop " + strings.TrimPrefix(name, "desktop."), Heavy: heavy, MemMB: map[bool]int{true: 256}[heavy],
			Plan: func(env change.Env, _ json.RawMessage) (change.Plan, error) {
				if err := localOnly(env); err != nil {
					return change.Plan{}, err
				}
				pl := change.Plan{Summary: "plan." + name, Risk: risk, Undoable: undo != ""}
				if files != nil {
					pl.Files = files()
				}
				return pl, nil
			},
			Apply: func(env change.Env, _ json.RawMessage) (any, *change.UndoRecipe, error) {
				a := svc(env)
				var out map[string]any
				if err := a.streamAgent(env, "local", path, nil, &out); err != nil {
					return nil, nil, err
				}
				if name == "desktop.uninstall" {
					a.db.Exec(`DELETE FROM rd_connections WHERE json_extract(spec, '$.system')='desktop'`)
				}
				var u *change.UndoRecipe
				if undo != "" {
					u = &change.UndoRecipe{Kind: undo, Params: json.RawMessage("{}")}
				}
				return out, u, nil
			},
		})
	}
	simple("desktop.disable", "/v1/desktop/disable", change.RiskLow, "desktop.enable", false, nil)
	simple("desktop.uninstall", "/v1/desktop/uninstall", change.RiskMedium, "", true, nil)

	change.Register(change.Kind{
		Name: "desktop.install", Perm: "settings.manage", Scope: "system:write", Title: "kind.desktop.install",
		Params: change.ParamSchema{Props: map[string]change.Prop{
			"items":  {Type: "array", Desc: "software to add: ids from GET /api/v1/remote (desktop.items)"},
			"preset": {Type: "string", Enum: []string{"", "basic", "dev"}, Desc: "pick a ready set instead of listing items"},
			"start":  {Type: "boolean", Desc: "turn the desktop on afterwards"}}},
		CLI: "berth desktop install", Heavy: true, MemMB: 256,
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			if err := localOnly(env); err != nil {
				return change.Plan{}, err
			}
			ids, mb, err := desktopItems(raw)
			if err != nil {
				return change.Plan{}, err
			}
			return change.Plan{Summary: "plan.desktop.install", SummaryP: map[string]any{"n": len(ids), "mb": 300 + mb}, Risk: change.RiskLow,
				Commands: []string{"dnf install xfce4-session xfwm4 xfce4-panel … tigervnc   # the desktop itself", "# then: " + strings.Join(ids, ", ")}}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			ids, _, err := desktopItems(raw)
			if err != nil {
				return nil, nil, err
			}
			a := svc(env)
			var out map[string]any
			if err := a.streamAgent(env, "local", "/v1/desktop/install", map[string]any{"items": ids}, &out); err != nil {
				return nil, nil, err
			}
			if !a.guacdRunning(env.Ctx) { // the desktop opens through it
				env.Log("starting the remote desktop component (guacd)")
				if err := a.ensureGuacd(env); err != nil {
					return nil, nil, err
				}
			}
			var p struct{ Start bool }
			json.Unmarshal(raw, &p)
			if p.Start { // "install and open": turn it on in the same step
				if out, err = a.enableDesktop(env, ""); err != nil {
					return nil, nil, err
				}
			}
			return out, nil, nil
		},
	})

	change.Register(change.Kind{
		Name: "desktop.enable", Perm: "settings.manage", Scope: "system:write", Title: "kind.desktop.enable",
		Params: change.ParamSchema{Props: map[string]change.Prop{"user": {Type: "string", Desc: "existing non-root user to run the desktop as (default berthdesk, created when missing)"}}},
		CLI:    "berth desktop enable",
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			if err := localOnly(env); err != nil {
				return change.Plan{}, err
			}
			var p struct{ User string }
			json.Unmarshal(raw, &p)
			if p.User == "" {
				p.User = desktop.DefaultUser
			}
			return change.Plan{Summary: "plan.desktop.enable", SummaryP: map[string]any{"user": p.User, "port": desktop.Port}, Risk: change.RiskMedium, Undoable: true,
				Commands: []string{"systemctl enable --now " + desktop.Unit + "   # Xvnc -localhost -rfbport 5909 + startxfce4"}}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			var p struct{ User string }
			json.Unmarshal(raw, &p)
			out, err := svc(env).enableDesktop(env, p.User)
			if err != nil {
				return nil, nil, err
			}
			return out, &change.UndoRecipe{Kind: "desktop.disable", Params: json.RawMessage("{}")}, nil
		},
	})
}

// desktopItems: the picked software (or a preset's), with an estimated download size.
func desktopItems(raw json.RawMessage) ([]string, int, error) {
	var p struct {
		Items  []string `json:"items"`
		Preset string   `json:"preset"`
	}
	json.Unmarshal(raw, &p)
	if p.Preset != "" {
		p.Items = desktop.PresetItems(p.Preset)
	}
	mb := 0
	for _, id := range p.Items {
		found := false
		for _, it := range desktop.Items {
			if it.ID == id {
				found, mb = true, mb+it.SizeMB
			}
		}
		if !found {
			return nil, 0, errs.New(400, "desktop.bad_item", "id", id)
		}
	}
	return p.Items, mb, nil
}

// enableDesktop turns the desktop on with a fresh password kept in the connection library.
func (a *App) enableDesktop(env change.Env, user string) (map[string]any, error) {
	pw := randomPassword(8) // VNC uses at most 8 characters
	var out map[string]any
	if err := a.streamAgent(env, "local", "/v1/desktop/enable", map[string]string{"user": user, "password": pw, "lang": desktopLang(a.deskLocale(env))}, &out); err != nil {
		return nil, err
	}
	id, err := a.desktopConn(env, pw)
	if err != nil {
		return nil, err
	}
	if out == nil {
		out = map[string]any{}
	}
	out["connection"] = id
	return out, nil
}

// desktopLang: the desktop speaks the panel's language.
func desktopLang(locale string) string {
	switch locale {
	case "zh-CN":
		return "zh_CN.UTF-8"
	case "zh-TW":
		return "zh_TW.UTF-8"
	}
	return "en_US.UTF-8"
}

// deskLocale: the panel's language, or else the language of whoever turns it on.
func (a *App) deskLocale(env change.Env) string {
	if l := a.setting("panel.locale"); l != "" {
		return l
	}
	var uid int64
	fmt.Sscanf(env.ActorID, "%d", &uid)
	if u, err := a.userByID(uid); err == nil && u != nil && u.Locale != "" {
		return u.Locale
	}
	return "zh-CN"
}
