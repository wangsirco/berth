package change

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"

	"github.com/wangsirco/berth/internal/store"
)

// 一个用内存 map 模拟的 Kind：设置键值，可还原。
var kv = map[string]string{}

func init() {
	Register(Kind{
		Name: "test.set", Perm: "settings.manage", Scope: "settings:write", Title: "kind.test.set",
		Params: ParamSchema{Props: map[string]Prop{"key": {Type: "string"}, "value": {Type: "string"}}, Required: []string{"key", "value"}},
		CLI:    "berth test set {{.key}} {{.value}}",
		Plan: func(env Env, raw json.RawMessage) (Plan, error) {
			var p struct{ Key, Value string }
			json.Unmarshal(raw, &p)
			return Plan{Summary: "plan.test.set", Risk: RiskLow, Undoable: true,
				Files: []FileDiff{{Path: "/kv/" + p.Key, Before: kv[p.Key], After: p.Value}}}, nil
		},
		Apply: func(env Env, raw json.RawMessage) (any, *UndoRecipe, error) {
			var p struct{ Key, Value string }
			json.Unmarshal(raw, &p)
			if p.Key == "boom" {
				return nil, nil, errors.New("boom")
			}
			old := kv[p.Key]
			kv[p.Key] = p.Value
			env.Log("set %s", p.Key)
			u, _ := json.Marshal(map[string]string{"key": p.Key, "value": old})
			return map[string]string{"ok": "1"}, &UndoRecipe{Kind: "test.set", Params: u}, nil
		},
	})
}

type allow struct{ confirm bool }

func (a allow) Check(Actor, Kind, Plan) (bool, error) { return a.confirm, nil }

func newP(t *testing.T, g Gate) *Pipeline {
	db, err := store.Open(filepath.Join(t.TempDir(), "b.db"))
	if err != nil {
		t.Fatal(err)
	}
	return NewPipeline(db, g)
}

var me = Actor{Type: "user", ID: "1", Name: "iwsheng", Via: "ui", IP: "10.0.2.5"}

func TestPreviewDoesNotApply(t *testing.T) {
	p := newP(t, allow{})
	c, err := p.Preview(context.Background(), me, "local", "test.set", json.RawMessage(`{"key":"a","value":"1"}`))
	if err != nil || c.Status != "planned" {
		t.Fatalf("%v %+v", err, c)
	}
	if kv["a"] != "" {
		t.Fatal("preview must not apply")
	}
	if c.Plan.Files[0].Unified == "" {
		t.Fatal("unified diff missing")
	}
}

func TestRunRecordsAndReverts(t *testing.T) {
	p := newP(t, allow{})
	c, err := p.Run(context.Background(), me, "local", "test.set", json.RawMessage(`{"key":"b","value":"2"}`))
	if err != nil || c.Status != "done" || kv["b"] != "2" {
		t.Fatalf("%v %+v", err, c)
	}
	got, _ := p.Get(c.ID)
	if got.Undo == nil || got.Log == "" || got.Actor.Name != "iwsheng" {
		t.Fatalf("not recorded: %+v", got)
	}
	r, err := p.Revert(context.Background(), me, c.ID)
	if err != nil || r.Reverts != c.ID || kv["b"] != "" {
		t.Fatalf("revert: %v %+v kv=%q", err, r, kv["b"])
	}
	again, _ := p.Get(c.ID)
	if again.Status != "reverted" {
		t.Fatalf("status %s", again.Status)
	}
	if _, err := p.Revert(context.Background(), me, c.ID); err == nil {
		t.Fatal("double revert must fail")
	}
}

func TestFailureRecorded(t *testing.T) {
	p := newP(t, allow{})
	c, err := p.Run(context.Background(), me, "local", "test.set", json.RawMessage(`{"key":"boom","value":"x"}`))
	if err == nil || c.Status != "failed" {
		t.Fatalf("%v %+v", err, c)
	}
}

func TestConfirmFlow(t *testing.T) {
	p := newP(t, allow{confirm: true})
	c, err := p.Run(context.Background(), me, "local", "test.set", json.RawMessage(`{"key":"c","value":"3"}`))
	if err != nil || c.Status != "pending_approval" || kv["c"] != "" {
		t.Fatalf("%v %+v", err, c)
	}
	done, err := p.Confirm(context.Background(), c.ID, me)
	if err != nil || done.Status != "done" || kv["c"] != "3" {
		t.Fatalf("%v %+v", err, done)
	}
}

func TestMissingRequiredParam(t *testing.T) {
	p := newP(t, allow{})
	if _, err := p.Preview(context.Background(), me, "local", "test.set", json.RawMessage(`{"key":"a"}`)); err == nil {
		t.Fatal("want validation error")
	}
}

func TestEquivalentCommand(t *testing.T) {
	got := EquivalentCommand("test.set", json.RawMessage(`{"key":"a b","value":"1"}`))
	if got != "berth test set 'a b' 1" {
		t.Fatalf("got %q", got)
	}
}
