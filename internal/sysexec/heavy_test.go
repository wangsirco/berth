package sysexec

import "testing"

func TestHeavyThroughEnv(t *testing.T) {
	for _, c := range []struct {
		name string
		args []string
		want bool
	}{
		{"dnf", []string{"-y", "install", "x"}, true},
		{"env", []string{"DEBIAN_FRONTEND=noninteractive", "apt-get", "-y", "install", "acl"}, true},
		{"/usr/bin/env", []string{"A=1", "B=2", "/usr/bin/apt-get", "upgrade"}, true},
		{"env", []string{"LC_ALL=C", "ls"}, false},
		{"systemctl", []string{"restart", "x"}, false},
	} {
		if got := isHeavy(c.name, c.args); got != c.want {
			t.Fatalf("%s %v: %v", c.name, c.args, got)
		}
	}
}
