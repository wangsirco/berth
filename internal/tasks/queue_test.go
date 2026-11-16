package tasks

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/wangsirco/berth/internal/change"
	"github.com/wangsirco/berth/internal/store"
)

func newQ(t *testing.T, avail int) (*Queue, chan string) {
	db, _ := store.Open(filepath.Join(t.TempDir(), "b.db"))
	done := make(chan string, 10)
	q := New(db, func(string) int { return avail }, func(c *change.Change, _ any, _ *change.UndoRecipe, err error) error {
		if err != nil {
			done <- c.ID + ":failed"
		} else {
			done <- c.ID + ":done"
		}
		return nil
	})
	q.Recheck = 20 * time.Millisecond
	return q, done
}

func chg(id string) *change.Change {
	return &change.Change{ID: id, Host: "local", Kind: "x", Params: json.RawMessage(`{}`)}
}

func TestHeavySerialized(t *testing.T) {
	q, done := newQ(t, 8000)
	k := change.Kind{Name: "x", Heavy: true, MemMB: 100}
	release := make(chan struct{})
	q.Submit(context.Background(), chg("a"), k, func(env change.Env) (any, *change.UndoRecipe, error) { <-release; return nil, nil, nil })
	q.Submit(context.Background(), chg("b"), k, func(env change.Env) (any, *change.UndoRecipe, error) { return nil, nil, nil })
	time.Sleep(50 * time.Millisecond)
	if v := q.view("b"); v.Status != "waiting" || v.WaitReason != "tasks.wait_heavy" {
		t.Fatalf("b should wait: %+v", v)
	}
	close(release)
	if <-done != "a:done" || <-done != "b:done" {
		t.Fatal("order")
	}
}

func TestMemoryGate(t *testing.T) {
	q, _ := newQ(t, 1000)
	k := change.Kind{Name: "x", MemMB: 6000}
	q.Submit(context.Background(), chg("m"), k, func(env change.Env) (any, *change.UndoRecipe, error) { return nil, nil, nil })
	time.Sleep(50 * time.Millisecond)
	if v := q.view("m"); v.WaitReason != "tasks.wait_memory" {
		t.Fatalf("%+v", v)
	}
}

func TestCancelAndSteps(t *testing.T) {
	q, done := newQ(t, 8000)
	k := change.Kind{Name: "x"}
	q.Submit(context.Background(), chg("c"), k, func(env change.Env) (any, *change.UndoRecipe, error) {
		env.Log("::step 1 pull")
		<-env.Ctx.Done()
		return nil, nil, env.Ctx.Err()
	})
	time.Sleep(30 * time.Millisecond)
	if q.view("c").Step != 1 {
		t.Fatal("step not parsed")
	}
	q.Cancel("c")
	if got := <-done; got != "c:failed" {
		t.Fatal(got)
	}
	if q.view("c").Status != "canceled" {
		t.Fatal(q.view("c").Status)
	}
}
