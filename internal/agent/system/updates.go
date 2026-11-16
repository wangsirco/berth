package system

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"sync"
	"time"
)

type Update struct {
	Name     string `json:"name"`
	Version  string `json:"version"`
	Repo     string `json:"repo,omitempty"`
	Security bool   `json:"security"`
	Kernel   bool   `json:"kernel"` // needs a restart to take effect
}

type Updates struct {
	Items     []Update `json:"items"`
	CheckedAt int64    `json:"checked_at"`
	Error     string   `json:"error,omitempty"`
}

var (
	updMu    sync.Mutex
	updCache Updates
)

var kernelPkgRe = regexp.MustCompile(`^(kernel|linux-image|kernel-16k|kernel-core|kernel-modules)`)

// CheckUpdates lists pending package updates (cached for 30 minutes unless refresh).
func CheckUpdates(ctx context.Context, refresh bool) Updates {
	updMu.Lock()
	defer updMu.Unlock()
	if !refresh && updCache.CheckedAt > time.Now().Add(-30*time.Minute).Unix() {
		return updCache
	}
	u := Updates{Items: []Update{}, CheckedAt: time.Now().Unix()}
	switch {
	case has("dnf"):
		c, cancel := context.WithTimeout(ctx, 4*time.Minute)
		defer cancel()
		out, err := run(c, "dnf", "-q", "check-upgrade")
		if err != nil && !strings.Contains(err.Error(), "exit status 100") { // 100 = updates available
			u.Error = lastLine(out)
		}
		sec := map[string]bool{}
		if s, err := run(c, "dnf", "-q", "updateinfo", "list", "--security"); err == nil {
			for _, l := range strings.Split(s, "\n") {
				if f := strings.Fields(l); len(f) >= 3 {
					sec[pkgName(f[len(f)-1])] = true
				}
			}
		}
		for _, l := range strings.Split(out, "\n") {
			f := strings.Fields(l)
			if len(f) != 3 || !strings.Contains(f[0], ".") || strings.HasPrefix(l, " ") {
				continue
			}
			name := f[0][:strings.LastIndex(f[0], ".")]
			u.Items = append(u.Items, Update{Name: name, Version: f[1], Repo: f[2], Security: sec[name], Kernel: kernelPkgRe.MatchString(name)})
		}
	case has("apt-get"):
		c, cancel := context.WithTimeout(ctx, 4*time.Minute)
		defer cancel()
		run(c, "apt-get", "update", "-qq")
		out, _ := run(c, "apt-get", "-s", "-qq", "upgrade")
		re := regexp.MustCompile(`^Inst (\S+) (?:\[[^\]]*\] )?\((\S+) ([^)]*)\)`)
		for _, l := range strings.Split(out, "\n") {
			if m := re.FindStringSubmatch(l); m != nil {
				u.Items = append(u.Items, Update{Name: m[1], Version: m[2], Repo: m[3], Security: strings.Contains(m[3], "security"), Kernel: kernelPkgRe.MatchString(m[1])})
			}
		}
	default:
		u.Error = "no dnf or apt"
	}
	updCache = u
	return u
}

// pkgName strips "-version-release.arch" from an NEVRA.
func pkgName(nevra string) string {
	parts := strings.Split(nevra, "-")
	if len(parts) >= 3 {
		return strings.Join(parts[:len(parts)-2], "-")
	}
	return nevra
}

func lastLine(s string) string {
	l := strings.Split(strings.TrimSpace(s), "\n")
	return l[len(l)-1]
}

// ApplyUpdates installs pending updates (security ones only when asked).
func ApplyUpdates(ctx context.Context, securityOnly bool, log func(string)) error {
	defer ForgetReboot() // the kernel or libc may now be newer than what runs
	stepf := func(name string, args ...string) error {
		log("$ " + name + " " + strings.Join(args, " "))
		out, err := run(ctx, name, args...)
		n := 0
		for _, l := range strings.Split(out, "\n") {
			if l = strings.TrimSpace(l); l != "" && !strings.Contains(l, "% |") && n < 400 {
				log(l)
				n++
			}
		}
		if err != nil {
			return errors.New(lastLine(out))
		}
		return nil
	}
	var err error
	switch {
	case has("dnf"):
		args := []string{"-y", "upgrade"}
		if securityOnly {
			args = append(args, "--security")
		}
		err = stepf("dnf", args...)
	case has("apt-get"):
		stepf("apt-get", "update", "-qq")
		if securityOnly {
			// Debian/Ubuntu: unattended-upgrade knows which origins are security
			if has("unattended-upgrade") {
				err = stepf("unattended-upgrade", "-v")
				break
			}
		}
		err = stepf("env", "DEBIAN_FRONTEND=noninteractive", "apt-get", "-y", "-o", "Dpkg::Options::=--force-confold", "upgrade")
	default:
		err = errors.New("no dnf or apt")
	}
	updMu.Lock()
	updCache = Updates{}
	updMu.Unlock()
	return err
}
