package system

// Automatic updates in a nightly window (方案 §5.14): dnf5-automatic on Fedora-like
// systems, unattended-upgrades on Debian-like ones. Never reboots by itself; the
// overview already says when a restart is needed.

import (
	"context"
	"errors"
	"github.com/wangsirco/berth/internal/agent/drift"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

type AutoUpdate struct {
	Family    string `json:"family"` // dnf | apt | "" (not supported)
	Installed bool   `json:"installed"`
	Enabled   bool   `json:"enabled"`
	Type      string `json:"type"` // security | all
	Time      string `json:"time"` // HH:MM, "" = the package's default
}

func pkgFamily() string {
	if _, err := exec.LookPath("dnf5"); err == nil {
		return "dnf"
	}
	if _, err := exec.LookPath("dnf"); err == nil {
		return "dnf"
	}
	if _, err := exec.LookPath("apt-get"); err == nil {
		return "apt"
	}
	return ""
}

func autoTimer(fam string) string {
	if fam == "apt" {
		return "apt-daily-upgrade.timer"
	}
	if _, err := os.Stat("/usr/lib/systemd/system/dnf5-automatic.timer"); err == nil {
		return "dnf5-automatic.timer"
	}
	return "dnf-automatic.timer"
}

func timerDropIn(fam string) string {
	return "/etc/systemd/system/" + autoTimer(fam) + ".d/berth.conf"
}

var hhmmRe = regexp.MustCompile(`^([01][0-9]|2[0-3]):[0-5][0-9]$`)

func GetAutoUpdate(ctx context.Context) AutoUpdate {
	st := AutoUpdate{Family: pkgFamily(), Type: "security"}
	switch st.Family {
	case "dnf":
		_, err := os.Stat("/etc/dnf/automatic.conf")
		st.Installed = err == nil
		if b, err := os.ReadFile("/etc/dnf/automatic.conf"); err == nil {
			if m := regexp.MustCompile(`(?m)^\s*upgrade_type\s*=\s*(\w+)`).FindSubmatch(b); m != nil && string(m[1]) == "default" {
				st.Type = "all"
			}
		}
	case "apt":
		_, err := os.Stat("/usr/bin/unattended-upgrade")
		st.Installed = err == nil
		if _, err := os.Stat("/etc/apt/apt.conf.d/52berth-all"); err == nil {
			st.Type = "all"
		}
	default:
		return st
	}
	if st.Installed {
		out, _ := run(ctx, "systemctl", "is-enabled", autoTimer(st.Family))
		st.Enabled = out == "enabled"
		if st.Family == "apt" && st.Enabled {
			b, _ := os.ReadFile("/etc/apt/apt.conf.d/20auto-upgrades")
			st.Enabled = strings.Contains(string(b), `Unattended-Upgrade "1"`)
		}
	}
	if b, err := os.ReadFile(timerDropIn(st.Family)); err == nil {
		if m := regexp.MustCompile(`OnCalendar=\*-\*-\* (\d\d:\d\d)`).FindSubmatch(b); m != nil {
			st.Time = string(m[1])
		}
	}
	return st
}

func SetAutoUpdate(ctx context.Context, want AutoUpdate, log func(string)) error {
	fam := pkgFamily()
	if fam == "" {
		return errors.New("refuse:autoupdate_unsupported")
	}
	if want.Type != "security" && want.Type != "all" {
		return errors.New("type must be security or all")
	}
	if want.Time != "" && !hhmmRe.MatchString(want.Time) {
		return errors.New("time must be HH:MM")
	}
	timer := autoTimer(fam)
	if !want.Enabled {
		log("systemctl disable --now " + timer)
		run(ctx, "systemctl", "disable", "--now", timer)
		if fam == "apt" {
			os.WriteFile("/etc/apt/apt.conf.d/20auto-upgrades", []byte("APT::Periodic::Update-Package-Lists \"1\";\nAPT::Periodic::Unattended-Upgrade \"0\";\n"), 0o644)
		}
		return nil
	}
	if !GetAutoUpdate(ctx).Installed {
		pkg := "dnf5-plugin-automatic"
		args := []string{"-y", "install", pkg}
		name := "dnf5"
		if _, err := exec.LookPath("dnf5"); err != nil {
			name, pkg = "dnf", "dnf-automatic"
			args = []string{"-y", "install", pkg}
		}
		if fam == "apt" {
			name, args = "apt-get", []string{"-y", "install", "unattended-upgrades"}
		}
		log(name + " " + strings.Join(args, " "))
		if out, err := run(ctx, name, args...); err != nil {
			return errors.New(out)
		}
		timer = autoTimer(fam)
	}
	switch fam {
	case "dnf":
		ut := map[string]string{"security": "security", "all": "default"}[want.Type]
		conf := "# Managed by Berth (panel → System → Updates)\n[commands]\nupgrade_type = " + ut + "\nrandom_sleep = 0\ndownload_updates = yes\napply_updates = yes\nreboot = never\n\n[emitters]\nemit_via = stdio\n"
		if err := os.WriteFile("/etc/dnf/automatic.conf", []byte(conf), 0o644); err != nil {
			return err
		}
	case "apt":
		os.WriteFile("/etc/apt/apt.conf.d/20auto-upgrades", []byte("APT::Periodic::Update-Package-Lists \"1\";\nAPT::Periodic::Unattended-Upgrade \"1\";\n"), 0o644)
		if want.Type == "all" {
			os.WriteFile("/etc/apt/apt.conf.d/52berth-all", []byte("// Managed by Berth: install all updates, not only security ones\nUnattended-Upgrade::Origins-Pattern { \"origin=*\"; };\n"), 0o644)
		} else {
			os.Remove("/etc/apt/apt.conf.d/52berth-all")
		}
	}
	drop := timerDropIn(fam)
	if want.Time == "" {
		os.Remove(drop)
	} else {
		os.MkdirAll(filepath.Dir(drop), 0o755)
		os.WriteFile(drop, []byte("# Managed by Berth\n[Timer]\nOnCalendar=\nOnCalendar=*-*-* "+want.Time+"\nRandomizedDelaySec=15m\n"), 0o644)
	}
	if fam == "dnf" {
		drift.Track("/etc/dnf/automatic.conf", "autoupdate", "")
	}
	drift.Track(drop, "autoupdate", "")
	run(ctx, "systemctl", "daemon-reload")
	log("systemctl enable --now " + timer)
	if out, err := run(ctx, "systemctl", "enable", "--now", timer); err != nil {
		return errors.New(out)
	}
	run(ctx, "systemctl", "restart", timer)
	return nil
}
