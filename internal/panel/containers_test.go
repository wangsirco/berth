package panel

import (
	"strings"
	"testing"
)

func TestContainerStopPreviewRunRevert(t *testing.T) {
	c := newTC(t)
	fa := c.withFakeAgent()
	c.mkUser("iwsheng", "correct horse 9", "super")
	c.login("iwsheng", "correct horse 9")
	_, out := c.do("POST", "/api/v1/hosts/local/changes/container.stop?plan=1", map[string]string{"name": "n8n"})
	plan := out["plan"].(map[string]any)
	if plan["risk"] != "medium" || out["equivalent"] != "berth container stop n8n" || plan["undoable"] != true {
		t.Fatalf("%v", out)
	}
	if fa.called("POST /v1/docker/containers/abc123def456/stop") {
		t.Fatal("preview stopped the container")
	}
	_, out = c.do("POST", "/api/v1/hosts/local/containers/n8n/stop", nil)
	if out["status"] != "done" || !fa.called("POST /v1/docker/containers/abc123def456/stop") {
		t.Fatalf("run: %v", out)
	}
	if r, _ := c.do("POST", "/api/v1/changes/"+out["id"].(string)+"/revert", nil); r.StatusCode != 200 {
		t.Fatalf("revert %d", r.StatusCode)
	}
	if !fa.called("POST /v1/docker/containers/abc123def456/start") {
		t.Fatal("revert did not start it")
	}
}

func TestContainerRemoveListsImpact(t *testing.T) {
	c := newTC(t)
	c.withFakeAgent()
	c.mkUser("iwsheng", "correct horse 9", "super")
	c.login("iwsheng", "correct horse 9")
	_, out := c.do("POST", "/api/v1/hosts/local/changes/container.remove?plan=1", map[string]any{"name": "n8n"})
	plan := out["plan"].(map[string]any)
	var got []string
	for _, o := range plan["affected"].([]any) {
		m := o.(map[string]any)
		got = append(got, m["type"].(string)+":"+m["id"].(string))
	}
	s := strings.Join(got, ",")
	for _, want := range []string{"container:n8n", "volume:n8n_data", "compose_project:n8n", "network:n8n_default"} {
		if !strings.Contains(s, want) {
			t.Fatalf("missing %s in %s", want, s)
		}
	}
	if plan["risk"] != "destructive" {
		t.Fatal(plan["risk"])
	}
}

func TestContainersNeedPermission(t *testing.T) {
	c := newTC(t)
	c.withFakeAgent()
	u := c.mkUser("ops", "correct horse 9", "admin")
	u.Perms = []string{"containers.view"}
	c.app.saveUser(u)
	c.login("ops", "correct horse 9")
	if r, _ := c.do("GET", "/api/v1/hosts/local/containers", nil); r.StatusCode != 200 {
		t.Fatalf("view %d", r.StatusCode)
	}
	r, out := c.do("POST", "/api/v1/hosts/local/changes/container.stop", map[string]string{"name": "n8n"})
	if r.StatusCode != 403 || out["error"].(map[string]any)["code"] != "auth.forbidden" {
		t.Fatalf("%d %v", r.StatusCode, out)
	}
}

func TestComposeUpdateUndoRestoresSecretEnv(t *testing.T) {
	c := newTC(t)
	fa := c.withFakeAgent()
	c.mkUser("iwsheng", "correct horse 9", "super")
	c.login("iwsheng", "correct horse 9")
	newCompose := "services:\n  n8n:\n    image: n8nio/n8n:2\n"
	_, out := c.do("POST", "/api/v1/hosts/local/changes/compose.update", map[string]string{"name": "n8n", "compose": newCompose, "env": "N8N_KEY=new\n"})
	if out["status"] != "done" {
		t.Fatalf("%v", out)
	}
	files := out["plan"].(map[string]any)["files"].([]any)
	diffs := ""
	for _, f := range files {
		diffs += f.(map[string]any)["unified"].(string)
	}
	if strings.Contains(diffs, "old") || strings.Contains(diffs, "new\n") || !strings.Contains(diffs, "+    image: n8nio/n8n:2") {
		t.Fatalf("diff leaks secrets or misses change:\n%s", diffs)
	}
	var stored string
	c.app.db.QueryRow(`SELECT params || undo FROM changes WHERE id=?`, out["id"]).Scan(&stored)
	if strings.Contains(stored, "N8N_KEY=old") || strings.Contains(stored, "N8N_KEY=new") {
		t.Fatalf("env stored in clear: %s", stored)
	}
	c.do("POST", "/api/v1/changes/"+out["id"].(string)+"/revert", nil)
	fa.mu.Lock()
	defer fa.mu.Unlock()
	if fa.files["env"] != "N8N_KEY=old\n" || !strings.Contains(fa.files["compose"], "n8n:1") {
		t.Fatalf("undo did not restore: %v", fa.files)
	}
}

func TestAdoptRecordsRelation(t *testing.T) {
	c := newTC(t)
	c.withFakeAgent()
	c.mkUser("iwsheng", "correct horse 9", "super")
	c.login("iwsheng", "correct horse 9")
	_, before := c.do("GET", "/api/v1/hosts/local/compose", nil)
	if before["items"].([]any)[0].(map[string]any)["adopted"] != false {
		t.Fatal("should start as discovered")
	}
	if _, out := c.do("POST", "/api/v1/hosts/local/changes/compose.adopt", map[string]string{"name": "n8n"}); out["status"] != "done" {
		t.Fatalf("%v", out)
	}
	_, after := c.do("GET", "/api/v1/hosts/local/compose", nil)
	if after["items"].([]any)[0].(map[string]any)["adopted"] != true {
		t.Fatal("not adopted")
	}
}
