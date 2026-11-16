package panel

import (
	"strings"
	"testing"

	"github.com/wangsirco/berth/internal/change"
)

// internalKinds have no command of their own: they are only undo steps or upload finishers.
var internalKinds = map[string]bool{"file.restore_modes": true, "file.upload": true}

func TestEveryKindHasCLIAndMessages(t *testing.T) {
	for _, k := range change.All() {
		if strings.Contains(k.Name, "test") {
			continue
		}
		if k.CLI == "" && !internalKinds[k.Name] {
			t.Errorf("%s: no CLI template (every change needs an equivalent command)", k.Name)
		}
		if k.Perm == "" && k.Name != "token.revoke" {
			t.Errorf("%s: no permission", k.Name)
		}
		keys := []string{"kind." + k.Name, "action." + k.Name}
		for _, l := range Locales {
			for _, key := range keys {
				if _, ok := messages[l][key]; !ok {
					t.Errorf("%s: missing %s in %s", k.Name, key, l)
				}
			}
		}
	}
	for _, l := range Locales {
		for key := range messages["zh-CN"] {
			if _, ok := messages[l][key]; !ok {
				t.Errorf("%s missing %s", l, key)
			}
		}
	}
}
