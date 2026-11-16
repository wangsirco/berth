package panel

// Backups (方案 §5.11): destinations (restic repositories), jobs (what, where, when,
// how long to keep), runs, restores and monthly restore drills.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/wangsirco/berth/internal/agent/backup"
	"github.com/wangsirco/berth/internal/agent/docker"
	"github.com/wangsirco/berth/internal/change"
	"github.com/wangsirco/berth/internal/errs"
	"github.com/wangsirco/berth/internal/pipeline"
	"github.com/wangsirco/berth/internal/sign"
)

// ---- destinations ----

// targetCfg is sealed in the database: everything needed to reach a repository.
type targetCfg struct {
	Kind     string            `json:"kind"` // local | s3 | sftp | rclone
	Path     string            `json:"path,omitempty"`
	Endpoint string            `json:"endpoint,omitempty"` // s3: https://… ; sftp: user@host
	Bucket   string            `json:"bucket,omitempty"`
	Access   string            `json:"access,omitempty"`
	Secret   string            `json:"secret,omitempty"`
	Remote   string            `json:"remote,omitempty"` // rclone backend type: drive, onedrive, dropbox, webdav, sftp…
	Options  map[string]string `json:"options,omitempty"`
	Token    string            `json:"token,omitempty"` // rclone authorize output
	Password string            `json:"password"`        // restic repository password (generated)
}

var rcloneKeyRe = regexp.MustCompile(`^[a-z_]{1,40}$`)

func (c targetCfg) repo(id int64) (backup.Repo, error) {
	r := backup.Repo{Password: c.Password}
	clean := path.Clean("/" + strings.TrimPrefix(c.Path, "/"))
	switch c.Kind {
	case "local":
		if !strings.HasPrefix(c.Path, "/") {
			return r, errs.New(400, "backup.bad_target")
		}
		r.URL = path.Clean(c.Path)
	case "s3":
		if c.Endpoint == "" || c.Bucket == "" || c.Access == "" || c.Secret == "" {
			return r, errs.New(400, "backup.bad_target")
		}
		r.URL = "s3:" + strings.TrimRight(c.Endpoint, "/") + "/" + c.Bucket + strings.TrimSuffix(clean, "/")
		r.Env = map[string]string{"AWS_ACCESS_KEY_ID": c.Access, "AWS_SECRET_ACCESS_KEY": c.Secret}
	case "rclone", "sftp":
		name := "berth" + strconv.FormatInt(id, 10)
		var conf strings.Builder
		fmt.Fprintf(&conf, "[%s]\n", name)
		remote := c.Remote
		if c.Kind == "sftp" {
			remote = "sftp"
		}
		if remote == "" || !rcloneKeyRe.MatchString(remote) {
			return r, errs.New(400, "backup.bad_target")
		}
		fmt.Fprintf(&conf, "type = %s\n", remote)
		for k, v := range c.Options {
			if !rcloneKeyRe.MatchString(k) || strings.ContainsAny(v, "\n\r") {
				return r, errs.New(400, "backup.bad_target")
			}
			fmt.Fprintf(&conf, "%s = %s\n", k, v)
		}
		if c.Token != "" {
			if strings.ContainsAny(c.Token, "\n\r") || !json.Valid([]byte(c.Token)) {
				return r, errs.New(400, "backup.bad_token")
			}
			fmt.Fprintf(&conf, "token = %s\n", c.Token)
		}
		r.Rclone = conf.String()
		if _, ok := c.Options["pass"]; ok {
			r.Obscure = []string{"pass"}
		}
		r.URL = "rclone:" + name + ":" + strings.TrimPrefix(clean, "/")
	default:
		return r, errs.New(400, "backup.bad_target")
	}
	return r, nil
}

type backupTarget struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	Where     string `json:"where"` // shown: path, bucket, remote
	CreatedAt int64  `json:"created_at"`
	cfgEnc    []byte
}

func (a *App) backupTargets() ([]backupTarget, error) {
	rows, err := a.db.Query(`SELECT id, name, kind, cfg_enc, created_at FROM backup_targets ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []backupTarget{}
	for rows.Next() {
		var t backupTarget
		rows.Scan(&t.ID, &t.Name, &t.Kind, &t.cfgEnc, &t.CreatedAt)
		if c, err := a.targetCfg(t); err == nil {
			t.Where = describeTarget(c)
		}
		out = append(out, t)
	}
	return out, nil
}

func describeTarget(c targetCfg) string {
	switch c.Kind {
	case "local":
		return c.Path
	case "s3":
		return c.Endpoint + "/" + c.Bucket + c.Path
	case "sftp":
		return c.Options["host"] + ":" + c.Path
	}
	return c.Remote + ":" + c.Path
}

func (a *App) backupTarget(id int64) (backupTarget, error) {
	var t backupTarget
	err := a.db.QueryRow(`SELECT id, name, kind, cfg_enc, created_at FROM backup_targets WHERE id=?`, id).Scan(&t.ID, &t.Name, &t.Kind, &t.cfgEnc, &t.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return t, errs.New(404, "backup.target_not_found", "id", id)
	}
	return t, err
}

func (a *App) targetCfg(t backupTarget) (targetCfg, error) {
	var c targetCfg
	plain, err := a.open(t.cfgEnc)
	if err != nil {
		return c, err
	}
	err = json.Unmarshal(plain, &c)
	return c, err
}

func (a *App) targetRepo(id int64) (backup.Repo, error) {
	t, err := a.backupTarget(id)
	if err != nil {
		return backup.Repo{}, err
	}
	c, err := a.targetCfg(t)
	if err != nil {
		return backup.Repo{}, err
	}
	return c.repo(t.ID)
}

// ---- jobs ----

type backupSource struct {
	Type string `json:"type"` // path | app | project | database | panel
	Ref  string `json:"ref"`  // the path, app or project name, or "instance/database"
}

type backupJob struct {
	ID         int64          `json:"id"`
	Name       string         `json:"name"`
	Host       string         `json:"host"`
	Target     int64          `json:"target"`
	Sources    []backupSource `json:"sources"`
	Excludes   []string       `json:"excludes"`
	Cron       string         `json:"cron"`
	Keep       backup.Keep    `json:"keep"`
	Stop       bool           `json:"stop"` // pause the source containers while files are read
	LimitKBps  int            `json:"limit_kbps"`
	Drill      bool           `json:"drill"` // monthly restore drill
	Enabled    bool           `json:"enabled"`
	LastRun    *backupRun     `json:"last_run,omitempty"`
	LastDrill  *backupRun     `json:"last_drill,omitempty"`
	TargetName string         `json:"target_name,omitempty"`
}

type backupRun struct {
	ID       int64  `json:"id"`
	JobID    int64  `json:"job_id"`
	Kind     string `json:"kind"`   // backup | drill | restore
	Status   string `json:"status"` // running | done | failed
	Snapshot string `json:"snapshot,omitempty"`
	Bytes    int64  `json:"bytes"`
	Message  string `json:"message,omitempty"`
	Started  int64  `json:"started"`
	Finished int64  `json:"finished,omitempty"`
	ChangeID string `json:"change_id,omitempty"`
}

const jobCols = `id, name, host, target_id, spec, enabled`

func (a *App) scanJob(row interface{ Scan(...any) error }) (backupJob, error) {
	var j backupJob
	var spec string
	var en int
	if err := row.Scan(&j.ID, &j.Name, &j.Host, &j.Target, &spec, &en); err != nil {
		return j, err
	}
	json.Unmarshal([]byte(spec), &j)
	j.Enabled = en == 1
	return j, nil
}

func (a *App) backupJobs() ([]backupJob, error) {
	rows, err := a.db.Query(`SELECT ` + jobCols + ` FROM backup_jobs ORDER BY name`)
	if err != nil {
		return nil, err
	}
	var out []backupJob
	for rows.Next() {
		j, err := a.scanJob(rows)
		if err == nil {
			out = append(out, j)
		}
	}
	rows.Close()
	for i := range out {
		out[i].LastRun = a.lastBackupRun(out[i].ID, "backup")
		out[i].LastDrill = a.lastBackupRun(out[i].ID, "drill")
		if t, err := a.backupTarget(out[i].Target); err == nil {
			out[i].TargetName = t.Name
		}
	}
	if out == nil {
		out = []backupJob{}
	}
	return out, nil
}

func (a *App) backupJob(id int64) (backupJob, error) {
	j, err := a.scanJob(a.db.QueryRow(`SELECT `+jobCols+` FROM backup_jobs WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return j, errs.New(404, "backup.job_not_found", "id", id)
	}
	return j, err
}

func (a *App) lastBackupRun(job int64, kind string) *backupRun {
	var r backupRun
	if a.db.QueryRow(`SELECT id, job_id, kind, status, snapshot, bytes, message, started, finished, change_id FROM backup_runs WHERE job_id=? AND kind=? ORDER BY id DESC LIMIT 1`, job, kind).
		Scan(&r.ID, &r.JobID, &r.Kind, &r.Status, &r.Snapshot, &r.Bytes, &r.Message, &r.Started, &r.Finished, &r.ChangeID) != nil {
		return nil
	}
	return &r
}

var backupNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,40}$`)

func (j *backupJob) validate() error {
	if !backupNameRe.MatchString(j.Name) || len(j.Sources) == 0 {
		return errs.New(400, "backup.bad_job")
	}
	if j.Cron != "" {
		if _, err := pipeline.ParseCron(j.Cron); err != nil {
			return pipeline.SpecErr(err)
		}
	}
	for _, s := range j.Sources {
		switch s.Type {
		case "path":
			if !strings.HasPrefix(s.Ref, "/") || path.Clean(s.Ref) == "/" {
				return errs.New(400, "backup.bad_path", "path", s.Ref)
			}
		case "app", "project", "database", "panel":
		default:
			return errs.New(400, "backup.bad_job")
		}
	}
	if j.Keep.Last+j.Keep.Daily+j.Keep.Weekly+j.Keep.Monthly == 0 {
		j.Keep = backup.Keep{Daily: 7, Weekly: 4, Monthly: 6}
	}
	return nil
}

// plan turns a job's sources into what the agent backs up.
func (a *App) backupRequest(ctx context.Context, j backupJob, repo backup.Repo) (backup.RunRequest, error) {
	req := backup.RunRequest{Repo: repo, Job: j.Name, Excludes: j.Excludes, Keep: j.Keep, LimitKBps: j.LimitKBps}
	containers, _ := a.containers(ctx, j.Host)
	for _, s := range j.Sources {
		switch s.Type {
		case "path":
			req.Paths = append(req.Paths, path.Clean(s.Ref))
		case "app":
			req.Paths = append(req.Paths, appsRoot+"/"+s.Ref, docker.StacksDir+"/"+s.Ref)
			if j.Stop {
				for _, c := range containers {
					if c.Project == s.Ref && c.State == "running" {
						req.Stop = append(req.Stop, c.Name)
					}
				}
			}
		case "project":
			p, err := a.findProject(ctx, j.Host, s.Ref)
			if err != nil {
				return req, err
			}
			req.Paths = append(req.Paths, p.Dir)
			for _, c := range containers {
				if c.Project != s.Ref {
					continue
				}
				for _, m := range c.Mounts {
					switch {
					case m.Type == "bind" && m.RW && m.Source != "/" && !strings.HasPrefix(m.Source, "/var/run") && !strings.HasPrefix(m.Source, "/proc") && !strings.HasPrefix(m.Source, "/sys"):
						req.Paths = append(req.Paths, m.Source)
					case m.Type == "volume" && m.Name != "":
						req.Paths = append(req.Paths, "/var/lib/docker/volumes/"+m.Name+"/_data")
					}
				}
				if j.Stop && c.State == "running" {
					req.Stop = append(req.Stop, c.Name)
				}
			}
		case "database":
			inst, db, _ := strings.Cut(s.Ref, "/")
			id, _ := strconv.ParseInt(inst, 10, 64)
			d, err := a.dbInstance(j.Host, id)
			if err != nil {
				return req, err
			}
			if d.Container == "" && d.Native == "" || d.Engine == "redis" || d.Engine == "sqlite" {
				return req, errs.New(400, "backup.db_needs_container", "name", d.Name)
			}
			t, err := a.dbTarget(ctx, d, db)
			if err != nil {
				return req, err
			}
			req.Dumps = append(req.Dumps, backup.Dump{Container: d.Container, Target: t})
		case "panel":
			if j.Host != "local" {
				return req, errs.New(400, "backup.panel_local_only")
			}
			snap, err := a.panelSnapshot()
			if err != nil {
				return req, err
			}
			req.Paths = append(req.Paths, snap, a.cfg.EtcDir)
		}
	}
	return req, nil
}

// panelSnapshot writes a consistent copy of the panel's database next to it.
func (a *App) panelSnapshot() (string, error) {
	p := path.Join(a.cfg.DataDir, "backup-snapshot.db")
	a.db.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`)
	_ = removeFile(p)
	if _, err := a.db.Exec(`VACUUM INTO ?`, p); err != nil {
		return "", err
	}
	return p, nil
}

// ---- runs ----

func (a *App) startBackupRun(job int64, kind, changeID string) int64 {
	res, err := a.db.Exec(`INSERT INTO backup_runs(job_id, kind, status, started, change_id) VALUES(?,?,?,?,?)`, job, kind, "running", time.Now().Unix(), changeID)
	if err != nil {
		return 0
	}
	id, _ := res.LastInsertId()
	return id
}

func (a *App) finishBackupRun(id int64, snapshot string, bytes int64, err error) {
	st, msg := "done", ""
	if err != nil {
		st, msg = "failed", storeErr(err)
	}
	a.db.Exec(`UPDATE backup_runs SET status=?, snapshot=?, bytes=?, message=?, finished=? WHERE id=?`, st, snapshot, bytes, msg, time.Now().Unix(), id)
	var job int64
	var kind string
	a.db.QueryRow(`SELECT job_id, kind FROM backup_runs WHERE id=?`, id).Scan(&job, &kind)
	a.hub.publish(event{Type: "backup.run", Data: map[string]any{"job": job, "run": id, "kind": kind, "status": st}})
	if err != nil && backupFailedHook != nil {
		backupFailedHook(a, job, kind, err)
	}
}

// backupFailedHook lets the alert module hear about failures.
var backupFailedHook func(a *App, job int64, kind string, err error)

func (a *App) streamAgent(env change.Env, host, path string, body any, result any) error {
	c, err := a.agent(host)
	if err != nil {
		return err
	}
	rc, err := c.Stream(env.Ctx, "POST", path, body)
	if err != nil {
		return err
	}
	return streamLines(rc, func(l string) {
		if strings.HasPrefix(l, "::result ") {
			if result != nil {
				json.Unmarshal([]byte(l[9:]), result)
			}
			return
		}
		env.Log("%s", l)
	})
}

// ---- kinds ----

func init() {
	routeHooks = append(routeHooks, func(a *App, mux *http.ServeMux) {
		mux.HandleFunc("GET /api/v1/backups", a.authed("backup.view", a.handleBackups))
		mux.HandleFunc("GET /api/v1/backups/jobs/{id}/runs", a.authed("backup.view", a.handleBackupRuns))
		mux.HandleFunc("GET /api/v1/backups/jobs/{id}/snapshots", a.authed("backup.view", a.handleSnapshots))
		mux.HandleFunc("POST /api/v1/backups/jobs/{id}/ls", a.authed("backup.view", a.handleSnapshotLs))
		mux.HandleFunc("POST /api/v1/backups/targets/{id}/key", a.authed("backup.manage", a.handleRecoveryKey))
		mux.HandleFunc("GET /api/v1/hosts/{host}/backup/tools", a.authed("backup.view", func(w http.ResponseWriter, r *http.Request) {
			var t map[string]string
			if err := a.call(r.Context(), r.PathValue("host"), "GET", "/v1/backup/tools", nil, &t); err != nil {
				a.fail(w, r, err)
				return
			}
			writeJSON(w, 200, t)
		}))
	})

	change.Register(change.Kind{
		Name: "backup.tools.install", Perm: "backup.manage", Scope: "system:write", Title: "kind.backup.tools.install",
		Params: change.ParamSchema{Props: map[string]change.Prop{}}, CLI: "berth backup tools install", Heavy: true, MemMB: 64,
		Plan: func(env change.Env, _ json.RawMessage) (change.Plan, error) {
			return change.Plan{Summary: "plan.backup.tools.install", Risk: change.RiskLow, Commands: []string{"dnf install restic rclone   # or apt-get"}}, nil
		},
		Apply: func(env change.Env, _ json.RawMessage) (any, *change.UndoRecipe, error) {
			var out map[string]string
			return out, nil, svc(env).streamAgent(env, env.Host, "/v1/backup/tools/install", nil, &out)
		},
	})

	targetProps := map[string]change.Prop{"id": {Type: "integer"}, "name": {Type: "string"}, "kind": {Type: "string", Enum: []string{"local", "s3", "sftp", "rclone"}},
		"path": {Type: "string"}, "endpoint": {Type: "string"}, "bucket": {Type: "string"}, "access": {Type: "string"}, "secret": {Type: "string"},
		"remote": {Type: "string", Desc: "rclone backend: drive, onedrive, dropbox, webdav…"}, "options": {Type: "object"}, "token": {Type: "string", Desc: "output of rclone authorize"}}
	change.Register(change.Kind{
		Name: "backup.target.set", Perm: "backup.manage", Scope: "system:write", Title: "kind.backup.target.set",
		Params: change.ParamSchema{Props: targetProps, Required: []string{"name", "kind"}}, Redact: []string{"secret", "token", "options"},
		CLI: "berth backup target set {{.name}}", Heavy: true, MemMB: 64,
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			var p struct {
				targetCfg
				ID   int64  `json:"id"`
				Name string `json:"name"`
			}
			json.Unmarshal(raw, &p)
			if !backupNameRe.MatchString(p.Name) {
				return change.Plan{}, errs.New(400, "backup.bad_name")
			}
			p.targetCfg.Password = "x"
			if _, err := p.targetCfg.repo(1); err != nil {
				return change.Plan{}, err
			}
			return change.Plan{Summary: "plan.backup.target.set", SummaryP: map[string]any{"name": p.Name, "where": describeTarget(p.targetCfg)}, Risk: change.RiskLow,
				Affected: []change.Object{{Type: "backup_target", ID: p.Name, Name: p.Name}}}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			a := svc(env)
			var p struct {
				targetCfg
				ID   int64  `json:"id"`
				Name string `json:"name"`
			}
			json.Unmarshal(raw, &p)
			cfg := p.targetCfg
			if p.ID != 0 { // keep the repository password; keep a token that was not re-entered
				old, err := a.backupTarget(p.ID)
				if err != nil {
					return nil, nil, err
				}
				oc, _ := a.targetCfg(old)
				cfg.Password = oc.Password
				if cfg.Token == "" {
					cfg.Token = oc.Token
				}
				if cfg.Secret == "" {
					cfg.Secret = oc.Secret
				}
			} else {
				cfg.Password = sign.RandomHex(24)
			}
			repo, err := cfg.repo(max(p.ID, 1))
			if err != nil {
				return nil, nil, err
			}
			b, _ := json.Marshal(cfg)
			enc, err := a.seal(b)
			if err != nil {
				return nil, nil, err
			}
			id := p.ID
			if id == 0 {
				res, err := a.db.Exec(`INSERT INTO backup_targets(name, kind, cfg_enc, created_at) VALUES(?,?,?,?)`, p.Name, cfg.Kind, enc, time.Now().Unix())
				if err != nil {
					return nil, nil, errs.New(409, "backup.exists", "name", p.Name)
				}
				id, _ = res.LastInsertId()
				repo, _ = cfg.repo(id)
			} else if _, err := a.db.Exec(`UPDATE backup_targets SET name=?, kind=?, cfg_enc=? WHERE id=?`, p.Name, cfg.Kind, enc, id); err != nil {
				return nil, nil, err
			}
			env.Log("initialising the repository at %s", describeTarget(cfg))
			if err := a.call(env.Ctx, env.Host, "POST", "/v1/backup/init", repo, nil); err != nil {
				if p.ID == 0 {
					a.db.Exec(`DELETE FROM backup_targets WHERE id=?`, id)
				}
				return nil, nil, err
			}
			return map[string]int64{"id": id}, nil, nil
		},
	})
	change.Register(change.Kind{
		Name: "backup.target.delete", Perm: "backup.manage", Scope: "system:write", Title: "kind.backup.target.delete",
		Params: change.ParamSchema{Props: map[string]change.Prop{"id": {Type: "integer"}}, Required: []string{"id"}}, CLI: "berth backup target rm {{.id}}",
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			var p struct{ ID int64 }
			json.Unmarshal(raw, &p)
			a := svc(env)
			t, err := a.backupTarget(p.ID)
			if err != nil {
				return change.Plan{}, err
			}
			var n int
			a.db.QueryRow(`SELECT COUNT(*) FROM backup_jobs WHERE target_id=?`, p.ID).Scan(&n)
			if n > 0 {
				return change.Plan{}, errs.New(409, "backup.target_in_use")
			}
			return change.Plan{Summary: "plan.backup.target.delete", SummaryP: map[string]any{"name": t.Name}, Risk: change.RiskMedium,
				Affected: []change.Object{{Type: "backup_target", ID: strconv.FormatInt(t.ID, 10), Name: t.Name}}}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			var p struct{ ID int64 }
			json.Unmarshal(raw, &p)
			_, err := svc(env).db.Exec(`DELETE FROM backup_targets WHERE id=?`, p.ID)
			return nil, nil, err
		},
	})

	jobProps := map[string]change.Prop{"id": {Type: "integer"}, "name": {Type: "string"}, "target": {Type: "integer"},
		"sources":  {Type: "array", Desc: "[{type: path|app|project|database|panel, ref}]"},
		"excludes": {Type: "array"}, "cron": {Type: "string"}, "keep": {Type: "object", Desc: "{last, daily, weekly, monthly}"},
		"stop": {Type: "boolean"}, "limit_kbps": {Type: "integer"}, "drill": {Type: "boolean"}, "enabled": {Type: "boolean"}}
	change.Register(change.Kind{
		Name: "backup.job.set", Perm: "backup.manage", Scope: "system:write", Title: "kind.backup.job.set",
		Params: change.ParamSchema{Props: jobProps, Required: []string{"name", "target", "sources"}}, CLI: "berth backup job set {{.name}}",
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			a := svc(env)
			var j backupJob
			json.Unmarshal(raw, &j)
			j.Host = env.Host
			if err := j.validate(); err != nil {
				return change.Plan{}, err
			}
			repo, err := a.targetRepo(j.Target)
			if err != nil {
				return change.Plan{}, err
			}
			req, err := a.backupRequest(env.Ctx, j, repo)
			if err != nil {
				return change.Plan{}, err
			}
			plan := change.Plan{Summary: "plan.backup.job.set", SummaryP: map[string]any{"name": j.Name, "cron": firstNonEmpty(j.Cron, "—")}, Risk: change.RiskLow, Undoable: true,
				Affected: []change.Object{{Type: "backup_job", ID: j.Name, Name: j.Name, Host: env.Host}}}
			for _, p := range req.Paths {
				plan.Commands = append(plan.Commands, "restic backup "+p)
			}
			for _, d := range req.Dumps {
				plan.Commands = append(plan.Commands, "dump "+d.Engine+" "+d.Container+"/"+d.Database)
			}
			if len(req.Stop) > 0 {
				plan.Commands = append(plan.Commands, "# pauses "+strings.Join(req.Stop, ", ")+" while reading files")
			}
			return plan, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			a := svc(env)
			var j backupJob
			json.Unmarshal(raw, &j)
			j.Host = env.Host
			if err := j.validate(); err != nil {
				return nil, nil, err
			}
			var old *backupJob
			if j.ID != 0 {
				o, err := a.backupJob(j.ID)
				if err != nil {
					return nil, nil, err
				}
				old = &o
			}
			spec, _ := json.Marshal(map[string]any{"sources": j.Sources, "excludes": j.Excludes, "cron": j.Cron, "keep": j.Keep, "stop": j.Stop, "limit_kbps": j.LimitKBps, "drill": j.Drill})
			en := 1
			if raw != nil && strings.Contains(string(raw), `"enabled":false`) {
				en = 0
			}
			if old == nil {
				res, err := a.db.Exec(`INSERT INTO backup_jobs(name, host, target_id, spec, enabled, created_at) VALUES(?,?,?,?,?,?)`, j.Name, j.Host, j.Target, string(spec), en, time.Now().Unix())
				if err != nil {
					return nil, nil, errs.New(409, "backup.exists", "name", j.Name)
				}
				j.ID, _ = res.LastInsertId()
				u, _ := json.Marshal(map[string]int64{"id": j.ID})
				return map[string]int64{"id": j.ID}, &change.UndoRecipe{Kind: "backup.job.delete", Params: u}, nil
			}
			if _, err := a.db.Exec(`UPDATE backup_jobs SET name=?, target_id=?, spec=?, enabled=? WHERE id=?`, j.Name, j.Target, string(spec), en, j.ID); err != nil {
				return nil, nil, err
			}
			u, _ := json.Marshal(old)
			return map[string]int64{"id": j.ID}, &change.UndoRecipe{Kind: "backup.job.set", Params: u}, nil
		},
	})
	change.Register(change.Kind{
		Name: "backup.job.delete", Perm: "backup.manage", Scope: "system:write", Title: "kind.backup.job.delete",
		Params: change.ParamSchema{Props: map[string]change.Prop{"id": {Type: "integer"}}, Required: []string{"id"}}, CLI: "berth backup job rm {{.id}}",
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			var p struct{ ID int64 }
			json.Unmarshal(raw, &p)
			j, err := svc(env).backupJob(p.ID)
			if err != nil {
				return change.Plan{}, err
			}
			return change.Plan{Summary: "plan.backup.job.delete", SummaryP: map[string]any{"name": j.Name}, Risk: change.RiskMedium, Undoable: true,
				Affected: []change.Object{{Type: "backup_job", ID: strconv.FormatInt(j.ID, 10), Name: j.Name, Host: j.Host}}}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			a := svc(env)
			var p struct{ ID int64 }
			json.Unmarshal(raw, &p)
			j, err := a.backupJob(p.ID)
			if err != nil {
				return nil, nil, err
			}
			a.db.Exec(`DELETE FROM backup_jobs WHERE id=?`, p.ID)
			j.ID = 0
			u, _ := json.Marshal(j)
			return nil, &change.UndoRecipe{Kind: "backup.job.set", Params: u}, nil
		},
	})
	change.Register(change.Kind{
		Name: "backup.run", Perm: "backup.manage", Scope: "system:write", Title: "kind.backup.run",
		Params: change.ParamSchema{Props: map[string]change.Prop{"id": {Type: "integer"}}, Required: []string{"id"}}, CLI: "berth backup run {{.id}}", Heavy: true, MemMB: 256,
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			var p struct{ ID int64 }
			json.Unmarshal(raw, &p)
			j, err := svc(env).backupJob(p.ID)
			if err != nil {
				return change.Plan{}, err
			}
			return change.Plan{Summary: "plan.backup.run", SummaryP: map[string]any{"name": j.Name}, Risk: change.RiskLow,
				Affected: []change.Object{{Type: "backup_job", ID: strconv.FormatInt(j.ID, 10), Name: j.Name, Host: j.Host}}}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			var p struct{ ID int64 }
			json.Unmarshal(raw, &p)
			res, err := svc(env).runBackup(env, p.ID)
			return res, nil, err
		},
	})
	change.Register(change.Kind{
		Name: "backup.drill", Perm: "backup.manage", Scope: "system:write", Title: "kind.backup.drill",
		Params: change.ParamSchema{Props: map[string]change.Prop{"id": {Type: "integer"}}, Required: []string{"id"}}, CLI: "berth backup drill {{.id}}", Heavy: true, MemMB: 256,
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			var p struct{ ID int64 }
			json.Unmarshal(raw, &p)
			j, err := svc(env).backupJob(p.ID)
			if err != nil {
				return change.Plan{}, err
			}
			return change.Plan{Summary: "plan.backup.drill", SummaryP: map[string]any{"name": j.Name}, Risk: change.RiskLow,
				Affected: []change.Object{{Type: "backup_job", ID: strconv.FormatInt(j.ID, 10), Name: j.Name, Host: j.Host}}}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			var p struct{ ID int64 }
			json.Unmarshal(raw, &p)
			res, err := svc(env).drillBackup(env, p.ID)
			return res, nil, err
		},
	})
	change.Register(change.Kind{
		Name: "backup.restore", Perm: "backup.restore", Scope: "system:write", Title: "kind.backup.restore",
		Params: change.ParamSchema{Props: map[string]change.Prop{"id": {Type: "integer", Desc: "job"}, "snapshot": {Type: "string"},
			"target": {Type: "string", Desc: "/ = original location, or a folder"}, "include": {Type: "array", Desc: "only these paths"},
			"to_host": {Type: "string", Desc: "restore onto another managed host (default: the job's own)"}}, Required: []string{"id", "snapshot", "target"}},
		CLI: "berth backup restore {{.id}} {{.snapshot}} --to {{.target}}", Heavy: true, MemMB: 256,
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			var p struct {
				ID       int64
				Snapshot string
				Target   string
				Include  []string
				ToHost   string `json:"to_host"`
			}
			json.Unmarshal(raw, &p)
			j, err := svc(env).backupJob(p.ID)
			if err != nil {
				return change.Plan{}, err
			}
			if p.ToHost != "" && p.ToHost != j.Host {
				if on, _ := svc(env).online(p.ToHost); !on {
					return change.Plan{}, errs.New(409, "agent.offline", "host", p.ToHost)
				}
				return change.Plan{Summary: "plan.backup.restore_other", SummaryP: map[string]any{"name": j.Name, "snapshot": p.Snapshot, "target": p.Target, "from": j.Host, "to": p.ToHost},
					Risk: change.RiskHigh, Commands: []string{"restic restore " + p.Snapshot + " --target " + p.Target + " --verify   # on " + p.ToHost},
					Affected: []change.Object{{Type: "backup_job", ID: strconv.FormatInt(j.ID, 10), Name: j.Name, Host: j.Host}, {Type: "host", ID: p.ToHost, Name: p.ToHost}}}, nil
			}
			plan := change.Plan{Summary: "plan.backup.restore_new", SummaryP: map[string]any{"name": j.Name, "snapshot": p.Snapshot, "target": p.Target}, Risk: change.RiskMedium,
				Commands: []string{"restic restore " + p.Snapshot + " --target " + p.Target + " --verify"}, Affected: []change.Object{{Type: "backup_job", ID: strconv.FormatInt(j.ID, 10), Name: j.Name, Host: j.Host}}}
			for _, i := range p.Include {
				plan.Commands[0] += " --include " + i
			}
			if path.Clean(p.Target) == "/" {
				plan.Summary, plan.Risk = "plan.backup.restore_original", change.RiskHigh
				plan.Commands = append([]string{"# a snapshot of the current state is taken first"}, plan.Commands...)
			}
			return plan, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			var p struct {
				ID       int64
				Snapshot string
				Target   string
				Include  []string
				ToHost   string `json:"to_host"`
			}
			json.Unmarshal(raw, &p)
			res, err := svc(env).restoreBackup(env, p.ID, p.Snapshot, p.Target, p.Include, p.ToHost)
			return res, nil, err
		},
	})
}

func (a *App) runBackup(env change.Env, id int64) (*backup.RunResult, error) {
	j, err := a.backupJob(id)
	if err != nil {
		return nil, err
	}
	run := a.startBackupRun(j.ID, "backup", env.ChangeID)
	repo, err := a.targetRepo(j.Target)
	if err != nil {
		a.finishBackupRun(run, "", 0, err)
		return nil, err
	}
	req, err := a.backupRequest(env.Ctx, j, repo)
	if err != nil {
		a.finishBackupRun(run, "", 0, err)
		return nil, err
	}
	var res backup.RunResult
	a.markPaused(j.Host, req.Stop, true)
	err = a.streamAgent(env, j.Host, "/v1/backup/run", req, &res)
	a.markPaused(j.Host, req.Stop, false)
	a.finishBackupRun(run, res.Snapshot, res.BytesAdded, err)
	return &res, err
}

func (a *App) drillBackup(env change.Env, id int64) (map[string]any, error) {
	j, err := a.backupJob(id)
	if err != nil {
		return nil, err
	}
	run := a.startBackupRun(j.ID, "drill", env.ChangeID)
	repo, err := a.targetRepo(j.Target)
	if err != nil {
		a.finishBackupRun(run, "", 0, err)
		return nil, err
	}
	var res map[string]any
	err = a.streamAgent(env, j.Host, "/v1/backup/drill", map[string]any{"repo": repo, "job": j.Name}, &res)
	snap, _ := res["snapshot"].(string)
	b, _ := res["bytes"].(float64)
	a.finishBackupRun(run, snap, int64(b), err)
	return res, err
}

func (a *App) restoreBackup(env change.Env, id int64, snapshot, target string, include []string, toHost string) (map[string]any, error) {
	j, err := a.backupJob(id)
	if err != nil {
		return nil, err
	}
	host := j.Host
	if toHost != "" {
		host = toHost
	}
	run := a.startBackupRun(j.ID, "restore", env.ChangeID)
	repo, err := a.targetRepo(j.Target)
	if err != nil {
		a.finishBackupRun(run, "", 0, err)
		return nil, err
	}
	req := backup.RestoreRequest{Repo: repo, Job: j.Name, Snapshot: snapshot, Target: target, Include: include}
	if path.Clean(target) == "/" && host == j.Host {
		r, err := a.backupRequest(env.Ctx, j, repo)
		if err == nil {
			req.SafetyPaths = r.Paths
			if len(include) > 0 {
				req.SafetyPaths = include
			}
		}
	}
	var res map[string]any
	err = a.streamAgent(env, host, "/v1/backup/restore", req, &res)
	a.finishBackupRun(run, snapshot, 0, err)
	return res, err
}

// backupLoop runs scheduled backups and the monthly drills (1st of the month, 04:30).
func (a *App) backupLoop(ctx context.Context) {
	for {
		next := time.Now().Truncate(time.Minute).Add(time.Minute)
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Until(next)):
		}
		jobs, err := a.backupJobs()
		if err != nil {
			continue
		}
		for _, j := range jobs {
			if !j.Enabled {
				continue
			}
			actor := change.Actor{Type: "system", ID: "schedule", Name: "schedule", Via: "api"}
			params, _ := json.Marshal(map[string]int64{"id": j.ID})
			if c, err := pipeline.ParseCron(j.Cron); j.Cron != "" && err == nil && c.Match(next) {
				if _, err := a.pipe.Run(context.Background(), actor, j.Host, "backup.run", params); err != nil {
					log.Printf("panel: scheduled backup %s: %v", j.Name, err)
				}
			}
			if j.Drill && next.Day() == 1 && next.Hour() == 4 && next.Minute() == 30 {
				a.pipe.Run(context.Background(), actor, j.Host, "backup.drill", params)
			}
		}
	}
}

// ---- API ----

func (a *App) handleBackups(w http.ResponseWriter, r *http.Request) {
	targets, err := a.backupTargets()
	if err != nil {
		a.fail(w, r, err)
		return
	}
	jobs, err := a.backupJobs()
	if err != nil {
		a.fail(w, r, err)
		return
	}
	for _, j := range jobs {
		for _, run := range []*backupRun{j.LastRun, j.LastDrill} {
			if run != nil {
				run.Message = a.shownErr(r, run.Message)
			}
		}
	}
	writeJSON(w, 200, map[string]any{"targets": targets, "jobs": jobs})
}

func (a *App) handleBackupRuns(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	rows, err := a.db.Query(`SELECT id, job_id, kind, status, snapshot, bytes, message, started, finished, change_id FROM backup_runs WHERE job_id=? ORDER BY id DESC LIMIT 50`, id)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	defer rows.Close()
	out := []backupRun{}
	for rows.Next() {
		var x backupRun
		rows.Scan(&x.ID, &x.JobID, &x.Kind, &x.Status, &x.Snapshot, &x.Bytes, &x.Message, &x.Started, &x.Finished, &x.ChangeID)
		x.Message = a.shownErr(r, x.Message)
		out = append(out, x)
	}
	writeJSON(w, 200, map[string]any{"items": out})
}

func (a *App) jobRepo(r *http.Request) (backupJob, backup.Repo, error) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	j, err := a.backupJob(id)
	if err != nil {
		return j, backup.Repo{}, err
	}
	repo, err := a.targetRepo(j.Target)
	return j, repo, err
}

func (a *App) handleSnapshots(w http.ResponseWriter, r *http.Request) {
	j, repo, err := a.jobRepo(r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	var out []backup.Snapshot
	if err := a.call(r.Context(), j.Host, "POST", "/v1/backup/snapshots", map[string]any{"repo": repo, "job": j.Name}, &out); err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"items": out})
}

func (a *App) handleSnapshotLs(w http.ResponseWriter, r *http.Request) {
	var in struct{ Snapshot, Dir string }
	readJSON(r, &in)
	j, repo, err := a.jobRepo(r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	var out []backup.Node
	if err := a.call(r.Context(), j.Host, "POST", "/v1/backup/ls", map[string]any{"repo": repo, "snapshot": in.Snapshot, "dir": in.Dir}, &out); err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"items": out})
}

// POST /api/v1/backups/targets/{id}/key: the repository password, to keep somewhere safe —
// without it the backups cannot be read if this server is lost.
func (a *App) handleRecoveryKey(w http.ResponseWriter, r *http.Request) {
	if err := a.requireReauth(r); err != nil {
		a.fail(w, r, err)
		return
	}
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	t, err := a.backupTarget(id)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	c, err := a.targetCfg(t)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	repo, _ := c.repo(t.ID)
	_, _ = a.pipe.Record(currentActor(r), "local", "backup.key_view", map[string]any{"id": id, "name": t.Name}, change.Plan{
		Summary: "plan.backup.key_view", SummaryP: map[string]any{"name": t.Name}, Risk: change.RiskHigh,
		Affected: []change.Object{{Type: "backup_target", ID: strconv.FormatInt(id, 10), Name: t.Name}}})
	writeJSON(w, 200, map[string]any{"repository": repo.URL, "password": c.Password, "rclone_config": repo.Rclone})
}

func removeFile(p string) error { return os.Remove(p) }
