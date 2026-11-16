package panel

import "testing"

func TestClaudeAsks(t *testing.T) {
	for s, want := range map[string]bool{
		"Done. Tests pass.":                  false,
		"Should I also update the README?":   true,
		"要不要顺便把测试也补上？":                       true,
		"Which port should it use?**\n\n":    true,
		"I fixed it (was it the cache?) ok.": false,
	} {
		if claudeAsks(s) != want {
			t.Errorf("%q: want %v", s, want)
		}
	}
}
