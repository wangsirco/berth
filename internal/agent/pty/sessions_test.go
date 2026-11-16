package pty

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestSessionSurvivesDetachAndReplays(t *testing.T) {
	m := NewManager(t.TempDir())
	s, err := m.Start(StartOpts{Argv: []string{"/bin/sh"}, Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	c1 := s.Attach()
	s.Write([]byte("echo berth-$((40+2))\n"))
	waitFor(t, c1, "berth-42")
	s.Detach(c1)
	time.Sleep(50 * time.Millisecond)
	if !m.Alive(s.ID) {
		t.Fatal("session should keep running with no clients")
	}
	c2 := s.Attach()
	waitFor(t, c2, "berth-42") // 回放
	m.Kill(s.ID)
}

func TestScrollbackCapped(t *testing.T) {
	r := newRing(16)
	r.Write([]byte("0123456789"))
	r.Write([]byte("abcdefghij"))
	if got := string(r.Bytes()); got != "456789abcdefghij" {
		t.Fatalf("%q", got)
	}
}

func waitFor(t *testing.T, ch <-chan []byte, want string) {
	var buf bytes.Buffer
	deadline := time.After(3 * time.Second)
	for !strings.Contains(buf.String(), want) {
		select {
		case b := <-ch:
			buf.Write(b)
		case <-deadline:
			t.Fatalf("timeout, got %q", buf.String())
		}
	}
}
