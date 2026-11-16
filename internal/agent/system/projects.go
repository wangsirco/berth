package system

// Projects (宝塔/1Panel 的 Node、Python、Go、Java 项目与进程守护): a directory, a start
// command and a port, kept running by a systemd unit Berth writes (start at boot, restart
// on crash). Language versions come from mise, installed under /opt/berth/mise, so each
// project can pin its own Node or Python without touching the system's.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/wangsirco/berth/internal/agent/drift"
	"github.com/wangsirco/berth/internal/sysexec"
)

const (
	miseDir  = "/opt/berth/mise"
	miseBin  = miseDir + "/bin/mise"
	miseData = miseDir + "/data"
)

type ProjectSpec struct {
	Name    string            `json:"name"`
	Kind    string            `json:"kind"`    // node | python | go | java | custom
	Version string            `json:"version"` // mise version for node, python, java ("" = custom/go: none)
	Dir     string            `json:"dir"`
	Command string            `json:"command"`
	Install string            `json:"install,omitempty"` // run once before the first start (npm ci, pip install …)
	User    string            `json:"user"`
	Env     map[string]string `json:"env,omitempty"`
	Port    int               `json:"port,omitempty"`
}

var (
	projNameRe = regexp.MustCompile(`^[a-z][a-z0-9-]{0,30}$`)
	versionRe  = regexp.MustCompile(`^[0-9][0-9a-z.\-]{0,20}$|^lts$|^latest$`)
	envKeyRe   = regexp.MustCompile(`^[A-Z_][A-Z0-9_]{0,63}$`)
)

func UnitName(name string) string { return "berth-app-" + name + ".service" }
func unitPath(name string) string { return "/etc/systemd/system/" + UnitName(name) }

func (s ProjectSpec) validate() error {
	if !projNameRe.MatchString(s.Name) {
		return errors.New("name: lowercase letters, digits and -")
	}
	switch s.Kind {
	case "node", "python", "java":
		if !versionRe.MatchString(s.Version) {
			return errors.New("bad version")
		}
	case "go", "custom":
	default:
		return errors.New("kind must be node, python, go, java or custom")
	}
	if !filepath.IsAbs(s.Dir) || strings.Contains(s.Dir, "..") {
		return errors.New("the directory must be an absolute path")
	}
	if fi, err := os.Stat(s.Dir); err != nil || !fi.IsDir() {
		return errors.New("no such directory: " + s.Dir)
	}
	if strings.TrimSpace(s.Command) == "" || strings.ContainsAny(s.Command+s.Install, "\n\r") {
		return errors.New("the start command must be one line")
	}
	if s.User == "" || s.User == "root" {
		return errors.New("refuse:project_root")
	}
	if _, err := user.Lookup(s.User); err != nil {
		return errors.New("no such user: " + s.User)
	}
	for k, v := range s.Env {
		if !envKeyRe.MatchString(k) || strings.ContainsAny(v, "\n\r\"\\") {
			return fmt.Errorf("bad environment variable %s", k)
		}
	}
	if s.Port < 0 || s.Port > 65535 {
		return errors.New("bad port")
	}
	return nil
}

func (s ProjectSpec) tool() string {
	if s.Version == "" {
		return ""
	}
	return map[string]string{"node": "node", "python": "python", "java": "java"}[s.Kind] + "@" + s.Version
}

// shell wraps a command so it runs with the project's runtime on PATH.
func (s ProjectSpec) shell(cmd string) string {
	if t := s.tool(); t != "" {
		return fmt.Sprintf("exec %s exec %s -- sh -c %s", miseBin, t, strconv.Quote(cmd))
	}
	return "exec " + cmd
}

func (s ProjectSpec) unit() string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Managed by Berth (Sites → Projects). Edits here show up as an external change.\n[Unit]\nDescription=Berth project %s\nAfter=network-online.target docker.service\nWants=network-online.target\n\n[Service]\n", s.Name)
	fmt.Fprintf(&b, "User=%s\nWorkingDirectory=%s\n", s.User, s.Dir)
	b.WriteString("Environment=MISE_DATA_DIR=" + miseData + "\nEnvironment=MISE_YES=1\n")
	if s.Port > 0 {
		fmt.Fprintf(&b, "Environment=PORT=%d\n", s.Port)
	}
	keys := make([]string, 0, len(s.Env))
	for k := range s.Env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(&b, "Environment=\"%s=%s\"\n", k, s.Env[k])
	}
	fmt.Fprintf(&b, "ExecStart=/bin/sh -c %s\nRestart=always\nRestartSec=3\nStartLimitIntervalSec=0\n\n[Install]\nWantedBy=multi-user.target\n", strconv.Quote(s.shell(s.Command)))
	return b.String()
}

// ensureMise installs mise and the runtime a project needs.
func ensureMise(ctx context.Context, tool string, log func(string)) error {
	if tool == "" {
		return nil
	}
	if _, err := os.Stat(miseBin); err != nil {
		log("installing mise (runtime version manager) into " + miseDir)
		os.MkdirAll(miseDir+"/bin", 0o755)
		n, a := sysexec.Scoped("sh", "-c", "curl -fsSL https://mise.run | MISE_INSTALL_PATH="+miseBin+" sh")
		if out, err := exec.CommandContext(ctx, n, a...).CombinedOutput(); err != nil {
			return errors.New("installing mise: " + strings.TrimSpace(string(out)))
		}
	}
	log("mise install " + tool)
	os.MkdirAll(miseData, 0o755)
	n, a := sysexec.Scoped(miseBin, "install", tool)
	cmd := exec.CommandContext(ctx, n, a...)
	cmd.Env = append(os.Environ(), "MISE_DATA_DIR="+miseData, "MISE_YES=1")
	out, err := cmd.CombinedOutput()
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if l != "" {
			log(l)
		}
	}
	if err != nil {
		return errors.New("mise install " + tool + " failed")
	}
	run(ctx, "chmod", "-R", "a+rX", miseData)
	return nil
}

// SetProject installs what the project needs, writes its unit and (re)starts it.
func SetProject(ctx context.Context, s ProjectSpec, log func(string)) error {
	if err := s.validate(); err != nil {
		return err
	}
	if err := ensureMise(ctx, s.tool(), log); err != nil {
		return err
	}
	if s.Install != "" {
		log("$ " + s.Install)
		full := "export MISE_DATA_DIR=" + miseData + " MISE_YES=1; cd " + strconv.Quote(s.Dir) + "; " + strings.TrimPrefix(s.shell(s.Install), "exec ")
		n, a := sysexec.Scoped("runuser", "-u", s.User, "--", "sh", "-c", full)
		out, err := exec.CommandContext(ctx, n, a...).CombinedOutput()
		lines := strings.Split(strings.TrimSpace(string(out)), "\n")
		if len(lines) > 40 {
			lines = lines[len(lines)-40:]
		}
		for _, l := range lines {
			log(l)
		}
		if err != nil {
			return errors.New("the install command failed")
		}
	}
	if err := os.WriteFile(unitPath(s.Name), []byte(s.unit()), 0o644); err != nil {
		return err
	}
	drift.Track(unitPath(s.Name), "project", s.Name)
	run(ctx, "systemctl", "daemon-reload")
	log("systemctl enable --now " + UnitName(s.Name))
	if out, err := run(ctx, "systemctl", "enable", UnitName(s.Name)); err != nil {
		return errors.New(out)
	}
	if out, err := run(ctx, "systemctl", "restart", UnitName(s.Name)); err != nil {
		return errors.New(out)
	}
	return nil
}

func RemoveProject(ctx context.Context, name string) error {
	if !projNameRe.MatchString(name) {
		return errors.New("bad name")
	}
	run(ctx, "systemctl", "disable", "--now", UnitName(name))
	os.Remove(unitPath(name))
	drift.Untrack(unitPath(name))
	run(ctx, "systemctl", "daemon-reload")
	return nil
}

func init() {
	drift.OnRestore("project", func(ctx context.Context, path string) error {
		run(ctx, "systemctl", "daemon-reload")
		_, err := run(ctx, "systemctl", "restart", filepath.Base(path))
		return err
	})
}

// ---- runtimes (运行环境): what mise has installed ----

type Runtime struct {
	Tool    string `json:"tool"`
	Version string `json:"version"`
	Path    string `json:"path"`
	Bytes   int64  `json:"bytes"`
}

var runtimeTools = map[string]bool{"node": true, "python": true, "java": true}

func miseCmd(ctx context.Context, args ...string) *exec.Cmd {
	n, a := sysexec.Scoped(miseBin, args...)
	cmd := exec.CommandContext(ctx, n, a...)
	cmd.Env = append(os.Environ(), "MISE_DATA_DIR="+miseData, "MISE_YES=1")
	return cmd
}

// Runtimes lists installed versions with the space each takes.
func Runtimes(ctx context.Context) ([]Runtime, bool) {
	out := []Runtime{}
	if _, err := os.Stat(miseBin); err != nil {
		return out, false
	}
	cmd := exec.CommandContext(ctx, miseBin, "ls", "--installed", "--json")
	cmd.Env = append(os.Environ(), "MISE_DATA_DIR="+miseData)
	b, err := cmd.Output()
	if err != nil {
		return out, true
	}
	var m map[string][]struct {
		Version     string `json:"version"`
		InstallPath string `json:"install_path"`
		Installed   bool   `json:"installed"`
	}
	json.Unmarshal(b, &m)
	for tool, vs := range m {
		for _, v := range vs {
			if !v.Installed {
				continue
			}
			r := Runtime{Tool: tool, Version: v.Version, Path: v.InstallPath}
			if o, err := run(ctx, "du", "-sb", v.InstallPath); err == nil {
				fmt.Sscan(o, &r.Bytes)
			}
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Tool != out[j].Tool {
			return out[i].Tool < out[j].Tool
		}
		return out[i].Version > out[j].Version
	})
	return out, true
}

// SetRuntime installs (or with remove, uninstalls) tool@version.
func SetRuntime(ctx context.Context, tool, version string, remove bool, log func(string)) error {
	if !runtimeTools[tool] || !versionRe.MatchString(version) {
		return errors.New("tool must be node, python or java, with a version")
	}
	if !remove {
		return ensureMise(ctx, tool+"@"+version, log)
	}
	log("mise uninstall " + tool + "@" + version)
	out, err := miseCmd(ctx, "uninstall", tool+"@"+version).CombinedOutput()
	if err != nil {
		return errors.New(strings.TrimSpace(string(out)))
	}
	return nil
}
