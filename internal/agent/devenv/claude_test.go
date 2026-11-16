package devenv

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func shadowFixture(t *testing.T) string {
	t.Helper()
	root, _ := filepath.EvalSymlinks(t.TempDir())
	old, oldState := allowedRoots, StateDir
	allowedRoots = append([]string{root + "/"}, allowedRoots...)
	StateDir = filepath.Join(root, "state")
	t.Cleanup(func() { allowedRoots, StateDir = old, oldState })
	dir := filepath.Join(root, "proj")
	os.MkdirAll(filepath.Join(dir, "node_modules", "x"), 0o755)
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one\ntwo\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "gone.txt"), []byte("bye\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "node_modules", "x", "i.js"), []byte("skip\n"), 0o644)
	return dir
}

func TestSnapshotDiffPatch(t *testing.T) {
	ctx := context.Background()
	dir := shadowFixture(t)
	before, err := Snapshot(ctx, "p", dir)
	if err != nil {
		t.Fatal(err)
	}
	// a task edits, adds and deletes
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one\nTWO\nthree\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "new.go"), []byte("package x\n"), 0o644)
	os.Remove(filepath.Join(dir, "gone.txt"))
	os.WriteFile(filepath.Join(dir, "node_modules", "x", "i.js"), []byte("changed\n"), 0o644)
	after, err := Snapshot(ctx, "p", dir)
	if err != nil {
		t.Fatal(err)
	}
	files, d, cut, err := Diff(ctx, "p", dir, before, after)
	if err != nil || cut {
		t.Fatal(err, cut)
	}
	got := map[string]FileStat{}
	for _, f := range files {
		got[f.Path] = f
	}
	if len(files) != 3 || got["a.txt"].Add != 2 || got["a.txt"].Del != 1 || got["new.go"].Add != 1 || got["gone.txt"].Del != 1 {
		t.Fatalf("files: %+v", files)
	}
	if !strings.Contains(d, "+TWO") || strings.Contains(d, "node_modules") {
		t.Fatalf("diff: %s", d)
	}
	// the project's own .git is never involved
	if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
		t.Fatal("made a .git in the project")
	}
	// take it back
	if err := Patch(ctx, "p", dir, before, after, true); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "a.txt")); string(b) != "one\ntwo\n" {
		t.Fatalf("a.txt after revert: %q", b)
	}
	if _, err := os.Stat(filepath.Join(dir, "new.go")); err == nil {
		t.Fatal("new.go still there")
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "gone.txt")); string(b) != "bye\n" {
		t.Fatal("gone.txt not back")
	}
	// and again (undo of the revert)
	if err := Patch(ctx, "p", dir, before, after, false); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "a.txt")); string(b) != "one\nTWO\nthree\n" {
		t.Fatalf("a.txt after re-apply: %q", b)
	}
	// edited since: refuse and leave the files alone
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("someone else\n"), 0o644)
	err = Patch(ctx, "p", dir, before, after, true)
	if err == nil || !strings.Contains(err.Error(), "dev.patch_conflict") {
		t.Fatalf("want conflict, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "new.go")); err != nil {
		t.Fatal("a refused patch changed files")
	}
}

func TestReadEvent(t *testing.T) {
	var out RunOut
	var lines []string
	log := func(s string) { lines = append(lines, s) }
	readEvent([]byte(`{"type":"system","subtype":"init","session_id":"s1"}`), &out, log)
	readEvent([]byte(`{"type":"assistant","message":{"content":[{"type":"text","text":"Looking   at\nit"},{"type":"tool_use","name":"Bash","input":{"command":"npm test"}}]}}`), &out, log)
	readEvent([]byte(`{"type":"result","subtype":"success","is_error":false,"result":"Done.","total_cost_usd":0.12,"num_turns":3,"duration_ms":4000,"session_id":"s1"}`), &out, log)
	readEvent([]byte(`not json`), &out, log)
	if out.SessionID != "s1" || out.Result != "Done." || out.CostUSD != 0.12 || out.Turns != 3 {
		t.Fatalf("%+v", out)
	}
	if len(lines) != 2 || lines[0] != "💬 Looking at it" || lines[1] != "🔧 Bash npm test" {
		t.Fatalf("%q", lines)
	}
}

func TestBridgeName(t *testing.T) {
	a, b := BridgeName("a-very-long-workspace-name-30ch"), BridgeName("other")
	if len(a) > 15 || !strings.HasPrefix(a, "bdev") || a == b || a != BridgeName("a-very-long-workspace-name-30ch") {
		t.Fatal(a, b)
	}
}
