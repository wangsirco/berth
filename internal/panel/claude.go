package panel

// Claude Code workspaces (方案 §5.9): a dev workspace with Claude Code in it.
//
//   - Talk to it in the panel's terminal (a resident session: closing the page does not stop it).
//   - Hand it a task: it runs unattended (`claude -p`) one after another per workspace, its
//     progress streams into the panel, and the person who sent it gets a notification when it
//     is done or asks something back (they answer by replying; the conversation continues).
//   - Every task's changes are kept as a before/after snapshot (a shadow repository in the
//     workspace's root-only state, never the project's own .git): the panel shows the diff and
//     takes it back (and forth again) as an ordinary undoable change.
//   - It signs in the way the person chooses: their own login inside the workspace, or an API
//     key / long-lived token the panel keeps sealed. Berth ships no key of its own.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/wangsirco/berth/internal/change"
	"github.com/wangsirco/berth/internal/errs"
	"github.com/wangsirco/berth/internal/panel/notify"
)

type claudeTask struct {
	ID         int64         `json:"id"`
	Workspace  string        `json:"workspace"`
	Prompt     string        `json:"prompt"`
	Mode       string        `json:"mode"`
	Parent     int64         `json:"parent,omitempty"`
	Status     string        `json:"status"` // queued | running | done | asking | failed | canceled
	Session    string        `json:"session,omitempty"`
	CreatedBy  string        `json:"created_by"`
	CreatedAt  int64         `json:"created_at"`
	StartedAt  int64         `json:"started_at,omitempty"`
	FinishedAt int64         `json:"finished_at,omitempty"`
	Log        string        `json:"log,omitempty"`
	Result     *claudeResult `json:"result,omitempty"`
	Before     string        `json:"before,omitempty"`
	After      string        `json:"after,omitempty"`
	Reverted   bool          `json:"reverted,omitempty"`
	creatorID  int64
	host       string
}

type claudeResult struct {
	Text       string     `json:"text"`
	IsError    bool       `json:"is_error,omitempty"`
	Subtype    string     `json:"subtype,omitempty"`
	CostUSD    float64    `json:"cost_usd"`
	Turns      int        `json:"turns"`
	DurationMS int64      `json:"duration_ms"`
	Files      []fileStat `json:"files"`
	DiffCut    bool       `json:"diff_cut,omitempty"`
	Error      *errs.E    `json:"error,omitempty"`
}

type fileStat struct {
	Path   string `json:"path"`
	Add    int    `json:"add"`
	Del    int    `json:"del"`
	Binary bool   `json:"binary,omitempty"`
}

// claudeSystem tells Claude how it is being run.
const claudeSystem = "You are running unattended: a task sent from the Berth server panel, in a dev container. " +
	"Nobody can answer questions while you work. Do the task as fully as you can on your own. " +
	"If you cannot go on without a decision from the person, stop and end your final message with that question. " +
	"Finish with a short summary of what you changed and anything left to do."

var claudeTokenRe = regexp.MustCompile(`^[A-Za-z0-9_\-.:/]{8,400}$`)

// --- storage ----------------------------------------------------------------------

const claudeTaskCols = `id, host, ws, prompt, mode, parent, status, session, created_by, created_by_id, created_at, started_at, finished_at, result, snap_before, snap_after, reverted`

func scanClaudeTask(sc interface{ Scan(...any) error }) (claudeTask, error) {
	var t claudeTask
	var res string
	var rev int
	err := sc.Scan(&t.ID, &t.host, &t.Workspace, &t.Prompt, &t.Mode, &t.Parent, &t.Status, &t.Session, &t.CreatedBy, &t.creatorID,
		&t.CreatedAt, &t.StartedAt, &t.FinishedAt, &res, &t.Before, &t.After, &rev)
	if res != "" {
		t.Result = &claudeResult{}
		json.Unmarshal([]byte(res), t.Result)
	}
	t.Reverted = rev == 1
	return t, err
}

func (a *App) claudeTask(host, ws string, id int64) (claudeTask, error) {
	t, err := scanClaudeTask(a.db.QueryRow(`SELECT `+claudeTaskCols+` FROM claude_tasks WHERE host=? AND ws=? AND id=?`, host, ws, id))
	if err != nil {
		return t, errs.New(404, "dev.task_not_found", "id", id)
	}
	a.db.QueryRow(`SELECT log FROM claude_tasks WHERE id=?`, id).Scan(&t.Log)
	return t, nil
}

func (a *App) claudeTasks(host, ws string, limit int) []claudeTask {
	out := []claudeTask{}
	rows, err := a.db.Query(`SELECT `+claudeTaskCols+` FROM claude_tasks WHERE host=? AND ws=? ORDER BY id DESC LIMIT ?`, host, ws, limit)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		if t, err := scanClaudeTask(rows); err == nil {
			out = append(out, t)
		}
	}
	return out
}

// claudeEnv: the sealed token, as the environment Claude Code reads.
func (a *App) claudeEnv(host, name string) map[string]string {
	var enc []byte
	a.db.QueryRow(`SELECT claude_enc FROM dev_workspaces WHERE host=? AND name=?`, host, name).Scan(&enc)
	if len(enc) == 0 {
		return nil
	}
	b, err := a.open(enc)
	if err != nil {
		return nil
	}
	var m map[string]string
	json.Unmarshal(b, &m)
	return m
}

func (a *App) publishClaude(host string, t claudeTask) {
	t.Log = ""
	a.hub.publish(event{Type: "dev.task", Data: map[string]any{"host": host, "task": t}, visible: func(r *http.Request) bool {
		u := currentUser(r)
		return u != nil && u.Can("dev.use")
	}})
}

// --- the runner: one task at a time per workspace -------------------------------

type claudeRunner struct {
	mu       sync.Mutex
	busy     map[string]bool
	cancel   map[int64]context.CancelFunc // stops a running task (asks the agent; the run then ends with its result)
	canceled map[int64]bool
}

var claudeRun = &claudeRunner{busy: map[string]bool{}, cancel: map[int64]context.CancelFunc{}, canceled: map[int64]bool{}}

// claudeRecover: tasks that were running when the panel stopped did not finish.
func (a *App) claudeRecover() {
	res, _ := json.Marshal(claudeResult{Error: errs.New(500, "tasks.interrupted")})
	a.db.Exec(`UPDATE claude_tasks SET status='failed', finished_at=?, result=? WHERE status='running'`, time.Now().Unix(), string(res))
	go func() {
		time.Sleep(15 * time.Second) // agents connect first
		rows, err := a.db.Query(`SELECT DISTINCT host, ws FROM claude_tasks WHERE status='queued'`)
		if err != nil {
			return
		}
		var keys [][2]string
		for rows.Next() {
			var h, w string
			rows.Scan(&h, &w)
			keys = append(keys, [2]string{h, w})
		}
		rows.Close()
		for _, k := range keys {
			a.claudeKick(k[0], k[1])
		}
	}()
}

func (a *App) claudeKick(host, ws string) {
	key := host + "/" + ws
	claudeRun.mu.Lock()
	if claudeRun.busy[key] {
		claudeRun.mu.Unlock()
		return
	}
	claudeRun.busy[key] = true
	claudeRun.mu.Unlock()
	go func() {
		defer func() {
			claudeRun.mu.Lock()
			delete(claudeRun.busy, key)
			claudeRun.mu.Unlock()
		}()
		for {
			var id int64
			if a.db.QueryRow(`SELECT id FROM claude_tasks WHERE host=? AND ws=? AND status='queued' ORDER BY id LIMIT 1`, host, ws).Scan(&id) != nil {
				return
			}
			a.claudeRunOne(host, ws, id)
		}
	}()
}

func (a *App) claudeRunOne(host, ws string, id int64) {
	t, err := a.claudeTask(host, ws, id)
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Hour)
	defer cancel()
	claudeRun.mu.Lock()
	claudeRun.cancel[id] = func() {
		claudeRun.mu.Lock()
		claudeRun.canceled[id] = true
		claudeRun.mu.Unlock()
		// the agent stops it in the container; the stream then ends normally with what it changed
		sctx, scancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer scancel()
		if a.call(sctx, host, "POST", "/v1/dev/"+ws+"/claude/stop", nil, nil) != nil {
			cancel()
		}
	}
	claudeRun.mu.Unlock()
	defer func() {
		claudeRun.mu.Lock()
		delete(claudeRun.cancel, id)
		delete(claudeRun.canceled, id)
		claudeRun.mu.Unlock()
	}()
	t.Status, t.StartedAt = "running", time.Now().Unix()
	if r, _ := a.db.Exec(`UPDATE claude_tasks SET status='running', started_at=? WHERE id=? AND status='queued'`, t.StartedAt, id); r != nil {
		if n, _ := r.RowsAffected(); n == 0 { // canceled meanwhile
			return
		}
	}
	a.publishClaude(host, t)

	var logMu sync.Mutex
	var logb strings.Builder
	lastFlush := time.Now()
	flush := func() {
		logMu.Lock()
		l := logb.String()
		logMu.Unlock()
		a.db.Exec(`UPDATE claude_tasks SET log=? WHERE id=?`, logTail(l, 200_000), id)
	}
	env := change.Env{Ctx: ctx, Host: host, Svc: a, Log: func(f string, args ...any) {
		logMu.Lock()
		fmt.Fprintf(&logb, time.Now().Format("15:04:05 ")+f+"\n", args...)
		logMu.Unlock()
		if time.Since(lastFlush) > time.Second {
			lastFlush = time.Now()
			flush()
		}
	}}

	res := &claudeResult{}
	var out struct {
		SessionID  string     `json:"session_id"`
		Result     string     `json:"result"`
		IsError    bool       `json:"is_error"`
		Subtype    string     `json:"subtype"`
		CostUSD    float64    `json:"cost_usd"`
		Turns      int        `json:"turns"`
		DurationMS int64      `json:"duration_ms"`
		Before     string     `json:"before"`
		After      string     `json:"after"`
		Files      []fileStat `json:"files"`
		DiffCut    bool       `json:"diff_cut"`
	}
	runErr := func() error {
		s, err := a.devWorkspace(host, ws)
		if err != nil {
			return err
		}
		if !s.Claude {
			return errs.New(409, "dev.claude_off", "name", ws)
		}
		var st struct {
			User string `json:"user"`
		}
		a.call(ctx, host, "GET", "/v1/dev/"+ws+"/status", nil, &st)
		resume := ""
		if t.Parent != 0 {
			if p, err := a.claudeTask(host, ws, t.Parent); err == nil {
				resume = p.Session
			}
		}
		in := map[string]any{"dir": s.Dir, "folder": s.Env.Folder, "user": firstNonEmpty(st.User, s.Env.User, "root"), "prompt": t.Prompt,
			"resume": resume, "mode": t.Mode, "system": claudeSystem, "env": a.claudeEnv(host, ws)}
		return a.streamAgent(env, host, "/v1/dev/"+ws+"/claude/run", in, &out)
	}()
	flush()
	res.Text, res.IsError, res.Subtype = out.Result, out.IsError, out.Subtype
	res.CostUSD, res.Turns, res.DurationMS, res.Files, res.DiffCut = out.CostUSD, out.Turns, out.DurationMS, out.Files, out.DiffCut
	if res.Files == nil {
		res.Files = []fileStat{}
	}
	claudeRun.mu.Lock()
	stopped := claudeRun.canceled[id]
	claudeRun.mu.Unlock()
	status := "done"
	switch {
	case stopped || (runErr != nil && ctx.Err() == context.Canceled):
		status = "canceled"
	case runErr != nil:
		status, res.Error = "failed", errs.From(runErr)
	case out.IsError:
		status = "failed"
	case claudeAsks(out.Result):
		status = "asking"
	}
	b, _ := json.Marshal(res)
	a.db.Exec(`UPDATE claude_tasks SET status=?, finished_at=?, session=?, result=?, snap_before=?, snap_after=? WHERE id=?`,
		status, time.Now().Unix(), out.SessionID, string(b), out.Before, out.After, id)
	if t2, err := a.claudeTask(host, ws, id); err == nil {
		a.publishClaude(host, t2)
		if status != "canceled" {
			a.notifyClaude(t2)
		}
	}
}

// claudeAsks: the final message ends with a question for the person.
func claudeAsks(s string) bool {
	s = strings.TrimSpace(s)
	s = strings.TrimRight(s, "*_` \n")
	return strings.HasSuffix(s, "?") || strings.HasSuffix(s, "？")
}

// notifyClaude tells the person who sent the task (their channels for "开发").
func (a *App) notifyClaude(t claudeTask) {
	chans, err := a.channelsFor("dev", t.host)
	if err != nil {
		return
	}
	d := a.dispatcher()
	d.mu.Lock()
	n := 0
	for _, c := range chans {
		if c.UserID != t.creatorID {
			continue
		}
		key := "dev.task_" + t.Status
		files := 0
		if t.Result != nil {
			files = len(t.Result.Files)
		}
		title := T(c.locale, "notify."+key, map[string]any{"name": t.Workspace})
		body := ""
		if t.Result != nil {
			body = oneLineText(t.Result.Text, 180)
			if t.Result.Error != nil {
				body = T(c.locale, t.Result.Error.Code, t.Result.Error.Params)
			}
		}
		if files > 0 {
			body = strings.TrimSpace(body + "\n" + T(c.locale, "notify.dev.task_files", map[string]any{"n": files}))
		}
		sev := "info"
		if t.Status == "failed" {
			sev = "warn"
		}
		link := a.publicURL() + "/" + t.host + "/dev?ws=" + url.QueryEscape(t.Workspace) + "&task=" + strconv.FormatInt(t.ID, 10)
		m := notify.Message{Title: title, Body: body, Severity: sev, Tag: "berth-dev-task-" + strconv.FormatInt(t.ID, 10), URL: link}
		d.pending[c.id] = append(d.pending[c.id], outMsg{channel: c.id, cfg: c.cfg, msg: m, cat: "dev"})
		n++
	}
	if n > 0 && !d.started {
		d.started = true
		go a.flush(d)
	}
	d.mu.Unlock()
}

func oneLineText(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) > n {
		r := []rune(s)
		return string(r[:n]) + "…"
	}
	return s
}

// --- kinds ------------------------------------------------------------------------

func (a *App) claudeWorkspace(host, name string) (devSpec, error) {
	s, err := a.devWorkspace(host, name)
	if err != nil {
		return s, err
	}
	if !s.Claude {
		return s, errs.New(409, "dev.claude_off", "name", name)
	}
	return s, nil
}

func init() {
	routeHooks = append(routeHooks, func(a *App, mux *http.ServeMux) {
		mux.HandleFunc("GET /api/v1/hosts/{host}/dev/{name}/claude", a.authed("dev.use", a.handleClaude))
		mux.HandleFunc("GET /api/v1/hosts/{host}/dev/{name}/claude/tasks/{id}", a.authed("dev.use", func(w http.ResponseWriter, r *http.Request) {
			id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
			t, err := a.claudeTask(r.PathValue("host"), r.PathValue("name"), id)
			if err != nil {
				a.fail(w, r, err)
				return
			}
			writeJSON(w, 200, t)
		}))
		mux.HandleFunc("GET /api/v1/hosts/{host}/dev/{name}/claude/tasks/{id}/diff", a.authed("dev.use", func(w http.ResponseWriter, r *http.Request) {
			host, name := r.PathValue("host"), r.PathValue("name")
			id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
			t, err := a.claudeTask(host, name, id)
			if err == nil && (t.Before == "" || t.After == "") {
				err = errs.New(404, "dev.no_diff")
			}
			s, err2 := a.devWorkspace(host, name)
			if err == nil {
				err = err2
			}
			if err != nil {
				a.fail(w, r, err)
				return
			}
			var out map[string]any
			q := "dir=" + url.QueryEscape(s.Dir) + "&from=" + t.Before + "&to=" + t.After
			if err := a.call(r.Context(), host, "GET", "/v1/dev/"+name+"/claude/diff?"+q, nil, &out); err != nil {
				a.fail(w, r, err)
				return
			}
			writeJSON(w, 200, out)
		}))
	})

	change.Register(change.Kind{
		Name: "dev.task", Perm: "dev.use", Scope: "dev:write", Title: "kind.dev.task",
		Params: change.ParamSchema{Props: map[string]change.Prop{
			"name":     {Type: "string", Desc: "workspace name"},
			"prompt":   {Type: "string", Desc: "what Claude Code should do"},
			"mode":     {Type: "string", Enum: []string{"auto", "edits"}, Desc: "auto: may run anything inside the workspace container (default: the workspace's setting); edits: change files only"},
			"reply_to": {Type: "integer", Desc: "a finished task to continue (its conversation): this prompt is the answer"},
		}, Required: []string{"name", "prompt"}},
		CLI: "berth dev task {{.name}}",
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			a := svc(env)
			p, s, err := a.taskParams(env, raw)
			if err != nil {
				return change.Plan{}, err
			}
			var queued int
			a.db.QueryRow(`SELECT COUNT(*) FROM claude_tasks WHERE host=? AND ws=? AND status IN ('queued','running')`, env.Host, s.Name).Scan(&queued)
			pl := change.Plan{Summary: "plan.dev.task", SummaryP: map[string]any{"name": s.Name, "prompt": oneLineText(p.Prompt, 60)},
				Risk: change.RiskLow, Affected: []change.Object{devObj(s.Name, env.Host)},
				Commands: []string{"claude -p --output-format stream-json " + map[string]string{"auto": "--dangerously-skip-permissions", "edits": "--permission-mode acceptEdits"}[p.Mode] + "   # in " + devContainer(s.Name)}}
			if queued > 0 {
				pl.Commands = append(pl.Commands, fmt.Sprintf("# waits for %d task(s) ahead of it", queued))
			}
			return pl, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			a := svc(env)
			p, s, err := a.taskParams(env, raw)
			if err != nil {
				return nil, nil, err
			}
			r, err := a.db.Exec(`INSERT INTO claude_tasks(host, ws, prompt, mode, parent, status, created_by, created_by_id, created_at) VALUES(?,?,?,?,?,?,?,?,?)`,
				env.Host, s.Name, p.Prompt, p.Mode, p.ReplyTo, "queued", env.ActorName, actorUID(env), time.Now().Unix())
			if err != nil {
				return nil, nil, err
			}
			id, _ := r.LastInsertId()
			if p.ReplyTo != 0 { // answered: no longer waiting
				a.db.Exec(`UPDATE claude_tasks SET status='done' WHERE id=? AND status='asking'`, p.ReplyTo)
			}
			if t, err := a.claudeTask(env.Host, s.Name, id); err == nil {
				a.publishClaude(env.Host, t)
			}
			a.claudeKick(env.Host, s.Name)
			return map[string]any{"task": id, "name": s.Name}, nil, nil
		},
	})

	taskRef := change.ParamSchema{Props: map[string]change.Prop{"name": {Type: "string"}, "task": {Type: "integer"}}, Required: []string{"name", "task"}}
	change.Register(change.Kind{
		Name: "dev.task_cancel", Perm: "dev.use", Scope: "dev:write", Title: "kind.dev.task_cancel", Params: taskRef,
		CLI: "berth dev task-cancel {{.name}} {{.task}}",
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			t, err := svc(env).taskRef(env, raw)
			if err != nil {
				return change.Plan{}, err
			}
			if t.Status != "queued" && t.Status != "running" {
				return change.Plan{}, errs.New(409, "dev.task_finished")
			}
			return change.Plan{Summary: "plan.dev.task_cancel", SummaryP: map[string]any{"name": t.Workspace, "id": t.ID}, Risk: change.RiskLow,
				Affected: []change.Object{devObj(t.Workspace, env.Host)}}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			a := svc(env)
			t, err := a.taskRef(env, raw)
			if err != nil {
				return nil, nil, err
			}
			if r, _ := a.db.Exec(`UPDATE claude_tasks SET status='canceled', finished_at=? WHERE id=? AND status='queued'`, time.Now().Unix(), t.ID); r != nil {
				if n, _ := r.RowsAffected(); n == 1 {
					t.Status = "canceled"
					a.publishClaude(env.Host, t)
					return map[string]any{"task": t.ID}, nil, nil
				}
			}
			claudeRun.mu.Lock()
			cancel := claudeRun.cancel[t.ID]
			claudeRun.mu.Unlock()
			if cancel != nil {
				cancel() // the runner records it as canceled, with what it changed so far
			}
			return map[string]any{"task": t.ID}, nil, nil
		},
	})

	revertProps := map[string]change.Prop{"name": {Type: "string"}, "task": {Type: "integer"}, "reapply": {Type: "boolean", Desc: "apply the task's changes again (after taking them back)"}}
	change.Register(change.Kind{
		Name: "dev.task_revert", Perm: "dev.use", Scope: "dev:write", Title: "kind.dev.task_revert",
		Params: change.ParamSchema{Props: revertProps, Required: []string{"name", "task"}}, CLI: "berth dev task-revert {{.name}} {{.task}}",
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			a := svc(env)
			t, err := a.taskRef(env, raw)
			if err != nil {
				return change.Plan{}, err
			}
			var p struct {
				Reapply bool `json:"reapply"`
			}
			json.Unmarshal(raw, &p)
			if t.Before == "" || t.After == "" || t.Before == t.After {
				return change.Plan{}, errs.New(409, "dev.no_diff")
			}
			if t.Reverted == !p.Reapply { // already taken back (or still applied)
				return change.Plan{}, errs.New(409, map[bool]string{false: "dev.task_reverted", true: "dev.task_applied"}[p.Reapply])
			}
			pl := change.Plan{Summary: map[bool]string{false: "plan.dev.task_revert", true: "plan.dev.task_reapply"}[p.Reapply],
				SummaryP: map[string]any{"name": t.Workspace, "id": t.ID}, Risk: change.RiskMedium, Undoable: true,
				Affected: []change.Object{devObj(t.Workspace, env.Host)}}
			if t.Result != nil {
				for i, f := range t.Result.Files {
					if i == 20 {
						pl.Commands = append(pl.Commands, fmt.Sprintf("# … %d more", len(t.Result.Files)-20))
						break
					}
					pl.Commands = append(pl.Commands, fmt.Sprintf("%s  (+%d −%d)", f.Path, f.Add, f.Del))
				}
			}
			return pl, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			a := svc(env)
			t, err := a.taskRef(env, raw)
			if err != nil {
				return nil, nil, err
			}
			var p struct {
				Reapply bool `json:"reapply"`
			}
			json.Unmarshal(raw, &p)
			s, err := a.devWorkspace(env.Host, t.Workspace)
			if err != nil {
				return nil, nil, err
			}
			if err := a.call(env.Ctx, env.Host, "POST", "/v1/dev/"+s.Name+"/claude/patch",
				map[string]any{"dir": s.Dir, "from": t.Before, "to": t.After, "reverse": !p.Reapply}, nil); err != nil {
				return nil, nil, err
			}
			a.db.Exec(`UPDATE claude_tasks SET reverted=? WHERE id=?`, map[bool]int{false: 1, true: 0}[p.Reapply], t.ID)
			if t2, err := a.claudeTask(env.Host, s.Name, t.ID); err == nil {
				a.publishClaude(env.Host, t2)
			}
			undo := &change.UndoRecipe{Kind: "dev.task_revert", Params: mustJSON(map[string]any{"name": s.Name, "task": t.ID, "reapply": !p.Reapply})}
			return map[string]any{"task": t.ID}, undo, nil
		},
	})

	change.Register(change.Kind{
		Name: "dev.claude_auth", Perm: "dev.use", Scope: "dev:write", Title: "kind.dev.claude_auth",
		Params: change.ParamSchema{Props: map[string]change.Prop{
			"name":   {Type: "string"},
			"method": {Type: "string", Enum: []string{"login", "api_key", "token"}, Desc: "login: sign in inside the workspace (claude, /login); api_key: an Anthropic API key; token: from `claude setup-token`"},
			"secret": {Type: "string", Desc: "the key or token (kept sealed; never shown again)"},
		}, Required: []string{"name", "method"}},
		Redact: []string{"secret"}, CLI: "berth dev claude-auth {{.name}} --method {{.method}}",
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			a := svc(env)
			s, m, _, err := a.authParams(env, raw)
			if err != nil {
				return change.Plan{}, err
			}
			return change.Plan{Summary: "plan.dev.claude_auth." + m, SummaryP: map[string]any{"name": s.Name}, Risk: change.RiskLow,
				Affected: []change.Object{devObj(s.Name, env.Host)}, Commands: []string{"# sealed in the panel; inside the workspace only its user can read it"}}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			a := svc(env)
			s, m, secret, err := a.authParams(env, raw)
			if err != nil {
				return nil, nil, err
			}
			var enc []byte
			if m != "login" {
				key := map[string]string{"api_key": "ANTHROPIC_API_KEY", "token": "CLAUDE_CODE_OAUTH_TOKEN"}[m]
				b, _ := json.Marshal(map[string]string{key: secret})
				if enc, err = a.seal(b); err != nil {
					return nil, nil, err
				}
			}
			if _, err := a.db.Exec(`UPDATE dev_workspaces SET claude_enc=? WHERE host=? AND name=?`, enc, env.Host, s.Name); err != nil {
				return nil, nil, err
			}
			s.ClaudeAuth = m
			a.saveDev(env.Host, s)
			if err := a.devState(env, s, false, false); err != nil {
				return nil, nil, err
			}
			if err := a.call(env.Ctx, env.Host, "POST", "/v1/dev/"+s.Name+"/claude/sync", nil, nil); err != nil {
				env.Log("sync: %v (applies on the next start)", err)
			}
			return map[string]any{"name": s.Name, "method": m}, nil, nil
		},
	})

	change.Register(change.Kind{
		Name: "dev.claude_update", Perm: "dev.use", Scope: "dev:write", Title: "kind.dev.claude_update",
		Params: change.ParamSchema{Props: map[string]change.Prop{}}, CLI: "berth dev claude-update", Heavy: true, MemMB: 64,
		Plan: func(env change.Env, _ json.RawMessage) (change.Plan, error) {
			return change.Plan{Summary: "plan.dev.claude_update", Risk: change.RiskLow,
				Commands: []string{"# download the newest Claude Code from downloads.claude.ai (checksum verified); workspaces use it from their next start"}}, nil
		},
		Apply: func(env change.Env, _ json.RawMessage) (any, *change.UndoRecipe, error) {
			return nil, nil, agentOp(env, "/v1/dev/claude/install", map[string]bool{"update": true})
		},
	})
}

type taskIn struct {
	Name    string `json:"name"`
	Prompt  string `json:"prompt"`
	Mode    string `json:"mode"`
	ReplyTo int64  `json:"reply_to"`
}

func (a *App) taskParams(env change.Env, raw json.RawMessage) (taskIn, devSpec, error) {
	var p taskIn
	json.Unmarshal(raw, &p)
	p.Prompt = strings.TrimSpace(p.Prompt)
	s, err := a.claudeWorkspace(env.Host, p.Name)
	if err != nil {
		return p, s, err
	}
	if p.Prompt == "" || len(p.Prompt) > 100_000 {
		return p, s, errs.New(400, "dev.bad_prompt")
	}
	if p.Mode == "" {
		p.Mode = firstNonEmpty(s.ClaudeMode, "auto")
	}
	if p.Mode != "auto" && p.Mode != "edits" {
		return p, s, errs.New(400, "params.enum", "name", "mode", "allowed", "auto, edits")
	}
	if p.ReplyTo != 0 {
		t, err := a.claudeTask(env.Host, s.Name, p.ReplyTo)
		if err != nil {
			return p, s, err
		}
		if t.Session == "" {
			return p, s, errs.New(409, "dev.task_no_session")
		}
	}
	return p, s, nil
}

func (a *App) taskRef(env change.Env, raw json.RawMessage) (claudeTask, error) {
	var p struct {
		Name string `json:"name"`
		Task int64  `json:"task"`
	}
	json.Unmarshal(raw, &p)
	return a.claudeTask(env.Host, p.Name, p.Task)
}

func (a *App) authParams(env change.Env, raw json.RawMessage) (devSpec, string, string, error) {
	var p struct {
		Name   string `json:"name"`
		Method string `json:"method"`
		Secret string `json:"secret"`
	}
	json.Unmarshal(raw, &p)
	s, err := a.claudeWorkspace(env.Host, p.Name)
	if err != nil {
		return s, "", "", err
	}
	p.Secret = strings.TrimSpace(p.Secret)
	switch p.Method {
	case "login":
	case "api_key", "token":
		if !claudeTokenRe.MatchString(p.Secret) {
			return s, "", "", errs.New(400, "dev.bad_token")
		}
	default:
		return s, "", "", errs.New(400, "params.enum", "name", "method", "allowed", "login, api_key, token")
	}
	return s, p.Method, p.Secret, nil
}

// GET /api/v1/hosts/{host}/dev/{name}/claude: Claude Code in the workspace and its tasks.
func (a *App) handleClaude(w http.ResponseWriter, r *http.Request) {
	host, name := r.PathValue("host"), r.PathValue("name")
	s, err := a.devWorkspace(host, name)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 6*time.Second)
	defer cancel()
	st := map[string]any{}
	if s.Claude {
		a.call(ctx, host, "GET", "/v1/dev/"+name+"/claude", nil, &st)
	}
	writeJSON(w, 200, map[string]any{"enabled": s.Claude, "mode": firstNonEmpty(s.ClaudeMode, "auto"), "auth": firstNonEmpty(s.ClaudeAuth, "login"),
		"state": st, "tasks": a.claudeTasks(host, name, 50)})
}

func logTail(s string, n int) string {
	if len(s) > n {
		return s[len(s)-n:]
	}
	return s
}
