package migrate

import (
	"archive/tar"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/wangsirco/berth/internal/agent/system"
	"github.com/wangsirco/berth/internal/errs"
)

// Entry names inside one item:
//
//	f/<absolute path>       a file, folder or link
//	v/<volume>/<path>       inside a docker volume
//	s/<stream>/<000001>     the next piece of a stream whose length is not known in advance
const pieceSize = 8 << 20

// Stream is data produced by a command rather than read from files.
type Stream struct {
	Name string `json:"name"`
	Kind string `json:"kind"` // incus | image | db | dbusers | redis | btdb (export) | sql (import)
	Arg  string `json:"arg"`  // instance, image ref, "engine:database", engine
	// a password the source needs (宝塔's MySQL root); never logged
	Secret string `json:"secret,omitempty"`
}

type ExportReq struct {
	Files   []string `json:"files"`
	Exclude []string `json:"exclude"` // absolute paths left out (old releases, caches)
	Volumes []string `json:"volumes"`
	Stop    []string `json:"stop"` // containers stopped while volumes are read
	Streams []Stream `json:"streams"`
}

var nameRe = func(s string) bool {
	if s == "" || len(s) > 128 || strings.Contains(s, "..") || strings.ContainsAny(s, "/\\ \n") {
		return false
	}
	return true
}

func owner(h *tar.Header, fi os.FileInfo) {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		h.Uid, h.Gid = int(st.Uid), int(st.Gid)
		if u, err := user.LookupId(strconv.Itoa(int(st.Uid))); err == nil {
			h.Uname = u.Username
		}
		if g, err := user.LookupGroupId(strconv.Itoa(int(st.Gid))); err == nil {
			h.Gname = g.Name
		}
	}
}

func excluded(p string, ex []string) bool {
	for _, e := range ex {
		if p == e || strings.HasPrefix(p, e+"/") {
			return true
		}
	}
	return false
}

// addTree writes dir (or one file) as entries named prefix + path relative to base.
func addTree(tw *tar.Writer, root, base, prefix string, ex []string) error {
	return filepath.Walk(root, func(p string, fi os.FileInfo, err error) error {
		if err != nil {
			return nil // vanished or unreadable: skip, keep going
		}
		if excluded(p, ex) {
			if fi.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		mode := fi.Mode()
		if !mode.IsRegular() && !mode.IsDir() && mode&os.ModeSymlink == 0 {
			return nil // sockets, devices, pipes
		}
		link := ""
		if mode&os.ModeSymlink != 0 {
			link, _ = os.Readlink(p)
		}
		h, err := tar.FileInfoHeader(fi, link)
		if err != nil {
			return nil
		}
		owner(h, fi)
		rel, _ := filepath.Rel(base, p)
		h.Name = prefix + filepath.ToSlash(rel)
		if rel == "." {
			h.Name = prefix
		}
		if mode.IsDir() && !strings.HasSuffix(h.Name, "/") {
			h.Name += "/"
		}
		h.Format = tar.FormatPAX
		if mode.IsRegular() {
			f, err := os.Open(p)
			if err != nil {
				return nil
			}
			defer f.Close()
			if err := tw.WriteHeader(h); err != nil {
				return err
			}
			// a file that grows while read is cut at its size; one that shrinks is padded
			n, err := io.Copy(tw, io.LimitReader(f, h.Size))
			if err != nil {
				return err
			}
			if n < h.Size {
				_, err = io.CopyN(tw, zeroReader{}, h.Size-n)
				return err
			}
			return nil
		}
		return tw.WriteHeader(h)
	})
}

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 0
	}
	return len(p), nil
}

// pieces turns a stream into numbered tar entries of up to pieceSize.
type pieces struct {
	tw   *tar.Writer
	name string
	seq  int
	buf  []byte
}

func (p *pieces) Write(b []byte) (int, error) {
	n := len(b)
	for len(b) > 0 {
		room := pieceSize - len(p.buf)
		k := min(room, len(b))
		p.buf = append(p.buf, b[:k]...)
		b = b[k:]
		if len(p.buf) == pieceSize {
			if err := p.flush(); err != nil {
				return 0, err
			}
		}
	}
	return n, nil
}

func (p *pieces) flush() error {
	if len(p.buf) == 0 && p.seq > 0 {
		return nil
	}
	p.seq++
	h := &tar.Header{Name: fmt.Sprintf("s/%s/%06d", p.name, p.seq), Mode: 0o600, Size: int64(len(p.buf)), Typeflag: tar.TypeReg}
	if err := p.tw.WriteHeader(h); err != nil {
		return err
	}
	_, err := p.tw.Write(p.buf)
	p.buf = p.buf[:0]
	return err
}

func volumeDir(ctx context.Context, name string) (string, error) {
	out, err := exec.CommandContext(ctx, "docker", "volume", "inspect", "--format", "{{.Mountpoint}}", name).Output()
	if err != nil {
		return "", errs.New(404, "docker.volume_not_found", "name", name)
	}
	return strings.TrimSpace(string(out)), nil
}

func cmdTo(ctx context.Context, w io.Writer, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout = w
	var e strings.Builder
	cmd.Stderr = &e
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s: %v %s", name, err, strings.TrimSpace(e.String()))
	}
	return nil
}

func writeStream(ctx context.Context, w io.Writer, s Stream) error {
	switch s.Kind {
	case "incus":
		// without snapshots: they would multiply the size; the target starts from now
		return cmdTo(ctx, w, "incus", "export", s.Arg, "-", "--instance-only", "--compression", "zstd", "-q")
	case "image":
		return cmdTo(ctx, w, "docker", "save", s.Arg)
	case "db":
		eng, db, _ := strings.Cut(s.Arg, ":")
		return system.DumpDB(ctx, eng, db, w)
	case "dbusers":
		return system.DumpUsers(ctx, s.Arg, w)
	case "btdb":
		return system.DumpDBAuth(ctx, s.Arg, s.Secret, w)
	case "redis":
		return system.RedisDump(ctx, w)
	}
	return errs.New(400, "migrate.bad_stream", "kind", s.Kind)
}

// Export writes one item's data into tw (the caller ends the archive).
func Export(ctx context.Context, req ExportReq, tw *tar.Writer, log func(string)) error {
	for _, f := range req.Files {
		f = filepath.Clean(f)
		if !filepath.IsAbs(f) || f == "/" {
			return errs.New(400, "migrate.bad_path", "path", f)
		}
		if _, err := os.Lstat(f); err != nil {
			continue
		}
		log("files " + f)
		// an exclusion that holds this root itself does not apply to it: {base, base/releases/x}
		// with base/releases excluded sends base and that one release
		ex := []string{}
		for _, e := range req.Exclude {
			if !(f == e || strings.HasPrefix(f, e+"/")) {
				ex = append(ex, e)
			}
		}
		if err := addTree(tw, f, "/", "f/", ex); err != nil {
			return err
		}
	}
	if len(req.Volumes) > 0 {
		if len(req.Stop) > 0 {
			log("pausing " + strings.Join(req.Stop, " ") + " while their data is read")
			exec.CommandContext(ctx, "docker", append([]string{"stop", "-t", "30"}, req.Stop...)...).Run()
			defer exec.Command("docker", append([]string{"start"}, req.Stop...)...).Run()
		}
		for _, v := range req.Volumes {
			if !nameRe(v) {
				return errs.New(400, "migrate.bad_name", "name", v)
			}
			dir, err := volumeDir(ctx, v)
			if err != nil {
				return err
			}
			log("volume " + v)
			// the folder entry itself carries the volume root's owner and mode
			if err := addTree(tw, dir, dir, "v/"+v+"/", nil); err != nil {
				return err
			}
		}
		if len(req.Stop) > 0 {
			exec.CommandContext(ctx, "docker", append([]string{"start"}, req.Stop...)...).Run()
			req.Stop = nil
		}
	}
	for _, s := range req.Streams {
		if !nameRe(s.Name) {
			return errs.New(400, "migrate.bad_name", "name", s.Name)
		}
		log(s.Kind + " " + s.Arg)
		p := &pieces{tw: tw, name: s.Name}
		if err := writeStream(ctx, p, s); err != nil {
			return err
		}
		if err := p.flush(); err != nil {
			return err
		}
	}
	return tw.Flush()
}
