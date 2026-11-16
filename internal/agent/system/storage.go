package system

// Disks, mounts, RAID, space analysis and clean-up (方案 §5.14 硬盘). Destructive steps
// refuse anything the running system sits on: a disk holding /, /boot, /var, /home or
// active swap, mounted file systems, and RAID members.

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/wangsirco/berth/internal/sysexec"
)

type Smart struct {
	OK          *bool  `json:"ok,omitempty"` // overall assessment; nil = unknown
	TempC       int    `json:"temp_c,omitempty"`
	PowerOnH    int    `json:"power_on_hours,omitempty"`
	UsedPct     int    `json:"used_pct,omitempty"` // NVMe wear
	Reallocated int    `json:"reallocated,omitempty"`
	MediaErrors int    `json:"media_errors,omitempty"`
	Error       string `json:"error,omitempty"`
}

type Disk struct {
	Name     string     `json:"name"`
	Path     string     `json:"path"`
	Size     int64      `json:"size"`
	Model    string     `json:"model,omitempty"`
	Serial   string     `json:"serial,omitempty"`
	Tran     string     `json:"tran,omitempty"`
	Rota     bool       `json:"rota"`
	Type     string     `json:"type"`
	System   bool       `json:"system"` // the running system lives on it
	Smart    *Smart     `json:"smart,omitempty"`
	Parts    []Part     `json:"parts"`
	FSType   string     `json:"fstype,omitempty"` // whole-disk file system
	UUID     string     `json:"uuid,omitempty"`
	Label    string     `json:"label,omitempty"`
	Mounts   []string   `json:"mounts,omitempty"`
	Holders  []string   `json:"holders,omitempty"` // md arrays using it
	Used     int64      `json:"used,omitempty"`
	Avail    int64      `json:"avail,omitempty"`
	Children []BlockDev `json:"-"`
}

type Part struct {
	Name    string   `json:"name"`
	Path    string   `json:"path"`
	Size    int64    `json:"size"`
	FSType  string   `json:"fstype,omitempty"`
	UUID    string   `json:"uuid,omitempty"`
	Label   string   `json:"label,omitempty"`
	Mounts  []string `json:"mounts,omitempty"`
	Used    int64    `json:"used,omitempty"`
	Avail   int64    `json:"avail,omitempty"`
	System  bool     `json:"system"`
	Holders []string `json:"holders,omitempty"`
	InFstab bool     `json:"in_fstab"`
	ByBerth bool     `json:"by_berth"`
}

type MDArray struct {
	Name     string   `json:"name"` // md127
	Path     string   `json:"path"` // /dev/md/data or /dev/md127
	Level    string   `json:"level"`
	State    string   `json:"state"` // clean, degraded, resyncing…
	Size     int64    `json:"size"`
	Devices  []string `json:"devices"`
	Failed   []string `json:"failed"`
	Sync     string   `json:"sync,omitempty"` // "recovery 12.5%"
	Degraded bool     `json:"degraded"`
	FSType   string   `json:"fstype,omitempty"`
	Mounts   []string `json:"mounts,omitempty"`
}

type BtrfsFS struct {
	UUID    string   `json:"uuid"`
	Label   string   `json:"label,omitempty"`
	Devices []string `json:"devices"`
	Data    string   `json:"data"` // single | raid1 | …
	Meta    string   `json:"meta"`
	Mounts  []string `json:"mounts,omitempty"`
	Errors  int      `json:"errors"` // device stat error counters, summed
	Missing bool     `json:"missing"`
	System  bool     `json:"system"`
}

type Storage struct {
	Disks  []Disk    `json:"disks"`
	Arrays []MDArray `json:"arrays"`
	Btrfs  []BtrfsFS `json:"btrfs"`
	Tools  []string  `json:"tools"` // mdadm, btrfs, smartctl, mkfs.* that are installed
}

// systemMounts are the mount points the running system needs.
var systemMounts = map[string]bool{"/": true, "/boot": true, "/boot/efi": true, "/var": true, "/home": true, "/usr": true, "/etc": true, "/srv": true, "/opt": true, "/var/lib": true, "/var/lib/docker": true}

type lsblkDev struct {
	Name, Path, Type, Model, Serial, Tran, Fstype, Label, UUID string
	Size                                                       any
	Rota                                                       any
	Mountpoints                                                []*string
	Children                                                   []lsblkDev
}

func (d lsblkDev) size() int64 {
	switch v := d.Size.(type) {
	case float64:
		return int64(v)
	case string:
		n, _ := strconv.ParseInt(v, 10, 64)
		return n
	}
	return 0
}

func (d lsblkDev) mounts() []string {
	var out []string
	for _, m := range d.Mountpoints {
		if m != nil && *m != "" {
			out = append(out, *m)
		}
	}
	return out
}

func lsblkAll(ctx context.Context) ([]lsblkDev, error) {
	out, err := run(ctx, "lsblk", "-J", "-b", "-o", "NAME,PATH,TYPE,SIZE,MODEL,SERIAL,TRAN,ROTA,FSTYPE,LABEL,UUID,MOUNTPOINTS")
	if err != nil {
		return nil, errors.New(out)
	}
	var raw struct {
		Blockdevices []lsblkDev `json:"blockdevices"`
	}
	return raw.Blockdevices, json.Unmarshal([]byte(out), &raw)
}

func activeSwaps() map[string]bool {
	out := map[string]bool{}
	b, _ := os.ReadFile("/proc/swaps")
	for i, l := range strings.Split(string(b), "\n") {
		if f := strings.Fields(l); i > 0 && len(f) > 0 {
			out[f[0]] = true
		}
	}
	return out
}

// isSystem: the device (or anything stacked on it) is mounted where the system needs it,
// or is active swap.
func isSystem(d lsblkDev, swaps map[string]bool) bool {
	if swaps[d.Path] {
		return true
	}
	for _, m := range d.mounts() {
		if systemMounts[m] || m == "[SWAP]" {
			return true
		}
	}
	for _, c := range d.Children {
		if isSystem(c, swaps) {
			return true
		}
	}
	return false
}

func statfs(path string) (used, avail int64) {
	var st syscall.Statfs_t
	if syscall.Statfs(path, &st) == nil {
		return int64(st.Blocks-st.Bfree) * int64(st.Bsize), int64(st.Bavail) * int64(st.Bsize)
	}
	return 0, 0
}

// fstabEntries: device spec (UUID=…, /dev/…) → mount point, and the ones Berth wrote.
func fstabEntries() (map[string]string, map[string]bool) {
	spec, ours := map[string]string{}, map[string]bool{}
	b, _ := os.ReadFile("/etc/fstab")
	for _, l := range strings.Split(string(b), "\n") {
		f := strings.Fields(l)
		if len(f) < 2 || strings.HasPrefix(f[0], "#") {
			continue
		}
		spec[f[0]] = f[1]
		if strings.Contains(l, "# berth") {
			ours[f[0]] = true
		}
	}
	return spec, ours
}

var smartCache sync.Map // path → {at time.Time, s *Smart}

type smartEntry struct {
	at time.Time
	s  *Smart
}

func smartOf(ctx context.Context, path string) *Smart {
	if v, ok := smartCache.Load(path); ok && time.Since(v.(smartEntry).at) < 10*time.Minute {
		return v.(smartEntry).s
	}
	s := &Smart{}
	out, _ := run(ctx, "smartctl", "-j", "-a", path)
	var r struct {
		SmartStatus *struct {
			Passed bool `json:"passed"`
		} `json:"smart_status"`
		Temperature struct {
			Current int `json:"current"`
		} `json:"temperature"`
		PowerOnTime struct {
			Hours int `json:"hours"`
		} `json:"power_on_time"`
		NVMe *struct {
			PercentageUsed int `json:"percentage_used"`
			MediaErrors    int `json:"media_errors"`
		} `json:"nvme_smart_health_information_log"`
		ATA *struct {
			Table []struct {
				ID  int `json:"id"`
				Raw struct {
					Value int `json:"value"`
				} `json:"raw"`
			} `json:"table"`
		} `json:"ata_smart_attributes"`
		Smartctl struct {
			Messages []struct {
				String string `json:"string"`
			} `json:"messages"`
		} `json:"smartctl"`
	}
	if json.Unmarshal([]byte(out), &r) != nil {
		s.Error = "smartctl gave no answer"
	} else {
		if r.SmartStatus != nil {
			ok := r.SmartStatus.Passed
			s.OK = &ok
		} else if len(r.Smartctl.Messages) > 0 {
			s.Error = r.Smartctl.Messages[0].String
		}
		s.TempC, s.PowerOnH = r.Temperature.Current, r.PowerOnTime.Hours
		if r.NVMe != nil {
			s.UsedPct, s.MediaErrors = r.NVMe.PercentageUsed, r.NVMe.MediaErrors
		}
		if r.ATA != nil {
			for _, a := range r.ATA.Table {
				if a.ID == 5 {
					s.Reallocated = a.Raw.Value
				}
			}
		}
	}
	smartCache.Store(path, smartEntry{time.Now(), s})
	return s
}

func holders(name string) []string {
	ents, _ := os.ReadDir("/sys/class/block/" + name + "/holders")
	var out []string
	for _, e := range ents {
		out = append(out, e.Name())
	}
	return out
}

func GetStorage(ctx context.Context) (Storage, error) {
	st := Storage{Disks: []Disk{}, Arrays: []MDArray{}, Btrfs: []BtrfsFS{}, Tools: []string{}}
	for _, t := range []string{"mdadm", "btrfs", "smartctl", "mkfs.ext4", "mkfs.xfs", "mkfs.btrfs", "mkfs.vfat", "mkfs.exfat"} {
		if _, err := exec.LookPath(t); err == nil {
			st.Tools = append(st.Tools, t)
		}
	}
	devs, err := lsblkAll(ctx)
	if err != nil {
		return st, err
	}
	swaps := activeSwaps()
	fst, ours := fstabEntries()
	inFstab := func(p lsblkDev) (bool, bool) {
		for _, k := range []string{"UUID=" + p.UUID, p.Path} {
			if _, ok := fst[k]; ok && (k != "UUID=" || p.UUID != "") {
				return true, ours[k]
			}
		}
		return false, false
	}
	smartOK := contains(st.Tools, "smartctl")
	mdFS := map[string]lsblkDev{}
	var walkMD func(d lsblkDev)
	walkMD = func(d lsblkDev) {
		if strings.HasPrefix(d.Type, "raid") {
			mdFS[d.Name] = d
		}
		for _, c := range d.Children {
			walkMD(c)
		}
	}
	for _, d := range devs {
		walkMD(d)
		if strings.HasPrefix(d.Name, "zram") || d.Type == "rom" || d.Type == "loop" && d.size() == 0 {
			continue
		}
		if d.Type != "disk" && d.Type != "loop" {
			continue
		}
		disk := Disk{Name: d.Name, Path: d.Path, Size: d.size(), Model: strings.TrimSpace(d.Model), Serial: d.Serial, Tran: d.Tran, Type: d.Type,
			Rota: d.Rota == true || d.Rota == "1", System: isSystem(d, swaps), Parts: []Part{}, FSType: d.Fstype, UUID: d.UUID, Label: d.Label,
			Mounts: d.mounts(), Holders: holders(d.Name)}
		if len(disk.Mounts) > 0 {
			disk.Used, disk.Avail = statfs(disk.Mounts[0])
		}
		if smartOK && d.Type == "disk" {
			disk.Smart = smartOf(ctx, d.Path)
		}
		for _, c := range d.Children {
			if c.Type != "part" {
				continue
			}
			p := Part{Name: c.Name, Path: c.Path, Size: c.size(), FSType: c.Fstype, UUID: c.UUID, Label: c.Label, Mounts: c.mounts(),
				System: isSystem(c, swaps), Holders: holders(c.Name)}
			p.InFstab, p.ByBerth = inFstab(c)
			if len(p.Mounts) > 0 && p.Mounts[0] != "[SWAP]" {
				p.Used, p.Avail = statfs(p.Mounts[0])
			}
			disk.Parts = append(disk.Parts, p)
		}
		st.Disks = append(st.Disks, disk)
	}
	st.Arrays = mdArrays(ctx, mdFS)
	st.Btrfs = btrfsList(ctx, devs, swaps)
	return st, nil
}

func mdArrays(ctx context.Context, fs map[string]lsblkDev) []MDArray {
	out := []MDArray{}
	b, err := os.ReadFile("/proc/mdstat")
	if err != nil {
		return out
	}
	lines := strings.Split(string(b), "\n")
	for i, l := range lines {
		f := strings.Fields(l)
		if len(f) < 4 || !strings.HasPrefix(f[0], "md") || f[1] != ":" {
			continue
		}
		a := MDArray{Name: f[0], Path: "/dev/" + f[0], State: f[2], Devices: []string{}, Failed: []string{}}
		rest := f[3:]
		if len(rest) > 0 && strings.HasPrefix(rest[0], "raid") || len(rest) > 0 && rest[0] == "linear" {
			a.Level, rest = rest[0], rest[1:]
		}
		for _, d := range rest {
			name := d[:strings.Index(d+"[", "[")]
			if strings.HasSuffix(d, "(F)") {
				a.Failed = append(a.Failed, "/dev/"+name)
			} else {
				a.Devices = append(a.Devices, "/dev/"+name)
			}
		}
		if i+1 < len(lines) {
			a.Degraded = strings.Contains(lines[i+1], "_")
		}
		if i+2 < len(lines) {
			if m := regexp.MustCompile(`(recovery|resync|check|reshape)\s*=\s*([\d.]+%)`).FindStringSubmatch(lines[i+2]); m != nil {
				a.Sync = m[1] + " " + m[2]
			}
		}
		if s, err := os.ReadFile("/sys/block/" + a.Name + "/size"); err == nil {
			n, _ := strconv.ParseInt(strings.TrimSpace(string(s)), 10, 64)
			a.Size = n * 512
		}
		if links, _ := filepath.Glob("/dev/md/*"); len(links) > 0 {
			for _, l := range links {
				if t, err := filepath.EvalSymlinks(l); err == nil && t == a.Path {
					a.Path = l
				}
			}
		}
		if d, ok := fs[a.Name]; ok {
			a.FSType, a.Mounts = d.Fstype, d.mounts()
		}
		if a.Degraded {
			a.State = "degraded"
		}
		out = append(out, a)
	}
	return out
}

func btrfsList(ctx context.Context, devs []lsblkDev, swaps map[string]bool) []BtrfsFS {
	out := []BtrfsFS{}
	if _, err := exec.LookPath("btrfs"); err != nil {
		return out
	}
	byUUID := map[string]*BtrfsFS{}
	var order []string
	var walk func(d lsblkDev)
	walk = func(d lsblkDev) {
		if d.Fstype == "btrfs" && d.UUID != "" {
			b := byUUID[d.UUID]
			if b == nil {
				b = &BtrfsFS{UUID: d.UUID, Label: d.Label, Devices: []string{}}
				byUUID[d.UUID] = b
				order = append(order, d.UUID)
			}
			b.Devices = append(b.Devices, d.Path)
			for _, m := range d.mounts() {
				if !contains(b.Mounts, m) {
					b.Mounts = append(b.Mounts, m)
				}
			}
			b.System = b.System || isSystem(d, swaps)
		}
		for _, c := range d.Children {
			walk(c)
		}
	}
	for _, d := range devs {
		walk(d)
	}
	for _, u := range order {
		b := byUUID[u]
		sort.Strings(b.Mounts)
		if len(b.Mounts) > 0 {
			mp := b.Mounts[0]
			if o, err := run(ctx, "btrfs", "filesystem", "df", mp); err == nil {
				for _, l := range strings.Split(o, "\n") {
					if v, ok := strings.CutPrefix(l, "Data, "); ok {
						b.Data = strings.ToLower(strings.SplitN(v, ":", 2)[0])
					} else if v, ok := strings.CutPrefix(l, "Metadata, "); ok {
						b.Meta = strings.ToLower(strings.SplitN(v, ":", 2)[0])
					}
				}
			}
			if o, err := run(ctx, "btrfs", "device", "stats", mp); err == nil {
				for _, l := range strings.Split(o, "\n") {
					if f := strings.Fields(l); len(f) == 2 {
						n, _ := strconv.Atoi(f[1])
						b.Errors += n
					}
				}
			}
			if o, _ := run(ctx, "btrfs", "filesystem", "show", mp); strings.Contains(o, "missing") {
				b.Missing = true
			}
		}
		out = append(out, *b)
	}
	return out
}

// ---- checks before destructive steps ----

var devPathRe = regexp.MustCompile(`^/dev/[a-zA-Z0-9/_.-]+$`)

// findDev returns the lsblk entry for a path (searching the whole tree).
func findDev(ctx context.Context, path string) (lsblkDev, bool) {
	devs, err := lsblkAll(ctx)
	if err != nil {
		return lsblkDev{}, false
	}
	var hit lsblkDev
	found := false
	var walk func(d lsblkDev)
	walk = func(d lsblkDev) {
		if d.Path == path {
			hit, found = d, true
		}
		for _, c := range d.Children {
			walk(c)
		}
	}
	for _, d := range devs {
		walk(d)
	}
	return hit, found
}

// mustBeFree refuses a device the system uses, that is mounted, or that belongs to an array.
func mustBeFree(ctx context.Context, path string) (lsblkDev, error) {
	if !devPathRe.MatchString(path) {
		return lsblkDev{}, errors.New("bad device path")
	}
	if t, err := filepath.EvalSymlinks(path); err == nil {
		path = t
	}
	d, ok := findDev(ctx, path)
	if !ok {
		return d, errors.New("no such device: " + path)
	}
	if isSystem(d, activeSwaps()) {
		return d, errors.New("refuse:disk_system")
	}
	var busy func(d lsblkDev) bool
	busy = func(d lsblkDev) bool {
		if len(d.mounts()) > 0 {
			return true
		}
		for _, c := range d.Children {
			if busy(c) {
				return true
			}
		}
		return false
	}
	if busy(d) {
		return d, errors.New("refuse:disk_mounted")
	}
	if h := holders(d.Name); len(h) > 0 {
		return d, errors.New("refuse:disk_in_array")
	}
	for _, c := range d.Children {
		if len(holders(c.Name)) > 0 {
			return d, errors.New("refuse:disk_in_array")
		}
	}
	return d, nil
}

func runLog(ctx context.Context, log func(string), name string, args ...string) error {
	log(name + " " + strings.Join(args, " "))
	n, a := sysexec.Scoped(name, args...)
	out, err := exec.CommandContext(ctx, n, a...).CombinedOutput()
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if l != "" {
			log(l)
		}
	}
	if err != nil {
		return fmt.Errorf("%s: %s", name, strings.TrimSpace(string(out)))
	}
	return nil
}

var fsTypes = map[string][]string{"ext4": {"mkfs.ext4", "-F"}, "xfs": {"mkfs.xfs", "-f"}, "btrfs": {"mkfs.btrfs", "-f"}, "vfat": {"mkfs.vfat"}, "exfat": {"mkfs.exfat"}}
var labelRe = regexp.MustCompile(`^[A-Za-z0-9_.-]{0,16}$`)

// Format makes a new file system on a free device (whole disk, partition or array).
func Format(ctx context.Context, path, fstype, label string, log func(string)) error {
	cmd, ok := fsTypes[fstype]
	if !ok {
		return errors.New("file system must be ext4, xfs, btrfs, vfat or exfat")
	}
	if !labelRe.MatchString(label) {
		return errors.New("label: up to 16 letters, digits, - _ .")
	}
	if _, err := mustBeFree(ctx, path); err != nil {
		return err
	}
	args := append([]string{}, cmd[1:]...)
	if label != "" {
		flag := "-L"
		if fstype == "vfat" {
			flag = "-n"
		}
		args = append(args, flag, label)
	}
	run(ctx, "wipefs", "-a", path)
	return runLog(ctx, log, cmd[0], append(args, path)...)
}

// ---- mounts ----

var mountPathRe = regexp.MustCompile(`^/[A-Za-z0-9/_.-]+$`)
var mountOptRe = regexp.MustCompile(`^[A-Za-z0-9_=,.:/-]*$`)

func mountTargetOK(p string) error {
	p = filepath.Clean(p)
	if !mountPathRe.MatchString(p) || strings.Contains(p, "..") {
		return errors.New("mount point must be an absolute path")
	}
	if systemMounts[p] {
		return errors.New("refuse:mount_system")
	}
	for _, bad := range []string{"/proc", "/sys", "/dev", "/run", "/boot", "/etc", "/usr", "/bin", "/sbin", "/lib"} {
		if p == bad || strings.HasPrefix(p, bad+"/") {
			return errors.New("refuse:mount_system")
		}
	}
	return nil
}

// Mount mounts a device at path and (persist) records it in /etc/fstab by UUID, checking
// the new fstab with findmnt --verify first and keeping a copy of the old one.
func Mount(ctx context.Context, dev, path, options string, persist bool) error {
	if err := mountTargetOK(path); err != nil {
		return err
	}
	if !mountOptRe.MatchString(options) {
		return errors.New("bad mount options")
	}
	if t, err := filepath.EvalSymlinks(dev); err == nil {
		dev = t
	}
	d, ok := findDev(ctx, dev)
	if !ok || d.Fstype == "" {
		return errors.New("refuse:disk_no_fs")
	}
	if isSystem(d, activeSwaps()) {
		return errors.New("refuse:disk_system")
	}
	if ents, err := os.ReadDir(path); err == nil && len(ents) > 0 {
		return errors.New("refuse:mount_not_empty")
	}
	os.MkdirAll(path, 0o755)
	if options == "" {
		options = "defaults"
	}
	args := []string{"-o", options, dev, path}
	if out, err := run(ctx, "mount", args...); err != nil {
		return errors.New("mount: " + out)
	}
	if !persist {
		return nil
	}
	spec := dev
	if d.UUID != "" {
		spec = "UUID=" + d.UUID
	}
	opts := options
	if !strings.Contains(opts, "nofail") {
		opts += ",nofail" // a missing disk must not stop the boot
	}
	line := fmt.Sprintf("%s %s %s %s 0 2 # berth", spec, filepath.Clean(path), d.Fstype, opts)
	if err := editFstab(ctx, func(lines []string) []string {
		var keep []string
		for _, l := range lines {
			if f := strings.Fields(l); len(f) >= 2 && (f[0] == spec || f[1] == filepath.Clean(path)) && !strings.HasPrefix(f[0], "#") {
				continue
			}
			keep = append(keep, l)
		}
		return append(keep, line)
	}); err != nil {
		run(ctx, "umount", path)
		return err
	}
	return nil
}

// Unmount unmounts path and (persist) drops its fstab line.
func Unmount(ctx context.Context, path string, persist bool) error {
	path = filepath.Clean(path)
	if err := mountTargetOK(path); err != nil {
		return err
	}
	if out, err := run(ctx, "umount", path); err != nil && !strings.Contains(out, "not mounted") {
		if strings.Contains(out, "busy") {
			return errors.New("refuse:mount_busy")
		}
		return errors.New("umount: " + out)
	}
	if !persist {
		return nil
	}
	return editFstab(ctx, func(lines []string) []string {
		var keep []string
		for _, l := range lines {
			if f := strings.Fields(l); len(f) >= 2 && f[1] == path && !strings.HasPrefix(f[0], "#") {
				continue
			}
			keep = append(keep, l)
		}
		return keep
	})
}

func editFstab(ctx context.Context, edit func([]string) []string) error {
	b, err := os.ReadFile("/etc/fstab")
	if err != nil {
		return err
	}
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	out := strings.Join(edit(lines), "\n") + "\n"
	if out == string(b) {
		return nil
	}
	tmp := "/etc/fstab.berth-new"
	if err := os.WriteFile(tmp, []byte(out), 0o644); err != nil {
		return err
	}
	if o, err := run(ctx, "findmnt", "--verify", "--tab-file", tmp); err != nil && strings.Contains(strings.ToLower(o), "error") {
		os.Remove(tmp)
		return errors.New("the new fstab did not pass findmnt --verify: " + o)
	}
	os.MkdirAll("/var/lib/berth", 0o755)
	os.WriteFile("/var/lib/berth/fstab.bak", b, 0o644)
	if err := os.Rename(tmp, "/etc/fstab"); err != nil {
		return err
	}
	run(ctx, "restorecon", "/etc/fstab")
	run(ctx, "systemctl", "daemon-reload")
	return nil
}

// ---- RAID ----

var arrayNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,30}$`)

func saveMDConf(ctx context.Context) {
	conf := "/etc/mdadm.conf"
	if _, err := os.Stat("/etc/mdadm"); err == nil {
		conf = "/etc/mdadm/mdadm.conf"
	}
	scan, _ := run(ctx, "mdadm", "--detail", "--scan")
	if strings.TrimSpace(scan) == "" {
		if b, err := os.ReadFile(conf); err == nil && strings.HasPrefix(string(b), "# Managed by Berth") {
			os.Remove(conf) // nothing left, and the file was ours
		}
		return
	}
	os.WriteFile(conf, []byte("# Managed by Berth (panel → System → Disks)\n"+scan+"\n"), 0o644)
}

// CreateRAID builds an md array or a multi-device btrfs file system.
func CreateRAID(ctx context.Context, kind, name, level string, devs []string, log func(string)) (string, error) {
	if !arrayNameRe.MatchString(name) {
		return "", errors.New("name: lowercase letters, digits, - and _")
	}
	if len(devs) < 2 {
		return "", errors.New("RAID needs at least two devices")
	}
	for _, d := range devs {
		if _, err := mustBeFree(ctx, d); err != nil {
			return "", err
		}
	}
	switch kind {
	case "md":
		min := map[string]int{"raid0": 2, "raid1": 2, "raid5": 3, "raid6": 4, "raid10": 4}[level]
		if min == 0 {
			return "", errors.New("level must be raid0, raid1, raid5, raid6 or raid10")
		}
		if len(devs) < min {
			return "", fmt.Errorf("%s needs at least %d devices", level, min)
		}
		for _, d := range devs {
			run(ctx, "wipefs", "-a", d)
		}
		path := "/dev/md/" + name
		args := []string{"--create", path, "--run", "--level=" + strings.TrimPrefix(level, "raid"), "--raid-devices=" + strconv.Itoa(len(devs)), "--metadata=1.2", "--homehost=any", "--name=" + name}
		if err := runLog(ctx, log, "mdadm", append(args, devs...)...); err != nil {
			return "", err
		}
		saveMDConf(ctx)
		return path, nil
	case "btrfs":
		if !map[string]bool{"raid0": true, "raid1": true, "raid10": true, "raid1c3": true, "single": true}[level] {
			return "", errors.New("btrfs level must be raid0, raid1, raid10, raid1c3 or single")
		}
		meta := level
		if level == "raid0" || level == "single" {
			meta = "raid1"
		}
		if err := runLog(ctx, log, "mkfs.btrfs", append([]string{"-f", "-L", name, "-d", level, "-m", meta}, devs...)...); err != nil {
			return "", err
		}
		return devs[0], nil
	}
	return "", errors.New("kind must be md or btrfs")
}

// RepairRAID adds a device to (or removes a failed one from) an md array.
func RepairRAID(ctx context.Context, array, op, dev string, log func(string)) error {
	if !devPathRe.MatchString(array) || !devPathRe.MatchString(dev) {
		return errors.New("bad device path")
	}
	switch op {
	case "add":
		if _, err := mustBeFree(ctx, dev); err != nil {
			return err
		}
		run(ctx, "wipefs", "-a", dev)
		if err := runLog(ctx, log, "mdadm", "--manage", array, "--add", dev); err != nil {
			return err
		}
	case "remove":
		run(ctx, "mdadm", "--manage", array, "--fail", dev)
		if err := runLog(ctx, log, "mdadm", "--manage", array, "--remove", dev); err != nil {
			return err
		}
	default:
		return errors.New("op must be add or remove")
	}
	saveMDConf(ctx)
	return nil
}

// Scrub starts a consistency check: md "check", or btrfs scrub on a mount point.
func Scrub(ctx context.Context, target string, log func(string)) error {
	if strings.HasPrefix(target, "/dev/") {
		t, err := filepath.EvalSymlinks(target)
		if err != nil {
			return errors.New("no such array")
		}
		name := filepath.Base(t)
		if b, err := os.ReadFile("/sys/block/" + name + "/md/sync_action"); err == nil {
			if cur := strings.TrimSpace(string(b)); cur != "idle" {
				log("the array is busy (" + cur + "); its check is part of that")
				return nil
			}
		}
		log("echo check > /sys/block/" + name + "/md/sync_action")
		return os.WriteFile("/sys/block/"+name+"/md/sync_action", []byte("check\n"), 0o644)
	}
	if !mountPathRe.MatchString(target) {
		return errors.New("bad mount point")
	}
	return runLog(ctx, log, "btrfs", "scrub", "start", target)
}

// DeleteRAID stops an md array and wipes its members' RAID metadata.
func DeleteRAID(ctx context.Context, array string, log func(string)) error {
	if !devPathRe.MatchString(array) {
		return errors.New("bad device path")
	}
	t, err := filepath.EvalSymlinks(array)
	if err != nil {
		return errors.New("no such array")
	}
	d, ok := findDev(ctx, t)
	if ok && len(d.mounts()) > 0 {
		return errors.New("refuse:disk_mounted")
	}
	var members []string
	for _, a := range mdArrays(ctx, nil) {
		if "/dev/"+a.Name == t {
			members = append(append(members, a.Devices...), a.Failed...)
		}
	}
	if err := runLog(ctx, log, "mdadm", "--stop", t); err != nil {
		return err
	}
	for _, m := range members {
		runLog(ctx, log, "mdadm", "--zero-superblock", m)
	}
	saveMDConf(ctx)
	return nil
}

// ---- space ----

type DirUse struct {
	Path  string `json:"path"`
	Bytes int64  `json:"bytes"`
	Dir   bool   `json:"dir"`
}

type SpaceReport struct {
	Path    string   `json:"path"`
	Total   int64    `json:"total"`
	Items   []DirUse `json:"items"`
	Big     []DirUse `json:"big"` // largest files below
	At      int64    `json:"at"`
	Partial bool     `json:"partial,omitempty"` // timed out before finishing
}

var spaceCache sync.Map // path → SpaceReport

// Space sums what each entry of a directory uses (on the same file system) and lists the
// largest files below it. Results are cached for ten minutes; refresh forces a new scan.
func Space(ctx context.Context, path string, refresh bool) (SpaceReport, error) {
	path = filepath.Clean(path)
	if !filepath.IsAbs(path) || strings.HasPrefix(path, "/proc") || strings.HasPrefix(path, "/sys") || strings.HasPrefix(path, "/dev") {
		return SpaceReport{}, errors.New("bad path")
	}
	if v, ok := spaceCache.Load(path); ok && !refresh && time.Since(time.Unix(v.(SpaceReport).At, 0)) < 10*time.Minute {
		return v.(SpaceReport), nil
	}
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	rep := SpaceReport{Path: path, Items: []DirUse{}, Big: []DirUse{}, At: time.Now().Unix()}
	// du is light on memory but reads every inode: run it outside the agent, gently
	n, a := sysexec.Scoped("nice", "-n", "15", "ionice", "-c3", "du", "-x", "-b", "-d", "1", path)
	out, _ := exec.CommandContext(ctx, n, a...).Output()
	if ctx.Err() != nil {
		rep.Partial = true
	}
	sc := bufio.NewScanner(strings.NewReader(string(out)))
	for sc.Scan() {
		size, p, ok := strings.Cut(sc.Text(), "\t")
		if !ok {
			continue
		}
		b, _ := strconv.ParseInt(size, 10, 64)
		if p == path {
			rep.Total = b
			continue
		}
		rep.Items = append(rep.Items, DirUse{Path: p, Bytes: b, Dir: true})
	}
	// files directly in the directory
	ents, _ := os.ReadDir(path)
	for _, e := range ents {
		if e.Type().IsRegular() {
			if fi, err := e.Info(); err == nil && fi.Size() > 0 {
				rep.Items = append(rep.Items, DirUse{Path: filepath.Join(path, e.Name()), Bytes: fi.Size()})
			}
		}
	}
	sort.Slice(rep.Items, func(i, j int) bool { return rep.Items[i].Bytes > rep.Items[j].Bytes })
	if len(rep.Items) > 60 {
		rep.Items = rep.Items[:60]
	}
	n, a = sysexec.Scoped("nice", "-n", "15", "ionice", "-c3", "find", path, "-xdev", "-type", "f", "-size", "+50M", "-printf", "%s\t%p\n")
	out, _ = exec.CommandContext(ctx, n, a...).Output()
	sc = bufio.NewScanner(strings.NewReader(string(out)))
	for sc.Scan() {
		size, p, ok := strings.Cut(sc.Text(), "\t")
		if ok {
			b, _ := strconv.ParseInt(size, 10, 64)
			rep.Big = append(rep.Big, DirUse{Path: p, Bytes: b})
		}
	}
	sort.Slice(rep.Big, func(i, j int) bool { return rep.Big[i].Bytes > rep.Big[j].Bytes })
	if len(rep.Big) > 20 {
		rep.Big = rep.Big[:20]
	}
	if ctx.Err() != nil {
		rep.Partial = true
	}
	spaceCache.Store(path, rep)
	return rep, nil
}

// ---- clean-up ----

type Cleanup struct {
	Key   string `json:"key"` // images | buildcache | journal | pkgcache | trash
	Bytes int64  `json:"bytes"`
	Note  string `json:"note,omitempty"`
}

func dirSize(ctx context.Context, p string) int64 {
	out, err := run(ctx, "du", "-sxb", p)
	if err != nil && out == "" {
		return 0
	}
	n, _ := strconv.ParseInt(strings.Fields(out + " 0")[0], 10, 64)
	return n
}

var humanRe = regexp.MustCompile(`([\d.]+)\s*([KMGT]?)i?B`)

func parseHuman(s string) int64 {
	m := humanRe.FindStringSubmatch(s)
	if m == nil {
		return 0
	}
	f, _ := strconv.ParseFloat(m[1], 64)
	mult := map[string]float64{"": 1, "K": 1 << 10, "M": 1 << 20, "G": 1 << 30, "T": 1 << 40}[m[2]]
	return int64(f * mult)
}

// CleanupSuggestions estimates what each clean-up would free.
func CleanupSuggestions(ctx context.Context, dockerReclaim func(context.Context) (images, build int64)) []Cleanup {
	out := []Cleanup{}
	if dockerReclaim != nil {
		img, bc := dockerReclaim(ctx)
		out = append(out, Cleanup{Key: "images", Bytes: img}, Cleanup{Key: "buildcache", Bytes: bc})
	}
	if o, err := run(ctx, "journalctl", "--disk-usage"); err == nil {
		total := parseHuman(o)
		out = append(out, Cleanup{Key: "journal", Bytes: max(0, total-200<<20), Note: "keep 14 days / 200 MB"})
	}
	for _, p := range []string{"/var/cache/libdnf5", "/var/cache/dnf", "/var/cache/apt/archives"} {
		if _, err := os.Stat(p); err == nil {
			out = append(out, Cleanup{Key: "pkgcache", Bytes: dirSize(ctx, p)})
			break
		}
	}
	return out
}

// RunCleanup does one clean-up and reports what it printed.
func RunCleanup(ctx context.Context, key string, log func(string)) error {
	switch key {
	case "journal":
		return runLog(ctx, log, "journalctl", "--vacuum-time=14d", "--vacuum-size=200M")
	case "pkgcache":
		if _, err := exec.LookPath("dnf"); err == nil {
			return runLog(ctx, log, "dnf", "clean", "packages")
		}
		return runLog(ctx, log, "apt-get", "clean")
	}
	return errors.New("unknown clean-up " + key)
}
