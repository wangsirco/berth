package panel

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wangsirco/berth/internal/agent"
)

func (c *tc) joinCode(name string) string {
	resp, out := c.do("POST", "/api/v1/hosts/join-codes", map[string]string{"name": name})
	if resp.StatusCode != 200 {
		c.t.Fatalf("join code: %d %v", resp.StatusCode, out)
	}
	if !strings.Contains(out["command"].(string), "/join/"+out["code"].(string)) {
		c.t.Fatalf("command: %v", out["command"])
	}
	return out["code"].(string)
}

func (c *tc) enroll(code, name string) (int, map[string]any) {
	resp, out := c.do("POST", "/api/v1/agents/enroll", map[string]string{"code": code, "name": name, "arch": "arm64", "os": "linux"})
	return resp.StatusCode, out
}

func TestJoinCodeSingleUseAndExpiry(t *testing.T) {
	c := newTC(t)
	c.mkUser("root", "pw-123456789", RoleSuper)
	c.login("root", "pw-123456789")

	if resp, _ := c.do("POST", "/api/v1/hosts/join-codes", map[string]string{"name": "local"}); resp.StatusCode != 400 {
		t.Fatal("reserved name accepted")
	}
	code := c.joinCode("edge-1")
	resp, err := c.hc.Get(c.srv.URL + "/join/" + code)
	if err != nil {
		t.Fatal(err)
	}
	script, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(script), `--name "edge-1"`) {
		t.Fatalf("script: %s", script)
	}
	if st, out := c.enroll(code, "other"); st != 400 {
		t.Fatalf("name mismatch: %d %v", st, out)
	}
	st, out := c.enroll(code, "edge-1")
	if st != 200 || out["key_id"] == "" || out["secret"] == "" {
		t.Fatalf("enroll: %d %v", st, out)
	}
	if st, _ := c.enroll(code, "edge-1"); st != 404 {
		t.Fatalf("reused code: %d", st)
	}
	if resp, _ := c.hc.Get(c.srv.URL + "/join/" + code); resp.StatusCode != 404 {
		t.Fatal("used code still serves the script")
	}
	if resp, _ := c.do("POST", "/api/v1/hosts/join-codes", map[string]string{"name": "edge-1"}); resp.StatusCode != 409 {
		t.Fatal("duplicate host name accepted")
	}

	code2 := c.joinCode("edge-2")
	c.app.db.Exec(`UPDATE join_codes SET expires_at=? WHERE code_hash=?`, time.Now().Add(-time.Minute).Unix(), codeHash(code2))
	if st, _ := c.enroll(code2, "edge-2"); st != 404 {
		t.Fatalf("expired code: %d", st)
	}
}

func TestRemoteAgentOverWebSocket(t *testing.T) {
	c := newTC(t)
	c.mkUser("root", "pw-123456789", RoleSuper)
	c.login("root", "pw-123456789")
	st, out := c.enroll(c.joinCode("edge"), "edge")
	if st != 200 {
		t.Fatalf("enroll: %d %v", st, out)
	}
	keyID := out["key_id"].(string)
	secret, _ := base64.StdEncoding.DecodeString(out["secret"].(string))
	fp := sha256.Sum256(c.srv.Certificate().Raw)

	s := agent.NewServer(agent.Config{Socket: t.TempDir() + "/unused.sock", Keys: map[string][]byte{keyID: secret}})
	s.Handle("GET /v1/echo", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		time.Sleep(20 * time.Millisecond)
		agent.WriteJSON(w, 200, map[string]string{"q": r.URL.Query().Get("q")})
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rc := agent.RemoteConfig{PanelURL: c.srv.URL, PanelFP: hex.EncodeToString(fp[:]), KeyID: keyID, Secret: secret, Name: "edge"}
	go agent.RunRemote(ctx, rc, s, t.Logf)

	waitHost := func(want bool) {
		t.Helper()
		for i := 0; i < 100; i++ {
			_, err := c.app.agent("edge")
			if (err == nil) == want {
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
		t.Fatalf("host connected=%v never happened", want)
	}
	waitHost(true)

	// Many concurrent requests share the one connection.
	var wg sync.WaitGroup
	errc := make(chan error, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			var v map[string]string
			q := strings.Repeat("x", i+1)
			if err := c.app.call(ctx, "edge", "GET", "/v1/echo?q="+q, nil, &v); err != nil || v["q"] != q {
				errc <- err
			}
		}(i)
	}
	wg.Wait()
	close(errc)
	for err := range errc {
		t.Fatalf("concurrent call: %v", err)
	}

	// The host list shows it as a remote host.
	_, hl := c.do("GET", "/api/v1/hosts", nil)
	if !strings.Contains(toJSON(hl), `"remote":true`) {
		t.Fatalf("hosts: %v", hl)
	}

	// The panel drops the session; the agent reconnects by itself.
	c.app.dropRemote("edge", nil)
	waitHost(false)
	waitHost(true)

	// The agent stops: the panel notices.
	cancel()
	waitHost(false)
}

func TestAgentConnectRejectsBadKey(t *testing.T) {
	c := newTC(t)
	c.mkUser("root", "pw-123456789", RoleSuper)
	c.login("root", "pw-123456789")
	_, out := c.enroll(c.joinCode("edge"), "edge")
	fp := sha256.Sum256(c.srv.Certificate().Raw)
	s := agent.NewServer(agent.Config{Socket: t.TempDir() + "/u.sock"})
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	rc := agent.RemoteConfig{PanelURL: c.srv.URL, PanelFP: hex.EncodeToString(fp[:]), KeyID: out["key_id"].(string), Secret: []byte("wrong"), Name: "edge"}
	go agent.RunRemote(ctx, rc, s, func(string, ...any) {})
	<-ctx.Done()
	if _, err := c.app.agent("edge"); err == nil {
		t.Fatal("agent with a wrong secret got connected")
	}

	// A wrong pin is refused before anything is sent.
	if err := agent.CheckPanel(context.Background(), agent.RemoteConfig{PanelURL: c.srv.URL, PanelFP: strings.Repeat("0", 64)}); err == nil {
		t.Fatal("wrong fingerprint accepted")
	}
}

func toJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
