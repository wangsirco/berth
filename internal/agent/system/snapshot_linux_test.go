//go:build linux

package system

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// On a loop-mounted btrfs (root only, BERTH_BTRFS_TEST=1): snapshot, a "system update" (a file
// changes, a new kernel lands in /boot), roll back, undo the rollback, and keep five automatic.
func TestSnapshotRollbackOnLoop(t *testing.T) {
	if os.Getenv("BERTH_BTRFS_TEST") != "1" || os.Geteuid() != 0 {
		t.Skip("needs root and BERTH_BTRFS_TEST=1")
	}
	ctx := context.Background()
	dir := t.TempDir()
	img := filepath.Join(dir, "fs.img")
	run := func(name string, args ...string) string {
		out, err := exec.Command(name, args...).CombinedOutput()
		if err != nil {
			t.Fatalf("%s %v: %v %s", name, args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	run("truncate", "-s", "1G", img)
	run("mkfs.btrfs", "-q", img)
	dev := run("losetup", "--find", "--show", img)
	defer exec.Command("losetup", "-d", dev).Run()
	setup := filepath.Join(dir, "setup")
	os.MkdirAll(setup, 0o755)
	run("mount", dev, setup)
	run("btrfs", "subvolume", "create", filepath.Join(setup, "root"))
	os.WriteFile(filepath.Join(setup, "root", "etc-version"), []byte("1"), 0o644)
	run("umount", setup)
	boot := filepath.Join(dir, "boot")
	os.MkdirAll(filepath.Join(boot, "loader", "entries"), 0o755)
	os.WriteFile(filepath.Join(boot, "vmlinuz-1"), []byte("k1"), 0o644)
	os.WriteFile(filepath.Join(boot, "loader", "entries", "1.conf"), []byte("e1"), 0o644)

	fs := snapFS{Dev: dev, RootSub: "root", Top: filepath.Join(dir, "top"), Boot: boot}
	defer exec.Command("umount", fs.Top).Run()
	s, err := fs.create(ctx, "before update", true)
	if err != nil {
		t.Fatal(err)
	}
	// the update
	os.WriteFile(filepath.Join(fs.Top, "root", "etc-version"), []byte("2"), 0o644)
	os.WriteFile(filepath.Join(boot, "vmlinuz-2"), []byte("k2"), 0o644)
	os.WriteFile(filepath.Join(boot, "loader", "entries", "2.conf"), []byte("e2"), 0o644)

	p, err := fs.rollback(ctx, s.ID)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(fs.Top, "root", "etc-version")); string(b) != "1" {
		t.Fatalf("root after rollback: %q", b)
	}
	if b, _ := os.ReadFile(filepath.Join(fs.Top, p.Kept, "etc-version")); string(b) != "2" {
		t.Fatalf("kept root: %q", b)
	}
	if _, err := os.Stat(filepath.Join(boot, "vmlinuz-2")); err == nil {
		t.Fatal("the newer kernel is still in /boot")
	}
	if _, err := os.Stat(filepath.Join(boot, "loader", "entries", "2.conf")); err == nil {
		t.Fatal("the newer boot entry is still there")
	}
	if _, err := os.Stat(filepath.Join(boot, "vmlinuz-1")); err != nil {
		t.Fatal("the old kernel is gone")
	}
	// the new root is writable
	if err := os.WriteFile(filepath.Join(fs.Top, "root", "w"), []byte("x"), 0o644); err != nil {
		t.Fatal("rolled-back root is read-only: ", err)
	}
	if _, err := fs.rollback(ctx, s.ID); err == nil {
		t.Fatal("a second rollback while one is pending")
	}
	if err := fs.undo(ctx, p); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(fs.Top, "root", "etc-version")); string(b) != "2" {
		t.Fatalf("root after undo: %q", b)
	}
	if _, err := os.Stat(filepath.Join(boot, "vmlinuz-2")); err != nil {
		t.Fatal("undo did not bring the newer kernel back")
	}
	if _, err := os.Stat(fs.pendingPath()); err == nil {
		t.Fatal("still pending after undo")
	}
	for i := 0; i < 7; i++ {
		if _, err := fs.create(ctx, "auto", true); err != nil {
			t.Fatal(err)
		}
	}
	if n := len(fs.list()); n != snapKeepAuto {
		t.Fatalf("%d automatic snapshots kept", n)
	}
}
