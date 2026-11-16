package panel

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"
	"testing"
	"time"
)

func (f *fakeAgent) image(svc string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.images[svc]
}

func (c *tc) waitChange(id string) string {
	c.t.Helper()
	for i := 0; i < 200; i++ {
		ch, err := c.app.pipe.Get(id)
		if err == nil && ch.Status != "running" && ch.Status != "planned" {
			return ch.Status
		}
		time.Sleep(20 * time.Millisecond)
	}
	c.t.Fatalf("change %s did not finish", id)
	return ""
}

const shopSpec = `name: shop
runner: { memory: 256m, cpus: 1 }
steps:
  - name: image
    docker_build: { context: ., platforms: [linux/amd64], push: registry.berth.lan/shop/web }
deploy:
  - host: local
    compose: /opt/shop/compose.yaml
    service: web
    healthcheck: { url: http://127.0.0.1:3000/, timeout: 30s }
`

func TestPipelineBuildDeployRollback(t *testing.T) {
	c := newTC(t)
	fa := c.withFakeAgent()
	c.mkUser("root", "correct horse 9", "super")
	c.login("root", "correct horse 9")
	c.app.setSetting("registry.addr", "192.0.2.10:5000")
	c.app.setSetting("registry.host", "local")

	_, out := c.do("POST", "/api/v1/hosts/local/changes/pipeline.create", map[string]any{"name": "shop", "source": map[string]any{"type": "git", "url": "https://git.example.com/shop.git", "ref": "main"},
		"token": "ghp_secret", "spec": shopSpec})
	if out["status"] != "done" {
		t.Fatalf("create: %v", out)
	}
	_, audit := c.do("GET", "/api/v1/changes?limit=5", nil)
	if strings.Contains(toJSON(audit), "ghp_secret") {
		t.Fatal("git token in the audit log")
	}

	// a run builds, records the image and releases it
	_, out = c.do("POST", "/api/v1/hosts/local/changes/pipeline.run", map[string]any{"name": "shop"})
	if st := c.waitChange(out["id"].(string)); st != "done" {
		ch, _ := c.app.pipe.Get(out["id"].(string))
		t.Fatalf("run: %s %s", st, ch.Log)
	}
	if !fa.called("run p1-r1 git 192.0.2.10:5000") {
		t.Fatal("runner not called with the registry address")
	}
	_, detail := c.do("GET", "/api/v1/pipelines/shop", nil)
	js := toJSON(detail)
	if !strings.Contains(js, `"image":"192.0.2.10:5000/shop/web:r1"`) || !strings.Contains(js, `"status":"done"`) || !strings.Contains(js, `"commit":"abc123"`) {
		t.Fatalf("detail: %s", js)
	}
	if fa.image("web") != "192.0.2.10:5000/shop/web:r1" {
		t.Fatalf("not deployed: %v", fa.images)
	}
	var n int
	c.app.db.QueryRow(`SELECT COUNT(*) FROM registry_tags WHERE repo='shop/web'`).Scan(&n)
	if n != 2 {
		t.Fatalf("registry tags %d", n)
	}

	// second run fails its health check: the release is marked rolled back
	fa.mu.Lock()
	fa.deployFail = true
	fa.mu.Unlock()
	_, out = c.do("POST", "/api/v1/hosts/local/changes/pipeline.run", map[string]any{"name": "shop"})
	if st := c.waitChange(out["id"].(string)); st != "failed" {
		t.Fatalf("run 2: %s", st)
	}
	_, rel := c.do("GET", "/api/v1/pipelines/shop/releases", nil)
	if !strings.Contains(toJSON(rel), `"status":"rolled_back"`) {
		t.Fatalf("releases: %v", rel)
	}
	fa.mu.Lock()
	fa.deployFail = false
	fa.mu.Unlock()

	// release an earlier version by hand, then undo it
	_, out = c.do("POST", "/api/v1/hosts/local/changes/release.deploy?plan=1", map[string]any{"pipeline": "shop", "release_id": 1})
	if !strings.Contains(toJSON(out), "shop/web:r1") {
		t.Fatalf("plan: %v", out)
	}
	fa.mu.Lock()
	fa.images["web"] = "192.0.2.10:5000/shop/web:r0"
	fa.mu.Unlock()
	_, out = c.do("POST", "/api/v1/hosts/local/changes/release.deploy", map[string]any{"pipeline": "shop", "release_id": 1})
	if st := c.waitChange(out["id"].(string)); st != "done" || fa.image("web") != "192.0.2.10:5000/shop/web:r1" {
		t.Fatalf("manual release: %s %v", st, fa.images)
	}
	c.do("POST", "/api/v1/changes/"+out["id"].(string)+"/revert", nil)
	time.Sleep(200 * time.Millisecond)
	for i := 0; i < 50 && fa.image("web") != "192.0.2.10:5000/shop/web:r0"; i++ {
		time.Sleep(20 * time.Millisecond)
	}
	if fa.image("web") != "192.0.2.10:5000/shop/web:r0" {
		t.Fatalf("undo: %v", fa.images)
	}

	// a failing build marks the run failed with its steps
	fa.mu.Lock()
	fa.buildFail = true
	fa.mu.Unlock()
	_, out = c.do("POST", "/api/v1/hosts/local/changes/pipeline.run", map[string]any{"name": "shop"})
	c.waitChange(out["id"].(string))
	runs, _ := c.app.runs(1, 1)
	if runs[0].Status != "failed" || runs[0].Steps[0].Status != "failed" {
		t.Fatalf("failed run: %+v", runs[0])
	}
}

func TestPipelineWebhook(t *testing.T) {
	c := newTC(t)
	fa := c.withFakeAgent()
	c.mkUser("root", "correct horse 9", "super")
	c.login("root", "correct horse 9")
	_, out := c.do("POST", "/api/v1/hosts/local/changes/pipeline.create", map[string]any{"name": "shop", "source": map[string]any{"type": "git", "url": "https://git.example.com/shop.git", "ref": "main"},
		"spec": shopSpec[:strings.Index(shopSpec, "deploy:")]})
	if out["status"] != "done" {
		t.Fatalf("create: %v", out)
	}
	_, info := c.do("GET", "/api/v1/pipelines/1/webhook", nil)
	if info["secret"] == nil {
		t.Fatalf("webhook info: %v", info)
	}
	secret := info["secret"].(string)
	post := func(body string, sign func(*http.Request)) int {
		req, _ := http.NewRequest("POST", c.srv.URL+"/hooks/pipelines/1", bytes.NewReader([]byte(body)))
		sign(req)
		resp, err := c.srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	gh := func(body string) func(*http.Request) {
		return func(r *http.Request) {
			m := hmac.New(sha256.New, []byte(secret))
			m.Write([]byte(body))
			r.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(m.Sum(nil)))
		}
	}
	push := `{"ref":"refs/heads/main","after":"0123456789abcdef"}`
	if st := post(push, func(r *http.Request) { r.Header.Set("X-Hub-Signature-256", "sha256=00") }); st != 401 {
		t.Fatalf("bad signature: %d", st)
	}
	if st := post(`{"ref":"refs/heads/dev"}`, gh(`{"ref":"refs/heads/dev"}`)); st != 200 {
		t.Fatalf("other branch: %d", st)
	}
	if st := post(push, gh(push)); st != 202 {
		t.Fatalf("push: %d", st)
	}
	for i := 0; i < 100 && !fa.called("run p1-r1 git "); i++ {
		time.Sleep(20 * time.Millisecond)
	}
	if !fa.called("run p1-r1 git ") {
		t.Fatal("webhook did not start a run")
	}
	runs, _ := c.app.runs(1, 1)
	if runs[0].Trigger != "webhook" || runs[0].Ref != "0123456789abcdef" {
		t.Fatalf("run: %+v", runs[0])
	}
	// GitLab uses a plain token
	if st := post(push, func(r *http.Request) { r.Header.Set("X-Gitlab-Token", secret) }); st != 202 {
		t.Fatalf("gitlab: %d", st)
	}
}
