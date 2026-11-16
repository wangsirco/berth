package system

// SFTP sub-accounts (宝塔/1Panel 的 FTP 账号，换成更安全的 SFTP): a login that only sees one
// directory. Each account is a system user in the group berth-sftp with no shell; sshd
// locks the group into its home with internal-sftp, and the shared directory is bind-mounted
// into that home, so a site's folder can be handed out without exposing the rest.
//
//	/srv/berth/sftp/<name>          root-owned (sshd's chroot rule)
//	/srv/berth/sftp/<name>/files    ← bind mount of the shared directory (ACL gives access)
//
// sshd: one drop-in with a Match block for the group. Before it is reloaded, root's
// effective settings are compared with what they were, so the drop-in can never change how
// anyone else logs in.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/wangsirco/berth/internal/agent/drift"
)

const (
	sftpGroup   = "berth-sftp"
	sftpRoot    = "/srv/berth/sftp"
	sftpDropIn  = "/etc/ssh/sshd_config.d/90-berth-sftp.conf"
	sftpState   = "/var/lib/berth/sftp.json"
	sftpConfTxt = "# Managed by Berth (Files → SFTP accounts). Only members of berth-sftp are affected.\n" +
		"Match Group " + sftpGroup + "\n" +
		"  ChrootDirectory %h\n" +
		"  ForceCommand internal-sftp -d /files\n" +
		"  PasswordAuthentication yes\n" +
		"  PubkeyAuthentication yes\n" +
		"  AllowTcpForwarding no\n" +
		"  AllowAgentForwarding no\n" +
		"  X11Forwarding no\n" +
		"  PermitTTY no\n"
)

type SFTPAccount struct {
	Name     string `json:"name"`
	Dir      string `json:"dir"`
	ReadOnly bool   `json:"readonly,omitempty"`
	Password string `json:"password,omitempty"` // in only; "" keeps the current one
	// PasswordHash sets the password from another server's crypt hash (migration); in only
	PasswordHash string   `json:"password_hash,omitempty"`
	Keys         []string `json:"keys,omitempty"`     // authorized public keys (in); fingerprints are not kept
	HasKeys      int      `json:"has_keys,omitempty"` // out: how many keys are authorized
	Mounted      bool     `json:"mounted"`            // out
}

var (
	sftpMu     sync.Mutex
	sftpNameRe = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,30}$`)
	// directories that are never handed out
	sftpDenied = []string{"/", "/etc", "/root", "/boot", "/proc", "/sys", "/dev", "/run", "/usr", "/bin", "/sbin", "/lib", "/lib64",
		"/var/lib/berth", "/etc/berth", "/srv/berth/sftp", "/var/lib/docker", "/var/lib/containers"}
)

func sftpLoad() map[string]SFTPAccount {
	m := map[string]SFTPAccount{}
	if b, err := os.ReadFile(sftpState); err == nil {
		json.Unmarshal(b, &m)
	}
	return m
}

func sftpSave(m map[string]SFTPAccount) error {
	b, _ := json.MarshalIndent(m, "", "  ")
	return writeFileAtomic(sftpState, b, 0o600)
}

func writeFileAtomic(path string, b []byte, mode os.FileMode) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, mode); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func sftpHome(name string) string  { return sftpRoot + "/" + name }
func sftpFiles(name string) string { return sftpHome(name) + "/files" }

func sftpDirAllowed(dir string) error {
	if !filepath.IsAbs(dir) || filepath.Clean(dir) != dir {
		return errors.New("the directory must be a clean absolute path")
	}
	for _, d := range sftpDenied {
		if dir == d || (d != "/" && strings.HasPrefix(dir, d+"/")) {
			return errors.New("refuse:sftp_dir_denied")
		}
	}
	fi, err := os.Stat(dir)
	if err != nil || !fi.IsDir() {
		return errors.New("no such directory: " + dir)
	}
	return nil
}

// mountUnit is the systemd unit that bind-mounts the shared directory.
func mountUnit(ctx context.Context, name string) string {
	out, err := run(ctx, "systemd-escape", "-p", "--suffix=mount", sftpFiles(name))
	if err != nil {
		return ""
	}
	return out
}

func isMounted(path string) bool {
	b, _ := os.ReadFile("/proc/self/mountinfo")
	for _, l := range strings.Split(string(b), "\n") {
		f := strings.Fields(l)
		if len(f) > 4 && f[4] == path {
			return true
		}
	}
	return false
}

// sshdRootView is what sshd would apply to root: the drop-in must leave it unchanged.
func sshdRootView(ctx context.Context) (string, error) {
	out, err := run(ctx, sshd(), "-T", "-C", "user=root,host=localhost,addr=127.0.0.1")
	if err != nil {
		return "", errors.New(out)
	}
	var keep []string
	for _, l := range strings.Split(out, "\n") {
		k := strings.SplitN(l, " ", 2)[0]
		switch k {
		case "port", "permitrootlogin", "passwordauthentication", "pubkeyauthentication", "subsystem", "forcecommand", "chrootdirectory", "allowtcpforwarding", "kbdinteractiveauthentication":
			keep = append(keep, l)
		}
	}
	sort.Strings(keep)
	return strings.Join(keep, "\n"), nil
}

// ensureSFTPConf installs the sshd drop-in once, checked and compared before the reload.
func ensureSFTPConf(ctx context.Context, log func(string)) error {
	if b, err := os.ReadFile(sftpDropIn); err == nil && string(b) == sftpConfTxt {
		return nil
	}
	if sshd() == "" {
		return errors.New("refuse:sftp_no_sshd")
	}
	before, err := sshdRootView(ctx)
	if err != nil {
		return err
	}
	old, oldErr := os.ReadFile(sftpDropIn)
	restore := func() {
		if oldErr == nil {
			os.WriteFile(sftpDropIn, old, 0o600)
		} else {
			os.Remove(sftpDropIn)
		}
	}
	if err := os.WriteFile(sftpDropIn, []byte(sftpConfTxt), 0o600); err != nil {
		return err
	}
	run(ctx, "restorecon", sftpDropIn)
	if out, err := run(ctx, sshd(), "-t"); err != nil {
		restore()
		return errors.New("sshd -t: " + out)
	}
	after, err := sshdRootView(ctx)
	if err != nil || after != before {
		restore()
		return errors.New("refuse:sftp_conf_changes_root")
	}
	log("sshd: " + sftpDropIn + " (only the group " + sftpGroup + ")")
	drift.Track(sftpDropIn, "sshd", "sftp")
	return reloadSSHD(ctx)
}

// SELinux: OpenSSH runs internal-sftp inside sshd-session (sshd_session_t), which may not
// touch ordinary data directories. A small module lets it, for the usual data types only;
// who sees what is still decided by the chroot, the ACL and the read-only mount.
const sftpSEModule = "berth_sftp"

var sftpSETypes = []string{"var_t", "default_t", "mnt_t", "container_file_t", "httpd_sys_content_t", "httpd_sys_rw_content_t",
	"public_content_t", "public_content_rw_t", "user_home_t", "home_root_t", "samba_share_t"}

func selinuxOn() bool {
	_, err := os.Stat("/sys/fs/selinux/enforce")
	return err == nil
}

func ensureSFTPSELinux(ctx context.Context, log func(string)) error {
	if !selinuxOn() {
		return nil
	}
	if out, _ := run(ctx, "semodule", "-l"); strings.Contains("\n"+out+"\n", "\n"+sftpSEModule+"\n") {
		return nil
	}
	// only types this policy knows (an unknown one makes the module fail to load)
	probe := sftpRoot + "/.typeprobe"
	os.WriteFile(probe, nil, 0o600)
	defer os.Remove(probe)
	var b strings.Builder
	b.WriteString("; Berth: let internal-sftp (sshd_session_t) reach shared data directories\n(typeattributeset cil_gen_require sshd_session_t)\n")
	for _, t := range sftpSETypes {
		if _, err := run(ctx, "chcon", "-t", t, probe); err != nil {
			continue
		}
		fmt.Fprintf(&b, "(typeattributeset cil_gen_require %s)\n", t)
		fmt.Fprintf(&b, "(allow sshd_session_t %s (dir (getattr search open read lock ioctl add_name remove_name write create rmdir rename setattr)))\n", t)
		fmt.Fprintf(&b, "(allow sshd_session_t %s (file (getattr open read write append create unlink rename setattr lock ioctl map)))\n", t)
		fmt.Fprintf(&b, "(allow sshd_session_t %s (lnk_file (getattr read)))\n", t)
	}
	cil := "/var/lib/berth/" + sftpSEModule + ".cil"
	if err := os.WriteFile(cil, []byte(b.String()), 0o600); err != nil {
		return err
	}
	log("semodule -i " + sftpSEModule + " (SELinux: SFTP sessions may use shared directories)")
	if out, err := run(ctx, "semodule", "-i", cil); err != nil {
		return errors.New("semodule: " + out)
	}
	return nil
}

func dropSFTPSELinux(ctx context.Context) {
	if selinuxOn() {
		run(ctx, "semodule", "-r", sftpSEModule)
	}
}

func setACL(ctx context.Context, name, dir string, readonly bool) error {
	perm := "rwX"
	if readonly {
		perm = "rX"
	}
	run(ctx, "setfacl", "-R", "-x", "u:"+name, dir)
	run(ctx, "setfacl", "-R", "-d", "-x", "u:"+name, dir)
	if out, err := run(ctx, "setfacl", "-R", "-m", "u:"+name+":"+perm, dir); err != nil {
		return errors.New("setfacl: " + out)
	}
	if out, err := run(ctx, "setfacl", "-R", "-d", "-m", "u:"+name+":"+perm, dir); err != nil {
		return errors.New("setfacl: " + out)
	}
	return nil
}

func dropACL(ctx context.Context, name, dir string) {
	if dir == "" {
		return
	}
	run(ctx, "setfacl", "-R", "-x", "u:"+name, dir)
	run(ctx, "setfacl", "-R", "-d", "-x", "u:"+name, dir)
}

func writeSFTPKeys(ctx context.Context, name string, keys []string) error {
	u, err := user.Lookup(name)
	if err != nil {
		return err
	}
	dir := sftpHome(name) + "/.ssh"
	var b strings.Builder
	for _, k := range keys {
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		if strings.ContainsAny(k, "\n\r") || !strings.HasPrefix(k, "ssh-") && !strings.HasPrefix(k, "ecdsa-") && !strings.HasPrefix(k, "sk-") {
			return errors.New("not a public key: " + firstN(k, 30))
		}
		b.WriteString(k + "\n")
	}
	os.MkdirAll(dir, 0o700)
	if err := os.WriteFile(dir+"/authorized_keys", []byte(b.String()), 0o600); err != nil {
		return err
	}
	var uid, gid int
	fmt.Sscan(u.Uid, &uid)
	fmt.Sscan(u.Gid, &gid)
	os.Chown(dir, uid, gid)
	os.Chown(dir+"/authorized_keys", uid, gid)
	run(ctx, "chcon", "-R", "-t", "ssh_home_t", dir) // sshd may read it under SELinux
	return nil
}

func firstN(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

// SetSFTP creates or updates an account.
func SetSFTP(ctx context.Context, a SFTPAccount, log func(string)) error {
	sftpMu.Lock()
	defer sftpMu.Unlock()
	if !sftpNameRe.MatchString(a.Name) {
		return errors.New("name: lowercase letters, digits, - and _")
	}
	if err := sftpDirAllowed(a.Dir); err != nil {
		return err
	}
	if strings.ContainsAny(a.Password, ":\n\r") {
		return errors.New("the password may not contain ':' or line breaks")
	}
	state := sftpLoad()
	old, exists := state[a.Name]
	if _, err := user.Lookup(a.Name); err == nil && !exists {
		return errors.New("refuse:sftp_user_exists")
	}
	if !exists && a.Password == "" && a.PasswordHash == "" && len(a.Keys) == 0 {
		return errors.New("refuse:sftp_no_login")
	}
	if a.PasswordHash != "" && !cryptHashRe.MatchString(a.PasswordHash) {
		return errors.New("not a password hash")
	}
	if _, err := exec.LookPath("setfacl"); err != nil {
		return errors.New("setfacl is missing (package acl)")
	}
	if _, err := user.LookupGroup(sftpGroup); err != nil {
		if out, err := run(ctx, "groupadd", "-r", sftpGroup); err != nil {
			return errors.New("groupadd: " + out)
		}
	}
	os.MkdirAll(sftpRoot, 0o755)
	os.Chmod(sftpRoot, 0o755)
	home := sftpHome(a.Name)
	if !exists {
		log("useradd " + a.Name + " (no shell, group " + sftpGroup + ")")
		if out, err := run(ctx, "useradd", "-M", "-d", home, "-s", "/sbin/nologin", "-U", "-G", sftpGroup, "-c", "Berth SFTP", a.Name); err != nil {
			return errors.New("useradd: " + out)
		}
	}
	err := setSFTPRest(ctx, a, old, exists, log)
	if err != nil && !exists { // a new account that did not come up leaves nothing behind
		log("undoing the half-made account")
		sftpTeardown(ctx, a.Name, a.Dir)
		if len(state) == 0 {
			sftpDropShared(ctx)
		}
		return err
	}
	if err != nil {
		return err
	}
	keys := old.HasKeys
	if a.Keys != nil {
		keys = 0
		for _, k := range a.Keys {
			if strings.TrimSpace(k) != "" {
				keys++
			}
		}
	}
	state[a.Name] = SFTPAccount{Name: a.Name, Dir: a.Dir, ReadOnly: a.ReadOnly, HasKeys: keys}
	return sftpSave(state)
}

func setSFTPRest(ctx context.Context, a SFTPAccount, old SFTPAccount, exists bool, log func(string)) error {
	home := sftpHome(a.Name)
	// the chroot must be owned by root and not writable by anyone else
	os.MkdirAll(sftpFiles(a.Name), 0o755)
	os.Chown(home, 0, 0)
	os.Chmod(home, 0o755)
	if a.Password != "" {
		if err := setPassword(ctx, a.Name, a.Password); err != nil {
			return err
		}
	} else if a.PasswordHash != "" {
		cmd := exec.CommandContext(ctx, "chpasswd", "-e") // the hash on stdin, never on a command line
		cmd.Stdin = strings.NewReader(a.Name + ":" + a.PasswordHash + "\n")
		if out, err := cmd.CombinedOutput(); err != nil {
			return errors.New("chpasswd: " + strings.TrimSpace(string(out)))
		}
	}
	if a.Keys != nil {
		if err := writeSFTPKeys(ctx, a.Name, a.Keys); err != nil {
			return err
		}
	}
	// the shared directory, mounted into the home
	unit := mountUnit(ctx, a.Name)
	if unit == "" {
		return errors.New("systemd-escape failed")
	}
	if exists && old.Dir != a.Dir {
		run(ctx, "systemctl", "stop", unit)
		dropACL(ctx, a.Name, old.Dir)
	}
	if err := setACL(ctx, a.Name, a.Dir, a.ReadOnly); err != nil {
		return err
	}
	opts := "bind"
	if a.ReadOnly {
		opts = "bind,ro"
	}
	unitText := fmt.Sprintf("# Managed by Berth (SFTP account %s)\n[Unit]\nDescription=Berth SFTP %s → %s\n\n[Mount]\nWhat=%s\nWhere=%s\nType=none\nOptions=%s\n\n[Install]\nWantedBy=multi-user.target\n",
		a.Name, a.Name, a.Dir, a.Dir, sftpFiles(a.Name), opts)
	if err := os.WriteFile("/etc/systemd/system/"+unit, []byte(unitText), 0o644); err != nil {
		return err
	}
	run(ctx, "systemctl", "daemon-reload")
	log("mount " + a.Dir + " → " + sftpFiles(a.Name))
	if isMounted(sftpFiles(a.Name)) {
		run(ctx, "systemctl", "stop", unit)
	}
	if out, err := run(ctx, "systemctl", "enable", "--now", unit); err != nil {
		return errors.New("mount: " + out)
	}
	if err := ensureSFTPSELinux(ctx, log); err != nil {
		return err
	}
	return ensureSFTPConf(ctx, log)
}

// RemoveSFTP deletes the account and its mount; the shared files stay where they are.
func RemoveSFTP(ctx context.Context, name string) error {
	sftpMu.Lock()
	defer sftpMu.Unlock()
	state := sftpLoad()
	acc, ok := state[name]
	if !ok || !sftpNameRe.MatchString(name) {
		return errors.New("refuse:sftp_not_found")
	}
	if err := sftpTeardown(ctx, name, acc.Dir); err != nil {
		return err
	}
	delete(state, name)
	if len(state) == 0 {
		sftpDropShared(ctx)
	}
	return sftpSave(state)
}

// sftpTeardown removes one account's user, mount and ACL; the shared files stay.
func sftpTeardown(ctx context.Context, name, dir string) error {
	run(ctx, "pkill", "-u", name) // open sessions
	if unit := mountUnit(ctx, name); unit != "" {
		run(ctx, "systemctl", "disable", "--now", unit)
		os.Remove("/etc/systemd/system/" + unit)
		run(ctx, "systemctl", "daemon-reload")
	}
	if isMounted(sftpFiles(name)) {
		return errors.New("the shared directory is still mounted at " + sftpFiles(name) + "; nothing was removed")
	}
	dropACL(ctx, name, dir)
	run(ctx, "userdel", name)
	// only Berth's own pieces of the home: never a recursive delete (the mount may be back)
	os.Remove(sftpHome(name) + "/.ssh/authorized_keys")
	os.Remove(sftpHome(name) + "/.ssh")
	os.Remove(sftpFiles(name))
	os.Remove(sftpHome(name))
	return nil
}

// sftpDropShared takes away what all accounts shared, once the last one is gone.
func sftpDropShared(ctx context.Context) {
	if _, err := os.Stat(sftpDropIn); err == nil {
		os.Remove(sftpDropIn)
		drift.Untrack(sftpDropIn)
		reloadSSHD(ctx)
	}
	dropSFTPSELinux(ctx)
}

func ListSFTP() []SFTPAccount {
	state := sftpLoad()
	out := []SFTPAccount{}
	for _, a := range state {
		a.Mounted = isMounted(sftpFiles(a.Name))
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// crypt(3) hashes as /etc/shadow holds them ($6$…, $y$…, $2b$…)
var cryptHashRe = regexp.MustCompile(`^\$[0-9a-z]+\$[A-Za-z0-9./$=,+-]{8,200}$`)

// SFTPExport is an account with what it logs in with, for moving it to another server.
type SFTPExport struct {
	SFTPAccount
	Hash string   `json:"hash,omitempty"`
	Keys []string `json:"keys,omitempty"`
}

// ExportSFTP: the accounts with their password hashes and authorized keys (migration only).
func ExportSFTP() []SFTPExport {
	shadow := map[string]string{}
	for _, s := range readColon("/etc/shadow", 2) {
		shadow[s[0]] = s[1]
	}
	out := []SFTPExport{}
	for _, a := range ListSFTP() {
		e := SFTPExport{SFTPAccount: a}
		if h := shadow[a.Name]; cryptHashRe.MatchString(h) {
			e.Hash = h
		}
		if b, err := os.ReadFile(sftpHome(a.Name) + "/.ssh/authorized_keys"); err == nil {
			for _, l := range strings.Split(string(b), "\n") {
				if l = strings.TrimSpace(l); l != "" {
					e.Keys = append(e.Keys, l)
				}
			}
		}
		out = append(out, e)
	}
	return out
}
