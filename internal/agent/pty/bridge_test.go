package pty

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestBridgeRoundTripAndResize(t *testing.T) {
	m := NewManager(t.TempDir())
	s, err := m.Start(StartOpts{Argv: []string{"/bin/sh"}, Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Kill(s.ID)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		Bridge(r.Context(), c, s)
	}))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close(websocket.StatusNormalClosure, "")
	c.Write(ctx, websocket.MessageText, []byte(`{"t":"resize","cols":100,"rows":30}`))
	c.Write(ctx, websocket.MessageBinary, []byte("stty size; echo ok-$((6*7))\n"))
	var got strings.Builder
	for !strings.Contains(got.String(), "ok-42") {
		_, b, err := c.Read(ctx)
		if err != nil {
			t.Fatalf("read: %v (got %q)", err, got.String())
		}
		got.Write(b)
	}
	if !strings.Contains(got.String(), "30 100") {
		t.Fatalf("resize not applied: %q", got.String())
	}
}
