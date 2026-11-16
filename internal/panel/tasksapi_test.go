package panel

import (
	"bufio"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/wangsirco/berth/internal/change"
)

func init() {
	change.Register(change.Kind{
		Name: "tasktest.slow", Perm: "settings.manage", Scope: "settings:write", Title: "kind.tasktest.slow", Heavy: true,
		Params: change.ParamSchema{Props: map[string]change.Prop{}},
		CLI:    "berth tasktest slow",
		Plan:   func(change.Env, json.RawMessage) (change.Plan, error) { return change.Plan{Risk: change.RiskLow}, nil },
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			env.Log("::step 0 working")
			env.Log("hello from task")
			return map[string]int{"n": 1}, nil, nil
		},
	})
}

func TestHeavyKindRunsInQueueAndStreams(t *testing.T) {
	c := newTC(t)
	c.mkUser("iwsheng", "correct horse 9", "super")
	c.login("iwsheng", "correct horse 9")

	req, _ := http.NewRequest("GET", c.srv.URL+"/api/v1/events", nil)
	resp, err := c.hc.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	lines := make(chan string, 100)
	go func() {
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			lines <- sc.Text()
		}
	}()

	_, out := c.do("POST", "/api/v1/hosts/local/changes/tasktest.slow", map[string]any{})
	if out["status"] != "running" {
		t.Fatalf("heavy kind should be queued: %v", out)
	}
	id := out["id"].(string)
	deadline := time.After(3 * time.Second)
	sawLog, sawDone, ev := false, false, ""
	for !(sawLog && sawDone) {
		select {
		case l := <-lines:
			if name, ok := strings.CutPrefix(l, "event: "); ok {
				ev = name
			}
			sawLog = sawLog || (ev == "task.log" && strings.Contains(l, "hello from task"))
			sawDone = sawDone || (ev == "change.done" && strings.HasPrefix(l, "data:") && strings.Contains(l, id) && strings.Contains(l, `"status":"done"`))
		case <-deadline:
			t.Fatalf("log=%v done=%v", sawLog, sawDone)
		}
	}
	got, _ := c.app.pipe.Get(id)
	if got.Status != "done" || string(got.Result) != `{"n":1}` {
		t.Fatalf("%+v", got)
	}
	_, list := c.do("GET", "/api/v1/tasks", nil)
	if items := list["items"].([]any); len(items) != 1 || items[0].(map[string]any)["status"] != "done" {
		t.Fatalf("%v", list)
	}
}
