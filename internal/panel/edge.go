package panel

// The edge: one Caddy container per host (berth-caddy on the berth-edge network) that
// serves every site. Installing it is a change like any other: preview, record, undo.

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/wangsirco/berth/internal/agent/caddy"
	"github.com/wangsirco/berth/internal/agent/docker"
	"github.com/wangsirco/berth/internal/change"
	"github.com/wangsirco/berth/internal/errs"
)

type edgeStatus struct {
	Installed    bool   `json:"installed"`
	Running      bool   `json:"running"`
	State        string `json:"state,omitempty"`
	Image        string `json:"image"`
	RunningImage string `json:"running_image,omitempty"`
	Outdated     bool   `json:"outdated"` // running an older Caddy build than this panel ships
	WAF          bool   `json:"waf"`      // the running build carries the Coraza WAF
}

func (a *App) edgeStatus(ctx context.Context, host string) (edgeStatus, error) {
	var st edgeStatus
	err := a.call(ctx, host, "GET", "/v1/edge/status", nil, &st)
	st.Image = caddy.ImageFor(st.WAF)
	st.Outdated = st.RunningImage != "" && st.RunningImage != st.Image
	if err == nil {
		wafCache.Store(host, wafSeen{st.WAF && st.Running, time.Now()})
	}
	return st, err
}

func init() {
	routeHooks = append(routeHooks, func(a *App, mux *http.ServeMux) {
		mux.HandleFunc("GET /api/v1/hosts/{host}/edge", a.authed("sites.view", func(w http.ResponseWriter, r *http.Request) {
			st, err := a.edgeStatus(r.Context(), r.PathValue("host"))
			if err != nil {
				a.fail(w, r, err)
				return
			}
			writeJSON(w, 200, st)
		}))
	})

	stackDir := docker.StacksDir + "/" + caddy.Project
	noParams := change.ParamSchema{Props: map[string]change.Prop{}}

	change.Register(change.Kind{
		Name: "edge.install", Perm: "sites.manage", Scope: "sites:write", Title: "kind.edge.install", Params: noParams,
		CLI: "berth edge install", Heavy: true, MemMB: 1200, // the first start builds the Caddy image
		Plan: func(env change.Env, _ json.RawMessage) (change.Plan, error) {
			st, err := svc(env).edgeStatus(env.Ctx, env.Host)
			if err != nil {
				return change.Plan{}, err
			}
			if st.Installed && st.Running {
				return change.Plan{}, errs.New(409, "edge.installed")
			}
			return change.Plan{Summary: "plan.edge.install", SummaryP: map[string]any{"image": caddy.Image}, Risk: change.RiskMedium,
				Files: []change.FileDiff{
					{Path: stackDir + "/compose.yaml", After: caddy.Compose()},
					{Path: stackDir + "/Dockerfile", After: caddy.Dockerfile},
					{Path: caddy.Root + "/config/config.json", After: string(caddy.Initial())},
				},
				Commands: []string{
					"mkdir -p " + caddy.Root + "/{config,data,logs,sites,run}",
					"docker network create " + caddy.Network,
					"docker compose -p " + caddy.Project + " up -d   # " + "builds " + caddy.Image + " on first run",
				},
				Affected: []change.Object{projObj(caddy.Project, env.Host), {Type: "network", ID: caddy.Network}},
				Undoable: true}, nil
		},
		Apply: func(env change.Env, _ json.RawMessage) (any, *change.UndoRecipe, error) {
			a := svc(env)
			env.Log("::step 0 prepare")
			var prep map[string]any
			if err := a.call(env.Ctx, env.Host, "POST", "/v1/edge/prepare", nil, &prep); err != nil {
				return nil, nil, err
			}
			env.Log("::step 1 up")
			if err := agentOp(env, "/v1/compose/projects/"+caddy.Project+"/up", nil); err != nil {
				return nil, nil, err
			}
			a.relate(env.Host, change.Object{Type: "host", ID: env.Host}, projObj(caddy.Project, env.Host), "manages", "module")
			return prep, &change.UndoRecipe{Kind: "edge.stop", Params: json.RawMessage(`{}`)}, nil
		},
	})

	change.Register(change.Kind{
		Name: "edge.upgrade", Perm: "sites.manage", Scope: "sites:write", Title: "kind.edge.upgrade", Params: noParams,
		CLI: "berth edge upgrade", Heavy: true, MemMB: 1200, // builds the new Caddy image
		Plan: func(env change.Env, _ json.RawMessage) (change.Plan, error) {
			st, err := svc(env).edgeStatus(env.Ctx, env.Host)
			if err != nil {
				return change.Plan{}, err
			}
			if !st.Installed {
				return change.Plan{}, errs.New(409, "edge.not_installed")
			}
			if !st.Outdated {
				return change.Plan{}, errs.New(409, "edge.up_to_date")
			}
			return change.Plan{Summary: "plan.edge.upgrade", SummaryP: map[string]any{"from": st.RunningImage, "to": st.Image}, Risk: change.RiskMedium,
				Files:    []change.FileDiff{{Path: stackDir + "/Dockerfile", After: caddy.Dockerfile}, {Path: stackDir + "/compose.yaml", After: caddy.ComposeFor(st.WAF)}},
				Commands: []string{"docker compose -p " + caddy.Project + " up -d   # builds " + st.Image + ", then recreates the container (sites pause for a few seconds)"},
				Affected: []change.Object{projObj(caddy.Project, env.Host)}}, nil
		},
		Apply: func(env change.Env, _ json.RawMessage) (any, *change.UndoRecipe, error) {
			a := svc(env)
			st, _ := a.edgeStatus(env.Ctx, env.Host)
			if err := a.rebuildEdge(env, st.WAF); err != nil {
				return nil, nil, err
			}
			return map[string]string{"image": caddy.ImageFor(st.WAF)}, nil, nil
		},
	})

	change.Register(change.Kind{
		Name: "edge.stop", Perm: "sites.manage", Scope: "sites:write", Title: "kind.edge.stop", Params: noParams,
		CLI: "berth edge stop", Heavy: true,
		Plan: func(env change.Env, _ json.RawMessage) (change.Plan, error) {
			return change.Plan{Summary: "plan.edge.stop", Risk: change.RiskHigh,
				Commands: []string{"docker compose -p " + caddy.Project + " down   # files under " + caddy.Root + " are kept"},
				Affected: []change.Object{projObj(caddy.Project, env.Host)}, Undoable: true}, nil
		},
		Apply: func(env change.Env, _ json.RawMessage) (any, *change.UndoRecipe, error) {
			if err := agentOp(env, "/v1/compose/projects/"+caddy.Project+"/down", nil); err != nil {
				return nil, nil, err
			}
			return nil, &change.UndoRecipe{Kind: "edge.install", Params: json.RawMessage(`{}`)}, nil
		},
	})
}

// rebuildEdge writes the stack for the edge with or without the WAF, builds it if needed and
// recreates the container (sites pause for a few seconds); the new container starts from the
// saved config, then site containers are reconnected.
func (a *App) rebuildEdge(env change.Env, waf bool) error {
	env.Log("::step 0 prepare")
	if err := a.call(env.Ctx, env.Host, "POST", "/v1/edge/prepare", map[string]bool{"waf": waf}, nil); err != nil {
		return err
	}
	env.Log("::step 1 build and restart")
	if err := agentOp(env, "/v1/compose/projects/"+caddy.Project+"/up", nil); err != nil {
		return err
	}
	wafCache.Delete(env.Host)
	// wait until the new container answers on its admin socket (loading the saved config
	// again changes nothing and proves it)
	var cfg json.RawMessage
	if err := a.call(env.Ctx, env.Host, "GET", "/v1/edge/config", nil, &cfg); err == nil {
		var last error
		for i := 0; i < 60; i++ {
			if last = a.call(env.Ctx, env.Host, "POST", "/v1/edge/load", cfg, nil); last == nil {
				break
			}
			time.Sleep(time.Second)
		}
		if last != nil {
			return last
		}
		env.Log("the edge is answering")
	}
	a.pruneEdgeImages(env)
	go func(host string) {
		time.Sleep(3 * time.Second)
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		a.ensureEdge(ctx, host)
	}(env.Host)
	return nil
}

// wafInstalled: does the host's edge carry the WAF (cached for a minute)?
type wafSeen struct {
	on bool
	at time.Time
}

var wafCache sync.Map

func (a *App) wafInstalled(ctx context.Context, host string) bool {
	if v, ok := wafCache.Load(host); ok && time.Since(v.(wafSeen).at) < time.Minute {
		return v.(wafSeen).on
	}
	st, err := a.edgeStatus(ctx, host)
	return err == nil && st.WAF && st.Running
}

// ensureEdge reconnects the containers sites point at to berth-edge. A container a site
// was connected to with `docker network connect` loses that network whenever compose
// recreates it (update, release, runtime switch), so this runs after such changes and
// every 30 seconds.
func (a *App) ensureEdge(ctx context.Context, host string) {
	sites, err := a.sites(host)
	if err != nil {
		return
	}
	need := map[string]bool{}
	for _, s := range sites {
		if s.Enabled && s.Target.Type == "container" {
			need[s.Target.Container] = true
		}
	}
	if len(need) == 0 {
		return
	}
	cs, err := a.containers(ctx, host)
	if err != nil {
		return
	}
	for _, c := range cs {
		if need[c.Name] && c.State == "running" && !contains(c.Networks, caddy.Network) {
			if err := a.call(ctx, host, "POST", "/v1/edge/connect", map[string]string{"container": c.Name}, nil); err == nil {
				log.Printf("panel: reconnected %s to %s on %s", c.Name, caddy.Network, host)
			}
		}
	}
}

func (a *App) edgeLoop(ctx context.Context) {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			for _, h := range a.connectedHosts() {
				c, cancel := context.WithTimeout(ctx, 20*time.Second)
				a.ensureEdge(c, h)
				cancel()
			}
		}
	}
}

// afterChange: container recreations may have dropped berth-edge.
func (a *App) afterChange(c *change.Change) {
	a.alertAfterChange(c)
	a.recordOwner(c)
	if c.Kind == "site.delete" && c.Status == "done" { // a deleted site leaves the tunnel too
		var p struct {
			ID int64 `json:"id"`
		}
		json.Unmarshal(c.Params, &p)
		go func(host string, id int64) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			a.unrouteSite(ctx, host, id)
			a.unpublishSite(ctx, host, id, "Berth · 删除站点")
		}(c.Host, p.ID)
	}
	for _, p := range []string{"compose.", "container.", "release.", "app.", "pipeline.run", "site."} {
		if strings.HasPrefix(c.Kind, p) {
			go func(host string) {
				time.Sleep(2 * time.Second)
				ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
				defer cancel()
				a.ensureEdge(ctx, host)
			}(c.Host)
			return
		}
	}
}

// pruneEdgeImages removes edge builds this panel no longer uses (older versions, dev builds);
// both current variants stay, so switching the WAF on or off again needs no rebuild.
func (a *App) pruneEdgeImages(env change.Env) {
	var imgs []docker.Image
	if a.call(env.Ctx, env.Host, "GET", "/v1/docker/images", nil, &imgs) != nil {
		return
	}
	for _, im := range imgs {
		for _, tag := range im.Tags {
			if !strings.HasPrefix(tag, "berth/caddy:") || tag == caddy.Image || tag == caddy.ImageWAF || len(im.UsedBy) > 0 {
				continue
			}
			if a.call(env.Ctx, env.Host, "POST", "/v1/docker/images/remove", map[string]string{"ref": tag}, nil) == nil {
				env.Log("removed the old edge image %s", tag)
			}
		}
	}
}
