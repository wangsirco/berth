package docker

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// AppsRoot is where store apps keep their data.
var AppsRoot = "/srv/berth/apps"

// DataSpec: folders under one app's data root, owned by the user its image runs as.
type DataSpec struct {
	Root  string   `json:"root"`
	Dirs  []string `json:"dirs"`
	Owner string   `json:"owner"` // uid:gid
}

// PrepareData creates the folders (and any missing parents up to Root) and hands them to
// Owner. Root itself stays root-owned but traversable. Existing folders are re-owned only
// at their top level, never recursively.
func PrepareData(d DataSpec) error {
	root := filepath.Clean(d.Root)
	if !strings.HasPrefix(root, AppsRoot+"/") || strings.Count(strings.TrimPrefix(root, AppsRoot+"/"), "/") != 0 {
		return fmt.Errorf("data root outside %s", AppsRoot)
	}
	u, g, ok := strings.Cut(d.Owner, ":")
	uid, err1 := strconv.Atoi(u)
	gid, err2 := strconv.Atoi(g)
	if !ok || err1 != nil || err2 != nil || uid < 0 || gid < 0 {
		return fmt.Errorf("bad owner %q", d.Owner)
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return err
	}
	for _, rel := range d.Dirs {
		p := filepath.Join(root, rel)
		if !strings.HasPrefix(p, root+"/") {
			return fmt.Errorf("data folder %q escapes the root", rel)
		}
		// walk down from root, creating and handing over each level below it
		cur := root
		for _, part := range strings.Split(strings.TrimPrefix(p, root+"/"), "/") {
			cur = filepath.Join(cur, part)
			fi, err := os.Lstat(cur)
			switch {
			case os.IsNotExist(err):
				if err := os.Mkdir(cur, 0o750); err != nil {
					return err
				}
			case err != nil:
				return err
			case fi.Mode()&os.ModeSymlink != 0:
				return fmt.Errorf("%s is a symlink", cur)
			case !fi.IsDir(): // a file mount: leave it alone
				continue
			}
			if err := os.Lchown(cur, uid, gid); err != nil {
				return err
			}
		}
	}
	return nil
}
