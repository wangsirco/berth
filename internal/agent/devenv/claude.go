package devenv

// Claude Code in dev workspaces (方案 §5.9): one copy of the native binary for every
// workspace (mounted read-only at /opt/berth/claude), tasks run non-interactively
// (`claude -p`) inside the workspace container, and every task's changes are recorded in
// a shadow git repository kept in the workspace's root-only state folder — the project's
// own .git is never touched — so the panel can show the diff and take it back.

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/wangsirco/berth/internal/errs"
)

var (
	ClaudeDir  = ToolsDir + "/claude"
	claudeBase = "https://downloads.claude.ai/claude-code-releases"
)

// ClaudeVersion is the installed copy's version ("" when missing).
func ClaudeVersion() string {
	b, err := os.ReadFile(filepath.Join(ClaudeDir, "VERSION"))
	if err != nil {
		return ""
	}
	if _, err := os.Stat(filepath.Join(ClaudeDir, "claude")); err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func claudePlatform() string {
	arch := map[string]string{"arm64": "arm64", "amd64": "x64"}[runtime.GOARCH]
	return "linux-" + arch
}

func httpText(ctx context.Context, url string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", errs.New(502, "dev.download_failed", "detail", err.Error())
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", errs.New(502, "dev.download_failed", "detail", url+": "+resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return string(b), err
}

// ClaudeLatest asks Anthropic's release server for the newest version.
func ClaudeLatest(ctx context.Context) (string, error) {
	v, err := httpText(ctx, claudeBase+"/latest")
	v = strings.TrimSpace(v)
	if err == nil && (v == "" || strings.ContainsAny(v, "/ <")) {
		err = errs.New(502, "dev.download_failed", "detail", "unexpected version "+strconv.Quote(v))
	}
	return v, err
}

// InstallClaude puts the newest Claude Code in place (update: even when one is there).
// Running workspaces keep the old file open; they see the new one on their next start.
func InstallClaude(ctx context.Context, update bool, log func(string)) error {
	cur := ClaudeVersion()
	if cur != "" && !update {
		return nil
	}
	ver, err := ClaudeLatest(ctx)
	if err != nil {
		return err
	}
	if ver == cur {
		log("Claude Code " + ver + " is the newest")
		return nil
	}
	m, err := httpText(ctx, claudeBase+"/"+ver+"/manifest.json")
	if err != nil {
		return err
	}
	var man struct {
		Platforms map[string]struct {
			Checksum string `json:"checksum"`
		} `json:"platforms"`
	}
	json.Unmarshal([]byte(m), &man)
	sum := man.Platforms[claudePlatform()].Checksum
	if len(sum) != 64 {
		return errs.New(502, "dev.download_failed", "detail", "no "+claudePlatform()+" build of Claude Code "+ver)
	}
	if err := os.MkdirAll(ClaudeDir, 0o755); err != nil {
		return err
	}
	tmp := filepath.Join(ClaudeDir, ".claude.new")
	defer os.Remove(tmp)
	log("downloading Claude Code " + ver)
	got, err := download(ctx, claudeBase+"/"+ver+"/"+claudePlatform()+"/claude", tmp, "Claude Code", log)
	if err != nil {
		return err
	}
	if got != sum {
		return errs.New(502, "dev.download_failed", "detail", "checksum mismatch")
	}
	os.Chmod(tmp, 0o755)
	if err := os.Rename(tmp, filepath.Join(ClaudeDir, "claude")); err != nil {
		return err
	}
	os.WriteFile(filepath.Join(ClaudeDir, "VERSION"), []byte(ver+"\n"), 0o644)
	log("Claude Code " + ver + " ready")
	return nil
}

// download writes url to dst with progress lines and returns its sha256.
func download(ctx context.Context, url, dst, label string, log func(string)) (string, error) {
	req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
	resp, err := (&http.Client{Timeout: 20 * time.Minute}).Do(req)
	if err != nil {
		return "", errs.New(502, "dev.download_failed", "detail", err.Error())
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", errs.New(502, "dev.download_failed", "detail", resp.Status)
	}
	f, err := os.Create(dst)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	total, done, last := resp.ContentLength, int64(0), -1
	buf := make([]byte, 256<<10)
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, err := f.Write(buf[:n]); err != nil {
				return "", err
			}
			h.Write(buf[:n])
			done += int64(n)
			if total > 0 {
				if pct := int(done * 100 / total); pct/5 != last/5 {
					last = pct
					log(fmt.Sprintf("::progress %d/100 %s %d MB", pct, label, done>>20))
				}
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return "", errs.New(502, "dev.download_failed", "detail", rerr.Error())
		}
	}
	return hex.EncodeToString(h.Sum(nil)), f.Close()
}

// --- the shadow repository ------------------------------------------------------

// big generated folders a snapshot skips (the project's own .gitignore applies too)
var shadowExclude = "node_modules/\n.venv/\nvenv/\n__pycache__/\n*.pyc\ntarget/\n.next/\n.nuxt/\n.cache/\n.gradle/\n.terraform/\n"

func shadowGit(ctx context.Context, name, dir string, args ...string) (string, error) {
	g := filepath.Join(StateDir, name, "shadow.git")
	full := append([]string{"--git-dir=" + g, "--work-tree=" + dir, "-c", "safe.directory=*", "-c", "core.quotepath=off",
		"-c", "user.name=Berth", "-c", "user.email=berth@localhost", "-c", "commit.gpgsign=false"}, args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		msg := err.Error()
		if ee, ok := err.(*exec.ExitError); ok {
			msg = strings.TrimSpace(string(ee.Stderr))
		}
		return "", errs.New(500, "dev.snapshot_failed", "detail", tail(msg, 400))
	}
	return strings.TrimRight(string(out), "\n"), nil
}

// Snapshot records the project folder as it is now and returns the commit.
func Snapshot(ctx context.Context, name, dir string) (string, error) {
	if err := CheckDir(dir); err != nil {
		return "", err
	}
	g := filepath.Join(StateDir, name, "shadow.git")
	if _, err := os.Stat(filepath.Join(g, "HEAD")); err != nil {
		os.MkdirAll(filepath.Join(StateDir, name), 0o700)
		if out, err := exec.CommandContext(ctx, "git", "init", "-q", "--bare", g).CombinedOutput(); err != nil {
			return "", errs.New(500, "dev.snapshot_failed", "detail", string(out))
		}
		os.MkdirAll(filepath.Join(g, "info"), 0o755)
		os.WriteFile(filepath.Join(g, "info", "exclude"), []byte(shadowExclude), 0o644)
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	if _, err := shadowGit(ctx, name, dir, "add", "-A", "."); err != nil {
		return "", err
	}
	if _, err := shadowGit(ctx, name, dir, "commit", "-q", "--allow-empty", "--no-verify", "-m", "berth snapshot"); err != nil {
		return "", err
	}
	return shadowGit(ctx, name, dir, "rev-parse", "HEAD")
}

type FileStat struct {
	Path   string `json:"path"`
	Add    int    `json:"add"`
	Del    int    `json:"del"`
	Binary bool   `json:"binary,omitempty"`
}

const maxDiff = 1 << 20

// Diff between two snapshots: per-file counts and the text diff (cut at 1 MB).
func Diff(ctx context.Context, name, dir, from, to string) ([]FileStat, string, bool, error) {
	ns, err := shadowGit(ctx, name, dir, "diff", "--numstat", "-M", from, to)
	if err != nil {
		return nil, "", false, err
	}
	files := []FileStat{}
	for _, l := range strings.Split(ns, "\n") {
		f := strings.SplitN(l, "\t", 3)
		if len(f) != 3 {
			continue
		}
		st := FileStat{Path: f[2]}
		if f[0] == "-" {
			st.Binary = true
		} else {
			st.Add, _ = strconv.Atoi(f[0])
			st.Del, _ = strconv.Atoi(f[1])
		}
		files = append(files, st)
	}
	d, err := shadowGit(ctx, name, dir, "diff", "--no-color", "-M", from, to)
	if err != nil {
		return nil, "", false, err
	}
	cut := len(d) > maxDiff
	if cut {
		d = d[:maxDiff]
	}
	return files, d, cut, nil
}

// Patch applies the change between two snapshots to the folder (reverse: takes it back).
// It refuses, changing nothing, when the files have moved on since.
func Patch(ctx context.Context, name, dir, from, to string, reverse bool) error {
	if err := CheckDir(dir); err != nil {
		return err
	}
	patch, err := shadowGit(ctx, name, dir, "diff", "--binary", "--full-index", from, to)
	if err != nil {
		return err
	}
	if strings.TrimSpace(patch) == "" {
		return nil
	}
	args := []string{"apply", "--whitespace=nowarn"}
	if reverse {
		args = append(args, "-R")
	}
	run := func(extra ...string) ([]byte, error) {
		cmd := exec.CommandContext(ctx, "git", append(append([]string{"-c", "safe.directory=*"}, args...), extra...)...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_DIR=/nonexistent") // a plain patch: never the project's own index
		cmd.Stdin = strings.NewReader(patch + "\n")
		return cmd.CombinedOutput()
	}
	if out, err := run("--check"); err != nil {
		return errs.New(409, "dev.patch_conflict", "detail", tail(strings.TrimSpace(string(out)), 600))
	}
	if out, err := run(); err != nil {
		return errs.New(500, "dev.patch_conflict", "detail", tail(strings.TrimSpace(string(out)), 600))
	}
	// files brought back belong to the folder's owner, as the rest
	if st, err := os.Stat(dir); err == nil {
		if sys, ok := st.Sys().(*syscall.Stat_t); ok {
			names, _ := shadowGit(ctx, name, dir, "diff", "--name-only", from, to)
			for _, n := range strings.Split(names, "\n") {
				if n == "" {
					continue
				}
				p := filepath.Join(dir, n)
				for q := p; strings.HasPrefix(q, dir+"/"); q = filepath.Dir(q) {
					os.Lchown(q, int(sys.Uid), int(sys.Gid))
				}
			}
		}
	}
	return nil
}

// --- running a task -------------------------------------------------------------

type RunIn struct {
	Dir     string            `json:"dir"`    // the project folder on the host
	Folder  string            `json:"folder"` // the same folder inside the container
	User    string            `json:"user"`
	Prompt  string            `json:"prompt"`
	Resume  string            `json:"resume"` // a session to continue
	Mode    string            `json:"mode"`   // auto | edits
	Model   string            `json:"model"`
	System  string            `json:"system"` // appended to the system prompt
	Env     map[string]string `json:"env"`    // ANTHROPIC_API_KEY / CLAUDE_CODE_OAUTH_TOKEN
	MaxTurn int               `json:"max_turns"`
}

type RunOut struct {
	SessionID  string     `json:"session_id"`
	Result     string     `json:"result"`
	IsError    bool       `json:"is_error"`
	Subtype    string     `json:"subtype"`
	CostUSD    float64    `json:"cost_usd"`
	Turns      int        `json:"turns"`
	DurationMS int64      `json:"duration_ms"`
	Before     string     `json:"before"`
	After      string     `json:"after"`
	Files      []FileStat `json:"files"`
	Diff       string     `json:"diff"`
	DiffCut    bool       `json:"diff_cut,omitempty"`
	Exit       int        `json:"exit"`
	Stderr     string     `json:"stderr,omitempty"`
}

var envKeyOK = map[string]bool{"ANTHROPIC_API_KEY": true, "CLAUDE_CODE_OAUTH_TOKEN": true, "ANTHROPIC_BASE_URL": true}

// RunClaude runs one task in the workspace container and records what it changed.
func RunClaude(ctx context.Context, name string, in RunIn, log func(string)) (*RunOut, error) {
	if !ValidName(name) || in.User == "" || strings.ContainsAny(in.User, " :/") || !strings.HasPrefix(in.Folder, "/") {
		return nil, errs.New(400, "params.invalid")
	}
	out := &RunOut{}
	var err error
	if out.Before, err = Snapshot(ctx, name, in.Dir); err != nil {
		return nil, err
	}
	container := "berth-dev-" + name
	args := []string{"exec", "-i", "-u", in.User, "-w", in.Folder, "-e", "BERTH_TASK=1"}
	env := os.Environ()
	for k, v := range in.Env {
		if envKeyOK[k] && v != "" {
			args = append(args, "-e", k) // the value comes from our environment, not the command line
			env = append(env, k+"="+v)
		}
	}
	args = append(args, container, "claude", "-p", "--output-format", "stream-json", "--verbose")
	if in.Mode == "edits" {
		args = append(args, "--permission-mode", "acceptEdits")
	} else {
		args = append(args, "--dangerously-skip-permissions")
	}
	if in.Resume != "" {
		args = append(args, "--resume", in.Resume)
	}
	if in.Model != "" {
		args = append(args, "--model", in.Model)
	}
	if in.MaxTurn > 0 {
		args = append(args, "--max-turns", strconv.Itoa(in.MaxTurn))
	}
	if in.System != "" {
		args = append(args, "--append-system-prompt", in.System)
	}
	cmd := exec.Command("docker", args...)
	cmd.Env = env
	cmd.Stdin = strings.NewReader(in.Prompt)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	pipe, _ := cmd.StdoutPipe()
	start := time.Now()
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	// a dropped connection stops the task inside the container too (killing docker exec does not)
	stop := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			StopClaude(name)
			cmd.Process.Kill()
		case <-stop:
		}
	}()
	sc := bufio.NewScanner(pipe)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for sc.Scan() {
		readEvent(sc.Bytes(), out, log)
	}
	werr := cmd.Wait()
	close(stop)
	if out.DurationMS == 0 {
		out.DurationMS = time.Since(start).Milliseconds()
	}
	if ee, ok := werr.(*exec.ExitError); ok {
		out.Exit = ee.ExitCode()
	}
	out.Stderr = tail(strings.TrimSpace(stderr.String()), 2000)
	// what it changed, even when it failed or was stopped half way
	snapCtx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	if out.After, err = Snapshot(snapCtx, name, in.Dir); err == nil {
		out.Files, out.Diff, out.DiffCut, _ = Diff(snapCtx, name, in.Dir, out.Before, out.After)
	}
	if out.SessionID == "" && out.Result == "" {
		detail := out.Stderr
		if strings.Contains(detail, "executable file not found") || strings.Contains(detail, "claude: not found") {
			return out, errs.New(500, "dev.claude_missing")
		}
		if ctx.Err() != nil {
			return out, errs.New(499, "tasks.canceled")
		}
		return out, errs.New(500, "dev.claude_failed", "detail", tail(detail, 600))
	}
	return out, nil
}

// StopClaude ends the running task in a workspace: an interrupt, then a kill after 5 s.
func StopClaude(name string) {
	c := "berth-dev-" + name
	pat := "--output-format stream-json"
	running := func() bool { return exec.Command("docker", "exec", c, "pgrep", "-f", "--", pat).Run() == nil }
	exec.Command("docker", "exec", c, "pkill", "-INT", "-f", "--", pat).Run()
	for i := 0; i < 10 && running(); i++ {
		time.Sleep(500 * time.Millisecond)
	}
	if running() {
		exec.Command("docker", "exec", c, "pkill", "-KILL", "-f", "--", pat).Run()
	}
}

// readEvent turns one stream-json line into a progress line and the final result.
func readEvent(line []byte, out *RunOut, log func(string)) {
	var ev struct {
		Type      string  `json:"type"`
		Subtype   string  `json:"subtype"`
		SessionID string  `json:"session_id"`
		Result    string  `json:"result"`
		IsError   bool    `json:"is_error"`
		Cost      float64 `json:"total_cost_usd"`
		Turns     int     `json:"num_turns"`
		Duration  int64   `json:"duration_ms"`
		Message   struct {
			Content []struct {
				Type  string         `json:"type"`
				Text  string         `json:"text"`
				Name  string         `json:"name"`
				Input map[string]any `json:"input"`
			} `json:"content"`
		} `json:"message"`
	}
	if json.Unmarshal(line, &ev) != nil {
		return
	}
	if ev.SessionID != "" {
		out.SessionID = ev.SessionID
	}
	switch ev.Type {
	case "assistant":
		for _, c := range ev.Message.Content {
			switch c.Type {
			case "text":
				if t := strings.TrimSpace(c.Text); t != "" {
					log("💬 " + oneLine(t, 300))
				}
			case "tool_use":
				log("🔧 " + c.Name + " " + toolHint(c.Input))
			}
		}
	case "result":
		out.Result, out.IsError, out.Subtype = ev.Result, ev.IsError, ev.Subtype
		out.CostUSD, out.Turns, out.DurationMS = ev.Cost, ev.Turns, ev.Duration
	}
}

func toolHint(in map[string]any) string {
	for _, k := range []string{"command", "file_path", "path", "pattern", "url", "description", "prompt"} {
		if v, ok := in[k].(string); ok && v != "" {
			return oneLine(v, 160)
		}
	}
	return ""
}

func oneLine(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

// ClaudeState: whether the workspace has Claude Code and a login of its own.
func ClaudeState(ctx context.Context, name string) map[string]any {
	user := strings.TrimSpace(readFile(filepath.Join(StateDir, name, "user")))
	st := map[string]any{"version": ClaudeVersion(), "user": user}
	if user == "" {
		return st
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "docker", "exec", "-u", user, "berth-dev-"+name, "sh", "-c",
		`command -v claude >/dev/null && echo bin; test -s "$HOME/.claude/.credentials.json" && echo login; true`).Output()
	if err == nil {
		st["in_container"] = strings.Contains(string(out), "bin")
		st["logged_in"] = strings.Contains(string(out), "login")
	}
	return st
}

func readFile(p string) string {
	b, _ := os.ReadFile(p)
	return string(b)
}

var hexRe = regexp.MustCompile(`^[0-9a-f]{40}$`)
