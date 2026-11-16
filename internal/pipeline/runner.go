package pipeline

// The runner executes a pipeline on a build host (inside berth-agent, as root): it
// prepares the workspace, runs every step in its own container with the runner's
// memory and CPU limits, keeps cache directories between runs, and builds images with
// buildx (other architectures through QEMU when the host has binfmt).

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

type Source struct {
	Type  string `json:"type"` // git | upload
	URL   string `json:"url,omitempty"`
	Ref   string `json:"ref,omitempty"` // branch, tag or commit
	Token string `json:"token,omitempty"`
}

type RunRequest struct {
	RunID    string `json:"run_id"`
	Pipeline string `json:"pipeline"`
	Source   Source `json:"source"`
	Spec     Spec   `json:"spec"`
	// SpecFromRepo: read berth.pipeline.yaml from the checked-out source instead
	SpecFromRepo bool   `json:"spec_from_repo,omitempty"`
	Version      string `json:"version"` // image tag for this run
	// Registry replaces "registry.berth.lan" in image names (the actual address)
	Registry string `json:"registry,omitempty"`
}

type StepResult struct {
	Name   string `json:"name"`
	Status string `json:"status"` // ok | failed | skipped
	MS     int64  `json:"ms"`
}

type Result struct {
	Commit string       `json:"commit,omitempty"`
	Image  string       `json:"image,omitempty"` // repository:version that was pushed
	Digest string       `json:"digest,omitempty"`
	Steps  []StepResult `json:"steps"`
}

type Executor struct {
	Root   string // /srv/berth/workspaces
	Docker string // docker CLI
}

func NewExecutor(root string) *Executor { return &Executor{Root: root, Docker: "docker"} }

func (r *Executor) dir(pipeline string, parts ...string) string {
	return filepath.Join(append([]string{r.Root, pipeline}, parts...)...)
}

// UploadPath is where `berth push` archives are stored before a run.
func (r *Executor) UploadPath(pipeline string) string { return r.dir(pipeline, "upload.tar.gz") }

// run runs a command and sends its output lines to log.
func run(ctx context.Context, log func(string), dir string, env []string, name string, args ...string) error {
	c := exec.CommandContext(ctx, name, args...)
	c.Dir = dir
	c.Env = append(os.Environ(), env...)
	pr, pw := io.Pipe()
	c.Stdout, c.Stderr = pw, pw
	if err := c.Start(); err != nil {
		return err
	}
	done := make(chan struct{})
	go func() {
		sc := bufio.NewScanner(pr)
		sc.Buffer(make([]byte, 64<<10), 1<<20)
		for sc.Scan() {
			log(sc.Text())
		}
		close(done)
	}()
	err := c.Wait()
	pw.Close()
	<-done
	return err
}

func (r *Executor) prepare(ctx context.Context, req RunRequest, log func(string)) (string, error) {
	src := r.dir(req.Pipeline, "src")
	switch req.Source.Type {
	case "git":
		if _, err := exec.LookPath("git"); err != nil {
			return "", fmt.Errorf("git is not installed on this host")
		}
		if err := os.MkdirAll(src, 0o755); err != nil {
			return "", err
		}
		if _, err := os.Stat(filepath.Join(src, ".git")); err != nil {
			if err := run(ctx, log, src, nil, "git", "init", "-q"); err != nil {
				return "", err
			}
		}
		ref := req.Source.Ref
		if ref == "" {
			ref = "HEAD"
		}
		args := []string{}
		if req.Source.Token != "" { // passed per command, never written to .git/config
			auth := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + req.Source.Token))
			args = append(args, "-c", "http.extraHeader=Authorization: Basic "+auth)
		}
		args = append(args, "fetch", "--depth", "50", "--no-tags", req.Source.URL, ref)
		if err := run(ctx, log, src, []string{"GIT_TERMINAL_PROMPT=0"}, "git", args...); err != nil {
			return "", fmt.Errorf("git fetch: %w", err)
		}
		if err := run(ctx, log, src, nil, "git", "checkout", "-q", "-f", "FETCH_HEAD"); err != nil {
			return "", err
		}
		if err := run(ctx, log, src, nil, "git", "clean", "-q", "-ffdx"); err != nil {
			return "", err
		}
		out, _ := exec.CommandContext(ctx, "git", "-C", src, "rev-parse", "--short=12", "HEAD").Output()
		return strings.TrimSpace(string(out)), nil
	case "upload":
		f, err := os.Open(r.UploadPath(req.Pipeline))
		if err != nil {
			return "", fmt.Errorf("no uploaded source (run berth push)")
		}
		defer f.Close()
		if err := os.RemoveAll(src); err != nil {
			return "", err
		}
		if err := Untar(f, src); err != nil {
			return "", err
		}
		return "", nil
	}
	return "", fmt.Errorf("unknown source type %q", req.Source.Type)
}

// Untar extracts a .tar.gz into dir, refusing entries that escape it.
func Untar(r io.Reader, dir string) error {
	zr, err := gzip.NewReader(r)
	if err != nil {
		return err
	}
	tr := tar.NewReader(zr)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	var total int64
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		name := filepath.Clean(h.Name)
		if filepath.IsAbs(name) || name == ".." || strings.HasPrefix(name, "../") {
			return fmt.Errorf("archive entry escapes the workspace: %s", h.Name)
		}
		p := filepath.Join(dir, name)
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(p, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			total += h.Size
			if total > 4<<30 {
				return fmt.Errorf("archive too large")
			}
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				return err
			}
			f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(h.Mode)&0o777|0o600)
			if err != nil {
				return err
			}
			if _, err := io.Copy(f, io.LimitReader(tr, h.Size)); err != nil {
				f.Close()
				return err
			}
			f.Close()
		case tar.TypeSymlink:
			t := h.Linkname
			if filepath.IsAbs(t) || strings.HasPrefix(filepath.Clean(filepath.Join(filepath.Dir(name), t)), "..") {
				continue // links out of the workspace are dropped
			}
			os.MkdirAll(filepath.Dir(p), 0o755)
			os.Symlink(t, p)
		}
	}
}

func cacheKey(p string) string { return strings.ReplaceAll(filepath.Clean(p), "/", "__") }

func envArgs(envs ...Env) []string {
	var out []string
	for _, e := range envs {
		for k, v := range e {
			out = append(out, "-e", k+"="+v)
		}
	}
	return out
}

func memArg(s Spec) string { return strconv.FormatInt(s.Runner.MemoryBytes(), 10) }

// swapArg lets a step use as much swap (zram on small hosts) as memory before it is
// killed: a build that peaks briefly slows down instead of failing.
func swapArg(s Spec) string { return strconv.FormatInt(2*s.Runner.MemoryBytes(), 10) }

// Run executes the pipeline. Log lines starting with "::" are markers for the panel:
// ::step <n> <name>, ::note <code>, ::result <json>.
func (r *Executor) Run(ctx context.Context, req RunRequest, log func(string)) (Result, error) {
	res := Result{Steps: []StepResult{}}
	log("::step 0 source")
	commit, err := r.prepare(ctx, req, log)
	if err != nil {
		return res, err
	}
	res.Commit = commit
	src := r.dir(req.Pipeline, "src")
	if req.SpecFromRepo {
		b, err := os.ReadFile(filepath.Join(src, "berth.pipeline.yaml"))
		if err != nil {
			return res, fmt.Errorf("berth.pipeline.yaml not found in the repository")
		}
		spec, err := Parse(b)
		if err != nil {
			return res, err
		}
		req.Spec = *spec
		sj, _ := json.Marshal(spec)
		log("::spec " + string(sj))
	}
	if req.Registry != "" {
		for i := range req.Spec.Steps {
			if db := req.Spec.Steps[i].DockerBuild; db != nil {
				db.Push = RewriteRegistry(db.Push, req.Registry)
			}
		}
	}
	for i, st := range req.Spec.Steps {
		log(fmt.Sprintf("::step %d %s", i+1, st.Name))
		start := time.Now()
		sctx, cancel := ctx, func() {}
		if st.Timeout != "" {
			d, _ := time.ParseDuration(st.Timeout)
			sctx, cancel = context.WithTimeout(ctx, d)
		}
		var err error
		if st.DockerBuild != nil {
			err = r.build(sctx, req, st, src, &res, log)
		} else {
			err = r.step(sctx, req, i, st, src, log)
		}
		cancel()
		sr := StepResult{Name: st.Name, Status: "ok", MS: time.Since(start).Milliseconds()}
		if err != nil {
			sr.Status = "failed"
			res.Steps = append(res.Steps, sr)
			for _, rest := range req.Spec.Steps[i+1:] {
				res.Steps = append(res.Steps, StepResult{Name: rest.Name, Status: "skipped"})
			}
			return res, fmt.Errorf("step %q: %w", st.Name, err)
		}
		res.Steps = append(res.Steps, sr)
	}
	return res, nil
}

func (r *Executor) step(ctx context.Context, req RunRequest, i int, st Step, src string, log func(string)) error {
	name := fmt.Sprintf("berth-run-%s-%d", req.RunID, i+1)
	args := []string{"run", "--rm", "--name", name, "-m", memArg(req.Spec), "--memory-swap", swapArg(req.Spec),
		"-v", src + ":/workspace", "-w", "/workspace", "--label", "io.berth.pipeline=" + req.Pipeline}
	if req.Spec.Runner.CPUs > 0 {
		args = append(args, "--cpus", strconv.FormatFloat(req.Spec.Runner.CPUs, 'f', -1, 64))
	}
	if st.Platform != "" {
		args = append(args, "--platform", st.Platform)
		if !strings.HasSuffix(st.Platform, "/"+runtime.GOARCH) {
			log("::note pipeline.emulated")
		}
	}
	// a cache declared on any step is mounted in every step (later steps use what
	// earlier ones installed)
	seen := map[string]bool{}
	for _, other := range req.Spec.Steps {
		for _, c := range other.Cache {
			c = filepath.Clean(c)
			if seen[c] {
				continue
			}
			seen[c] = true
			host := r.dir(req.Pipeline, "cache", cacheKey(c))
			if err := os.MkdirAll(host, 0o755); err != nil {
				return err
			}
			args = append(args, "-v", host+":/workspace/"+c)
		}
	}
	args = append(args, envArgs(req.Spec.Env, st.Env, Env{"BERTH_RUN": req.RunID, "BERTH_VERSION": req.Version, "CI": "true"})...)
	args = append(args, "--entrypoint", "/bin/sh", st.Image, "-c", st.Run)
	err := run(ctx, log, src, nil, r.Docker, args...)
	if ctx.Err() != nil { // timeout or cancel: make sure the container is gone
		exec.Command(r.Docker, "rm", "-f", name).Run()
		return ctx.Err()
	}
	return err
}

func (r *Executor) build(ctx context.Context, req RunRequest, st Step, src string, res *Result, log func(string)) error {
	db := st.DockerBuild
	meta := filepath.Join(r.dir(req.Pipeline), "build-meta.json")
	os.Remove(meta)
	args := []string{"buildx", "build", "--progress", "plain", "--metadata-file", meta}
	if len(db.Platforms) > 0 {
		args = append(args, "--platform", strings.Join(db.Platforms, ","))
		for _, p := range db.Platforms {
			if !strings.HasSuffix(p, "/"+runtime.GOARCH) {
				log("::note pipeline.cross_build")
				break
			}
		}
	}
	if db.File != "" {
		args = append(args, "-f", filepath.Join(src, db.File))
	}
	if db.Target != "" {
		args = append(args, "--target", db.Target)
	}
	for k, v := range db.BuildArgs {
		args = append(args, "--build-arg", k+"="+v)
	}
	if db.Push != "" {
		tag := db.Push + ":" + req.Version
		args = append(args, "-t", tag, "-t", db.Push+":latest", "--push")
		res.Image = tag
	}
	args = append(args, filepath.Join(src, db.Context))
	if err := run(ctx, log, src, nil, r.Docker, args...); err != nil {
		return err
	}
	var m map[string]any
	if b, err := os.ReadFile(meta); err == nil && json.Unmarshal(b, &m) == nil {
		if d, ok := m["containerimage.digest"].(string); ok && db.Push != "" {
			res.Digest = d
		}
	}
	return nil
}

// RewriteRegistry points images named after the internal registry's DNS name at its
// actual address (hosts trust it by address).
func RewriteRegistry(image, addr string) string {
	if addr != "" && strings.HasPrefix(image, "registry.berth.lan/") {
		return addr + strings.TrimPrefix(image, "registry.berth.lan")
	}
	return image
}
