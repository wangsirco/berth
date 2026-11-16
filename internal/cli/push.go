package cli

// berth push: pack the working directory (respecting .gitignore and .berthignore, with
// uncommitted changes) and run its pipeline on the panel.

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
)

const maxPush = 60 << 20

// ignoreRule is one line of a .gitignore-style file.
type ignoreRule struct {
	re     *regexp.Regexp
	negate bool
	dir    bool // only matches directories
}

// globRe turns a gitignore pattern into a regexp over slash-separated paths.
func globRe(p string) *regexp.Regexp {
	anchored := strings.Contains(strings.TrimSuffix(p, "/"), "/")
	p = strings.TrimPrefix(p, "/")
	var b strings.Builder
	if !anchored {
		b.WriteString(`^(.*/)?`)
	} else {
		b.WriteString(`^`)
	}
	for i := 0; i < len(p); i++ {
		switch c := p[i]; {
		case c == '*' && i+1 < len(p) && p[i+1] == '*':
			if i+2 < len(p) && p[i+2] == '/' {
				b.WriteString(`(.*/)?`)
				i += 2
			} else {
				b.WriteString(`.*`)
				i++
			}
		case c == '*':
			b.WriteString(`[^/]*`)
		case c == '?':
			b.WriteString(`[^/]`)
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	b.WriteString(`(/.*)?$`) // a matched directory excludes everything in it
	return regexp.MustCompile(b.String())
}

func readIgnore(file string) []ignoreRule {
	f, err := os.Open(file)
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []ignoreRule
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		l := strings.TrimRight(sc.Text(), " \t\r")
		if l == "" || strings.HasPrefix(l, "#") {
			continue
		}
		r := ignoreRule{}
		if strings.HasPrefix(l, "!") {
			r.negate, l = true, l[1:]
		}
		if strings.HasSuffix(l, "/") {
			r.dir, l = true, strings.TrimSuffix(l, "/")
		}
		if l == "" {
			continue
		}
		r.re = globRe(l)
		out = append(out, r)
	}
	return out
}

// Ignored applies the rules in order; the last match wins.
func ignored(rules []ignoreRule, rel string, isDir bool) bool {
	out := false
	for _, r := range rules {
		if r.dir && !isDir && !strings.Contains(rel, "/") {
			continue
		}
		if r.re.MatchString(rel) {
			out = !r.negate
		}
	}
	return out
}

// Pack builds a tar.gz of dir.
func Pack(dir string) ([]byte, int, error) {
	rules := append(readIgnore(filepath.Join(dir, ".gitignore")), readIgnore(filepath.Join(dir, ".berthignore"))...)
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	files := 0
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		rel = filepath.ToSlash(rel)
		if rel == "." {
			return nil
		}
		base := path.Base(rel)
		if d.IsDir() && (base == ".git" || ignored(rules, rel, true)) {
			return filepath.SkipDir
		}
		if !d.IsDir() && ignored(rules, rel, false) {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		switch {
		case d.IsDir():
			return tw.WriteHeader(&tar.Header{Name: rel + "/", Mode: 0o755, Typeflag: tar.TypeDir, ModTime: info.ModTime()})
		case info.Mode()&fs.ModeSymlink != 0:
			t, _ := os.Readlink(p)
			return tw.WriteHeader(&tar.Header{Name: rel, Linkname: t, Typeflag: tar.TypeSymlink, ModTime: info.ModTime()})
		case info.Mode().IsRegular():
			f, err := os.Open(p)
			if err != nil {
				return err
			}
			defer f.Close()
			if err := tw.WriteHeader(&tar.Header{Name: rel, Mode: int64(info.Mode().Perm()), Size: info.Size(), Typeflag: tar.TypeReg, ModTime: info.ModTime()}); err != nil {
				return err
			}
			if _, err := io.Copy(tw, f); err != nil {
				return err
			}
			files++
			if buf.Len() > maxPush {
				return errors.New("the archive is over 60 MB; exclude build output and dependencies in .berthignore")
			}
		}
		return nil
	})
	if err != nil {
		return nil, 0, err
	}
	tw.Close()
	zw.Close()
	return buf.Bytes(), files, nil
}

var specNameRe = regexp.MustCompile(`(?m)^name:\s*["']?([a-z0-9][a-z0-9_-]*)`)

func cmdPush(args []string) error {
	fs := flag.NewFlagSet("push", flag.ContinueOnError)
	name := fs.String("pipeline", "", "pipeline name (default: name in berth.pipeline.yaml, else the directory name)")
	if err := fs.Parse(reorder(args)); err != nil {
		return err
	}
	dir := "."
	if fs.NArg() > 0 {
		dir = fs.Arg(0)
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	if *name == "" {
		if b, err := os.ReadFile(filepath.Join(abs, "berth.pipeline.yaml")); err == nil {
			if m := specNameRe.FindSubmatch(b); m != nil {
				*name = string(m[1])
			}
		}
	}
	if *name == "" {
		*name = strings.ToLower(filepath.Base(abs))
	}
	c, err := newClient()
	if err != nil {
		return err
	}
	data, n, err := Pack(abs)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "Uploading %d files (%s) to pipeline %s…\n", n, bytesH(int64(len(data))), *name)
	var ch changeOut
	if err := c.do("POST", "/api/v1/pipelines/"+*name+"/push", data, &ch); err != nil {
		return err
	}
	if ch.Status == "running" {
		if ch, err = c.follow(ch.ID); err != nil {
			return err
		}
	}
	if ch.Status != "done" {
		return fmt.Errorf("pipeline %s: %s", *name, ch.Status)
	}
	fmt.Fprintln(os.Stderr, "Done.")
	return nil
}
