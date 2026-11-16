// Package metrics samples the host once a second from /proc and /sys, and reads
// cgroup v2 memory for the "berth bar" (who is using the memory).
package metrics

import (
	"bufio"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Roots are variables so tests can point them at testdata.
var (
	procRoot   = "/proc"
	sysRoot    = "/sys"
	cgroupRoot = "/sys/fs/cgroup"
)

type Disk struct {
	Mount string `json:"mount"`
	FS    string `json:"fs"`
	Total int64  `json:"total"`
	Used  int64  `json:"used"`
}

type Iface struct {
	Name  string `json:"name"`
	RxBps int64  `json:"rx_bps"`
	TxBps int64  `json:"tx_bps"`
}

type Temp struct {
	Label string  `json:"label"`
	C     float64 `json:"c"`
}

type Host struct {
	TS        int64      `json:"ts"`
	CPU       float64    `json:"cpu"`
	PerCPU    []float64  `json:"per_cpu"`
	Cores     int        `json:"cores"`
	Load      [3]float64 `json:"load"`
	MemTotal  int64      `json:"mem_total"`
	MemUsed   int64      `json:"mem_used"`
	MemAvail  int64      `json:"mem_avail"`
	Buffers   int64      `json:"buffers"`
	Cached    int64      `json:"cached"`
	SwapTotal int64      `json:"swap_total"`
	SwapUsed  int64      `json:"swap_used"`
	ZramTotal int64      `json:"zram_total"` // disksize
	ZramUsed  int64      `json:"zram_used"`  // memory it takes (compressed + overhead)
	ZramOrig  int64      `json:"zram_orig"`  // data stored, uncompressed
	Disks     []Disk     `json:"disks"`
	DiskRBps  int64      `json:"disk_r_bps"`
	DiskWBps  int64      `json:"disk_w_bps"`
	Net       []Iface    `json:"net"`
	Temps     []Temp     `json:"temps"`
	Uptime    int64      `json:"uptime"`
}

type memInfo struct {
	MemTotal, MemUsed, MemAvail, Buffers, Cached, SwapTotal, SwapUsed int64
}

func readMemInfo(path string) (memInfo, error) {
	f, err := os.Open(path)
	if err != nil {
		return memInfo{}, err
	}
	defer f.Close()
	v := map[string]int64{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		k, rest, ok := strings.Cut(sc.Text(), ":")
		if !ok {
			continue
		}
		f := strings.Fields(rest)
		if len(f) == 0 {
			continue
		}
		n, _ := strconv.ParseInt(f[0], 10, 64)
		v[k] = n * 1024
	}
	m := memInfo{MemTotal: v["MemTotal"], MemAvail: v["MemAvailable"], Buffers: v["Buffers"], Cached: v["Cached"], SwapTotal: v["SwapTotal"]}
	m.MemUsed = m.MemTotal - m.MemAvail
	m.SwapUsed = v["SwapTotal"] - v["SwapFree"]
	return m, nil
}

type cpuTimes struct{ user, nice, system, idle, iowait, irq, softirq, steal uint64 }

func (c cpuTimes) total() uint64 {
	return c.user + c.nice + c.system + c.idle + c.iowait + c.irq + c.softirq + c.steal
}

func cpuPercent(a, b cpuTimes) float64 {
	dt := float64(b.total() - a.total())
	if dt <= 0 {
		return 0
	}
	idle := float64((b.idle + b.iowait) - (a.idle + a.iowait))
	return (dt - idle) / dt * 100
}

func readCPU() (all cpuTimes, per []cpuTimes) {
	f, err := os.Open(filepath.Join(procRoot, "stat"))
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		l := sc.Text()
		if !strings.HasPrefix(l, "cpu") {
			continue
		}
		fs := strings.Fields(l)
		if len(fs) < 9 {
			continue
		}
		n := make([]uint64, 8)
		for i := range n {
			n[i], _ = strconv.ParseUint(fs[i+1], 10, 64)
		}
		t := cpuTimes{n[0], n[1], n[2], n[3], n[4], n[5], n[6], n[7]}
		if fs[0] == "cpu" {
			all = t
		} else {
			per = append(per, t)
		}
	}
	return
}

var wholeDisk = regexp.MustCompile(`^(nvme\d+n\d+|sd[a-z]+|vd[a-z]+|xvd[a-z]+|mmcblk\d+)$`)

func readDiskIO() (r, w uint64) {
	f, err := os.Open(filepath.Join(procRoot, "diskstats"))
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fs := strings.Fields(sc.Text())
		if len(fs) < 10 || !wholeDisk.MatchString(fs[2]) {
			continue
		}
		rs, _ := strconv.ParseUint(fs[5], 10, 64)
		ws, _ := strconv.ParseUint(fs[9], 10, 64)
		r, w = r+rs*512, w+ws*512
	}
	return
}

// skipIface: loopback and container plumbing; the host's own links stay.
func skipIface(n string) bool {
	return n == "lo" || strings.HasPrefix(n, "veth") || strings.HasPrefix(n, "br-") || strings.HasPrefix(n, "docker")
}

func readNet() map[string][2]uint64 {
	out := map[string][2]uint64{}
	f, err := os.Open(filepath.Join(procRoot, "net", "dev"))
	if err != nil {
		return out
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		name, rest, ok := strings.Cut(sc.Text(), ":")
		if !ok {
			continue
		}
		name = strings.TrimSpace(name)
		fs := strings.Fields(rest)
		if len(fs) < 9 || skipIface(name) {
			continue
		}
		rx, _ := strconv.ParseUint(fs[0], 10, 64)
		tx, _ := strconv.ParseUint(fs[8], 10, 64)
		out[name] = [2]uint64{rx, tx}
	}
	return out
}

var realFS = map[string]bool{"ext4": true, "ext3": true, "xfs": true, "btrfs": true, "f2fs": true, "zfs": true, "vfat": true, "exfat": true, "ntfs3": true}

func readDisks() []Disk {
	f, err := os.Open(filepath.Join(procRoot, "mounts"))
	if err != nil {
		return nil
	}
	defer f.Close()
	seen := map[string]bool{}
	var out []Disk
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fs := strings.Fields(sc.Text())
		if len(fs) < 3 || !realFS[fs[2]] || seen[fs[0]] {
			continue // btrfs subvolumes share a device: count it once
		}
		mount := strings.ReplaceAll(fs[1], `\040`, " ")
		var st syscall.Statfs_t
		if syscall.Statfs(mount, &st) != nil {
			continue
		}
		bs := int64(st.Bsize)
		total := int64(st.Blocks) * bs
		if total < 256<<20 { // skip tiny boot/EFI partitions
			continue
		}
		seen[fs[0]] = true
		out = append(out, Disk{Mount: mount, FS: fs[2], Total: total, Used: total - int64(st.Bfree)*bs})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Mount < out[j].Mount })
	return out
}

func readZram() (total, used, orig int64) {
	dirs, _ := filepath.Glob(filepath.Join(sysRoot, "block", "zram*"))
	for _, d := range dirs {
		if b, err := os.ReadFile(filepath.Join(d, "disksize")); err == nil {
			n, _ := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
			total += n
		}
		if b, err := os.ReadFile(filepath.Join(d, "mm_stat")); err == nil {
			fs := strings.Fields(string(b))
			if len(fs) >= 3 {
				o, _ := strconv.ParseInt(fs[0], 10, 64)
				u, _ := strconv.ParseInt(fs[2], 10, 64)
				orig, used = orig+o, used+u
			}
		}
	}
	return
}

func readTemps() []Temp {
	var out []Temp
	hw, _ := filepath.Glob(filepath.Join(sysRoot, "class", "hwmon", "hwmon*"))
	for _, h := range hw {
		name, _ := os.ReadFile(filepath.Join(h, "name"))
		inputs, _ := filepath.Glob(filepath.Join(h, "temp*_input"))
		for _, in := range inputs {
			b, err := os.ReadFile(in)
			if err != nil {
				continue
			}
			v, err := strconv.ParseFloat(strings.TrimSpace(string(b)), 64)
			if err != nil || v <= 0 {
				continue
			}
			label := strings.TrimSpace(string(name))
			if l, err := os.ReadFile(strings.TrimSuffix(in, "_input") + "_label"); err == nil {
				label += " " + strings.TrimSpace(string(l))
			}
			out = append(out, Temp{Label: label, C: v / 1000})
		}
	}
	return out
}

func readLoad() (l [3]float64) {
	b, err := os.ReadFile(filepath.Join(procRoot, "loadavg"))
	if err != nil {
		return
	}
	fs := strings.Fields(string(b))
	for i := 0; i < 3 && i < len(fs); i++ {
		l[i], _ = strconv.ParseFloat(fs[i], 64)
	}
	return
}

func readUptime() int64 {
	b, err := os.ReadFile(filepath.Join(procRoot, "uptime"))
	if err != nil {
		return 0
	}
	f, _ := strconv.ParseFloat(strings.Fields(string(b))[0], 64)
	return int64(f)
}

// Sampler keeps the latest host snapshot, refreshed every second.
type Sampler struct {
	mu      sync.RWMutex
	last    Host
	prevCPU cpuTimes
	prevPer []cpuTimes
	prevNet map[string][2]uint64
	prevR   uint64
	prevW   uint64
	prevAt  time.Time
	disks   []Disk
	disksAt time.Time
}

func NewSampler() *Sampler {
	s := &Sampler{}
	s.sample()
	return s
}

// Run samples every interval until stop is closed.
func (s *Sampler) Run(interval time.Duration, stop <-chan struct{}) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			s.sample()
		}
	}
}

func (s *Sampler) Host() Host {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.last
}

func (s *Sampler) sample() {
	now := time.Now()
	all, per := readCPU()
	netNow := readNet()
	r, w := readDiskIO()
	m, _ := readMemInfo(filepath.Join(procRoot, "meminfo"))
	h := Host{TS: now.Unix(), Load: readLoad(), Uptime: readUptime(), Cores: len(per),
		MemTotal: m.MemTotal, MemUsed: m.MemUsed, MemAvail: m.MemAvail, Buffers: m.Buffers, Cached: m.Cached,
		SwapTotal: m.SwapTotal, SwapUsed: m.SwapUsed, Temps: readTemps()}
	h.ZramTotal, h.ZramUsed, h.ZramOrig = readZram()

	s.mu.Lock()
	defer s.mu.Unlock()
	if now.Sub(s.disksAt) > 30*time.Second { // statfs on every mount each second is wasteful
		s.disks, s.disksAt = readDisks(), now
	}
	h.Disks = s.disks
	dt := now.Sub(s.prevAt).Seconds()
	if !s.prevAt.IsZero() && dt > 0 {
		h.CPU = cpuPercent(s.prevCPU, all)
		for i := range per {
			if i < len(s.prevPer) {
				h.PerCPU = append(h.PerCPU, cpuPercent(s.prevPer[i], per[i]))
			}
		}
		h.DiskRBps = int64(float64(r-s.prevR) / dt)
		h.DiskWBps = int64(float64(w-s.prevW) / dt)
		names := make([]string, 0, len(netNow))
		for n := range netNow {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			p, ok := s.prevNet[n]
			if !ok {
				continue
			}
			c := netNow[n]
			h.Net = append(h.Net, Iface{Name: n, RxBps: int64(float64(c[0]-p[0]) / dt), TxBps: int64(float64(c[1]-p[1]) / dt)})
		}
	}
	s.prevCPU, s.prevPer, s.prevNet, s.prevR, s.prevW, s.prevAt = all, per, netNow, r, w, now
	s.last = h
}
