package docker

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"github.com/wangsirco/berth/internal/agent/drift"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/wangsirco/berth/internal/errs"
)

// StacksDir holds projects created by Berth (方案 §4.7).
var StacksDir = "/srv/berth/stacks"

type Project struct {
	Name        string   `json:"name"`
	Dir         string   `json:"dir"`
	ConfigFiles []string `json:"config_files"`
	Services    []string `json:"services"`
	Running     int      `json:"running"`
	Total       int      `json:"total"`
	InStacks    bool     `json:"in_stacks"` // lives under /srv/berth/stacks
}

var projectNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)

func ValidProjectName(n string) bool { return projectNameRe.MatchString(n) }

// Projects finds compose projects from container labels plus stacks with no containers yet.
func (c *Client) Projects(ctx context.Context) ([]Project, error) {
	cts, err := c.Containers(ctx)
	if err != nil {
		return nil, err
	}
	m := map[string]*Project{}
	for _, ct := range cts {
		if ct.Project == "" {
			continue
		}
		p := m[ct.Project]
		if p == nil {
			p = &Project{Name: ct.Project, Services: []string{}, ConfigFiles: []string{}}
			if files := ct.Labels["com.docker.compose.project.config_files"]; files != "" {
				p.ConfigFiles = strings.Split(files, ",")
			}
			p.Dir = ct.Labels["com.docker.compose.project.working_dir"]
			if p.Dir == "" && len(p.ConfigFiles) > 0 {
				p.Dir = filepath.Dir(p.ConfigFiles[0])
			}
			m[ct.Project] = p
		}
		p.Total++
		if ct.State == "running" {
			p.Running++
		}
		if ct.Service != "" && !contains(p.Services, ct.Service) {
			p.Services = append(p.Services, ct.Service)
		}
	}
	entries, _ := os.ReadDir(StacksDir)
	for _, e := range entries {
		if !e.IsDir() || m[e.Name()] != nil {
			continue
		}
		dir := filepath.Join(StacksDir, e.Name())
		if f := composeFile(dir); f != "" {
			m[e.Name()] = &Project{Name: e.Name(), Dir: dir, ConfigFiles: []string{f}, Services: []string{}}
		}
	}
	out := make([]Project, 0, len(m))
	for _, p := range m {
		p.InStacks = strings.HasPrefix(p.Dir, StacksDir+"/")
		sort.Strings(p.Services)
		out = append(out, *p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func contains(l []string, v string) bool {
	for _, x := range l {
		if x == v {
			return true
		}
	}
	return false
}

func composeFile(dir string) string {
	for _, n := range []string{"compose.yaml", "compose.yml", "docker-compose.yaml", "docker-compose.yml"} {
		if _, err := os.Stat(filepath.Join(dir, n)); err == nil {
			return filepath.Join(dir, n)
		}
	}
	return ""
}

func (c *Client) project(ctx context.Context, name string) (*Project, error) {
	ps, err := c.Projects(ctx)
	if err != nil {
		return nil, err
	}
	for _, p := range ps {
		if p.Name == name {
			return &p, nil
		}
	}
	return nil, errs.New(404, "compose.not_found", "name", name)
}

type ProjectFiles struct {
	Dir     string `json:"dir"`
	Compose string `json:"compose"`
	Env     string `json:"env"`
	File    string `json:"file"`
}

// ReadProject returns the project's compose file and .env as they are on disk.
func ReadProject(dir, file string) (*ProjectFiles, error) {
	if file == "" {
		file = composeFile(dir)
	}
	if file == "" {
		return nil, errs.New(404, "compose.no_file", "dir", dir)
	}
	b, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	env, err := os.ReadFile(filepath.Join(dir, ".env"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	return &ProjectFiles{Dir: dir, File: file, Compose: string(b), Env: string(env)}, nil
}

// WriteProject saves compose and .env, keeping the previous versions in .berth/history/.
func WriteProject(dir, file, compose, env string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if file == "" {
		file = composeFile(dir)
		if file == "" {
			file = filepath.Join(dir, "compose.yaml")
		}
	}
	hist := filepath.Join(dir, ".berth", "history", time.Now().Format("20060102-150405.000"))
	if old, err := os.ReadFile(file); err == nil {
		if err := os.MkdirAll(hist, 0o700); err != nil {
			return err
		}
		_ = os.WriteFile(filepath.Join(hist, filepath.Base(file)), old, 0o600)
		if oldEnv, err := os.ReadFile(filepath.Join(dir, ".env")); err == nil {
			_ = os.WriteFile(filepath.Join(hist, ".env"), oldEnv, 0o600)
		}
		pruneHistory(filepath.Join(dir, ".berth", "history"), 20)
	}
	if err := writeAtomic(file, []byte(compose), 0o644); err != nil {
		return err
	}
	project := filepath.Base(dir)
	drift.Track(file, "compose", project)
	envPath := filepath.Join(dir, ".env")
	if env == "" {
		if err := os.Remove(envPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		drift.Track(envPath, "compose", project)
		return nil
	}
	err := writeAtomic(envPath, []byte(env), 0o600)
	drift.Track(envPath, "compose", project)
	return err
}

func writeAtomic(path string, b []byte, mode os.FileMode) error {
	tmp := path + ".berth-tmp"
	if err := os.WriteFile(tmp, b, mode); err != nil {
		return err
	}
	if err := os.Chmod(tmp, mode); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func pruneHistory(dir string, keep int) {
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) <= keep {
		return
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for _, e := range entries[:len(entries)-keep] {
		_ = os.RemoveAll(filepath.Join(dir, e.Name()))
	}
}

var secretKey = regexp.MustCompile(`(?i)(PASS|SECRET|TOKEN|KEY|PRIVATE)`)

// maskEnv hides values of secret-looking keys in .env text, keeping comments and order.
func maskEnv(text string) string {
	lines := strings.Split(text, "\n")
	for i, l := range lines {
		t := strings.TrimSpace(l)
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		k, _, ok := strings.Cut(l, "=")
		if ok && secretKey.MatchString(k) {
			lines[i] = k + "=••••"
		}
	}
	return strings.Join(lines, "\n")
}

func maskEnvList(env []string) []string {
	if env == nil {
		return nil
	}
	out := make([]string, len(env))
	for i, e := range env {
		out[i] = maskEnv(e)
	}
	return out
}

// Validate checks compose text with `docker compose config -q` in the project directory
// (so relative paths and .env resolve as they will at up time).
func Validate(ctx context.Context, dir, compose, env string) error {
	tmp, err := os.MkdirTemp("", "berth-compose-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	f := filepath.Join(tmp, "compose.yaml")
	if err := os.WriteFile(f, []byte(compose), 0o600); err != nil {
		return err
	}
	args := []string{"compose", "--project-directory", dir, "-f", f}
	if env != "" {
		ef := filepath.Join(tmp, ".env")
		if err := os.WriteFile(ef, []byte(env), 0o600); err != nil {
			return err
		}
		args = append(args, "--env-file", ef)
	}
	args = append(args, "config", "-q")
	out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
	if err != nil {
		return errs.New(400, "compose.invalid", "detail", strings.TrimSpace(strings.ReplaceAll(string(out), tmp+"/", "")))
	}
	return nil
}

// Compose runs `docker compose <cmd>` for a project and streams its output lines.
func Compose(ctx context.Context, name, dir string, files []string, cmd string, line func(string)) error {
	args := []string{"compose", "--project-directory", dir, "-p", name}
	for _, f := range files {
		args = append(args, "-f", f)
	}
	switch cmd {
	case "up":
		args = append(args, "up", "-d", "--remove-orphans")
	case "down":
		args = append(args, "down")
	case "pull":
		args = append(args, "pull")
	case "restart":
		args = append(args, "restart")
	case "build":
		args = append(args, "build", "--pull")
	default:
		return errs.New(400, "params.enum", "name", "cmd", "allowed", "up, down, pull, restart, build")
	}
	c := exec.CommandContext(ctx, "docker", args...)
	c.Env = append(os.Environ(), "COMPOSE_ANSI=never", "COMPOSE_PROGRESS=plain")
	pr, pw := io.Pipe()
	c.Stdout, c.Stderr = pw, pw
	if err := c.Start(); err != nil {
		return err
	}
	done := make(chan struct{})
	go func() {
		sc := bufio.NewScanner(pr)
		for sc.Scan() {
			line(sc.Text())
		}
		close(done)
	}()
	err := c.Wait()
	pw.Close()
	<-done
	if err != nil {
		return errs.New(500, "compose.failed", "cmd", cmd, "name", name, "detail", fmt.Sprint(err))
	}
	return nil
}

// ComposeArgs runs `docker compose <args>` for a project (args after the project flags).
func ComposeArgs(ctx context.Context, p *Project, args []string, line func(string)) error {
	full := []string{"compose", "--project-directory", p.Dir, "-p", p.Name}
	for _, f := range p.ConfigFiles {
		full = append(full, "-f", f)
	}
	c := exec.CommandContext(ctx, "docker", append(full, args...)...)
	c.Env = append(os.Environ(), "COMPOSE_ANSI=never", "COMPOSE_PROGRESS=plain")
	out, err := c.CombinedOutput()
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if l != "" {
			line(l)
		}
	}
	return err
}
