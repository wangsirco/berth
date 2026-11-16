package agent

// Restoring a panel snapshot from the panel itself: the panel cannot replace its own folders
// while it runs, so the agent checks the file and the password, then hands `berth
// restore-panel` to a transient systemd unit that outlives the panel (and, for the main
// panel, the agent) while it stops them, swaps the folders and starts them again.

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/wangsirco/berth/internal/errs"
)

var panelUnitRe = regexp.MustCompile(`^berth-panel(-[a-z0-9]+)?\.service$`)

// unitOf: the systemd service a process runs in, from its cgroup.
func unitOf(pid int) string {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/cgroup")
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		parts := strings.SplitN(line, ":", 3)
		if len(parts) == 3 {
			for _, seg := range strings.Split(parts[2], "/") {
				if strings.HasSuffix(seg, ".service") {
					return seg
				}
			}
		}
	}
	return ""
}

// transientSpec: how to start a transient (systemd-run) unit again — unit, user, group, memory
// limit and command line, base64 so it survives as one argument; "" for a normal unit.
func transientSpec(ctx context.Context, unit string) string {
	out, err := exec.CommandContext(ctx, "systemctl", "show", unit, "-p", "Transient", "-p", "User", "-p", "Group", "-p", "MemoryMax", "-p", "ExecStart").Output()
	if err != nil {
		return ""
	}
	p := map[string]string{}
	for _, l := range strings.Split(string(out), "\n") {
		if k, v, ok := strings.Cut(l, "="); ok {
			p[k] = v
		}
	}
	if p["Transient"] != "yes" {
		return ""
	}
	argv := ""
	if i := strings.Index(p["ExecStart"], "argv[]="); i >= 0 {
		argv = p["ExecStart"][i+len("argv[]="):]
		if j := strings.Index(argv, " ;"); j >= 0 {
			argv = argv[:j]
		}
	}
	if argv == "" {
		return ""
	}
	b, _ := json.Marshal(map[string]any{"unit": strings.TrimSuffix(unit, ".service"), "user": p["User"], "group": p["Group"], "mem": p["MemoryMax"], "argv": strings.Fields(argv)})
	return base64.StdEncoding.EncodeToString(b)
}

func registerPanelRestore(s *Server) {
	s.Handle("POST /v1/panel/restore", func(w http.ResponseWriter, r *http.Request, body []byte) {
		var in struct {
			File     string `json:"file"`
			Password string `json:"password"`
			DataDir  string `json:"data_dir"`
			EtcDir   string `json:"etc_dir"`
			PID      int    `json:"pid"`
			Check    bool   `json:"check"` // only check the file and the password
		}
		json.Unmarshal(body, &in)
		file := filepath.Clean(in.File)
		if !snapDataRe.MatchString(in.DataDir) || !snapEtcRe.MatchString(in.EtcDir) || filepath.Dir(file) != filepath.Join(in.DataDir, "uploads") ||
			!strings.HasSuffix(file, ".berthsnap") || len(in.Password) < 8 {
			WriteErr(w, errs.New(400, "params.invalid_json"))
			return
		}
		if _, err := os.Stat(file); err != nil {
			WriteErr(w, errs.New(404, "files.not_found", "path", file))
			return
		}
		unit := unitOf(in.PID)
		if !panelUnitRe.MatchString(unit) {
			WriteErr(w, errs.New(409, "snapshot.unknown_panel_unit", "unit", unit))
			return
		}
		units := []string{strings.TrimSuffix(unit, ".service")}
		if in.DataDir == "/var/lib/berth" {
			units = append(units, "berth-agent") // the main panel's folders hold the agent's keys too
		}
		exe, err := os.Executable()
		if err != nil {
			WriteErr(w, err)
			return
		}
		args := []string{"restore-panel", "--etc", in.EtcDir, "--data", in.DataDir, "--units", strings.Join(units, ",")}
		// a panel started with systemd-run is gone once stopped: start it again the same way
		if spec := transientSpec(r.Context(), unit); spec != "" {
			args = append(args, "--rerun", spec)
		}
		// check first, here, so a wrong password or a damaged file is reported to the person
		chk := exec.CommandContext(r.Context(), exe, append(append([]string{}, args...), "--check", file)...)
		chk.Env = append(os.Environ(), "BERTH_SNAPSHOT_PASSWORD="+in.Password)
		out, err := chk.CombinedOutput()
		if err != nil {
			WriteErr(w, errs.New(400, "snapshot.check_failed", "detail", strings.TrimSpace(string(out))))
			return
		}
		if in.Check {
			WriteJSON(w, 200, map[string]any{"ok": true, "detail": strings.TrimSpace(string(out)), "units": units})
			return
		}
		rnd := make([]byte, 6)
		rand.Read(rnd)
		pw := "/run/berth-restore-" + hex.EncodeToString(rnd) + ".pw"
		if err := os.WriteFile(pw, []byte(in.Password), 0o600); err != nil {
			WriteErr(w, err)
			return
		}
		name := "berth-restore-" + time.Now().Format("20060102-150405")
		cmd := append([]string{"--unit", name, "--collect", "--no-block", "--property=Type=oneshot", "--", exe}, args...)
		run := exec.Command("systemd-run", append(cmd, "--password-file", pw, file)...)
		if out, err := run.CombinedOutput(); err != nil {
			os.Remove(pw)
			WriteErr(w, errs.New(500, "snapshot.restore_start_failed", "detail", strings.TrimSpace(string(out))))
			return
		}
		WriteJSON(w, 200, map[string]any{"unit": name, "units": units, "kept": []string{in.EtcDir + ".before-restore-*", in.DataDir + ".before-restore-*"}})
	})
}
