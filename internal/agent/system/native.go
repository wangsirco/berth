package system

// Native projects with releases (原生项目, R1): each deploy builds a complete release next to
// the running one, starts it on the project's other port, checks it answers, and only then is
// it switched to; the old one keeps serving until the panel has moved the site over. Each
// project runs as its own system user in a systemd sandbox that can write only its shared
// folder. Runtimes come from mise and are shared between projects.

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/wangsirco/berth/internal/errs"
)

const (
	NativeRoot   = "/srv/berth/projects"
	nativeEnvDir = "/etc/berth/projects"
	keepReleases = 5
)

type NativeSource struct {
	Type   string `json:"type"`             // dir | git
	Path   string `json:"path,omitempty"`   // dir: a folder on this host
	URL    string `json:"url,omitempty"`    // git
	Branch string `json:"branch,omitempty"` // git (default main)
}

type NativeSpec struct {
	Name       string         `json:"name"`
	Kind       string         `json:"kind"`    // node (R1)
	Version    string         `json:"version"` // Node version or major
	Source     NativeSource   `json:"source"`
	Install    string         `json:"install"`
	Build      string         `json:"build,omitempty"`
	Release    string         `json:"release,omitempty"` // runs in the new release before it starts (migrations)
	Start      string         `json:"start"`
	Health     string         `json:"health"`           // path checked before switching
	Shared     []string       `json:"shared"`           // kept across releases, writable
	MemMB      int            `json:"mem_mb,omitempty"` // runtime limit
	CPUs       float64        `json:"cpus,omitempty"`   // runtime limit
	BuildMemMB int            `json:"build_mem_mb,omitempty"`
	WaitSecs   int            `json:"wait_secs,omitempty"` // how long the new release has to answer
	Workers    []NativeWorker `json:"workers,omitempty"`   // long-running commands beside the web process
}

var (
	nativeNameRe = regexp.MustCompile(`^[a-z][a-z0-9-]{0,25}$`)
	sharedRe     = regexp.MustCompile(`^[A-Za-z0-9_.@-][A-Za-z0-9_.@/-]{0,120}$`)
	gitURLRe     = regexp.MustCompile(`^(https://[^\s'"]+|git@[A-Za-z0-9.-]+:[^\s'"]+)$`)
	branchRe     = regexp.MustCompile(`^[A-Za-z0-9._/-]{1,100}$`)
	releaseIDRe  = regexp.MustCompile(`^[0-9]{8}-[0-9]{6}$`)
)

func nativeBase(name string) string { return filepath.Join(NativeRoot, name) }
func nativeUser(name string) string { return "site-" + name }
func nativeUnit(name string) string { return "berth-proj-" + name + "@.service" }
func nativeInst(name string, port int) string {
	return "berth-proj-" + name + "@" + strconv.Itoa(port) + ".service"
}
func nativeEnvFile(name string) string { return filepath.Join(nativeEnvDir, name+".env") }

// NativePorts: two ports per project in 20000–29997, stable for a name.
func NativePorts(name string) [2]int {
	h := fnv.New32a()
	h.Write([]byte(name))
	p := 20000 + int(h.Sum32()%4999)*2
	return [2]int{p, p + 1}
}

func (s *NativeSpec) Validate() error {
	if !nativeNameRe.MatchString(s.Name) {
		return errs.New(400, "native.bad_name")
	}
	if s.Kind != "node" && s.Kind != "java" && s.Kind != "go" && s.Kind != "python" {
		return errs.New(400, "native.bad_kind", "kind", s.Kind)
	}
	if !versionRe.MatchString(s.Version) {
		return errs.New(400, "native.bad_version")
	}
	switch s.Source.Type {
	case "dir":
		if !filepath.IsAbs(s.Source.Path) || strings.Contains(s.Source.Path, "..") || strings.HasPrefix(filepath.Clean(s.Source.Path), NativeRoot) {
			return errs.New(400, "native.bad_source")
		}
	case "git":
		if !gitURLRe.MatchString(s.Source.URL) {
			return errs.New(400, "native.bad_source")
		}
		if s.Source.Branch == "" {
			s.Source.Branch = "main"
		}
		if !branchRe.MatchString(s.Source.Branch) || strings.Contains(s.Source.Branch, "..") {
			return errs.New(400, "native.bad_source")
		}
	default:
		return errs.New(400, "native.bad_source")
	}
	for _, c := range []string{s.Install, s.Build, s.Release, s.Start} {
		if strings.ContainsAny(c, "\n\r") {
			return errs.New(400, "native.bad_command")
		}
	}
	if strings.TrimSpace(s.Start) == "" {
		return errs.New(400, "native.bad_command")
	}
	if s.Health == "" {
		s.Health = "/"
	}
	if !strings.HasPrefix(s.Health, "/") || strings.ContainsAny(s.Health, " \n") {
		return errs.New(400, "native.bad_health")
	}
	for _, p := range s.Shared {
		if !sharedRe.MatchString(p) || strings.Contains(p, "..") || strings.HasPrefix(p, "/") {
			return errs.New(400, "native.bad_shared", "path", p)
		}
	}
	if s.MemMB < 0 || s.CPUs < 0 || s.BuildMemMB < 0 || s.WaitSecs < 0 || s.WaitSecs > 1800 {
		return errs.New(400, "native.bad_limits")
	}
	return validateWorkers(s.Workers)
}

// miseTool names the mise tool for a project's runtime: a bare Java major means Temurin.
func miseTool(kind, version string) string {
	switch kind {
	case "java":
		if regexp.MustCompile(`^[0-9]+$`).MatchString(version) {
			return "java@temurin-" + version
		}
		return "java@" + version
	case "go", "python":
		return kind + "@" + version
	}
	return "node@" + version
}

// toolHome is where mise put a tool (its bin/ goes on PATH; mise itself is not needed at run
// time, so the sandbox can keep mise's folders read-only).
func toolHome(ctx context.Context, tool string) (string, error) {
	cmd := miseCmd(ctx, "where", tool)
	out, err := cmd.Output()
	p := strings.TrimSpace(string(out))
	if err != nil || p == "" {
		return "", errors.New(strings.Replace(tool, "@", " ", 1) + " is not installed")
	}
	return p, nil
}

// runtimeHome finds the project's runtime, installing it when missing.
func runtimeHome(ctx context.Context, s NativeSpec, log func(string)) (string, error) {
	tool := miseTool(s.Kind, s.Version)
	if p, err := toolHome(ctx, tool); err == nil {
		return p, nil
	}
	if err := ensureMise(ctx, tool, log); err != nil {
		return "", err
	}
	return toolHome(ctx, tool)
}

func nativePath(home string) string {
	return home + "/bin:/usr/local/bin:/usr/bin:/bin"
}

// langEnv is what each language adds to the service's environment (dir is the release folder).
func langEnv(kind, home, dir string) []string {
	switch kind {
	case "java":
		// Java takes its home from the passwd entry (the read-only project folder), not $HOME, so
		// Maven's ~/.m2, Gradle's ~/.gradle and the app's own ~ would land there; point it at
		// the shared home instead.
		return []string{"JAVA_HOME=" + home, "SERVER_PORT=$PORT", "JAVA_TOOL_OPTIONS=-XX:MaxRAMPercentage=75.0 -Duser.home=" + javaHome(dir)}
	case "python":
		return []string{"VIRTUAL_ENV=" + dir + "/.venv", "PYTHONUNBUFFERED=1", "PYTHONDONTWRITEBYTECODE=1"}
	case "go":
		return []string{"GIN_MODE=release"}
	}
	return []string{"NODE_ENV=production"}
}

// javaHome is the shared home seen from a release folder dir (…/run-%i or …/releases/<id>).
func javaHome(dir string) string {
	base := filepath.Dir(dir)
	if filepath.Base(base) == "releases" {
		base = filepath.Dir(base)
	}
	return base + "/shared/.home"
}

func runPath(kind, home, dir string) string {
	if kind == "python" {
		return dir + "/.venv/bin:" + nativePath(home)
	}
	return nativePath(home)
}

// unitText is the template unit; the instance name is the port.
func (s NativeSpec) unitText(home string) string {
	base := nativeBase(s.Name)
	run := base + "/run-%i"
	var b strings.Builder
	fmt.Fprintf(&b, "# Managed by Berth (native project %s). Edits here show up as an external change.\n", s.Name)
	fmt.Fprintf(&b, "[Unit]\nDescription=Berth project %s (port %%i)\nAfter=network-online.target\nWants=network-online.target\nStartLimitIntervalSec=0\n\n[Service]\n", s.Name)
	fmt.Fprintf(&b, "User=%s\nGroup=%s\nWorkingDirectory=%s\n", nativeUser(s.Name), nativeUser(s.Name), run)
	fmt.Fprintf(&b, "Environment=PORT=%%i\nEnvironment=HOSTNAME=0.0.0.0\nEnvironment=HOST=0.0.0.0\n")
	for _, e := range langEnv(s.Kind, home, run) {
		fmt.Fprintf(&b, "Environment=%s\n", strings.Replace(e, "$PORT", "%i", 1))
	}
	fmt.Fprintf(&b, "Environment=PATH=%s\nEnvironment=HOME=%s/shared/.home\n", runPath(s.Kind, home, run), base)
	fmt.Fprintf(&b, "EnvironmentFile=-%s\n", nativeEnvFile(s.Name))
	fmt.Fprintf(&b, "ExecStart=/bin/sh -c %s\n", strconv.Quote("exec "+s.Start))
	b.WriteString("Restart=always\nRestartSec=3\nTimeoutStopSec=20\n")
	// the sandbox: read-only system and code, writable shared folder only
	// releases/ is writable only where the project's user owns something there (.next for
	// frameworks that rewrite pages at run time); the code itself belongs to root
	fmt.Fprintf(&b, "ProtectSystem=strict\nReadWritePaths=%s/shared %s/releases\n", base, base)
	b.WriteString("PrivateTmp=yes\nProtectHome=yes\nNoNewPrivileges=yes\nRestrictSUIDSGID=yes\nProtectKernelTunables=yes\nProtectKernelModules=yes\nProtectKernelLogs=yes\nProtectControlGroups=yes\nProtectClock=yes\nRestrictNamespaces=yes\nLockPersonality=yes\nRestrictRealtime=yes\nPrivateDevices=yes\n")
	if s.MemMB > 0 {
		fmt.Fprintf(&b, "MemoryMax=%dM\n", s.MemMB)
	}
	if s.CPUs > 0 {
		fmt.Fprintf(&b, "CPUQuota=%d%%\n", int(s.CPUs*100))
	}
	b.WriteString("\n[Install]\nWantedBy=multi-user.target\n")
	return b.String()
}

type nativeState struct {
	Release string `json:"release"`
	Port    int    `json:"port"`
}

func readNativeState(name string) nativeState {
	var st nativeState
	b, _ := os.ReadFile(filepath.Join(nativeBase(name), "state.json"))
	json.Unmarshal(b, &st)
	return st
}

func writeNativeState(name string, st nativeState) error {
	b, _ := json.Marshal(st)
	tmp := filepath.Join(nativeBase(name), "state.json.new")
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(nativeBase(name), "state.json"))
}

// relink points link at target atomically.
func relink(target, link string) error {
	tmp := link + ".new"
	os.Remove(tmp)
	if err := os.Symlink(target, tmp); err != nil {
		return err
	}
	return os.Rename(tmp, link)
}

func ensureNativeUser(ctx context.Context, name string) (uid, gid int, err error) {
	u := nativeUser(name)
	if _, e := user.Lookup(u); e != nil {
		if out, e := run(ctx, "useradd", "--system", "--home-dir", nativeBase(name), "--no-create-home", "--shell", "/sbin/nologin", "--comment", "Berth project "+name, u); e != nil {
			return 0, 0, errors.New("useradd: " + out)
		}
	}
	uu, e := user.Lookup(u)
	if e != nil {
		return 0, 0, e
	}
	uid, _ = strconv.Atoi(uu.Uid)
	gid, _ = strconv.Atoi(uu.Gid)
	return uid, gid, nil
}

// asUser runs a shell line as the project's user in a memory-limited scope, streaming its output.
// The environment (secrets included) goes in the process environment, never on the command
// line: systemd logs a scope's command line to the journal.
func asUser(ctx context.Context, name, dir, line string, env []string, memMB int, log func(string)) error {
	return asUserNamed(ctx, nativeUser(name), dir, line, env, memMB, log)
}

func asUserNamed(ctx context.Context, username, dir, line string, env []string, memMB int, log func(string)) error {
	u, err := user.Lookup(username)
	if err != nil {
		return err
	}
	args := []string{"--scope", "--quiet", "--collect"}
	if memMB > 0 {
		args = append(args, "-p", fmt.Sprintf("MemoryMax=%dM", memMB))
	}
	args = append(args, "setpriv", "--reuid="+u.Uid, "--regid="+u.Gid, "--clear-groups", "--", "sh", "-c", "cd "+strconv.Quote(dir)+" && "+line)
	cmd := exec.CommandContext(ctx, "systemd-run", args...)
	cmd.Env = env
	pr, pw := io.Pipe()
	cmd.Stdout, cmd.Stderr = pw, pw
	if err := cmd.Start(); err != nil {
		return err
	}
	done := make(chan struct{})
	var tail []string
	go func() {
		sc := bufio.NewScanner(pr)
		sc.Buffer(make([]byte, 64<<10), 1<<20)
		n := 0
		for sc.Scan() {
			l := sc.Text()
			if tail = append(tail, l); len(tail) > 8 {
				tail = tail[1:]
			}
			if n < 2000 {
				log(l)
				n++
			}
		}
		close(done)
	}()
	err = cmd.Wait()
	pw.Close()
	<-done
	if err != nil {
		return errors.New(strings.Join(tail, " | "))
	}
	return nil
}

// buildEnv is the environment of install, build and release commands in release folder dir.
// Caches (npm, ~/.m2, ~/.gradle, Go's module and build caches, pip) live under the shared
// home, so the next release builds faster. tools are extra mise tools (maven, gradle).
func buildEnv(s NativeSpec, home, dir string, tools []string, secrets map[string]string) []string {
	base := nativeBase(s.Name)
	path := runPath(s.Kind, home, dir)
	for _, t := range tools {
		path = t + "/bin:" + path
	}
	env := []string{
		"PATH=" + path, "HOME=" + base + "/shared/.home", "npm_config_cache=" + base + "/shared/.npm",
		"COREPACK_HOME=" + base + "/shared/.corepack", "COREPACK_ENABLE_DOWNLOAD_PROMPT=0", "NEXT_TELEMETRY_DISABLED=1", "LANG=C.UTF-8",
	}
	for _, e := range langEnv(s.Kind, home, dir) {
		switch {
		case strings.HasPrefix(e, "SERVER_PORT="):
		case strings.HasPrefix(e, "JAVA_TOOL_OPTIONS="):
			env = append(env, "JAVA_TOOL_OPTIONS=-Duser.home="+javaHome(dir), "MAVEN_OPTS=-Dmaven.repo.local="+javaHome(dir)+"/.m2/repository", "GRADLE_USER_HOME="+javaHome(dir)+"/.gradle")
		default:
			env = append(env, e)
		}
	}
	if s.Kind == "go" {
		env = append(env, "GOFLAGS=-buildvcs=false")
	}
	keys := make([]string, 0, len(secrets))
	for k := range secrets {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		env = append(env, k+"="+secrets[k])
	}
	return env
}

func writeEnvFile(name string, secrets map[string]string) error {
	if err := os.MkdirAll(nativeEnvDir, 0o700); err != nil {
		return err
	}
	keys := make([]string, 0, len(secrets))
	for k := range secrets {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString("# Managed by Berth: the project's environment (secrets). Edit it in the panel.\n")
	for _, k := range keys {
		if !envKeyRe.MatchString(k) {
			return errs.New(400, "native.bad_env", "key", k)
		}
		v := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`).Replace(secrets[k])
		fmt.Fprintf(&b, "%s=\"%s\"\n", k, v)
	}
	tmp := nativeEnvFile(name) + ".new"
	if err := os.WriteFile(tmp, []byte(b.String()), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, nativeEnvFile(name))
}

// fetchSource puts the project's code into dst.
func fetchSource(ctx context.Context, s NativeSpec, dst string, log func(string)) (map[string]string, error) {
	info := map[string]string{"source": s.Source.Type}
	switch s.Source.Type {
	case "dir":
		if fi, err := os.Stat(s.Source.Path); err != nil || !fi.IsDir() {
			return nil, errs.New(404, "native.no_source", "path", s.Source.Path)
		}
		// build output, caches and local secrets are never part of a release
		args := []string{"-a", "--delete"}
		for _, x := range []string{"node_modules", ".next*", ".nuxt", ".output", ".turbo", ".cache", ".git", ".env", ".env.local", ".env.*.local", ".berth", "*.log", "tsconfig.tsbuildinfo"} {
			args = append(args, "--exclude", x)
		}
		if _, err := os.Stat(filepath.Join(s.Source.Path, ".berthignore")); err == nil {
			args = append(args, "--exclude-from", filepath.Join(s.Source.Path, ".berthignore"))
		}
		for _, p := range s.Shared {
			args = append(args, "--exclude", "/"+strings.TrimSuffix(p, "/"))
		}
		args = append(args, strings.TrimSuffix(s.Source.Path, "/")+"/", dst+"/")
		log("rsync " + s.Source.Path + " → " + dst)
		if out, err := exec.CommandContext(ctx, "rsync", args...).CombinedOutput(); err != nil {
			return nil, errors.New("rsync: " + strings.TrimSpace(string(out)))
		}
		info["path"] = s.Source.Path
	case "git":
		repo := filepath.Join(nativeBase(s.Name), "repo")
		git := func(args ...string) (string, error) {
			cmd := exec.CommandContext(ctx, "git", args...)
			cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_SSH_COMMAND=ssh -o StrictHostKeyChecking=accept-new -o BatchMode=yes")
			out, err := cmd.CombinedOutput()
			return strings.TrimSpace(string(out)), err
		}
		if _, err := os.Stat(filepath.Join(repo, ".git")); err != nil {
			log("git clone " + s.Source.URL + " (" + s.Source.Branch + ")")
			os.RemoveAll(repo)
			if out, err := git("clone", "--branch", s.Source.Branch, "--single-branch", s.Source.URL, repo); err != nil {
				return nil, errs.New(502, "native.git_failed", "detail", out)
			}
		} else {
			log("git fetch " + s.Source.Branch)
			if out, err := git("-C", repo, "fetch", "--prune", "origin", s.Source.Branch); err != nil {
				return nil, errs.New(502, "native.git_failed", "detail", out)
			}
			if out, err := git("-C", repo, "reset", "--hard", "FETCH_HEAD"); err != nil {
				return nil, errs.New(502, "native.git_failed", "detail", out)
			}
		}
		sha, _ := git("-C", repo, "rev-parse", "--short", "HEAD")
		msg, _ := git("-C", repo, "log", "-1", "--format=%s")
		info["commit"], info["message"] = sha, msg
		log("commit " + sha + " " + msg)
		if out, err := exec.CommandContext(ctx, "sh", "-c", "git -C "+strconv.Quote(repo)+" archive HEAD | tar -x -C "+strconv.Quote(dst)).CombinedOutput(); err != nil {
			return nil, errors.New("git archive: " + strings.TrimSpace(string(out)))
		}
	}
	return info, nil
}

func installedNodes(ctx context.Context) []string { return installedTool(ctx, "node") }

func installedTool(ctx context.Context, tool string) []string {
	var out []string
	cmd := exec.CommandContext(ctx, miseBin, "ls", "--installed", "--json", tool)
	cmd.Env = append(os.Environ(), "MISE_DATA_DIR="+miseData)
	b, err := cmd.Output()
	if err != nil {
		return nil
	}
	var list []struct{ Version string }
	json.Unmarshal(b, &list)
	for _, x := range list {
		out = append(out, x.Version)
	}
	return out
}

func memFreeMB() int {
	b, _ := os.ReadFile("/proc/meminfo")
	var avail, swap int
	for _, l := range strings.Split(string(b), "\n") {
		f := strings.Fields(l)
		if len(f) >= 2 {
			n, _ := strconv.Atoi(f[1])
			switch f[0] {
			case "MemAvailable:":
				avail = n
			case "SwapFree:":
				swap = n
			}
		}
	}
	return (avail + swap) / 1024
}

func buildTools() map[string]bool {
	have := map[string]bool{}
	for _, t := range []string{"gcc", "g++", "make", "python3"} {
		_, err := exec.LookPath(t)
		have[t] = err == nil
	}
	return have
}

// DetectKind guesses a project's language from its files.
func DetectKind(dir string) string {
	has := func(f string) bool { _, err := os.Stat(filepath.Join(dir, f)); return err == nil }
	switch {
	case has("package.json"):
		return "node"
	case has("pom.xml") || has("build.gradle") || has("build.gradle.kts"):
		return "java"
	case has("go.mod"):
		return "go"
	case has("requirements.txt") || has("pyproject.toml") || has("manage.py"):
		return "python"
	}
	if m, _ := filepath.Glob(filepath.Join(dir, "*.jar")); len(m) > 0 {
		return "java"
	}
	return ""
}

// CheckDir runs the compatibility check on a project folder; kind "" means detect it.
func CheckDir(ctx context.Context, dir, kind string) (NodeReport, error) {
	if kind == "" {
		if kind = DetectKind(dir); kind == "" {
			return NodeReport{Findings: []Finding{{Level: "block", Code: "no_project"}}}, nil
		}
	}
	read := func(names ...string) map[string][]byte {
		m := map[string][]byte{}
		for _, f := range names {
			if b, err := os.ReadFile(filepath.Join(dir, f)); err == nil {
				m[f] = b
			}
		}
		return m
	}
	switch kind {
	case "java":
		have := map[string]bool{}
		for _, f := range []string{"mvnw", "gradlew"} {
			if _, err := os.Stat(filepath.Join(dir, f)); err == nil {
				have[f] = true
			}
		}
		jars, _ := filepath.Glob(filepath.Join(dir, "*.jar"))
		for _, j := range jars {
			have[filepath.Base(j)] = true
		}
		return AnalyzeJava(read("pom.xml", "build.gradle", "build.gradle.kts"), have, memFreeMB()), nil
	case "go":
		mod, err := os.ReadFile(filepath.Join(dir, "go.mod"))
		if err != nil {
			return NodeReport{Kind: "go", Findings: []Finding{{Level: "block", Code: "no_go_mod"}}}, nil
		}
		var cmds []string
		ents, _ := os.ReadDir(filepath.Join(dir, "cmd"))
		for _, e := range ents {
			if e.IsDir() && goMainIn(filepath.Join(dir, "cmd", e.Name())) {
				cmds = append(cmds, e.Name())
			}
		}
		return AnalyzeGo(mod, goMainIn(dir), cmds), nil
	case "python":
		names := []string{"requirements.txt", "pyproject.toml", ".python-version", "manage.py"}
		if py, _ := filepath.Glob(filepath.Join(dir, "*.py")); len(py) > 0 {
			for _, f := range py {
				names = append(names, filepath.Base(f))
			}
		}
		files := read(names...)
		if len(files) == 0 {
			return NodeReport{Kind: "python", Findings: []Finding{{Level: "block", Code: "no_python"}}}, nil
		}
		return AnalyzePython(files, installedTool(ctx, "python"), buildTools(), memFreeMB()), nil
	}
	pkg, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if err != nil {
		return NodeReport{Kind: "node", Findings: []Finding{{Level: "block", Code: "no_package_json"}}}, nil
	}
	return AnalyzeNode(pkg, read("package-lock.json", "pnpm-lock.yaml", "yarn.lock"), installedNodes(ctx), memFreeMB(), buildTools()), nil
}

// goMainIn says whether a folder holds Go files of package main.
func goMainIn(dir string) bool {
	fs, _ := filepath.Glob(filepath.Join(dir, "*.go"))
	for _, f := range fs {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, _ := os.ReadFile(f)
		if regexp.MustCompile(`(?m)^package\s+main\b`).Match(b) {
			return true
		}
	}
	return false
}

// NativeCheck fetches the source into a scratch folder and checks it.
func NativeCheck(ctx context.Context, s NativeSpec) (NodeReport, error) {
	kind := s.Kind // "" means detect
	v := s
	if v.Kind == "" {
		v.Kind = "node"
	}
	if v.Version == "" {
		v.Version = "lts"
	}
	if v.Start == "" {
		v.Start = "-"
	}
	if err := v.Validate(); err != nil {
		return NodeReport{}, err
	}
	s.Source = v.Source
	if s.Source.Type == "dir" {
		return CheckDir(ctx, s.Source.Path, kind)
	}
	os.MkdirAll(nativeBase(s.Name), 0o755)
	tmp, err := os.MkdirTemp(nativeBase(s.Name), "check-")
	if err != nil {
		return NodeReport{}, err
	}
	defer os.RemoveAll(tmp)
	if _, err := fetchSource(ctx, s, tmp, func(string) {}); err != nil {
		return NodeReport{}, err
	}
	return CheckDir(ctx, tmp, kind)
}

func healthy(ctx context.Context, port int, path string, wait time.Duration) (int, error) {
	hc := &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	deadline := time.Now().Add(wait)
	var last error
	for time.Now().Before(deadline) {
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}
		resp, err := hc.Get("http://127.0.0.1:" + strconv.Itoa(port) + path)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode < 500 {
				return resp.StatusCode, nil
			}
			last = fmt.Errorf("HTTP %d", resp.StatusCode)
		} else {
			last = err
		}
		time.Sleep(2 * time.Second)
	}
	return 0, fmt.Errorf("no healthy answer on port %d%s within %s (%v)", port, path, wait, last)
}

func nativeActive(ctx context.Context, name string, port int) bool {
	out, _ := run(ctx, "systemctl", "is-active", nativeInst(name, port))
	return strings.TrimSpace(out) == "active"
}

// startOn runs release rel on port and waits for it to answer.
func startOn(ctx context.Context, s NativeSpec, rel string, port int, log func(string)) error {
	base := nativeBase(s.Name)
	if err := relink(rel, filepath.Join(base, "run-"+strconv.Itoa(port))); err != nil {
		return err
	}
	inst := nativeInst(s.Name, port)
	run(ctx, "systemctl", "stop", inst)
	log("systemctl start " + inst)
	if out, err := run(ctx, "systemctl", "start", inst); err != nil {
		return errors.New(out)
	}
	wait := time.Duration(s.WaitSecs) * time.Second
	if wait == 0 {
		wait = 120 * time.Second
	}
	log(fmt.Sprintf("waiting for http://127.0.0.1:%d%s", port, s.Health))
	code, err := healthy(ctx, port, s.Health, wait)
	if err != nil {
		j, _ := run(ctx, "journalctl", "-u", inst, "-n", "25", "--no-pager", "-o", "cat")
		for _, l := range strings.Split(j, "\n") {
			log("│ " + l)
		}
		run(ctx, "systemctl", "stop", inst)
		return errs.New(500, "native.unhealthy", "detail", err.Error())
	}
	log(fmt.Sprintf("answered HTTP %d", code))
	return nil
}

func ensureNativeFirewall(ctx context.Context) {
	applyNftTable(ctx, "berth_proj", `	chain input {
		type filter hook input priority -5; policy accept;
		iifname "lo" accept
		iifname "docker0" accept
		iifname "br-*" accept
		iifname "podman*" accept
		tcp dport 20000-29999 drop
	}
`)
}

func dropNativeFirewallIfUnused(ctx context.Context) {
	m, _ := filepath.Glob("/etc/systemd/system/berth-proj-*@.service")
	p, _ := filepath.Glob("/etc/systemd/system/berth-php-*.service")
	if len(m)+len(p) == 0 {
		dropNftTable(ctx, "berth_proj")
	}
}

type DeployResult struct {
	Release string            `json:"release"`
	Port    int               `json:"port"`
	OldPort int               `json:"old_port,omitempty"`
	Info    map[string]string `json:"info,omitempty"`
	Report  NodeReport        `json:"report"`
}

// NativeDeploy builds a new release and starts it on the free port. The old instance keeps
// running; NativeRetire stops it once traffic has moved.
func NativeDeploy(ctx context.Context, s NativeSpec, secrets map[string]string, log func(string)) (DeployResult, error) {
	var res DeployResult
	if err := s.Validate(); err != nil {
		return res, err
	}
	base := nativeBase(s.Name)
	uid, gid, err := ensureNativeUser(ctx, s.Name)
	if err != nil {
		return res, err
	}
	for _, d := range []string{base, base + "/releases", base + "/shared", base + "/shared/.home", base + "/shared/.npm", base + "/shared/.corepack"} {
		os.MkdirAll(d, 0o755)
	}
	os.Chmod(base, 0o755)
	os.Chown(base+"/releases", 0, 0)
	for _, d := range []string{base + "/shared", base + "/shared/.home", base + "/shared/.npm", base + "/shared/.corepack"} {
		os.Chown(d, uid, gid)
	}
	id := time.Now().Format("20060102-150405")
	rel := filepath.Join(base, "releases", id)
	if err := os.MkdirAll(rel, 0o755); err != nil {
		return res, err
	}
	ok := false
	defer func() {
		if !ok {
			os.RemoveAll(rel)
		}
	}()
	log("::step 0 fetch")
	info, err := fetchSource(ctx, s, rel, log)
	if err != nil {
		return res, err
	}
	exec.CommandContext(ctx, "chown", "-R", fmt.Sprintf("%d:%d", uid, gid), rel).Run()

	log("::step 1 check")
	rep, _ := CheckDir(ctx, rel, s.Kind)
	res.Report = rep
	for _, f := range rep.Findings {
		log(fmt.Sprintf("[%s] %s %v", f.Level, f.Code, f.Params))
		if f.Level == "block" {
			return res, errs.New(409, "native.blocked", "code", f.Code)
		}
	}
	home, err := runtimeHome(ctx, s, log)
	if err != nil {
		return res, err
	}
	var tools []string
	for _, t := range rep.Tools {
		if s.Kind == "java" && !strings.Contains(s.Build, strings.SplitN(t, "@", 2)[0]) {
			continue // the build uses the project's wrapper, or the user changed it
		}
		h, err := toolHome(ctx, t)
		if err != nil {
			if err := ensureMise(ctx, t, log); err != nil {
				return res, err
			}
			if h, err = toolHome(ctx, t); err != nil {
				return res, err
			}
		}
		tools = append(tools, h)
	}
	env := buildEnv(s, home, rel, tools, secrets)

	log("::step 2 build")
	buildMem := s.BuildMemMB
	if buildMem == 0 {
		buildMem = 3072
	}
	if s.Install != "" {
		log("$ " + s.Install)
		if err := asUser(ctx, s.Name, rel, s.Install, env, buildMem, log); err != nil {
			return res, errs.New(500, "native.install_failed", "detail", err.Error())
		}
	}
	if s.Build != "" {
		log("$ " + s.Build)
		if err := asUser(ctx, s.Name, rel, s.Build, env, buildMem, log); err != nil {
			return res, errs.New(500, "native.build_failed", "detail", err.Error())
		}
	}

	// shared folders: seeded from the release the first time, then linked
	for _, p := range s.Shared {
		src, dst := filepath.Join(rel, p), filepath.Join(base, "shared", p)
		if _, err := os.Stat(dst); err != nil {
			os.MkdirAll(filepath.Dir(dst), 0o755)
			if fi, e := os.Stat(src); e == nil && fi.IsDir() {
				exec.CommandContext(ctx, "cp", "-a", src, dst).Run()
			} else {
				os.MkdirAll(dst, 0o755)
			}
			exec.CommandContext(ctx, "chown", "-R", fmt.Sprintf("%d:%d", uid, gid), filepath.Join(base, "shared", strings.SplitN(p, "/", 2)[0])).Run()
		}
		os.RemoveAll(src)
		os.MkdirAll(filepath.Dir(src), 0o755)
		os.Symlink(dst, src)
	}
	meta, _ := json.Marshal(map[string]any{"at": time.Now().Unix(), "info": info, "node": s.Version, "kind": s.Kind})
	os.WriteFile(filepath.Join(rel, ".berth-release.json"), meta, 0o644)
	// the code is not the running site's to change
	exec.CommandContext(ctx, "chown", "-R", "root:root", rel).Run()
	exec.CommandContext(ctx, "chmod", "-R", "a+rX,go-w", rel).Run()
	for _, p := range s.Shared {
		os.Lchown(filepath.Join(rel, p), 0, 0)
	}
	if fi, err := os.Stat(filepath.Join(rel, ".next")); err == nil && fi.IsDir() {
		exec.CommandContext(ctx, "chown", "-R", "--no-dereference", fmt.Sprintf("%d:%d", uid, gid), filepath.Join(rel, ".next")).Run()
	}

	if s.Release != "" {
		log("::step 3 release")
		log("$ " + s.Release)
		// the release command (migrations) writes to shared data, so it runs as the project's user
		exec.CommandContext(ctx, "chown", "-R", fmt.Sprintf("%d:%d", uid, gid), filepath.Join(base, "shared")).Run()
		if err := asUser(ctx, s.Name, rel, s.Release, env, buildMem, log); err != nil {
			return res, errs.New(500, "native.release_failed", "detail", err.Error())
		}
	}

	log("::step 4 start")
	if err := writeEnvFile(s.Name, secrets); err != nil {
		return res, err
	}
	if err := os.WriteFile(filepath.Join("/etc/systemd/system", nativeUnit(s.Name)), []byte(s.unitText(home)), 0o644); err != nil {
		return res, err
	}
	run(ctx, "systemctl", "daemon-reload")
	ensureNativeFirewall(ctx)
	ports := NativePorts(s.Name)
	st := readNativeState(s.Name)
	port := ports[0]
	if st.Port == ports[0] {
		port = ports[1]
	}
	if err := startOn(ctx, s, rel, port, log); err != nil {
		return res, err
	}
	if err := relink(rel, filepath.Join(base, "current")); err != nil {
		return res, err
	}
	run(ctx, "systemctl", "enable", nativeInst(s.Name, port))
	if err := writeNativeState(s.Name, nativeState{Release: id, Port: port}); err != nil {
		return res, err
	}
	ok = true
	pruneReleases(s.Name, id, st.Release)
	if err := syncWorkers(ctx, s, home, log); err != nil {
		return res, err
	}
	res.Release, res.Port, res.Info = id, port, info
	if st.Port != 0 && st.Port != port {
		res.OldPort = st.Port
	}
	return res, nil
}

// NativeActivate starts an earlier release on the free port (rollback).
func NativeActivate(ctx context.Context, s NativeSpec, release string, log func(string)) (DeployResult, error) {
	var res DeployResult
	if err := s.Validate(); err != nil {
		return res, err
	}
	if !releaseIDRe.MatchString(release) {
		return res, errs.New(400, "native.bad_release")
	}
	rel := filepath.Join(nativeBase(s.Name), "releases", release)
	if _, err := os.Stat(rel); err != nil {
		return res, errs.New(404, "native.no_release", "release", release)
	}
	home, err := runtimeHome(ctx, s, log)
	if err != nil {
		return res, err
	}
	os.WriteFile(filepath.Join("/etc/systemd/system", nativeUnit(s.Name)), []byte(s.unitText(home)), 0o644)
	run(ctx, "systemctl", "daemon-reload")
	ports := NativePorts(s.Name)
	st := readNativeState(s.Name)
	port := ports[0]
	if st.Port == ports[0] {
		port = ports[1]
	}
	if err := startOn(ctx, s, rel, port, log); err != nil {
		return res, err
	}
	relink(rel, filepath.Join(nativeBase(s.Name), "current"))
	run(ctx, "systemctl", "enable", nativeInst(s.Name, port))
	writeNativeState(s.Name, nativeState{Release: release, Port: port})
	if err := syncWorkers(ctx, s, home, log); err != nil {
		return res, err
	}
	res.Release, res.Port = release, port
	if st.Port != 0 && st.Port != port {
		res.OldPort = st.Port
	}
	return res, nil
}

// NativeRetire stops the instance on a port that no longer takes traffic.
func NativeRetire(ctx context.Context, name string, port int) error {
	if !nativeNameRe.MatchString(name) {
		return errs.New(400, "native.bad_name")
	}
	if st := readNativeState(name); st.Port == port {
		return nil // never the live one
	}
	run(ctx, "systemctl", "disable", "--now", nativeInst(name, port))
	return nil
}

func pruneReleases(name string, keep ...string) {
	dir := filepath.Join(nativeBase(name), "releases")
	ents, _ := os.ReadDir(dir)
	var ids []string
	for _, e := range ents {
		if e.IsDir() && releaseIDRe.MatchString(e.Name()) {
			ids = append(ids, e.Name())
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(ids)))
	for i, id := range ids {
		if i < keepReleases || contains(keep, id) {
			continue
		}
		os.RemoveAll(filepath.Join(dir, id))
	}
}

type NativeRelease struct {
	ID   string            `json:"id"`
	At   int64             `json:"at"`
	Info map[string]string `json:"info,omitempty"`
	Node string            `json:"node,omitempty"` // runtime version
	Kind string            `json:"kind,omitempty"`
}

type NativeStatus struct {
	Exists   bool              `json:"exists"`
	Release  string            `json:"release"`
	Port     int               `json:"port"`
	Active   bool              `json:"active"`
	Releases []NativeRelease   `json:"releases"`
	Ports    [2]int            `json:"ports"`
	SharedMB int64             `json:"shared_mb"`
	User     string            `json:"user"`
	Workers  map[string]string `json:"workers"` // worker → systemd state
}

func NativeStat(ctx context.Context, name string) (NativeStatus, error) {
	if !nativeNameRe.MatchString(name) {
		return NativeStatus{}, errs.New(400, "native.bad_name")
	}
	st := readNativeState(name)
	out := NativeStatus{Release: st.Release, Port: st.Port, Ports: NativePorts(name), Releases: []NativeRelease{}, User: nativeUser(name), Workers: workerStates(ctx, name)}
	if _, err := os.Stat(nativeBase(name)); err != nil {
		return out, nil
	}
	out.Exists = true
	if st.Port > 0 {
		out.Active = nativeActive(ctx, name, st.Port)
	}
	dir := filepath.Join(nativeBase(name), "releases")
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		if !e.IsDir() || !releaseIDRe.MatchString(e.Name()) {
			continue
		}
		r := NativeRelease{ID: e.Name()}
		var m struct {
			At   int64             `json:"at"`
			Info map[string]string `json:"info"`
			Node string            `json:"node"`
			Kind string            `json:"kind"`
		}
		if b, err := os.ReadFile(filepath.Join(dir, e.Name(), ".berth-release.json")); err == nil {
			json.Unmarshal(b, &m)
			r.At, r.Info, r.Node, r.Kind = m.At, m.Info, m.Node, m.Kind
		}
		out.Releases = append(out.Releases, r)
	}
	sort.Slice(out.Releases, func(i, j int) bool { return out.Releases[i].ID > out.Releases[j].ID })
	if o, err := run(ctx, "du", "-sm", filepath.Join(nativeBase(name), "shared")); err == nil {
		if f := strings.Fields(o); len(f) > 0 {
			out.SharedMB, _ = strconv.ParseInt(f[0], 10, 64)
		}
	}
	return out, nil
}

// NativeRemove stops and forgets the project's service; its folder (releases, data) stays.
func NativeRemove(ctx context.Context, name string) error {
	if !nativeNameRe.MatchString(name) {
		return errs.New(400, "native.bad_name")
	}
	removeWorkers(ctx, name)
	for _, p := range NativePorts(name) {
		run(ctx, "systemctl", "disable", "--now", nativeInst(name, p))
		os.Remove(filepath.Join(nativeBase(name), "run-"+strconv.Itoa(p)))
	}
	os.Remove(filepath.Join("/etc/systemd/system", nativeUnit(name)))
	os.Remove(nativeEnvFile(name))
	os.Remove(filepath.Join(nativeBase(name), "state.json"))
	run(ctx, "systemctl", "daemon-reload")
	dropNativeFirewallIfUnused(ctx)
	return nil
}
