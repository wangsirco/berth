package system

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// ---- services ----

type Service struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Load        string `json:"load"`
	Active      string `json:"active"` // active | inactive | failed | activating …
	Sub         string `json:"sub"`    // running | exited | dead …
	Enabled     string `json:"enabled,omitempty"`
	MemBytes    int64  `json:"mem_bytes,omitempty"`
	Protected   string `json:"protected,omitempty"` // why stopping it is refused
}

// protected units: stopping them cuts the panel or the server off
var protectedUnits = map[string]string{
	"berth-agent.service": "panel", "berth-panel.service": "panel", "sshd.service": "ssh", "ssh.service": "ssh",
	"NetworkManager.service": "network", "systemd-networkd.service": "network", "systemd-journald.service": "system",
	"systemd-logind.service": "system", "dbus.service": "system", "dbus-broker.service": "system", "firewalld.service": "firewall",
	"docker.service": "docker", "containerd.service": "docker",
}

func Services(ctx context.Context) ([]Service, error) {
	out, err := run(ctx, "systemctl", "list-units", "--type=service", "--all", "--no-pager", "--output=json")
	if err != nil {
		return nil, errors.New(out)
	}
	var raw []struct {
		Unit, Load, Active, Sub, Description string
	}
	if err := json.Unmarshal([]byte(out), &raw); err != nil {
		return nil, err
	}
	enabled := map[string]string{}
	if out, err := run(ctx, "systemctl", "list-unit-files", "--type=service", "--no-pager", "--output=json"); err == nil {
		var files []struct {
			UnitFile string `json:"unit_file"`
			State    string `json:"state"`
		}
		json.Unmarshal([]byte(out), &files)
		for _, f := range files {
			enabled[f.UnitFile] = f.State
		}
	}
	list := make([]Service, 0, len(raw))
	seen := map[string]bool{}
	for _, r := range raw {
		if r.Load == "not-found" {
			continue
		}
		s := Service{Name: r.Unit, Description: r.Description, Load: r.Load, Active: r.Active, Sub: r.Sub, Enabled: enabled[r.Unit], Protected: protectedUnits[r.Unit]}
		if r.Active == "active" {
			s.MemBytes = readInt("/sys/fs/cgroup/system.slice/" + r.Unit + "/memory.current")
		}
		seen[r.Unit] = true
		list = append(list, s)
	}
	// installed but never loaded (disabled services)
	for name, st := range enabled {
		if !seen[name] && !strings.Contains(name, "@") && (st == "disabled" || st == "enabled") {
			list = append(list, Service{Name: name, Load: "loaded", Active: "inactive", Sub: "dead", Enabled: st, Protected: protectedUnits[name]})
		}
	}
	rank := map[string]int{"failed": 0, "active": 1, "activating": 1, "reloading": 1, "deactivating": 2, "inactive": 3}
	sort.Slice(list, func(i, j int) bool {
		if rank[list[i].Active] != rank[list[j].Active] {
			return rank[list[i].Active] < rank[list[j].Active]
		}
		return list[i].Name < list[j].Name
	})
	return list, nil
}

var unitRe = regexp.MustCompile(`^[a-zA-Z0-9:_.@-]+\.service$`)

func ServiceAction(ctx context.Context, name, action string) error {
	if !unitRe.MatchString(name) {
		return errors.New("bad unit name")
	}
	switch action {
	case "start", "restart", "enable", "disable", "stop":
	default:
		return errors.New("bad action")
	}
	if why := protectedUnits[name]; why != "" && (action == "stop" || action == "disable") {
		return errors.New("protected:" + why)
	}
	args := []string{action, name}
	if action == "enable" || action == "disable" {
		args = []string{action, "--now", name}
		if action == "enable" {
			args = []string{"enable", name} // enabling does not start it; start is its own button
		}
	}
	if out, err := run(ctx, "systemctl", args...); err != nil {
		return errors.New(out)
	}
	return nil
}

func ServiceLogs(ctx context.Context, name string, lines int) (string, error) {
	if !unitRe.MatchString(name) {
		return "", errors.New("bad unit name")
	}
	if lines <= 0 || lines > 2000 {
		lines = 300
	}
	out, _ := run(ctx, "journalctl", "-u", name, "-n", strconv.Itoa(lines), "--no-pager", "-o", "short-iso")
	return out, nil
}

// ---- processes ----

type Process struct {
	PID       int     `json:"pid"`
	PPID      int     `json:"ppid"`
	Name      string  `json:"name"`
	User      string  `json:"user"`
	State     string  `json:"state"`
	CPU       float64 `json:"cpu"` // % of one core
	RSS       int64   `json:"rss"`
	Threads   int     `json:"threads"`
	Started   int64   `json:"started"`
	Cmd       string  `json:"cmd"`
	Unit      string  `json:"unit,omitempty"`      // systemd service it belongs to
	Container string  `json:"container,omitempty"` // Docker container (filled by the handler)
	Instance  string  `json:"instance,omitempty"`  // Incus instance
	Scope     string  `json:"-"`
}

type procTimes struct {
	ticks uint64
}

var clkTck = 100.0

func readProcs() map[int]procTimes {
	out := map[int]procTimes{}
	ents, _ := os.ReadDir("/proc")
	for _, e := range ents {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		b, err := os.ReadFile("/proc/" + e.Name() + "/stat")
		if err != nil {
			continue
		}
		f := statFields(string(b))
		if len(f) < 22 {
			continue
		}
		ut, _ := strconv.ParseUint(f[13], 10, 64)
		st, _ := strconv.ParseUint(f[14], 10, 64)
		out[pid] = procTimes{ut + st}
	}
	return out
}

// statFields splits /proc/PID/stat, keeping the (comm) field whole.
func statFields(s string) []string {
	i, j := strings.IndexByte(s, '('), strings.LastIndexByte(s, ')')
	if i < 0 || j < i {
		return nil
	}
	return append([]string{s[:i-1], s[i+1 : j]}, strings.Fields(s[j+1:])...)
}

var lxcRe = regexp.MustCompile(`lxc\.payload\.([a-z0-9-]+)`)

var dockerScopeRe = regexp.MustCompile(`docker-([0-9a-f]{12,64})\.scope|/docker/([0-9a-f]{12,64})`)

// Processes samples CPU over a short window and returns the busiest first.
func Processes(ctx context.Context, limit int) []Process {
	a := readProcs()
	t0 := time.Now()
	select {
	case <-ctx.Done():
	case <-time.After(500 * time.Millisecond):
	}
	b := readProcs()
	secs := time.Since(t0).Seconds()
	boot := int64(0)
	if bb, err := os.ReadFile("/proc/stat"); err == nil {
		if m := regexp.MustCompile(`(?m)^btime (\d+)`).FindSubmatch(bb); m != nil {
			boot, _ = strconv.ParseInt(string(m[1]), 10, 64)
		}
	}
	page := int64(os.Getpagesize())
	users := map[string]string{}
	list := []Process{}
	for pid, t := range b {
		dir := "/proc/" + strconv.Itoa(pid)
		st, err := os.ReadFile(dir + "/stat")
		if err != nil {
			continue
		}
		f := statFields(string(st))
		if len(f) < 24 {
			continue
		}
		p := Process{PID: pid, Name: f[1], State: f[2]}
		p.PPID, _ = strconv.Atoi(f[3])
		p.Threads, _ = strconv.Atoi(f[19])
		start, _ := strconv.ParseFloat(f[21], 64)
		p.Started = boot + int64(start/clkTck)
		rss, _ := strconv.ParseInt(f[23], 10, 64)
		p.RSS = rss * page
		if prev, ok := a[pid]; ok && secs > 0 {
			p.CPU = float64(t.ticks-prev.ticks) / clkTck / secs * 100
		}
		if cmd, err := os.ReadFile(dir + "/cmdline"); err == nil {
			p.Cmd = strings.TrimSpace(strings.ReplaceAll(string(cmd), "\x00", " "))
			if len(p.Cmd) > 300 {
				p.Cmd = p.Cmd[:300] + "…"
			}
		}
		if p.Cmd == "" {
			p.Cmd = "[" + p.Name + "]" // kernel thread
		}
		if fi, err := os.Stat(dir); err == nil {
			uid := strconv.Itoa(int(fi.Sys().(*syscall.Stat_t).Uid))
			if _, ok := users[uid]; !ok {
				if u, err := user.LookupId(uid); err == nil {
					users[uid] = u.Username
				} else {
					users[uid] = uid
				}
			}
			p.User = users[uid]
		}
		if cg, err := os.ReadFile(dir + "/cgroup"); err == nil {
			sc := bufio.NewScanner(strings.NewReader(string(cg)))
			for sc.Scan() {
				_, path, _ := strings.Cut(sc.Text(), "::")
				if path == "" {
					continue
				}
				p.Scope = path
				if m := lxcRe.FindStringSubmatch(path); m != nil {
					p.Instance = m[1] // inside an Incus instance; its users are mapped
				} else if m := dockerScopeRe.FindStringSubmatch(path); m != nil {
					p.Container = m[1] + m[2] // the id; the handler names it
				} else if base := filepath.Base(path); strings.HasSuffix(base, ".service") {
					p.Unit = base
				}
			}
		}
		list = append(list, p)
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].CPU != list[j].CPU {
			return list[i].CPU > list[j].CPU
		}
		return list[i].RSS > list[j].RSS
	})
	if limit > 0 && len(list) > limit {
		list = list[:limit]
	}
	return list
}

// Signal sends TERM or KILL; the server's own core and the panel are refused.
func Signal(pid int, sig string) error {
	if pid <= 1 || pid == os.Getpid() || pid == os.Getppid() {
		return errors.New("protected process")
	}
	b, _ := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/cgroup")
	for unit, why := range protectedUnits {
		if why != "docker" && strings.Contains(string(b), "/"+unit) {
			return errors.New("protected:" + why)
		}
	}
	s := syscall.SIGTERM
	if sig == "KILL" {
		s = syscall.SIGKILL
	}
	return syscall.Kill(pid, s)
}

// ---- disks ----

type BlockDev struct {
	Name     string     `json:"name"`
	Type     string     `json:"type"` // disk | part | rom | loop | crypt | lvm
	Size     int64      `json:"size"`
	Model    string     `json:"model,omitempty"`
	Serial   string     `json:"serial,omitempty"`
	Tran     string     `json:"tran,omitempty"` // nvme | sata | usb
	Rota     bool       `json:"rota"`           // spinning disk
	FSType   string     `json:"fstype,omitempty"`
	Label    string     `json:"label,omitempty"`
	Mounts   []string   `json:"mounts,omitempty"`
	Used     int64      `json:"used,omitempty"`
	Avail    int64      `json:"avail,omitempty"`
	Children []BlockDev `json:"children,omitempty"`
}

func Disks(ctx context.Context) ([]BlockDev, error) {
	out, err := run(ctx, "lsblk", "-J", "-b", "-o", "NAME,TYPE,SIZE,MODEL,SERIAL,TRAN,ROTA,FSTYPE,LABEL,MOUNTPOINTS")
	if err != nil {
		return nil, errors.New(out)
	}
	var raw struct {
		Blockdevices []json.RawMessage `json:"blockdevices"`
	}
	if err := json.Unmarshal([]byte(out), &raw); err != nil {
		return nil, err
	}
	var conv func(json.RawMessage) BlockDev
	conv = func(m json.RawMessage) BlockDev {
		var r struct {
			Name, Type, Model, Serial, Tran, Fstype, Label string
			Size                                           any
			Rota                                           any
			Mountpoints                                    []*string
			Children                                       []json.RawMessage
		}
		json.Unmarshal(m, &r)
		d := BlockDev{Name: r.Name, Type: r.Type, Model: strings.TrimSpace(r.Model), Serial: r.Serial, Tran: r.Tran, FSType: r.Fstype, Label: r.Label}
		switch v := r.Size.(type) {
		case float64:
			d.Size = int64(v)
		case string:
			d.Size, _ = strconv.ParseInt(v, 10, 64)
		}
		d.Rota = r.Rota == true || r.Rota == "1"
		for _, mp := range r.Mountpoints {
			if mp != nil && *mp != "" {
				d.Mounts = append(d.Mounts, *mp)
			}
		}
		if len(d.Mounts) > 0 {
			var st syscall.Statfs_t
			if syscall.Statfs(d.Mounts[0], &st) == nil {
				d.Avail = int64(st.Bavail) * int64(st.Bsize)
				d.Used = int64(st.Blocks-st.Bfree) * int64(st.Bsize)
			}
		}
		for _, c := range r.Children {
			d.Children = append(d.Children, conv(c))
		}
		return d
	}
	list := []BlockDev{}
	for _, m := range raw.Blockdevices {
		d := conv(m)
		if d.Type == "loop" && len(d.Mounts) == 0 {
			continue
		}
		if strings.HasPrefix(d.Name, "zram") {
			continue // compressed memory, shown with memory
		}
		list = append(list, d)
	}
	return list, nil
}
