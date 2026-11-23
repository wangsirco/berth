package system

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Berth's block is replaced, everything else in the file stays
func TestWriteManagedBlock(t *testing.T) {
	p := filepath.Join(t.TempDir(), "hosts")
	os.WriteFile(p, []byte("# avahi hosts\n192.168.1.9 printer.local\n"), 0o644)
	if err := writeManagedBlock(p, []string{"192.168.1.20 berth.local"}); err != nil {
		t.Fatal(err)
	}
	if err := writeManagedBlock(p, []string{"192.168.1.20 nas.local"}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	s := string(b)
	if !strings.Contains(s, "printer.local") || !strings.Contains(s, "nas.local") || strings.Contains(s, "berth.local") || strings.Count(s, mdnsBegin) != 1 {
		t.Fatalf("file:\n%s", s)
	}
	writeManagedBlock(p, nil)
	b, _ = os.ReadFile(p)
	if strings.Contains(string(b), mdnsBegin) || !strings.Contains(string(b), "printer.local") {
		t.Fatalf("after removing:\n%s", b)
	}
}
