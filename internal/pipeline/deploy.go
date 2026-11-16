package pipeline

// Releasing to a compose service on the target host (方案 §5.10.3): pull the new image,
// remember the current one, change only that service's image line, start it, check its
// health, and put the old image back when the check fails.

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/wangsirco/berth/internal/agent"
	"github.com/wangsirco/berth/internal/errs"
)

type DeployRequest struct {
	Compose     string      `json:"compose"`
	Service     string      `json:"service"`
	Image       string      `json:"image"`
	Healthcheck Healthcheck `json:"healthcheck"`
	Rollback    string      `json:"rollback"`
	PreDeploy   string      `json:"pre_deploy,omitempty"`
	PostDeploy  string      `json:"post_deploy,omitempty"`
	SkipPull    bool        `json:"skip_pull,omitempty"`
}

type DeployResult struct {
	OldImage   string `json:"old_image"`
	NewImage   string `json:"new_image"`
	RolledBack bool   `json:"rolled_back"`
	MS         int64  `json:"ms"`
}

// imageLine finds the line of services.<service>.image in a compose file.
func imageLine(src []byte, service string) (line, col int, value string, err error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(src, &doc); err != nil {
		return 0, 0, "", err
	}
	if len(doc.Content) == 0 {
		return 0, 0, "", fmt.Errorf("empty compose file")
	}
	find := func(m *yaml.Node, key string) *yaml.Node {
		if m == nil || m.Kind != yaml.MappingNode {
			return nil
		}
		for i := 0; i+1 < len(m.Content); i += 2 {
			if m.Content[i].Value == key {
				return m.Content[i+1]
			}
		}
		return nil
	}
	svc := find(find(doc.Content[0], "services"), service)
	if svc == nil {
		return 0, 0, "", errs.New(404, "deploy.no_service", "service", service)
	}
	img := find(svc, "image")
	if img == nil || img.Kind != yaml.ScalarNode {
		return 0, 0, "", errs.New(400, "deploy.no_image", "service", service)
	}
	if strings.Contains(img.Value, "${") {
		return 0, 0, "", errs.New(400, "deploy.image_is_variable", "service", service, "value", img.Value)
	}
	return img.Line, img.Column, img.Value, nil
}

// SetImage returns src with the service's image replaced, everything else untouched.
func SetImage(src []byte, service, image string) ([]byte, string, error) {
	line, col, old, err := imageLine(src, service)
	if err != nil {
		return nil, "", err
	}
	lines := strings.SplitAfter(string(src), "\n")
	l := lines[line-1]
	end := strings.TrimRight(l, "\r\n")
	nl := l[len(end):]
	// keep a trailing comment, if any
	rest := end[col-1:]
	comment := ""
	if i := strings.Index(rest, " #"); i >= 0 {
		comment = rest[len(strings.TrimRight(rest[:i+1], " \t")):] // with the spaces before it
	}
	lines[line-1] = end[:col-1] + image + comment + nl
	return []byte(strings.Join(lines, "")), old, nil
}

func healthy(ctx context.Context, url string) bool {
	hc := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}, // the app's own cert may be anything
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
	resp, err := hc.Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode < 400
}

func (x *Executor) containerOK(ctx context.Context, dir, file, service string) bool {
	out, err := exec.CommandContext(ctx, x.Docker, "compose", "--project-directory", dir, "-f", file, "ps", "--format", "json", service).Output()
	if err != nil {
		return false
	}
	ok := false
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		var c struct {
			State  string
			Health string
		}
		if json.Unmarshal([]byte(l), &c) == nil {
			ok = c.State == "running" && c.Health != "unhealthy"
		}
	}
	return ok
}

func (x *Executor) up(ctx context.Context, log func(string), dir, file, service string) error {
	return run(ctx, log, dir, []string{"COMPOSE_ANSI=never", "COMPOSE_PROGRESS=plain"}, x.Docker,
		"compose", "--project-directory", dir, "-f", file, "up", "-d", "--no-deps", service)
}

func (x *Executor) waitHealthy(ctx context.Context, req DeployRequest, dir string, log func(string)) bool {
	timeout := 60 * time.Second
	if d, err := time.ParseDuration(req.Healthcheck.Timeout); err == nil && d > 0 {
		timeout = d
	}
	deadline := time.Now().Add(timeout)
	stable := 0
	for time.Now().Before(deadline) {
		if ctx.Err() != nil {
			return false
		}
		ok := x.containerOK(ctx, dir, req.Compose, req.Service)
		if ok && req.Healthcheck.URL != "" {
			ok = healthy(ctx, req.Healthcheck.URL)
		}
		if ok {
			stable++
			if req.Healthcheck.URL != "" || stable >= 3 { // without a URL: running for a few checks
				return true
			}
		} else {
			stable = 0
		}
		time.Sleep(3 * time.Second)
	}
	log("health check did not pass within " + timeout.String())
	return false
}

// Deploy runs one release. The error is set when the new version is not running.
func (x *Executor) Deploy(ctx context.Context, req DeployRequest, log func(string)) (DeployResult, error) {
	start := time.Now()
	res := DeployResult{NewImage: req.Image}
	if !filepath.IsAbs(req.Compose) {
		return res, errs.New(400, "deploy.bad_compose")
	}
	src, err := os.ReadFile(req.Compose)
	if err != nil {
		return res, errs.New(404, "deploy.no_compose", "path", req.Compose)
	}
	dir := filepath.Dir(req.Compose)
	newSrc, old, err := SetImage(src, req.Service, req.Image)
	if err != nil {
		return res, err
	}
	res.OldImage = old
	if req.PreDeploy != "" {
		log("::note deploy.pre")
		if err := run(ctx, log, dir, nil, "sh", "-c", req.PreDeploy); err != nil {
			return res, errs.New(500, "deploy.pre_failed", "detail", err.Error())
		}
	}
	if !req.SkipPull {
		if err := run(ctx, log, dir, nil, x.Docker, "pull", req.Image); err != nil {
			return res, errs.New(502, "deploy.pull_failed", "image", req.Image)
		}
	}
	if err := os.WriteFile(req.Compose+".berth-prev", src, 0o600); err != nil {
		return res, err
	}
	writeCompose := func(b []byte) error {
		tmp := req.Compose + ".berth-tmp"
		fi, _ := os.Stat(req.Compose)
		mode := os.FileMode(0o644)
		if fi != nil {
			mode = fi.Mode().Perm()
		}
		if err := os.WriteFile(tmp, b, mode); err != nil {
			return err
		}
		return os.Rename(tmp, req.Compose)
	}
	if err := writeCompose(newSrc); err != nil {
		return res, err
	}
	log(fmt.Sprintf("%s: %s → %s", req.Service, old, req.Image))
	upErr := x.up(ctx, log, dir, req.Compose, req.Service)
	ok := upErr == nil && x.waitHealthy(ctx, req, dir, log)
	if ok {
		if req.PostDeploy != "" {
			log("::note deploy.post")
			if err := run(ctx, log, dir, nil, "sh", "-c", req.PostDeploy); err != nil {
				log("post_deploy failed: " + err.Error()) // the release itself is up
			}
		}
		res.MS = time.Since(start).Milliseconds()
		return res, nil
	}
	if req.Rollback == "manual" {
		res.MS = time.Since(start).Milliseconds()
		return res, errs.New(500, "deploy.unhealthy", "service", req.Service)
	}
	log("::note deploy.rolling_back")
	if err := writeCompose(src); err != nil {
		return res, err
	}
	rbCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Minute)
	defer cancel()
	if err := x.up(rbCtx, log, dir, req.Compose, req.Service); err != nil {
		return res, errs.New(500, "deploy.rollback_failed", "detail", err.Error())
	}
	res.RolledBack = true
	res.MS = time.Since(start).Milliseconds()
	return res, errs.New(500, "deploy.rolled_back", "service", req.Service, "image", old)
}

// CurrentImage reads the image a compose service is set to.
func CurrentImage(compose, service string) (string, error) {
	src, err := os.ReadFile(compose)
	if err != nil {
		return "", errs.New(404, "deploy.no_compose", "path", compose)
	}
	_, _, v, err := imageLine(src, service)
	return v, err
}

func registerDeploy(s *agent.Server, x *Executor) {
	s.Handle("POST /v1/deploy/compose", func(w http.ResponseWriter, r *http.Request, body []byte) {
		var req DeployRequest
		if json.Unmarshal(body, &req) != nil || req.Service == "" || req.Image == "" {
			agent.WriteErr(w, errs.New(400, "params.invalid_json"))
			return
		}
		st := agent.NewStream(w)
		log := func(l string) { st.Send(map[string]string{"l": l}) }
		res, err := x.Deploy(r.Context(), req, log)
		b, _ := json.Marshal(res)
		log("::result " + string(b))
		st.End(err)
	})
	s.Handle("GET /v1/deploy/current", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		img, err := CurrentImage(r.URL.Query().Get("compose"), r.URL.Query().Get("service"))
		if err != nil {
			agent.WriteErr(w, err)
			return
		}
		agent.WriteJSON(w, 200, map[string]string{"image": img})
	})
}
