package panel

// Berth's own optional extensions (R5): the site WAF (Coraza in the edge) and virus scanning
// (ClamAV). Neither is installed by default; whether one is comes from the host itself (the
// edge's image, the ClamAV image), so hosts that already used them count as installed.

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/wangsirco/berth/internal/agent"
	"github.com/wangsirco/berth/internal/agent/caddy"
	"github.com/wangsirco/berth/internal/agent/docker"
	"github.com/wangsirco/berth/internal/change"
	"github.com/wangsirco/berth/internal/errs"
	"github.com/wangsirco/berth/internal/panel/caddycfg"
)

type wafSite struct {
	ID     int64         `json:"id"`
	Domain string        `json:"domain"`
	WAF    *caddycfg.WAF `json:"waf"`
}

// wafSites: the sites with the WAF on.
func (a *App) wafSites(host string) []wafSite {
	out := []wafSite{}
	ss, _ := a.sites(host)
	for _, s := range ss {
		if s.Options.WAF != nil {
			out = append(out, wafSite{s.ID, s.Primary(), s.Options.WAF})
		}
	}
	return out
}

func (a *App) avState(ctx context.Context, host string) (agent.AVStatus, error) {
	var st agent.AVStatus
	err := a.call(ctx, host, "GET", "/v1/av/status", nil, &st)
	return st, err
}

// avJobs: scheduled jobs that run a virus scan.
func (a *App) avJobs(host string) []string {
	out := []string{}
	for _, j := range a.jobs(host) {
		if j.Action.Type == "change" && j.Action.Kind == "av.scan" {
			out = append(out, j.Name)
		}
	}
	return out
}

func init() {
	routeHooks = append(routeHooks, func(a *App, mux *http.ServeMux) {
		mux.HandleFunc("GET /api/v1/hosts/{host}/extensions/builtin", a.authed("overview.view", func(w http.ResponseWriter, r *http.Request) {
			host := r.PathValue("host")
			edge, eerr := a.edgeStatus(r.Context(), host)
			av, aerr := a.avState(r.Context(), host)
			out := map[string]any{
				"waf": map[string]any{"installed": eerr == nil && edge.WAF && edge.Running, "edge": edge.Installed && edge.Running, "image": edge.RunningImage, "sites": a.wafSites(host)},
				"av":  map[string]any{"installed": aerr == nil && av.Installed, "db_date": av.DBDate, "jobs": a.avJobs(host)},
			}
			writeJSON(w, 200, out)
		}))
	})

	noParams := change.ParamSchema{Props: map[string]change.Prop{}}
	sitesProp := change.ParamSchema{Props: map[string]change.Prop{"sites": {Type: "array", Desc: "sites whose WAF settings to put back (undo of a removal)"}}}

	change.Register(change.Kind{
		Name: "ext.waf.install", Perm: "sites.manage", Scope: "sites:write", Title: "kind.ext.waf.install", CLI: "berth ext waf install",
		Heavy: true, MemMB: 1200, Params: sitesProp,
		Plan: func(env change.Env, _ json.RawMessage) (change.Plan, error) {
			st, err := svc(env).edgeStatus(env.Ctx, env.Host)
			if err != nil {
				return change.Plan{}, err
			}
			if !st.Installed || !st.Running {
				return change.Plan{}, errs.New(409, "edge.not_installed")
			}
			if st.WAF {
				return change.Plan{}, errs.New(409, "waf.installed")
			}
			return change.Plan{Summary: "plan.ext.waf.install", Risk: change.RiskMedium, Undoable: true,
				Files: []change.FileDiff{{Path: docker.StacksDir + "/" + caddy.Project + "/compose.yaml", Before: caddy.ComposeFor(false), After: caddy.ComposeFor(true)}},
				Commands: []string{"build " + caddy.ImageWAF + " (Caddy with the Coraza WAF and the OWASP Core Rule Set; a few minutes the first time)",
					"recreate berth-caddy with it (sites pause for a few seconds)"},
				Affected: []change.Object{projObj(caddy.Project, env.Host)}}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			a := svc(env)
			var p struct{ Sites []wafSite }
			json.Unmarshal(raw, &p)
			if err := a.rebuildEdge(env, true); err != nil {
				return nil, nil, err
			}
			wafCache.Delete(env.Host)
			// an undone removal: the sites get their WAF settings back
			for _, ws := range p.Sites {
				s, err := a.site(env.Host, ws.ID)
				if err != nil || ws.WAF == nil {
					continue
				}
				s.Options.WAF = ws.WAF
				if err := a.saveSite(env.Host, &s, env.ActorName); err == nil {
					env.Log("WAF back on for %s", s.Primary())
				}
			}
			if len(p.Sites) > 0 {
				if err := a.applyEdge(env); err != nil {
					return nil, nil, err
				}
			}
			return nil, &change.UndoRecipe{Kind: "ext.waf.remove", Params: json.RawMessage(`{}`)}, nil
		},
	})
	change.Register(change.Kind{
		Name: "ext.waf.remove", Perm: "sites.manage", Scope: "sites:write", Title: "kind.ext.waf.remove", CLI: "berth ext waf remove",
		Heavy: true, MemMB: 1200, Params: noParams,
		Plan: func(env change.Env, _ json.RawMessage) (change.Plan, error) {
			a := svc(env)
			st, err := a.edgeStatus(env.Ctx, env.Host)
			if err != nil {
				return change.Plan{}, err
			}
			if !st.WAF {
				return change.Plan{}, errs.New(409, "waf.not_installed")
			}
			cmds := []string{}
			aff := []change.Object{projObj(caddy.Project, env.Host)}
			for _, s := range a.wafSites(env.Host) {
				cmds = append(cmds, "turn the WAF off for "+s.Domain+" (it comes back if you undo)")
				aff = append(aff, change.Object{Type: "site", ID: strconv.FormatInt(s.ID, 10), Name: s.Domain, Host: env.Host})
			}
			cmds = append(cmds, "build "+caddy.Image+" (Caddy without the WAF) and recreate berth-caddy with it (sites pause for a few seconds)")
			return change.Plan{Summary: "plan.ext.waf.remove", SummaryP: map[string]any{"n": len(cmds) - 1}, Risk: change.RiskMedium, Undoable: true,
				Commands: cmds, Affected: aff}, nil
		},
		Apply: func(env change.Env, _ json.RawMessage) (any, *change.UndoRecipe, error) {
			a := svc(env)
			was := a.wafSites(env.Host)
			for _, ws := range was {
				s, err := a.site(env.Host, ws.ID)
				if err != nil {
					continue
				}
				s.Options.WAF = nil
				if err := a.saveSite(env.Host, &s, env.ActorName); err != nil {
					return nil, nil, err
				}
				env.Log("WAF off for %s", s.Primary())
			}
			if len(was) > 0 {
				if err := a.applyEdge(env); err != nil {
					return nil, nil, err
				}
			}
			if err := a.rebuildEdge(env, false); err != nil {
				return nil, nil, err
			}
			u, _ := json.Marshal(map[string]any{"sites": was})
			return map[string]any{"sites": len(was)}, &change.UndoRecipe{Kind: "ext.waf.install", Params: u}, nil
		},
	})
	change.Register(change.Kind{
		Name: "ext.av.install", Perm: "system.manage", Scope: "system:write", Title: "kind.ext.av.install", CLI: "berth ext av install",
		Heavy: true, MemMB: 600, Params: noParams,
		Plan: func(env change.Env, _ json.RawMessage) (change.Plan, error) {
			st, err := svc(env).avState(env.Ctx, env.Host)
			if err != nil {
				return change.Plan{}, err
			}
			if st.Installed {
				return change.Plan{}, errs.New(409, "av.installed")
			}
			return change.Plan{Summary: "plan.ext.av.install", Risk: change.RiskLow, Undoable: true,
				Commands: []string{"docker pull " + st.Image + "   (about 560 MB; nothing is installed on the system itself)",
					"docker volume create berth-clamav-db; freshclam   (the virus signatures)"}}, nil
		},
		Apply: func(env change.Env, _ json.RawMessage) (any, *change.UndoRecipe, error) {
			if err := agentOp(env, "/v1/av/install", nil); err != nil {
				return nil, nil, err
			}
			return nil, &change.UndoRecipe{Kind: "ext.av.remove", Params: json.RawMessage(`{}`)}, nil
		},
	})
	change.Register(change.Kind{
		Name: "ext.av.remove", Perm: "system.manage", Scope: "system:write", Title: "kind.ext.av.remove", CLI: "berth ext av remove",
		Heavy: true, Params: noParams,
		Plan: func(env change.Env, _ json.RawMessage) (change.Plan, error) {
			a := svc(env)
			st, err := a.avState(env.Ctx, env.Host)
			if err != nil {
				return change.Plan{}, err
			}
			if !st.Installed && !st.Volume {
				return change.Plan{}, errs.New(409, "av.not_installed")
			}
			cmds := []string{"docker rmi " + st.Image, "docker volume rm berth-clamav-db   (the signatures)"}
			for _, j := range a.avJobs(env.Host) {
				cmds = append(cmds, "the scheduled job \""+j+"\" will fail until the extension is back: delete or change it")
			}
			return change.Plan{Summary: "plan.ext.av.remove", Risk: change.RiskLow, Undoable: true, Commands: cmds}, nil
		},
		Apply: func(env change.Env, _ json.RawMessage) (any, *change.UndoRecipe, error) {
			if err := agentOp(env, "/v1/av/remove", nil); err != nil {
				return nil, nil, err
			}
			return nil, &change.UndoRecipe{Kind: "ext.av.install", Params: json.RawMessage(`{}`)}, nil
		},
	})
}
