package panel

// External changes (方案 §4.4 外部修改检测): every five minutes the panel asks each host's
// agent which managed files no longer match what Berth wrote, raises an alert for new ones,
// and offers "adopt as current" or "put Berth's version back" — each with the diff as its
// preview.

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/wangsirco/berth/internal/change"
	"github.com/wangsirco/berth/internal/errs"
)

type driftItem struct {
	Path   string `json:"path"`
	Kind   string `json:"kind"`
	Object string `json:"object"`
	State  string `json:"state"`
	Since  int64  `json:"since"`
}

var driftKnown sync.Map // host → map[path]bool

func (a *App) checkDrift(ctx context.Context, host string) ([]driftItem, error) {
	var out struct {
		Items []driftItem `json:"items"`
	}
	if err := a.call(ctx, host, "GET", "/v1/drift", nil, &out); err != nil {
		return nil, err
	}
	now := map[string]bool{}
	prevV, _ := driftKnown.Load(host)
	prev, _ := prevV.(map[string]bool)
	r, _ := a.alertRule("external_change")
	for _, d := range out.Items {
		now[d.Path] = true
		if !prev[d.Path] {
			a.fire(r, host+":"+d.Path, host, map[string]any{"path": d.Path, "kind": d.Kind, "object": d.Object})
			a.hub.publish(event{Type: "drift", Data: map[string]any{"host": host, "path": d.Path, "state": d.State}})
		}
	}
	for p := range prev {
		if !now[p] {
			a.resolve(r.Key, host+":"+p)
			a.hub.publish(event{Type: "drift", Data: map[string]any{"host": host, "path": p, "state": "ok"}})
		}
	}
	driftKnown.Store(host, now)
	if out.Items == nil {
		out.Items = []driftItem{}
	}
	return out.Items, nil
}

func (a *App) driftLoop(ctx context.Context) {
	t := time.NewTicker(5 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(30 * time.Second): // first check soon after start
		case <-t.C:
		}
		hosts := []string{"local"}
		for _, h := range a.remoteHosts() {
			hosts = append(hosts, h.Name)
		}
		for _, h := range hosts {
			cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
			a.checkDrift(cctx, h)
			cancel()
		}
	}
}

func (a *App) driftVersions(env change.Env, path string) (berth, now string, err error) {
	var v struct{ Berth, Now string }
	if err := a.call(env.Ctx, env.Host, "POST", "/v1/drift/versions", map[string]string{"path": path}, &v); err != nil {
		return "", "", err
	}
	return v.Berth, v.Now, nil
}

func init() {
	startHooks = append(startHooks, func(a *App, ctx context.Context) { go a.driftLoop(ctx) })
	routeHooks = append(routeHooks, func(a *App, mux *http.ServeMux) {
		mux.HandleFunc("GET /api/v1/hosts/{host}/drift", a.authed("system.view", func(w http.ResponseWriter, r *http.Request) {
			items, err := a.checkDrift(r.Context(), r.PathValue("host"))
			if err != nil {
				a.fail(w, r, err)
				return
			}
			writeJSON(w, 200, map[string]any{"items": items})
		}))
	})
	for _, op := range []string{"accept", "restore"} {
		op := op
		change.Register(change.Kind{Name: "drift." + op, Perm: "system.manage", Scope: "system:write", Title: "kind.drift." + op, CLI: "berth drift " + op + " {{.path}}",
			Params: change.ParamSchema{Props: map[string]change.Prop{"path": {Type: "string", Desc: "the changed file"}}, Required: []string{"path"}},
			Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
				var p struct{ Path string }
				json.Unmarshal(raw, &p)
				berth, now, err := svc(env).driftVersions(env, p.Path)
				if err != nil {
					return change.Plan{}, err
				}
				if berth == now {
					return change.Plan{}, errs.New(409, "drift.nothing")
				}
				// accept: shows what changed outside (Berth's → now); restore: what goes back (now → Berth's)
				diff := change.FileDiff{Path: p.Path, Before: berth, After: now}
				risk := change.RiskLow
				if op == "restore" {
					diff.Before, diff.After = now, berth
					risk = change.RiskMedium
				}
				return change.Plan{Summary: "plan.drift." + op, SummaryP: map[string]any{"path": p.Path}, Risk: risk, Files: []change.FileDiff{diff}}, nil
			},
			Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
				a := svc(env)
				var p struct{ Path string }
				json.Unmarshal(raw, &p)
				if err := a.call(env.Ctx, env.Host, "POST", "/v1/drift/"+op, raw, nil); err != nil {
					return nil, nil, err
				}
				a.checkDrift(env.Ctx, env.Host)
				return nil, nil, nil
			}})
	}
}
