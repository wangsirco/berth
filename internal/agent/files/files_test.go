package files

import (
	"os"
	"path/filepath"
	"testing"
)

func TestInsideRoot(t *testing.T) {
	root := t.TempDir()
	os.Symlink("/etc", filepath.Join(root, "evil"))
	cases := map[string]bool{
		filepath.Join(root, "a/b"):         true,
		filepath.Join(root, "../x"):        false,
		filepath.Join(root, "evil/passwd"): false,
		root:                               true,
	}
	for p, want := range cases {
		if _, err := resolve(root, p); (err == nil) != want {
			t.Errorf("%s: err=%v want ok=%v", p, err, want)
		}
	}
}

func TestTrashAndRestore(t *testing.T) {
	root := t.TempDir()
	trash := t.TempDir()
	f := filepath.Join(root, "note.txt")
	os.WriteFile(f, []byte("hi"), 0o644)
	m := New(trash)
	id, err := m.Trash(root, []string{f})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(f); !os.IsNotExist(err) {
		t.Fatal("still there")
	}
	if err := m.Restore(id); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(f); string(b) != "hi" {
		t.Fatal("not restored")
	}
}

func TestChunkedUpload(t *testing.T) {
	root := t.TempDir()
	m := New(t.TempDir())
	dst := filepath.Join(root, "big.bin")
	id, _ := m.UploadBegin(root, dst, 10)
	m.UploadChunk(id, 0, []byte("01234"))
	if got, _ := m.UploadStatus(id); got != 5 {
		t.Fatalf("received %d", got)
	}
	if err := m.UploadChunk(id, 7, []byte("x")); err == nil {
		t.Fatal("gap must be rejected")
	}
	m.UploadChunk(id, 5, []byte("56789"))
	if err := m.UploadFinish(id); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(dst); string(b) != "0123456789" {
		t.Fatal(string(b))
	}
}
