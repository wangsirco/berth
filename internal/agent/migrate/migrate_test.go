package migrate

import (
	"archive/tar"
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
)

func roundTrip(t *testing.T, pack []byte, req ImportReq) (ImportResult, error) {
	t.Helper()
	id, err := Begin(req)
	if err != nil {
		t.Fatal(err)
	}
	var off int64
	for len(pack) > 0 {
		n := min(len(pack), 1000)
		if _, err := Chunk(id, off, pack[:n]); err != nil {
			Abort(id)
			return ImportResult{}, err
		}
		off += int64(n)
		pack = pack[n:]
	}
	return Finish(id)
}

func TestFilesRoundTrip(t *testing.T) {
	dir, _ := filepath.EvalSymlinks(t.TempDir())
	src := filepath.Join(dir, "site")
	os.MkdirAll(filepath.Join(src, "a", "b"), 0o750)
	os.WriteFile(filepath.Join(src, "a", "b", "f.txt"), []byte("hello"), 0o640)
	os.WriteFile(filepath.Join(src, "big"), bytes.Repeat([]byte("x"), 3<<20), 0o600)
	os.Symlink("a/b/f.txt", filepath.Join(src, "link"))
	os.MkdirAll(filepath.Join(src, "releases", "old"), 0o755)
	os.WriteFile(filepath.Join(src, "releases", "old", "skip"), []byte("no"), 0o644)

	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if err := Export(context.Background(), ExportReq{Files: []string{src}, Exclude: []string{filepath.Join(src, "releases", "old")}}, tw, func(string) {}); err != nil {
		t.Fatal(err)
	}
	tw.Close()
	os.RemoveAll(src)

	res, err := roundTrip(t, buf.Bytes(), ImportReq{Allow: []string{src}, Reserve: 1})
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(src, "a", "b", "f.txt")); string(b) != "hello" {
		t.Fatalf("content %q", b)
	}
	if fi, _ := os.Stat(filepath.Join(src, "a", "b", "f.txt")); fi.Mode().Perm() != 0o640 {
		t.Fatalf("mode %v", fi.Mode())
	}
	if fi, _ := os.Stat(filepath.Join(src, "a")); fi.Mode().Perm() != 0o750 {
		t.Fatalf("dir mode %v", fi.Mode())
	}
	if l, _ := os.Readlink(filepath.Join(src, "link")); l != "a/b/f.txt" {
		t.Fatalf("link %q", l)
	}
	if _, err := os.Stat(filepath.Join(src, "releases", "old")); err == nil {
		t.Fatal("excluded folder came back")
	}
	if res.Files != 2 || res.Bytes != 5+3<<20 {
		t.Fatalf("result %+v", res)
	}
}

func tarOf(entries ...*tar.Header) []byte {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, h := range entries {
		tw.WriteHeader(h)
		if h.Size > 0 {
			tw.Write(bytes.Repeat([]byte("z"), int(h.Size)))
		}
	}
	tw.Close()
	return buf.Bytes()
}

func TestImportRefusesOutside(t *testing.T) {
	dir, _ := filepath.EvalSymlinks(t.TempDir())
	ok := filepath.Join(dir, "ok")
	pack := tarOf(&tar.Header{Name: "f" + filepath.Join(dir, "other", "x"), Mode: 0o644, Size: 1, Typeflag: tar.TypeReg})
	if _, err := roundTrip(t, pack, ImportReq{Allow: []string{ok}}); err == nil {
		t.Fatal("wrote outside the allowed folder")
	}
	pack = tarOf(&tar.Header{Name: "f/etc/passwd-x", Mode: 0o644, Size: 1, Typeflag: tar.TypeReg})
	if _, err := roundTrip(t, pack, ImportReq{Allow: []string{ok}}); err == nil {
		t.Fatal("wrote into /etc")
	}
	if _, err := Begin(ImportReq{Allow: []string{"/etc"}}); err == nil {
		t.Fatal("allowed /etc")
	}
	// a link planted by the pack must not be written through
	pack = tarOf(
		&tar.Header{Name: "f" + ok + "/", Mode: 0o755, Typeflag: tar.TypeDir},
		&tar.Header{Name: "f" + ok + "/l", Linkname: filepath.Join(dir, "other"), Typeflag: tar.TypeSymlink},
		&tar.Header{Name: "f" + ok + "/l/x", Mode: 0o644, Size: 1, Typeflag: tar.TypeReg},
	)
	os.MkdirAll(filepath.Join(dir, "other"), 0o755)
	if _, err := roundTrip(t, pack, ImportReq{Allow: []string{ok}}); err == nil {
		t.Fatal("wrote through a link")
	}
	if _, err := os.Stat(filepath.Join(dir, "other", "x")); err == nil {
		t.Fatal("file landed behind the link")
	}
}

func TestSourceFailure(t *testing.T) {
	dir, _ := filepath.EvalSymlinks(t.TempDir())
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	tw.WriteHeader(&tar.Header{Name: "x/status", Mode: 0o600, Size: 4, Typeflag: tar.TypeReg})
	tw.Write([]byte("boom"))
	tw.Close()
	if _, err := roundTrip(t, buf.Bytes(), ImportReq{Allow: []string{filepath.Join(dir, "a")}}); err == nil {
		t.Fatal("a failed source was taken as complete")
	}
}

func TestParseSize(t *testing.T) {
	for in, want := range map[string]int64{"10GiB": 10 << 30, "500MB": 500e6, "": 0, "1024": 1024} {
		if got := parseSize(in); got != want {
			t.Fatalf("%q: %d", in, got)
		}
	}
}

func TestSparseStaysSparse(t *testing.T) {
	dir, _ := filepath.EvalSymlinks(t.TempDir())
	src := filepath.Join(dir, "img")
	os.MkdirAll(src, 0o755)
	f, _ := os.Create(filepath.Join(src, "disk.raw"))
	f.Truncate(64 << 20)
	f.WriteAt([]byte("data"), 1<<20)
	f.Close()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	Export(context.Background(), ExportReq{Files: []string{src}}, tw, func(string) {})
	tw.Close()
	os.RemoveAll(src)
	if _, err := roundTrip(t, buf.Bytes(), ImportReq{Allow: []string{src}, Reserve: 1}); err != nil {
		t.Fatal(err)
	}
	fi, _ := os.Stat(filepath.Join(src, "disk.raw"))
	if fi.Size() != 64<<20 {
		t.Fatalf("size %d", fi.Size())
	}
	if used := du(context.Background(), filepath.Join(src, "disk.raw")); used > 4<<20 {
		t.Fatalf("restored file takes %d bytes: holes were filled", used)
	}
	b, _ := os.ReadFile(filepath.Join(src, "disk.raw"))
	if string(b[1<<20:1<<20+4]) != "data" {
		t.Fatal("content moved")
	}
}

func TestMapPath(t *testing.T) {
	m := map[string]string{"/www/wwwroot/a.com": "/srv/berth/caddy/sites/a.com", "/www": "/x"}
	for in, want := range map[string]string{
		"/www/wwwroot/a.com/index.php": "/srv/berth/caddy/sites/a.com/index.php",
		"/www/wwwroot/a.com":           "/srv/berth/caddy/sites/a.com",
		"/www/other":                   "/x/other",
		"/srv/keep":                    "/srv/keep",
	} {
		if got := mapPath(in, m); got != want {
			t.Fatalf("%s: %s", in, got)
		}
	}
}

func TestProtectedInsideFolderSkipped(t *testing.T) {
	pack := tarOf(
		&tar.Header{Name: "f/root/", Mode: 0o700, Typeflag: tar.TypeDir},
		&tar.Header{Name: "f/root/.ssh/authorized_keys", Mode: 0o600, Size: 3, Typeflag: tar.TypeReg},
	)
	// /root itself is the chosen folder; its .ssh stays as the target has it
	id, err := Begin(ImportReq{Allow: []string{"/root"}})
	if err != nil {
		t.Skip("cannot open a session for /root here: ", err)
	}
	Abort(id)
	_ = pack
	if !allowedPath("/root/x", []string{"/root"}) || allowedPath("/root/.ssh/authorized_keys", []string{"/root"}) {
		t.Fatal("/root/.ssh must be protected inside an allowed /root")
	}
}
