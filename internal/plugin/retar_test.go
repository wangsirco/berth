package plugin

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"io"
	"testing"
)

// retar rewrites one file inside a package, keeping its signature as it was.
func retar(t *testing.T, pkg []byte, name string, content []byte) []byte {
	gz, _ := gzip.NewReader(bytes.NewReader(pkg))
	tr := tar.NewReader(gz)
	var out bytes.Buffer
	zw := gzip.NewWriter(&out)
	tw := tar.NewWriter(zw)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(tr)
		if h.Name == name {
			b = content
			h.Size = int64(len(b))
		}
		tw.WriteHeader(h)
		tw.Write(b)
	}
	tw.Close()
	zw.Close()
	return out.Bytes()
}
