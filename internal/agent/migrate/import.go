package migrate

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/wangsirco/berth/internal/agent/incus"
	"github.com/wangsirco/berth/internal/agent/system"
	"github.com/wangsirco/berth/internal/errs"
	"github.com/wangsirco/berth/internal/sign"
)

// ImportReq opens a session that writes one item's tar stream into place.
type ImportReq struct {
	Allow   []string                     `json:"allow"`   // folders files may be written under
	Users   []string                     `json:"users"`   // native projects whose system users must exist first
	Volumes map[string]map[string]string `json:"volumes"` // volume → labels it is created with
	Streams map[string]Stream            `json:"streams"`
	Reserve int64                        `json:"reserve"` // stop before free space goes below this
	// Map moves files to another place: source folder → target folder (a 宝塔 site's
	// /www/wwwroot/x lands in Berth's site folder). Checked against Allow after mapping.
	Map map[string]string `json:"map,omitempty"`
}

type ImportResult struct {
	Files    int64    `json:"files"`
	Bytes    int64    `json:"bytes"`
	Warnings []string `json:"warnings"`
}

// never written into, whatever a pack says
var denied = []string{"/etc", "/usr", "/bin", "/sbin", "/lib", "/lib64", "/boot", "/proc", "/sys", "/dev", "/run", "/var/lib/berth", "/var/lib/docker", "/var/lib/incus", "/var/lib/containerd", "/root/.ssh", "/var/lib/mysql", "/var/lib/pgsql", "/var/lib/postgresql"}

func under(p, dir string) bool {
	return p == dir || strings.HasPrefix(p, strings.TrimSuffix(dir, "/")+"/")
}

func allowedPath(p string, allow []string) bool {
	for _, d := range denied {
		if under(p, d) {
			return false
		}
	}
	for _, a := range allow {
		if under(p, a) {
			return true
		}
	}
	return false
}

// safeParent makes the parent folders of p below root, refusing to pass through a link (a pack
// could otherwise plant a link and then write through it).
func safeParent(root, p string) error {
	rel, err := filepath.Rel(root, filepath.Dir(p))
	if err != nil || strings.HasPrefix(rel, "..") {
		return errs.New(400, "migrate.bad_path", "path", p)
	}
	cur := root
	os.MkdirAll(root, 0o755) // the allowed folder's parent: trusted, from the panel
	if fi, err := os.Lstat(cur); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		return errs.New(400, "migrate.bad_path", "path", p)
	}
	if rel == "." {
		return os.MkdirAll(root, 0o755)
	}
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		cur = filepath.Join(cur, part)
		fi, err := os.Lstat(cur)
		if errors.Is(err, os.ErrNotExist) {
			if err := os.Mkdir(cur, 0o755); err != nil {
				return err
			}
			continue
		}
		if err != nil {
			return err
		}
		if fi.Mode()&os.ModeSymlink != 0 || !fi.IsDir() {
			return errs.New(400, "migrate.bad_path", "path", p)
		}
	}
	return nil
}

func ids(h *tar.Header) (int, int) {
	uid, gid := h.Uid, h.Gid
	if h.Uname != "" {
		if u, err := user.Lookup(h.Uname); err == nil {
			uid, _ = strconv.Atoi(u.Uid)
		}
	}
	if h.Gname != "" {
		if g, err := user.LookupGroup(h.Gname); err == nil {
			gid, _ = strconv.Atoi(g.Gid)
		}
	}
	return uid, gid
}

type dirFix struct {
	path string
	mode os.FileMode
	mt   time.Time
}

type writer struct {
	ctx     context.Context
	req     ImportReq
	res     ImportResult
	dirs    []dirFix
	vols    map[string]string // volume → mountpoint
	sinceCk int64
}

var errFull = errs.New(507, "migrate.disk_full")

// room stops the import before the disk goes below the reserve.
func (w *writer) room(at string, need int64) error {
	w.sinceCk += need
	if w.sinceCk < 32<<20 && need < 32<<20 {
		return nil
	}
	w.sinceCk = 0
	if free := freeAt(at); free >= 0 && free-need < w.req.Reserve {
		return errs.New(507, "migrate.disk_full", "path", at, "free", free, "need", need)
	}
	return nil
}

func (w *writer) put(root, p string, h *tar.Header, r io.Reader) error {
	if err := safeParent(root, p); err != nil {
		return err
	}
	uid, gid := ids(h)
	mode := os.FileMode(h.Mode).Perm() | os.FileMode(h.Mode)&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky)
	switch h.Typeflag {
	case tar.TypeDir:
		if fi, err := os.Lstat(p); err == nil && !fi.IsDir() {
			os.Remove(p)
		}
		if err := os.Mkdir(p, 0o755); err != nil && !errors.Is(err, os.ErrExist) {
			return err
		}
		os.Lchown(p, uid, gid)
		w.dirs = append(w.dirs, dirFix{p, os.FileMode(h.Mode), h.ModTime})
	case tar.TypeReg:
		if err := w.room(filepath.Dir(p), h.Size); err != nil {
			return err
		}
		if fi, err := os.Lstat(p); err == nil && (fi.Mode()&os.ModeSymlink != 0 || fi.IsDir()) {
			os.RemoveAll(p)
		}
		f, err := os.OpenFile(p, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		n, err := sparseCopy(f, r)
		if err == nil {
			err = f.Truncate(n) // a file ending in a hole needs its length set
		}
		f.Close()
		if err != nil {
			return err
		}
		os.Lchown(p, uid, gid)
		os.Chmod(p, mode)
		os.Chtimes(p, h.ModTime, h.ModTime)
		w.res.Files++
		w.res.Bytes += n
	case tar.TypeSymlink:
		os.RemoveAll(p)
		if err := os.Symlink(h.Linkname, p); err != nil {
			return err
		}
		os.Lchown(p, uid, gid)
	case tar.TypeLink:
		src := filepath.Clean(strings.TrimPrefix(h.Linkname, "f"))
		if !allowedPath(src, w.req.Allow) {
			return errs.New(403, "migrate.path_not_allowed", "path", src)
		}
		os.RemoveAll(p)
		if err := os.Link(src, p); err != nil {
			w.res.Warnings = append(w.res.Warnings, "link "+p+": "+err.Error())
		}
	}
	return nil
}

func (w *writer) volume(name string) (string, error) {
	if d, ok := w.vols[name]; ok {
		return d, nil
	}
	if !nameRe(name) {
		return "", errs.New(400, "migrate.bad_name", "name", name)
	}
	if _, err := volumeDir(w.ctx, name); err != nil {
		args := []string{"volume", "create"}
		for k, v := range w.req.Volumes[name] {
			args = append(args, "--label", k+"="+v)
		}
		if out, err := exec.CommandContext(w.ctx, "docker", append(args, name)...).CombinedOutput(); err != nil {
			return "", fmt.Errorf("docker volume create %s: %s", name, strings.TrimSpace(string(out)))
		}
	}
	d, err := volumeDir(w.ctx, name)
	if err != nil {
		return "", err
	}
	w.vols[name] = d
	return d, nil
}

// guarded stops a stream when its disk runs low.
type guarded struct {
	w    io.Writer
	at   string
	res  int64
	seen int64
}

func (g *guarded) Write(b []byte) (int, error) {
	g.seen += int64(len(b))
	if g.seen >= 64<<20 {
		g.seen = 0
		if free := freeAt(g.at); free >= 0 && free < g.res {
			return 0, errs.New(507, "migrate.disk_full", "path", g.at, "free", free)
		}
	}
	return g.w.Write(b)
}

func restoreStream(ctx context.Context, s Stream, r io.Reader) error {
	switch s.Kind {
	case "incus":
		return incus.ImportBackup(ctx, s.Arg, r)
	case "image":
		cmd := exec.CommandContext(ctx, "docker", "load")
		cmd.Stdin = r
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("docker load: %s", strings.TrimSpace(string(out)))
		}
		return nil
	case "db":
		eng, _, _ := strings.Cut(s.Arg, ":")
		return system.RestoreSQL(ctx, eng, r, true, false)
	case "dbusers", "sql":
		return system.RestoreSQL(ctx, s.Arg, r, false, true)
	case "redis":
		return system.RedisRestore(ctx, r)
	}
	return errs.New(400, "migrate.bad_stream", "kind", s.Kind)
}

func streamAt(s Stream) string {
	switch s.Kind {
	case "incus":
		return "/var/lib/incus"
	case "image":
		return "/var/lib/docker"
	case "redis":
		return "/var/lib"
	}
	return "/var/lib"
}

type runningStream struct {
	name string
	pw   *io.PipeWriter
	done chan error
	g    *guarded
}

func (w *writer) run(r io.Reader) error {
	tr := tar.NewReader(r)
	var cur *runningStream
	endStream := func() error {
		if cur == nil {
			return nil
		}
		cur.pw.Close()
		err := <-cur.done
		cur = nil
		return err
	}
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			endStream()
			return err
		}
		name := h.Name
		switch {
		case strings.HasPrefix(name, "f/"):
			if err := endStream(); err != nil {
				return err
			}
			p := mapPath(filepath.Clean("/"+strings.TrimPrefix(name, "f/")), w.req.Map)
			if h.Typeflag == tar.TypeLink {
				h.Linkname = "f" + mapPath(filepath.Clean("/"+strings.TrimPrefix(h.Linkname, "f/")), w.req.Map)
			}
			if !allowedPath(p, w.req.Allow) {
				inside := false
				for _, a := range w.req.Allow {
					inside = inside || under(p, a)
				}
				if !inside { // outside what this item may write: the pack is not what it claims
					return errs.New(403, "migrate.path_not_allowed", "path", p)
				}
				// inside a chosen folder but protected (/root/.ssh): left out, said so
				if len(w.res.Warnings) < 50 {
					w.res.Warnings = append(w.res.Warnings, "migrate.warn.protected:"+p)
				}
				continue
			}
			root := "/"
			for _, a := range w.req.Allow {
				if under(p, a) {
					root = filepath.Dir(a)
					break
				}
			}
			if err := w.put(root, p, h, tr); err != nil {
				return err
			}
		case strings.HasPrefix(name, "v/"):
			if err := endStream(); err != nil {
				return err
			}
			vol, rest, _ := strings.Cut(strings.TrimPrefix(name, "v/"), "/")
			dir, err := w.volume(vol)
			if err != nil {
				return err
			}
			p := filepath.Clean(filepath.Join(dir, rest))
			if !under(p, dir) {
				return errs.New(400, "migrate.bad_path", "path", name)
			}
			if p == dir { // the volume root: its owner and mode only
				uid, gid := ids(h)
				os.Lchown(dir, uid, gid)
				os.Chmod(dir, os.FileMode(h.Mode).Perm())
				continue
			}
			if err := w.put(dir, p, h, tr); err != nil {
				return err
			}
		case strings.HasPrefix(name, "s/"):
			sname, _, _ := strings.Cut(strings.TrimPrefix(name, "s/"), "/")
			if cur != nil && cur.name != sname {
				if err := endStream(); err != nil {
					return err
				}
			}
			if cur == nil {
				spec, ok := w.req.Streams[sname]
				if !ok {
					return errs.New(400, "migrate.bad_stream", "kind", sname)
				}
				pr, pw := io.Pipe()
				cur = &runningStream{name: sname, pw: pw, done: make(chan error, 1)}
				cur.g = &guarded{w: pw, at: streamAt(spec), res: w.req.Reserve}
				go func(c *runningStream, s Stream) {
					err := restoreStream(w.ctx, s, pr)
					if err != nil {
						pr.CloseWithError(err) // the writer side sees why
					} else {
						io.Copy(io.Discard, pr)
					}
					c.done <- err
				}(cur, spec)
			}
			n, err := io.Copy(cur.g, tr)
			w.res.Bytes += n
			if err != nil {
				cur.pw.CloseWithError(err)
				<-cur.done
				return err
			}
		case name == "x/status":
			// the source's own end marker: an error there means the item is incomplete
			b, _ := io.ReadAll(io.LimitReader(tr, 64<<10))
			if s := strings.TrimSpace(string(b)); s != "" && s != "ok" {
				endStream()
				return errs.New(502, "migrate.source_failed", "detail", s)
			}
		}
	}
	if err := endStream(); err != nil {
		return err
	}
	for i := len(w.dirs) - 1; i >= 0; i-- { // after their content, or writing it moves mtime
		d := w.dirs[i]
		os.Chmod(d.path, d.mode.Perm()|d.mode&(os.ModeSetgid|os.ModeSticky))
		os.Chtimes(d.path, d.mt, d.mt)
	}
	if _, err := exec.LookPath("restorecon"); err == nil {
		for _, a := range w.req.Allow {
			if _, err := os.Stat(a); err == nil {
				exec.CommandContext(w.ctx, "restorecon", "-R", a).Run()
			}
		}
	}
	return nil
}

// ---- sessions ----

type session struct {
	mu   sync.Mutex
	pw   *io.PipeWriter
	recv int64
	done chan struct{}
	err  error
	res  ImportResult
	at   time.Time
	stop context.CancelFunc
}

var (
	smu      sync.Mutex
	sessions = map[string]*session{}
)

func Begin(req ImportReq) (string, error) {
	for _, a := range req.Allow {
		if !filepath.IsAbs(a) || filepath.Clean(a) != a || a == "/" {
			return "", errs.New(400, "migrate.bad_path", "path", a)
		}
		for _, d := range denied {
			if under(a, d) || under(d, a) {
				return "", errs.New(403, "migrate.path_not_allowed", "path", a)
			}
		}
	}
	if req.Reserve <= 0 {
		req.Reserve = 1 << 30
	}
	ctx, cancel := context.WithCancel(context.Background())
	for _, u := range req.Users {
		if err := system.EnsureNativeUser(ctx, u); err != nil {
			cancel()
			return "", err
		}
	}
	pr, pw := io.Pipe()
	s := &session{pw: pw, done: make(chan struct{}), at: time.Now(), stop: cancel}
	w := &writer{ctx: ctx, req: req, vols: map[string]string{}, res: ImportResult{Warnings: []string{}}}
	go func() {
		err := w.run(pr)
		if err == nil {
			io.Copy(io.Discard, pr) // anything after the tar end
		}
		// read only after done is closed, so no lock (Chunk holds s.mu while it waits)
		s.err, s.res = err, w.res
		pr.CloseWithError(errOr(err, io.ErrClosedPipe))
		close(s.done)
	}()
	id := sign.RandomHex(12)
	smu.Lock()
	sessions[id] = s
	smu.Unlock()
	go reap()
	return id, nil
}

func errOr(err, def error) error {
	if err != nil {
		return err
	}
	return def
}

func get(id string) (*session, error) {
	smu.Lock()
	defer smu.Unlock()
	s, ok := sessions[id]
	if !ok {
		return nil, errs.New(404, "migrate.no_session")
	}
	return s, nil
}

// Chunk feeds the next bytes; off must be the count received so far (a retried chunk that
// already arrived is acknowledged without writing it again).
func Chunk(id string, off int64, b []byte) (int64, error) {
	s, err := get(id)
	if err != nil {
		return 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.at = time.Now()
	if off+int64(len(b)) == s.recv && len(b) > 0 {
		return s.recv, nil
	}
	if off != s.recv {
		return s.recv, errs.New(409, "migrate.offset", "expected", s.recv)
	}
	if _, err := s.pw.Write(b); err != nil {
		<-s.done
		return s.recv, errOr(s.err, err)
	}
	s.recv += int64(len(b))
	return s.recv, nil
}

func Finish(id string) (ImportResult, error) {
	s, err := get(id)
	if err != nil {
		return ImportResult{}, err
	}
	s.pw.Close()
	<-s.done
	smu.Lock()
	delete(sessions, id)
	smu.Unlock()
	s.stop()
	return s.res, s.err
}

func Abort(id string) {
	if s, err := get(id); err == nil {
		s.pw.CloseWithError(errors.New("aborted"))
		s.stop()
		<-s.done
		smu.Lock()
		delete(sessions, id)
		smu.Unlock()
	}
}

// reap drops sessions nobody has fed for an hour.
func reap() {
	smu.Lock()
	defer smu.Unlock()
	for id, s := range sessions {
		s.mu.Lock()
		old := time.Since(s.at) > time.Hour
		s.mu.Unlock()
		if old {
			s.pw.CloseWithError(errors.New("abandoned"))
			s.stop()
			delete(sessions, id)
		}
	}
}

// sparseCopy writes r to f, leaving all-zero blocks as holes: a sparse file (a preallocated
// image, a database's empty extents) takes on the target what it took on the source, which is
// what the size check counted.
func sparseCopy(f *os.File, r io.Reader) (int64, error) {
	buf := make([]byte, 64<<10)
	var n int64
	for {
		k, err := io.ReadFull(r, buf)
		if k > 0 {
			if allZero(buf[:k]) {
				if _, serr := f.Seek(int64(k), io.SeekCurrent); serr != nil {
					return n, serr
				}
			} else if _, werr := f.Write(buf[:k]); werr != nil {
				return n, werr
			}
			n += int64(k)
		}
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			return n, nil
		}
		if err != nil {
			return n, err
		}
	}
}

func allZero(b []byte) bool {
	for _, c := range b {
		if c != 0 {
			return false
		}
	}
	return true
}

// mapPath applies the longest matching folder mapping.
func mapPath(p string, m map[string]string) string {
	best := ""
	for from := range m {
		if under(p, from) && len(from) > len(best) {
			best = from
		}
	}
	if best == "" {
		return p
	}
	return filepath.Join(m[best], strings.TrimPrefix(p, best))
}
