package panel

// Pipelines (方案 §5.10): build and release. Definitions live here (or in the repository
// as berth.pipeline.yaml); runs execute on the runner host's agent and are heavy tasks,
// so they wait for memory like everything else.

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/wangsirco/berth/internal/change"
	"github.com/wangsirco/berth/internal/errs"
	"github.com/wangsirco/berth/internal/pipeline"
	"github.com/wangsirco/berth/internal/sign"
)

type pipelineRow struct {
	ID        int64           `json:"id"`
	Name      string          `json:"name"`
	Host      string          `json:"host"`
	Source    pipeline.Source `json:"source"`
	Spec      string          `json:"spec"` // YAML; empty = berth.pipeline.yaml in the repository
	Cron      string          `json:"cron,omitempty"`
	HasToken  bool            `json:"has_token"`
	CreatedBy string          `json:"created_by,omitempty"`
	CreatedAt int64           `json:"created_at"`
	UpdatedAt int64           `json:"updated_at"`
	LastRun   *pipelineRun    `json:"last_run,omitempty"`

	tokenEnc  []byte
	secretEnc []byte
}

type pipelineRun struct {
	ID         int64                 `json:"id"`
	PipelineID int64                 `json:"pipeline_id"`
	Number     int64                 `json:"number"`
	Status     string                `json:"status"` // running | done | failed | cancelled
	Trigger    string                `json:"trigger"`
	Actor      string                `json:"actor"`
	Ref        string                `json:"ref,omitempty"`
	Commit     string                `json:"commit,omitempty"`
	Version    string                `json:"version"`
	Image      string                `json:"image,omitempty"`
	Digest     string                `json:"digest,omitempty"`
	Steps      []pipeline.StepResult `json:"steps"`
	Error      string                `json:"error,omitempty"`
	ChangeID   string                `json:"change_id,omitempty"`
	StartedAt  int64                 `json:"started_at"`
	FinishedAt int64                 `json:"finished_at,omitempty"`
}

const pipelineCols = `id, name, host, source, token_enc, spec, cron, hook_secret_enc, created_by, created_at, updated_at`

func scanPipeline(row interface{ Scan(...any) error }) (pipelineRow, error) {
	var p pipelineRow
	var src string
	err := row.Scan(&p.ID, &p.Name, &p.Host, &src, &p.tokenEnc, &p.Spec, &p.Cron, &p.secretEnc, &p.CreatedBy, &p.CreatedAt, &p.UpdatedAt)
	json.Unmarshal([]byte(src), &p.Source)
	p.HasToken = len(p.tokenEnc) > 0
	return p, err
}

func (a *App) pipelines() ([]pipelineRow, error) {
	rows, err := a.db.Query(`SELECT ` + pipelineCols + ` FROM pipelines ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []pipelineRow{}
	for rows.Next() {
		p, err := scanPipeline(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// pipelineByRef finds a pipeline by id or name.
func (a *App) pipelineByRef(ref string) (pipelineRow, error) {
	q := `SELECT ` + pipelineCols + ` FROM pipelines WHERE name=?`
	if _, err := strconv.ParseInt(ref, 10, 64); err == nil {
		q = `SELECT ` + pipelineCols + ` FROM pipelines WHERE id=?`
	}
	p, err := scanPipeline(a.db.QueryRow(q, ref))
	if errors.Is(err, sql.ErrNoRows) {
		return p, errs.New(404, "pipeline.not_found", "name", ref)
	}
	return p, err
}

const runCols = `id, pipeline_id, number, status, trigger, actor, ref, commit_sha, version, image, digest, steps, error, change_id, started_at, finished_at`

func scanRun(row interface{ Scan(...any) error }) (pipelineRun, error) {
	var r pipelineRun
	var steps string
	err := row.Scan(&r.ID, &r.PipelineID, &r.Number, &r.Status, &r.Trigger, &r.Actor, &r.Ref, &r.Commit, &r.Version, &r.Image, &r.Digest, &steps, &r.Error, &r.ChangeID, &r.StartedAt, &r.FinishedAt)
	json.Unmarshal([]byte(steps), &r.Steps)
	if r.Steps == nil {
		r.Steps = []pipeline.StepResult{}
	}
	return r, err
}

func (a *App) runs(pipelineID int64, limit int) ([]pipelineRun, error) {
	rows, err := a.db.Query(`SELECT `+runCols+` FROM pipeline_runs WHERE pipeline_id=? ORDER BY number DESC LIMIT ?`, pipelineID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []pipelineRun{}
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ---- kinds ----

type pipelineParams struct {
	ID     int64           `json:"id,omitempty"`
	Name   string          `json:"name"`
	Host   string          `json:"host,omitempty"` // runner host (default: the change's host)
	Source pipeline.Source `json:"source"`
	Token  string          `json:"token,omitempty"` // git access token
	Spec   string          `json:"spec,omitempty"`
	Cron   string          `json:"cron,omitempty"`
	// undo of delete / update: credentials as they were, sealed
	TokenSealed  string `json:"token_sealed,omitempty"`
	SecretSealed string `json:"secret_sealed,omitempty"`
}

func (a *App) checkPipeline(p *pipelineParams, envHost string) error {
	if p.Host == "" {
		p.Host = envHost
	}
	s := pipeline.Spec{Name: p.Name, Steps: []pipeline.Step{{Name: "x", Image: "x", Run: "x"}}}
	if err := s.Validate(); err != nil {
		return pipeline.SpecErr(err)
	}
	if p.Spec != "" {
		spec, err := pipeline.Parse([]byte(p.Spec))
		if err != nil {
			return pipeline.SpecErr(err)
		}
		if spec.Name != p.Name {
			return errs.New(400, "pipeline.name_mismatch", "name", spec.Name)
		}
	}
	switch p.Source.Type {
	case "git":
		if !strings.HasPrefix(p.Source.URL, "https://") && !strings.HasPrefix(p.Source.URL, "http://") && !strings.HasPrefix(p.Source.URL, "git@") {
			return errs.New(400, "pipeline.bad_source")
		}
	case "upload":
	default:
		return errs.New(400, "pipeline.bad_source")
	}
	if p.Spec == "" && p.Source.Type == "upload" {
		// the definition must then come with each upload; fine
	}
	if p.Cron != "" {
		if _, err := pipeline.ParseCron(p.Cron); err != nil {
			return pipeline.SpecErr(err)
		}
	}
	if _, err := a.agent(p.Host); err != nil {
		return err
	}
	return nil
}

func (a *App) sealString(s string) ([]byte, error) {
	if s == "" {
		return nil, nil
	}
	return a.seal([]byte(s))
}

func b64(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	return base64.StdEncoding.EncodeToString(b)
}

func unb64(s string) []byte { b, _ := base64.StdEncoding.DecodeString(s); return b }

func (a *App) savePipeline(p pipelineParams, by string, keepToken []byte, secret []byte) (int64, error) {
	src, _ := json.Marshal(p.Source)
	tok := keepToken
	if p.Token != "" {
		var err error
		if tok, err = a.sealString(p.Token); err != nil {
			return 0, err
		}
	}
	if p.TokenSealed != "" {
		tok = unb64(p.TokenSealed)
	}
	now := time.Now().Unix()
	if p.ID != 0 {
		res, err := a.db.Exec(`UPDATE pipelines SET name=?, host=?, source=?, token_enc=?, spec=?, cron=?, updated_at=? WHERE id=?`,
			p.Name, p.Host, string(src), tok, p.Spec, p.Cron, now, p.ID)
		if err != nil {
			return 0, err
		}
		if n, _ := res.RowsAffected(); n == 1 {
			return p.ID, nil
		}
	}
	if len(secret) == 0 {
		secret = []byte(sign.RandomHex(20))
		var err error
		if secret, err = a.seal(secret); err != nil {
			return 0, err
		}
	}
	var res sql.Result
	var err error
	if p.ID != 0 { // restoring a deleted pipeline keeps its id (and webhook URL)
		res, err = a.db.Exec(`INSERT INTO pipelines(id, name, host, source, token_enc, spec, cron, hook_secret_enc, created_by, created_at, updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
			p.ID, p.Name, p.Host, string(src), tok, p.Spec, p.Cron, secret, by, now, now)
	} else {
		res, err = a.db.Exec(`INSERT INTO pipelines(name, host, source, token_enc, spec, cron, hook_secret_enc, created_by, created_at, updated_at) VALUES(?,?,?,?,?,?,?,?,?,?)`,
			p.Name, p.Host, string(src), tok, p.Spec, p.Cron, secret, by, now, now)
	}
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return 0, errs.New(409, "pipeline.exists", "name", p.Name)
		}
		return 0, err
	}
	id, _ := res.LastInsertId()
	return id, nil
}

func pipelineObj(id int64, name string) change.Object {
	return change.Object{Type: "pipeline", ID: strconv.FormatInt(id, 10), Name: name}
}

// the app the memory estimate reads pipelines from (one panel per process)
var pipelineApp *App

func pipelineMem(raw json.RawMessage) int {
	if pipelineApp == nil {
		return 0
	}
	var p struct {
		ID   any    `json:"id"`
		Name string `json:"name"`
	}
	json.Unmarshal(raw, &p)
	ref := p.Name
	if p.ID != nil {
		ref = fmt.Sprint(p.ID)
	}
	pr, err := pipelineApp.pipelineByRef(ref)
	if err != nil || pr.Spec == "" {
		return 2048
	}
	spec, err := pipeline.Parse([]byte(pr.Spec))
	if err != nil {
		return 2048
	}
	// The gate waits for this much free memory before starting. The container's own limit
	// (with swap) is the real cap, so a definition asking for more than a small host ever
	// has free still runs once the host is otherwise quiet.
	return min(int(spec.Runner.MemoryBytes()>>20), pipelineGateMaxMB)
}

const pipelineGateMaxMB = 3072

type runParams struct {
	ID      int64  `json:"id,omitempty"`
	Name    string `json:"name,omitempty"`
	Ref     string `json:"ref,omitempty"`    // overrides the source branch/tag
	Source  string `json:"source,omitempty"` // "upload": use the last berth push
	Trigger string `json:"trigger,omitempty"`
}

func (p runParams) ref() string {
	if p.ID != 0 {
		return strconv.FormatInt(p.ID, 10)
	}
	return p.Name
}

func init() {
	routeHooks = append(routeHooks, func(a *App, mux *http.ServeMux) {
		pipelineApp = a
		mux.HandleFunc("GET /api/v1/pipelines", a.authed("pipeline.run", a.handlePipelines))
		mux.HandleFunc("GET /api/v1/pipelines/{id}", a.authed("pipeline.run", a.handlePipeline))
		mux.HandleFunc("GET /api/v1/pipelines/{id}/webhook", a.authed("pipeline.manage", a.handlePipelineWebhookInfo))
		mux.HandleFunc("POST /api/v1/pipelines/{id}/push", a.authed("pipeline.run", a.handlePipelinePush))
		mux.HandleFunc("POST /hooks/pipelines/{id}", a.handlePipelineHook)
	})

	props := map[string]change.Prop{
		"id": {Type: "integer"}, "name": {Type: "string"}, "host": {Type: "string", Desc: "runner host"},
		"source": {Type: "object", Desc: "{type: git|upload, url, ref}"}, "token": {Type: "string", Desc: "git access token (private repositories)"},
		"spec": {Type: "string", Desc: "berth.pipeline.yaml content; empty = read it from the repository"}, "cron": {Type: "string"},
	}
	change.Register(change.Kind{
		Name: "pipeline.create", Perm: "pipeline.manage", Scope: "pipeline:write", Title: "kind.pipeline.create",
		Params: change.ParamSchema{Props: props, Required: []string{"name", "source"}}, Redact: []string{"token", "token_sealed", "secret_sealed"},
		CLI: "berth pipeline create {{.name}}",
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			var p pipelineParams
			json.Unmarshal(raw, &p)
			if err := svc(env).checkPipeline(&p, env.Host); err != nil {
				return change.Plan{}, err
			}
			if _, err := svc(env).pipelineByRef(p.Name); err == nil {
				return change.Plan{}, errs.New(409, "pipeline.exists", "name", p.Name)
			}
			plan := change.Plan{Summary: "plan.pipeline.create", SummaryP: map[string]any{"name": p.Name, "host": p.Host}, Risk: change.RiskLow,
				Affected: []change.Object{pipelineObj(p.ID, p.Name)}, Undoable: true}
			if p.Spec != "" {
				plan.Files = []change.FileDiff{{Path: "berth.pipeline.yaml", After: p.Spec}}
			}
			return plan, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			a := svc(env)
			var p pipelineParams
			json.Unmarshal(raw, &p)
			if err := a.checkPipeline(&p, env.Host); err != nil {
				return nil, nil, err
			}
			id, err := a.savePipeline(p, "", nil, unb64(p.SecretSealed))
			if err != nil {
				return nil, nil, err
			}
			u, _ := json.Marshal(map[string]int64{"id": id})
			return map[string]any{"id": id, "webhook": "/hooks/pipelines/" + strconv.FormatInt(id, 10)}, &change.UndoRecipe{Kind: "pipeline.delete", Params: u}, nil
		},
	})
	change.Register(change.Kind{
		Name: "pipeline.update", Perm: "pipeline.manage", Scope: "pipeline:write", Title: "kind.pipeline.update",
		Params: change.ParamSchema{Props: props, Required: []string{"id", "name", "source"}}, Redact: []string{"token", "token_sealed", "secret_sealed"},
		CLI: "berth pipeline edit {{.id}}",
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			a := svc(env)
			var p pipelineParams
			json.Unmarshal(raw, &p)
			old, err := a.pipelineByRef(strconv.FormatInt(p.ID, 10))
			if err != nil {
				return change.Plan{}, err
			}
			if err := a.checkPipeline(&p, old.Host); err != nil {
				return change.Plan{}, err
			}
			return change.Plan{Summary: "plan.pipeline.update", SummaryP: map[string]any{"name": p.Name}, Risk: change.RiskLow,
				Files:    []change.FileDiff{{Path: "berth.pipeline.yaml", Before: old.Spec, After: p.Spec}},
				Affected: []change.Object{pipelineObj(p.ID, p.Name)}, Undoable: true}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			a := svc(env)
			var p pipelineParams
			json.Unmarshal(raw, &p)
			old, err := a.pipelineByRef(strconv.FormatInt(p.ID, 10))
			if err != nil {
				return nil, nil, err
			}
			if err := a.checkPipeline(&p, old.Host); err != nil {
				return nil, nil, err
			}
			if _, err := a.savePipeline(p, "", old.tokenEnc, nil); err != nil {
				return nil, nil, err
			}
			u, _ := json.Marshal(pipelineParams{ID: old.ID, Name: old.Name, Host: old.Host, Source: old.Source, Spec: old.Spec, Cron: old.Cron, TokenSealed: b64(old.tokenEnc)})
			return map[string]any{"id": p.ID}, &change.UndoRecipe{Kind: "pipeline.update", Params: u}, nil
		},
	})
	change.Register(change.Kind{
		Name: "pipeline.delete", Perm: "pipeline.manage", Scope: "pipeline:write", Title: "kind.pipeline.delete",
		Params: change.ParamSchema{Props: map[string]change.Prop{"id": {Type: "integer"}}, Required: []string{"id"}},
		CLI:    "berth pipeline rm {{.id}}",
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			var p struct{ ID int64 }
			json.Unmarshal(raw, &p)
			old, err := svc(env).pipelineByRef(strconv.FormatInt(p.ID, 10))
			if err != nil {
				return change.Plan{}, err
			}
			return change.Plan{Summary: "plan.pipeline.delete", SummaryP: map[string]any{"name": old.Name}, Risk: change.RiskMedium,
				Affected: []change.Object{pipelineObj(old.ID, old.Name)}, Undoable: true}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			a := svc(env)
			var p struct{ ID int64 }
			json.Unmarshal(raw, &p)
			old, err := a.pipelineByRef(strconv.FormatInt(p.ID, 10))
			if err != nil {
				return nil, nil, err
			}
			if _, err := a.db.Exec(`DELETE FROM pipelines WHERE id=?`, old.ID); err != nil {
				return nil, nil, err
			}
			u, _ := json.Marshal(pipelineParams{ID: old.ID, Name: old.Name, Host: old.Host, Source: old.Source, Spec: old.Spec, Cron: old.Cron,
				TokenSealed: b64(old.tokenEnc), SecretSealed: b64(old.secretEnc)})
			return nil, &change.UndoRecipe{Kind: "pipeline.create", Params: u}, nil
		},
	})
	change.Register(change.Kind{
		Name: "pipeline.run", Perm: "pipeline.run", Scope: "pipeline:write", Title: "kind.pipeline.run",
		Params: change.ParamSchema{Props: map[string]change.Prop{"id": {Type: "integer"}, "name": {Type: "string"},
			"ref": {Type: "string", Desc: "branch, tag or commit (default: the pipeline's)"}, "source": {Type: "string", Enum: []string{"", "upload"}},
			"trigger": {Type: "string", Desc: "set by the panel: manual, push, webhook, schedule"}}},
		CLI: "berth pipeline run {{.name}}{{.id}}", Heavy: true, MemFor: pipelineMem,
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			var p runParams
			json.Unmarshal(raw, &p)
			pr, err := svc(env).pipelineByRef(p.ref())
			if err != nil {
				return change.Plan{}, err
			}
			plan := change.Plan{Summary: "plan.pipeline.run", SummaryP: map[string]any{"name": pr.Name, "host": pr.Host}, Risk: change.RiskLow,
				Affected: []change.Object{pipelineObj(pr.ID, pr.Name)}}
			if pr.Spec != "" {
				if spec, err := pipeline.Parse([]byte(pr.Spec)); err == nil {
					for _, st := range spec.Steps {
						plan.Commands = append(plan.Commands, stepLine(st))
					}
					for _, d := range spec.Deploy {
						plan.Commands = append(plan.Commands, "deploy → "+d.Host+" "+d.Compose+" ("+d.Service+")")
						plan.Risk = change.RiskMedium
					}
				}
			}
			return plan, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			var p runParams
			json.Unmarshal(raw, &p)
			run, err := svc(env).runPipeline(env, p)
			return run, nil, err
		},
	})
	change.Register(change.Kind{
		Name: "pipeline.cancel", Perm: "pipeline.run", Scope: "pipeline:write", Title: "kind.pipeline.cancel",
		Params: change.ParamSchema{Props: map[string]change.Prop{"run_id": {Type: "integer"}}, Required: []string{"run_id"}},
		CLI:    "berth pipeline cancel {{.run_id}}",
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			return change.Plan{Summary: "plan.pipeline.cancel", Risk: change.RiskLow}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			a := svc(env)
			var p struct {
				RunID int64 `json:"run_id"`
			}
			json.Unmarshal(raw, &p)
			r, err := scanRun(a.db.QueryRow(`SELECT `+runCols+` FROM pipeline_runs WHERE id=?`, p.RunID))
			if err != nil {
				return nil, nil, errs.New(404, "pipeline.run_not_found")
			}
			pr, err := a.pipelineByRef(strconv.FormatInt(r.PipelineID, 10))
			if err != nil {
				return nil, nil, err
			}
			var out map[string]bool
			err = a.call(env.Ctx, pr.Host, "POST", "/v1/pipeline/cancel", map[string]string{"run_id": runKey(pr, r.Number)}, &out)
			return out, nil, err
		},
	})
	change.Register(change.Kind{
		Name: "binfmt.install", Perm: "pipeline.manage", Scope: "pipeline:write", Title: "kind.binfmt.install",
		Params: change.ParamSchema{Props: map[string]change.Prop{}}, CLI: "docker run --privileged --rm tonistiigi/binfmt --install amd64,arm64", Heavy: true,
		Plan: func(env change.Env, _ json.RawMessage) (change.Plan, error) {
			return change.Plan{Summary: "plan.binfmt.install", Risk: change.RiskMedium,
				Commands: []string{"docker run --privileged --rm tonistiigi/binfmt --install amd64,arm64"}}, nil
		},
		Apply: func(env change.Env, _ json.RawMessage) (any, *change.UndoRecipe, error) {
			return nil, nil, agentOp(env, "/v1/binfmt/install", nil)
		},
	})
}

func stepLine(st pipeline.Step) string {
	if st.DockerBuild != nil {
		l := "docker buildx build " + st.DockerBuild.Context
		if len(st.DockerBuild.Platforms) > 0 {
			l += " --platform " + strings.Join(st.DockerBuild.Platforms, ",")
		}
		if st.DockerBuild.Push != "" {
			l += " --push " + st.DockerBuild.Push
		}
		return st.Name + ": " + l
	}
	return st.Name + ": [" + st.Image + "] " + st.Run
}

func runKey(p pipelineRow, number int64) string { return fmt.Sprintf("p%d-r%d", p.ID, number) }

// deployHook runs the deploy section after a successful build (the release module).
var deployHook func(env change.Env, p pipelineRow, run *pipelineRun, spec *pipeline.Spec) error

func (a *App) runPipeline(env change.Env, p runParams) (*pipelineRun, error) {
	pr, err := a.pipelineByRef(p.ref())
	if err != nil {
		return nil, err
	}
	var number int64
	a.db.QueryRow(`SELECT COALESCE(MAX(number),0)+1 FROM pipeline_runs WHERE pipeline_id=?`, pr.ID).Scan(&number)
	trigger := firstNonEmpty(p.Trigger, "manual")
	if p.Source == "upload" {
		trigger = "push"
	}
	run := &pipelineRun{PipelineID: pr.ID, Number: number, Status: "running", Trigger: trigger, Actor: env.ActorName,
		Ref: firstNonEmpty(p.Ref, pr.Source.Ref), Version: "r" + strconv.FormatInt(number, 10), Steps: []pipeline.StepResult{},
		ChangeID: env.ChangeID, StartedAt: time.Now().Unix()}
	res, err := a.db.Exec(`INSERT INTO pipeline_runs(pipeline_id, number, status, trigger, actor, ref, version, change_id, started_at) VALUES(?,?,?,?,?,?,?,?,?)`,
		pr.ID, number, run.Status, run.Trigger, run.Actor, run.Ref, run.Version, run.ChangeID, run.StartedAt)
	if err != nil {
		return nil, err
	}
	run.ID, _ = res.LastInsertId()
	a.hub.publish(event{Type: "pipeline.run", Data: map[string]any{"pipeline": pr.ID, "run": run.ID, "status": "running"}})

	req := pipeline.RunRequest{RunID: runKey(pr, number), Pipeline: pr.Name, Source: pr.Source, Version: run.Version}
	req.Source.Ref = run.Ref
	if p.Source == "upload" {
		req.Source = pipeline.Source{Type: "upload"}
	}
	if len(pr.tokenEnc) > 0 {
		tok, err := a.open(pr.tokenEnc)
		if err != nil {
			return a.finishRun(run, nil, err)
		}
		req.Source.Token = string(tok)
	}
	var spec *pipeline.Spec
	if pr.Spec != "" {
		if spec, err = pipeline.Parse([]byte(pr.Spec)); err != nil {
			return a.finishRun(run, nil, pipeline.SpecErr(err))
		}
		req.Spec = *spec
	} else {
		req.SpecFromRepo = true
	}
	if reg, ok := a.registry(); ok {
		req.Registry = reg.Addr
	}
	c, err := a.agent(pr.Host)
	if err != nil {
		return a.finishRun(run, nil, err)
	}
	rc, err := c.Stream(env.Ctx, "POST", "/v1/pipeline/run", req)
	if err != nil {
		return a.finishRun(run, nil, err)
	}
	var result pipeline.Result
	runErr := streamLines(rc, func(l string) {
		switch {
		case strings.HasPrefix(l, "::result "):
			json.Unmarshal([]byte(l[9:]), &result)
		case strings.HasPrefix(l, "::spec "):
			var s pipeline.Spec
			if json.Unmarshal([]byte(l[7:]), &s) == nil {
				spec = &s
				a.db.Exec(`INSERT INTO pipeline_specs(pipeline_id, spec) VALUES(?,?) ON CONFLICT(pipeline_id) DO UPDATE SET spec=excluded.spec`, pr.ID, l[7:])
			}
		default:
			env.Log("%s", l)
		}
	})
	run.Commit, run.Image, run.Digest, run.Steps = result.Commit, result.Image, result.Digest, result.Steps
	if runErr == nil && run.Image != "" {
		repo, tag, _ := strings.Cut(strings.TrimPrefix(run.Image, req.Registry+"/"), ":")
		if req.Registry != "" && strings.HasPrefix(run.Image, req.Registry+"/") {
			now := time.Now().Unix()
			for _, t := range []string{tag, "latest"} {
				a.db.Exec(`INSERT INTO registry_tags(repo, tag, digest, pushed_at, source) VALUES(?,?,?,?,?) ON CONFLICT(repo, tag) DO UPDATE SET digest=excluded.digest, pushed_at=excluded.pushed_at`,
					repo, t, run.Digest, now, "pipeline:"+pr.Name)
			}
		}
	}
	if runErr == nil && spec != nil && len(spec.Deploy) > 0 {
		if deployHook == nil {
			runErr = errs.New(500, "pipeline.no_deploy")
		} else {
			env.Log("::step %d deploy", len(spec.Steps)+1)
			start := time.Now()
			runErr = deployHook(env, pr, run, spec)
			st := pipeline.StepResult{Name: "deploy", Status: "ok", MS: time.Since(start).Milliseconds()}
			if runErr != nil {
				st.Status = "failed"
			}
			run.Steps = append(run.Steps, st)
		}
	}
	if env.Ctx.Err() != nil && runErr != nil {
		run.Status = "cancelled"
	}
	return a.finishRun(run, &result, runErr)
}

func (a *App) finishRun(run *pipelineRun, _ *pipeline.Result, err error) (*pipelineRun, error) {
	if run.Status == "running" {
		run.Status = "done"
		if err != nil {
			run.Status = "failed"
		}
	}
	if err != nil {
		run.Error = storeErr(err)
	}
	run.FinishedAt = time.Now().Unix()
	steps, _ := json.Marshal(run.Steps)
	a.db.Exec(`UPDATE pipeline_runs SET status=?, commit_sha=?, image=?, digest=?, steps=?, error=?, finished_at=? WHERE id=?`,
		run.Status, run.Commit, run.Image, run.Digest, string(steps), run.Error, run.FinishedAt, run.ID)
	a.hub.publish(event{Type: "pipeline.run", Data: map[string]any{"pipeline": run.PipelineID, "run": run.ID, "status": run.Status}})
	return run, err
}

// storeErr keeps an error as {code, params} so it can be translated when shown.
func storeErr(err error) string {
	e := errs.From(err)
	if e.Code == "internal" && e.Params["detail"] == nil {
		e = errs.New(500, "internal", "detail", err.Error())
	}
	b, _ := json.Marshal(map[string]any{"code": e.Code, "params": e.Params})
	return string(b)
}

// shownErr translates a stored error for the reader.
func (a *App) shownErr(r *http.Request, stored string) string {
	var e struct {
		Code   string         `json:"code"`
		Params map[string]any `json:"params"`
	}
	if stored == "" || json.Unmarshal([]byte(stored), &e) != nil || e.Code == "" {
		return stored
	}
	return T(a.locale(r), e.Code, e.Params)
}

// ---- triggers ----

// startRun starts pipeline.run as a system actor (webhook, schedule).
func (a *App) startRun(p pipelineRow, trigger, ref string) error {
	params, _ := json.Marshal(runParams{ID: p.ID, Ref: ref, Trigger: trigger})
	_, err := a.pipe.Run(context.Background(), change.Actor{Type: "system", ID: trigger, Name: trigger, Via: "api"}, p.Host, "pipeline.run", params)
	return err
}

func (a *App) cronLoop(ctx context.Context) {
	for {
		now := time.Now()
		next := now.Truncate(time.Minute).Add(time.Minute)
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Until(next)):
		}
		for _, h := range cronHooks {
			h(a, next)
		}
		list, err := a.pipelines()
		if err != nil {
			continue
		}
		for _, p := range list {
			if p.Cron == "" {
				continue
			}
			c, err := pipeline.ParseCron(p.Cron)
			if err == nil && c.Match(next) {
				if err := a.startRun(p, "schedule", ""); err != nil {
					log.Printf("panel: scheduled run of %s: %v", p.Name, err)
				}
			}
		}
	}
}

// verifyHook checks a GitHub / Gitea / Forgejo signature or a GitLab token.
func verifyHook(r *http.Request, body, secret []byte) bool {
	mac := hmac.New(sha256.New, secret)
	mac.Write(body)
	want := hex.EncodeToString(mac.Sum(nil))
	if s := r.Header.Get("X-Hub-Signature-256"); s != "" {
		return hmac.Equal([]byte(strings.TrimPrefix(s, "sha256=")), []byte(want))
	}
	for _, h := range []string{"X-Gitea-Signature", "X-Forgejo-Signature", "X-Gogs-Signature"} {
		if s := r.Header.Get(h); s != "" {
			return hmac.Equal([]byte(s), []byte(want))
		}
	}
	if s := r.Header.Get("X-Gitlab-Token"); s != "" {
		return subtle.ConstantTimeCompare([]byte(s), secret) == 1
	}
	return false
}

// refMatches: the pushed ref is the pipeline's branch or tag (any ref when it has none).
func refMatches(pushed, want string) bool {
	if want == "" {
		return true
	}
	return pushed == want || pushed == "refs/heads/"+want || pushed == "refs/tags/"+want
}

func (a *App) handlePipelineHook(w http.ResponseWriter, r *http.Request) {
	p, err := a.pipelineByRef(r.PathValue("id"))
	if err != nil {
		blank404(w)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 5<<20))
	if err != nil {
		blank404(w)
		return
	}
	secret, err := a.open(p.secretEnc)
	if err != nil || !verifyHook(r, body, secret) {
		http.Error(w, "bad signature", http.StatusUnauthorized)
		return
	}
	if r.Header.Get("X-GitHub-Event") == "ping" {
		writeJSON(w, 200, map[string]string{"ok": "pong"})
		return
	}
	var payload struct {
		Ref   string `json:"ref"`
		After string `json:"after"`
	}
	json.Unmarshal(body, &payload)
	if !refMatches(payload.Ref, p.Source.Ref) {
		writeJSON(w, 200, map[string]string{"ignored": payload.Ref})
		return
	}
	ref := p.Source.Ref
	if payload.After != "" && strings.Trim(payload.After, "0") != "" {
		ref = payload.After // build exactly the pushed commit
	}
	if err := a.startRun(p, "webhook", ref); err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, 202, map[string]string{"ok": "queued"})
}

// POST /api/v1/pipelines/{id}/push: `berth push` uploads the working tree (tar.gz).
func (a *App) handlePipelinePush(w http.ResponseWriter, r *http.Request) {
	p, err := a.pipelineByRef(r.PathValue("id"))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 60<<20+1))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if len(body) > 60<<20 {
		a.fail(w, r, errs.New(413, "pipeline.upload_too_large", "max", "60 MB"))
		return
	}
	c, err := a.agent(p.Host)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if err := c.DoRaw(r.Context(), "PUT", "/v1/pipeline/"+p.Name+"/upload", body); err != nil {
		a.fail(w, r, err)
		return
	}
	raw, _ := json.Marshal(runParams{ID: p.ID, Source: "upload", Trigger: "push"})
	a.runChange(w, r, p.Host, "pipeline.run", raw)
}

// ---- API ----

func (a *App) handlePipelines(w http.ResponseWriter, r *http.Request) {
	list, err := a.pipelines()
	if err != nil {
		a.fail(w, r, err)
		return
	}
	for i := range list {
		if runs, _ := a.runs(list[i].ID, 1); len(runs) == 1 {
			runs[0].Error = a.shownErr(r, runs[0].Error)
			list[i].LastRun = &runs[0]
		}
	}
	writeJSON(w, 200, map[string]any{"items": list})
}

func (a *App) handlePipeline(w http.ResponseWriter, r *http.Request) {
	p, err := a.pipelineByRef(r.PathValue("id"))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	runs, _ := a.runs(p.ID, 50)
	for i := range runs {
		runs[i].Error = a.shownErr(r, runs[i].Error)
	}
	writeJSON(w, 200, map[string]any{"pipeline": p, "runs": runs})
}

func (a *App) handlePipelineWebhookInfo(w http.ResponseWriter, r *http.Request) {
	p, err := a.pipelineByRef(r.PathValue("id"))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	secret, err := a.open(p.secretEnc)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]string{"url": panelBase(r) + "/hooks/pipelines/" + strconv.FormatInt(p.ID, 10), "secret": string(secret),
		"content_type": "application/json"})
}
