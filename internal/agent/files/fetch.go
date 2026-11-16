package files

// Remote download (宝塔的“远程下载”): fetch a URL straight into a directory on the server,
// reporting progress, without the file passing through the browser.

import (
	"context"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/wangsirco/berth/internal/errs"
)

var fetchNameRe = regexp.MustCompile(`^[^/\\\x00]{1,200}$`)

var fetchClient = &http.Client{Timeout: 6 * time.Hour}

// Fetch downloads u into dir (name from the URL or the server's Content-Disposition when
// empty) and returns the file's path. An existing file is never overwritten.
func Fetch(ctx context.Context, root, dir, u, name string, progress func(string)) (string, error) {
	pu, err := url.Parse(u)
	if err != nil || (pu.Scheme != "http" && pu.Scheme != "https") || pu.Host == "" {
		return "", errs.New(400, "files.bad_url")
	}
	d, err := resolve(root, dir)
	if err != nil {
		return "", err
	}
	if fi, err := os.Stat(d); err != nil || !fi.IsDir() {
		return "", errs.New(404, "files.not_found", "path", dir)
	}
	req, _ := http.NewRequestWithContext(ctx, "GET", u, nil)
	req.Header.Set("User-Agent", "Berth")
	resp, err := fetchClient.Do(req)
	if err != nil {
		return "", errs.New(502, "files.fetch_failed", "detail", err.Error())
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return "", errs.New(502, "files.fetch_failed", "detail", resp.Status)
	}
	if name == "" {
		if _, ps, err := mime.ParseMediaType(resp.Header.Get("Content-Disposition")); err == nil && ps["filename"] != "" {
			name = filepath.Base(ps["filename"])
		} else {
			name = path.Base(pu.Path)
		}
	}
	if name == "" || name == "/" || name == "." || !fetchNameRe.MatchString(name) {
		name = "download"
	}
	dest := filepath.Join(d, name)
	if _, err := os.Stat(dest); err == nil {
		return "", errs.New(409, "files.exists", "path", dest)
	}
	tmp := dest + ".berth-part"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o644)
	if err != nil {
		return "", errs.New(500, "files.fetch_failed", "detail", err.Error())
	}
	total := resp.ContentLength
	var got int64
	last := time.Now()
	buf := make([]byte, 256<<10)
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := f.Write(buf[:n]); werr != nil {
				f.Close()
				os.Remove(tmp)
				return "", errs.New(500, "files.fetch_failed", "detail", werr.Error())
			}
			got += int64(n)
			if time.Since(last) > 2*time.Second {
				last = time.Now()
				if total > 0 {
					progress(fmt.Sprintf("%.0f%% · %d / %d MB", float64(got)*100/float64(total), got>>20, total>>20))
				} else {
					progress(fmt.Sprintf("%d MB", got>>20))
				}
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			f.Close()
			os.Remove(tmp)
			return "", errs.New(502, "files.fetch_failed", "detail", rerr.Error())
		}
	}
	f.Close()
	if err := os.Rename(tmp, dest); err != nil {
		os.Remove(tmp)
		return "", err
	}
	progress(fmt.Sprintf("saved %s (%d MB)", dest, got>>20))
	_ = strings.TrimSpace
	return dest, nil
}
