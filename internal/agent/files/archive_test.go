package files

import (
	"archive/tar"
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestArchiveRoundTrip(t *testing.T) {
	for _, ext := range []string{"tar.zst", "tar.gz", "zip"} {
		root := t.TempDir()
		src := filepath.Join(root, "a")
		os.MkdirAll(filepath.Join(src, "y"), 0o755)
		os.WriteFile(filepath.Join(src, "x.txt"), []byte("x"), 0o644)
		os.WriteFile(filepath.Join(src, "y", "z.txt"), []byte("zz"), 0o644)
		arc := filepath.Join(root, "a."+ext)
		if err := Archive(context.Background(), root, []string{src}, arc, func(string) {}); err != nil {
			t.Fatalf("%s: %v", ext, err)
		}
		out := filepath.Join(root, "out")
		if err := Extract(context.Background(), root, arc, out, func(string) {}); err != nil {
			t.Fatalf("%s extract: %v", ext, err)
		}
		if b, _ := os.ReadFile(filepath.Join(out, "a", "y", "z.txt")); string(b) != "zz" {
			t.Fatalf("%s: got %q", ext, b)
		}
	}
}

func TestExtractRefusesEscape(t *testing.T) {
	root := t.TempDir()
	arc := filepath.Join(root, "evil.tar")
	f, _ := os.Create(arc)
	tw := tar.NewWriter(f)
	tw.WriteHeader(&tar.Header{Name: "../escape", Mode: 0o644, Size: 1, Typeflag: tar.TypeReg})
	tw.Write([]byte("x"))
	tw.Close()
	f.Close()
	err := Extract(context.Background(), root, arc, filepath.Join(root, "out"), func(string) {})
	if err == nil || err.Error() != "files.unsafe_archive" {
		t.Fatalf("got %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "escape")); err == nil {
		t.Fatal("file escaped")
	}
}

func TestSearch(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "Notes.md"), []byte("hello berth"), 0o644)
	os.WriteFile(filepath.Join(root, "other.txt"), []byte("nothing"), 0o644)
	got, _ := Search(context.Background(), root, root, "note", "", 0)
	if len(got) != 1 {
		t.Fatalf("name: %v", got)
	}
	got, _ = Search(context.Background(), root, root, "", "BERTH", 0)
	if len(got) != 1 {
		t.Fatalf("content: %v", got)
	}
}
