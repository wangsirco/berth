package panel

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"testing"
	"time"
)

// A push to the project's branch, signed with its secret, starts a deploy as "webhook".
func TestNativePushHook(t *testing.T) {
	c := newTC(t)
	c.withFakeAgent()
	c.mkUser("root", "correct horse 9", "super")
	c.login("root", "correct horse 9")
	_, out := c.do("POST", "/api/v1/hosts/local/changes/native.set", map[string]any{"name": "web", "kind": "go", "version": "1.24",
		"source": map[string]any{"type": "git", "url": "https://git.example.com/web.git", "branch": "main"}, "start": "./.berth/app"})
	if out["status"] != "done" {
		t.Fatalf("set: %v", out)
	}
	_, info := c.do("GET", "/api/v1/hosts/local/native/web/webhook", nil)
	secret, _ := info["secret"].(string)
	if len(secret) < 20 {
		t.Fatalf("webhook info: %v", info)
	}
	_, again := c.do("GET", "/api/v1/hosts/local/native/web/webhook", nil)
	if again["secret"] != secret {
		t.Fatal("the secret must stay the same")
	}
	_, det := c.do("GET", "/api/v1/hosts/local/native/web", nil)
	if bytes.Contains([]byte(toJSON(det)), []byte("hook_enc")) {
		t.Fatal("the sealed secret leaks in the API")
	}
	post := func(body, sig string) int {
		req, _ := http.NewRequest("POST", c.srv.URL+"/hooks/native/local/web", bytes.NewReader([]byte(body)))
		req.Header.Set("X-Hub-Signature-256", sig)
		resp, err := c.srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	sign := func(body string) string {
		m := hmac.New(sha256.New, []byte(secret))
		m.Write([]byte(body))
		return "sha256=" + hex.EncodeToString(m.Sum(nil))
	}
	push := `{"ref":"refs/heads/main"}`
	if st := post(push, "sha256=00"); st != 401 {
		t.Fatalf("bad signature: %d", st)
	}
	if st := post(`{"ref":"refs/heads/dev"}`, sign(`{"ref":"refs/heads/dev"}`)); st != 200 {
		t.Fatalf("other branch: %d", st)
	}
	if st := post(push, sign(push)); st != 202 {
		t.Fatalf("push: %d", st)
	}
	for i := 0; i < 50; i++ {
		var n int
		c.app.db.QueryRow(`SELECT count(*) FROM changes WHERE kind='native.deploy' AND actor_name='webhook'`).Scan(&n)
		if n == 1 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the push did not start a deploy")
}

// Behind a secret entry path, callers that bring their own proof still get through.
func TestGateLetsSignedCallersThrough(t *testing.T) {
	for _, p := range []string{"/hooks/native/local/web", "/hooks/pipelines/3", "/migrate/pull/manifest", "/migrate/pull/pack"} {
		if !remoteAgentPath(p) {
			t.Fatalf("%s is blocked by the entry gate", p)
		}
	}
	for _, p := range []string{"/migrate/download/abc", "/api/v1/hosts", "/"} {
		if remoteAgentPath(p) {
			t.Fatalf("%s passes the entry gate without the cookie", p)
		}
	}
}
