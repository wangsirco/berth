package system

// System snapshots on btrfs (系统快照): before system updates and package changes, the root
// subvolume is snapshotted read-only (seconds, and it costs only what changes afterwards), with
// a copy of /boot (a separate partition on Fedora, where new kernels land). Rolling back makes a
// writable root from a snapshot under the same name the boot loader mounts (rootflags=
// subvol=root), keeps the current root beside it, puts /boot back, and takes effect at the next
// boot. /var and /home are other subvolumes and are left alone: sites, databases, containers
// and VMs keep their data.

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/wangsirco/berth/internal/errs"
)

type SysSnapshot struct {
	ID      string `json:"id"`
	Reason  string `json:"reason"`
	Kernel  string `json:"kernel"`
	Created int64  `json:"created"`
	Auto    bool   `json:"auto"`
	Boot    bool   `json:"boot"` // /boot was saved with it
}

type SnapStatus struct {
	Supported bool          `json:"supported"`
	Why       string        `json:"why,omitempty"` // why not: not btrfs, root not a subvolume
	RootSub   string        `json:"root_sub,omitempty"`
	Snapshots []SysSnapshot `json:"snapshots"`
	Pending   string        `json:"pending,omitempty"` // a rollback waiting for the reboot
	Previous  []string      `json:"previous"`          // roots kept from earlier rollbacks
}

// snapFS is where the work happens; tests point it at a loop device.
type snapFS struct {
	Dev     string // the btrfs device holding /
	RootSub string // the subvolume mounted at / (e.g. "root")
	Top     string // where the top level (subvolid=5) is mounted
	Boot    string // the /boot folder to save and restore ("" when /boot is part of root)
}

const snapDirName = "berth-snapshots"
const snapKeepAuto = 5

var (
	snapIDRe = regexp.MustCompile(`^[0-9]{8}-[0-9]{6}$`)
	prevRe   = regexp.MustCompile(`^[A-Za-z0-9_-]+\.before-[0-9]{8}-[0-9]{6}$`)
)

// detectSnapFS: is / a btrfs subvolume we can snapshot?
func detectSnapFS(ctx context.Context) (snapFS, string) {
	out, err := exec.CommandContext(ctx, "findmnt", "-no", "SOURCE,FSTYPE,OPTIONS", "/").Output()
	if err != nil {
		return snapFS{}, "snap.no_findmnt"
	}
	f := strings.Fields(string(out))
	if len(f) < 3 || f[1] != "btrfs" {
		return snapFS{}, "snap.not_btrfs"
	}
	dev := f[0]
	if i := strings.Index(dev, "["); i > 0 {
		dev = dev[:i]
	}
	sub := ""
	for _, o := range strings.Split(f[2], ",") {
		if v, ok := strings.CutPrefix(o, "subvol="); ok {
			sub = strings.TrimPrefix(v, "/")
		}
	}
	if sub == "" || strings.Contains(sub, "/") {
		return snapFS{}, "snap.root_not_subvol"
	}
	fs := snapFS{Dev: dev, RootSub: sub, Top: "/run/berth-btrfs"}
	if b, err := exec.CommandContext(ctx, "findmnt", "-no", "TARGET", "/boot").Output(); err == nil && strings.TrimSpace(string(b)) == "/boot" {
		fs.Boot = "/boot"
	}
	return fs, ""
}

func (fs snapFS) mount(ctx context.Context) error {
	if b, _ := exec.CommandContext(ctx, "findmnt", "-no", "TARGET", fs.Top).Output(); strings.TrimSpace(string(b)) == fs.Top {
		return nil
	}
	os.MkdirAll(fs.Top, 0o700)
	if out, err := exec.CommandContext(ctx, "mount", "-o", "subvolid=5", fs.Dev, fs.Top).CombinedOutput(); err != nil {
		return errors.New("mount the btrfs top level: " + strings.TrimSpace(string(out)))
	}
	return nil
}

func (fs snapFS) dir() string { return filepath.Join(fs.Top, snapDirName) }

func btrfs(ctx context.Context, args ...string) error {
	if out, err := exec.CommandContext(ctx, "btrfs", args...).CombinedOutput(); err != nil {
		return errors.New("btrfs " + args[0] + " " + args[1] + ": " + strings.TrimSpace(string(out)))
	}
	return nil
}

func (fs snapFS) create(ctx context.Context, reason string, auto bool) (SysSnapshot, error) {
	if err := fs.mount(ctx); err != nil {
		return SysSnapshot{}, err
	}
	s := SysSnapshot{ID: time.Now().Format("20060102-150405"), Reason: reason, Created: time.Now().Unix(), Auto: auto}
	if k, err := os.ReadFile("/proc/sys/kernel/osrelease"); err == nil {
		s.Kernel = strings.TrimSpace(string(k))
	}
	d := filepath.Join(fs.dir(), s.ID)
	if _, err := os.Stat(d); err == nil { // two in the same second
		time.Sleep(time.Second)
		return fs.create(ctx, reason, auto)
	}
	if err := os.MkdirAll(d, 0o700); err != nil {
		return s, err
	}
	if err := btrfs(ctx, "subvolume", "snapshot", "-r", filepath.Join(fs.Top, fs.RootSub), filepath.Join(d, "root")); err != nil {
		os.RemoveAll(d)
		return s, err
	}
	if fs.Boot != "" {
		if out, err := exec.CommandContext(ctx, "tar", "-czf", filepath.Join(d, "boot.tar.gz"), "-C", fs.Boot, ".").CombinedOutput(); err != nil {
			fs.delete(ctx, s.ID)
			return s, errors.New("save /boot: " + strings.TrimSpace(string(out)))
		}
		s.Boot = true
	}
	b, _ := json.MarshalIndent(s, "", " ")
	os.WriteFile(filepath.Join(d, "meta.json"), b, 0o600)
	if auto {
		fs.prune(ctx)
	}
	return s, nil
}

func (fs snapFS) list() []SysSnapshot {
	out := []SysSnapshot{}
	es, _ := os.ReadDir(fs.dir())
	for _, e := range es {
		var s SysSnapshot
		if b, err := os.ReadFile(filepath.Join(fs.dir(), e.Name(), "meta.json")); err == nil && json.Unmarshal(b, &s) == nil && s.ID == e.Name() {
			out = append(out, s)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	return out
}

// prune keeps the newest automatic snapshots; ones people took stay until they delete them.
func (fs snapFS) prune(ctx context.Context) {
	n := 0
	for _, s := range fs.list() {
		if !s.Auto {
			continue
		}
		if n++; n > snapKeepAuto {
			fs.delete(ctx, s.ID)
		}
	}
}

func (fs snapFS) delete(ctx context.Context, id string) error {
	if !snapIDRe.MatchString(id) {
		return errs.New(400, "snap.bad_id")
	}
	d := filepath.Join(fs.dir(), id)
	if _, err := os.Stat(filepath.Join(d, "root")); err == nil {
		if err := btrfs(ctx, "subvolume", "delete", filepath.Join(d, "root")); err != nil {
			return err
		}
	}
	return os.RemoveAll(d)
}

func (fs snapFS) pendingPath() string { return filepath.Join(fs.dir(), "pending.json") }

type snapPending struct {
	ID   string `json:"id"`   // the snapshot rolled back to
	Kept string `json:"kept"` // the root it replaced, kept under this name
	Boot string `json:"boot"` // /boot as it was, to undo
}

// rollback makes the snapshot the root for the next boot. The current root is kept as
// <root>.before-<time>; undoRollback reverses this until the reboot.
func (fs snapFS) rollback(ctx context.Context, id string) (snapPending, error) {
	var p snapPending
	if !snapIDRe.MatchString(id) {
		return p, errs.New(400, "snap.bad_id")
	}
	if err := fs.mount(ctx); err != nil {
		return p, err
	}
	if b, err := os.ReadFile(fs.pendingPath()); err == nil && len(b) > 0 {
		return p, errs.New(409, "snap.pending")
	}
	src := filepath.Join(fs.dir(), id, "root")
	if _, err := os.Stat(src); err != nil {
		return p, errs.New(404, "snap.not_found", "id", id)
	}
	ts := time.Now().Format("20060102-150405")
	cur := filepath.Join(fs.Top, fs.RootSub)
	kept := fs.RootSub + ".before-" + ts
	p = snapPending{ID: id, Kept: kept}
	if fs.Boot != "" {
		p.Boot = filepath.Join(fs.dir(), "boot-before-"+ts+".tar.gz")
		if out, err := exec.CommandContext(ctx, "tar", "-czf", p.Boot, "-C", fs.Boot, ".").CombinedOutput(); err != nil {
			return p, errors.New("save /boot: " + strings.TrimSpace(string(out)))
		}
	}
	if err := os.Rename(cur, filepath.Join(fs.Top, kept)); err != nil {
		return p, err
	}
	if err := btrfs(ctx, "subvolume", "snapshot", src, cur); err != nil {
		os.Rename(filepath.Join(fs.Top, kept), cur)
		return p, err
	}
	if fs.Boot != "" {
		if _, err := os.Stat(filepath.Join(fs.dir(), id, "boot.tar.gz")); err == nil {
			if err := restoreBoot(ctx, fs.Boot, filepath.Join(fs.dir(), id, "boot.tar.gz")); err != nil {
				fs.undo(ctx, p)
				return p, err
			}
		}
	}
	b, _ := json.Marshal(p)
	os.WriteFile(fs.pendingPath(), b, 0o600)
	return p, nil
}

// restoreBoot makes dir hold exactly what the archive holds (the kernels and boot entries of
// that time; a newer kernel whose modules are not in the rolled-back root must not boot).
func restoreBoot(ctx context.Context, dir, archive string) error {
	tmp := dir + "/.berth-restore"
	os.RemoveAll(tmp)
	if err := os.MkdirAll(tmp, 0o700); err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	if out, err := exec.CommandContext(ctx, "tar", "-xzf", archive, "-C", tmp).CombinedOutput(); err != nil {
		return errors.New("read the saved /boot: " + strings.TrimSpace(string(out)))
	}
	// remove what the archive does not have, then copy it in
	var extra []string
	filepath.Walk(dir, func(p string, fi os.FileInfo, err error) error {
		if err != nil || p == dir || strings.HasPrefix(p, tmp) {
			if p == tmp {
				return filepath.SkipDir
			}
			return nil
		}
		rel, _ := filepath.Rel(dir, p)
		if rel == "lost+found" || strings.HasPrefix(rel, "efi") {
			if fi.IsDir() {
				return filepath.SkipDir // another filesystem (the EFI partition) or fsck's
			}
			return nil
		}
		if _, err := os.Lstat(filepath.Join(tmp, rel)); errors.Is(err, os.ErrNotExist) {
			extra = append(extra, p)
			if fi.IsDir() {
				return filepath.SkipDir
			}
		}
		return nil
	})
	for _, p := range extra {
		os.RemoveAll(p)
	}
	if out, err := exec.CommandContext(ctx, "cp", "-a", tmp+"/.", dir+"/").CombinedOutput(); err != nil {
		return errors.New("restore /boot: " + strings.TrimSpace(string(out)))
	}
	return nil
}

func (fs snapFS) undo(ctx context.Context, p snapPending) error {
	cur := filepath.Join(fs.Top, fs.RootSub)
	if _, err := os.Stat(filepath.Join(fs.Top, p.Kept)); err != nil {
		return errs.New(409, "snap.nothing_to_undo")
	}
	if err := btrfs(ctx, "subvolume", "delete", cur); err != nil {
		return err
	}
	if err := os.Rename(filepath.Join(fs.Top, p.Kept), cur); err != nil {
		return err
	}
	if p.Boot != "" && fs.Boot != "" {
		if err := restoreBoot(ctx, fs.Boot, p.Boot); err != nil {
			return err
		}
		os.Remove(p.Boot)
	}
	os.Remove(fs.pendingPath())
	return nil
}

// previous: roots kept by earlier rollbacks (deletable to free space).
func (fs snapFS) previous() []string {
	out := []string{}
	es, _ := os.ReadDir(fs.Top)
	for _, e := range es {
		if strings.HasPrefix(e.Name(), fs.RootSub+".before-") {
			out = append(out, e.Name())
		}
	}
	return out
}

// ---- for the routes ----

func snapFSOrErr(ctx context.Context) (snapFS, error) {
	fs, why := detectSnapFS(ctx)
	if why != "" {
		return fs, errs.New(409, why)
	}
	return fs, nil
}

func SnapshotStatus(ctx context.Context) SnapStatus {
	fs, why := detectSnapFS(ctx)
	st := SnapStatus{Snapshots: []SysSnapshot{}, Previous: []string{}}
	if why != "" {
		st.Why = why
		return st
	}
	if err := fs.mount(ctx); err != nil {
		st.Why = "snap.mount_failed"
		return st
	}
	st.Supported, st.RootSub, st.Snapshots, st.Previous = true, fs.RootSub, fs.list(), fs.previous()
	if b, err := os.ReadFile(fs.pendingPath()); err == nil {
		var p snapPending
		json.Unmarshal(b, &p)
		// after the reboot the kept root is what came before: nothing is pending any more
		if bootedAfter(fs.pendingPath()) {
			os.Remove(fs.pendingPath())
		} else {
			st.Pending = p.ID
		}
	}
	return st
}

// bootedAfter: did the machine start after this file was written?
func bootedAfter(p string) bool {
	fi, err := os.Stat(p)
	if err != nil {
		return false
	}
	b, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return false
	}
	var up float64
	if f := strings.Fields(string(b)); len(f) > 0 {
		json.Unmarshal([]byte(f[0]), &up)
	}
	return time.Since(fi.ModTime()).Seconds() > up
}

func SnapshotCreate(ctx context.Context, reason string, auto bool) (SysSnapshot, error) {
	fs, err := snapFSOrErr(ctx)
	if err != nil {
		return SysSnapshot{}, err
	}
	return fs.create(ctx, reason, auto)
}

func SnapshotDelete(ctx context.Context, id string) error {
	fs, err := snapFSOrErr(ctx)
	if err != nil {
		return err
	}
	if err := fs.mount(ctx); err != nil {
		return err
	}
	if b, err := os.ReadFile(fs.pendingPath()); err == nil {
		var p snapPending
		if json.Unmarshal(b, &p) == nil && p.ID == id {
			return errs.New(409, "snap.pending")
		}
	}
	return fs.delete(ctx, id)
}

// SnapshotDeletePrevious frees a root kept by an earlier rollback.
func SnapshotDeletePrevious(ctx context.Context, name string) error {
	fs, err := snapFSOrErr(ctx)
	if err != nil {
		return err
	}
	if err := fs.mount(ctx); err != nil {
		return err
	}
	if !strings.HasPrefix(name, fs.RootSub+".before-") || !prevRe.MatchString(name) {
		return errs.New(400, "snap.bad_id")
	}
	if b, err := os.ReadFile(fs.pendingPath()); err == nil {
		var p snapPending
		if json.Unmarshal(b, &p) == nil && p.Kept == name {
			return errs.New(409, "snap.pending")
		}
	}
	return btrfs(ctx, "subvolume", "delete", filepath.Join(fs.Top, name))
}

func SnapshotRollback(ctx context.Context, id string) error {
	fs, err := snapFSOrErr(ctx)
	if err != nil {
		return err
	}
	_, err = fs.rollback(ctx, id)
	return err
}

func SnapshotUndoRollback(ctx context.Context) error {
	fs, err := snapFSOrErr(ctx)
	if err != nil {
		return err
	}
	if err := fs.mount(ctx); err != nil {
		return err
	}
	b, err := os.ReadFile(fs.pendingPath())
	if err != nil {
		return errs.New(409, "snap.nothing_to_undo")
	}
	var p snapPending
	json.Unmarshal(b, &p)
	return fs.undo(ctx, p)
}
