package panel

// Releases (方案 §5.10.3): each deployment of an image to a compose service. Builds
// deploy through their pipeline's deploy section; release.deploy redeploys any image
// (a rollback is a release of an earlier version).

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/wangsirco/berth/internal/change"
	"github.com/wangsirco/berth/internal/errs"
	"github.com/wangsirco/berth/internal/pipeline"
)

type deployRecord struct {
	ID         int64  `json:"id"`
	PipelineID int64  `json:"pipeline_id"`
	RunID      int64  `json:"run_id,omitempty"`
	Version    string `json:"version"`
	Host       string `json:"host"`
	Compose    string `json:"compose"`
	Service    string `json:"service"`
	Image      string `json:"image"`
	OldImage   string `json:"old_image"`
	Status     string `json:"status"` // done | failed | rolled_back
	Actor      string `json:"actor"`
	Error      string `json:"error,omitempty"`
	ChangeID   string `json:"change_id,omitempty"`
	StartedAt  int64  `json:"started_at"`
	MS         int64  `json:"ms"`
}

const releaseCols = `id, pipeline_id, run_id, version, host, compose, service, image, old_image, status, actor, error, change_id, started_at, ms`

func scanRelease(row interface{ Scan(...any) error }) (deployRecord, error) {
	var r deployRecord
	err := row.Scan(&r.ID, &r.PipelineID, &r.RunID, &r.Version, &r.Host, &r.Compose, &r.Service, &r.Image, &r.OldImage, &r.Status, &r.Actor, &r.Error, &r.ChangeID, &r.StartedAt, &r.MS)
	return r, err
}

func (a *App) releases(pipelineID int64, limit int) ([]deployRecord, error) {
	rows, err := a.db.Query(`SELECT `+releaseCols+` FROM releases WHERE pipeline_id=? ORDER BY id DESC LIMIT ?`, pipelineID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []deployRecord{}
	for rows.Next() {
		r, err := scanRelease(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// deploy runs one release on d.Host and records it.
func (a *App) deploy(env change.Env, pipelineID, runID int64, version string, d pipeline.Deploy, image string) (*deployRecord, error) {
	rel := &deployRecord{PipelineID: pipelineID, RunID: runID, Version: version, Host: d.Host, Compose: d.Compose, Service: d.Service,
		Image: image, Status: "running", Actor: env.ActorName, ChangeID: env.ChangeID, StartedAt: time.Now().Unix()}
	res, err := a.db.Exec(`INSERT INTO releases(pipeline_id, run_id, version, host, compose, service, image, old_image, status, actor, error, change_id, started_at, ms)
		VALUES(?,?,?,?,?,?,?,'',?,?,'',?,?,0)`, pipelineID, runID, version, d.Host, d.Compose, d.Service, image, rel.Status, rel.Actor, rel.ChangeID, rel.StartedAt)
	if err != nil {
		return nil, err
	}
	rel.ID, _ = res.LastInsertId()
	finish := func(err error) (*deployRecord, error) {
		switch {
		case err == nil:
			rel.Status = "done"
		case strings.Contains(errs.From(err).Code, "rolled_back"):
			rel.Status = "rolled_back"
		default:
			rel.Status = "failed"
		}
		if err != nil {
			rel.Error = storeErr(err)
		}
		a.db.Exec(`UPDATE releases SET old_image=?, status=?, error=?, ms=? WHERE id=?`, rel.OldImage, rel.Status, rel.Error, rel.MS, rel.ID)
		a.hub.publish(event{Type: "release", Data: map[string]any{"pipeline": pipelineID, "release": rel.ID, "status": rel.Status}})
		return rel, err
	}
	c, err := a.agent(d.Host)
	if err != nil {
		return finish(err)
	}
	if err := a.trustRegistry(env.Ctx, d.Host); err != nil {
		env.Log("registry trust on %s: %v", d.Host, err)
	}
	req := pipeline.DeployRequest{Compose: d.Compose, Service: d.Service, Image: image, Healthcheck: d.Healthcheck,
		Rollback: d.Rollback, PreDeploy: d.PreDeploy, PostDeploy: d.PostDeploy}
	rc, err := c.Stream(env.Ctx, "POST", "/v1/deploy/compose", req)
	if err != nil {
		return finish(err)
	}
	var out pipeline.DeployResult
	derr := streamLines(rc, func(l string) {
		if strings.HasPrefix(l, "::result ") {
			json.Unmarshal([]byte(l[9:]), &out)
			return
		}
		if strings.HasPrefix(l, "::") { // markers stay recognisable
			env.Log("%s", l)
			return
		}
		env.Log("[%s] %s", d.Host, l)
	})
	rel.OldImage, rel.MS = out.OldImage, out.MS
	return finish(derr)
}

func init() {
	deployHook = func(env change.Env, p pipelineRow, run *pipelineRun, spec *pipeline.Spec) error {
		if run.Image == "" {
			return errs.New(400, "pipeline.deploy_needs_image")
		}
		for _, d := range spec.Deploy {
			if _, err := svc(env).deploy(env, p.ID, run.ID, run.Version, d, run.Image); err != nil {
				return err
			}
		}
		return nil
	}
	// images of the newest releases of every service stay in the registry
	protectedDigestsHook = func(a *App) map[string]bool {
		out := map[string]bool{}
		rows, err := a.db.Query(`SELECT DISTINCT r.digest FROM releases rel JOIN pipeline_runs r ON r.id = rel.run_id
			WHERE rel.status='done' AND rel.id IN (SELECT id FROM releases ORDER BY id DESC LIMIT 200)`)
		if err != nil {
			return out
		}
		defer rows.Close()
		for rows.Next() {
			var d string
			rows.Scan(&d)
			out[d] = true
		}
		return out
	}

	routeHooks = append(routeHooks, func(a *App, mux *http.ServeMux) {
		mux.HandleFunc("GET /api/v1/pipelines/{id}/releases", a.authed("pipeline.run", func(w http.ResponseWriter, r *http.Request) {
			p, err := a.pipelineByRef(r.PathValue("id"))
			if err != nil {
				a.fail(w, r, err)
				return
			}
			list, err := a.releases(p.ID, 100)
			if err != nil {
				a.fail(w, r, err)
				return
			}
			for i := range list {
				list[i].Error = a.shownErr(r, list[i].Error)
			}
			writeJSON(w, 200, map[string]any{"items": list})
		}))
	})

	props := map[string]change.Prop{
		"pipeline": {Type: "string", Desc: "pipeline id or name"}, "deploy": {Type: "integer", Desc: "index in the pipeline's deploy list (default 0)"},
		"image": {Type: "string", Desc: "image to release; or"}, "release_id": {Type: "integer", Desc: "release whose image to deploy again (rollback)"},
	}
	change.Register(change.Kind{
		Name: "release.deploy", Perm: "pipeline.run", Scope: "pipeline:write", Title: "kind.release.deploy",
		Params: change.ParamSchema{Props: props, Required: []string{"pipeline"}}, CLI: "berth release deploy {{.pipeline}} {{.image}}", Heavy: true, MemMB: 64,
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			a := svc(env)
			p, d, image, version, err := a.releaseTarget(raw)
			if err != nil {
				return change.Plan{}, err
			}
			var cur struct {
				Image string `json:"image"`
			}
			if err := a.call(env.Ctx, d.Host, "GET", "/v1/deploy/current?compose="+url.QueryEscape(d.Compose)+"&service="+url.QueryEscape(d.Service), nil, &cur); err != nil {
				return change.Plan{}, err
			}
			return change.Plan{Summary: "plan.release.deploy", SummaryP: map[string]any{"pipeline": p.Name, "version": version, "host": d.Host, "service": d.Service,
				"before": cur.Image, "after": image}, Risk: change.RiskHigh,
				Files:    []change.FileDiff{{Path: d.Compose, Before: "image: " + cur.Image, After: "image: " + image}},
				Commands: []string{"docker pull " + image, "docker compose -f " + d.Compose + " up -d --no-deps " + d.Service},
				Affected: []change.Object{{Type: "service", ID: d.Host + "/" + d.Service, Name: d.Service, Host: d.Host}, pipelineObj(p.ID, p.Name)},
				Undoable: true}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			a := svc(env)
			p, d, image, version, err := a.releaseTarget(raw)
			if err != nil {
				return nil, nil, err
			}
			rel, err := a.deploy(env, p.ID, 0, version, d, image)
			if err != nil {
				return rel, nil, err
			}
			var in map[string]any
			json.Unmarshal(raw, &in)
			undo, _ := json.Marshal(map[string]any{"pipeline": strconv.FormatInt(p.ID, 10), "deploy": in["deploy"], "image": rel.OldImage})
			return rel, &change.UndoRecipe{Kind: "release.deploy", Params: undo}, nil
		},
	})
}

// releaseTarget resolves release.deploy params into the pipeline, deploy target and image.
func (a *App) releaseTarget(raw json.RawMessage) (pipelineRow, pipeline.Deploy, string, string, error) {
	var in struct {
		Pipeline  string `json:"pipeline"`
		Deploy    int    `json:"deploy"`
		Image     string `json:"image"`
		ReleaseID int64  `json:"release_id"`
	}
	json.Unmarshal(raw, &in)
	p, err := a.pipelineByRef(in.Pipeline)
	if err != nil {
		return p, pipeline.Deploy{}, "", "", err
	}
	spec, err := a.pipelineSpec(p)
	if err != nil {
		return p, pipeline.Deploy{}, "", "", err
	}
	if in.Deploy < 0 || in.Deploy >= len(spec.Deploy) {
		return p, pipeline.Deploy{}, "", "", errs.New(400, "release.no_target")
	}
	d := spec.Deploy[in.Deploy]
	image, version := in.Image, ""
	if in.ReleaseID != 0 {
		r, err := scanRelease(a.db.QueryRow(`SELECT `+releaseCols+` FROM releases WHERE id=? AND pipeline_id=?`, in.ReleaseID, p.ID))
		if err != nil {
			return p, d, "", "", errs.New(404, "release.not_found")
		}
		image, version = r.Image, r.Version
	}
	if image == "" {
		return p, d, "", "", errs.New(400, "release.no_image")
	}
	if version == "" {
		if i := strings.LastIndex(image, ":"); i > 0 && !strings.Contains(image[i:], "/") {
			version = image[i+1:]
		}
	}
	return p, d, image, version, nil
}

// pipelineSpec: the stored definition, or the one the latest run read from the repository.
func (a *App) pipelineSpec(p pipelineRow) (*pipeline.Spec, error) {
	if p.Spec != "" {
		s, err := pipeline.Parse([]byte(p.Spec))
		if err != nil {
			return nil, pipeline.SpecErr(err)
		}
		return s, nil
	}
	var raw string
	if a.db.QueryRow(`SELECT spec FROM pipeline_specs WHERE pipeline_id=?`, p.ID).Scan(&raw) == nil {
		var s pipeline.Spec
		if json.Unmarshal([]byte(raw), &s) == nil {
			return &s, nil
		}
	}
	return nil, errs.New(409, "release.spec_unknown")
}
