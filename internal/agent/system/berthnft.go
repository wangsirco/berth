package system

// Berth's own small nft tables (berth_proj for native project ports, berth_db for native
// databases). Each is written to /etc/berth/nft/<table>.nft and loaded at boot by
// berth-nft.service, before the network comes up — so a reboot never leaves the ports open
// while the services that use them start.

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const (
	nftDir      = "/etc/berth/nft"
	nftBootUnit = "berth-nft.service"
)

const nftBootUnitText = `# Managed by Berth: loads Berth's nft tables at boot.
[Unit]
Description=Berth firewall tables
DefaultDependencies=no
After=local-fs.target
Before=network-pre.target
Wants=network-pre.target

[Service]
Type=oneshot
RemainAfterExit=yes
ExecStart=/bin/sh -c 'for f in ` + nftDir + `/*.nft; do [ -e "$f" ] && /usr/sbin/nft -f "$f"; done; true'

[Install]
WantedBy=sysinit.target
`

// applyNftTable loads a table (body is what goes inside "table inet <name> { … }") now and
// at every boot.
func applyNftTable(ctx context.Context, name, body string) error {
	text := "table inet " + name + "\ndelete table inet " + name + "\ntable inet " + name + " {\n" + body + "}\n"
	os.MkdirAll(nftDir, 0o755)
	if err := os.WriteFile(filepath.Join(nftDir, name+".nft"), []byte(text), 0o644); err != nil {
		return err
	}
	ensureNftBootUnit(ctx)
	cmd := exec.CommandContext(ctx, "nft", "-f", "-")
	cmd.Stdin = strings.NewReader(text)
	if out, err := cmd.CombinedOutput(); err != nil {
		return &nftError{strings.TrimSpace(string(out))}
	}
	return nil
}

type nftError struct{ msg string }

func (e *nftError) Error() string { return "nft: " + e.msg }

func dropNftTable(ctx context.Context, name string) {
	exec.CommandContext(ctx, "nft", "delete", "table", "inet", name).Run()
	os.Remove(filepath.Join(nftDir, name+".nft"))
	if m, _ := filepath.Glob(filepath.Join(nftDir, "*.nft")); len(m) == 0 {
		run(ctx, "systemctl", "disable", nftBootUnit)
		os.Remove(filepath.Join("/etc/systemd/system", nftBootUnit))
		run(ctx, "systemctl", "daemon-reload")
	}
}

func ensureNftBootUnit(ctx context.Context) {
	p := filepath.Join("/etc/systemd/system", nftBootUnit)
	if b, err := os.ReadFile(p); err == nil && string(b) == nftBootUnitText {
		return
	}
	os.WriteFile(p, []byte(nftBootUnitText), 0o644)
	run(ctx, "systemctl", "daemon-reload")
	run(ctx, "systemctl", "enable", nftBootUnit)
}

// RestoreNftTables reloads the saved tables (the agent calls it when it starts, in case the
// boot unit is missing, e.g. right after an upgrade from a version without it).
func RestoreNftTables(ctx context.Context) {
	m, _ := filepath.Glob(filepath.Join(nftDir, "*.nft"))
	for _, f := range m {
		exec.CommandContext(ctx, "nft", "-f", f).Run()
	}
	if len(m) > 0 {
		ensureNftBootUnit(ctx)
		return
	}
	// before R4 the project table was only loaded at deploy time
	u, _ := filepath.Glob("/etc/systemd/system/berth-proj-*@.service")
	p, _ := filepath.Glob("/etc/systemd/system/berth-php-*.service")
	if len(u)+len(p) > 0 {
		ensureNativeFirewall(ctx)
	}
}
