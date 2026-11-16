package agent

// ARM / Asahi compatibility layer. Apple-silicon kernels use 16K memory pages; a few
// programs (mostly ones built with a 4K-page jemalloc) crash on them. libkrun runs such a
// container in a tiny VM whose kernel uses 4K pages; Docker gets it as the "krun" runtime.

import (
	"encoding/json"
	"github.com/wangsirco/berth/internal/agent/drift"
	"github.com/wangsirco/berth/internal/sysexec"
	"net/http"
	"os"
	"os/exec"
	"strings"

	"github.com/wangsirco/berth/internal/errs"
)

var DockerDaemonJSON = "/etc/docker/daemon.json"

type CompatStatus struct {
	PageSize  int    `json:"page_size"`
	Needed    bool   `json:"needed"` // pages larger than 4K
	KVM       bool   `json:"kvm"`
	Packages  bool   `json:"packages"`  // libkrun + krun installed
	Runtime   bool   `json:"runtime"`   // registered in Docker
	Supported bool   `json:"supported"` // this OS can install it (dnf)
	OS        string `json:"os"`
}

func compatStatus() CompatStatus {
	id, _ := osRelease()
	st := CompatStatus{PageSize: os.Getpagesize(), OS: id, Packages: has("krun")}
	st.Needed = st.PageSize > 4096
	_, err := os.Stat("/dev/kvm")
	st.KVM = err == nil
	st.Supported = has("dnf")
	if b, err := os.ReadFile(DockerDaemonJSON); err == nil {
		var cfg map[string]any
		if json.Unmarshal(b, &cfg) == nil {
			if rt, ok := cfg["runtimes"].(map[string]any); ok {
				_, st.Runtime = rt["krun"]
			}
		}
	}
	return st
}

// setKrunRuntime adds or removes runtimes.krun in daemon.json, keeping everything else.
func setKrunRuntime(on bool) error {
	cfg := map[string]any{}
	if b, err := os.ReadFile(DockerDaemonJSON); err == nil && len(strings.TrimSpace(string(b))) > 0 {
		if err := json.Unmarshal(b, &cfg); err != nil {
			return errs.New(409, "compat.daemon_json", "detail", err.Error())
		}
	}
	rt, _ := cfg["runtimes"].(map[string]any)
	if rt == nil {
		rt = map[string]any{}
	}
	if on {
		rt["krun"] = map[string]string{"path": "/usr/bin/krun"}
	} else {
		delete(rt, "krun")
	}
	if len(rt) == 0 {
		delete(cfg, "runtimes")
	} else {
		cfg["runtimes"] = rt
	}
	b, _ := json.MarshalIndent(cfg, "", "  ")
	if err := os.MkdirAll("/etc/docker", 0o755); err != nil {
		return err
	}
	if old, err := os.ReadFile(DockerDaemonJSON); err == nil {
		_ = os.WriteFile(DockerDaemonJSON+".berth-prev", old, 0o644)
	}
	tmp := DockerDaemonJSON + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, DockerDaemonJSON); err != nil {
		return err
	}
	drift.Track(DockerDaemonJSON, "docker", "")
	return nil
}

func run(st *StreamWriter, name string, args ...string) error {
	name, args = sysexec.Wrap(name, args...)
	c := exec.Command(name, args...)
	out, err := c.CombinedOutput()
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if l != "" {
			st.Send(map[string]string{"l": l})
		}
	}
	return err
}

func registerCompat(s *Server) {
	s.Handle("GET /v1/compat", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		WriteJSON(w, 200, compatStatus())
	})
	s.Handle("POST /v1/compat/install", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		st0 := compatStatus()
		if !st0.KVM {
			WriteErr(w, errs.New(409, "compat.no_kvm"))
			return
		}
		if !st0.Packages && !st0.Supported {
			WriteErr(w, errs.New(409, "compat.unsupported_os", "os", st0.OS))
			return
		}
		st := NewStream(w)
		if !st0.Packages {
			if err := run(st, "dnf", "-y", "-q", "install", "crun-krun", "libkrun"); err != nil {
				st.End(errs.New(500, "compat.install_failed", "detail", err.Error()))
				return
			}
		}
		if err := setKrunRuntime(true); err != nil {
			st.End(err)
			return
		}
		// reload, not restart: running containers keep running
		if err := run(st, "systemctl", "reload", "docker"); err != nil {
			st.End(errs.New(500, "compat.install_failed", "detail", err.Error()))
			return
		}
		st.End(nil)
	})
	s.Handle("POST /v1/compat/uninstall", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		if err := setKrunRuntime(false); err != nil {
			WriteErr(w, err)
			return
		}
		if out, err := exec.Command("systemctl", "reload", "docker").CombinedOutput(); err != nil {
			WriteErr(w, errs.New(500, "compat.install_failed", "detail", string(out)))
			return
		}
		WriteJSON(w, 200, map[string]bool{"ok": true})
	})
}
