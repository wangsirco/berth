// Package migrate moves what Berth manages from one server to another (R6). The agent side:
// measuring what there is and what room the target has, writing it out as a tar stream, and
// writing a stream back into place on the target without keeping a copy of it first.
package migrate

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"

	"github.com/wangsirco/berth/internal/errs"

	"github.com/wangsirco/berth/internal/agent/system"
)

// ---- what there is ----

type ScanReq struct {
	Paths   []string `json:"paths"`   // folders to size
	Compose bool     `json:"compose"` // list compose projects with their volumes and images
	VMs     bool     `json:"vms"`
	DBs     []string `json:"dbs"` // native engines whose databases to size
}

type Image struct {
	Ref   string `json:"ref"`
	Bytes int64  `json:"bytes"`
	Local bool   `json:"local"` // built here: no registry to pull it from, so it travels in the pack
}

type Volume struct {
	Name   string            `json:"name"`
	Bytes  int64             `json:"bytes"`
	Labels map[string]string `json:"labels,omitempty"`
}

type PathSize struct {
	Path  string `json:"path"`
	Bytes int64  `json:"bytes"`
}

type ComposeProject struct {
	Name       string     `json:"name"`
	Dir        string     `json:"dir"`
	Files      []string   `json:"files"` // compose files
	Bytes      int64      `json:"bytes"` // the project folder
	Volumes    []Volume   `json:"volumes"`
	Binds      []PathSize `json:"binds"` // bind-mounted folders outside the project folder
	Images     []Image    `json:"images"`
	Containers []string   `json:"containers"`
	Stop       []string   `json:"stop"` // database containers, paused while their data is read
	Running    bool       `json:"running"`
}

type VM struct {
	Name  string `json:"name"`
	Type  string `json:"type"` // container | virtual-machine
	State string `json:"state"`
	Used  int64  `json:"used"`  // what its disk holds
	Limit int64  `json:"limit"` // the disk size it was given (0 = none)
}

type ScanResult struct {
	Paths      map[string]int64            `json:"paths"`
	Compose    []ComposeProject            `json:"compose"`
	VMs        []VM                        `json:"vms"`
	DBs        map[string]map[string]int64 `json:"dbs"`
	DockerRoot string                      `json:"docker_root"`
	IncusPool  string                      `json:"incus_pool"`
}

// du: the bytes a folder takes on disk (blocks, so sparse files count what they use).
func du(ctx context.Context, p string) int64 {
	out, err := exec.CommandContext(ctx, "du", "-s", "-B1", p).Output()
	if err != nil && len(out) == 0 {
		return 0
	}
	f := strings.Fields(string(out))
	if len(f) == 0 {
		return 0
	}
	n, _ := strconv.ParseInt(f[0], 10, 64)
	return n
}

var dbImageHints = []string{"mysql", "mariadb", "postgres", "postgis", "mongo", "redis", "valkey", "clickhouse", "elasticsearch", "opensearch", "influxdb", "etcd", "timescale", "couchdb", "rabbitmq", "minio", "rustfs"}

func isDBImage(img string) bool {
	base := img
	if i := strings.LastIndex(base, "/"); i >= 0 {
		base = base[i+1:]
	}
	for _, h := range dbImageHints {
		if strings.HasPrefix(base, h) {
			return true
		}
	}
	return false
}

// skipBind: bind mounts that belong to the machine, not to the application.
func skipBind(p string) bool {
	for _, pre := range []string{"/var/run/", "/run/", "/proc", "/sys", "/dev", "/etc/", "/var/lib/berth", "/var/lib/docker", "/usr/", "/lib", "/boot", "/tmp"} {
		if p == strings.TrimSuffix(pre, "/") || strings.HasPrefix(p, pre) {
			return true
		}
	}
	return p == "/" || p == "/etc"
}

func dockerJSON(ctx context.Context, v any, args ...string) error {
	out, err := exec.CommandContext(ctx, "docker", args...).Output()
	if err != nil {
		return err
	}
	return json.Unmarshal(out, v)
}

func composeProjects(ctx context.Context) []ComposeProject {
	var ls []struct {
		Name        string
		Status      string
		ConfigFiles string
	}
	if dockerJSON(ctx, &ls, "compose", "ls", "-a", "--format", "json") != nil {
		return nil
	}
	out := []ComposeProject{}
	imgSeen := map[string]Image{}
	for _, p := range ls {
		cp := ComposeProject{Name: p.Name, Running: strings.Contains(p.Status, "running"), Volumes: []Volume{}, Binds: []PathSize{}, Images: []Image{}, Containers: []string{}, Stop: []string{}}
		if f := strings.Split(p.ConfigFiles, ",")[0]; f != "" {
			cp.Files = strings.Split(p.ConfigFiles, ",")
			cp.Dir = filepath.Dir(f)
			cp.Bytes = du(ctx, cp.Dir)
		}
		ids, _ := exec.CommandContext(ctx, "docker", "ps", "-aq", "--filter", "label=com.docker.compose.project="+p.Name).Output()
		var cs []struct {
			Name   string
			Config struct {
				Image  string
				Labels map[string]string
			}
			Mounts []struct {
				Type, Name, Source, Destination string
			}
		}
		if f := strings.Fields(string(ids)); len(f) > 0 {
			dockerJSON(ctx, &cs, append([]string{"inspect"}, f...)...)
		}
		vols := map[string]bool{}
		binds := map[string]bool{}
		for _, c := range cs {
			name := strings.TrimPrefix(c.Name, "/")
			cp.Containers = append(cp.Containers, name)
			if isDBImage(c.Config.Image) {
				cp.Stop = append(cp.Stop, name)
			}
			if _, ok := imgSeen[c.Config.Image]; !ok {
				var ii []struct {
					Size        int64
					RepoDigests []string
				}
				dockerJSON(ctx, &ii, "image", "inspect", c.Config.Image)
				im := Image{Ref: c.Config.Image}
				if len(ii) > 0 {
					im.Bytes, im.Local = ii[0].Size, len(ii[0].RepoDigests) == 0
				}
				imgSeen[c.Config.Image] = im
			}
			for _, m := range c.Mounts {
				switch m.Type {
				case "volume":
					vols[m.Name] = true
				case "bind":
					if cp.Dir != "" && (m.Source == cp.Dir || strings.HasPrefix(m.Source, cp.Dir+"/")) || skipBind(m.Source) {
						continue
					}
					binds[m.Source] = true
				}
			}
		}
		for _, c := range cs {
			if im, ok := imgSeen[c.Config.Image]; ok && !containsImg(cp.Images, im.Ref) {
				cp.Images = append(cp.Images, im)
			}
		}
		for v := range vols {
			var vi []struct {
				Mountpoint string
				Labels     map[string]string
			}
			dockerJSON(ctx, &vi, "volume", "inspect", v)
			vol := Volume{Name: v}
			if len(vi) > 0 {
				vol.Bytes, vol.Labels = du(ctx, vi[0].Mountpoint), vi[0].Labels
			}
			cp.Volumes = append(cp.Volumes, vol)
		}
		for b := range binds {
			cp.Binds = append(cp.Binds, PathSize{b, du(ctx, b)})
		}
		sort.Slice(cp.Volumes, func(i, j int) bool { return cp.Volumes[i].Name < cp.Volumes[j].Name })
		sort.Slice(cp.Binds, func(i, j int) bool { return cp.Binds[i].Path < cp.Binds[j].Path })
		out = append(out, cp)
	}
	return out
}

func containsImg(l []Image, ref string) bool {
	for _, i := range l {
		if i.Ref == ref {
			return true
		}
	}
	return false
}

func vms(ctx context.Context) []VM {
	if _, err := exec.LookPath("incus"); err != nil {
		return nil
	}
	var ls []struct {
		Name            string
		Type            string
		Status          string
		ExpandedDevices map[string]map[string]string `json:"expanded_devices"`
		State           *struct {
			Disk map[string]struct {
				Usage int64 `json:"usage"`
			} `json:"disk"`
		} `json:"state"`
	}
	out, err := exec.CommandContext(ctx, "incus", "list", "--format", "json").Output()
	if err != nil || json.Unmarshal(out, &ls) != nil {
		return nil
	}
	res := []VM{}
	for _, i := range ls {
		v := VM{Name: i.Name, Type: i.Type, State: strings.ToLower(i.Status)}
		if i.State != nil {
			v.Used = i.State.Disk["root"].Usage
		}
		if d, ok := i.ExpandedDevices["root"]; ok {
			v.Limit = parseSize(d["size"])
		}
		if v.Used == 0 { // stopped VMs report no usage: measure the volume
			v.Used = volumeUsed(ctx, i.Name, i.Type)
		}
		res = append(res, v)
	}
	return res
}

func volumeUsed(ctx context.Context, name, typ string) int64 {
	dir := "containers"
	if typ == "virtual-machine" {
		dir = "virtual-machines"
	}
	for _, pool := range []string{"default"} {
		p := filepath.Join("/var/lib/incus/storage-pools", pool, dir, name)
		if _, err := os.Stat(p); err == nil {
			return du(ctx, p)
		}
	}
	return 0
}

func parseSize(s string) int64 {
	s = strings.TrimSpace(strings.ToUpper(s))
	if s == "" {
		return 0
	}
	mult := int64(1)
	for _, u := range []struct {
		suf string
		m   int64
	}{{"TIB", 1 << 40}, {"GIB", 1 << 30}, {"MIB", 1 << 20}, {"KIB", 1 << 10}, {"TB", 1e12}, {"GB", 1e9}, {"MB", 1e6}, {"KB", 1e3}, {"B", 1}} {
		if strings.HasSuffix(s, u.suf) {
			s, mult = strings.TrimSuffix(s, u.suf), u.m
			break
		}
	}
	n, _ := strconv.ParseFloat(strings.TrimSpace(s), 64)
	return int64(n * float64(mult))
}

func dockerRoot(ctx context.Context) string {
	out, err := exec.CommandContext(ctx, "docker", "info", "--format", "{{.DockerRootDir}}").Output()
	if err != nil {
		return "/var/lib/docker"
	}
	return strings.TrimSpace(string(out))
}

func Scan(ctx context.Context, req ScanReq) ScanResult {
	res := ScanResult{Paths: map[string]int64{}, DBs: map[string]map[string]int64{}, Compose: []ComposeProject{}, VMs: []VM{},
		DockerRoot: dockerRoot(ctx), IncusPool: "/var/lib/incus/storage-pools/default"}
	for _, p := range req.Paths {
		if fi, err := os.Stat(p); err == nil && filepath.IsAbs(p) {
			if fi.IsDir() {
				res.Paths[p] = du(ctx, p)
			} else {
				res.Paths[p] = fi.Size()
			}
		}
	}
	if req.Compose {
		res.Compose = composeProjects(ctx)
	}
	if req.VMs {
		res.VMs = vms(ctx)
	}
	for _, e := range req.DBs {
		if m, err := system.DBSizes(ctx, e); err == nil {
			res.DBs[e] = m
		}
	}
	return res
}

// ---- room on the target ----

type FS struct {
	Mount string `json:"mount"`
	Free  int64  `json:"free"`
	Total int64  `json:"total"`
}

type SpaceResult struct {
	Filesystems map[string]FS     `json:"filesystems"` // by device id
	Paths       map[string]string `json:"paths"`       // asked path → device id
}

// existing: the path itself or its nearest parent that exists.
func existing(p string) string {
	for p != "/" && p != "." {
		if _, err := os.Stat(p); err == nil {
			return p
		}
		p = filepath.Dir(p)
	}
	return "/"
}

func mounts() []string {
	f, err := os.Open("/proc/self/mountinfo")
	if err != nil {
		return []string{"/"}
	}
	defer f.Close()
	out := []string{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if fs := strings.Fields(sc.Text()); len(fs) > 4 {
			out = append(out, strings.ReplaceAll(fs[4], `\040`, " "))
		}
	}
	return out
}

func mountOf(p string, ms []string) string {
	best := "/"
	for _, m := range ms {
		if (p == m || strings.HasPrefix(p, strings.TrimSuffix(m, "/")+"/")) && len(m) > len(best) {
			best = m
		}
	}
	return best
}

// Space tells, for each path, which filesystem it would land on and how much room that has.
func Space(paths []string) SpaceResult {
	res := SpaceResult{Filesystems: map[string]FS{}, Paths: map[string]string{}}
	ms := mounts()
	for _, p := range paths {
		e := existing(filepath.Clean(p))
		var st syscall.Stat_t
		var sf syscall.Statfs_t
		if syscall.Stat(e, &st) != nil || syscall.Statfs(e, &sf) != nil {
			continue
		}
		id := strconv.FormatUint(uint64(st.Dev), 10)
		res.Paths[p] = id
		if _, ok := res.Filesystems[id]; !ok {
			res.Filesystems[id] = FS{Mount: mountOf(e, ms), Free: int64(sf.Bavail) * int64(sf.Bsize), Total: int64(sf.Blocks) * int64(sf.Bsize)}
		}
	}
	return res
}

func freeAt(p string) int64 {
	var sf syscall.Statfs_t
	if syscall.Statfs(existing(p), &sf) != nil {
		return -1
	}
	return int64(sf.Bavail) * int64(sf.Bsize)
}

var projRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)

// ComposeUp pulls what is missing and starts a project from its folder.
func ComposeUp(ctx context.Context, name, dir string, files []string, log func(string)) error {
	if !projRe.MatchString(name) || !filepath.IsAbs(dir) {
		return errs.New(400, "compose.bad_name")
	}
	args := []string{"compose", "-p", name, "--project-directory", dir}
	for _, f := range files {
		if filepath.Dir(f) != dir && !strings.HasPrefix(f, dir+"/") {
			return errs.New(400, "migrate.bad_path", "path", f)
		}
		args = append(args, "-f", f)
	}
	cmd := exec.CommandContext(ctx, "docker", append(args, "up", "-d", "--quiet-pull")...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if l != "" {
			log(l)
		}
	}
	if err != nil {
		return errs.New(500, "compose.failed", "detail", lastLine(string(out)))
	}
	return nil
}

func lastLine(s string) string {
	ls := strings.Split(strings.TrimSpace(s), "\n")
	return ls[len(ls)-1]
}
