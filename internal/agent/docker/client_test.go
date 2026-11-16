package docker

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"testing"
)

func fakeDocker(t *testing.T, h http.Handler) *Client {
	sock := filepath.Join(t.TempDir(), "d.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/version" {
			w.Write([]byte(`{"ApiVersion":"1.47"}`))
			return
		}
		h.ServeHTTP(w, r)
	})}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	return New(sock)
}

func TestListContainers(t *testing.T) {
	c := fakeDocker(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1.45/containers/json" || r.URL.Query().Get("all") != "1" {
			t.Errorf("path %s", r.URL)
		}
		w.Write([]byte(`[{"Id":"abc","Names":["/n8n"],"Image":"n8nio/n8n:1","State":"running","Status":"Up 2 hours",
		  "Created":1700000000,"Ports":[{"PrivatePort":5678,"PublicPort":5678,"Type":"tcp","IP":"127.0.0.1"}],
		  "Labels":{"com.docker.compose.project":"n8n","com.docker.compose.service":"n8n"},
		  "NetworkSettings":{"Networks":{"n8n_default":{}}},"Mounts":[{"Type":"volume","Name":"n8n_data","Destination":"/home/node"}]}]`))
	}))
	got, err := c.Containers(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	g := got[0]
	if g.Name != "n8n" || g.Project != "n8n" || g.Service != "n8n" || g.Ports[0].Private != 5678 || g.Networks[0] != "n8n_default" || g.Mounts[0].Name != "n8n_data" {
		t.Fatalf("%+v", g)
	}
}

func TestDemuxLogs(t *testing.T) {
	frame := func(stream byte, s string) []byte {
		n := len(s)
		return append([]byte{stream, 0, 0, 0, byte(n >> 24), byte(n >> 16), byte(n >> 8), byte(n)}, s...)
	}
	raw := append(frame(1, "2026-11-11T10:00:00.000000000Z hello\n"), frame(2, "2026-11-11T10:00:01.000000000Z oops\n")...)
	var lines []LogLine
	if err := demux(bytesReader(raw), func(l LogLine) { lines = append(lines, l) }); err != nil {
		t.Fatal(err)
	}
	if len(lines) != 2 || lines[0].Stream != "out" || lines[0].Line != "hello" || lines[1].Stream != "err" {
		t.Fatalf("%+v", lines)
	}
}

func bytesReader(b []byte) io.Reader { return bytes.NewReader(b) }

func TestOlderDaemonAPI(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "d.sock")
	ln, _ := net.Listen("unix", sock)
	var got string
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/version" {
			w.Write([]byte(`{"ApiVersion":"1.41"}`))
			return
		}
		got = r.URL.Path
		w.Write([]byte(`[]`))
	})}
	go srv.Serve(ln)
	defer srv.Close()
	if _, err := New(sock).Containers(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got != "/v1.41/containers/json" {
		t.Fatalf("old daemon asked with %s", got)
	}
	if !olderAPI("1.41", "1.45") || olderAPI("1.47", "1.45") || olderAPI("1.45", "1.45") {
		t.Fatal("olderAPI")
	}
}
