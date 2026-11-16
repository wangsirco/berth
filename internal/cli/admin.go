package cli

// berth admin: the panel's rescue menu over SSH (security entry, username, password). It is
// berth-panel's own `admin` command, which works on the panel database directly.

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

func cmdAdmin(args []string) error {
	bin, err := exec.LookPath("berth-panel")
	if err != nil {
		bin = "/usr/local/bin/berth-panel"
		if _, err := os.Stat(bin); err != nil {
			return errors.New("这台机器上没有 berth-panel，请在装了面板的服务器上运行 / berth-panel is not installed here; run this on the panel's server")
		}
	}
	if os.Geteuid() != 0 {
		return errors.New("请用 root 运行：sudo berth admin / run it as root: sudo berth admin")
	}
	return syscall.Exec(bin, append([]string{bin, "admin"}, args...), os.Environ())
}
