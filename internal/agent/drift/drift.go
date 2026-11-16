// Package drift notices when files Berth manages are changed by someone else (方案 §4.4
// 外部修改检测). Whenever Berth writes such a file it calls Track, which keeps a copy and its
// hash; Check compares the files on disk with those copies. The panel shows what differs
// and offers to adopt the file as it is now, or to put Berth's version back.
package drift

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

var Dir = "/var/lib/berth/drift"

type Entry struct {
	Path   string `json:"path"`
	Kind   string `json:"kind"`   // compose | sshd | sysctl | zram | resolver | autoupdate | docker
	Object string `json:"object"` // e.g. the compose project
	Hash   string `json:"hash"`   // "" = Berth expects the file to be absent
	At     int64  `json:"at"`
}

type Drift struct {
	Entry
	State string `json:"state"` // changed | missing | appeared
	Since int64  `json:"since"`
}

var (
	mu    sync.Mutex
	index map[string]Entry
	seen  = map[string]int64{} // path → when the difference was first noticed
)

func indexFile() string { return filepath.Join(Dir, "index.json") }
func copyFile(path string) string {
	h := sha256.Sum256([]byte(path))
	return filepath.Join(Dir, "files", hex.EncodeToString(h[:12]))
}

func load() {
	if index != nil {
		return
	}
	index = map[string]Entry{}
	if b, err := os.ReadFile(indexFile()); err == nil {
		json.Unmarshal(b, &index)
	}
}

func save() {
	os.MkdirAll(Dir, 0o700)
	b, _ := json.MarshalIndent(index, "", " ")
	tmp := indexFile() + ".tmp"
	if os.WriteFile(tmp, b, 0o600) == nil {
		os.Rename(tmp, indexFile())
	}
}

func hashOf(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// Track records the file as Berth just wrote it (a missing file means Berth removed it).
func Track(path, kind, object string) {
	mu.Lock()
	defer mu.Unlock()
	load()
	e := Entry{Path: path, Kind: kind, Object: object, At: time.Now().Unix()}
	b, err := os.ReadFile(path)
	if err == nil {
		e.Hash = hashOf(b)
		os.MkdirAll(filepath.Join(Dir, "files"), 0o700)
		os.WriteFile(copyFile(path), b, 0o600)
	} else {
		os.Remove(copyFile(path))
	}
	index[path] = e
	delete(seen, path)
	save()
}

// Untrack forgets a file (its object was deleted through Berth).
func Untrack(path string) {
	mu.Lock()
	defer mu.Unlock()
	load()
	if _, ok := index[path]; ok {
		delete(index, path)
		delete(seen, path)
		os.Remove(copyFile(path))
		save()
	}
}

// UntrackPrefix forgets every file below dir.
func UntrackPrefix(dir string) {
	mu.Lock()
	defer mu.Unlock()
	load()
	changed := false
	for p := range index {
		if p == dir || len(p) > len(dir) && p[:len(dir)+1] == dir+"/" {
			delete(index, p)
			os.Remove(copyFile(p))
			changed = true
		}
	}
	if changed {
		save()
	}
}

// Check lists the tracked files that are not as Berth left them.
func Check() []Drift {
	runBefore()
	mu.Lock()
	defer mu.Unlock()
	load()
	out := []Drift{}
	now := time.Now().Unix()
	for p, e := range index {
		b, err := os.ReadFile(p)
		state := ""
		switch {
		case err != nil && e.Hash != "":
			state = "missing"
		case err == nil && e.Hash == "":
			state = "appeared"
		case err == nil && hashOf(b) != e.Hash:
			state = "changed"
		}
		if state == "" {
			delete(seen, p)
			continue
		}
		if seen[p] == 0 {
			seen[p] = now
		}
		out = append(out, Drift{Entry: e, State: state, Since: seen[p]})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// Versions returns what Berth wrote and what is there now ("" for absent).
func Versions(path string) (berth, now string, err error) {
	mu.Lock()
	defer mu.Unlock()
	load()
	e, ok := index[path]
	if !ok {
		return "", "", errors.New("not a file Berth manages")
	}
	if e.Hash != "" {
		b, err := os.ReadFile(copyFile(path))
		if err != nil {
			return "", "", errors.New("Berth's copy is gone")
		}
		berth = string(b)
	}
	if b, err := os.ReadFile(path); err == nil {
		now = string(b)
	}
	return berth, now, nil
}

// Accept takes the file as it is now as Berth's version.
func Accept(path string) error {
	mu.Lock()
	e, ok := index[path]
	mu.Unlock()
	if !ok {
		return errors.New("not a file Berth manages")
	}
	Track(path, e.Kind, e.Object)
	return nil
}

// restoreHooks re-apply a file after it was put back (reload a service, …).
var (
	hookMu       sync.Mutex
	restoreHooks = map[string]func(ctx context.Context, path string) error{}
)

// OnRestore registers what to do after a file of this kind is put back.
func OnRestore(kind string, fn func(ctx context.Context, path string) error) {
	hookMu.Lock()
	restoreHooks[kind] = fn
	hookMu.Unlock()
}

// Restore writes Berth's version back (or removes the file when Berth had removed it) and
// runs the kind's hook. The changed file is kept next to it as <name>.external.
func Restore(ctx context.Context, path string) error {
	berth, now, err := Versions(path)
	if err != nil {
		return err
	}
	mu.Lock()
	e := index[path]
	mu.Unlock()
	mode := os.FileMode(0o644)
	if fi, err := os.Stat(path); err == nil {
		mode = fi.Mode().Perm()
	}
	if now != "" {
		os.WriteFile(path+".external", []byte(now), 0o600)
	}
	if e.Hash == "" {
		os.Remove(path)
	} else {
		tmp := path + ".berth-tmp"
		if err := os.WriteFile(tmp, []byte(berth), mode); err != nil {
			return err
		}
		if err := os.Rename(tmp, path); err != nil {
			return err
		}
	}
	mu.Lock()
	delete(seen, path)
	mu.Unlock()
	hookMu.Lock()
	fn := restoreHooks[e.Kind]
	hookMu.Unlock()
	if fn != nil {
		return fn(ctx, path)
	}
	return nil
}

var before []func()

// BeforeCheck registers a function run before each check (to settle in-flight changes).
func BeforeCheck(fn func()) { before = append(before, fn) }

func runBefore() {
	for _, f := range before {
		f()
	}
}
