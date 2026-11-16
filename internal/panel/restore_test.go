package panel

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"testing"
)

func (c *tc) putRaw(path string, body []byte) (*http.Response, map[string]any) {
	req, _ := http.NewRequest("PUT", c.srv.URL+path, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/octet-stream")
	if c.csrf != "" {
		req.Header.Set("X-CSRF-Token", c.csrf)
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer resp.Body.Close()
	out := map[string]any{}
	json.NewDecoder(resp.Body).Decode(&out)
	return resp, out
}

func TestUploadResumes(t *testing.T) {
	c := newTC(t)
	c.mkUser("boss", "correct horse 9", "super")
	c.login("boss", "correct horse 9")
	if resp, _ := c.do("POST", "/api/v1/uploads", map[string]any{"name": "evil.sh", "size": 10}); resp.StatusCode != 400 {
		t.Fatal("non-pack upload accepted")
	}
	data := []byte("0123456789abcdefghij")
	resp, u := c.do("POST", "/api/v1/uploads", map[string]any{"name": "old-server.berthpack", "size": len(data)})
	if resp.StatusCode != 200 {
		t.Fatal(u)
	}
	id := u["id"].(string)
	if _, p := c.putRaw("/api/v1/uploads/"+id+"?offset=0", data[:8]); p["received"].(float64) != 8 {
		t.Fatal(p)
	}
	// a gap is refused; a repeat from an earlier offset is taken from there
	if resp, _ := c.putRaw("/api/v1/uploads/"+id+"?offset=12", data[12:]); resp.StatusCode != 409 {
		t.Fatal("gap accepted")
	}
	c.putRaw("/api/v1/uploads/"+id+"?offset=4", data[4:10])
	_, p := c.putRaw("/api/v1/uploads/"+id+"?offset=10", data[10:])
	if p["done"] != true || p["received"].(float64) != float64(len(data)) {
		t.Fatal(p)
	}
	got, _ := os.ReadFile(p["path"].(string))
	if string(got) != string(data) {
		t.Fatalf("%q", got)
	}
	if _, err := c.app.uploadedFile(id, ".berthsnap"); err == nil {
		t.Fatal("a pack passed as a snapshot")
	}
	if _, err := c.app.uploadedFile(id, ".berthpack"); err != nil {
		t.Fatal(err)
	}
}

func TestExternalRepoNeedsPassword(t *testing.T) {
	e := externalRepo{Target: targetCfg{Kind: "local", Path: "/srv/old-backups"}}
	if _, err := e.repo(); err == nil {
		t.Fatal("no password accepted")
	}
	e.Target.Password = "secret-pass"
	r, err := e.repo()
	if err != nil || r.URL != "/srv/old-backups" || r.Password != "secret-pass" {
		t.Fatal(r, err)
	}
}
