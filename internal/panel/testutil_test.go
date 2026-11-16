package panel

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/wangsirco/berth/internal/store"
)

type tc struct {
	t    *testing.T
	app  *App
	srv  *httptest.Server
	hc   *http.Client
	csrf string
}

func newTC(t *testing.T) *tc {
	db, err := store.Open(filepath.Join(t.TempDir(), "b.db"))
	if err != nil {
		t.Fatal(err)
	}
	app := New(Config{DataDir: t.TempDir(), AllowCIDRs: []string{"127.0.0.0/8"}}, db, nil, nil)
	app.masterKey = bytes.Repeat([]byte{7}, 32)
	srv := httptest.NewTLSServer(app.Handler())
	t.Cleanup(srv.Close)
	jar, _ := cookiejar.New(nil)
	hc := srv.Client()
	hc.Jar = jar
	return &tc{t: t, app: app, srv: srv, hc: hc}
}

func (c *tc) do(method, path string, in any) (*http.Response, map[string]any) {
	var body bytes.Buffer
	if in != nil {
		json.NewEncoder(&body).Encode(in)
	}
	req, _ := http.NewRequest(method, c.srv.URL+path, &body)
	req.Header.Set("Content-Type", "application/json")
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
	if t, ok := out["csrf"].(string); ok {
		c.csrf = t
	}
	return resp, out
}

func (c *tc) mkUser(name, pw, role string) *User {
	u, err := c.app.createUser(name, name, pw, role)
	if err != nil {
		c.t.Fatal(err)
	}
	return u
}

func (c *tc) login(name, pw string) {
	resp, out := c.do("POST", "/api/v1/auth/login", map[string]string{"username": name, "password": pw})
	if resp.StatusCode != 200 {
		c.t.Fatalf("login %d %v", resp.StatusCode, out)
	}
}

func mustUser(c *tc, name string) *User {
	u, _, err := scanUser(c.app.db.QueryRow(`SELECT `+userCols+` FROM users WHERE username=?`, name))
	if err != nil {
		c.t.Fatal(err)
	}
	return u
}

// testRequest is a request for building actors in tests (only RemoteAddr is read).
func testRequest() *http.Request {
	r, _ := http.NewRequest("GET", "/", nil)
	r.RemoteAddr = "127.0.0.1:1"
	return r
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }
