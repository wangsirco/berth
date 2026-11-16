package caddy

import (
	"strings"
	"testing"
)

func TestWAFVariants(t *testing.T) {
	for img, want := range map[string]bool{"berth/caddy:2.11-2": true, "berth/caddy:2.11-1": true, "berth/caddy:2.11-3": false, "berth/caddy:2.11-3-waf": true,
		"berth/caddy:2.11-10": false, "berth/caddy:2.12-1": false, "caddy:2": false, "": false} {
		if HasWAF(img) != want {
			t.Errorf("HasWAF(%q) = %v", img, !want)
		}
	}
	if !strings.Contains(ComposeFor(true), "image: "+ImageWAF) || !strings.Contains(ComposeFor(true), `WAF: "1"`) || !strings.Contains(Compose(), `WAF: "0"`) {
		t.Error(ComposeFor(true))
	}
	if !strings.Contains(Dockerfile, `[ "$WAF" = 1 ]`) || !HasWAF(ImageWAF) || HasWAF(Image) {
		t.Error("dockerfile/images")
	}
}
