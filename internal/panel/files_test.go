package panel

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wangsirco/berth/internal/agent"
	"github.com/wangsirco/berth/internal/agent/files"
	"github.com/wangsirco/berth/internal/agentproto"
)

// withFileAgent runs the real files module over a temp tree, with every path the panel
// sends re-rooted under base (so /srv/berth/x lands in base/srv/berth/x).
func (c *tc) withFileAgent() string {
	base, _ := os.MkdirTemp("", "bf")
	c.t.Cleanup(func() { os.RemoveAll(base) })
	base, _ = filepath.EvalSymlinks(base)
	for _, d := range []string{"srv/berth/stacks", "etc/berth"} {
		os.MkdirAll(filepath.Join(base, d), 0o755)
	}
	sock := filepath.Join(base, "a.sock")
	s := agent.NewServer(agent.Config{Socket: sock, Keys: map[string][]byte{"k": []byte("s")}})
	inner := agent.NewServer(agent.Config{Socket: filepath.Join(base, "inner.sock"), Keys: map[string][]byte{"k": []byte("s")}})
	files.Register(inner, files.New(filepath.Join(base, "trash")))
	go inner.ListenAndServe()
	inner.WaitReady()
	innerClient := agentproto.NewClient(filepath.Join(base, "inner.sock"), "k", []byte("s"))
	rebase := func(v any) any {
		if s, ok := v.(string); ok && strings.HasPrefix(s, "/") {
			return filepath.Join(base, s)
		}
		if l, ok := v.([]any); ok {
			for i := range l {
				if s, ok := l[i].(string); ok && strings.HasPrefix(s, "/") {
					l[i] = filepath.Join(base, s)
				}
			}
		}
		return v
	}
	for _, op := range []string{"read", "write", "mkdir", "rename", "trash", "trash/restore", "list"} {
		s.Handle("POST /v1/files/"+op, func(w http.ResponseWriter, r *http.Request, body []byte) {
			var in map[string]any
			json.Unmarshal(body, &in)
			for _, k := range []string{"root", "path", "to", "paths"} {
				if v, ok := in[k]; ok {
					in[k] = rebase(v)
				}
			}
			var out json.RawMessage
			if err := innerClient.Do(r.Context(), "POST", "/v1/files/"+op, in, &out); err != nil {
				agent.WriteErr(w, err)
				return
			}
			agent.WriteJSON(w, 200, out)
		})
	}
	go s.ListenAndServe()
	s.WaitReady()
	c.t.Cleanup(func() { s.Close(); inner.Close() })
	c.app.agents["local"] = agentproto.NewClient(sock, "k", []byte("s"))
	return base
}

func TestFileRootsAndSensitive(t *testing.T) {
	c := newTC(t)
	c.withFileAgent()
	ops := c.mkUser("ops", "correct horse 9", "admin")
	ops.Perms = []string{"files.read", "files.write"}
	c.app.saveUser(ops)
	c.login("ops", "correct horse 9")
	r, out := c.do("POST", "/api/v1/hosts/local/changes/file.write", map[string]string{"path": "/etc/hosts", "content": "x"})
	if r.StatusCode != 403 || out["error"].(map[string]any)["code"] != "files.outside_root" {
		t.Fatalf("%d %v", r.StatusCode, out)
	}
	ops.Scope.Paths = []string{"/etc/berth"}
	c.app.saveUser(ops)
	r, out = c.do("POST", "/api/v1/hosts/local/changes/file.write", map[string]string{"path": "/etc/berth/x", "content": "x"})
	if r.StatusCode != 403 || out["error"].(map[string]any)["code"] != "files.sensitive" {
		t.Fatalf("%d %v", r.StatusCode, out)
	}
}

func TestFileWriteRevertRestoresAndSealsOldContent(t *testing.T) {
	c := newTC(t)
	base := c.withFileAgent()
	c.mkUser("iwsheng", "correct horse 9", "super")
	c.login("iwsheng", "correct horse 9")
	target := filepath.Join(base, "srv/berth/stacks/app.env")
	os.WriteFile(target, []byte("SECRET=old\n"), 0o600)
	_, out := c.do("POST", "/api/v1/hosts/local/changes/file.write", map[string]string{"path": "/srv/berth/stacks/app.env", "content": "SECRET=new\n"})
	if out["status"] != "done" {
		t.Fatalf("%v", out)
	}
	if b, _ := os.ReadFile(target); string(b) != "SECRET=new\n" {
		t.Fatalf("written %q", b)
	}
	var params, undo string
	c.app.db.QueryRow(`SELECT params, undo FROM changes WHERE id=?`, out["id"]).Scan(&params, &undo)
	if strings.Contains(params, "SECRET=new") || strings.Contains(undo, "SECRET=old") || strings.Contains(undo, base64.StdEncoding.EncodeToString([]byte("SECRET=old\n"))) {
		t.Fatalf("content stored in clear: %s | %s", params, undo)
	}
	if st, _ := os.Stat(target); st.Mode().Perm() != 0o600 {
		t.Fatalf("mode changed to %v", st.Mode().Perm())
	}
	if r, _ := c.do("POST", "/api/v1/changes/"+out["id"].(string)+"/revert", nil); r.StatusCode != 200 {
		t.Fatalf("revert %d", r.StatusCode)
	}
	if b, _ := os.ReadFile(target); string(b) != "SECRET=old\n" {
		t.Fatalf("after undo %q", b)
	}
}

func TestTrashUndo(t *testing.T) {
	c := newTC(t)
	base := c.withFileAgent()
	c.mkUser("iwsheng", "correct horse 9", "super")
	c.login("iwsheng", "correct horse 9")
	f := filepath.Join(base, "srv/berth/note.txt")
	os.WriteFile(f, []byte("hi"), 0o644)
	_, out := c.do("POST", "/api/v1/hosts/local/changes/file.trash", map[string]any{"paths": []string{"/srv/berth/note.txt"}})
	if out["status"] != "done" {
		t.Fatalf("%v", out)
	}
	if _, err := os.Stat(f); !os.IsNotExist(err) {
		t.Fatal("still there")
	}
	c.do("POST", "/api/v1/changes/"+out["id"].(string)+"/revert", nil)
	if b, _ := os.ReadFile(f); string(b) != "hi" {
		t.Fatal("not restored")
	}
}
