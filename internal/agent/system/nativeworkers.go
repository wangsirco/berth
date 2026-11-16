package system

// Workers of a native project (queue consumers, Celery, schedulers…): long-running commands
// beside the web process, in the current release, as the project's user, in the same sandbox,
// restarted by systemd when they exit and after every deploy or rollback.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/wangsirco/berth/internal/errs"
)

type NativeWorker struct {
	Name    string `json:"name"`
	Command string `json:"command"`
}

var projWorkerRe = regexp.MustCompile(`^[a-z][a-z0-9-]{0,20}$`)

func validateWorkers(ws []NativeWorker) error {
	if len(ws) > 8 {
		return errs.New(400, "native.bad_worker", "name", "")
	}
	seen := map[string]bool{}
	for _, w := range ws {
		if !projWorkerRe.MatchString(w.Name) || seen[w.Name] || strings.TrimSpace(w.Command) == "" || strings.ContainsAny(w.Command, "\n\r") {
			return errs.New(400, "native.bad_worker", "name", w.Name)
		}
		seen[w.Name] = true
	}
	return nil
}

func workerUnit(name, w string) string { return "berth-proj-" + name + "-w-" + w + ".service" }

// workerUnitText runs one worker in the current release.
func (s NativeSpec) workerUnitText(w NativeWorker, home string) string {
	base := nativeBase(s.Name)
	cur := base + "/current"
	var b strings.Builder
	fmt.Fprintf(&b, "# Managed by Berth (native project %s, worker %s). Edits here show up as an external change.\n", s.Name, w.Name)
	fmt.Fprintf(&b, "[Unit]\nDescription=Berth project %s worker %s\nAfter=network-online.target\nWants=network-online.target\nStartLimitIntervalSec=0\n\n[Service]\n", s.Name, w.Name)
	fmt.Fprintf(&b, "User=%s\nGroup=%s\nWorkingDirectory=%s\n", nativeUser(s.Name), nativeUser(s.Name), cur)
	for _, e := range langEnv(s.Kind, home, cur) {
		if !strings.HasPrefix(e, "SERVER_PORT=") {
			fmt.Fprintf(&b, "Environment=%s\n", e)
		}
	}
	fmt.Fprintf(&b, "Environment=PATH=%s\nEnvironment=HOME=%s/shared/.home\n", runPath(s.Kind, home, cur), base)
	fmt.Fprintf(&b, "EnvironmentFile=-%s\n", nativeEnvFile(s.Name))
	fmt.Fprintf(&b, "ExecStart=/bin/sh -c %s\n", strconv.Quote("exec "+w.Command))
	b.WriteString("Restart=always\nRestartSec=5\nTimeoutStopSec=30\n")
	fmt.Fprintf(&b, "ProtectSystem=strict\nReadWritePaths=%s/shared %s/releases\n", base, base)
	b.WriteString("PrivateTmp=yes\nProtectHome=yes\nNoNewPrivileges=yes\nRestrictSUIDSGID=yes\nProtectKernelTunables=yes\nProtectKernelModules=yes\nProtectKernelLogs=yes\nProtectControlGroups=yes\nProtectClock=yes\nRestrictNamespaces=yes\nLockPersonality=yes\nRestrictRealtime=yes\nPrivateDevices=yes\n")
	if s.MemMB > 0 {
		fmt.Fprintf(&b, "MemoryMax=%dM\n", s.MemMB)
	}
	b.WriteString("\n[Install]\nWantedBy=multi-user.target\n")
	return b.String()
}

// syncWorkers writes, (re)starts and removes the project's worker units to match the spec.
func syncWorkers(ctx context.Context, s NativeSpec, home string, log func(string)) error {
	want := map[string]bool{}
	for _, w := range s.Workers {
		want[workerUnit(s.Name, w.Name)] = true
		if err := os.WriteFile(filepath.Join("/etc/systemd/system", workerUnit(s.Name, w.Name)), []byte(s.workerUnitText(w, home)), 0o644); err != nil {
			return err
		}
	}
	old, _ := filepath.Glob("/etc/systemd/system/berth-proj-" + s.Name + "-w-*.service")
	for _, f := range old {
		if u := filepath.Base(f); !want[u] {
			run(ctx, "systemctl", "disable", "--now", u)
			os.Remove(f)
		}
	}
	run(ctx, "systemctl", "daemon-reload")
	for _, w := range s.Workers {
		u := workerUnit(s.Name, w.Name)
		log("worker " + w.Name + ": systemctl restart " + u)
		run(ctx, "systemctl", "enable", u)
		if out, err := run(ctx, "systemctl", "restart", u); err != nil {
			return errs.New(500, "native.worker_failed", "name", w.Name, "detail", strings.TrimSpace(out))
		}
	}
	return nil
}

func removeWorkers(ctx context.Context, name string) {
	old, _ := filepath.Glob("/etc/systemd/system/berth-proj-" + name + "-w-*.service")
	for _, f := range old {
		run(ctx, "systemctl", "disable", "--now", filepath.Base(f))
		os.Remove(f)
	}
}

func workerStates(ctx context.Context, name string) map[string]string {
	out := map[string]string{}
	m, _ := filepath.Glob("/etc/systemd/system/berth-proj-" + name + "-w-*.service")
	for _, f := range m {
		u := filepath.Base(f)
		st, _ := run(ctx, "systemctl", "is-active", u)
		out[strings.TrimSuffix(strings.TrimPrefix(u, "berth-proj-"+name+"-w-"), ".service")] = strings.TrimSpace(st)
	}
	return out
}

// NativeSyncWorkers applies the worker list of a deployed project without a new release.
func NativeSyncWorkers(ctx context.Context, s NativeSpec, log func(string)) error {
	if err := s.Validate(); err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(nativeBase(s.Name), "current")); err != nil {
		return nil // not deployed yet: the first deploy starts them
	}
	home, err := runtimeHome(ctx, s, log)
	if err != nil {
		return err
	}
	return syncWorkers(ctx, s, home, log)
}
