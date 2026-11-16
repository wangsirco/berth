package metrics

import (
	"bufio"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// cgroupMem is memory.current minus inactive file cache (what `docker stats` shows):
// reclaimable cache would otherwise make every container look bigger than it is.
func cgroupMem(dir string) (int64, bool) {
	b, err := os.ReadFile(filepath.Join(dir, "memory.current"))
	if err != nil {
		return 0, false
	}
	cur, _ := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
	return max(0, cur-statField(filepath.Join(dir, "memory.stat"), "inactive_file")), true
}

func statField(path, key string) int64 {
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), " ")
		if ok && k == key {
			n, _ := strconv.ParseInt(v, 10, 64)
			return n
		}
	}
	return 0
}

func containerMem(root, id string) int64 {
	n, _ := cgroupMem(filepath.Join(root, "system.slice", "docker-"+id+".scope"))
	return n
}

type cgroupUse struct {
	Name     string
	MemBytes int64
	CPUUsec  int64
}

// services lists systemd services under system.slice with their memory.
func services(root string) []cgroupUse {
	dirs, _ := filepath.Glob(filepath.Join(root, "system.slice", "*.service"))
	var out []cgroupUse
	for _, d := range dirs {
		if m, ok := cgroupMem(d); ok {
			out = append(out, cgroupUse{Name: filepath.Base(d), MemBytes: m, CPUUsec: statField(filepath.Join(d, "cpu.stat"), "usage_usec")})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].MemBytes > out[j].MemBytes })
	return out
}

// Berth is one segment of the berth bar.
type Berth struct {
	Type     string  `json:"type"` // container|service|user|kernel|free
	ID       string  `json:"id"`
	Name     string  `json:"name"`
	MemBytes int64   `json:"mem_bytes"`
	CPU      float64 `json:"cpu"`
	Project  string  `json:"project,omitempty"`
}

// ContainerRef names a running container for the bar.
type ContainerRef struct {
	ID, Name, Project string
}

// cpuTracker turns cumulative cgroup usage_usec into percentages between calls.
type cpuTracker struct {
	mu   sync.Mutex
	prev map[string]int64
	at   time.Time
}

func (t *cpuTracker) percent(now time.Time, cur map[string]int64) map[string]float64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := map[string]float64{}
	dt := now.Sub(t.at).Microseconds()
	if t.prev != nil && dt > 0 {
		for k, v := range cur {
			if p, ok := t.prev[k]; ok && v >= p {
				out[k] = float64(v-p) / float64(dt) * 100
			}
		}
	}
	t.prev, t.at = cur, now
	return out
}

var berthCPU cpuTracker

// Berths builds the bar: containers, the top services, user sessions, kernel/other, free.
func Berths(h Host, running []ContainerRef) []Berth {
	now := time.Now()
	var out []Berth
	usage := map[string]int64{}
	var accounted int64
	for _, c := range running {
		dir := filepath.Join(cgroupRoot, "system.slice", "docker-"+c.ID+".scope")
		m, ok := cgroupMem(dir)
		if !ok {
			continue
		}
		usage["c:"+c.ID] = statField(filepath.Join(dir, "cpu.stat"), "usage_usec")
		out = append(out, Berth{Type: "container", ID: c.ID, Name: c.Name, Project: c.Project, MemBytes: m})
		accounted += m
	}
	svcs := services(cgroupRoot)
	if len(svcs) > 20 {
		svcs = svcs[:20]
	}
	for _, s := range svcs {
		usage["s:"+s.Name] = s.CPUUsec
		out = append(out, Berth{Type: "service", ID: s.Name, Name: strings.TrimSuffix(s.Name, ".service"), MemBytes: s.MemBytes})
		accounted += s.MemBytes
	}
	if m, ok := cgroupMem(filepath.Join(cgroupRoot, "user.slice")); ok && m > 0 {
		out = append(out, Berth{Type: "user", ID: "user.slice", Name: "user", MemBytes: m})
		accounted += m
	}
	cpu := berthCPU.percent(now, usage)
	for i := range out {
		key := "s:" + out[i].ID
		if out[i].Type == "container" {
			key = "c:" + out[i].ID
		}
		out[i].CPU = cpu[key]
	}
	out = append(out, Berth{Type: "kernel", ID: "kernel", Name: "kernel", MemBytes: max(0, h.MemUsed-accounted)})
	out = append(out, Berth{Type: "free", ID: "free", Name: "free", MemBytes: h.MemAvail})
	return out
}

// withVMs adds one segment per running VM or system container, just before kernel and free.
// Their memory was counted inside incus.service (VMs: the qemu process) or as kernel/other
// (containers: lxc.payload), so it comes out of those, never below zero.
func withVMs(bs []Berth, vms []Berth) []Berth {
	if len(vms) == 0 {
		return bs
	}
	var inVM, inCT int64
	for _, v := range vms {
		if v.Project == "virtual-machine" {
			inVM += v.MemBytes
		} else {
			inCT += v.MemBytes
		}
	}
	var head, tail []Berth
	for _, b := range bs {
		switch b.Type {
		case "service":
			if b.ID == "incus.service" {
				b.MemBytes = max(0, b.MemBytes-inVM)
			}
			head = append(head, b)
		case "kernel":
			b.MemBytes = max(0, b.MemBytes-inCT)
			tail = append(tail, b)
		case "free":
			tail = append(tail, b)
		default:
			head = append(head, b)
		}
	}
	return append(append(head, vms...), tail...)
}
