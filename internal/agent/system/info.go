// Package system is the server itself (方案 §5.13): what it is, its network cards,
// routes and resolvers, services, processes, disks, updates and power. Changes that could
// cut the panel off (network settings) go through NetworkManager checkpoints that roll
// back on their own unless confirmed.
package system

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"github.com/wangsirco/berth/internal/sysexec"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

func has(bin string) bool { _, err := exec.LookPath(bin); return err == nil }

func run(ctx context.Context, name string, args ...string) (string, error) {
	var out bytes.Buffer
	name, args = sysexec.Wrap(name, args...)
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout, cmd.Stderr = &out, &out
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	err := cmd.Run()
	return strings.TrimSpace(out.String()), err
}

type Info struct {
	Hostname   string `json:"hostname"`
	OS         string `json:"os"`
	Kernel     string `json:"kernel"`
	Arch       string `json:"arch"`
	Model      string `json:"model,omitempty"` // board / machine
	CPU        string `json:"cpu,omitempty"`
	Cores      int    `json:"cores"`
	MemTotal   int64  `json:"mem_total"`
	PageSize   int    `json:"page_size"`
	Virt       string `json:"virt,omitempty"` // none | kvm | lxc …
	BootTime   int64  `json:"boot_time"`
	Timezone   string `json:"timezone"`
	NTP        bool   `json:"ntp"`        // time sync service on
	NTPSynced  bool   `json:"ntp_synced"` // clock currently synchronised
	Time       int64  `json:"time"`
	NeedReboot bool   `json:"need_reboot"` // updates wait for a restart
	Scheduled  string `json:"scheduled,omitempty"`
	PkgManager string `json:"pkg_manager,omitempty"` // dnf | apt
	NetManager string `json:"net_manager,omitempty"` // NetworkManager when it runs
}

func GetInfo(ctx context.Context) Info {
	in := Info{Arch: runtime.GOARCH, Cores: runtime.NumCPU(), PageSize: os.Getpagesize(), Time: time.Now().Unix()}
	in.Hostname, _ = os.Hostname()
	if b, err := os.ReadFile("/etc/os-release"); err == nil {
		if m := regexp.MustCompile(`(?m)^PRETTY_NAME="?([^"\n]+)`).FindSubmatch(b); m != nil {
			in.OS = string(m[1])
		}
	}
	if b, err := os.ReadFile("/proc/sys/kernel/osrelease"); err == nil {
		in.Kernel = strings.TrimSpace(string(b))
	}
	for _, p := range []string{"/sys/firmware/devicetree/base/model", "/sys/devices/virtual/dmi/id/product_name"} {
		if b, err := os.ReadFile(p); err == nil {
			if s := strings.Trim(strings.TrimSpace(string(b)), "\x00"); s != "" {
				in.Model = s
				break
			}
		}
	}
	in.CPU = cpuModel()
	if f, err := os.Open("/proc/meminfo"); err == nil {
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			if f := strings.Fields(sc.Text()); len(f) >= 2 && f[0] == "MemTotal:" {
				kb, _ := strconv.ParseInt(f[1], 10, 64)
				in.MemTotal = kb * 1024
			}
		}
		f.Close()
	}
	if b, err := os.ReadFile("/proc/stat"); err == nil {
		if m := regexp.MustCompile(`(?m)^btime (\d+)`).FindSubmatch(b); m != nil {
			in.BootTime, _ = strconv.ParseInt(string(m[1]), 10, 64)
		}
	}
	if has("systemd-detect-virt") {
		in.Virt, _ = run(ctx, "systemd-detect-virt")
	}
	if out, err := run(ctx, "timedatectl", "show", "-p", "Timezone", "-p", "NTP", "-p", "NTPSynchronized"); err == nil {
		for _, l := range strings.Split(out, "\n") {
			k, v, _ := strings.Cut(l, "=")
			switch k {
			case "Timezone":
				in.Timezone = v
			case "NTP":
				in.NTP = v == "yes"
			case "NTPSynchronized":
				in.NTPSynced = v == "yes"
			}
		}
	}
	switch {
	case has("dnf"):
		in.PkgManager = "dnf"
		in.NeedReboot = dnfNeedsReboot()
	case has("apt-get"):
		in.PkgManager = "apt"
		_, err := os.Stat("/run/reboot-required")
		in.NeedReboot = err == nil
	}
	if out, _ := run(ctx, "systemctl", "is-active", "NetworkManager"); out == "active" {
		in.NetManager = "NetworkManager"
	}
	in.Scheduled = scheduledPower()
	return in
}

func cpuModel() string {
	b, err := os.ReadFile("/proc/cpuinfo")
	if err != nil {
		return ""
	}
	for _, key := range []string{"model name", "Hardware", "cpu model"} {
		if m := regexp.MustCompile(`(?m)^` + key + `\s*:\s*(.+)$`).FindSubmatch(b); m != nil {
			return strings.TrimSpace(string(m[1]))
		}
	}
	// ARM: implementer / part numbers only; lscpu knows the names
	if out, err := run(context.Background(), "lscpu"); err == nil {
		names := []string{}
		for _, l := range strings.Split(out, "\n") {
			if k, v, ok := strings.Cut(l, ":"); ok && strings.TrimSpace(k) == "Model name" {
				if v = strings.TrimSpace(v); v != "" && !contains(names, v) {
					names = append(names, v)
				}
			}
		}
		return strings.Join(names, " + ")
	}
	return ""
}

func contains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

var hostnameRe = regexp.MustCompile(`^[a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?$`)

func SetHostname(ctx context.Context, name string) error {
	if !hostnameRe.MatchString(name) {
		return errors.New("bad host name")
	}
	if out, err := run(ctx, "hostnamectl", "set-hostname", name); err != nil {
		return errors.New(out)
	}
	return nil
}

var tzRe = regexp.MustCompile(`^[A-Za-z_]+(/[A-Za-z0-9_+-]+){0,2}$|^UTC$`)

func SetTimezone(ctx context.Context, tz string) error {
	if !tzRe.MatchString(tz) {
		return errors.New("bad time zone")
	}
	if out, err := run(ctx, "timedatectl", "set-timezone", tz); err != nil {
		return errors.New(out)
	}
	return nil
}

func SetNTP(ctx context.Context, on bool) error {
	v := "false"
	if on {
		v = "true"
	}
	if out, err := run(ctx, "timedatectl", "set-ntp", v); err != nil {
		return errors.New(out)
	}
	return nil
}

func Timezones(ctx context.Context) []string {
	out, err := run(ctx, "timedatectl", "list-timezones")
	if err != nil {
		return []string{}
	}
	return strings.Fields(out)
}

// ---- power ----

const powerUnit = "berth-power"

// SchedulePower reboots or powers off after `minutes` (0 = in a few seconds, so the
// answer reaches the panel first). A pending one is replaced.
func SchedulePower(ctx context.Context, action string, minutes int) error {
	if action != "reboot" && action != "poweroff" {
		return errors.New("bad action")
	}
	if minutes < 0 || minutes > 7*24*60 {
		return errors.New("bad delay")
	}
	CancelPower(ctx)
	when := strconv.Itoa(minutes*60+5) + "s"
	if out, err := run(ctx, "systemd-run", "--unit", powerUnit, "--description", "Berth: "+action, "--on-active", when, "/usr/bin/systemctl", action); err != nil {
		return errors.New(out)
	}
	return nil
}

func CancelPower(ctx context.Context) {
	run(ctx, "systemctl", "stop", powerUnit+".timer")
	run(ctx, "systemctl", "reset-failed", powerUnit+".timer", powerUnit+".service")
}

// scheduledPower: "reboot@UNIX" when a Berth timer waits.
func scheduledPower() string {
	out, err := run(context.Background(), "systemctl", "show", powerUnit+".timer", "-p", "ActiveState", "-p", "NextElapseUSecMonotonic", "-p", "Description")
	if err != nil || !strings.Contains(out, "ActiveState=active") {
		return ""
	}
	action := "reboot"
	if strings.Contains(out, "poweroff") {
		action = "poweroff"
	}
	// the monotonic deadline: uptime-based
	var next time.Duration
	for _, l := range strings.Split(out, "\n") {
		if v, ok := strings.CutPrefix(l, "NextElapseUSecMonotonic="); ok {
			next = parseSystemdSpan(v)
		}
	}
	up := uptime()
	at := time.Now().Add(next - up)
	return action + "@" + strconv.FormatInt(at.Unix(), 10)
}

func uptime() time.Duration {
	b, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return 0
	}
	f, _ := strconv.ParseFloat(strings.Fields(string(b))[0], 64)
	return time.Duration(f * float64(time.Second))
}

// parseSystemdSpan reads "1d 2h 3min 4.5s" style spans.
func parseSystemdSpan(s string) time.Duration {
	var d time.Duration
	for _, part := range strings.Fields(s) {
		i := strings.IndexFunc(part, func(r rune) bool { return (r < '0' || r > '9') && r != '.' })
		if i <= 0 {
			continue
		}
		n, _ := strconv.ParseFloat(part[:i], 64)
		unit := map[string]time.Duration{"d": 24 * time.Hour, "h": time.Hour, "min": time.Minute, "s": time.Second, "ms": time.Millisecond, "us": time.Microsecond}[part[i:]]
		d += time.Duration(n * float64(unit))
	}
	return d
}

// dnfNeedsReboot: `dnf needs-restarting -r` takes a while (and now and then refreshes its
// metadata), so the answer is kept for ten minutes and refreshed in the background; the
// page never waits for it.
var reboot struct {
	sync.Mutex
	known, need, busy bool
	at                time.Time
}

func dnfNeedsReboot() bool {
	reboot.Lock()
	defer reboot.Unlock()
	if !reboot.busy && (!reboot.known || time.Since(reboot.at) > 10*time.Minute) {
		reboot.busy = true
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			_, err := run(ctx, "dnf", "needs-restarting", "-r")
			var ee *exec.ExitError
			need := err != nil && errors.As(err, &ee) && ee.ExitCode() == 1
			reboot.Lock()
			reboot.known, reboot.need, reboot.busy, reboot.at = true, need, false, time.Now()
			reboot.Unlock()
		}()
	}
	return reboot.need
}

// ForgetReboot: after updates the answer may have changed.
func ForgetReboot() {
	reboot.Lock()
	reboot.known = false
	reboot.Unlock()
}
