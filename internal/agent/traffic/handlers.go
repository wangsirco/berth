package traffic

import (
	"context"
	"encoding/json"
	"github.com/wangsirco/berth/internal/sysexec"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/wangsirco/berth/internal/agent"
	"github.com/wangsirco/berth/internal/agent/docker"
	"github.com/wangsirco/berth/internal/errs"
)

// SysctlFile turns on per-connection byte counters at boot.
var SysctlFile = "/etc/sysctl.d/90-berth-traffic.conf"

func toolsStatus() map[string]bool {
	_, err := exec.LookPath("tc")
	return map[string]bool{"tc": err == nil, "acct": acctOn(), "dnf": func() bool { _, e := exec.LookPath("dnf"); return e == nil }()}
}

// Register adds /v1/traffic/*.
func Register(s *agent.Server, dc *docker.Client) {
	pidOf := func(ctx context.Context, name string) (int, error) {
		d, err := dc.Inspect(ctx, name)
		if err != nil {
			return 0, err
		}
		return d.Pid, nil
	}
	s.Handle("GET /v1/traffic/containers", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		cs, err := dc.Containers(r.Context())
		if err != nil {
			agent.WriteErr(w, err)
			return
		}
		out := []Counters{}
		for _, c := range cs {
			if c.State != "running" {
				continue
			}
			pid, err := pidOf(r.Context(), c.ID)
			if err != nil || pid == 0 {
				continue
			}
			rx, tx, err := netDev(pid)
			if err != nil {
				continue
			}
			out = append(out, Counters{Name: c.Name, ID: c.ID, RX: rx, TX: tx})
		}
		agent.WriteJSON(w, 200, out)
	})
	s.Handle("GET /v1/traffic/live", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		sum, err := Live()
		if err != nil {
			agent.WriteErr(w, errs.New(500, "traffic.unavailable", "detail", err.Error()))
			return
		}
		agent.WriteJSON(w, 200, sum)
	})
	s.Handle("GET /v1/traffic/tools", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		agent.WriteJSON(w, 200, toolsStatus())
	})
	// installs tc and turns on conntrack byte counters (now and at boot)
	s.Handle("POST /v1/traffic/tools/install", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		st := agent.NewStream(w)
		log := func(l string) { st.Send(map[string]string{"l": l}) }
		runLog := func(name string, args ...string) error {
			name, args = sysexec.Wrap(name, args...)
			out, err := exec.CommandContext(r.Context(), name, args...).CombinedOutput()
			for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
				if l != "" {
					log(l)
				}
			}
			return err
		}
		var err error
		if _, e := exec.LookPath("tc"); e != nil {
			if _, e := exec.LookPath("dnf"); e != nil {
				err = errs.New(409, "traffic.no_package_manager")
			} else if e := runLog("dnf", "-y", "-q", "install", "iproute-tc"); e != nil {
				err = errs.New(500, "traffic.tools_failed", "detail", e.Error())
			}
		}
		if err == nil {
			os.MkdirAll(filepath.Dir(SysctlFile), 0o755)
			os.WriteFile(SysctlFile, []byte("# Berth: per-connection byte counters for traffic statistics\nnet.netfilter.nf_conntrack_acct = 1\n"), 0o644)
			if e := runLog("sysctl", "-w", "net.netfilter.nf_conntrack_acct=1"); e != nil {
				err = errs.New(500, "traffic.tools_failed", "detail", e.Error())
			}
		}
		if err == nil {
			b, _ := json.Marshal(toolsStatus())
			log("::result " + string(b))
		}
		st.End(err)
	})
	s.Handle("POST /v1/traffic/limits", func(w http.ResponseWriter, r *http.Request, body []byte) {
		var limits []Limit
		if err := json.Unmarshal(body, &limits); err != nil {
			agent.WriteErr(w, errs.New(400, "traffic.bad_limit"))
			return
		}
		if _, err := exec.LookPath("tc"); err != nil && len(limits) > 0 {
			agent.WriteErr(w, errs.New(409, "traffic.no_tc"))
			return
		}
		agent.WriteJSON(w, 200, map[string]any{"errors": Apply(r.Context(), limits, pidOf)})
	})
}
