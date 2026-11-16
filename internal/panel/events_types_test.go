package panel

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Every event type the panel publishes must be one the browser listens for: EventSource
// only delivers named events that have a listener (web/src/lib/events.ts).
func TestEventTypesSubscribed(t *testing.T) {
	web, err := os.ReadFile("../../web/src/lib/events.ts")
	if err != nil {
		t.Skip("web sources not present")
	}
	m := regexp.MustCompile(`(?s)const TYPES = \[(.*?)\]`).FindSubmatch(web)
	if m == nil {
		t.Fatal("TYPES not found")
	}
	listened := string(m[1])
	files, _ := filepath.Glob("*.go")
	files2, _ := filepath.Glob("../tasks/*.go")
	pub := regexp.MustCompile(`(?:event\{Type: |Event\{Type: |publishChange\()"([a-z][a-z._]*)"`)
	for _, f := range append(files, files2...) {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, _ := os.ReadFile(f)
		for _, x := range pub.FindAllSubmatch(b, -1) {
			if typ := string(x[1]); !strings.Contains(listened, "'"+typ+"'") {
				t.Errorf("%s publishes %q, which web/src/lib/events.ts does not listen for", f, typ)
			}
		}
	}
}
