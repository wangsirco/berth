package pipeline

import (
	"strings"
	"testing"
)

func TestSetImageKeepsTheRest(t *testing.T) {
	src := `# Acme
services:
  web:
    image: 192.0.2.10:5000/acme/web:r11   # pinned by Berth
    restart: unless-stopped
  db:
    image: postgres:17
`
	out, old, err := SetImage([]byte(src), "web", "192.0.2.10:5000/acme/web:r12")
	if err != nil || old != "192.0.2.10:5000/acme/web:r11" {
		t.Fatal(err, old)
	}
	want := strings.Replace(src, ":r11   #", ":r12   #", 1)
	if string(out) != want {
		t.Fatalf("got:\n%s", out)
	}
	if _, _, err := SetImage([]byte(src), "cache", "x"); err == nil {
		t.Fatal("missing service accepted")
	}
	if _, _, err := SetImage([]byte("services:\n  web:\n    image: ${WEB_IMAGE}\n"), "web", "x"); err == nil {
		t.Fatal("variable image rewritten")
	}
	quoted, _, _ := SetImage([]byte("services:\n  web:\n    image: \"a:1\"\n"), "web", "a:2")
	if string(quoted) != "services:\n  web:\n    image: a:2\n" {
		t.Fatalf("quoted: %q", quoted)
	}
}
