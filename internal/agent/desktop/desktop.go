// Package desktop gives the server a graphical desktop (方案 §5.8): Xfce on a TigerVNC
// display that listens on 127.0.0.1 only, opened through the panel's remote desktop.
// Turning it off stops the service; uninstalling removes only what Berth installed.
package desktop

import (
	"bytes"
	"context"
	"crypto/des"
	"errors"
	"fmt"
	"github.com/wangsirco/berth/internal/agent/system"
	"github.com/wangsirco/berth/internal/sysexec"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	Unit        = "berth-desktop.service"
	unitPath    = "/etc/systemd/system/" + Unit
	scriptPath  = "/usr/local/libexec/berth-desktop"
	stateDir    = "/var/lib/berth-desktop"
	Display     = 9
	Port        = 5900 + Display // 5909: away from the 5901… people use for their own VNC
	DefaultUser = "berthdesk"
)

type Status struct {
	Supported bool   `json:"supported"` // dnf or apt
	Installed bool   `json:"installed"`
	Enabled   bool   `json:"enabled"`
	Running   bool   `json:"running"`
	User      string `json:"user,omitempty"`
	Port      int    `json:"port"`
	MemBytes  int64  `json:"mem_bytes,omitempty"`
	Packages  int    `json:"packages,omitempty"` // how many Berth installed (removed on uninstall)
}

func has(bin string) bool { _, err := exec.LookPath(bin); return err == nil }

func run(ctx context.Context, name string, args ...string) (string, error) {
	var out bytes.Buffer
	name, args = sysexec.Wrap(name, args...)
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout, cmd.Stderr = &out, &out
	cmd.Env = append(os.Environ(), "DEBIAN_FRONTEND=noninteractive")
	err := cmd.Run()
	return strings.TrimSpace(out.String()), err
}

func xvnc() string {
	for _, b := range []string{"Xvnc", "Xtigervnc"} {
		if p, err := exec.LookPath(b); err == nil {
			return p
		}
	}
	return ""
}

func Get(ctx context.Context) Status {
	st := Status{Supported: has("dnf") || has("apt-get"), Installed: xvnc() != "" && has("startxfce4"), Port: Port}
	if b, err := os.ReadFile(filepath.Join(stateDir, "user")); err == nil {
		st.User = strings.TrimSpace(string(b))
	}
	if b, err := os.ReadFile(filepath.Join(stateDir, "packages")); err == nil {
		st.Packages = len(strings.Fields(string(b)))
	}
	out, _ := run(ctx, "systemctl", "show", Unit, "-p", "ActiveState,UnitFileState,MemoryCurrent")
	for _, l := range strings.Split(out, "\n") {
		k, v, _ := strings.Cut(l, "=")
		switch k {
		case "ActiveState":
			st.Running = v == "active"
		case "UnitFileState":
			st.Enabled = v == "enabled"
		case "MemoryCurrent":
			st.MemBytes, _ = strconv.ParseInt(v, 10, 64)
		}
	}
	return st
}

// packages for the distribution: a light Xfce, the VNC X server and dbus.
func packages(ctx context.Context) (mgr string, pkgs []string) {
	if has("dnf") {
		vnc := "tigervnc-x11-server" // Fedora 45+; earlier releases call it tigervnc-server-minimal
		if _, err := run(ctx, "dnf", "-q", "list", "--available", "--installed", vnc); err != nil {
			vnc = "tigervnc-server-minimal"
		}
		return "dnf", []string{"xfce4-session", "xfwm4", "xfce4-panel", "xfdesktop", "xfce4-settings", "xfce4-terminal", "Thunar",
			"dbus-x11", "adwaita-icon-theme", "dejavu-sans-fonts", "google-noto-sans-cjk-vf-fonts", vnc}
	}
	return "apt", []string{"xfce4", "xfce4-terminal", "dbus-x11", "tigervnc-standalone-server", "fonts-noto-cjk"}
}

func installed(ctx context.Context, mgr, pkg string) bool {
	if mgr == "dnf" {
		_, err := run(ctx, "rpm", "-q", "--whatprovides", pkg)
		return err == nil
	}
	out, err := run(ctx, "dpkg-query", "-W", "-f=${Status}", pkg)
	return err == nil && strings.Contains(out, "install ok installed")
}

func step(ctx context.Context, log func(string), name string, args ...string) error {
	log("$ " + name + " " + strings.Join(args, " "))
	out, err := run(ctx, name, args...)
	n := 0
	for _, l := range strings.Split(out, "\n") {
		if l = strings.TrimSpace(l); l != "" && n < 200 && !strings.Contains(l, "% |") { // skip progress bars
			log(l)
			n++
		}
	}
	if err != nil {
		return system.Killed(fmt.Errorf("%s: %v", name, err))
	}
	return nil
}

// Install puts the desktop in (when missing) and then the picked items, one group at a time
// so the log shows progress and one unavailable package does not stop the rest. Packages
// that were not on the server before are remembered, so uninstalling takes only those.
func Install(ctx context.Context, items []string, log func(string)) error {
	if !has("dnf") && !has("apt-get") {
		return errors.New("no dnf or apt on this host")
	}
	if !validItems(items) {
		return errors.New("unknown software item")
	}
	defer system.TempSwap(ctx, log)() // small machines: dnf / apt would be killed for lack of memory
	mgr, base := packages(ctx)
	type group struct {
		name string
		pkgs []string
	}
	groups := []group{{"desktop", base}}
	for _, id := range items {
		groups = append(groups, group{id, pickPackages([]string{id})})
	}
	os.MkdirAll(stateDir, 0o755)
	prev, _ := os.ReadFile(filepath.Join(stateDir, "packages"))
	all := strings.Fields(string(prev))
	if mgr == "apt" {
		step(ctx, log, "apt-get", "update", "-qq")
	}
	var failed []string
	for n, g := range groups {
		var fresh []string
		for _, p := range g.pkgs {
			if !installed(ctx, mgr, p) {
				fresh = append(fresh, p)
			}
		}
		log(fmt.Sprintf("::progress %d/%d %s", n+1, len(groups), g.name))
		if len(fresh) == 0 {
			continue
		}
		var err error
		if mgr == "dnf" {
			err = step(ctx, log, "dnf", append([]string{"-y", "-q", "install", "--setopt=install_weak_deps=False"}, fresh...)...)
		} else {
			err = step(ctx, log, "apt-get", append([]string{"install", "-y", "-qq", "--no-install-recommends"}, fresh...)...)
		}
		if err != nil {
			if g.name == "desktop" {
				return err
			}
			failed = append(failed, g.name)
			continue
		}
		for _, p := range fresh {
			if !contains(all, p) {
				all = append(all, p)
			}
		}
		os.WriteFile(filepath.Join(stateDir, "packages"), []byte(strings.Join(all, "\n")+"\n"), 0o644)
	}
	if xvnc() == "" || !has("startxfce4") {
		return errors.New("the VNC server or Xfce is still missing after installing")
	}
	if b, err := os.ReadFile(filepath.Join(stateDir, "user")); err == nil && has("wireshark") {
		step(ctx, log, "usermod", "-aG", "wireshark", strings.TrimSpace(string(b))) // capture without root
	}
	if len(failed) > 0 {
		return fmt.Errorf("could not install: %s", strings.Join(failed, ", "))
	}
	return nil
}

func contains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

var userRe = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)

// Enable writes the VNC password and the service, then starts it. The desktop runs as an
// ordinary user (berthdesk unless another existing one is named), never as root.
var langRe = regexp.MustCompile(`^[a-z]{2}_[A-Z]{2}\.UTF-8$`)

func Enable(ctx context.Context, username, password, lang string, log func(string)) error {
	if !langRe.MatchString(lang) {
		lang = "en_US.UTF-8"
	}
	if username == "" {
		username = DefaultUser
	}
	if !userRe.MatchString(username) || username == "root" {
		return errors.New("bad user name")
	}
	if len(password) < 6 {
		return errors.New("password too short")
	}
	u, err := user.Lookup(username)
	if err != nil {
		if username != DefaultUser {
			return fmt.Errorf("no user %s on this server", username)
		}
		if err := step(ctx, log, "useradd", "-m", "-s", "/bin/bash", "-c", "Berth desktop", DefaultUser); err != nil {
			return err
		}
		if u, err = user.Lookup(username); err != nil {
			return err
		}
	}
	uid, _ := strconv.Atoi(u.Uid)
	gid, _ := strconv.Atoi(u.Gid)
	if uid == 0 {
		return errors.New("the desktop does not run as root")
	}
	os.MkdirAll(stateDir, 0o755)
	pw := filepath.Join(stateDir, "passwd")
	if err := os.WriteFile(pw, VNCPassword(password), 0o600); err != nil {
		return err
	}
	os.Chown(pw, uid, gid)
	os.WriteFile(filepath.Join(stateDir, "user"), []byte(username+"\n"), 0o644)
	userDefaults(u.HomeDir, uid, gid)
	applyLook(u.HomeDir, uid, gid, log)
	if has("wireshark") {
		run(ctx, "usermod", "-aG", "wireshark", username)
	}
	if err := os.MkdirAll(filepath.Dir(scriptPath), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(scriptPath, []byte(script()), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(unitPath, []byte(unit(username, u.HomeDir, lang)), 0o644); err != nil {
		return err
	}
	step(ctx, log, "systemctl", "daemon-reload")
	if err := step(ctx, log, "systemctl", "enable", Unit); err != nil {
		return err
	}
	if err := step(ctx, log, "systemctl", "restart", Unit); err != nil {
		return err
	}
	// wait for the VNC port: Xvnc is up within a second or two
	for i := 0; i < 40; i++ {
		if out, _ := run(ctx, "ss", "-Hltn", "sport = :"+strconv.Itoa(Port)); strings.Contains(out, "127.0.0.1:"+strconv.Itoa(Port)) {
			log("desktop listening on 127.0.0.1:" + strconv.Itoa(Port))
			return nil
		}
		time.Sleep(250 * time.Millisecond)
	}
	out, _ := run(ctx, "journalctl", "-u", Unit, "-n", "20", "--no-pager")
	log(out)
	return errors.New("the desktop did not start")
}

func Disable(ctx context.Context, log func(string)) error {
	if _, err := os.Stat(unitPath); err != nil {
		return nil
	}
	return step(ctx, log, "systemctl", "disable", "--now", Unit)
}

// Uninstall stops the desktop and removes the service and the packages Berth put in.
// The berthdesk user and its home folder stay (they may hold files).
func Uninstall(ctx context.Context, log func(string)) error {
	Disable(ctx, log)
	os.Remove(unitPath)
	os.Remove(scriptPath)
	step(ctx, log, "systemctl", "daemon-reload")
	b, _ := os.ReadFile(filepath.Join(stateDir, "packages"))
	pkgs := strings.Fields(string(b))
	if len(pkgs) > 0 {
		var err error
		if has("dnf") {
			err = step(ctx, log, "dnf", append([]string{"-y", "-q", "remove"}, pkgs...)...)
		} else {
			err = step(ctx, log, "apt-get", append([]string{"remove", "-y", "-qq", "--autoremove"}, pkgs...)...)
		}
		if err != nil {
			return err
		}
	}
	return os.RemoveAll(stateDir)
}

func script() string {
	return fmt.Sprintf(`#!/bin/sh
# Berth: Xfce on a VNC display that only this server can reach
X=$(command -v Xvnc || command -v Xtigervnc)
"$X" :%[1]d -localhost -rfbport %[2]d -SecurityTypes VncAuth -rfbauth %[3]s/passwd \
  -geometry 1600x900 -depth 24 -AlwaysShared -DisconnectClients=0 &
XPID=$!
for i in $(seq 1 50); do [ -e /tmp/.X11-unix/X%[1]d ] && break; sleep 0.1; done
export DISPLAY=:%[1]d XDG_SESSION_TYPE=x11 XDG_CURRENT_DESKTOP=XFCE
if command -v fcitx5 >/dev/null 2>&1; then # Chinese input
  export GTK_IM_MODULE=fcitx QT_IM_MODULE=fcitx XMODIFIERS=@im=fcitx
fi
dbus-run-session startxfce4
kill $XPID 2>/dev/null
`, Display, Port, stateDir)
}

func unit(username, home, lang string) string {
	return fmt.Sprintf(`# Berth: server desktop (local VNC only, opened from the panel)
[Unit]
Description=Berth desktop (Xfce over VNC on 127.0.0.1:%d)
After=network.target

[Service]
User=%s
WorkingDirectory=%s
Environment=HOME=%s
Environment=LANG=%s
ExecStartPre=-/bin/rm -f /tmp/.X%d-lock /tmp/.X11-unix/X%d
ExecStart=%s
Restart=on-failure
RestartSec=3
MemoryMax=2G

[Install]
WantedBy=multi-user.target
`, Port, username, home, home, lang, Display, Display, scriptPath)
}

// VNCPassword encodes a password the way vncpasswd does: the first 8 bytes, DES-encrypted
// with the fixed VNC key (whose bits VNC reads in reverse order).
func VNCPassword(pw string) []byte {
	fixed := []byte{23, 82, 107, 6, 35, 78, 88, 7}
	key := make([]byte, 8)
	for i, b := range fixed {
		key[i] = reverse(b)
	}
	plain := make([]byte, 8)
	copy(plain, pw)
	c, _ := des.NewCipher(key)
	out := make([]byte, 8)
	c.Encrypt(out, plain)
	return out
}

func reverse(b byte) byte {
	var r byte
	for i := 0; i < 8; i++ {
		r = r<<1 | b&1
		b >>= 1
	}
	return r
}

// userDefaults: first-time settings for the desktop user, written only when missing.
// The PolicyKit agent fails without a logind seat (an error box at every start), and the
// server's own disks do not belong on the desktop.
func userDefaults(home string, uid, gid int) {
	files := map[string]string{
		".config/autostart/xfce-polkit.desktop": "[Desktop Entry]\nType=Application\nName=PolicyKit agent (off: Berth desktop has no seat)\nHidden=true\n",
		".config/xfce4/xfconf/xfce-perchannel-xml/xfce4-desktop.xml": `<?xml version="1.0" encoding="UTF-8"?>
<channel name="xfce4-desktop" version="1.0">
  <property name="desktop-icons" type="empty">
    <property name="file-icons" type="empty">
      <property name="show-removable" type="bool" value="false"/>
      <property name="show-filesystem" type="bool" value="false"/>
    </property>
  </property>
</channel>
`,
		".config/xfce4/xfconf/xfce-perchannel-xml/xfce4-screensaver.xml": `<?xml version="1.0" encoding="UTF-8"?>
<channel name="xfce4-screensaver" version="1.0">
  <property name="saver" type="empty"><property name="enabled" type="bool" value="false"/></property>
  <property name="lock" type="empty"><property name="enabled" type="bool" value="false"/></property>
</channel>
`,
	}
	for rel, body := range files {
		p := filepath.Join(home, rel)
		if _, err := os.Stat(p); err == nil {
			continue
		}
		for d := filepath.Dir(p); strings.HasPrefix(d, home+"/"); d = filepath.Dir(d) {
			if _, err := os.Stat(d); err == nil {
				break
			}
			defer os.Chown(d, uid, gid) // after the whole path exists
		}
		os.MkdirAll(filepath.Dir(p), 0o755)
		if os.WriteFile(p, []byte(body), 0o644) == nil {
			os.Chown(p, uid, gid)
		}
	}
}
