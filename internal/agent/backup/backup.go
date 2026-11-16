// Package backup runs restic (deduplicated, encrypted, incremental) with rclone as the
// transport for cloud drives (方案 §5.11). The panel keeps the repository passwords and
// destination credentials; each call brings what it needs.
package backup

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/wangsirco/berth/internal/sysexec"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/wangsirco/berth/internal/agent"
	"github.com/wangsirco/berth/internal/agent/dbconn"
	"github.com/wangsirco/berth/internal/errs"
)

// Repo is one restic repository and how to reach it.
type Repo struct {
	URL      string            `json:"url"`      // /path, s3:https://…, rclone:berth:path, sftp:…
	Password string            `json:"password"` // restic repository password
	Env      map[string]string `json:"env,omitempty"`
	Rclone   string            `json:"rclone,omitempty"`  // rclone.conf text when URL is rclone:…
	Obscure  []string          `json:"obscure,omitempty"` // rclone options to store obscured (passwords)
}

var StagingRoot = "/srv/berth/backups/staging"

var envKeyRe = regexp.MustCompile(`^(AWS_[A-Z_]+|B2_[A-Z_]+|AZURE_[A-Z_]+|RESTIC_[A-Z_]+|GOOGLE_[A-Z_]+)$`)

// env builds the command environment; it writes the rclone config to a private file.
func (r Repo) env() ([]string, func(), error) {
	cleanup := func() {}
	if r.URL == "" || r.Password == "" {
		return nil, cleanup, errs.New(400, "backup.bad_repo")
	}
	env := append(os.Environ(), "RESTIC_REPOSITORY="+r.URL, "RESTIC_PASSWORD="+r.Password, "RESTIC_CACHE_DIR=/var/cache/berth-restic")
	for k, v := range r.Env {
		if !envKeyRe.MatchString(k) {
			return nil, cleanup, errs.New(400, "backup.bad_repo")
		}
		env = append(env, k+"="+v)
	}
	if r.Rclone != "" {
		conf := r.Rclone
		for _, key := range r.Obscure {
			re := regexp.MustCompile(`(?m)^(` + regexp.QuoteMeta(key) + ` = )(.*)$`)
			if m := re.FindStringSubmatch(conf); m != nil {
				out, err := exec.Command("rclone", "obscure", m[2]).Output()
				if err != nil {
					return nil, cleanup, errs.New(500, "backup.no_restic")
				}
				conf = strings.Replace(conf, m[0], m[1]+strings.TrimSpace(string(out)), 1)
			}
		}
		r.Rclone = conf
		h := sha256.Sum256([]byte(r.Rclone))
		p := filepath.Join(os.TempDir(), "berth-rclone-"+hex.EncodeToString(h[:6])+".conf")
		if err := os.WriteFile(p, []byte(r.Rclone), 0o600); err != nil {
			return nil, cleanup, err
		}
		env = append(env, "RCLONE_CONFIG="+p)
		cleanup = func() { os.Remove(p) }
	}
	return env, cleanup, nil
}

func restic(ctx context.Context, r Repo, line func(string), args ...string) (string, error) {
	env, cleanup, err := r.env()
	if err != nil {
		return "", err
	}
	defer cleanup()
	rn, rargs := sysexec.Wrap("restic", args...)
	c := exec.CommandContext(ctx, rn, rargs...)
	c.Env = env
	var out strings.Builder
	pr, pw := io.Pipe()
	c.Stdout = io.MultiWriter(&out, pw)
	var stderr strings.Builder
	c.Stderr = io.MultiWriter(&stderr, pw)
	if err := c.Start(); err != nil {
		return "", errs.New(500, "backup.no_restic")
	}
	done := make(chan struct{})
	go func() {
		sc := bufio.NewScanner(pr)
		sc.Buffer(make([]byte, 64<<10), 1<<20)
		for sc.Scan() {
			if line != nil {
				line(sc.Text())
			}
		}
		close(done)
	}()
	err = c.Wait()
	pw.Close()
	<-done
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if i := strings.LastIndex(msg, "\n"); i >= 0 && len(msg)-i < 400 {
			msg = msg[i+1:]
		}
		if len(msg) > 400 {
			msg = msg[len(msg)-400:]
		}
		return out.String(), errs.New(500, "backup.restic_failed", "detail", msg)
	}
	return out.String(), nil
}

// Init creates the repository unless it exists.
func Init(ctx context.Context, r Repo) error {
	if _, err := restic(ctx, r, nil, "cat", "config"); err == nil {
		return nil
	}
	_, err := restic(ctx, r, nil, "init")
	return err
}

type Dump struct {
	Container string `json:"container"`
	dbconn.Target
}

type Keep struct {
	Last    int `json:"last,omitempty"`
	Daily   int `json:"daily,omitempty"`
	Weekly  int `json:"weekly,omitempty"`
	Monthly int `json:"monthly,omitempty"`
}

type RunRequest struct {
	Repo      Repo     `json:"repo"`
	Job       string   `json:"job"` // tag and staging folder name
	Paths     []string `json:"paths"`
	Excludes  []string `json:"excludes,omitempty"`
	Dumps     []Dump   `json:"dumps,omitempty"`
	Stop      []string `json:"stop,omitempty"` // containers paused while files are read
	Keep      Keep     `json:"keep"`
	LimitKBps int      `json:"limit_kbps,omitempty"`
	Tags      []string `json:"tags,omitempty"`
}

type RunResult struct {
	Snapshot   string `json:"snapshot"`
	FilesNew   int64  `json:"files_new"`
	FilesTotal int64  `json:"files_total"`
	BytesAdded int64  `json:"bytes_added"`
	BytesTotal int64  `json:"bytes_total"`
	Seconds    int64  `json:"seconds"`
}

var jobRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)

func cleanPaths(ps []string) ([]string, error) {
	var out []string
	for _, p := range ps {
		c := filepath.Clean(p)
		if !filepath.IsAbs(c) || c == "/" {
			return nil, errs.New(400, "backup.bad_path", "path", p)
		}
		if _, err := os.Stat(c); err == nil {
			out = append(out, c)
		}
	}
	return out, nil
}

// Run takes one backup.
func Run(ctx context.Context, req RunRequest, log func(string)) (RunResult, error) {
	start := time.Now()
	var res RunResult
	if !jobRe.MatchString(req.Job) {
		return res, errs.New(400, "backup.bad_job")
	}
	paths, err := cleanPaths(req.Paths)
	if err != nil {
		return res, err
	}
	if err := Init(ctx, req.Repo); err != nil {
		return res, err
	}
	stage := filepath.Join(StagingRoot, req.Job)
	os.RemoveAll(stage)
	defer os.RemoveAll(stage)
	for _, d := range req.Dumps {
		path := filepath.Join(stage, "db", d.Container+"-"+d.Database+".sql.gz")
		log(fmt.Sprintf("::step 0 dump %s/%s", d.Container, d.Database))
		n, err := dbconn.Dump(ctx, d.Container, d.Target, path)
		if err != nil {
			return res, err
		}
		log(fmt.Sprintf("dumped %s (%d bytes)", path, n))
	}
	if len(req.Dumps) > 0 {
		paths = append(paths, stage)
	}
	if len(paths) == 0 {
		return res, errs.New(400, "backup.nothing")
	}
	if len(req.Stop) > 0 {
		log("::step 1 pause " + strings.Join(req.Stop, " "))
		exec.CommandContext(ctx, "docker", append([]string{"stop", "-t", "30"}, req.Stop...)...).Run()
		defer func() {
			exec.Command("docker", append([]string{"start"}, req.Stop...)...).Run()
			log("started " + strings.Join(req.Stop, " "))
		}()
	}
	log("::step 2 backup")
	args := []string{"backup", "--json", "--tag", "berth", "--tag", "job:" + req.Job}
	for _, t := range req.Tags {
		args = append(args, "--tag", t)
	}
	for _, e := range req.Excludes {
		args = append(args, "--exclude", e)
	}
	if req.LimitKBps > 0 {
		args = append(args, "--limit-upload", strconv.Itoa(req.LimitKBps))
	}
	args = append(args, paths...)
	lastPct := -10
	out, err := restic(ctx, req.Repo, func(l string) {
		var m struct {
			MessageType string  `json:"message_type"`
			Percent     float64 `json:"percent_done"`
		}
		if json.Unmarshal([]byte(l), &m) == nil && m.MessageType == "status" {
			if p := int(m.Percent * 100); p >= lastPct+10 {
				lastPct = p
				log(fmt.Sprintf("%d%%", p))
			}
		} else if !strings.HasPrefix(l, "{") {
			log(l)
		}
	}, args...)
	if err != nil {
		return res, err
	}
	for _, l := range strings.Split(out, "\n") {
		var s struct {
			MessageType string `json:"message_type"`
			SnapshotID  string `json:"snapshot_id"`
			FilesNew    int64  `json:"files_new"`
			TotalFiles  int64  `json:"total_files_processed"`
			DataAdded   int64  `json:"data_added"`
			TotalBytes  int64  `json:"total_bytes_processed"`
		}
		if json.Unmarshal([]byte(l), &s) == nil && s.MessageType == "summary" {
			res = RunResult{Snapshot: s.SnapshotID, FilesNew: s.FilesNew, FilesTotal: s.TotalFiles, BytesAdded: s.DataAdded, BytesTotal: s.TotalBytes}
		}
	}
	if k := req.Keep; k.Last+k.Daily+k.Weekly+k.Monthly > 0 {
		log("::step 3 retention")
		args := []string{"forget", "--prune", "--tag", "job:" + req.Job, "--group-by", "tags"}
		for flag, n := range map[string]int{"--keep-last": k.Last, "--keep-daily": k.Daily, "--keep-weekly": k.Weekly, "--keep-monthly": k.Monthly} {
			if n > 0 {
				args = append(args, flag, strconv.Itoa(n))
			}
		}
		if _, err := restic(ctx, req.Repo, log, args...); err != nil {
			log("retention: " + err.Error()) // the backup itself succeeded
		}
	}
	res.Seconds = int64(time.Since(start).Seconds())
	return res, nil
}

type Snapshot struct {
	ID    string    `json:"id"`
	Short string    `json:"short_id"`
	Time  time.Time `json:"time"`
	Paths []string  `json:"paths"`
	Tags  []string  `json:"tags"`
	Host  string    `json:"hostname"`
}

func Snapshots(ctx context.Context, r Repo, job string) ([]Snapshot, error) {
	args := []string{"snapshots", "--json"}
	if job != "" {
		args = append(args, "--tag", "job:"+job)
	}
	out, err := restic(ctx, r, nil, args...)
	if err != nil {
		return nil, err
	}
	var s []Snapshot
	json.Unmarshal([]byte(out), &s)
	if s == nil {
		s = []Snapshot{}
	}
	return s, nil
}

type Node struct {
	Path  string    `json:"path"`
	Name  string    `json:"name"`
	Type  string    `json:"type"`
	Size  int64     `json:"size"`
	MTime time.Time `json:"mtime"`
}

// Ls lists one directory level of a snapshot.
func Ls(ctx context.Context, r Repo, snapshot, dir string) ([]Node, error) {
	if dir == "" {
		dir = "/"
	}
	out, err := restic(ctx, r, nil, "ls", "--json", snapshot, filepath.Clean(dir))
	if err != nil {
		return nil, err
	}
	var nodes []Node
	want := filepath.Clean(dir)
	for _, l := range strings.Split(out, "\n") {
		var n struct {
			StructType string `json:"struct_type"`
			Node
		}
		if json.Unmarshal([]byte(l), &n) != nil || n.StructType != "node" {
			continue
		}
		if filepath.Dir(n.Path) == want && n.Path != want {
			nodes = append(nodes, n.Node)
		}
	}
	if nodes == nil {
		nodes = []Node{}
	}
	return nodes, nil
}

type RestoreRequest struct {
	Repo     Repo     `json:"repo"`
	Job      string   `json:"job"`
	Snapshot string   `json:"snapshot"`
	Target   string   `json:"target"`            // "/" = original location
	Include  []string `json:"include,omitempty"` // only these paths
	// Before restoring over the original location the current state is backed up.
	SafetyPaths []string `json:"safety_paths,omitempty"`
}

func Restore(ctx context.Context, req RestoreRequest, log func(string)) (string, error) {
	target := filepath.Clean(req.Target)
	if !filepath.IsAbs(target) || !regexp.MustCompile(`^[0-9a-f]{8,64}$|^latest$`).MatchString(req.Snapshot) {
		return "", errs.New(400, "backup.bad_restore")
	}
	safety := ""
	if target == "/" && len(req.SafetyPaths) > 0 {
		log("::step 0 safety snapshot")
		res, err := Run(ctx, RunRequest{Repo: req.Repo, Job: req.Job, Paths: req.SafetyPaths, Tags: []string{"pre-restore"}}, log)
		if err != nil {
			return "", err
		}
		safety = res.Snapshot
		log("current state saved as snapshot " + safety)
	}
	log("::step 1 restore")
	args := []string{"restore", req.Snapshot, "--target", target, "--verify"}
	for _, inc := range req.Include {
		args = append(args, "--include", filepath.Clean(inc))
	}
	if _, err := restic(ctx, req.Repo, log, args...); err != nil {
		return safety, err
	}
	return safety, nil
}

// Drill restores the latest snapshot of a job to a scratch folder, verifies it and checks
// a sample of the repository's data.
func Drill(ctx context.Context, r Repo, job string, log func(string)) (map[string]any, error) {
	snaps, err := Snapshots(ctx, r, job)
	if err != nil {
		return nil, err
	}
	if len(snaps) == 0 {
		return nil, errs.New(409, "backup.no_snapshot")
	}
	latest := snaps[len(snaps)-1]
	dir := filepath.Join("/srv/berth/backups/drill", job)
	os.RemoveAll(dir)
	defer os.RemoveAll(dir)
	log("::step 0 restore " + latest.Short)
	if _, err := restic(ctx, r, log, "restore", latest.ID, "--target", dir, "--verify"); err != nil {
		return nil, err
	}
	files, bytes, dumps, bad := 0, int64(0), 0, []string{}
	filepath.Walk(dir, func(p string, fi os.FileInfo, err error) error {
		if err == nil && fi.Mode().IsRegular() {
			files++
			bytes += fi.Size()
			if strings.HasSuffix(p, ".sql.gz") {
				dumps++
				if exec.Command("gzip", "-t", p).Run() != nil {
					bad = append(bad, strings.TrimPrefix(p, dir))
				}
			}
		}
		return nil
	})
	if len(bad) > 0 {
		return nil, errs.New(500, "backup.drill_corrupt", "files", strings.Join(bad, ", "))
	}
	log("::step 1 check data sample")
	if _, err := restic(ctx, r, log, "check", "--read-data-subset=5%"); err != nil {
		return nil, err
	}
	return map[string]any{"snapshot": latest.Short, "time": latest.Time, "files": files, "bytes": bytes, "dumps": dumps}, nil
}

// ---- tools ----

var (
	toolsMu   sync.Mutex
	toolsLast map[string]string
	toolsAt   time.Time
)

// Tools: the installed versions, detected at most once a minute (and one run at a time: each
// probe is a large binary counted against the agent's memory limit).
func Tools() map[string]string {
	toolsMu.Lock()
	defer toolsMu.Unlock()
	if toolsLast != nil && time.Since(toolsAt) < time.Minute {
		return toolsLast
	}
	toolsLast, toolsAt = detectTools(), time.Now()
	return toolsLast
}

// ForgetTools makes the next Tools detect again (after installing them).
func ForgetTools() {
	toolsMu.Lock()
	toolsLast = nil
	toolsMu.Unlock()
}

func detectTools() map[string]string {
	out := map[string]string{}
	for _, t := range []string{"restic", "rclone"} {
		if b, err := exec.Command(t, "version").Output(); err == nil {
			out[t] = strings.TrimSpace(strings.SplitN(string(b), "\n", 2)[0])
		}
	}
	return out
}

// ---- routes ----

func Register(s *agent.Server) {
	stream := func(w http.ResponseWriter, fn func(log func(string)) (any, error)) {
		st := agent.NewStream(w)
		log := func(l string) { st.Send(map[string]string{"l": l}) }
		v, err := fn(log)
		if err == nil {
			b, _ := json.Marshal(v)
			log("::result " + string(b))
		}
		st.End(err)
	}
	s.Handle("GET /v1/backup/tools", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		agent.WriteJSON(w, 200, Tools())
	})
	s.Handle("POST /v1/backup/tools/install", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		stream(w, func(log func(string)) (any, error) {
			var c *exec.Cmd
			switch {
			case has("dnf"):
				n, a := sysexec.Wrap("dnf", "-y", "-q", "install", "restic", "rclone")
				c = exec.CommandContext(r.Context(), n, a...)
			case has("apt-get"):
				n, a := sysexec.Scoped("sh", "-c", "apt-get update -q && DEBIAN_FRONTEND=noninteractive apt-get install -y -q restic rclone")
				c = exec.CommandContext(r.Context(), n, a...)
			default:
				return nil, errs.New(409, "backup.no_package_manager")
			}
			out, err := c.CombinedOutput()
			for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
				log(l)
			}
			if err != nil {
				return nil, errs.New(500, "backup.tools_failed", "detail", err.Error())
			}
			ForgetTools()
			return Tools(), nil
		})
	})
	s.Handle("POST /v1/backup/init", func(w http.ResponseWriter, r *http.Request, body []byte) {
		var in Repo
		json.Unmarshal(body, &in)
		if err := Init(r.Context(), in); err != nil {
			agent.WriteErr(w, err)
			return
		}
		agent.WriteJSON(w, 200, map[string]bool{"ok": true})
	})
	s.Handle("POST /v1/backup/run", func(w http.ResponseWriter, r *http.Request, body []byte) {
		var in RunRequest
		json.Unmarshal(body, &in)
		stream(w, func(log func(string)) (any, error) { return Run(r.Context(), in, log) })
	})
	s.Handle("POST /v1/backup/snapshots", func(w http.ResponseWriter, r *http.Request, body []byte) {
		var in struct {
			Repo Repo   `json:"repo"`
			Job  string `json:"job"`
		}
		json.Unmarshal(body, &in)
		v, err := Snapshots(r.Context(), in.Repo, in.Job)
		if err != nil {
			agent.WriteErr(w, err)
			return
		}
		agent.WriteJSON(w, 200, v)
	})
	s.Handle("POST /v1/backup/ls", func(w http.ResponseWriter, r *http.Request, body []byte) {
		var in struct {
			Repo     Repo   `json:"repo"`
			Snapshot string `json:"snapshot"`
			Dir      string `json:"dir"`
		}
		json.Unmarshal(body, &in)
		v, err := Ls(r.Context(), in.Repo, in.Snapshot, in.Dir)
		if err != nil {
			agent.WriteErr(w, err)
			return
		}
		agent.WriteJSON(w, 200, v)
	})
	s.Handle("POST /v1/backup/restore", func(w http.ResponseWriter, r *http.Request, body []byte) {
		var in RestoreRequest
		json.Unmarshal(body, &in)
		stream(w, func(log func(string)) (any, error) {
			safety, err := Restore(r.Context(), in, log)
			return map[string]string{"safety_snapshot": safety}, err
		})
	})
	s.Handle("POST /v1/backup/drill", func(w http.ResponseWriter, r *http.Request, body []byte) {
		var in struct {
			Repo Repo   `json:"repo"`
			Job  string `json:"job"`
		}
		json.Unmarshal(body, &in)
		stream(w, func(log func(string)) (any, error) { return Drill(r.Context(), in.Repo, in.Job, log) })
	})
}

func has(bin string) bool { _, err := exec.LookPath(bin); return err == nil }
