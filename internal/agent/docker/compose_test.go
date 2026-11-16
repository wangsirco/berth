package docker

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMaskEnv(t *testing.T) {
	got := maskEnv("A=1\nSECRET_KEY=abc\n# c\nDB_PASSWORD=x\n")
	if got != "A=1\nSECRET_KEY=••••\n# c\nDB_PASSWORD=••••\n" {
		t.Fatalf("%q", got)
	}
}

func TestWriteKeepsHistoryAndModes(t *testing.T) {
	dir := t.TempDir()
	if err := WriteProject(dir, "", "services: {}\n", "A=1\n"); err != nil {
		t.Fatal(err)
	}
	if err := WriteProject(dir, "", "services: {a: {image: x}}\n", "A=2\n"); err != nil {
		t.Fatal(err)
	}
	p, err := ReadProject(dir, "")
	if err != nil || p.Compose != "services: {a: {image: x}}\n" || p.Env != "A=2\n" {
		t.Fatalf("%+v %v", p, err)
	}
	st, _ := os.Stat(filepath.Join(dir, ".env"))
	if st.Mode().Perm() != 0o600 {
		t.Fatalf(".env mode %v", st.Mode().Perm())
	}
	hist, _ := os.ReadDir(filepath.Join(dir, ".berth", "history"))
	if len(hist) != 1 {
		t.Fatalf("history entries %d", len(hist))
	}
	old, _ := os.ReadFile(filepath.Join(dir, ".berth", "history", hist[0].Name(), "compose.yaml"))
	if string(old) != "services: {}\n" {
		t.Fatalf("history content %q", old)
	}
}

func TestProjectNames(t *testing.T) {
	for n, ok := range map[string]bool{"n8n": true, "my-app_2": true, "": false, "Bad": false, "../x": false, "-x": false} {
		if ValidProjectName(n) != ok {
			t.Errorf("%q", n)
		}
	}
}
