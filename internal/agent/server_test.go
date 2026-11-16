package agent

import (
	"context"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/wangsirco/berth/internal/agentproto"
	"github.com/wangsirco/berth/internal/errs"
)

func start(t *testing.T) (*Server, string) {
	sock := filepath.Join(t.TempDir(), "a.sock")
	s := NewServer(Config{Socket: sock, Keys: map[string][]byte{"k1": []byte("secret")}})
	s.Handle("POST /v1/echo", func(w http.ResponseWriter, r *http.Request, body []byte) {
		WriteJSON(w, 200, map[string]string{"got": string(body)})
	})
	s.Handle("GET /v1/fail", func(w http.ResponseWriter, r *http.Request, body []byte) {
		WriteErr(w, errs.New(409, "x.busy", "n", 1))
	})
	go s.ListenAndServe()
	t.Cleanup(func() { s.Close() })
	s.WaitReady()
	return s, sock
}

func TestSignedRoundTrip(t *testing.T) {
	_, sock := start(t)
	c := agentproto.NewClient(sock, "k1", []byte("secret"))
	var out map[string]string
	if err := c.Do(context.Background(), "POST", "/v1/echo", map[string]int{"a": 1}, &out); err != nil {
		t.Fatal(err)
	}
	if out["got"] != `{"a":1}` {
		t.Fatalf("got %q", out["got"])
	}
}

func TestWrongKeyRejected(t *testing.T) {
	_, sock := start(t)
	c := agentproto.NewClient(sock, "k1", []byte("nope"))
	err := c.Do(context.Background(), "POST", "/v1/echo", nil, nil)
	if e := errs.From(err); e.Status != 401 {
		t.Fatalf("want 401, got %+v", e)
	}
}

func TestErrorShape(t *testing.T) {
	_, sock := start(t)
	c := agentproto.NewClient(sock, "k1", []byte("secret"))
	e := errs.From(c.Do(context.Background(), "GET", "/v1/fail", nil, nil))
	if e.Status != 409 || e.Code != "x.busy" || e.Params["n"].(float64) != 1 {
		t.Fatalf("got %+v", e)
	}
}
