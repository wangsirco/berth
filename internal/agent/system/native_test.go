package system

import (
	"strings"
	"testing"
)

func TestNativePorts(t *testing.T) {
	p := NativePorts("acme")
	if p[0] < 20000 || p[1] > 29999 || p[1] != p[0]+1 || p[0]%2 != 0 {
		t.Fatal(p)
	}
	if NativePorts("acme") != p {
		t.Fatal("not stable")
	}
}

func TestNativeUnit(t *testing.T) {
	s := NativeSpec{Name: "web", Kind: "node", Version: "24", Start: "npx next start -p $PORT", MemMB: 1024, CPUs: 2}
	u := s.unitText("/opt/berth/mise/data/installs/node/24.11.0")
	for _, want := range []string{"User=site-web", "WorkingDirectory=/srv/berth/projects/web/run-%i", "Environment=PORT=%i", "ProtectSystem=strict",
		"ReadWritePaths=/srv/berth/projects/web/shared /srv/berth/projects/web/releases", "NoNewPrivileges=yes", "MemoryMax=1024M", "CPUQuota=200%",
		"EnvironmentFile=-/etc/berth/projects/web.env", "PATH=/opt/berth/mise/data/installs/node/24.11.0/bin:"} {
		if !strings.Contains(u, want) {
			t.Errorf("missing %q in\n%s", want, u)
		}
	}
}

func TestNativeValidate(t *testing.T) {
	ok := NativeSpec{Name: "web", Kind: "node", Version: "24", Start: "node x.js", Source: NativeSource{Type: "dir", Path: "/srv/code/web"}, Shared: []string{"data", ".next/cache"}}
	if err := ok.Validate(); err != nil || ok.Health != "/" {
		t.Fatal(err, ok.Health)
	}
	bad := []NativeSpec{
		{Name: "Web", Kind: "node", Version: "24", Start: "x", Source: NativeSource{Type: "dir", Path: "/a"}},
		{Name: "web", Kind: "node", Version: "24", Start: "x", Source: NativeSource{Type: "dir", Path: "relative"}},
		{Name: "web", Kind: "node", Version: "24", Start: "x", Source: NativeSource{Type: "dir", Path: "/srv/berth/projects/other"}},
		{Name: "web", Kind: "node", Version: "24", Start: "x", Source: NativeSource{Type: "dir", Path: "/a"}, Shared: []string{"../etc"}},
		{Name: "web", Kind: "node", Version: "24", Start: "x", Source: NativeSource{Type: "dir", Path: "/a"}, Shared: []string{"/etc"}},
		{Name: "web", Kind: "node", Version: "24", Start: "x\nrm -rf /", Source: NativeSource{Type: "dir", Path: "/a"}},
		{Name: "web", Kind: "node", Version: "24", Start: "x", Source: NativeSource{Type: "git", URL: "file:///etc"}},
		{Name: "web", Kind: "php", Version: "24", Start: "x", Source: NativeSource{Type: "dir", Path: "/a"}},
	}
	for i, b := range bad {
		if b.Validate() == nil {
			t.Errorf("bad spec %d accepted", i)
		}
	}
	g := NativeSpec{Name: "web", Kind: "node", Version: "24", Start: "x", Source: NativeSource{Type: "git", URL: "https://github.com/a/b.git"}}
	if err := g.Validate(); err != nil || g.Source.Branch != "main" {
		t.Fatal(err, g.Source.Branch)
	}
}
