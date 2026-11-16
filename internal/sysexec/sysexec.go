// Package sysexec runs the agent's heavy helpers (package managers, backups, git) outside
// the agent's own cgroup. The agent is held to a small memory budget (MemoryHigh=48M,
// MemoryMax=96M); a child such as dnf counted against it is throttled into seconds of
// reclaim, or killed. `systemd-run --scope` moves the child into a scope of its own and
// then runs it directly, so its output and cancellation work as before.
package sysexec

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

var heavy = map[string]bool{"dnf": true, "dnf5": true, "yum": true, "apt-get": true, "apt": true, "restic": true, "rclone": true, "git": true, "semodule": true}

var (
	once sync.Once
	ok   bool
)

func usable() bool {
	once.Do(func() {
		_, err := exec.LookPath("systemd-run")
		_, sd := os.Stat("/run/systemd/system")
		ok = err == nil && sd == nil && os.Geteuid() == 0
	})
	return ok
}

// Wrap returns the command to run instead: unchanged for light ones, in a scope of its own
// for heavy ones (when systemd is there).
func Wrap(name string, args ...string) (string, []string) {
	if !isHeavy(name, args) || !usable() {
		return name, args
	}
	return "systemd-run", append([]string{"--scope", "--quiet", "--collect", "--", name}, args...)
}

// Scoped is Wrap for a command line that is heavy whatever its name (e.g. sh -c "apt-get …").
func Scoped(name string, args ...string) (string, []string) {
	if !usable() {
		return name, args
	}
	return "systemd-run", append([]string{"--scope", "--quiet", "--collect", "--", name}, args...)
}

// isHeavy judges by the program that runs: `env VAR=… apt-get …` is apt-get (Debian's package
// installs went through env and so stayed in the agent's cgroup, throttled to a standstill).
func isHeavy(name string, args []string) bool {
	if filepath.Base(name) == "env" {
		for _, a := range args {
			if strings.HasPrefix(a, "-") || strings.Contains(a, "=") {
				continue
			}
			return heavy[filepath.Base(a)]
		}
		return false
	}
	return heavy[filepath.Base(name)]
}
