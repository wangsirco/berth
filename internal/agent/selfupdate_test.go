package agent

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

func mkTarball(t *testing.T, path string, files map[string]string) string {
	f, _ := os.Create(path)
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	for n, c := range files {
		tw.WriteHeader(&tar.Header{Name: "berth_1.0/" + n, Mode: 0o755, Size: int64(len(c)), Typeflag: tar.TypeReg})
		tw.Write([]byte(c))
	}
	tw.Close()
	gz.Close()
	f.Close()
	b, _ := os.ReadFile(path)
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func TestInstallBacksUpAndRestores(t *testing.T) {
	root := t.TempDir()
	BinDir, DataDir, UnitDir = filepath.Join(root, "bin"), filepath.Join(root, "data"), filepath.Join(root, "units")
	for _, d := range []string{BinDir, DataDir, UnitDir, updatesDir()} {
		os.MkdirAll(d, 0o755)
	}
	os.WriteFile(filepath.Join(BinDir, "berth"), []byte("old-agent"), 0o755)
	os.WriteFile(filepath.Join(BinDir, "berth-panel"), []byte("old-panel"), 0o755)
	os.WriteFile(filepath.Join(DataDir, "berth.db"), []byte("db-v1"), 0o600)
	tb := filepath.Join(updatesDir(), "r.tar.gz")
	sum := mkTarball(t, tb, map[string]string{"berth": "new-agent", "berth-panel": "new-panel", "berth-panel.service": "[Unit]", "evil.sh": "rm -rf /"})
	if _, err := Install(tb, "00", filepath.Join(DataDir, "rb0")); err == nil {
		t.Fatal("bad checksum accepted")
	}
	rb := filepath.Join(DataDir, "rb")
	names, err := Install(tb, sum, rb)
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 3 {
		t.Fatalf("installed %v (evil.sh must be ignored)", names)
	}
	if b, _ := os.ReadFile(filepath.Join(BinDir, "berth-panel")); string(b) != "new-panel" {
		t.Fatal("not installed")
	}
	if _, err := os.Stat(filepath.Join(BinDir, "evil.sh")); err == nil {
		t.Fatal("unknown file installed")
	}
	os.WriteFile(filepath.Join(DataDir, "berth.db"), []byte("db-v2-migrated"), 0o600)
	Restore(rb, true)
	if b, _ := os.ReadFile(filepath.Join(BinDir, "berth-panel")); string(b) != "old-panel" {
		t.Fatal("binary not restored")
	}
	if b, _ := os.ReadFile(filepath.Join(DataDir, "berth.db")); string(b) != "db-v1" {
		t.Fatal("database not restored")
	}
}
