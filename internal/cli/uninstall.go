package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/wangsirco/berth/internal/agent/files"
)

// cmdUninstall removes the panel and agent (方案 §13). By default everything they set up
// keeps running: /srv/berth, containers and compose projects stay. --purge also removes
// those after listing them and asking for "purge".
func cmdUninstall(args []string) error {
	purge, yes := false, false
	for _, a := range args {
		switch a {
		case "--purge":
			purge = true
		case "--yes", "-y":
			yes = true
		default:
			return fmt.Errorf("unknown option %s", a)
		}
	}
	if os.Geteuid() != 0 {
		return errors.New("run as root on the server: sudo berth uninstall")
	}
	stacks, _ := filepath.Glob("/srv/berth/stacks/*/compose.yaml")
	fmt.Println("This removes berth-panel and berth-agent:")
	fmt.Println("  - stops and deletes the berth-panel and berth-agent services and binaries")
	fmt.Println("  - backs up /etc/berth and /var/lib/berth to /srv/berth/backups, then deletes them")
	if purge {
		fmt.Println("  --purge also:")
		for _, s := range stacks {
			fmt.Printf("    - docker compose down for %s (volumes are kept)\n", filepath.Dir(s))
		}
		fmt.Println("    - deletes /srv/berth entirely (stacks, sites, backups, workspaces)")
		if !yes && ask(`Type "purge" to continue: `) != "purge" {
			return errors.New("cancelled")
		}
	} else if !yes {
		if a := strings.ToLower(ask("Continue? [y/N] ")); a != "y" && a != "yes" {
			return errors.New("cancelled")
		}
	}
	run := func(name string, args ...string) {
		if out, err := exec.Command(name, args...).CombinedOutput(); err != nil {
			fmt.Fprintf(os.Stderr, "  (%s %s: %v %s)\n", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
		}
	}
	run("systemctl", "disable", "--now", "berth-panel.service", "berth-agent.service")

	backup := ""
	if !purge {
		_ = os.MkdirAll("/srv/berth/backups", 0o700)
		backup = fmt.Sprintf("/srv/berth/backups/berth-panel-%s.tar.zst", time.Now().Format("20060102-150405"))
		var srcs []string
		for _, p := range []string{"/etc/berth", "/var/lib/berth"} {
			if _, err := os.Stat(p); err == nil {
				srcs = append(srcs, p)
			}
		}
		if len(srcs) > 0 {
			if err := files.Archive(context.Background(), "/", srcs, backup, func(string) {}); err != nil {
				return fmt.Errorf("backup failed, nothing deleted: %w", err)
			}
			_ = os.Chmod(backup, 0o600)
		}
	} else {
		for _, s := range stacks {
			dir := filepath.Dir(s)
			fmt.Printf("docker compose down: %s\n", dir)
			run("docker", "compose", "--project-directory", dir, "down")
		}
	}
	for _, p := range []string{"/etc/systemd/system/berth-panel.service", "/etc/systemd/system/berth-agent.service", "/usr/local/bin/berth-panel", "/etc/berth", "/var/lib/berth", "/run/berth"} {
		_ = os.RemoveAll(p)
	}
	run("systemctl", "daemon-reload")
	if purge {
		_ = os.RemoveAll("/srv/berth")
	}
	_ = os.Remove("/usr/local/bin/berth") // this binary; Linux keeps the running copy until exit

	fmt.Println("\nBerth is removed.")
	if purge {
		fmt.Println("Everything under /srv/berth is gone. Docker volumes were kept: `docker volume ls`.")
	} else {
		fmt.Printf("Backup of the panel's data: %s\n", backup)
		fmt.Println("What keeps running, and who manages it now:")
		fmt.Println("  - compose projects in /srv/berth/stacks/<name>: run `docker compose` in that directory")
		fmt.Println("  - other containers: plain `docker` commands")
		fmt.Println("  - files you kept in /srv/berth (sites, backups, workspaces) are untouched")
	}
	if b, err := os.ReadFile("/etc/berth-installed-packages"); err == nil && len(b) > 0 {
		fmt.Printf("Packages Berth installed (remove them yourself if unwanted): %s\n", strings.Join(strings.Fields(string(b)), " "))
	}
	return nil
}
