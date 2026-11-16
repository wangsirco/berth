package pipeline

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func tgz(t *testing.T, files map[string]string, extra ...*tar.Header) []byte {
	var b bytes.Buffer
	zw := gzip.NewWriter(&b)
	tw := tar.NewWriter(zw)
	for n, c := range files {
		tw.WriteHeader(&tar.Header{Name: n, Mode: 0o644, Size: int64(len(c)), Typeflag: tar.TypeReg})
		tw.Write([]byte(c))
	}
	for _, h := range extra {
		tw.WriteHeader(h)
	}
	tw.Close()
	zw.Close()
	return b.Bytes()
}

func TestUntarRefusesEscapes(t *testing.T) {
	dir := t.TempDir()
	if err := Untar(bytes.NewReader(tgz(t, map[string]string{"../evil": "x"})), dir); err == nil {
		t.Fatal("../ accepted")
	}
	ok := tgz(t, map[string]string{"a/b.txt": "hi"}, &tar.Header{Name: "a/link", Linkname: "/etc/passwd", Typeflag: tar.TypeSymlink})
	if err := Untar(bytes.NewReader(ok), dir); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "a/b.txt")); string(b) != "hi" {
		t.Fatal("file not extracted")
	}
	if _, err := os.Lstat(filepath.Join(dir, "a/link")); err == nil {
		t.Fatal("absolute symlink kept")
	}
}

// TestExecutorBusybox runs two steps with Docker and checks that the cache survives
// between runs. Skipped where Docker is unavailable (run it on the dev server).
func TestExecutorBusybox(t *testing.T) {
	if exec.Command("docker", "info").Run() != nil {
		t.Skip("docker not available")
	}
	r := NewExecutor(t.TempDir())
	os.MkdirAll(filepath.Dir(r.UploadPath("demo")), 0o755)
	os.WriteFile(r.UploadPath("demo"), tgz(t, map[string]string{"hello.txt": "hello"}), 0o644)
	spec, err := Parse([]byte("name: demo\nrunner: {memory: 128m, cpus: 1}\nsteps:\n  - {name: one, image: busybox, run: 'cat hello.txt && echo $(( $(cat .cache/n 2>/dev/null || echo 0) + 1 )) > .cache/n', cache: [.cache]}\n  - {name: two, image: busybox, run: 'cat .cache/n'}\n"))
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	log := func(l string) { out = append(out, l) }
	for i := 0; i < 2; i++ {
		out = nil
		res, err := r.Run(context.Background(), RunRequest{RunID: "t", Pipeline: "demo", Source: Source{Type: "upload"}, Spec: *spec, Version: "v1"}, log)
		if err != nil || len(res.Steps) != 2 {
			t.Fatalf("%v %v", err, out)
		}
	}
	if !strings.Contains(strings.Join(out, "\n"), "\n2") {
		t.Fatalf("cache not kept: %v", out)
	}
	bad, _ := Parse([]byte("name: demo\nsteps:\n  - {name: fail, image: busybox, run: 'exit 3'}\n  - {name: after, image: busybox, run: 'true'}\n"))
	res, err := r.Run(context.Background(), RunRequest{RunID: "t2", Pipeline: "demo", Source: Source{Type: "upload"}, Spec: *bad}, log)
	if err == nil || res.Steps[0].Status != "failed" || res.Steps[1].Status != "skipped" {
		t.Fatalf("failure: %v %+v", err, res.Steps)
	}
}
