package panel

// File sync (文件同步): push a directory to another managed host with rsync over SSH, on a
// schedule. A sync is an ordinary scheduled job whose script starts with a "# berth-sync:"
// line, so it shows up (and alerts on failure) like any other job; the target lets the
// source's key in only for an rsync receiver confined to the target directory.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/wangsirco/berth/internal/agent"
	"github.com/wangsirco/berth/internal/change"
	"github.com/wangsirco/berth/internal/errs"
	"github.com/wangsirco/berth/internal/pipeline"
)

const syncMarker = "# berth-sync: "

type syncSpec struct {
	Name       string `json:"name"`
	Path       string `json:"path"`        // on this host
	Target     string `json:"target"`      // a managed host
	TargetPath string `json:"target_path"` // there
	Cron       string `json:"cron"`
	Delete     bool   `json:"delete,omitempty"` // remove there what is gone here
	Address    string `json:"address,omitempty"`
	Port       int    `json:"port,omitempty"`
}

type syncView struct {
	syncSpec
	JobID   int64       `json:"job_id"`
	Enabled bool        `json:"enabled"`
	LastRun *jobRunView `json:"last_run,omitempty"`
}

var syncNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,40}$`)

func syncTag(host, name string) string { return "berth-sync-" + strings.ToLower(host) + "." + name }

func syncScript(s syncSpec) string {
	b, _ := json.Marshal(s)
	flags := "-a --partial --numeric-ids"
	if s.Delete {
		flags += " --delete"
	}
	ssh := fmt.Sprintf("ssh -i %[1]s/id_ed25519 -p %[2]d -o BatchMode=yes -o ConnectTimeout=15 -o StrictHostKeyChecking=accept-new -o UserKnownHostsFile=%[1]s/known_hosts", agent.SyncDir, s.Port)
	return syncMarker + string(b) + "\nset -e\nrsync " + flags + " -e '" + ssh + "' " + strings.TrimSuffix(s.Path, "/") + "/ root@" + s.Address + ":/\necho synced " + s.Path + " to " + s.Target + ":" + s.TargetPath + "\n"
}

func parseSync(j cronJob) (syncSpec, bool) {
	if j.Action.Type != "script" || !strings.HasPrefix(j.Action.Script, syncMarker) {
		return syncSpec{}, false
	}
	line, _, _ := strings.Cut(strings.TrimPrefix(j.Action.Script, syncMarker), "\n")
	var s syncSpec
	return s, json.Unmarshal([]byte(line), &s) == nil
}

func (a *App) syncs(host string) []syncView {
	out := []syncView{}
	for _, j := range a.jobs(host) {
		if s, ok := parseSync(j); ok {
			out = append(out, syncView{syncSpec: s, JobID: j.ID, Enabled: j.Enabled, LastRun: a.lastRun(j.ID)})
		}
	}
	return out
}

// syncTargetAddr: how the source reaches the target (where its agent connects from) and its SSH port.
func (a *App) syncTargetAddr(env change.Env, target string) (string, int) {
	addr := ""
	if h, err := a.remoteHost(target); err == nil {
		addr = h.Addr
	}
	var ssh struct {
		Effective struct {
			Port int `json:"port"`
		} `json:"effective"`
	}
	a.call(env.Ctx, target, "GET", "/v1/system/ssh", nil, &ssh)
	return addr, max(ssh.Effective.Port, 0)
}

func (a *App) checkSync(env change.Env, s *syncSpec) error {
	if !syncNameRe.MatchString(s.Name) {
		return errs.New(400, "sync.bad_name")
	}
	if !agent.ValidSyncDir(strings.TrimSuffix(s.Path, "/")) {
		return errs.New(400, "sync.bad_dir", "dir", s.Path)
	}
	if !agent.ValidSyncDir(s.TargetPath) {
		return errs.New(400, "sync.bad_dir", "dir", s.TargetPath)
	}
	if s.Target == "" || s.Target == env.Host {
		return errs.New(400, "sync.same_host")
	}
	if _, err := a.agent(s.Target); err != nil {
		return errs.New(409, "sync.target_offline", "host", s.Target)
	}
	if s.Cron == "" {
		s.Cron = "0 3 * * *"
	}
	if _, err := pipeline.ParseCron(s.Cron); err != nil {
		return pipeline.SpecErr(err)
	}
	addr, port := a.syncTargetAddr(env, s.Target)
	if s.Address == "" {
		s.Address = addr
	}
	if s.Port == 0 {
		s.Port = port
	}
	if s.Port == 0 {
		s.Port = 22
	}
	if s.Address == "" || strings.ContainsAny(s.Address, " '\"@:;|&$`") {
		return errs.New(400, "sync.no_address", "host", s.Target)
	}
	return nil
}

func init() {
	routeHooks = append(routeHooks, func(a *App, mux *http.ServeMux) {
		mux.HandleFunc("GET /api/v1/hosts/{host}/syncs", a.authed("system.view", func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, 200, map[string]any{"items": a.syncs(r.PathValue("host"))})
		}))
	})
	change.Register(change.Kind{
		Name: "sync.set", Perm: "system.manage", Scope: "system:write", Title: "kind.sync.set", CLI: "berth sync set {{.name}} {{.path}} {{.target}}:{{.target_path}}",
		Params: change.ParamSchema{Props: map[string]change.Prop{
			"name": {Type: "string"}, "path": {Type: "string", Desc: "directory on this host"}, "target": {Type: "string", Desc: "another managed host"},
			"target_path": {Type: "string"}, "cron": {Type: "string", Desc: "default 0 3 * * * (03:00 every day)"},
			"delete": {Type: "boolean", Desc: "also delete there what was deleted here"},
			"address": {Type: "string", Desc: "how this host reaches the target (default: where its agent connects from)"}, "port": {Type: "integer", Desc: "the target's SSH port"}},
			Required: []string{"name", "path", "target", "target_path"}},
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			var s syncSpec
			json.Unmarshal(raw, &s)
			if err := svc(env).checkSync(env, &s); err != nil {
				return change.Plan{}, err
			}
			risk := change.RiskMedium
			sum := "plan.sync.set"
			if s.Delete {
				risk, sum = change.RiskHigh, "plan.sync.set_delete"
			}
			return change.Plan{Summary: sum, SummaryP: map[string]any{"path": s.Path, "target": s.Target, "target_path": s.TargetPath, "cron": s.Cron}, Risk: risk, Undoable: true,
				Commands: []string{
					"(" + env.Host + ") make a sync key in " + agent.SyncDir + " if there is none; install rsync if missing",
					"(" + s.Target + ") mkdir -p " + s.TargetPath + "; let that key in for `berth sync-serve " + s.TargetPath + "` only (no shell, cannot read)",
					"(" + env.Host + ") scheduled job \"" + s.Cron + "\": " + strings.Split(syncScript(s), "\n")[2],
					"run it once now",
				},
				Affected: []change.Object{{Type: "path", ID: s.Path, Host: env.Host}, {Type: "path", ID: s.TargetPath, Host: s.Target}}}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			a := svc(env)
			var s syncSpec
			json.Unmarshal(raw, &s)
			if err := a.checkSync(env, &s); err != nil {
				return nil, nil, err
			}
			for _, x := range a.syncs(env.Host) {
				if x.Name == s.Name {
					return nil, nil, errs.New(409, "sync.exists", "name", s.Name)
				}
			}
			var key struct{ Pub string }
			if err := a.call(env.Ctx, env.Host, "POST", "/v1/sync/key", nil, &key); err != nil {
				return nil, nil, err
			}
			env.Log("sync key on %s: %s", env.Host, key.Pub)
			if err := a.call(env.Ctx, s.Target, "POST", "/v1/sync/authorize", map[string]any{"pub": key.Pub, "dir": s.TargetPath, "tag": syncTag(env.Host, s.Name)}, nil); err != nil {
				return nil, nil, err
			}
			env.Log("%s lets it in for %s only", s.Target, s.TargetPath)
			spec, _ := json.Marshal(jobSpec{Name: "sync " + s.Name, Cron: s.Cron, Action: jobAction{Type: "script", Script: syncScript(s), Timeout: 6 * 3600}})
			byID, _ := strconv.ParseInt(env.ActorID, 10, 64)
			res, err := a.db.Exec(`INSERT INTO cron_jobs(host, spec, enabled, created_by, created_by_id, created_at) VALUES(?,?,1,?,?,?)`,
				env.Host, string(spec), env.ActorName, byID, time.Now().Unix())
			if err != nil {
				return nil, nil, err
			}
			id, _ := res.LastInsertId()
			u, _ := json.Marshal(map[string]int64{"job": id})
			undo := &change.UndoRecipe{Kind: "sync.delete", Params: u}
			// the first run, to show it works
			j, _ := a.job(id)
			rid := a.startJob(j, "manual")
			for i := 0; i < 600; i++ {
				time.Sleep(time.Second)
				var st, out string
				a.db.QueryRow(`SELECT status, COALESCE(output,'') FROM cron_runs WHERE id=?`, rid).Scan(&st, &out)
				if st == "running" {
					continue
				}
				for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
					env.Log("%s", l)
				}
				if st != "ok" { // leave nothing half set up behind
					a.db.Exec(`DELETE FROM cron_jobs WHERE id=?`, id)
					a.call(env.Ctx, s.Target, "POST", "/v1/sync/authorize", map[string]any{"tag": syncTag(env.Host, s.Name), "remove": true}, nil)
					return nil, nil, errs.New(500, "sync.first_run_failed", "detail", lastLines(out, 3))
				}
				break
			}
			return map[string]int64{"job": id}, undo, nil
		},
	})
	change.Register(change.Kind{
		Name: "sync.delete", Perm: "system.manage", Scope: "system:write", Title: "kind.sync.delete", CLI: "berth sync delete {{.job}}",
		Params: change.ParamSchema{Props: map[string]change.Prop{"job": {Type: "integer"}}, Required: []string{"job"}},
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			var p struct{ Job int64 }
			json.Unmarshal(raw, &p)
			j, err := svc(env).job(p.Job)
			s, ok := parseSync(j)
			if err != nil || !ok {
				return change.Plan{}, errs.New(404, "job.not_found")
			}
			return change.Plan{Summary: "plan.sync.delete", SummaryP: map[string]any{"name": s.Name, "target": s.Target}, Risk: change.RiskLow, Undoable: true,
				Commands: []string{"delete the scheduled job", "(" + s.Target + ") remove the sync key (files already copied there stay)"}}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			a := svc(env)
			var p struct{ Job int64 }
			json.Unmarshal(raw, &p)
			j, err := a.job(p.Job)
			s, ok := parseSync(j)
			if err != nil || !ok {
				return nil, nil, errs.New(404, "job.not_found")
			}
			if err := a.call(env.Ctx, s.Target, "POST", "/v1/sync/authorize", map[string]any{"tag": syncTag(j.Host, s.Name), "remove": true}, nil); err != nil {
				env.Log("could not remove the key on %s: %v", s.Target, err)
			}
			a.db.Exec(`DELETE FROM cron_jobs WHERE id=?`, p.Job)
			u, _ := json.Marshal(s)
			return nil, &change.UndoRecipe{Kind: "sync.set", Params: u}, nil
		},
	})
}
