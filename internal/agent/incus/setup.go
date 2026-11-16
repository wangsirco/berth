package incus

import (
	"bytes"
	"context"
	"github.com/wangsirco/berth/internal/sysexec"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Status is what the panel needs before offering anything.
type Status struct {
	Installed  bool     `json:"installed"`
	Running    bool     `json:"running"`
	KVM        bool     `json:"kvm"`        // /dev/kvm present
	VMs        bool     `json:"vms"`        // Incus can run virtual machines here
	Containers bool     `json:"containers"` // and system containers
	Network    bool     `json:"network"`    // incusbr0 exists
	PageSize   int      `json:"page_size"`  // host
	Arch       string   `json:"arch"`
	Supported  bool     `json:"supported"` // this OS can install it (dnf)
	Version    string   `json:"version,omitempty"`
	Problems   []string `json:"problems,omitempty"` // codes of things the installer would fix
}

func has(bin string) bool { _, err := exec.LookPath(bin); return err == nil }

func run(ctx context.Context, name string, args ...string) (string, error) {
	var out bytes.Buffer
	name, args = sysexec.Wrap(name, args...)
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	return strings.TrimSpace(out.String()), err
}

const (
	edk2DropIn = "/etc/systemd/system/incus.service.d/berth-edk2.conf"
	subID      = "root:1000000:1000000000"
)

// Packages the installer puts in (Fedora names).
var Packages = []string{"incus", "incus-agent", "qemu-kvm-core", "edk2-aarch64",
	"qemu-device-display-virtio-gpu", "qemu-device-display-virtio-gpu-pci", "qemu-char-spice", "qemu-ui-spice-core"}

func (c *Client) Status(ctx context.Context) Status {
	st := Status{PageSize: os.Getpagesize(), Arch: goarch(), Installed: has("incus"), Supported: has("dnf")}
	if _, err := os.Stat("/dev/kvm"); err == nil {
		st.KVM = true
	}
	if !st.Installed {
		return st
	}
	var info struct {
		Environment struct {
			Drivers       string `json:"driver"`
			ServerVersion string `json:"server_version"`
		} `json:"environment"`
	}
	if _, err := c.do(ctx, "GET", "/1.0", nil, &info); err == nil {
		st.Running = true
		st.Version = info.Environment.ServerVersion
		st.Containers = strings.Contains(info.Environment.Drivers, "lxc")
		st.VMs = strings.Contains(info.Environment.Drivers, "qemu")
	}
	if _, err := c.do(ctx, "GET", "/1.0/networks/incusbr0", nil, nil); err == nil {
		st.Network = true
	}
	if !idmapOK() {
		st.Problems = append(st.Problems, "idmap")
	}
	if goarch() == "arm64" {
		if b, err := os.ReadFile(edk2DropIn); err != nil || !bytes.Contains(b, []byte("AAVMF")) {
			st.Problems = append(st.Problems, "firmware")
		}
	}
	if st.KVM && !st.VMs {
		st.Problems = append(st.Problems, "vm_driver")
	}
	return st
}

func idmapOK() bool {
	for _, f := range []string{"/etc/subuid", "/etc/subgid"} {
		b, err := os.ReadFile(f)
		if err != nil || !bytes.Contains(b, []byte("\nroot:")) && !bytes.HasPrefix(b, []byte("root:")) {
			return false
		}
	}
	return true
}

func ensureLine(path, line string) error {
	b, _ := os.ReadFile(path)
	for _, l := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(l, "root:") {
			return nil
		}
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	if len(b) > 0 && !bytes.HasSuffix(b, []byte("\n")) {
		f.WriteString("\n")
	}
	_, err = f.WriteString(line + "\n")
	return err
}

// Install sets Incus up and fixes what Fedora Asahi needs (see the phase 5 plan):
// packages, root idmap, ARM firmware path, init, and the bridge's firewall trust.
func (c *Client) Install(ctx context.Context, log func(string)) error {
	step := func(name string, args ...string) error {
		log("$ " + name + " " + strings.Join(args, " "))
		out, err := run(ctx, name, args...)
		for _, l := range strings.Split(out, "\n") {
			if l = strings.TrimSpace(l); l != "" && !strings.Contains(l, "Downloading") {
				log(l)
			}
		}
		return err
	}
	if !has("dnf") {
		return &Error{Status: 409, Message: "no dnf on this host"}
	}
	if err := step("dnf", append([]string{"-y", "-q", "install"}, Packages...)...); err != nil {
		return err
	}
	log("root idmap for unprivileged containers")
	if err := ensureLine("/etc/subuid", subID); err != nil {
		return err
	}
	if err := ensureLine("/etc/subgid", subID); err != nil {
		return err
	}
	if goarch() == "arm64" {
		log("ARM firmware path for Incus (the package points at the x86 one)")
		os.MkdirAll(filepath.Dir(edk2DropIn), 0o755)
		if err := os.WriteFile(edk2DropIn, []byte("[Service]\n# Berth: ARM firmware (the packaged path is the x86 one)\nEnvironment=INCUS_EDK2_PATH=/usr/share/AAVMF\n"), 0o644); err != nil {
			return err
		}
		step("systemctl", "daemon-reload")
	}
	if err := step("systemctl", "enable", "--now", "incus.socket", "incus.service"); err != nil {
		return err
	}
	step("systemctl", "restart", "incus.service")
	for i := 0; i < 30; i++ {
		if _, err := c.do(ctx, "GET", "/1.0", nil, nil); err == nil {
			break
		}
		time.Sleep(time.Second)
	}
	if _, err := c.do(ctx, "GET", "/1.0/networks/incusbr0", nil, nil); err != nil {
		if err := step("incus", "admin", "init", "--minimal"); err != nil {
			return err
		}
	}
	EnsureNetwork(ctx, log)
	return nil
}

// EnsureNetwork lets the bridge through firewalld and Docker's forward policy. The Docker
// rules do not survive a reboot, so the agent calls this at start too.
func EnsureNetwork(ctx context.Context, log func(string)) {
	if log == nil {
		log = func(string) {}
	}
	if _, err := os.Stat("/sys/class/net/incusbr0"); err != nil {
		return
	}
	if has("firewall-cmd") {
		if out, _ := run(ctx, "firewall-cmd", "--get-zone-of-interface=incusbr0"); out != "trusted" {
			log("firewalld: incusbr0 → trusted zone")
			run(ctx, "firewall-cmd", "--zone=trusted", "--change-interface=incusbr0", "--permanent")
			run(ctx, "firewall-cmd", "--zone=trusted", "--change-interface=incusbr0")
		}
	}
	if has("iptables") {
		if _, err := run(ctx, "iptables", "-L", "DOCKER-USER", "-n"); err == nil {
			for _, dir := range []string{"-i", "-o"} {
				if _, err := run(ctx, "iptables", "-C", "DOCKER-USER", dir, "incusbr0", "-j", "ACCEPT"); err != nil {
					log("docker: allow incusbr0 " + dir)
					run(ctx, "iptables", "-I", "DOCKER-USER", dir, "incusbr0", "-j", "ACCEPT")
				}
			}
		}
	}
}
