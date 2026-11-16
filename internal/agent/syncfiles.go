package agent

// File sync (文件同步, 宝塔的同名插件): a directory pushed with rsync over SSH to another
// managed host on a schedule. The source host has its own key; the target lets that key
// in only for `berth sync-serve <dir>`, which accepts nothing but an rsync receiver
// confined to that directory — no shell, no reading, no other paths.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/wangsirco/berth/internal/errs"
	"github.com/wangsirco/berth/internal/sysexec"
)

var (
	SyncDir     = DataDir + "/sync"
	syncPathRe  = regexp.MustCompile(`^/[A-Za-z0-9/._@+-]*$`)
	syncTagRe   = regexp.MustCompile(`^berth-sync-[a-z0-9._-]{1,80}$`)
	shortOptsRe = regexp.MustCompile(`^-[A-Za-z0-9.]+$`)
	longOpts    = map[string]bool{"--server": true, "--delete": true, "--delete-after": true, "--delete-during": true, "--delete-delay": true,
		"--partial": true, "--numeric-ids": true, "--safe-links": true, "--inplace": true, "--append": true, "--fsync": true}
)

// ValidSyncDir: absolute, no "..", nothing that needs quoting, not the whole system.
func ValidSyncDir(d string) bool {
	if !syncPathRe.MatchString(d) || filepath.Clean(d) != d || d == "/" {
		return false
	}
	for _, bad := range []string{"/etc", "/usr", "/bin", "/sbin", "/lib", "/lib64", "/boot", "/proc", "/sys", "/dev", "/run", "/root/.ssh", "/var/lib/berth", "/etc/berth"} {
		if d == bad || strings.HasPrefix(d, bad+"/") {
			return false
		}
	}
	return true
}

// ensureRsync installs rsync when it is missing.
func ensureRsync(ctx context.Context) error {
	if _, err := exec.LookPath("rsync"); err == nil {
		return nil
	}
	var name string
	var args []string
	switch {
	case hasBin("dnf"):
		name, args = "dnf", []string{"-y", "-q", "install", "rsync"}
	case hasBin("apt-get"):
		name, args = "apt-get", []string{"-y", "-q", "install", "rsync"}
	default:
		return errors.New("rsync is not installed and there is no dnf or apt to install it")
	}
	n, a := sysexec.Wrap(name, args...)
	cmd := exec.CommandContext(ctx, n, a...)
	if name == "apt-get" {
		cmd.Env = append(os.Environ(), "DEBIAN_FRONTEND=noninteractive")
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		return errors.New("installing rsync: " + strings.TrimSpace(string(out)))
	}
	return nil
}

func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }

func hasBin(b string) bool { _, err := exec.LookPath(b); return err == nil }

// syncKey makes (once) this host's sync key and returns the public half.
func syncKey(ctx context.Context) (string, error) {
	if err := ensureRsync(ctx); err != nil {
		return "", err
	}
	key := filepath.Join(SyncDir, "id_ed25519")
	if _, err := os.Stat(key); err != nil {
		if err := os.MkdirAll(SyncDir, 0o700); err != nil {
			return "", err
		}
		host, _ := os.Hostname()
		if out, err := exec.CommandContext(ctx, "ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", "berth-sync@"+host, "-f", key).CombinedOutput(); err != nil {
			return "", errors.New(strings.TrimSpace(string(out)))
		}
	}
	b, err := os.ReadFile(key + ".pub")
	return strings.TrimSpace(string(b)), err
}

const authKeys = "/root/.ssh/authorized_keys"

// syncAuthorize adds (or with remove, drops) the restricted key line tagged tag.
func syncAuthorize(ctx context.Context, pub, dir, tag string, remove bool) error {
	if !syncTagRe.MatchString(tag) {
		return errs.New(400, "sync.bad_name")
	}
	var lines []string
	if b, err := os.ReadFile(authKeys); err == nil {
		for _, l := range strings.Split(strings.TrimRight(string(b), "\n"), "\n") {
			if l != "" && !strings.HasSuffix(l, " "+tag) {
				lines = append(lines, l)
			}
		}
	}
	if !remove {
		f := strings.Fields(pub)
		if len(f) < 2 || f[0] != "ssh-ed25519" {
			return errs.New(400, "sync.bad_key")
		}
		if !ValidSyncDir(dir) {
			return errs.New(400, "sync.bad_dir", "dir", dir)
		}
		if !hasBin("sshd") && !fileExists("/usr/sbin/sshd") {
			return errs.New(409, "sync.no_sshd")
		}
		if err := ensureRsync(ctx); err != nil {
			return err
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		self, _ := os.Executable()
		if self == "" || strings.Contains(self, " ") {
			self = "/usr/local/bin/berth"
		}
		lines = append(lines, `command="`+self+` sync-serve `+dir+`",restrict `+f[0]+" "+f[1]+" "+tag)
	}
	if err := os.MkdirAll(filepath.Dir(authKeys), 0o700); err != nil {
		return err
	}
	tmp := authKeys + ".berth-new"
	body := strings.Join(lines, "\n")
	if body != "" {
		body += "\n"
	}
	if err := os.WriteFile(tmp, []byte(body), 0o600); err != nil {
		return err
	}
	exec.Command("restorecon", tmp).Run()
	if err := os.Rename(tmp, authKeys); err != nil {
		return err
	}
	exec.Command("restorecon", "-R", "/root/.ssh").Run()
	return nil
}

// SyncServeArgs checks what the SSH client asked to run and returns the rsync command to
// run instead: a receiver only, with its destination moved under dir.
func SyncServeArgs(dir, original string) ([]string, error) {
	if !ValidSyncDir(dir) {
		return nil, errors.New("bad sync directory")
	}
	f := strings.Fields(original)
	if len(f) < 4 || f[0] != "rsync" || f[1] != "--server" {
		return nil, errors.New("only rsync is allowed with this key")
	}
	out := []string{"rsync"}
	dot, path := false, false
	for i, a := range f[1:] {
		last := i == len(f)-2
		switch {
		case a == "--sender":
			return nil, errors.New("this key may only send files here, not read them")
		case last && dot:
			rel := filepath.Clean("/" + a)
			if strings.Contains(a, "..") {
				return nil, errors.New("path outside the sync directory")
			}
			out = append(out, filepath.Join(dir, rel)+"/")
			path = true
		case a == ".":
			dot = true
			out = append(out, a)
		case longOpts[a] || shortOptsRe.MatchString(a) || strings.HasPrefix(a, "--compress-level="):
			out = append(out, a)
		default:
			return nil, errors.New("rsync option not allowed: " + a)
		}
	}
	if !dot || !path {
		return nil, errors.New("unexpected rsync command")
	}
	return out, nil
}

func registerSync(s *Server) {
	s.Handle("POST /v1/sync/key", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		pub, err := syncKey(r.Context())
		if err != nil {
			WriteErr(w, errs.New(500, "sync.failed", "detail", err.Error()))
			return
		}
		WriteJSON(w, 200, map[string]string{"pub": pub})
	})
	s.Handle("POST /v1/sync/authorize", func(w http.ResponseWriter, r *http.Request, body []byte) {
		var in struct {
			Pub, Dir, Tag string
			Remove        bool
		}
		json.Unmarshal(body, &in)
		if err := syncAuthorize(r.Context(), in.Pub, in.Dir, in.Tag, in.Remove); err != nil {
			if _, ok := err.(*errs.E); !ok {
				err = errs.New(500, "sync.failed", "detail", err.Error())
			}
			WriteErr(w, err)
			return
		}
		WriteJSON(w, 200, map[string]bool{"ok": true})
	})
}
