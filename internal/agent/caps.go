package agent

import (
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/wangsirco/berth/internal/agentproto"
)

func osRelease() (id, ver string) {
	b, _ := os.ReadFile("/etc/os-release")
	for _, l := range strings.Split(string(b), "\n") {
		k, v, ok := strings.Cut(l, "=")
		if !ok {
			continue
		}
		v = strings.Trim(v, `"`)
		switch k {
		case "ID":
			id = v
		case "VERSION_ID":
			ver = v
		}
	}
	return
}

func has(bin string) bool { _, err := exec.LookPath(bin); return err == nil }

// Caps answers from a cache: detecting runs docker and the compose plugin (tens of MB each),
// and the agent's memory limit counts them. Callers at the same time wait for one run instead
// of each starting their own (dozens at once once pinned the agent's cgroup at its limit).
var (
	capsMu   sync.Mutex
	capsLast agentproto.Caps
	capsAt   time.Time
)

const capsTTL = time.Minute

func Caps() agentproto.Caps {
	capsMu.Lock()
	defer capsMu.Unlock()
	if !capsAt.IsZero() && time.Since(capsAt) < capsTTL {
		return capsLast
	}
	capsLast, capsAt = DetectCaps(), time.Now()
	return capsLast
}

// ForgetCaps makes the next Caps detect again (after installing or removing packages).
func ForgetCaps() {
	capsMu.Lock()
	capsAt = time.Time{}
	capsMu.Unlock()
}

func DetectCaps() agentproto.Caps {
	c := agentproto.Caps{Arch: runtime.GOARCH, PageSize: os.Getpagesize()}
	c.OS, c.OSVersion = osRelease()
	if b, err := os.ReadFile("/proc/sys/kernel/osrelease"); err == nil {
		c.Kernel = strings.TrimSpace(string(b))
	}
	if _, err := os.Stat("/var/run/docker.sock"); err == nil {
		c.Docker = true
		if out, err := exec.Command("docker", "version", "--format", "{{.Server.Version}}").Output(); err == nil {
			c.DockerVersion = strings.TrimSpace(string(out))
		}
		c.Compose = exec.Command("docker", "compose", "version").Run() == nil
	}
	_, kvm := os.Stat("/dev/kvm")
	c.KVM = kvm == nil
	c.Incus, c.Smartctl, c.TC, c.Nft = has("incus"), has("smartctl"), has("tc"), has("nft")
	for name, ok := range map[string]bool{"docker-ce": c.Docker, "docker-compose-plugin": c.Compose, "smartmontools": c.Smartctl, "iproute-tc": c.TC, "nftables": c.Nft} {
		if !ok {
			c.Missing = append(c.Missing, name)
		}
	}
	return c
}
