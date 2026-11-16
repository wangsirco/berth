package files

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"compress/gzip"
	"context"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/klauspost/compress/zstd"

	"github.com/wangsirco/berth/internal/errs"
)

// formatOf picks the archive format from a file name.
func formatOf(name string) string {
	n := strings.ToLower(name)
	switch {
	case strings.HasSuffix(n, ".zip"):
		return "zip"
	case strings.HasSuffix(n, ".tar.gz"), strings.HasSuffix(n, ".tgz"):
		return "tar.gz"
	case strings.HasSuffix(n, ".tar.zst"), strings.HasSuffix(n, ".tzst"):
		return "tar.zst"
	case strings.HasSuffix(n, ".tar"):
		return "tar"
	case strings.HasSuffix(n, ".7z"):
		return "7z"
	}
	return ""
}

// walkSources calls fn for every file under paths, with names relative to each path's parent.
func walkSources(paths []string, fn func(path, name string, fi fs.FileInfo) error) error {
	for _, src := range paths {
		base := filepath.Dir(src)
		err := filepath.Walk(src, func(p string, fi fs.FileInfo, err error) error {
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(base, p)
			return fn(p, filepath.ToSlash(rel), fi)
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// WriteZip streams a zip of paths to w (multi-select download).
func WriteZip(root string, paths []string, w io.Writer) error {
	for i, p := range paths {
		rp, err := resolve(root, p)
		if err != nil {
			return err
		}
		paths[i] = rp
	}
	zw := zip.NewWriter(w)
	err := walkSources(paths, func(p, name string, fi fs.FileInfo) error {
		if fi.Mode()&fs.ModeSymlink != 0 {
			return nil
		}
		h, err := zip.FileInfoHeader(fi)
		if err != nil {
			return err
		}
		h.Name = name
		if fi.IsDir() {
			h.Name += "/"
			_, err := zw.CreateHeader(h)
			return err
		}
		h.Method = zip.Deflate
		out, err := zw.CreateHeader(h)
		if err != nil {
			return err
		}
		f, err := os.Open(p)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = io.Copy(out, f)
		return err
	})
	if err != nil {
		return err
	}
	return zw.Close()
}

// Archive packs paths into dest (format from its extension).
func Archive(ctx context.Context, root string, paths []string, dest string, progress func(string)) error {
	for i, p := range paths {
		rp, err := resolve(root, p)
		if err != nil {
			return err
		}
		paths[i] = rp
	}
	dest, err := resolve(root, dest)
	if err != nil {
		return err
	}
	if _, err := os.Lstat(dest); err == nil {
		return errs.New(409, "files.exists", "path", dest)
	}
	format := formatOf(dest)
	if format == "7z" {
		return run7z(ctx, progress, append([]string{"a", dest}, paths...)...)
	}
	if format == "" {
		return errs.New(400, "files.bad_archive_format", "name", filepath.Base(dest))
	}
	f, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o644)
	if err != nil {
		return fsErr(err, dest)
	}
	ok := false
	defer func() {
		f.Close()
		if !ok {
			os.Remove(dest)
		}
	}()
	bw := bufio.NewWriterSize(f, 256<<10)
	if format == "zip" {
		if err := WriteZip("/", paths, bw); err != nil {
			return err
		}
	} else {
		var w io.WriteCloser = nopCloser{bw}
		switch format {
		case "tar.gz":
			w = gzip.NewWriter(bw)
		case "tar.zst":
			zw, err := zstd.NewWriter(bw)
			if err != nil {
				return err
			}
			w = zw
		}
		tw := tar.NewWriter(w)
		err := walkSources(paths, func(p, name string, fi fs.FileInfo) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			link := ""
			if fi.Mode()&fs.ModeSymlink != 0 {
				link, _ = os.Readlink(p)
			}
			h, err := tar.FileInfoHeader(fi, link)
			if err != nil {
				return err
			}
			h.Name = name
			if fi.IsDir() {
				h.Name += "/"
			}
			if err := tw.WriteHeader(h); err != nil {
				return err
			}
			if !fi.Mode().IsRegular() {
				return nil
			}
			progress(name)
			src, err := os.Open(p)
			if err != nil {
				return err
			}
			defer src.Close()
			_, err = io.Copy(tw, src)
			return err
		})
		if err != nil {
			return err
		}
		if err := tw.Close(); err != nil {
			return err
		}
		if err := w.Close(); err != nil {
			return err
		}
	}
	if err := bw.Flush(); err != nil {
		return err
	}
	ok = true
	return nil
}

type nopCloser struct{ io.Writer }

func (nopCloser) Close() error { return nil }

// safeJoin refuses entries that would land outside dest ("../x", absolute names).
func safeJoin(dest, name string) (string, error) {
	p := filepath.Join(dest, filepath.FromSlash(name)) // Join cleans "..", so p escapes only if within fails
	if !within(dest, p) {
		return "", errs.New(400, "files.unsafe_archive", "entry", name)
	}
	return p, nil
}

// Extract unpacks archive p into dest (created if missing). Symlinks are only kept when
// they point inside dest; device files are skipped.
func Extract(ctx context.Context, root, p, dest string, progress func(string)) error {
	p, err := resolve(root, p)
	if err != nil {
		return err
	}
	if dest, err = resolve(root, dest); err != nil {
		return err
	}
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return fsErr(err, dest)
	}
	switch formatOf(p) {
	case "zip":
		return extractZip(ctx, p, dest, progress)
	case "7z":
		return run7z(ctx, progress, "x", "-y", "-o"+dest, p)
	case "":
		return errs.New(400, "files.bad_archive_format", "name", filepath.Base(p))
	}
	f, err := os.Open(p)
	if err != nil {
		return fsErr(err, p)
	}
	defer f.Close()
	var r io.Reader = bufio.NewReaderSize(f, 256<<10)
	switch formatOf(p) {
	case "tar.gz":
		gz, err := gzip.NewReader(r)
		if err != nil {
			return errs.New(400, "files.bad_archive", "detail", err.Error())
		}
		r = gz
	case "tar.zst":
		zr, err := zstd.NewReader(r)
		if err != nil {
			return errs.New(400, "files.bad_archive", "detail", err.Error())
		}
		defer zr.Close()
		r = zr
	}
	tr := tar.NewReader(r)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		h, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return errs.New(400, "files.bad_archive", "detail", err.Error())
		}
		target, err := safeJoin(dest, h.Name)
		if err != nil {
			return err
		}
		mode := os.FileMode(h.Mode).Perm()
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, mode|0o700); err != nil {
				return err
			}
		case tar.TypeReg:
			progress(h.Name)
			if err := writeEntry(target, mode, tr); err != nil {
				return err
			}
		case tar.TypeSymlink:
			linkDest := h.Linkname
			if !filepath.IsAbs(linkDest) {
				linkDest = filepath.Join(filepath.Dir(target), linkDest)
			}
			if !within(dest, filepath.Clean(linkDest)) {
				return errs.New(400, "files.unsafe_archive", "entry", h.Name)
			}
			_ = os.MkdirAll(filepath.Dir(target), 0o755)
			if err := os.Symlink(h.Linkname, target); err != nil {
				return err
			}
		}
	}
}

func extractZip(ctx context.Context, p, dest string, progress func(string)) error {
	zr, err := zip.OpenReader(p)
	if err != nil {
		return errs.New(400, "files.bad_archive", "detail", err.Error())
	}
	defer zr.Close()
	for _, f := range zr.File {
		if err := ctx.Err(); err != nil {
			return err
		}
		target, err := safeJoin(dest, f.Name)
		if err != nil {
			return err
		}
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			continue
		}
		if f.Mode()&fs.ModeSymlink != 0 {
			continue
		}
		progress(f.Name)
		rc, err := f.Open()
		if err != nil {
			return err
		}
		err = writeEntry(target, f.Mode().Perm()|0o600, rc)
		rc.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

func writeEntry(target string, mode os.FileMode, r io.Reader) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC|syscall.O_NOFOLLOW, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, r); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

func run7z(ctx context.Context, progress func(string), args ...string) error {
	bin, err := exec.LookPath("7z")
	if err != nil {
		if bin, err = exec.LookPath("7za"); err != nil {
			return errs.New(409, "files.need_7z")
		}
	}
	c := exec.CommandContext(ctx, bin, args...)
	out, err := c.CombinedOutput()
	for _, l := range strings.Split(string(out), "\n") {
		if strings.TrimSpace(l) != "" {
			progress(l)
		}
	}
	if err != nil {
		return errs.New(500, "files.archive_failed", "detail", err.Error())
	}
	return nil
}

// Search finds files by name (substring, case-insensitive) or content under dir.
// It uses ripgrep for content when available. At most limit results.
func Search(ctx context.Context, root, dir, name, content string, limit int) ([]string, error) {
	dir, err := resolve(root, dir)
	if err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 500 {
		limit = 500
	}
	var out []string
	if content != "" {
		if rg, err := exec.LookPath("rg"); err == nil {
			args := []string{"-l", "--max-count", "1", "-F", "-i", content}
			if name != "" {
				args = append(args, "--iglob", "*"+name+"*")
			}
			res, _ := exec.CommandContext(ctx, rg, append(args, dir)...).Output()
			for _, l := range strings.Split(strings.TrimSpace(string(res)), "\n") {
				if l != "" && len(out) < limit {
					out = append(out, l)
				}
			}
			return out, nil
		}
	}
	lname, lcontent := strings.ToLower(name), []byte(strings.ToLower(content))
	errStop := io.EOF
	err = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // unreadable dirs are skipped
		}
		if ctx.Err() != nil || len(out) >= limit {
			return errStop
		}
		if lname != "" && !strings.Contains(strings.ToLower(d.Name()), lname) {
			return nil
		}
		if content != "" {
			if d.IsDir() || !d.Type().IsRegular() {
				return nil
			}
			b, err := os.ReadFile(p)
			if err != nil || len(b) > 16<<20 || !strings.Contains(strings.ToLower(string(b)), string(lcontent)) {
				return nil
			}
		}
		if p != dir {
			out = append(out, p)
		}
		return nil
	})
	if err != nil && err != errStop {
		return nil, err
	}
	return out, nil
}
