package cli

import "testing"

func TestRestoreArgs(t *testing.T) {
	o, err := parseRestoreArgs([]string{"x.berthsnap"})
	if err != nil || o.etc != "/etc/berth" || o.data != "/var/lib/berth" || len(o.units) != 2 {
		t.Fatal(o, err)
	}
	o, err = parseRestoreArgs([]string{"--etc", "/etc/berth-dev", "--data", "/var/lib/berth-dev", "--units", "berth-panel-dev", "--password-file", "/run/p", "y.berthsnap"})
	if err != nil || o.units[0] != "berth-panel-dev" || len(o.units) != 1 || o.passFile != "/run/p" || o.file != "y.berthsnap" {
		t.Fatal(o, err)
	}
	if got := o.target("etc/berth/panel.toml"); got != "/etc/berth-dev/panel.toml" {
		t.Fatal(got)
	}
	if got := o.target("var/lib/berth/berth.db"); got != "/var/lib/berth-dev/berth.db" {
		t.Fatal(got)
	}
	for _, bad := range [][]string{{"--etc", "/etc", "f"}, {"--data", "/tmp/x", "f"}, {"--units", "sshd", "f"}, {}, {"a", "b"}} {
		if _, err := parseRestoreArgs(bad); err == nil {
			t.Errorf("%v accepted", bad)
		}
	}
}
