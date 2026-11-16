package panel

import "testing"

func TestVersionUses(t *testing.T) {
	for _, c := range []struct {
		want, inst string
		newest, ok bool
	}{
		{"22", "22.23.5", false, true}, {"22", "220.1.0", false, false}, {"3.12", "3.12.7", false, true},
		{"3.1", "3.12.7", false, false}, {"20.18.1", "20.18.1", false, true}, {"lts", "22.23.5", true, true}, {"lts", "20.1.0", false, false},
	} {
		if got := versionUses(c.want, c.inst, c.newest); got != c.ok {
			t.Errorf("%s vs %s: %v", c.want, c.inst, got)
		}
	}
}
