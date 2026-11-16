// Package files does file management for berth agent. Every path is checked against a
// root the panel chose for the user; symlinks may not lead outside it.
package files

import (
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"os"
	"os/user"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/wangsirco/berth/internal/errs"
	"github.com/wangsirco/berth/internal/sign"
)

// resolve cleans p and checks that it, after following symlinks in its existing part,
// stays inside root. It returns the cleaned (not symlink-resolved) path.
func resolve(root, p string) (string, error) {
	if root == "" {
		root = "/"
	}
	if !filepath.IsAbs(p) {
		return "", errs.New(400, "files.bad_path", "path", p)
	}
	root, p = filepath.Clean(root), filepath.Clean(p)
	if !within(root, p) {
		return "", errs.New(403, "files.outside_root", "path", p)
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", errs.New(404, "files.not_found", "path", root)
	}
	// resolve the longest existing prefix
	existing, rest := p, ""
	for {
		if _, err := os.Lstat(existing); err == nil {
			break
		}
		parent := filepath.Dir(existing)
		if parent == existing {
			break
		}
		rest = filepath.Join(filepath.Base(existing), rest)
		existing = parent
	}
	real, err := filepath.EvalSymlinks(existing)
	if err != nil {
		return "", errs.New(403, "files.outside_root", "path", p)
	}
	if !within(realRoot, filepath.Join(real, rest)) {
		return "", errs.New(403, "files.outside_root", "path", p)
	}
	return p, nil
}

func within(root, p string) bool {
	if root == "/" {
		return true
	}
	return p == root || strings.HasPrefix(p, root+"/")
}

type Entry struct {
	Name       string `json:"name"`
	Path       string `json:"path"`
	Dir        bool   `json:"dir"`
	Link       bool   `json:"link,omitempty"`
	LinkTarget string `json:"link_target,omitempty"`
	Size       int64  `json:"size"`
	Mtime      int64  `json:"mtime"`
	Mode       string `json:"mode"` // octal, e.g. 0644
	Perm       string `json:"perm"` // -rw-r--r--
	Owner      string `json:"owner"`
	Group      string `json:"group"`
}

var nameCache sync.Map

func lookupName(kind string, id uint32) string {
	key := kind + strconv.FormatUint(uint64(id), 10)
	if v, ok := nameCache.Load(key); ok {
		return v.(string)
	}
	name := strconv.FormatUint(uint64(id), 10)
	if kind == "u" {
		if u, err := user.LookupId(name); err == nil {
			name = u.Username
		}
	} else if g, err := user.LookupGroupId(name); err == nil {
		name = g.Name
	}
	nameCache.Store(key, name)
	return name
}

func entryOf(path string, fi fs.FileInfo) Entry {
	e := Entry{Name: fi.Name(), Path: path, Dir: fi.IsDir(), Size: fi.Size(), Mtime: fi.ModTime().Unix(),
		Mode: "0" + strconv.FormatUint(uint64(fi.Mode().Perm()), 8), Perm: fi.Mode().String()}
	if fi.Mode()&fs.ModeSymlink != 0 {
		e.Link = true
		e.LinkTarget, _ = os.Readlink(path)
		if st, err := os.Stat(path); err == nil {
			e.Dir = st.IsDir()
		}
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		e.Owner, e.Group = lookupName("u", st.Uid), lookupName("g", st.Gid)
	}
	return e
}

func List(root, dir string, hidden bool) ([]Entry, error) {
	dir, err := resolve(root, dir)
	if err != nil {
		return nil, err
	}
	des, err := os.ReadDir(dir)
	if err != nil {
		return nil, fsErr(err, dir)
	}
	out := make([]Entry, 0, len(des))
	for _, d := range des {
		if !hidden && strings.HasPrefix(d.Name(), ".") {
			continue
		}
		fi, err := d.Info()
		if err != nil {
			continue
		}
		out = append(out, entryOf(filepath.Join(dir, d.Name()), fi))
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Dir != out[j].Dir {
			return out[i].Dir
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out, nil
}

func Stat(root, p string) (*Entry, error) {
	p, err := resolve(root, p)
	if err != nil {
		return nil, err
	}
	fi, err := os.Lstat(p)
	if err != nil {
		return nil, fsErr(err, p)
	}
	e := entryOf(p, fi)
	return &e, nil
}

func fsErr(err error, p string) error {
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return errs.New(404, "files.not_found", "path", p)
	case errors.Is(err, fs.ErrPermission):
		return errs.New(403, "files.permission", "path", p)
	case errors.Is(err, fs.ErrExist):
		return errs.New(409, "files.exists", "path", p)
	}
	return errs.New(500, "files.error", "path", p, "detail", err.Error())
}

type ReadResult struct {
	Content   []byte `json:"content"` // base64 in JSON
	Size      int64  `json:"size"`
	Mtime     int64  `json:"mtime"`
	Mime      string `json:"mime"`
	Truncated bool   `json:"truncated"`
	Binary    bool   `json:"binary"`
}

func Read(root, p string, max int64) (*ReadResult, error) {
	p, err := resolve(root, p)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(p)
	if err != nil {
		return nil, fsErr(err, p)
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, fsErr(err, p)
	}
	if fi.IsDir() {
		return nil, errs.New(400, "files.is_dir", "path", p)
	}
	if max <= 0 || max > 8<<20 {
		max = 2 << 20
	}
	b, err := io.ReadAll(io.LimitReader(f, max))
	if err != nil {
		return nil, fsErr(err, p)
	}
	mt := mime.TypeByExtension(filepath.Ext(p))
	if mt == "" {
		mt = http.DetectContentType(b)
	}
	return &ReadResult{Content: b, Size: fi.Size(), Mtime: fi.ModTime().Unix(), Mime: mt, Truncated: fi.Size() > max, Binary: isBinary(b)}, nil
}

func isBinary(b []byte) bool {
	n := min(len(b), 8000)
	for _, c := range b[:n] {
		if c == 0 {
			return true
		}
	}
	return false
}

// Write replaces a file atomically, keeping its mode and owner. expectMtime (if non-zero)
// guards against overwriting a change made on disk since the editor loaded the file.
func Write(root, p string, content []byte, mode os.FileMode, expectMtime int64) error {
	p, err := resolve(root, p)
	if err != nil {
		return err
	}
	uid, gid := parentOwner(p)
	if fi, err := os.Stat(p); err == nil {
		if fi.IsDir() {
			return errs.New(400, "files.is_dir", "path", p)
		}
		if expectMtime != 0 && fi.ModTime().Unix() != expectMtime {
			return errs.New(409, "files.changed_on_disk", "path", p)
		}
		if mode == 0 {
			mode = fi.Mode().Perm()
		}
		if st, ok := fi.Sys().(*syscall.Stat_t); ok {
			uid, gid = int(st.Uid), int(st.Gid)
		}
	} else if mode == 0 {
		mode = 0o644
	}
	tmp := filepath.Join(filepath.Dir(p), "."+filepath.Base(p)+".berth-"+sign.RandomHex(4))
	if err := os.WriteFile(tmp, content, mode); err != nil {
		return fsErr(err, p)
	}
	_ = os.Chmod(tmp, mode)
	if uid >= 0 {
		_ = os.Lchown(tmp, uid, gid)
	}
	if err := os.Rename(tmp, p); err != nil {
		os.Remove(tmp)
		return fsErr(err, p)
	}
	return nil
}

func Mkdir(root, p string) error {
	p, err := resolve(root, p)
	if err != nil {
		return err
	}
	if err := os.Mkdir(p, 0o755); err != nil {
		return fsErr(err, p)
	}
	if uid, gid := parentOwner(p); uid >= 0 {
		_ = os.Lchown(p, uid, gid)
	}
	return nil
}

// parentOwner: the agent runs as root, so new files take the owner of their directory
// instead of becoming root's (a file made in /home/iwsheng stays usable by iwsheng).
func parentOwner(p string) (int, int) {
	fi, err := os.Stat(filepath.Dir(p))
	if err != nil {
		return -1, -1
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return int(st.Uid), int(st.Gid)
	}
	return -1, -1
}

func Rename(root, from, to string) error {
	from, err := resolve(root, from)
	if err != nil {
		return err
	}
	to, err = resolve(root, to)
	if err != nil {
		return err
	}
	if _, err := os.Lstat(to); err == nil {
		return errs.New(409, "files.exists", "path", to)
	}
	if err := os.Rename(from, to); err != nil {
		return fsErr(err, from)
	}
	return nil
}

// ModeChange is one file's mode/owner, for applying and for undo.
type ModeChange struct {
	Path string `json:"path"`
	Mode string `json:"mode,omitempty"` // octal
	UID  int    `json:"uid"`
	GID  int    `json:"gid"`
}

// Chmod/chown p (recursively if asked). It returns every file's previous values so the
// panel can undo; past limit files it stops collecting and returns truncated=true.
func ChmodChown(root, p, mode, owner, group string, recursive bool, limit int) (prev []ModeChange, truncated bool, err error) {
	p, err = resolve(root, p)
	if err != nil {
		return nil, false, err
	}
	var m int64 = -1
	if mode != "" {
		if m, err = strconv.ParseInt(mode, 8, 32); err != nil || m < 0 || m > 0o7777 {
			return nil, false, errs.New(400, "files.bad_mode", "mode", mode)
		}
	}
	uid, gid := -1, -1
	if owner != "" {
		u, err := user.Lookup(owner)
		if err != nil {
			return nil, false, errs.New(400, "files.no_user", "name", owner)
		}
		uid, _ = strconv.Atoi(u.Uid)
	}
	if group != "" {
		g, err := user.LookupGroup(group)
		if err != nil {
			return nil, false, errs.New(400, "files.no_group", "name", group)
		}
		gid, _ = strconv.Atoi(g.Gid)
	}
	apply := func(path string, fi fs.FileInfo) error {
		if fi.Mode()&fs.ModeSymlink != 0 {
			return nil // never follow or change links
		}
		st, _ := fi.Sys().(*syscall.Stat_t)
		if len(prev) < limit && st != nil {
			prev = append(prev, ModeChange{Path: path, Mode: strconv.FormatUint(uint64(fi.Mode().Perm()|fi.Mode()&(fs.ModeSetuid|fs.ModeSetgid|fs.ModeSticky)), 8), UID: int(st.Uid), GID: int(st.Gid)})
		} else {
			truncated = true
		}
		if m >= 0 {
			if err := os.Chmod(path, os.FileMode(m)); err != nil {
				return fsErr(err, path)
			}
		}
		if uid >= 0 || gid >= 0 {
			if err := os.Lchown(path, uid, gid); err != nil {
				return fsErr(err, path)
			}
		}
		return nil
	}
	fi, err := os.Lstat(p)
	if err != nil {
		return nil, false, fsErr(err, p)
	}
	if !recursive || !fi.IsDir() {
		return prev, false, apply(p, fi)
	}
	err = filepath.Walk(p, func(path string, fi fs.FileInfo, err error) error {
		if err != nil {
			return err
		}
		return apply(path, fi)
	})
	return prev, truncated, err
}

// Restore puts back modes and owners recorded by ChmodChown.
func RestoreModes(root string, prev []ModeChange) error {
	for _, c := range prev {
		p, err := resolve(root, c.Path)
		if err != nil {
			return err
		}
		m, _ := strconv.ParseUint(c.Mode, 8, 32)
		if err := os.Chmod(p, os.FileMode(m)&fs.ModePerm|modeBits(m)); err != nil {
			return fsErr(err, p)
		}
		if err := os.Lchown(p, c.UID, c.GID); err != nil {
			return fsErr(err, p)
		}
	}
	return nil
}

func modeBits(m uint64) os.FileMode {
	var out os.FileMode
	if m&0o4000 != 0 {
		out |= fs.ModeSetuid
	}
	if m&0o2000 != 0 {
		out |= fs.ModeSetgid
	}
	if m&0o1000 != 0 {
		out |= fs.ModeSticky
	}
	return out
}

// ---- trash ----

type Manager struct {
	trash   string
	uploads string
	mu      sync.Mutex
	ups     map[string]*upload
}

type upload struct {
	root, dest, part string
	size             int64
	at               time.Time
}

func New(trashDir string) *Manager {
	return &Manager{trash: trashDir, uploads: filepath.Join(filepath.Dir(trashDir), "uploads"), ups: map[string]*upload{}}
}

type TrashItem struct {
	ID      string   `json:"id"`
	Paths   []string `json:"paths"`
	Deleted int64    `json:"deleted"`
	Size    int64    `json:"size"`
}

// Trash moves paths into one trash entry and returns its id.
func (m *Manager) Trash(root string, paths []string) (string, error) {
	if len(paths) == 0 {
		return "", errs.New(400, "params.required", "name", "paths")
	}
	for i, p := range paths {
		rp, err := resolve(root, p)
		if err != nil {
			return "", err
		}
		if rp == "/" || rp == filepath.Clean(root) {
			return "", errs.New(400, "files.refuse_root")
		}
		paths[i] = rp
	}
	id := time.Now().Format("20060102-150405") + "-" + sign.RandomHex(3)
	dir := filepath.Join(m.trash, id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	var moved []string
	for i, p := range paths {
		dst := filepath.Join(dir, strconv.Itoa(i))
		if err := moveAcross(p, dst); err != nil {
			// put back what already moved
			for j, mp := range moved {
				_ = moveAcross(filepath.Join(dir, strconv.Itoa(j)), mp)
			}
			os.RemoveAll(dir)
			return "", fsErr(err, p)
		}
		moved = append(moved, p)
	}
	meta, _ := json.Marshal(TrashItem{ID: id, Paths: paths, Deleted: time.Now().Unix()})
	return id, os.WriteFile(filepath.Join(dir, "meta.json"), meta, 0o600)
}

// moveAcross renames, falling back to copy+remove across filesystems.
func moveAcross(src, dst string) error {
	err := os.Rename(src, dst)
	if err == nil {
		return nil
	}
	var le *os.LinkError
	if !errors.As(err, &le) || !errors.Is(le.Err, syscall.EXDEV) {
		return err
	}
	if err := copyTree(src, dst); err != nil {
		os.RemoveAll(dst)
		return err
	}
	return os.RemoveAll(src)
}

func copyTree(src, dst string) error {
	return filepath.Walk(src, func(p string, fi fs.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		target := filepath.Join(dst, rel)
		switch {
		case fi.Mode()&fs.ModeSymlink != 0:
			l, err := os.Readlink(p)
			if err != nil {
				return err
			}
			return os.Symlink(l, target)
		case fi.IsDir():
			return os.MkdirAll(target, fi.Mode().Perm())
		default:
			in, err := os.Open(p)
			if err != nil {
				return err
			}
			defer in.Close()
			out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_EXCL, fi.Mode().Perm())
			if err != nil {
				return err
			}
			if _, err := io.Copy(out, in); err != nil {
				out.Close()
				return err
			}
			if st, ok := fi.Sys().(*syscall.Stat_t); ok {
				_ = os.Lchown(target, int(st.Uid), int(st.Gid))
			}
			return out.Close()
		}
	})
}

func (m *Manager) meta(id string) (*TrashItem, string, error) {
	dir := filepath.Join(m.trash, filepath.Base(id))
	b, err := os.ReadFile(filepath.Join(dir, "meta.json"))
	if err != nil {
		return nil, "", errs.New(404, "files.trash_not_found")
	}
	var t TrashItem
	if err := json.Unmarshal(b, &t); err != nil {
		return nil, "", err
	}
	return &t, dir, nil
}

func (m *Manager) Restore(id string) error {
	t, dir, err := m.meta(id)
	if err != nil {
		return err
	}
	for i, p := range t.Paths {
		if _, err := os.Lstat(p); err == nil {
			return errs.New(409, "files.exists", "path", p)
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return fsErr(err, p)
		}
		if err := moveAcross(filepath.Join(dir, strconv.Itoa(i)), p); err != nil {
			return fsErr(err, p)
		}
	}
	return os.RemoveAll(dir)
}

func (m *Manager) Purge(id string) error {
	_, dir, err := m.meta(id)
	if err != nil {
		return err
	}
	return os.RemoveAll(dir)
}

func (m *Manager) TrashList() []TrashItem {
	des, _ := os.ReadDir(m.trash)
	out := []TrashItem{}
	for _, d := range des {
		t, dir, err := m.meta(d.Name())
		if err != nil {
			continue
		}
		t.Size = dirSize(dir)
		out = append(out, *t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Deleted > out[j].Deleted })
	return out
}

// PurgeOlder empties trash entries older than days (方案 §5.6: retention is configurable).
func (m *Manager) PurgeOlder(days int) {
	cut := time.Now().AddDate(0, 0, -days).Unix()
	for _, t := range m.TrashList() {
		if t.Deleted < cut {
			_ = m.Purge(t.ID)
		}
	}
}

func dirSize(dir string) int64 {
	var n int64
	_ = filepath.Walk(dir, func(_ string, fi fs.FileInfo, err error) error {
		if err == nil && !fi.IsDir() {
			n += fi.Size()
		}
		return nil
	})
	return n
}

// ---- chunked upload ----

func (m *Manager) UploadBegin(root, dest string, size int64) (string, error) {
	dest, err := resolve(root, dest)
	if err != nil {
		return "", err
	}
	if _, err := os.Lstat(dest); err == nil {
		return "", errs.New(409, "files.exists", "path", dest)
	}
	if err := os.MkdirAll(m.uploads, 0o700); err != nil {
		return "", err
	}
	id := sign.RandomHex(12)
	part := filepath.Join(m.uploads, id+".part")
	if err := os.WriteFile(part, nil, 0o600); err != nil {
		return "", err
	}
	m.mu.Lock()
	m.ups[id] = &upload{root: root, dest: dest, part: part, size: size, at: time.Now()}
	m.mu.Unlock()
	return id, nil
}

func (m *Manager) up(id string) (*upload, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	u, ok := m.ups[id]
	if !ok {
		return nil, errs.New(404, "files.upload_not_found")
	}
	u.at = time.Now()
	return u, nil
}

func (m *Manager) UploadStatus(id string) (int64, error) {
	u, err := m.up(id)
	if err != nil {
		return 0, err
	}
	fi, err := os.Stat(u.part)
	if err != nil {
		return 0, err
	}
	return fi.Size(), nil
}

// UploadChunk appends data at offset; offset must equal the bytes received so far.
func (m *Manager) UploadChunk(id string, offset int64, data []byte) error {
	u, err := m.up(id)
	if err != nil {
		return err
	}
	got, err := m.UploadStatus(id)
	if err != nil {
		return err
	}
	if offset != got {
		return errs.New(409, "files.upload_offset", "expected", got)
	}
	if got+int64(len(data)) > u.size {
		return errs.New(400, "files.upload_too_big")
	}
	f, err := os.OpenFile(u.part, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

func (m *Manager) UploadFinish(id string) error {
	u, err := m.up(id)
	if err != nil {
		return err
	}
	got, _ := m.UploadStatus(id)
	if got != u.size {
		return errs.New(409, "files.upload_incomplete", "received", got, "size", u.size)
	}
	if _, err := resolve(u.root, u.dest); err != nil {
		return err
	}
	if err := moveAcross(u.part, u.dest); err != nil {
		return fsErr(err, u.dest)
	}
	_ = os.Chmod(u.dest, 0o644)
	if uid, gid := parentOwner(u.dest); uid >= 0 {
		_ = os.Lchown(u.dest, uid, gid)
	}
	m.mu.Lock()
	delete(m.ups, id)
	m.mu.Unlock()
	return nil
}

// ExpireUploads drops uploads idle for a day.
func (m *Manager) ExpireUploads() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, u := range m.ups {
		if time.Since(u.at) > 24*time.Hour {
			os.Remove(u.part)
			delete(m.ups, id)
		}
	}
}
