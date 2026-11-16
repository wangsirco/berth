package system

// SSH server settings and authorized keys (方案 §5.14). Berth writes one drop-in that sorts
// before the distribution's own, checks it with `sshd -t`, reloads, and arms a timer that
// puts the previous file back unless the change is confirmed — so a wrong port or a
// disabled login method cannot lock anyone out for good.

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"github.com/wangsirco/berth/internal/agent/drift"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	sshDropIn     = "/etc/ssh/sshd_config.d/00-berth-panel.conf"
	sshPrev       = "/var/lib/berth/ssh-prev.conf"
	sshPending    = "/var/lib/berth/ssh-pending"
	sshRollback   = "/var/lib/berth/ssh-rollback.sh"
	sshTimerUnit  = "berth-ssh-rollback"
	sshArmed      = "/var/lib/berth/ssh-armed" // a change was made; gone once kept or rolled back by Berth
	absentMarker  = "#berth:absent\n"
	sshdConfigDir = "/etc/ssh/sshd_config.d"
)

type SSHSettings struct {
	Port         int    `json:"port"`
	PasswordAuth bool   `json:"password_auth"`
	RootLogin    string `json:"root_login"` // yes | prohibit-password | no
	PubkeyAuth   bool   `json:"pubkey_auth"`
	Remove       bool   `json:"remove,omitempty"` // drop Berth's file (back to the system's own settings)
}

type SSHState struct {
	Effective  SSHSettings `json:"effective"`
	Managed    bool        `json:"managed"` // Berth's drop-in exists
	Pending    bool        `json:"pending"`
	RollbackAt int64       `json:"rollback_at,omitempty"`
	Socket     bool        `json:"socket"` // started by ssh.socket (port lives there)
	Installed  bool        `json:"installed"`
	Keys       []UserKeys  `json:"keys"`
}

type AuthKey struct {
	Type        string `json:"type"`
	Comment     string `json:"comment"`
	Fingerprint string `json:"fingerprint"`
}

type UserKeys struct {
	User string    `json:"user"`
	Keys []AuthKey `json:"keys"`
}

func sshd() string {
	for _, p := range []string{"/usr/sbin/sshd", "/usr/bin/sshd", "/sbin/sshd"} {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

// effectiveSSH asks sshd what it would use (keys are matched case-insensitively: newer
// OpenSSH prints them capitalized).
func effectiveSSH(ctx context.Context) (SSHSettings, error) {
	s := SSHSettings{Port: 22, PubkeyAuth: true, RootLogin: "prohibit-password"}
	bin := sshd()
	if bin == "" {
		return s, errors.New("sshd is not installed")
	}
	out, err := run(ctx, bin, "-T")
	if err != nil {
		return s, errors.New(out)
	}
	ports := 0
	for _, l := range strings.Split(out, "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(l), " ")
		if !ok {
			continue
		}
		switch strings.ToLower(k) {
		case "port":
			if ports == 0 {
				s.Port, _ = strconv.Atoi(v)
			}
			ports++
		case "passwordauthentication":
			s.PasswordAuth = v == "yes"
		case "permitrootlogin":
			s.RootLogin = v
			if v == "without-password" {
				s.RootLogin = "prohibit-password"
			}
		case "pubkeyauthentication":
			s.PubkeyAuth = v == "yes"
		}
	}
	return s, nil
}

func sshPendingAt() int64 {
	b, err := os.ReadFile(sshPending)
	if err != nil {
		return 0
	}
	at, _ := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
	if at < time.Now().Unix()-5 {
		os.Remove(sshPending) // the timer has put the old file back by now
		return 0
	}
	return at
}

func GetSSH(ctx context.Context) SSHState {
	st := SSHState{Keys: []UserKeys{}}
	st.Installed = sshd() != ""
	st.Effective, _ = effectiveSSH(ctx)
	_, err := os.Stat(sshDropIn)
	st.Managed = err == nil
	if at := sshPendingAt(); at > 0 {
		st.Pending, st.RollbackAt = true, at
	}
	out, _ := run(ctx, "systemctl", "is-active", "ssh.socket")
	st.Socket = out == "active"
	for _, u := range keyUsers() {
		st.Keys = append(st.Keys, UserKeys{User: u.Username, Keys: readKeys(filepath.Join(u.HomeDir, ".ssh", "authorized_keys"))})
	}
	return st
}

// keyUsers: root and people with a login shell.
func keyUsers() []*user.User {
	var out []*user.User
	if u, err := user.Lookup("root"); err == nil {
		out = append(out, u)
	}
	for _, a := range Accounts().Users {
		if !a.System && !a.NoLogin {
			if u, err := user.Lookup(a.Name); err == nil {
				out = append(out, u)
			}
		}
	}
	return out
}

var keyTypeRe = regexp.MustCompile(`^(ssh-(ed25519|rsa|dss)|ecdsa-sha2-nistp(256|384|521)|sk-(ssh-ed25519|ecdsa-sha2-nistp256)@openssh\.com)$`)

// parseKey reads one authorized_keys line (options before the type are allowed).
func parseKey(line string) (AuthKey, bool) {
	f := strings.Fields(line)
	for i := 0; i+1 < len(f); i++ {
		if keyTypeRe.MatchString(f[i]) {
			blob, err := base64.StdEncoding.DecodeString(f[i+1])
			if err != nil || len(blob) < 16 {
				return AuthKey{}, false
			}
			sum := sha256.Sum256(blob)
			return AuthKey{Type: f[i], Comment: strings.Join(f[i+2:], " "), Fingerprint: "SHA256:" + base64.RawStdEncoding.EncodeToString(sum[:])}, true
		}
	}
	return AuthKey{}, false
}

func readKeys(path string) []AuthKey {
	out := []AuthKey{}
	f, err := os.Open(path)
	if err != nil {
		return out
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		l := strings.TrimSpace(sc.Text())
		if l == "" || strings.HasPrefix(l, "#") {
			continue
		}
		if k, ok := parseKey(l); ok {
			out = append(out, k)
		}
	}
	return out
}

// ModifyKeys adds a public key to, or removes one (by fingerprint) from, a user's
// authorized_keys. It returns the fingerprint it acted on and, when removing, the line.
func ModifyKeys(who, add, removeFP string) (string, string, error) {
	fp, line, err := modifyKeys(who, add, removeFP)
	return fp, line, err
}

func modifyKeys(who, add, removeFP string) (fp, removed string, err error) {
	if !unameRe.MatchString(who) {
		return "", "", errors.New("bad user")
	}
	u, err := user.Lookup(who)
	if err != nil {
		return "", "", errors.New("no such user: " + who)
	}
	uid, _ := strconv.Atoi(u.Uid)
	gid, _ := strconv.Atoi(u.Gid)
	dir := filepath.Join(u.HomeDir, ".ssh")
	path := filepath.Join(dir, "authorized_keys")
	b, _ := os.ReadFile(path)
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	if len(lines) == 1 && lines[0] == "" {
		lines = nil
	}
	switch {
	case add != "":
		add = strings.TrimSpace(add)
		if strings.ContainsAny(add, "\r\n") {
			return "", "", errors.New("one key per line")
		}
		k, ok := parseKey(add)
		if !ok {
			return "", "", errors.New("not an SSH public key")
		}
		fp = k.Fingerprint
		for _, l := range lines {
			if x, ok := parseKey(l); ok && x.Fingerprint == fp {
				return fp, "", nil // already there
			}
		}
		lines = append(lines, add)
	case removeFP != "":
		fp = removeFP
		kept := lines[:0]
		found := false
		for _, l := range lines {
			if x, ok := parseKey(l); ok && x.Fingerprint == fp {
				found = true
				removed = l
				continue
			}
			kept = append(kept, l)
		}
		if !found {
			return "", "", errors.New("key not found")
		}
		lines = kept
	default:
		return "", "", errors.New("nothing to do")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", "", err
	}
	os.Chown(dir, uid, gid)
	out := strings.Join(lines, "\n")
	if out != "" {
		out += "\n"
	}
	tmp := path + ".berth-tmp"
	if err := os.WriteFile(tmp, []byte(out), 0o600); err != nil {
		return "", "", err
	}
	os.Chown(tmp, uid, gid)
	if err := os.Rename(tmp, path); err != nil {
		return "", "", err
	}
	run(context.Background(), "restorecon", "-R", dir) // SELinux labels for sshd
	return fp, removed, nil
}

func sshContent(s SSHSettings) string {
	yn := map[bool]string{true: "yes", false: "no"}
	var b strings.Builder
	b.WriteString("# Managed by Berth (panel → System → SSH). Edits here show up as an external change.\n")
	if s.Port != 0 && s.Port != 22 {
		fmt.Fprintf(&b, "Port %d\n", s.Port)
	}
	fmt.Fprintf(&b, "PasswordAuthentication %s\n", yn[s.PasswordAuth])
	fmt.Fprintf(&b, "KbdInteractiveAuthentication %s\n", yn[s.PasswordAuth])
	fmt.Fprintf(&b, "PermitRootLogin %s\n", s.RootLogin)
	fmt.Fprintf(&b, "PubkeyAuthentication %s\n", yn[s.PubkeyAuth])
	return b.String()
}

func reloadSSHD(ctx context.Context) error {
	for _, u := range []string{"sshd", "ssh"} {
		if out, _ := run(ctx, "systemctl", "is-active", u+".service"); out == "active" {
			if out, err := run(ctx, "systemctl", "reload", u+".service"); err != nil {
				return errors.New(out)
			}
			return nil
		}
	}
	return errors.New("the SSH service is not running")
}

func firewalldOn(ctx context.Context) bool {
	out, _ := run(ctx, "systemctl", "is-active", "firewalld")
	return out == "active"
}

// SetSSH writes the drop-in and arms the rollback timer (secs ≥ 30; 0 = 60).
func SetSSH(ctx context.Context, s SSHSettings, secs int) error {
	if s.Remove {
		s = SSHSettings{Remove: true, RootLogin: "yes", PasswordAuth: true}
	}
	if s.RootLogin != "yes" && s.RootLogin != "prohibit-password" && s.RootLogin != "no" {
		return errors.New("root login must be yes, prohibit-password or no")
	}
	if s.Port != 0 && (s.Port < 1 || s.Port > 65535) {
		return errors.New("bad port")
	}
	if !s.PasswordAuth && !s.PubkeyAuth {
		return errors.New("refuse:ssh_no_method")
	}
	if secs == 0 {
		secs = 60
	}
	if secs < 30 || secs > 600 {
		return errors.New("rollback time must be 30–600 seconds")
	}
	if sshd() == "" {
		return errors.New("sshd is not installed")
	}
	cur, err := effectiveSSH(ctx)
	if err != nil {
		return err
	}
	if s.Port == 0 || s.Remove {
		s.Port = cur.Port
	}
	if s.Port != cur.Port {
		if out, _ := run(ctx, "systemctl", "is-active", "ssh.socket"); out == "active" {
			return errors.New("refuse:ssh_socket")
		}
	}
	if sshPendingAt() > 0 {
		return errors.New("refuse:ssh_pending")
	}
	prev, err := os.ReadFile(sshDropIn)
	if err != nil {
		prev = []byte(absentMarker)
	}
	os.MkdirAll("/var/lib/berth", 0o755)
	if err := os.WriteFile(sshPrev, prev, 0o600); err != nil {
		return err
	}
	restore := func() {
		if string(prev) == absentMarker {
			os.Remove(sshDropIn)
		} else {
			os.WriteFile(sshDropIn, prev, 0o600)
		}
	}
	os.MkdirAll(sshdConfigDir, 0o755)
	if s.Remove {
		os.Remove(sshDropIn)
	} else if err := os.WriteFile(sshDropIn, []byte(sshContent(s)), 0o600); err != nil {
		return err
	}
	run(ctx, "restorecon", sshDropIn)
	if out, err := run(ctx, sshd(), "-t"); err != nil {
		restore()
		return errors.New("sshd rejected the settings: " + out)
	}
	if now, _ := effectiveSSH(ctx); !s.Remove && (now.Port != s.Port || now.PasswordAuth != s.PasswordAuth || now.RootLogin != s.RootLogin || now.PubkeyAuth != s.PubkeyAuth) {
		restore()
		return errors.New("refuse:ssh_overridden")
	}
	// a new port: SELinux and firewalld must let sshd use it
	var undoFW string
	if s.Port != cur.Port {
		if out, _ := run(ctx, "getenforce"); out == "Enforcing" {
			if out, err := run(ctx, "semanage", "port", "-a", "-t", "ssh_port_t", "-p", "tcp", strconv.Itoa(s.Port)); err != nil && !strings.Contains(out, "already defined") {
				restore()
				return errors.New("SELinux would block the new port: " + out)
			}
		}
		if firewalldOn(ctx) {
			p := strconv.Itoa(s.Port) + "/tcp"
			if out, _ := run(ctx, "firewall-cmd", "--query-port="+p); out != "yes" {
				run(ctx, "firewall-cmd", "--add-port="+p)
				undoFW = "firewall-cmd --remove-port=" + p + " >/dev/null 2>&1\n"
			}
		}
	}
	script := "#!/bin/sh\n# put the SSH settings back unless the panel confirmed them\n"
	if string(prev) == absentMarker {
		script += "rm -f " + sshDropIn + "\n"
	} else {
		script += "cp " + sshPrev + " " + sshDropIn + "\n"
	}
	script += undoFW + "systemctl reload sshd.service 2>/dev/null || systemctl reload ssh.service\nrm -f " + sshPending + "\n"
	os.WriteFile(sshRollback, []byte(script), 0o700)
	at := time.Now().Add(time.Duration(secs) * time.Second).Unix()
	run(ctx, "systemctl", "stop", sshTimerUnit+".timer")
	run(ctx, "systemctl", "reset-failed", sshTimerUnit+".service")
	if out, err := run(ctx, "systemd-run", "--unit", sshTimerUnit, "--description", "Berth: undo SSH settings unless confirmed",
		"--on-active", strconv.Itoa(secs)+"s", "/bin/sh", sshRollback); err != nil {
		restore()
		return errors.New("could not arm the rollback timer: " + out)
	}
	os.WriteFile(sshPending, []byte(strconv.FormatInt(at, 10)), 0o600)
	os.WriteFile(sshArmed, nil, 0o600)
	drift.Track(sshDropIn, "sshd", "")
	if err := reloadSSHD(ctx); err != nil {
		RollbackSSH(ctx)
		return err
	}
	return nil
}

// ConfirmSSH keeps the new settings: stops the timer and makes a new port's firewall
// opening permanent.
func ConfirmSSH(ctx context.Context) error {
	if sshPendingAt() == 0 {
		return errors.New("nothing waits for confirmation (it may have rolled back already)")
	}
	run(ctx, "systemctl", "stop", sshTimerUnit+".timer")
	if b, err := os.ReadFile(sshRollback); err == nil {
		for _, l := range strings.Split(string(b), "\n") {
			if p, ok := strings.CutPrefix(l, "firewall-cmd --remove-port="); ok {
				p, _, _ = strings.Cut(p, " ")
				run(ctx, "firewall-cmd", "--permanent", "--add-port="+p)
			}
		}
	}
	os.Remove(sshPending)
	os.Remove(sshRollback)
	os.Remove(sshPrev)
	os.Remove(sshArmed)
	drift.Track(sshDropIn, "sshd", "")
	return nil
}

func RollbackSSH(ctx context.Context) error {
	if _, err := os.Stat(sshRollback); err != nil {
		return nil
	}
	run(ctx, "systemctl", "stop", sshTimerUnit+".timer")
	out, err := run(ctx, "/bin/sh", sshRollback)
	if err != nil {
		return errors.New(out)
	}
	os.Remove(sshRollback)
	os.Remove(sshPrev)
	os.Remove(sshArmed)
	drift.Track(sshDropIn, "sshd", "")
	return nil
}

// settleSSH notices a rollback done by the timer (not by the agent) and takes the restored
// file as Berth's again, so it is not reported as an external change.
func settleSSH() {
	if _, err := os.Stat(sshArmed); err != nil {
		return
	}
	if sshPendingAt() > 0 {
		return
	}
	os.Remove(sshArmed)
	drift.Track(sshDropIn, "sshd", "")
}

func init() {
	drift.BeforeCheck(settleSSH)
	drift.OnRestore("sshd", func(ctx context.Context, path string) error {
		if out, err := run(ctx, sshd(), "-t"); err != nil {
			return errors.New("sshd rejects the restored file: " + out)
		}
		return reloadSSHD(ctx)
	})
	drift.OnRestore("sysctl", func(ctx context.Context, path string) error {
		if _, err := os.Stat(path); err != nil {
			return nil
		}
		if out, err := run(ctx, "sysctl", "-p", path); err != nil {
			return errors.New(out)
		}
		return nil
	})
	drift.OnRestore("resolver", func(ctx context.Context, path string) error {
		_, err := run(ctx, "systemctl", "restart", "systemd-resolved")
		return err
	})
	for _, k := range []string{"zram", "autoupdate"} {
		drift.OnRestore(k, func(ctx context.Context, path string) error {
			_, err := run(ctx, "systemctl", "daemon-reload")
			return err
		})
	}
}
