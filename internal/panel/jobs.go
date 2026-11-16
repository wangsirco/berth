package panel

// Scheduled jobs (方案 §5.14 计划任务). The panel keeps and times them: a job runs a script
// on its host (as a chosen user, in a scope of its own, with a time limit) or a Berth
// action as the person who made the job — through the same change pipeline, so it is
// audited and limited to what that person may do. Every run is recorded.

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/wangsirco/berth/internal/change"
	"github.com/wangsirco/berth/internal/errs"
	"github.com/wangsirco/berth/internal/pipeline"
)

type jobAction struct {
	Type    string          `json:"type"` // script | change
	Script  string          `json:"script,omitempty"`
	User    string          `json:"user,omitempty"`    // runs as (default root)
	Timeout int             `json:"timeout,omitempty"` // seconds (default 3600)
	Kind    string          `json:"kind,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type jobSpec struct {
	Name   string    `json:"name"`
	Cron   string    `json:"cron"`
	Action jobAction `json:"action"`
}

type cronJob struct {
	ID        int64     `json:"id"`
	Host      string    `json:"host"`
	Name      string    `json:"name"`
	Cron      string    `json:"cron"`
	Action    jobAction `json:"action"`
	Enabled   bool      `json:"enabled"`
	CreatedBy string    `json:"created_by"`
	byID      int64

	Next int64       `json:"next,omitempty"`
	Last *jobRunView `json:"last,omitempty"`
}

type jobRunView struct {
	ID       int64  `json:"id"`
	JobID    int64  `json:"job_id"`
	Started  int64  `json:"started_at"`
	MS       int64  `json:"ms"`
	Status   string `json:"status"` // running | ok | failed | skipped | interrupted | pending
	Code     int    `json:"code"`
	Trigger  string `json:"trigger"`
	ChangeID string `json:"change_id,omitempty"`
	Output   string `json:"output,omitempty"`
}

var jobUserRe = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)

func (j jobSpec) validate() error {
	if strings.TrimSpace(j.Name) == "" || len(j.Name) > 80 {
		return errs.New(400, "job.bad_name")
	}
	if _, err := pipeline.ParseCron(j.Cron); err != nil {
		return errs.New(400, "job.bad_cron", "value", j.Cron)
	}
	switch j.Action.Type {
	case "script":
		if strings.TrimSpace(j.Action.Script) == "" || len(j.Action.Script) > 64<<10 {
			return errs.New(400, "job.bad_script")
		}
		if j.Action.User != "" && !jobUserRe.MatchString(j.Action.User) {
			return errs.New(400, "job.bad_user")
		}
		if j.Action.Timeout < 0 || j.Action.Timeout > 86400 {
			return errs.New(400, "job.bad_timeout")
		}
	case "change":
		k, ok := change.Get(j.Action.Kind)
		if !ok || strings.HasPrefix(k.Name, "cron.") {
			return errs.New(400, "job.bad_kind", "kind", j.Action.Kind)
		}
		if len(k.Redact) > 0 {
			return errs.New(400, "job.secret_kind", "kind", j.Action.Kind) // stored params would hold a secret
		}
		if len(j.Action.Params) > 0 && !json.Valid(j.Action.Params) {
			return errs.New(400, "job.bad_params")
		}
	default:
		return errs.New(400, "job.bad_action")
	}
	return nil
}

func scanJob(id int64, host, spec string, enabled bool, by string, byID int64) cronJob {
	var s jobSpec
	json.Unmarshal([]byte(spec), &s)
	return cronJob{ID: id, Host: host, Name: s.Name, Cron: s.Cron, Action: s.Action, Enabled: enabled, CreatedBy: by, byID: byID}
}

func (a *App) jobs(host string) []cronJob {
	q := `SELECT id, host, spec, enabled, created_by, created_by_id FROM cron_jobs`
	args := []any{}
	if host != "" {
		q += ` WHERE host=?`
		args = append(args, host)
	}
	rows, err := a.db.Query(q+` ORDER BY id`, args...)
	out := []cronJob{}
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var id, byID int64
		var h, spec, by string
		var en bool
		rows.Scan(&id, &h, &spec, &en, &by, &byID)
		out = append(out, scanJob(id, h, spec, en, by, byID))
	}
	return out
}

func (a *App) job(id int64) (cronJob, error) {
	var h, spec, by string
	var en bool
	var byID int64
	if err := a.db.QueryRow(`SELECT host, spec, enabled, created_by, created_by_id FROM cron_jobs WHERE id=?`, id).Scan(&h, &spec, &en, &by, &byID); err != nil {
		return cronJob{}, errs.New(404, "job.not_found")
	}
	return scanJob(id, h, spec, en, by, byID), nil
}

func (a *App) lastRun(jobID int64) *jobRunView {
	var r jobRunView
	if a.db.QueryRow(`SELECT id, started_at, ms, status, code, trigger, change_id FROM cron_runs WHERE job_id=? ORDER BY id DESC LIMIT 1`, jobID).
		Scan(&r.ID, &r.Started, &r.MS, &r.Status, &r.Code, &r.Trigger, &r.ChangeID) != nil {
		return nil
	}
	r.JobID = jobID
	return &r
}

// ---- running ----

var jobBusy sync.Map // job id → true while a run is going

func (a *App) runDueJobs(at time.Time) {
	for _, j := range a.jobs("") {
		if !j.Enabled {
			continue
		}
		if c, err := pipeline.ParseCron(j.Cron); err == nil && c.Match(at) {
			a.startJob(j, "schedule")
		}
	}
}

// startJob records a run and does it in the background; it returns the run id.
func (a *App) startJob(j cronJob, trigger string) int64 {
	now := time.Now().Unix()
	if _, busy := jobBusy.LoadOrStore(j.ID, true); busy {
		res, _ := a.db.Exec(`INSERT INTO cron_runs(job_id, started_at, status, trigger, output) VALUES(?,?, 'skipped', ?, 'the previous run is still going')`, j.ID, now, trigger)
		id, _ := res.LastInsertId()
		return id
	}
	res, err := a.db.Exec(`INSERT INTO cron_runs(job_id, started_at, status, trigger) VALUES(?,?, 'running', ?)`, j.ID, now, trigger)
	if err != nil {
		jobBusy.Delete(j.ID)
		return 0
	}
	rid, _ := res.LastInsertId()
	a.hub.publish(event{Type: "cron.run", Data: map[string]any{"host": j.Host, "job_id": j.ID, "id": rid, "status": "running"}})
	go func() {
		defer jobBusy.Delete(j.ID)
		start := time.Now()
		status, code, out, cid := a.execJob(j)
		a.db.Exec(`UPDATE cron_runs SET status=?, code=?, ms=?, output=?, change_id=? WHERE id=?`, status, code, time.Since(start).Milliseconds(), out, cid, rid)
		// keep the last 50 runs of each job
		a.db.Exec(`DELETE FROM cron_runs WHERE job_id=? AND id NOT IN (SELECT id FROM cron_runs WHERE job_id=? ORDER BY id DESC LIMIT 50)`, j.ID, j.ID)
		a.hub.publish(event{Type: "cron.run", Data: map[string]any{"host": j.Host, "job_id": j.ID, "id": rid, "status": status}})
		r, _ := a.alertRule("job_failed")
		subj := strconv.FormatInt(j.ID, 10)
		if status == "failed" {
			a.fire(r, subj, j.Host, map[string]any{"name": j.Name, "code": code, "output": truncate(lastLines(out, 5), 400)})
		} else if status == "ok" {
			a.resolve(r.Key, subj)
		}
	}()
	return rid
}

func lastLines(s string, n int) string {
	ls := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(ls) > n {
		ls = ls[len(ls)-n:]
	}
	return strings.Join(ls, "\n")
}

// tailBuf keeps the last max bytes of output.
type tailBuf struct {
	b   []byte
	max int
}

func (t *tailBuf) add(l string) {
	t.b = append(t.b, l...)
	t.b = append(t.b, '\n')
	if len(t.b) > t.max {
		cut := len(t.b) - t.max
		if i := strings.IndexByte(string(t.b[cut:]), '\n'); i >= 0 {
			cut += i + 1
		}
		t.b = append([]byte("…\n"), t.b[cut:]...)
	}
}

func (a *App) execJob(j cronJob) (status string, code int, output, changeID string) {
	ctx := context.Background()
	switch j.Action.Type {
	case "script":
		c, err := a.agent(j.Host)
		if err != nil {
			return "failed", -1, errs.From(err).Error(), ""
		}
		timeout := j.Action.Timeout
		if timeout == 0 {
			timeout = 3600
		}
		ctx, cancel := context.WithTimeout(ctx, time.Duration(timeout+60)*time.Second)
		defer cancel()
		rc, err := c.Stream(ctx, "POST", "/v1/system/cron/exec", map[string]any{"script": j.Action.Script, "user": j.Action.User, "timeout": timeout})
		if err != nil {
			return "failed", -1, errs.From(err).Error(), ""
		}
		buf := &tailBuf{max: 64 << 10}
		var res struct {
			Code  int    `json:"code"`
			Error string `json:"error"`
		}
		res.Code = -1
		err = streamLines(rc, func(l string) {
			if r, ok := strings.CutPrefix(l, "::result "); ok {
				json.Unmarshal([]byte(r), &res)
				return
			}
			buf.add(l)
		})
		if err != nil {
			buf.add("berth: " + errs.From(err).Error())
			return "failed", -1, string(buf.b), ""
		}
		if res.Error != "" {
			buf.add("berth: " + res.Error)
		}
		if res.Code != 0 || res.Error != "" {
			return "failed", res.Code, string(buf.b), ""
		}
		return "ok", 0, string(buf.b), ""
	case "change":
		u, err := a.userByID(j.byID)
		if err != nil || u.Disabled {
			return "failed", -1, "the person who made this job no longer exists or is disabled", ""
		}
		k, ok := change.Get(j.Action.Kind)
		if !ok {
			return "failed", -1, "unknown action " + j.Action.Kind, ""
		}
		if k.Perm != "" && !u.Can(k.Perm) || len(u.Scope.Hosts) > 0 && !contains(u.Scope.Hosts, j.Host) && u.Role != RoleSuper {
			return "failed", -1, u.Username + " may no longer do " + j.Action.Kind, ""
		}
		params := j.Action.Params
		if len(params) == 0 {
			params = json.RawMessage(`{}`)
		}
		ac := change.Actor{Type: "schedule", ID: "job:" + strconv.FormatInt(j.ID, 10), Name: u.Username + " · " + j.Name, Via: "schedule"}
		c, err := a.pipe.Run(ctx, ac, j.Host, j.Action.Kind, params)
		id := ""
		if c != nil {
			id = c.ID
		}
		if err != nil {
			return "failed", -1, errs.From(err).Error(), id
		}
		switch c.Status {
		case "done", "running":
			return "ok", 0, c.Log, id
		case "pending_approval":
			return "pending", 0, "", id
		}
		return "failed", -1, c.Log, id
	}
	return "failed", -1, "bad action", ""
}

// interruptedJobs marks runs that were going when the panel stopped.
func (a *App) interruptedJobs() {
	a.db.Exec(`UPDATE cron_runs SET status='interrupted' WHERE status='running'`)
}

// ---- API ----

func init() {
	cronHooks = append(cronHooks, func(a *App, at time.Time) { a.runDueJobs(at) })
	startHooks = append(startHooks, func(a *App, ctx context.Context) { a.interruptedJobs() })
	routeHooks = append(routeHooks, func(a *App, mux *http.ServeMux) {
		mux.HandleFunc("GET /api/v1/hosts/{host}/jobs", a.authed("system.view", func(w http.ResponseWriter, r *http.Request) {
			host := r.PathValue("host")
			list := a.jobs(host)
			now := time.Now()
			for i := range list {
				if c, err := pipeline.ParseCron(list[i].Cron); err == nil && list[i].Enabled {
					if n := c.Next(now); !n.IsZero() {
						list[i].Next = n.Unix()
					}
				}
				list[i].Last = a.lastRun(list[i].ID)
			}
			var sys json.RawMessage
			if err := a.call(r.Context(), host, "GET", "/v1/system/cron", nil, &sys); err != nil {
				sys = json.RawMessage(`null`)
			}
			writeJSON(w, 200, map[string]any{"items": list, "system": sys})
		}))
		mux.HandleFunc("GET /api/v1/hosts/{host}/jobs/{id}/runs", a.authed("system.view", func(w http.ResponseWriter, r *http.Request) {
			id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
			rows, err := a.db.Query(`SELECT id, started_at, ms, status, code, trigger, change_id, output FROM cron_runs WHERE job_id=? ORDER BY id DESC LIMIT 50`, id)
			if err != nil {
				a.fail(w, r, err)
				return
			}
			defer rows.Close()
			out := []jobRunView{}
			for rows.Next() {
				v := jobRunView{JobID: id}
				rows.Scan(&v.ID, &v.Started, &v.MS, &v.Status, &v.Code, &v.Trigger, &v.ChangeID, &v.Output)
				out = append(out, v)
			}
			writeJSON(w, 200, map[string]any{"items": out})
		}))
		mux.HandleFunc("POST /api/v1/jobs/next", a.authed("", func(w http.ResponseWriter, r *http.Request) {
			// the next few times an expression fires, for the editor
			var p struct{ Cron string }
			json.NewDecoder(r.Body).Decode(&p)
			c, err := pipeline.ParseCron(p.Cron)
			if err != nil {
				a.fail(w, r, errs.New(400, "job.bad_cron", "value", p.Cron))
				return
			}
			out := []int64{}
			t := time.Now()
			for range 5 {
				if t = c.Next(t); t.IsZero() {
					break
				}
				out = append(out, t.Unix())
			}
			writeJSON(w, 200, map[string]any{"next": out})
		}))
	})

	jobProps := map[string]change.Prop{"id": {Type: "integer", Desc: "existing job (omit to add)"}, "name": {Type: "string"}, "cron": {Type: "string", Desc: "five fields or @hourly/@daily/@weekly/@monthly"},
		"action":  {Type: "object", Desc: `{"type":"script","script":"...","user":"root","timeout":3600} or {"type":"change","kind":"backup.run","params":{...}}`},
		"enabled": {Type: "boolean"}}
	change.Register(change.Kind{
		Name: "cron.set", Perm: "system.manage", Scope: "system:write", Title: "kind.cron.set",
		Params: change.ParamSchema{Props: jobProps, Required: []string{"name", "cron", "action"}}, CLI: "berth job set {{.name}}",
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			var p struct {
				jobSpec
				ID int64 `json:"id"`
			}
			json.Unmarshal(raw, &p)
			if err := p.validate(); err != nil {
				return change.Plan{}, err
			}
			what := p.Action.Kind
			cmds := []string{}
			if p.Action.Type == "script" {
				what = firstNonEmpty(p.Action.User, "root")
				cmds = append(cmds, strings.Split(strings.TrimRight(p.Action.Script, "\n"), "\n")...)
			}
			risk := change.RiskLow
			if p.Action.Type == "script" {
				risk = change.RiskMedium
			}
			sum := "plan.cron.add_" + p.Action.Type
			if p.ID != 0 {
				sum = "plan.cron.update_" + p.Action.Type
			}
			return change.Plan{Summary: sum, SummaryP: map[string]any{"name": p.Name, "cron": p.Cron, "what": what}, Risk: risk, Undoable: true, Commands: cmds}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			a := svc(env)
			var p struct {
				jobSpec
				ID      int64 `json:"id"`
				Enabled *bool `json:"enabled"`
			}
			json.Unmarshal(raw, &p)
			en := p.Enabled == nil || *p.Enabled
			spec, _ := json.Marshal(p.jobSpec)
			if p.Action.Type == "change" && env.ActorType != "user" {
				if old, err := a.job(p.ID); err != nil || old.Action.Kind != p.Action.Kind {
					return nil, nil, errs.New(403, "job.change_needs_person")
				}
			}
			byID, _ := strconv.ParseInt(env.ActorID, 10, 64)
			if env.ActorType != "user" {
				byID = 0
			}
			if p.ID != 0 {
				if old, err := a.job(p.ID); err == nil {
					if old.Host != env.Host {
						return nil, nil, errs.New(409, "job.other_host")
					}
					if byID == 0 {
						byID = old.byID
					}
					a.db.Exec(`UPDATE cron_jobs SET spec=?, enabled=?, created_by_id=? WHERE id=?`, string(spec), en, byID, p.ID)
					prev, _ := json.Marshal(map[string]any{"id": old.ID, "name": old.Name, "cron": old.Cron, "action": old.Action, "enabled": old.Enabled})
					return map[string]int64{"id": p.ID}, &change.UndoRecipe{Kind: "cron.set", Params: prev}, nil
				}
				// putting a deleted job back keeps its id
				if _, err := a.db.Exec(`INSERT INTO cron_jobs(id, host, spec, enabled, created_by, created_by_id, created_at) VALUES(?,?,?,?,?,?,?)`,
					p.ID, env.Host, string(spec), en, env.ActorName, byID, time.Now().Unix()); err != nil {
					return nil, nil, err
				}
				u, _ := json.Marshal(map[string]int64{"id": p.ID})
				return map[string]int64{"id": p.ID}, &change.UndoRecipe{Kind: "cron.delete", Params: u}, nil
			}
			res, err := a.db.Exec(`INSERT INTO cron_jobs(host, spec, enabled, created_by, created_by_id, created_at) VALUES(?,?,?,?,?,?)`,
				env.Host, string(spec), en, env.ActorName, byID, time.Now().Unix())
			if err != nil {
				return nil, nil, err
			}
			id, _ := res.LastInsertId()
			u, _ := json.Marshal(map[string]int64{"id": id})
			return map[string]int64{"id": id}, &change.UndoRecipe{Kind: "cron.delete", Params: u}, nil
		},
	})
	change.Register(change.Kind{
		Name: "cron.delete", Perm: "system.manage", Scope: "system:write", Title: "kind.cron.delete",
		Params: change.ParamSchema{Props: map[string]change.Prop{"id": {Type: "integer"}}, Required: []string{"id"}}, CLI: "berth job delete {{.id}}",
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			var p struct{ ID int64 }
			json.Unmarshal(raw, &p)
			j, err := svc(env).job(p.ID)
			if err != nil {
				return change.Plan{}, err
			}
			return change.Plan{Summary: "plan.cron.delete", SummaryP: map[string]any{"name": j.Name}, Risk: change.RiskLow, Undoable: true}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			a := svc(env)
			var p struct{ ID int64 }
			json.Unmarshal(raw, &p)
			j, err := a.job(p.ID)
			if err != nil {
				return nil, nil, err
			}
			a.db.Exec(`DELETE FROM cron_jobs WHERE id=?`, p.ID)
			a.resolve("job_failed", strconv.FormatInt(p.ID, 10))
			prev, _ := json.Marshal(map[string]any{"id": j.ID, "name": j.Name, "cron": j.Cron, "action": j.Action, "enabled": j.Enabled})
			return nil, &change.UndoRecipe{Kind: "cron.set", Params: prev}, nil
		},
	})
	change.Register(change.Kind{
		Name: "cron.run", Perm: "system.manage", Scope: "system:write", Title: "kind.cron.run",
		Params: change.ParamSchema{Props: map[string]change.Prop{"id": {Type: "integer"}}, Required: []string{"id"}}, CLI: "berth job run {{.id}}",
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			var p struct{ ID int64 }
			json.Unmarshal(raw, &p)
			j, err := svc(env).job(p.ID)
			if err != nil {
				return change.Plan{}, err
			}
			return change.Plan{Summary: "plan.cron.run", SummaryP: map[string]any{"name": j.Name}, Risk: change.RiskLow}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			a := svc(env)
			var p struct{ ID int64 }
			json.Unmarshal(raw, &p)
			j, err := a.job(p.ID)
			if err != nil {
				return nil, nil, err
			}
			rid := a.startJob(j, "manual")
			if rid == 0 {
				log.Printf("panel: job %d did not start", j.ID)
			}
			return map[string]int64{"run": rid}, nil, nil
		},
	})
}
