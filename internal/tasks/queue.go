// Package tasks runs long changes (pulls, builds, backups...) one by one per host,
// with a memory gate so an 8 GB machine is not pushed into OOM (方案 §4.5).
package tasks

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/wangsirco/berth/internal/change"
	"github.com/wangsirco/berth/internal/errs"
)

// MemProbe returns the host's available memory in MB.
type MemProbe func(host string) (availableMB int)

// MemReserveMB stays free for the system and the panel itself.
const MemReserveMB = 512

// MemWaitMax: how long a task waits for memory to free up. A small machine may never have
// what a task would like; after this it runs anyway (slower, maybe swapping) and says so in
// its log, instead of waiting forever.
var MemWaitMax = 2 * time.Minute

type TaskView struct {
	ID         string         `json:"id"`
	ChangeID   string         `json:"change_id"`
	Host       string         `json:"host"`
	Kind       string         `json:"kind"`
	Title      string         `json:"title"`
	Status     string         `json:"status"` // waiting|running|done|failed|canceled
	WaitReason string         `json:"wait_reason,omitempty"`
	WaitParams map[string]any `json:"wait_params,omitempty"`
	Steps      []string       `json:"steps"`
	Step       int            `json:"step"`
	CreatedBy  string         `json:"created_by"`
	CreatedAt  int64          `json:"created_at"`
	StartedAt  int64          `json:"started_at,omitempty"`
	FinishedAt int64          `json:"finished_at,omitempty"`
}

type Event struct {
	Type string    `json:"type"` // task.update | task.log
	Task *TaskView `json:"task,omitempty"`
	ID   string    `json:"id,omitempty"`
	Seq  int       `json:"seq,omitempty"`
	Line string    `json:"line,omitempty"`
}

type RunFunc func(env change.Env) (any, *change.UndoRecipe, error)

type task struct {
	view     TaskView
	kind     change.Kind
	c        *change.Change
	run      RunFunc
	ctx      context.Context
	cancel   context.CancelFunc
	canceled bool
	seq      int
	memSince time.Time      // when it started waiting for memory
	lowMem   map[string]any // started without the memory it wanted (need / avail)
}

type Queue struct {
	db     *sql.DB
	mem    MemProbe
	finish func(c *change.Change, res any, undo *change.UndoRecipe, err error) error
	// Recheck is how often waiting tasks are re-evaluated (memory may have freed up).
	Recheck time.Duration
	// HeavyParallel is how many heavy tasks may run at once per host (default 1).
	HeavyParallel func() int

	mu      sync.Mutex
	tasks   map[string]*task // by change id
	order   []string
	subs    map[int]chan Event
	nextSub int
	wake    chan struct{}
	started bool
}

func New(db *sql.DB, mem MemProbe, finish func(c *change.Change, res any, undo *change.UndoRecipe, err error) error) *Queue {
	return &Queue{db: db, mem: mem, finish: finish, Recheck: 5 * time.Second,
		HeavyParallel: func() int { return 1 }, tasks: map[string]*task{}, subs: map[int]chan Event{}, wake: make(chan struct{}, 1)}
}

func taskID(changeID string) string { return "tsk_" + strings.TrimPrefix(changeID, "chg_") }

// Submit queues a change's run function. It satisfies change.Runner.
func (q *Queue) Submit(ctx context.Context, c *change.Change, k change.Kind, run func(env change.Env) (any, *change.UndoRecipe, error)) error {
	now := time.Now().Unix()
	if k.MemFor != nil {
		if mb := k.MemFor(c.Params); mb > 0 {
			k.MemMB = mb
		}
	}
	t := &task{kind: k, c: c, run: run, view: TaskView{ID: taskID(c.ID), ChangeID: c.ID, Host: c.Host, Kind: c.Kind, Title: k.Title,
		Status: "waiting", Steps: []string{}, CreatedBy: c.Actor.Name, CreatedAt: now}}
	// tasks outlive the HTTP request that created them
	t.ctx, t.cancel = context.WithCancel(context.WithoutCancel(ctx))
	if _, err := q.db.Exec(`INSERT INTO tasks(id, host, title, change_id, heavy, mem_mb, status, created_by, created_at) VALUES(?,?,?,?,?,?,?,?,?)`,
		t.view.ID, c.Host, k.Title, c.ID, k.Heavy, k.MemMB, "waiting", c.Actor.Name, now); err != nil {
		return err
	}
	q.mu.Lock()
	q.tasks[c.ID] = t
	q.order = append(q.order, c.ID)
	if !q.started {
		q.started = true
		go q.loop()
	}
	q.mu.Unlock()
	q.publish(Event{Type: "task.update", Task: q.snapshot(t)})
	q.poke()
	return nil
}

func (q *Queue) poke() {
	select {
	case q.wake <- struct{}{}:
	default:
	}
}

func (q *Queue) loop() {
	tick := time.NewTicker(q.Recheck)
	defer tick.Stop()
	for {
		q.schedule()
		select {
		case <-q.wake:
		case <-tick.C:
		}
	}
}

// schedule starts every waiting task whose gates are open, in submission order.
func (q *Queue) schedule() {
	q.mu.Lock()
	var start []*task
	var changed []*task
	heavyRunning := map[string]int{}
	for _, id := range q.order {
		if t := q.tasks[id]; t.view.Status == "running" && t.kind.Heavy {
			heavyRunning[t.view.Host]++
		}
	}
	avail := map[string]int{}
	for _, id := range q.order {
		t := q.tasks[id]
		if t.view.Status != "waiting" {
			continue
		}
		reason, params := "", map[string]any(nil)
		if t.kind.Heavy && heavyRunning[t.view.Host] >= q.HeavyParallel() {
			reason = "tasks.wait_heavy"
		} else if t.kind.MemMB > 0 {
			a, ok := avail[t.view.Host]
			if !ok {
				a = q.mem(t.view.Host)
			}
			if t.kind.MemMB > a-MemReserveMB {
				if t.memSince.IsZero() {
					t.memSince = time.Now()
				}
				if time.Since(t.memSince) < MemWaitMax {
					reason, params = "tasks.wait_memory", map[string]any{"need": t.kind.MemMB, "avail": a, "max": int(MemWaitMax.Minutes())}
				} else {
					t.lowMem = map[string]any{"need": t.kind.MemMB, "avail": a}
				}
			} else {
				avail[t.view.Host] = a - t.kind.MemMB // reserve for tasks started in this pass
			}
		}
		if reason != "" {
			if t.view.WaitReason != reason || fmt.Sprint(t.view.WaitParams) != fmt.Sprint(params) {
				t.view.WaitReason, t.view.WaitParams = reason, params
				changed = append(changed, t)
			}
			continue
		}
		if t.kind.Heavy {
			heavyRunning[t.view.Host]++
		}
		t.view.Status, t.view.WaitReason, t.view.WaitParams = "running", "", nil
		t.view.StartedAt = time.Now().Unix()
		start = append(start, t)
	}
	q.mu.Unlock()
	for _, t := range changed {
		q.persist(t)
		q.publish(Event{Type: "task.update", Task: q.snapshot(t)})
	}
	for _, t := range start {
		q.persist(t)
		q.publish(Event{Type: "task.update", Task: q.snapshot(t)})
		go q.execute(t)
	}
}

func (q *Queue) execute(t *task) {
	env := change.Env{Ctx: t.ctx, Host: t.view.Host, Log: func(f string, a ...any) { q.logLine(t, fmt.Sprintf(f, a...)) }}
	if t.lowMem != nil {
		q.logLine(t, fmt.Sprintf("内存偏紧：这个任务最好有 %v MB 空闲，现在只有 %v MB。不再等待，照常进行，可能会慢一些，期间其他程序也可能变慢。 / Memory is tight (%v MB wanted, %v MB free): going ahead anyway; it may be slower, and so may everything else meanwhile.",
			t.lowMem["need"], t.lowMem["avail"], t.lowMem["need"], t.lowMem["avail"]))
	}
	res, undo, err := t.run(env)
	q.mu.Lock()
	switch {
	case t.canceled:
		t.view.Status = "canceled"
		if err == nil {
			err = errs.New(409, "tasks.canceled")
		}
	case err != nil:
		t.view.Status = "failed"
	default:
		t.view.Status = "done"
	}
	t.view.FinishedAt = time.Now().Unix()
	q.mu.Unlock()
	t.cancel()
	q.persist(t)
	q.publish(Event{Type: "task.update", Task: q.snapshot(t)})
	_ = q.finish(t.c, res, undo, err)
	q.forget(t)
	q.poke()
}

// forget drops finished tasks from memory, keeping the last 50 for the drawer.
func (q *Queue) forget(t *task) {
	q.mu.Lock()
	defer q.mu.Unlock()
	var finished []string
	for _, id := range q.order {
		if s := q.tasks[id].view.Status; s != "waiting" && s != "running" {
			finished = append(finished, id)
		}
	}
	for len(finished) > 50 {
		delete(q.tasks, finished[0])
		finished = finished[1:]
	}
	keep := q.order[:0]
	for _, id := range q.order {
		if _, ok := q.tasks[id]; ok {
			keep = append(keep, id)
		}
	}
	q.order = keep
}

func (q *Queue) logLine(t *task, line string) {
	for _, l := range strings.Split(strings.TrimRight(line, "\n"), "\n") {
		q.mu.Lock()
		t.seq++
		seq := t.seq
		stepChanged := false
		if rest, ok := strings.CutPrefix(l, "::step "); ok {
			numStr, name, _ := strings.Cut(rest, " ")
			if n, err := strconv.Atoi(numStr); err == nil && n >= 0 {
				for len(t.view.Steps) <= n {
					t.view.Steps = append(t.view.Steps, "")
				}
				t.view.Steps[n], t.view.Step, stepChanged = name, n, true
			}
		}
		q.mu.Unlock()
		_, _ = q.db.Exec(`INSERT INTO task_logs(task_id, seq, ts, line) VALUES(?,?,?,?)`, t.view.ID, seq, time.Now().Unix(), l)
		q.publish(Event{Type: "task.log", ID: t.view.ID, Seq: seq, Line: l})
		if stepChanged {
			q.persist(t)
			q.publish(Event{Type: "task.update", Task: q.snapshot(t)})
		}
	}
}

func (q *Queue) persist(t *task) {
	v := q.snapshot(t)
	steps, _ := json.Marshal(v.Steps)
	_, _ = q.db.Exec(`UPDATE tasks SET status=?, wait_reason=?, steps=?, step=?, started_at=?, finished_at=? WHERE id=?`,
		v.Status, v.WaitReason, string(steps), v.Step, v.StartedAt, v.FinishedAt, v.ID)
}

func (q *Queue) snapshot(t *task) *TaskView {
	q.mu.Lock()
	defer q.mu.Unlock()
	v := t.view
	v.Steps = append([]string{}, t.view.Steps...)
	return &v
}

// Cancel stops a waiting task or asks a running one to stop (its context is canceled).
func (q *Queue) Cancel(id string) error {
	q.mu.Lock()
	t := q.lookup(id)
	if t == nil {
		q.mu.Unlock()
		return errs.New(404, "tasks.not_found")
	}
	switch t.view.Status {
	case "waiting":
		t.view.Status, t.view.FinishedAt, t.canceled = "canceled", time.Now().Unix(), true
		q.mu.Unlock()
		t.cancel()
		q.persist(t)
		q.publish(Event{Type: "task.update", Task: q.snapshot(t)})
		_ = q.finish(t.c, nil, nil, errs.New(409, "tasks.canceled"))
		q.forget(t)
		return nil
	case "running":
		t.canceled = true
		q.mu.Unlock()
		t.cancel()
		return nil
	}
	q.mu.Unlock()
	return errs.New(409, "tasks.not_running")
}

func (q *Queue) lookup(id string) *task {
	if t, ok := q.tasks[id]; ok {
		return t
	}
	for _, t := range q.tasks {
		if t.view.ID == id {
			return t
		}
	}
	return nil
}

func (q *Queue) view(id string) TaskView {
	q.mu.Lock()
	t := q.lookup(id)
	q.mu.Unlock()
	if t == nil {
		return TaskView{}
	}
	return *q.snapshot(t)
}

// Get returns one task (live or from the database).
func (q *Queue) Get(id string) (*TaskView, error) {
	q.mu.Lock()
	t := q.lookup(id)
	q.mu.Unlock()
	if t != nil {
		return q.snapshot(t), nil
	}
	list, err := q.query(`WHERE id=?`, id)
	if err != nil {
		return nil, err
	}
	if len(list) == 0 {
		return nil, errs.New(404, "tasks.not_found")
	}
	return list[0], nil
}

// List returns live tasks plus recent finished ones for host ("" = all), newest first.
func (q *Queue) List(host string, limit int) ([]*TaskView, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	if host == "" {
		return q.query(`ORDER BY created_at DESC, id DESC LIMIT ?`, limit)
	}
	return q.query(`WHERE host=? ORDER BY created_at DESC, id DESC LIMIT ?`, host, limit)
}

func (q *Queue) query(tail string, args ...any) ([]*TaskView, error) {
	rows, err := q.db.Query(`SELECT id, change_id, host, title, status, wait_reason, steps, step, created_by, created_at, started_at, finished_at FROM tasks `+tail, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*TaskView{}
	for rows.Next() {
		var v TaskView
		var steps string
		if err := rows.Scan(&v.ID, &v.ChangeID, &v.Host, &v.Title, &v.Status, &v.WaitReason, &steps, &v.Step, &v.CreatedBy, &v.CreatedAt, &v.StartedAt, &v.FinishedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(steps), &v.Steps)
		v.Kind = strings.TrimPrefix(v.Title, "kind.") // titles are "kind.<name>"; the kind is not stored separately
		// a live task knows more than its row (the numbers behind a wait reason)
		q.mu.Lock()
		if live := q.lookup(v.ID); live != nil {
			v.WaitParams = live.view.WaitParams
		}
		q.mu.Unlock()
		out = append(out, &v)
	}
	return out, rows.Err()
}

type LogLine struct {
	Seq  int    `json:"seq"`
	TS   int64  `json:"ts"`
	Line string `json:"line"`
}

func (q *Queue) Logs(id string, after, limit int) ([]LogLine, error) {
	if limit <= 0 || limit > 5000 {
		limit = 2000
	}
	rows, err := q.db.Query(`SELECT seq, ts, line FROM task_logs WHERE task_id=? AND seq>? ORDER BY seq LIMIT ?`, id, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []LogLine{}
	for rows.Next() {
		var l LogLine
		if err := rows.Scan(&l.Seq, &l.TS, &l.Line); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// Counts returns running and waiting tasks for the status bar.
func (q *Queue) Counts() (running, waiting int) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, t := range q.tasks {
		switch t.view.Status {
		case "running":
			running++
		case "waiting":
			waiting++
		}
	}
	return
}

// Subscribe receives task events until the returned stop function is called.
func (q *Queue) Subscribe() (<-chan Event, func()) {
	ch := make(chan Event, 256)
	q.mu.Lock()
	id := q.nextSub
	q.nextSub++
	q.subs[id] = ch
	q.mu.Unlock()
	return ch, func() {
		q.mu.Lock()
		delete(q.subs, id)
		q.mu.Unlock()
	}
}

func (q *Queue) publish(e Event) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, ch := range q.subs {
		select {
		case ch <- e:
		default: // slow subscriber: drop rather than block tasks
		}
	}
}

// MarkInterrupted fails tasks left running or waiting by a previous process and returns
// their change ids, so the caller can mark those changes failed too.
func MarkInterrupted(db *sql.DB) ([]string, error) {
	rows, err := db.Query(`SELECT change_id FROM tasks WHERE status IN ('running','waiting')`)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()
	if len(ids) == 0 {
		return nil, nil
	}
	_, err = db.Exec(`UPDATE tasks SET status='failed', wait_reason='tasks.interrupted', finished_at=? WHERE status IN ('running','waiting')`, time.Now().Unix())
	return ids, errors.Join(err)
}
