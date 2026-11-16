// Package devenv backs dev workspaces (方案 §5.9): a container per project with the
// project's folder mounted, SSH for desktop editors, and web VS Code (code-server).
//
// The host keeps three things under Root:
//
//	<name>/          the project's files when Berth made the folder (new or git clone)
//	.state/<name>/   root-only: SSH host key, authorized_keys, code-server password and TLS
//	                 certificate, lifecycle scripts and their logs
//	.tools/          code-server (one copy for every workspace) and boot.sh
//
// The containers themselves are compose projects the panel writes.
package devenv

import (
	"bufio"
	"context"
	_ "embed"
	"fmt"
	"github.com/wangsirco/berth/internal/sysexec"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/wangsirco/berth/internal/errs"
)

const (
	Root              = "/srv/berth/dev"
	CodeServerVersion = "4.145.2"
)

var (
	ToolsDir = Root + "/.tools"
	StateDir = Root + "/.state"
	nameRe   = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,30}$`)
	// a folder that may be mounted into a workspace: under these, never Berth's own data
	allowedRoots = []string{"/srv/", "/home/", "/opt/", "/data/", "/mnt/", "/media/", "/var/www/"}
	deniedRoots  = []string{"/srv/berth/", "/var/lib/berth/"}
	codeURL      = "https://github.com/coder/code-server/releases/download/v%[1]s/code-server-%[1]s-linux-arm64.tar.gz"
)

//go:embed boot.sh
var bootScript []byte

//go:embed langpacks.js
var langpacksJS []byte

//go:embed claude-chat.sh
var claudeChat []byte

func ValidName(n string) bool { return nameRe.MatchString(n) }

// CheckDir reports whether an existing folder may be mounted into a workspace.
func CheckDir(dir string) error {
	clean := filepath.Clean(dir)
	if !filepath.IsAbs(clean) {
		return errs.New(400, "dev.bad_dir", "dir", dir)
	}
	real, err := filepath.EvalSymlinks(clean)
	if err != nil {
		return errs.New(404, "dev.dir_missing", "dir", dir)
	}
	ok := real != Root && strings.HasPrefix(real+"/", Root+"/") && !strings.HasPrefix(real+"/", ToolsDir+"/") && !strings.HasPrefix(real+"/", StateDir+"/")
	for _, r := range allowedRoots {
		if strings.HasPrefix(real+"/", r) && real+"/" != r {
			ok = true
		}
	}
	for _, r := range deniedRoots {
		if strings.HasPrefix(real+"/", r) && !strings.HasPrefix(real+"/", Root+"/") {
			ok = false
		}
	}
	if !ok {
		return errs.New(400, "dev.dir_not_allowed", "dir", dir, "allowed", strings.Join(allowedRoots, " "))
	}
	if st, err := os.Stat(real); err != nil || !st.IsDir() {
		return errs.New(400, "dev.not_a_dir", "dir", dir)
	}
	return nil
}

// Tools reports the installed code-server version ("" when missing).
func Tools() map[string]string {
	v := ""
	if b, err := os.ReadFile(filepath.Join(ToolsDir, "code-server", "VERSION")); err == nil {
		v = strings.TrimSpace(string(b))
	}
	return map[string]string{"code_server": v, "want": CodeServerVersion, "claude": ClaudeVersion()}
}

// WriteBoot puts the current boot.sh in place (every create and rebuild).
func WriteBoot() error {
	if err := os.MkdirAll(ToolsDir, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(ToolsDir, "langpacks.js"), langpacksJS, 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(ToolsDir, "claude-chat.sh"), claudeChat, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(ToolsDir, "boot.sh"), bootScript, 0o755)
}

// InstallCodeServer downloads code-server once for every workspace, with progress lines.
func InstallCodeServer(ctx context.Context, log func(string)) error {
	if err := WriteBoot(); err != nil {
		return err
	}
	if Tools()["code_server"] == CodeServerVersion {
		return nil
	}
	tmp := filepath.Join(ToolsDir, ".code-server.tar.gz")
	defer os.Remove(tmp)
	if _, err := download(ctx, fmt.Sprintf(codeURL, CodeServerVersion), tmp, "code-server", log); err != nil {
		return err
	}
	log("unpacking")
	stage := filepath.Join(ToolsDir, ".code-server.new")
	os.RemoveAll(stage)
	os.MkdirAll(stage, 0o755)
	if out, err := exec.CommandContext(ctx, "tar", "-xzf", tmp, "-C", stage, "--strip-components=1").CombinedOutput(); err != nil {
		os.RemoveAll(stage)
		return errs.New(500, "dev.unpack_failed", "detail", strings.TrimSpace(string(out)))
	}
	os.WriteFile(filepath.Join(stage, "VERSION"), []byte(CodeServerVersion+"\n"), 0o644)
	dst := filepath.Join(ToolsDir, "code-server")
	os.RemoveAll(dst + ".old")
	os.Rename(dst, dst+".old")
	if err := os.Rename(stage, dst); err != nil {
		return err
	}
	os.RemoveAll(dst + ".old")
	log("code-server " + CodeServerVersion + " ready")
	return nil
}

// PrepareIn: where a workspace's files come from.
type PrepareIn struct {
	Name   string `json:"name"`
	Source string `json:"source"` // new | git | dir
	GitURL string `json:"git_url"`
	GitRef string `json:"git_ref"`
	Dir    string `json:"dir"`
}

// Prepared: the folder to mount and its devcontainer.json (text, "" when none).
type Prepared struct {
	Dir          string `json:"dir"`
	Devcontainer string `json:"devcontainer"`
	DevPath      string `json:"devcontainer_path"` // relative to Dir
	Made         bool   `json:"made"`              // Berth made the folder (its files go to the user)
}

var gitURLRe = regexp.MustCompile(`^(https://[^\s]+|git@[A-Za-z0-9.-]+:[^\s]+|ssh://[^\s]+)$`)

// Prepare makes, clones or checks the folder and reads its devcontainer.json.
func Prepare(ctx context.Context, in PrepareIn, log func(string)) (Prepared, error) {
	if !ValidName(in.Name) {
		return Prepared{}, errs.New(400, "dev.bad_name")
	}
	out := Prepared{}
	switch in.Source {
	case "", "new":
		out.Dir, out.Made = filepath.Join(Root, in.Name), true
		if _, err := os.Stat(out.Dir); err == nil {
			out.Made = false // an earlier workspace's files, kept on delete: use them again
		} else if err := os.MkdirAll(out.Dir, 0o755); err != nil {
			return out, err
		}
	case "git":
		if !gitURLRe.MatchString(in.GitURL) {
			return out, errs.New(400, "dev.bad_git_url")
		}
		out.Dir, out.Made = filepath.Join(Root, in.Name), true
		if _, err := os.Stat(out.Dir); err == nil {
			return out, errs.New(409, "dev.dir_exists", "dir", out.Dir)
		}
		args := []string{"clone", "--progress"}
		if in.GitRef != "" {
			args = append(args, "--branch", in.GitRef)
		}
		args = append(args, "--", in.GitURL, out.Dir)
		log("git clone " + redactURL(in.GitURL))
		gn, gargs := sysexec.Wrap("git", args...)
		cmd := exec.CommandContext(ctx, gn, gargs...)
		cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_SSH_COMMAND=ssh -o BatchMode=yes -o StrictHostKeyChecking=accept-new")
		pr, pw := io.Pipe()
		cmd.Stdout, cmd.Stderr = pw, pw
		if err := cmd.Start(); err != nil {
			return out, err
		}
		var tail []string
		read := make(chan struct{})
		go func() {
			defer close(read)
			sc := bufio.NewScanner(pr)
			sc.Split(scanLinesCR)
			for sc.Scan() {
				l := strings.TrimSpace(sc.Text())
				if l == "" {
					continue
				}
				tail = append(tail, l)
				if len(tail) > 6 {
					tail = tail[1:]
				}
				if strings.Contains(l, "%") && !strings.HasSuffix(l, "done.") {
					continue // in-place progress: only the final line of each phase
				}
				log(redactURL(l))
			}
		}()
		err := cmd.Wait()
		pw.Close()
		<-read
		if err != nil {
			os.RemoveAll(out.Dir)
			all := strings.Join(tail, " / ")
			if strings.Contains(all, "could not read Username") || strings.Contains(all, "Repository not found") || strings.Contains(all, "Authentication failed") || strings.Contains(all, "Permission denied (publickey)") {
				return out, errs.New(400, "dev.clone_auth", "url", redactURL(in.GitURL))
			}
			return out, errs.New(400, "dev.clone_failed", "detail", redactURL(all))
		}
	case "dir":
		if err := CheckDir(in.Dir); err != nil {
			return out, err
		}
		out.Dir, _ = filepath.EvalSymlinks(filepath.Clean(in.Dir))
	default:
		return out, errs.New(400, "dev.bad_source")
	}
	out.DevPath, out.Devcontainer = FindDevcontainer(out.Dir)
	return out, nil
}

// FindDevcontainer: the first of the standard places that exists.
func FindDevcontainer(dir string) (string, string) {
	cands := []string{".devcontainer/devcontainer.json", ".devcontainer.json"}
	if ents, err := os.ReadDir(filepath.Join(dir, ".devcontainer")); err == nil {
		for _, e := range ents { // .devcontainer/<variant>/devcontainer.json
			if e.IsDir() {
				cands = append(cands, ".devcontainer/"+e.Name()+"/devcontainer.json")
			}
		}
	}
	for _, c := range cands {
		if b, err := os.ReadFile(filepath.Join(dir, c)); err == nil && len(b) < 1<<20 {
			return c, string(b)
		}
	}
	return "", ""
}

func redactURL(s string) string {
	return regexp.MustCompile(`(https?://)[^/@\s]+@`).ReplaceAllString(s, "$1***@")
}

func scanLinesCR(data []byte, atEOF bool) (int, []byte, error) {
	for i, b := range data {
		if b == '\n' || b == '\r' {
			return i + 1, data[:i], nil
		}
	}
	if atEOF && len(data) > 0 {
		return len(data), data, nil
	}
	return 0, nil, nil
}

// StateIn: the per-workspace secrets and scripts boot.sh reads.
type StateIn struct {
	AuthorizedKeys string            `json:"authorized_keys"`
	Password       string            `json:"password"`
	TLSCert        string            `json:"tls_cert"`
	TLSKey         string            `json:"tls_key"`
	Hooks          map[string]string `json:"hooks"` // on-create | post-create | post-start → script
	Extensions     []string          `json:"extensions"`
	Chown          bool              `json:"chown"`      // hand the folder to the workspace user on first start
	Fresh          bool              `json:"fresh"`      // forget lifecycle progress (rebuild runs postCreate again)
	ClaudeEnv      map[string]string `json:"claude_env"` // ANTHROPIC_API_KEY | CLAUDE_CODE_OAUTH_TOKEN
}

var tokenRe = regexp.MustCompile(`^[A-Za-z0-9_\-.:/]{8,400}$`)

var hookNames = []string{"on-create", "post-create", "post-start"}

func WriteState(name string, in StateIn) error {
	if !ValidName(name) {
		return errs.New(400, "dev.bad_name")
	}
	d := filepath.Join(StateDir, name)
	if err := os.MkdirAll(d, 0o700); err != nil {
		return err
	}
	os.Chmod(StateDir, 0o700)
	w := func(n, s string) error { return os.WriteFile(filepath.Join(d, n), []byte(s), 0o600) }
	if err := w("authorized_keys", in.AuthorizedKeys); err != nil {
		return err
	}
	if in.Password != "" {
		w("password", in.Password)
	}
	if in.TLSCert != "" {
		w("tls.crt", in.TLSCert)
		w("tls.key", in.TLSKey)
	} else {
		os.Remove(filepath.Join(d, "tls.crt"))
		os.Remove(filepath.Join(d, "tls.key"))
	}
	for _, h := range hookNames {
		if s := in.Hooks[h]; s != "" {
			w(h+".sh", s)
		} else {
			os.Remove(filepath.Join(d, h+".sh"))
		}
	}
	if len(in.Extensions) > 0 {
		w("extensions", strings.Join(in.Extensions, "\n")+"\n")
	} else {
		os.Remove(filepath.Join(d, "extensions"))
	}
	var ce strings.Builder
	for k, v := range in.ClaudeEnv {
		if envKeyOK[k] && tokenRe.MatchString(v) {
			fmt.Fprintf(&ce, "export %s='%s'\n", k, v)
		}
	}
	if ce.Len() > 0 {
		w("claude.env", ce.String())
	} else {
		os.Remove(filepath.Join(d, "claude.env"))
	}
	if in.Chown {
		w("chown-pending", "")
	}
	if in.Fresh {
		for _, f := range []string{"created", "extensions.done"} {
			os.Remove(filepath.Join(d, f))
		}
	}
	return nil
}

// Status: what boot.sh reported (user, lifecycle hooks) for the workspace page.
func Status(name string) map[string]any {
	d := filepath.Join(StateDir, name)
	read := func(n string) string {
		b, _ := os.ReadFile(filepath.Join(d, n))
		return strings.TrimSpace(string(b))
	}
	hooks := map[string]any{}
	for _, h := range hookNames {
		if _, err := os.Stat(filepath.Join(d, h+".sh")); err != nil {
			continue
		}
		hooks[h] = map[string]string{"status": read(h + ".status"), "log": tail(read(h+".log"), 4000)}
	}
	return map[string]any{"user": read("user"), "hooks": hooks, "code_log": tail(read("code-server.log"), 2000),
		"extensions_log": tail(read("extensions.log"), 2000)}
}

func tail(s string, n int) string {
	if len(s) > n {
		return s[len(s)-n:]
	}
	return s
}

// RemoveState forgets a workspace's secrets (its files are the panel's call: files trash).
func RemoveState(name string) error {
	if !ValidName(name) {
		return errs.New(400, "dev.bad_name")
	}
	return os.RemoveAll(filepath.Join(StateDir, name))
}
