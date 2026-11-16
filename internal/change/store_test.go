package change

import (
	"context"
	"encoding/json"
	"testing"
)

func init() {
	Register(Kind{
		Name: "test.touch", Title: "kind.test.touch",
		Params: ParamSchema{Props: map[string]Prop{"name": {Type: "string"}}, Required: []string{"name"}},
		Plan: func(env Env, raw json.RawMessage) (Plan, error) {
			var p struct{ Name string }
			json.Unmarshal(raw, &p)
			return Plan{Summary: "plan.test.touch", Risk: RiskLow, Affected: []Object{{Type: "container", ID: p.Name, Name: p.Name}}}, nil
		},
		Apply: func(env Env, raw json.RawMessage) (any, *UndoRecipe, error) { return nil, nil, nil },
	})
}

func TestListByObjectAndPaging(t *testing.T) {
	p := newP(t, allow{})
	ctx := context.Background()
	var ids []string
	for _, n := range []string{"n8n", "redis", "n8n"} {
		c, err := p.Run(ctx, me, "local", "test.touch", json.RawMessage(`{"name":"`+n+`"}`))
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, c.ID)
	}
	got, err := p.List(ListFilter{ObjectType: "container", ObjectID: "n8n"})
	if err != nil || len(got) != 2 {
		t.Fatalf("by object: %v %d", err, len(got))
	}
	all, _ := p.List(ListFilter{Limit: 2})
	if len(all) != 2 {
		t.Fatalf("limit: %d", len(all))
	}
	older, _ := p.List(ListFilter{Before: all[1].ID})
	if len(older) != 1 || older[0].ID == all[0].ID || older[0].ID == all[1].ID {
		t.Fatalf("paging: %+v", older)
	}
}

func TestReject(t *testing.T) {
	p := newP(t, allow{confirm: true})
	c, _ := p.Run(context.Background(), me, "local", "test.touch", json.RawMessage(`{"name":"x"}`))
	r, err := p.Reject(c.ID, me)
	if err != nil || r.Status != "rejected" {
		t.Fatalf("%v %+v", err, r)
	}
	if _, err := p.Confirm(context.Background(), c.ID, me); err == nil {
		t.Fatal("rejected change must not be confirmable")
	}
}

func TestRedactedParamsNeverStored(t *testing.T) {
	Register(Kind{
		Name: "test.secret", Redact: []string{"pw"},
		Params: ParamSchema{Props: map[string]Prop{"user": {Type: "string"}, "pw": {Type: "string"}}},
		Plan:   func(Env, json.RawMessage) (Plan, error) { return Plan{Risk: RiskLow}, nil },
		Apply: func(env Env, raw json.RawMessage) (any, *UndoRecipe, error) {
			var p struct{ PW string }
			json.Unmarshal(raw, &p)
			if p.PW != "hunter2" {
				t.Errorf("apply must see the real value, got %q", p.PW)
			}
			return nil, nil, nil
		},
	})
	p := newP(t, allow{})
	c, err := p.Run(context.Background(), me, "local", "test.secret", json.RawMessage(`{"user":"a","pw":"hunter2"}`))
	if err != nil {
		t.Fatal(err)
	}
	var raw string
	p.db.QueryRow(`SELECT params FROM changes WHERE id=?`, c.ID).Scan(&raw)
	if raw != `{"pw":"[redacted]","user":"a"}` {
		t.Fatalf("stored %s", raw)
	}
	if _, err := newP(t, allow{confirm: true}).Run(context.Background(), me, "local", "test.secret", json.RawMessage(`{"pw":"x"}`)); err == nil {
		t.Fatal("redacted kinds must not be deferred")
	}
}
