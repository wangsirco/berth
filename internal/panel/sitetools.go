package panel

// Site extras: the access log view and tamper protection (防篡改).

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"

	"github.com/wangsirco/berth/internal/change"
	"github.com/wangsirco/berth/internal/errs"
)

func init() {
	routeHooks = append(routeHooks, func(a *App, mux *http.ServeMux) {
		mux.HandleFunc("GET /api/v1/hosts/{host}/sites/{id}/log", a.authed("sites.view", func(w http.ResponseWriter, r *http.Request) {
			id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
			s, err := a.site(r.PathValue("host"), id)
			if err != nil {
				a.fail(w, r, err)
				return
			}
			q := url.Values{"domain": {s.Primary()}, "lines": {r.URL.Query().Get("lines")}, "q": {r.URL.Query().Get("q")}}
			var out json.RawMessage
			if err := a.call(r.Context(), r.PathValue("host"), "GET", "/v1/edge/log?"+q.Encode(), nil, &out); err != nil {
				a.fail(w, r, err)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.Write(out)
		}))
		mux.HandleFunc("GET /api/v1/hosts/{host}/sites/{id}/tamper", a.authed("sites.view", func(w http.ResponseWriter, r *http.Request) {
			id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
			s, err := a.site(r.PathValue("host"), id)
			if err != nil {
				a.fail(w, r, err)
				return
			}
			var out struct {
				On bool `json:"on"`
			}
			a.call(r.Context(), r.PathValue("host"), "GET", "/v1/edge/tamper?domain="+url.QueryEscape(s.Primary()), nil, &out)
			writeJSON(w, 200, map[string]any{"on": out.On, "writable": a.setting("tamper." + r.PathValue("host") + "." + r.PathValue("id"))})
		}))
	})
	change.Register(change.Kind{
		Name: "site.tamper", Perm: "sites.manage", Scope: "sites:write", Title: "kind.site.tamper", CLI: "berth site tamper {{.id}} {{.on}}",
		Params: change.ParamSchema{Props: map[string]change.Prop{"id": {Type: "integer"}, "on": {Type: "boolean"},
			"writable": {Type: "array", Desc: "sub-directories that stay writable, e.g. wp-content/uploads"}}, Required: []string{"id", "on"}},
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			var p struct {
				ID       int64
				On       bool
				Writable []string
			}
			json.Unmarshal(raw, &p)
			s, err := svc(env).site(env.Host, p.ID)
			if err != nil {
				return change.Plan{}, err
			}
			if s.Target.Type != "static" && s.Target.Type != "php" {
				return change.Plan{}, errs.New(409, "site.tamper_files_only")
			}
			if !p.On {
				return change.Plan{Summary: "plan.site.tamper_off", SummaryP: map[string]any{"domain": s.Primary()}, Risk: change.RiskLow, Undoable: true}, nil
			}
			cmds := []string{"chattr -R +i /srv/berth/caddy/sites/" + s.Primary()}
			for _, w := range p.Writable {
				cmds = append(cmds, "chattr -R -i …/"+w+"  (stays writable)")
			}
			return change.Plan{Summary: "plan.site.tamper_on", SummaryP: map[string]any{"domain": s.Primary()}, Risk: change.RiskMedium, Undoable: true, Commands: cmds}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			a := svc(env)
			var p struct {
				ID       int64
				On       bool
				Writable []string
			}
			json.Unmarshal(raw, &p)
			s, err := a.site(env.Host, p.ID)
			if err != nil {
				return nil, nil, err
			}
			key := "tamper." + env.Host + "." + strconv.FormatInt(p.ID, 10)
			prevW := a.setting(key)
			var prev struct {
				On bool `json:"on"`
			}
			a.call(env.Ctx, env.Host, "GET", "/v1/edge/tamper?domain="+url.QueryEscape(s.Primary()), nil, &prev)
			if err := a.call(env.Ctx, env.Host, "POST", "/v1/edge/tamper", map[string]any{"domain": s.Primary(), "on": p.On, "writable": orEmpty(p.Writable)}, nil); err != nil {
				return nil, nil, err
			}
			wj, _ := json.Marshal(orEmpty(p.Writable))
			a.setSetting(key, string(wj))
			var pw []string
			json.Unmarshal([]byte(prevW), &pw)
			u, _ := json.Marshal(map[string]any{"id": p.ID, "on": prev.On, "writable": orEmpty(pw)})
			return nil, &change.UndoRecipe{Kind: "site.tamper", Params: u}, nil
		},
	})
}
