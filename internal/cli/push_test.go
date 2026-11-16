package cli

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func TestPackIgnoreRules(t *testing.T) {
	d := t.TempDir()
	write := func(p, s string) {
		os.MkdirAll(filepath.Dir(filepath.Join(d, p)), 0o755)
		os.WriteFile(filepath.Join(d, p), []byte(s), 0o644)
	}
	write(".gitignore", "node_modules/\n*.log\n/dist\n!keep.log\n")
	write(".berthignore", "web/.next/\nsecrets/**\n")
	for _, p := range []string{"main.go", "debug.log", "keep.log", "node_modules/x/index.js", "web/node_modules/y.js", "dist/app.js",
		"web/dist/ok.js", "web/.next/cache/a", "web/page.tsx", "secrets/a/b.key", ".git/HEAD", "uncommitted.txt"} {
		write(p, "x")
	}
	data, n, err := Pack(d)
	if err != nil {
		t.Fatal(err)
	}
	zr, _ := gzip.NewReader(bytes.NewReader(data))
	tr := tar.NewReader(zr)
	var got []string
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if h.Typeflag == tar.TypeReg {
			got = append(got, h.Name)
		}
	}
	sort.Strings(got)
	want := ".berthignore .gitignore keep.log main.go uncommitted.txt web/dist/ok.js web/page.tsx"
	if strings.Join(got, " ") != want || n != 7 {
		t.Fatalf("got %v", got)
	}
}
