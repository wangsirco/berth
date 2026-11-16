package change

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/wangsirco/berth/internal/errs"
)

type Gate interface {
	Check(a Actor, k Kind, p Plan) (needConfirm bool, err error)
}

// Runner lets heavy kinds go through the task queue (internal/tasks implements it).
type Runner interface {
	Submit(ctx context.Context, c *Change, k Kind, run func(env Env) (any, *UndoRecipe, error)) error
}

type Pipeline struct {
	db     *sql.DB
	gate   Gate
	Runner Runner        // nil: heavy kinds run inline
	OnDone func(*Change) // 动态时间线 / 事件推送用
	// OnPending runs when a change starts waiting for approval; OnRejected when it is declined.
	OnPending, OnRejected func(*Change)
	// Quota refuses a change that would take its actor over their limits (nil = none).
	Quota func(a Actor, host, kind string, params json.RawMessage) error
	Svc   any        // passed to kinds as Env.Svc
	mu    sync.Mutex // 同一主机串行执行写操作
}

func NewPipeline(db *sql.DB, gate Gate) *Pipeline { return &Pipeline{db: db, gate: gate} }

// plan builds a preview. validate is false for undo recipes, whose params are
// produced by Apply (e.g. restoring an empty previous value) rather than typed by a user.
func (p *Pipeline) plan(ctx context.Context, a Actor, host, kind string, params json.RawMessage, validate bool) (*Change, Kind, error) {
	k, ok := Get(kind)
	if !ok {
		return nil, k, errs.New(404, "change.unknown_kind", "kind", kind)
	}
	if len(params) == 0 {
		params = json.RawMessage(`{}`)
	}
	if validate {
		if err := k.Params.Validate(params); err != nil {
			return nil, k, err
		}
	}
	if p.Quota != nil {
		if err := p.Quota(a, host, kind, params); err != nil {
			return nil, k, err
		}
	}
	pl, err := k.Plan(Env{Ctx: ctx, Host: host, Log: func(string, ...any) {}, Svc: p.Svc, ActorIP: a.IP, ActorName: a.Name, ActorType: a.Type, ActorID: a.ID}, params)
	if err != nil {
		return nil, k, err
	}
	for i := range pl.Files {
		pl.Files[i].Unified = Unified(pl.Files[i].Path, pl.Files[i].Before, pl.Files[i].After)
	}
	c := &Change{ID: newID(), Host: host, Kind: kind, Actor: a, Params: params, Plan: pl, Status: "planned", TS: time.Now().Unix()}
	return c, k, nil
}

func (p *Pipeline) Preview(ctx context.Context, a Actor, host, kind string, params json.RawMessage) (*Change, error) {
	c, _, err := p.plan(ctx, a, host, kind, params, true)
	return c, err
}

func (p *Pipeline) Run(ctx context.Context, a Actor, host, kind string, params json.RawMessage) (*Change, error) {
	c, k, err := p.plan(ctx, a, host, kind, params, true)
	if err != nil {
		return nil, err
	}
	need, err := p.gate.Check(a, k, c.Plan)
	if err != nil {
		return nil, err
	}
	if need {
		if len(k.Redact) > 0 {
			return nil, errs.New(403, "change.cannot_defer", "kind", kind)
		}
		c.Status = "pending_approval"
		if err := p.save(c); err != nil {
			return c, err
		}
		if p.OnPending != nil {
			p.OnPending(c)
		}
		return c, nil
	}
	return p.apply(ctx, c, k)
}

func (p *Pipeline) Confirm(ctx context.Context, id string, by Actor) (*Change, error) {
	c, err := p.Get(id)
	if err != nil {
		return nil, err
	}
	if c.Status != "pending_approval" {
		return nil, errs.New(409, "change.not_pending")
	}
	k, _ := Get(c.Kind)
	c.Log += fmt.Sprintf("confirmed by %s (%s)\n", by.Name, by.Via)
	return p.apply(ctx, c, k)
}

func (p *Pipeline) apply(ctx context.Context, c *Change, k Kind) (*Change, error) {
	var logb strings.Builder
	logb.WriteString(c.Log)
	env := Env{Ctx: ctx, Host: c.Host, Svc: p.Svc, ActorIP: c.Actor.IP, Log: func(f string, a ...any) {
		fmt.Fprintf(&logb, time.Now().Format("15:04:05 ")+f+"\n", a...)
	}}
	params := c.Params
	run := func(env Env) (any, *UndoRecipe, error) {
		if !k.Heavy {
			// quick changes are serialized; heavy ones are already ordered by the task
			// queue and must not hold this lock for minutes
			p.mu.Lock()
			defer p.mu.Unlock()
		}
		env.Svc = p.Svc
		env.ChangeID, env.ActorName, env.ActorType, env.ActorID = c.ID, c.Actor.Name, c.Actor.Type, c.Actor.ID
		return k.Apply(env, params)
	}
	c.Status = "running"
	if err := p.save(c); err != nil {
		return nil, err
	}
	if k.Heavy && p.Runner != nil {
		// The task queue runs it later and calls p.Finish. It gets its own copy, so the
		// caller can still read (and serialize) c while the task updates its copy.
		queued := *c
		return c, p.Runner.Submit(ctx, &queued, k, run)
	}
	res, undo, err := run(env)
	c.Log = logb.String()
	return c, p.Finish(c, res, undo, err)
}

// Finish records the outcome. Exported for the task runner.
func (p *Pipeline) Finish(c *Change, res any, undo *UndoRecipe, runErr error) error {
	c.Status = "done"
	if runErr != nil {
		c.Status = "failed"
		e := errs.From(runErr)
		c.Result, _ = json.Marshal(map[string]any{"error": e})
	} else {
		c.Result, _ = json.Marshal(res)
		if c.Plan.Undoable {
			c.Undo = undo
		}
	}
	if err := p.save(c); err != nil {
		return err
	}
	if c.Reverts != "" && runErr == nil {
		_, _ = p.db.Exec(`UPDATE changes SET status='reverted', reverted_by=? WHERE id=?`, c.ID, c.Reverts)
	}
	if p.OnDone != nil {
		p.OnDone(c)
	}
	return runErr
}

func (p *Pipeline) Revert(ctx context.Context, a Actor, id string) (*Change, error) {
	orig, err := p.Get(id)
	if err != nil {
		return nil, err
	}
	if orig.Status != "done" || orig.Undo == nil {
		return nil, errs.New(409, "change.not_revertible")
	}
	c, k, err := p.plan(ctx, a, orig.Host, orig.Undo.Kind, orig.Undo.Params, false)
	if err != nil {
		return nil, err
	}
	c.Reverts = orig.ID
	need, err := p.gate.Check(a, k, c.Plan)
	if err != nil {
		return nil, err
	}
	if need {
		c.Status = "pending_approval"
		if err := p.save(c); err != nil {
			return c, err
		}
		if p.OnPending != nil {
			p.OnPending(c)
		}
		return c, nil
	}
	out, err := p.apply(ctx, c, k)
	return out, err
}

// Reject declines a change waiting for approval.
func (p *Pipeline) Reject(id string, by Actor) (*Change, error) {
	c, err := p.Get(id)
	if err != nil {
		return nil, err
	}
	if c.Status != "pending_approval" {
		return nil, errs.New(409, "change.not_pending")
	}
	c.Status = "rejected"
	c.Log += fmt.Sprintf("rejected by %s (%s)\n", by.Name, by.Via)
	if err := p.save(c); err != nil {
		return c, err
	}
	if p.OnRejected != nil {
		p.OnRejected(c)
	}
	return c, nil
}

// Record stores a change that already happened outside Apply (browser ceremonies such as
// adding a passkey), so it still shows in the audit log. It is never undoable.
func (p *Pipeline) Record(a Actor, host, kind string, params any, plan Plan) (*Change, error) {
	raw, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}
	c := &Change{ID: newID(), Host: host, Kind: kind, Actor: a, Params: raw, Plan: plan, Status: "done", TS: time.Now().Unix()}
	if err := p.save(c); err != nil {
		return nil, err
	}
	if p.OnDone != nil {
		p.OnDone(c)
	}
	return c, nil
}
