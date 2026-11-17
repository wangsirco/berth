package system

// Kernel parameters, swap and zram, the system DNS resolver, and automatic security
// updates (方案 §5.14). Each writes one file Berth owns, so removing Berth's file puts the
// distribution's defaults back.

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"github.com/wangsirco/berth/internal/agent/drift"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	sysctlFile   = "/etc/sysctl.d/90-berth.conf"
	zramFile     = "/etc/systemd/zram-generator.conf"
	berthSwap    = "/var/swap/berth.swap"
	resolvedFile = "/etc/systemd/resolved.conf.d/90-berth.conf"
)

// TunableKeys are the kernel parameters the panel offers (with a description in the UI).
var TunableKeys = []string{
	"net.ipv4.icmp_echo_ignore_all", "net.ipv4.ip_forward", "net.ipv6.conf.all.forwarding", "net.ipv4.tcp_congestion_control", "net.core.default_qdisc",
	"net.core.somaxconn", "net.ipv4.tcp_tw_reuse", "net.ipv4.ip_local_port_range",
	"fs.file-max", "fs.inotify.max_user_watches", "fs.inotify.max_user_instances",
	"vm.swappiness", "vm.overcommit_memory", "vm.max_map_count", "vm.vfs_cache_pressure",
}

var sysctlValRe = regexp.MustCompile(`^[0-9a-z_]+([ \t][0-9a-z_]+)*$`)

func tunable(k string) bool {
	for _, x := range TunableKeys {
		if x == k {
			return true
		}
	}
	return false
}

func procPath(k string) string { return "/proc/sys/" + strings.ReplaceAll(k, ".", "/") }

type SysctlState struct {
	Values    map[string]string `json:"values"`    // what the kernel uses now
	File      map[string]string `json:"file"`      // what Berth's file sets at boot
	Available map[string]string `json:"available"` // choices where the kernel lists them
}

func readSysctlFile() map[string]string {
	out := map[string]string{}
	f, err := os.Open(sysctlFile)
	if err != nil {
		return out
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		l := strings.TrimSpace(sc.Text())
		if l == "" || strings.HasPrefix(l, "#") || strings.HasPrefix(l, ";") {
			continue
		}
		if k, v, ok := strings.Cut(l, "="); ok {
			out[strings.TrimSpace(k)] = strings.Join(strings.Fields(v), " ")
		}
	}
	return out
}

func GetSysctl() SysctlState {
	st := SysctlState{Values: map[string]string{}, File: readSysctlFile(), Available: map[string]string{}}
	for _, k := range TunableKeys {
		if b, err := os.ReadFile(procPath(k)); err == nil {
			st.Values[k] = strings.Join(strings.Fields(string(b)), " ")
		}
	}
	if b, err := os.ReadFile("/proc/sys/net/ipv4/tcp_available_congestion_control"); err == nil {
		st.Available["net.ipv4.tcp_congestion_control"] = strings.TrimSpace(string(b))
	}
	return st
}

// SetSysctl writes Berth's file (whole) and applies the given values now.
func SetSysctl(ctx context.Context, file, apply map[string]string) error {
	for _, m := range []map[string]string{file, apply} {
		for k, v := range m {
			if !tunable(k) {
				return errors.New("not an offered parameter: " + k)
			}
			if !sysctlValRe.MatchString(v) || len(v) > 64 {
				return fmt.Errorf("bad value for %s: %q", k, v)
			}
		}
	}
	if v := apply["net.ipv4.tcp_congestion_control"]; v == "bbr" {
		run(ctx, "modprobe", "tcp_bbr")
		os.MkdirAll("/etc/modules-load.d", 0o755)
		os.WriteFile("/etc/modules-load.d/berth-bbr.conf", []byte("tcp_bbr\n"), 0o644)
	}
	for k, v := range apply {
		if out, err := run(ctx, "sysctl", "-w", k+"="+v); err != nil {
			return fmt.Errorf("%s: %s", k, out)
		}
	}
	if len(file) == 0 {
		os.Remove(sysctlFile)
		drift.Track(sysctlFile, "sysctl", "")
		return nil
	}
	keys := make([]string, 0, len(file))
	for k := range file {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString("# Managed by Berth (panel → System → Kernel). Applied at boot.\n")
	for _, k := range keys {
		fmt.Fprintf(&b, "%s = %s\n", k, file[k])
	}
	os.MkdirAll(filepath.Dir(sysctlFile), 0o755)
	err := os.WriteFile(sysctlFile, []byte(b.String()), 0o644)
	drift.Track(sysctlFile, "sysctl", "")
	return err
}

// ---- swap and zram ----

type SwapDev struct {
	Name string `json:"name"`
	Type string `json:"type"` // partition | file
	Size int64  `json:"size"`
	Used int64  `json:"used"`
	Prio int    `json:"prio"`
}

type SwapState struct {
	Devices  []SwapDev `json:"devices"`
	ZramSize string    `json:"zram_size"` // zram-generator expression, "" = no zram
	ZramAlgo string    `json:"zram_algo,omitempty"`
	ZramOurs bool      `json:"zram_custom"` // /etc file exists (vs the distribution default)
	ZramOK   bool      `json:"zram_supported"`
	BerthMB  int       `json:"berth_swap_mb"` // size of Berth's swap file, 0 = none
	MemMB    int       `json:"mem_mb"`
}

func readZram() (size, algo string, custom bool) {
	for _, p := range []string{zramFile, "/usr/lib/systemd/zram-generator.conf", "/usr/local/lib/systemd/zram-generator.conf"} {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		custom = p == zramFile
		in := false
		for _, l := range strings.Split(string(b), "\n") {
			l = strings.TrimSpace(l)
			if strings.HasPrefix(l, "[") {
				in = l == "[zram0]"
				continue
			}
			if k, v, ok := strings.Cut(l, "="); ok && in {
				switch strings.TrimSpace(k) {
				case "zram-size", "zram-fraction":
					size = strings.TrimSpace(v)
				case "compression-algorithm":
					algo = strings.TrimSpace(v)
				}
			}
		}
		return
	}
	return
}

func GetSwap(ctx context.Context) SwapState {
	st := SwapState{Devices: []SwapDev{}}
	out, _ := run(ctx, "swapon", "--show=NAME,TYPE,SIZE,USED,PRIO", "--bytes", "--noheadings", "--raw")
	for _, l := range strings.Split(out, "\n") {
		f := strings.Fields(l)
		if len(f) < 5 {
			continue
		}
		d := SwapDev{Name: f[0], Type: f[1]}
		d.Size, _ = strconv.ParseInt(f[2], 10, 64)
		d.Used, _ = strconv.ParseInt(f[3], 10, 64)
		d.Prio, _ = strconv.Atoi(f[4])
		st.Devices = append(st.Devices, d)
		if f[0] == berthSwap {
			st.BerthMB = int(d.Size >> 20)
		}
	}
	st.ZramSize, st.ZramAlgo, st.ZramOurs = readZram()
	_, err := os.Stat("/usr/lib/systemd/system-generators/zram-generator")
	st.ZramOK = err == nil
	if b, err := os.ReadFile("/proc/meminfo"); err == nil {
		var kb int
		fmt.Sscanf(strings.SplitN(string(b), "\n", 2)[0], "MemTotal: %d kB", &kb)
		st.MemMB = kb >> 10
	}
	return st
}

var zramSizeRe = regexp.MustCompile(`^(ram|ram ?[/*] ?[0-9.]+|[0-9]+|min\(ram ?/ ?[0-9]+, ?[0-9]+\))$`)

// SetZram writes the zram size ("" = back to the distribution default, "0" = off) and
// restarts zram0. Pages in zram move to RAM or other swap first, which can take a while.
func SetZram(ctx context.Context, size string, log func(string)) error {
	size = strings.TrimSpace(size)
	if size != "" && size != "0" && !zramSizeRe.MatchString(size) {
		return errors.New("zram size must look like ram / 2, ram, or a number of MiB")
	}
	switch size {
	case "":
		os.Remove(zramFile)
	case "0":
		os.WriteFile(zramFile, []byte("# Managed by Berth: zram off\n"), 0o644)
	default:
		_, algo, _ := readZram()
		if algo == "" {
			algo = "zstd"
		}
		os.WriteFile(zramFile, []byte(fmt.Sprintf("# Managed by Berth (panel → System → Memory)\n[zram0]\nzram-size = %s\ncompression-algorithm = %s\n", size, algo)), 0o644)
	}
	drift.Track(zramFile, "zram", "")
	log("systemctl daemon-reload")
	run(ctx, "systemctl", "daemon-reload")
	log("systemctl restart systemd-zram-setup@zram0 (moving swapped pages out of zram first)")
	if size == "0" {
		run(ctx, "swapoff", "/dev/zram0")
		run(ctx, "systemctl", "stop", "systemd-zram-setup@zram0.service")
		return nil
	}
	if out, err := run(ctx, "systemctl", "restart", "systemd-zram-setup@zram0.service"); err != nil {
		return errors.New(out)
	}
	run(ctx, "systemctl", "start", "dev-zram0.swap")
	return nil
}

// SetSwapFile creates, resizes or (mb = 0) removes Berth's swap file.
func SetSwapFile(ctx context.Context, mb int, log func(string)) error {
	if mb < 0 || mb > 256<<10 {
		return errors.New("swap file size must be 0–262144 MiB")
	}
	if _, err := os.Stat(berthSwap); err == nil {
		log("swapoff " + berthSwap)
		if out, err := run(ctx, "swapoff", berthSwap); err != nil && !strings.Contains(out, "Invalid argument") {
			return errors.New("swapoff: " + out)
		}
		os.Remove(berthSwap)
	}
	fstabSwap(false)
	if mb == 0 {
		return nil
	}
	if err := makeSwap(ctx, berthSwap, mb, log); err != nil {
		return err
	}
	fstabSwap(true)
	return nil
}

// makeSwap creates and turns on a swap file at path (btrfs needs its own way, and its own
// subvolume so snapshots of /var leave it out).
func makeSwap(ctx context.Context, path string, mb int, log func(string)) error {
	dir := filepath.Dir(path)
	if _, err := os.Stat(dir); err != nil {
		fs, _ := run(ctx, "findmnt", "-no", "FSTYPE", "-T", "/var")
		if fs == "btrfs" {
			if out, err := run(ctx, "btrfs", "subvolume", "create", dir); err != nil {
				return errors.New(out)
			}
		} else {
			os.MkdirAll(dir, 0o700)
		}
	}
	fs, _ := run(ctx, "findmnt", "-no", "FSTYPE", "-T", dir)
	size := strconv.Itoa(mb) + "m"
	if fs == "btrfs" {
		log("btrfs filesystem mkswapfile --size " + size + " " + path)
		if out, err := run(ctx, "btrfs", "filesystem", "mkswapfile", "--size", size, path); err != nil {
			return errors.New(out)
		}
	} else {
		log("fallocate -l " + size + " " + path + " && mkswap")
		if out, err := run(ctx, "fallocate", "-l", strconv.Itoa(mb)+"MiB", path); err != nil {
			return errors.New(out)
		}
		os.Chmod(path, 0o600)
		if out, err := run(ctx, "mkswap", path); err != nil {
			os.Remove(path)
			return errors.New(out)
		}
	}
	run(ctx, "restorecon", path)
	log("swapon " + path)
	if out, err := run(ctx, "swapon", "-p", "5", path); err != nil {
		os.Remove(path)
		return errors.New(out)
	}
	return nil
}

// installSwap lives only while packages are installed on a small machine (never in fstab).
const installSwap = "/var/swap/berth-install.swap"

// TempSwap: on a machine with little memory and (almost) no swap, dnf and apt get killed by
// the kernel halfway (dnf alone wants a few hundred MB for the repository metadata). For the
// duration of an install, add a swap file; the returned func removes it. It does nothing on
// machines with enough memory or swap, or too little disk.
func TempSwap(ctx context.Context, log func(string)) func() {
	if _, err := os.Stat(installSwap); err == nil { // left over from an install that was cut off
		run(ctx, "swapoff", installSwap)
		os.Remove(installSwap)
	}
	st := GetSwap(ctx)
	var swap int64
	for _, d := range st.Devices {
		swap += d.Size
	}
	if st.MemMB == 0 || st.MemMB >= 1536 || swap >= 1<<30 {
		return func() {}
	}
	mb := 1024
	if st.MemMB < 768 {
		mb = 2048
	}
	var fs syscall.Statfs_t
	if err := syscall.Statfs("/var", &fs); err != nil || int64(fs.Bavail)*int64(fs.Bsize) < int64(mb+2048)<<20 {
		log(fmt.Sprintf("内存只有 %d MB 且几乎没有 swap，硬盘空间也不够加临时 swap：安装可能因内存不足被系统终止 / only %d MB of memory, almost no swap and too little disk for a temporary one: the install may be killed for lack of memory", st.MemMB, st.MemMB))
		return func() {}
	}
	log(fmt.Sprintf("内存只有 %d MB、几乎没有 swap：安装期间临时加 %d MB swap（装完自动删除），会慢一些但不会因为内存不足失败 / only %d MB of memory and almost no swap: adding %d MB of temporary swap for the install (removed afterwards)", st.MemMB, mb, st.MemMB, mb))
	if err := makeSwap(ctx, installSwap, mb, log); err != nil {
		log("临时 swap 没加上 / could not add the temporary swap: " + err.Error())
		return func() {}
	}
	return func() {
		c, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		run(c, "swapoff", installSwap)
		os.Remove(installSwap)
		log("已删除临时 swap / temporary swap removed")
	}
}

// Killed explains a package manager the kernel killed (out of memory) in words.
func Killed(err error) error {
	if err == nil || !strings.Contains(err.Error(), "signal: killed") {
		return err
	}
	st := GetSwap(context.Background())
	return fmt.Errorf("内存不足，安装进程被系统终止（这台机器共 %d MB 内存）。可以在「系统 → 内核与内存」加一个 swap 文件后重试 / out of memory: the installer was killed (%d MB of memory); add a swap file under System → Kernel & memory and try again", st.MemMB, st.MemMB)
}

// fstabSwap adds or removes Berth's swap line in /etc/fstab (other lines are left as they are).
func fstabSwap(on bool) {
	b, err := os.ReadFile("/etc/fstab")
	if err != nil {
		return
	}
	var keep []string
	for _, l := range strings.Split(strings.TrimRight(string(b), "\n"), "\n") {
		if f := strings.Fields(l); len(f) > 0 && f[0] == berthSwap {
			continue
		}
		keep = append(keep, l)
	}
	if on {
		keep = append(keep, berthSwap+" none swap defaults,pri=5 0 0")
	}
	out := strings.Join(keep, "\n") + "\n"
	if out != string(b) {
		os.WriteFile("/etc/fstab.berth-tmp", []byte(out), 0o644)
		os.Rename("/etc/fstab.berth-tmp", "/etc/fstab")
		run(context.Background(), "restorecon", "/etc/fstab")
		run(context.Background(), "systemctl", "daemon-reload")
	}
}

// ---- resolver ----

type ResolverState struct {
	Resolved bool     `json:"resolved"` // systemd-resolved runs
	Servers  []string `json:"servers"`  // Berth's global servers ([] = none set by Berth)
}

func GetResolver(ctx context.Context) ResolverState {
	st := ResolverState{Servers: []string{}}
	out, _ := run(ctx, "systemctl", "is-active", "systemd-resolved")
	st.Resolved = out == "active"
	if b, err := os.ReadFile(resolvedFile); err == nil {
		for _, l := range strings.Split(string(b), "\n") {
			if v, ok := strings.CutPrefix(strings.TrimSpace(l), "DNS="); ok {
				st.Servers = append(st.Servers, strings.Fields(v)...)
			}
		}
	}
	return st
}

func SetResolver(ctx context.Context, servers []string) error {
	for _, s := range servers {
		host := s
		if h, _, ok := strings.Cut(s, "#"); ok { // 1.1.1.1#cloudflare-dns.com (DoT name)
			host = h
		}
		if net.ParseIP(host) == nil {
			return fmt.Errorf("bad DNS server %q", s)
		}
	}
	if !GetResolver(ctx).Resolved {
		return errors.New("refuse:resolver_unmanaged")
	}
	if len(servers) == 0 {
		os.Remove(resolvedFile)
	} else {
		os.MkdirAll(filepath.Dir(resolvedFile), 0o755)
		if err := os.WriteFile(resolvedFile, []byte("# Managed by Berth (panel → System → Network)\n[Resolve]\nDNS="+strings.Join(servers, " ")+"\n"), 0o644); err != nil {
			return err
		}
	}
	drift.Track(resolvedFile, "resolver", "")
	if out, err := run(ctx, "systemctl", "restart", "systemd-resolved"); err != nil {
		return errors.New(out)
	}
	return nil
}
