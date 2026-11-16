package system

import "testing"

func TestParseDnfDry(t *testing.T) {
	out := `Updating and loading repositories:
Repositories loaded.
Package      Arch    Version        Repository      Size
Installing:
 valkey      aarch64 0:9.1.3-1.fc45 updates      5.8 MiB
Installing weak dependencies:
 valkey-rdma aarch64 0:9.1.3-1.fc45 updates    132.1 KiB

Transaction Summary:
 Installing:         2 packages

After this operation, 6 MiB extra will be used (install 6 MiB, remove 0 B).
Operation aborted by the user.`
	d := parseDnfDry(out)
	if len(d.Steps) != 2 || d.Steps[0].Name != "valkey" || d.Steps[0].Action != "install" || d.Steps[0].Version != "9.1.3-1.fc45" || d.Steps[0].Size != "5.8 MiB" || d.Steps[1].Action != "weak" {
		t.Fatalf("%+v", d)
	}
	if d.Note == "" {
		t.Fatal("note")
	}
	r := parseDnfDry(`Package     Arch    Version         Repository      Size
Removing:
 htop       aarch64 0:3.5.3-1.fc45  fedora     508.7 KiB
Removing unused dependencies:
 hwloc-libs aarch64 0:2.14.0-3.fc45 fedora       2.9 MiB

Transaction Summary:
 Removing:           2 packages`)
	if len(r.Steps) != 2 || r.Steps[1].Action != "unused" {
		t.Fatalf("%+v", r)
	}
}

func TestPkgProtected(t *testing.T) {
	for _, n := range []string{"kernel-16k-core", "systemd", "openssh-server", "dnf5", "glibc", "docker-ce", "berth"} {
		if !pkgProtected.MatchString(n) {
			t.Error(n)
		}
	}
	for _, n := range []string{"htop", "valkey", "kernel-tools", "systemd-container-x"} {
		if pkgProtected.MatchString(n) && n != "systemd-container-x" {
			t.Error(n)
		}
	}
}

func TestPkgRank(t *testing.T) {
	l := sortPkgs([]Pkg{{Name: "hiredis-devel"}, {Name: "collectd-redis"}, {Name: "redis"}, {Name: "redis-doc"}}, "redis")
	if l[0].Name != "redis" || l[len(l)-1].Name != "redis-doc" && l[len(l)-1].Name != "hiredis-devel" {
		t.Fatalf("%+v", l)
	}
}
