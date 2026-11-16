package panel

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/wangsirco/berth/internal/change"
)

var auditKV = struct {
	sync.Mutex
	m map[string]string
}{m: map[string]string{}}

func init() {
	set := func(raw json.RawMessage) (old string, key, val string) {
		var p struct{ Key, Value string }
		json.Unmarshal(raw, &p)
		auditKV.Lock()
		defer auditKV.Unlock()
		old = auditKV.m[p.Key]
		auditKV.m[p.Key] = p.Value
		return old, p.Key, p.Value
	}
	props := map[string]change.Prop{"key": {Type: "string"}, "value": {Type: "string"}, "risk": {Type: "string"}}
	change.Register(change.Kind{
		Name: "audittest.set", Perm: "settings.manage", Scope: "settings:write", Title: "kind.audittest.set",
		Params: change.ParamSchema{Props: props, Required: []string{"key"}},
		CLI:    "berth audittest set {{.key}} {{.value}}",
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			var p struct{ Key, Risk string }
			json.Unmarshal(raw, &p)
			risk := change.RiskLow
			if p.Risk != "" {
				risk = change.Risk(p.Risk)
			}
			return change.Plan{Summary: "plan.audittest.set", Risk: risk, Undoable: true,
				Affected: []change.Object{{Type: "setting", ID: p.Key}}}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			old, key, _ := set(raw)
			u, _ := json.Marshal(map[string]string{"key": key, "value": old})
			return nil, &change.UndoRecipe{Kind: "audittest.set", Params: u}, nil
		},
	})
}

func kv(k string) string {
	auditKV.Lock()
	defer auditKV.Unlock()
	return auditKV.m[k]
}

func TestPlanRunListRevert(t *testing.T) {
	c := newTC(t)
	c.mkUser("iwsheng", "correct horse 9", "super")
	c.login("iwsheng", "correct horse 9")
	_, out := c.do("POST", "/api/v1/hosts/local/changes/audittest.set?plan=1", map[string]string{"key": "a1", "value": "v"})
	if out["status"] != "planned" || kv("a1") != "" || out["equivalent"] != "berth audittest set a1 v" {
		t.Fatalf("plan: %v", out)
	}
	_, out = c.do("POST", "/api/v1/hosts/local/changes/audittest.set", map[string]string{"key": "a1", "value": "v"})
	if out["status"] != "done" || kv("a1") != "v" {
		t.Fatalf("run: %v", out)
	}
	id := out["id"].(string)
	_, list := c.do("GET", "/api/v1/changes?limit=5", nil)
	first := list["items"].([]any)[0].(map[string]any)
	actor := first["actor"].(map[string]any)
	if first["id"] != id || actor["name"] != "iwsheng" || actor["via"] != "ui" {
		t.Fatalf("list: %v", first)
	}
	if r, out := c.do("POST", "/api/v1/changes/"+id+"/revert", nil); r.StatusCode != 200 || out["reverts"] != id {
		t.Fatalf("revert %d %v", r.StatusCode, out)
	}
	if kv("a1") != "" {
		t.Fatal("not reverted")
	}
	_, orig := c.do("GET", "/api/v1/changes/"+id, nil)
	if orig["status"] != "reverted" {
		t.Fatalf("status %v", orig["status"])
	}
}

func agtCall(c *tc, tok, path string, body any) (int, map[string]any) {
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest("POST", c.srv.URL+path, strings.NewReader(string(b)))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("X-Berth-Via", "mcp")
	req.Header.Set("X-Berth-Session", "s-42")
	resp, err := c.srv.Client().Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer resp.Body.Close()
	out := map[string]any{}
	json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func TestAgentTokenHighRiskWaitsForPerson(t *testing.T) {
	c := newTC(t)
	u := c.mkUser("iwsheng", "correct horse 9", "super")
	tok, _ := c.app.issueToken(u, tokenSpec{Kind: "agt", Name: "claude", Scopes: []string{"settings:write"}, DangerPolicy: "confirm"})
	code, out := agtCall(c, tok, "/api/v1/hosts/local/changes/audittest.set", map[string]string{"key": "h1", "value": "x", "risk": "high"})
	if code != 200 || out["status"] != "pending_approval" || kv("h1") != "" {
		t.Fatalf("%d %v", code, out)
	}
	id := out["id"].(string)
	if code, _ := agtCall(c, tok, "/api/v1/changes/"+id+"/confirm", nil); code != 403 {
		t.Fatalf("token approved itself: %d", code)
	}
	c.login("iwsheng", "correct horse 9")
	if r, out := c.do("POST", "/api/v1/changes/"+id+"/confirm", nil); r.StatusCode != 200 || out["status"] != "done" {
		t.Fatalf("confirm %d %v", r.StatusCode, out)
	}
	if kv("h1") != "x" {
		t.Fatal("not applied after confirm")
	}
	got, _ := c.app.pipe.Get(id)
	if got.Actor.Name != "claude" || got.Actor.Via != "mcp" || got.Actor.AISession != "s-42" {
		t.Fatalf("actor %+v", got.Actor)
	}
	// low risk runs directly
	if _, out := agtCall(c, tok, "/api/v1/hosts/local/changes/audittest.set", map[string]string{"key": "l1", "value": "y"}); out["status"] != "done" {
		t.Fatalf("low: %v", out)
	}
	// out of scope
	if code, _ := agtCall(c, tok, "/api/v1/hosts/local/changes/user.update", map[string]any{"id": 1}); code != 403 {
		t.Fatalf("scope %d", code)
	}
}
