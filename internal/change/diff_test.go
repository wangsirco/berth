package change

import (
	"strings"
	"testing"
)

func TestUnified(t *testing.T) {
	d := Unified("/x", "a\nb\nc\n", "a\nB\nc\nd\n")
	for _, want := range []string{"--- a/x", "+++ b/x", "-b", "+B", "+d", " a"} {
		if !strings.Contains(d, want) {
			t.Fatalf("missing %q in\n%s", want, d)
		}
	}
	if Unified("/x", "same", "same") != "" {
		t.Fatal("no diff expected")
	}
}
