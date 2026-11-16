package docker

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func TestPrepareData(t *testing.T) {
	old := AppsRoot
	AppsRoot = t.TempDir()
	defer func() { AppsRoot = old }()
	uid, gid := os.Getuid(), os.Getgid()
	own := itoa(uid) + ":" + itoa(gid)
	root := filepath.Join(AppsRoot, "n8n")
	if err := PrepareData(DataSpec{Root: root, Dirs: []string{"data", "a/b"}, Owner: own}); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{"data", "a", "a/b"} {
		if fi, err := os.Stat(filepath.Join(root, d)); err != nil || !fi.IsDir() {
			t.Fatal(d, err)
		}
	}
	os.Symlink("/etc", filepath.Join(root, "evil"))
	for _, bad := range []DataSpec{
		{Root: "/etc", Dirs: []string{"x"}, Owner: own},
		{Root: AppsRoot + "/x/../../etc", Dirs: []string{"x"}, Owner: own},
		{Root: root, Dirs: []string{"../other"}, Owner: own},
		{Root: root, Dirs: []string{"evil/x"}, Owner: own},
		{Root: root, Dirs: []string{"x"}, Owner: "root"},
	} {
		if PrepareData(bad) == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
}

func itoa(n int) string { return strconv.Itoa(n) }
