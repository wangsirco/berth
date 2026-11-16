package panel

import (
	"strings"
	"testing"
)

func TestPruneSelect(t *testing.T) {
	tags := []regTag{
		{"shop/web", "v1", "sha256:a", 100}, {"shop/web", "v2", "sha256:b", 200}, {"shop/web", "v3", "sha256:c", 300},
		{"shop/web", "v4", "sha256:d", 400}, {"shop/web", "latest", "sha256:d", 400},
		{"shop/web", "manual", "sha256:x", 0}, // pushed by hand: never pruned
		{"shop/web", "v0", "sha256:c", 50},    // same digest as a kept tag
		{"shop/api", "v1", "sha256:e", 100}, {"shop/api", "v2", "sha256:f", 200},
	}
	got := pruneSelect(tags, 2, map[string]bool{"sha256:a": true}) // a is deployed somewhere
	var names []string
	for _, t := range got {
		names = append(names, t.Repo+":"+t.Tag)
	}
	if strings.Join(names, " ") != "shop/web:v2" {
		t.Fatalf("pruned %v", names)
	}
}
