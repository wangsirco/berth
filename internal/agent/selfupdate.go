package agent

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/wangsirco/berth/internal/errs"
)

// Self-update (方案 §13): the panel downloads and verifies a release tarball; the agent
// (root) backs up the current binaries and database, installs the new ones, and starts a
// watchdog that restarts both services and rolls back if the new panel does not come up.

var (
	BinDir     = "/usr/local/bin"
	DataDir    = "/var/lib/berth"
	UnitDir    = "/etc/systemd/system"
	updatesDir = func() string { return filepath.Join(DataDir, "updates") }
)

var installable = map[string]string{ // name in tarball -> destination dir
	"berth": "bin", "berth-panel": "bin", "berth-agent.service": "unit", "berth-panel.service": "unit",
}

func dest(name string) string {
	if installable[name] == "unit" {
		return filepath.Join(UnitDir, name)
	}
	return filepath.Join(BinDir, name)
}

// stage unpacks the release tarball into dir, keeping only known files.
func stage(tarball, dir string) ([]string, error) {
	f, err := os.Open(tarball)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return nil, errs.New(400, "update.bad_package", "detail", err.Error())
	}
	tr := tar.NewReader(gz)
	var names []string
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, errs.New(400, "update.bad_package", "detail", err.Error())
		}
		name := filepath.Base(h.Name)
		if _, ok := installable[name]; !ok || h.Typeflag != tar.TypeReg {
			continue
		}
		out, err := os.OpenFile(filepath.Join(dir, name), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
		if err != nil {
			return nil, err
		}
		if _, err := io.Copy(out, io.LimitReader(tr, 256<<20)); err != nil {
			out.Close()
			return nil, err
		}
		out.Close()
		names = append(names, name)
	}
	for _, need := range []string{"berth", "berth-panel"} {
		found := false
		for _, n := range names {
			found = found || n == need
		}
		if !found {
			return nil, errs.New(400, "update.bad_package", "detail", "missing "+need)
		}
	}
	return names, nil
}

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".berth-new"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, dst) // atomic: a running binary keeps its old inode
}

// Install stages tarball, backs up what it replaces into rollbackDir, and installs.
func Install(tarball, sha, rollbackDir string) ([]string, error) {
	b, err := os.ReadFile(tarball)
	if err != nil {
		return nil, err
	}
	if sum := sha256.Sum256(b); hex.EncodeToString(sum[:]) != strings.ToLower(sha) {
		return nil, errs.New(400, "update.bad_checksum")
	}
	stageDir, err := os.MkdirTemp(updatesDir(), "stage-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(stageDir)
	names, err := stage(tarball, stageDir)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(rollbackDir, 0o700); err != nil {
		return nil, err
	}
	for _, n := range names {
		if _, err := os.Stat(dest(n)); err == nil {
			if err := copyFile(dest(n), filepath.Join(rollbackDir, n), 0o755); err != nil {
				return nil, fmt.Errorf("backup %s: %w", n, err)
			}
		}
	}
	// the database is copied while the panel is still running; SQLite WAL files go along
	for _, f := range []string{"berth.db", "berth.db-wal", "berth.db-shm"} {
		if _, err := os.Stat(filepath.Join(DataDir, f)); err == nil {
			_ = copyFile(filepath.Join(DataDir, f), filepath.Join(rollbackDir, f), 0o600)
		}
	}
	for _, n := range names {
		mode := os.FileMode(0o755)
		if installable[n] == "unit" {
			mode = 0o644
		}
		if err := copyFile(filepath.Join(stageDir, n), dest(n), mode); err != nil {
			Restore(rollbackDir, false)
			return nil, fmt.Errorf("install %s: %w", n, err)
		}
	}
	return names, nil
}

// Restore puts back what Install replaced (and the database if withDB).
func Restore(rollbackDir string, withDB bool) {
	for n := range installable {
		if _, err := os.Stat(filepath.Join(rollbackDir, n)); err == nil {
			mode := os.FileMode(0o755)
			if installable[n] == "unit" {
				mode = 0o644
			}
			_ = copyFile(filepath.Join(rollbackDir, n), dest(n), mode)
		}
	}
	if withDB {
		for _, f := range []string{"berth.db", "berth.db-wal", "berth.db-shm"} {
			if _, err := os.Stat(filepath.Join(rollbackDir, f)); err == nil {
				_ = copyFile(filepath.Join(rollbackDir, f), filepath.Join(DataDir, f), 0o600)
				_ = exec.Command("chown", "berth:berth", filepath.Join(DataDir, f)).Run()
			}
		}
	}
}

// UpdateResult is written by the watchdog for the panel to report after restart.
type UpdateResult struct {
	From, To   string
	OK         bool
	RolledBack bool
	Detail     string
	At         int64
}

func WriteResult(r UpdateResult) {
	r.At = time.Now().Unix()
	b, _ := json.Marshal(r)
	_ = os.WriteFile(filepath.Join(DataDir, "update-result.json"), b, 0o640)
	_ = exec.Command("chown", "root:berth", filepath.Join(DataDir, "update-result.json")).Run()
}

func registerSelfUpdate(s *Server) {
	s.Handle("POST /v1/self/update", func(w http.ResponseWriter, r *http.Request, body []byte) {
		var in struct {
			Tarball string `json:"tarball"`
			SHA256  string `json:"sha256"`
			From    string `json:"from"`
			To      string `json:"to"`
		}
		if err := json.Unmarshal(body, &in); err != nil {
			WriteErr(w, errs.New(400, "params.invalid_json"))
			return
		}
		// only files the panel put into its updates folder
		if filepath.Dir(filepath.Clean(in.Tarball)) != updatesDir() {
			WriteErr(w, errs.New(400, "update.bad_package", "detail", "unexpected path"))
			return
		}
		rb := filepath.Join(DataDir, "rollback", time.Now().Format("20060102-150405"))
		names, err := Install(in.Tarball, in.SHA256, rb)
		if err != nil {
			WriteErr(w, err)
			return
		}
		_ = os.Remove(in.Tarball)
		// the watchdog runs outside both services so it survives their restart
		unit := "berth-update-" + time.Now().Format("150405")
		cmd := exec.Command("systemd-run", "--unit", unit, "--on-active=2", filepath.Join(BinDir, "berth"), "update-finish",
			"--rollback", rb, "--from", in.From, "--to", in.To)
		if out, err := cmd.CombinedOutput(); err != nil {
			Restore(rb, false)
			WriteErr(w, errs.New(500, "update.watchdog_failed", "detail", strings.TrimSpace(string(out))))
			return
		}
		WriteJSON(w, 200, map[string]any{"installed": names, "rollback": rb, "watchdog": unit})
	})
}
