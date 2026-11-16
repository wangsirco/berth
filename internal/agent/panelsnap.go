package agent

// Panel snapshot (面板快照): a tar.gz of /etc/berth and /var/lib/berth with a consistent
// copy of the panel database in place of the live one. The panel seals it with a password
// before it leaves the machine; `berth restore-panel` puts it back on another one.

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"

	"github.com/wangsirco/berth/internal/errs"
)

// SnapshotSkip are data-dir entries left out: rebuilt or downloaded again, or large.
var SnapshotSkip = []string{"trash", "geo", "updates", "uploads", "rollback", "drift", "accesslog-state.json", "berth.db", "berth.db-wal", "berth.db-shm", "backup-snapshot.db", "panel-snapshot.db"}

func skipped(rel string) bool {
	first := strings.SplitN(rel, "/", 2)[0]
	for _, s := range SnapshotSkip {
		if first == s {
			return true
		}
	}
	return strings.HasSuffix(rel, ".part") || strings.HasSuffix(rel, ".tmp")
}

func addTree(tw *tar.Writer, dir, prefix string, skip func(string) bool) error {
	return filepath.Walk(dir, func(p string, fi os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(dir, p)
		if rel == "." {
			return nil
		}
		if skip != nil && skip(filepath.ToSlash(rel)) {
			if fi.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if !fi.Mode().IsRegular() && !fi.IsDir() {
			return nil
		}
		h, err := tar.FileInfoHeader(fi, "")
		if err != nil {
			return nil
		}
		if st, ok := fi.Sys().(*syscall.Stat_t); ok {
			if u, err := user.LookupId(strconv.Itoa(int(st.Uid))); err == nil {
				h.Uname = u.Username
			}
			if g, err := user.LookupGroupId(strconv.Itoa(int(st.Gid))); err == nil {
				h.Gname = g.Name
			}
		}
		h.Name = prefix + "/" + filepath.ToSlash(rel)
		if err := tw.WriteHeader(h); err != nil {
			return err
		}
		if fi.Mode().IsRegular() {
			f, err := os.Open(p)
			if err != nil {
				return err
			}
			_, err = io.Copy(tw, f)
			f.Close()
			return err
		}
		return nil
	})
}

var (
	snapDataRe = regexp.MustCompile(`^/var/lib/berth(-[a-z0-9]+)?$`)
	snapEtcRe  = regexp.MustCompile(`^/etc/berth(-[a-z0-9]+)?$`)
)

func registerPanelSnapshot(s *Server) {
	s.Handle("POST /v1/panel/snapshot", func(w http.ResponseWriter, r *http.Request, body []byte) {
		var in struct {
			DB      string `json:"db"`       // the panel's VACUUM INTO copy, in its data dir
			DataDir string `json:"data_dir"` // the panel's directories (another instance may use /var/lib/berth-x)
			EtcDir  string `json:"etc_dir"`
		}
		json.Unmarshal(body, &in)
		if in.DataDir == "" {
			in.DataDir, in.EtcDir = DataDir, "/etc/berth"
		}
		if !snapDataRe.MatchString(in.DataDir) || !snapEtcRe.MatchString(in.EtcDir) ||
			filepath.Dir(in.DB) != in.DataDir || filepath.Base(in.DB) != "panel-snapshot.db" {
			WriteErr(w, errs.New(400, "params.invalid_json"))
			return
		}
		db, err := os.Open(in.DB)
		if err != nil {
			WriteErr(w, errs.New(404, "files.not_found", "path", in.DB))
			return
		}
		defer os.Remove(in.DB)
		defer db.Close()
		w.Header().Set("Content-Type", "application/gzip")
		gz := gzip.NewWriter(w)
		tw := tar.NewWriter(gz)
		// always stored under the standard names, so it restores onto a normal install
		addTree(tw, in.EtcDir, "etc/berth", nil)
		addTree(tw, in.DataDir, "var/lib/berth", skipped)
		fi, _ := db.Stat()
		h, _ := tar.FileInfoHeader(fi, "")
		h.Name, h.Uname, h.Gname = "var/lib/berth/berth.db", "berth", "berth"
		tw.WriteHeader(h)
		io.Copy(tw, db)
		tw.Close()
		gz.Close()
	})
}
