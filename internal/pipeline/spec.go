// Package pipeline defines berth.pipeline.yaml (方案 §5.10) and runs its steps.
package pipeline

import (
	"fmt"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

type Spec struct {
	Name   string   `yaml:"name" json:"name"`
	Runner Runner   `yaml:"runner" json:"runner"`
	Env    Env      `yaml:"env,omitempty" json:"env,omitempty"`
	Steps  []Step   `yaml:"steps" json:"steps"`
	Deploy []Deploy `yaml:"deploy,omitempty" json:"deploy,omitempty"`
}

type Runner struct {
	Memory string  `yaml:"memory" json:"memory"` // 6g, 512m
	CPUs   float64 `yaml:"cpus" json:"cpus"`
	Host   string  `yaml:"host,omitempty" json:"host,omitempty"` // which agent builds (default: the panel's host)
}

type Env map[string]string

type Step struct {
	Name        string       `yaml:"name" json:"name"`
	Image       string       `yaml:"image,omitempty" json:"image,omitempty"`
	Run         string       `yaml:"run,omitempty" json:"run,omitempty"`
	Cache       []string     `yaml:"cache,omitempty" json:"cache,omitempty"`
	Env         Env          `yaml:"env,omitempty" json:"env,omitempty"`
	Platform    string       `yaml:"platform,omitempty" json:"platform,omitempty"` // run under emulation, e.g. linux/amd64
	Timeout     string       `yaml:"timeout,omitempty" json:"timeout,omitempty"`
	DockerBuild *DockerBuild `yaml:"docker_build,omitempty" json:"docker_build,omitempty"`
}

type DockerBuild struct {
	Context   string   `yaml:"context" json:"context"`
	File      string   `yaml:"file,omitempty" json:"file,omitempty"`
	Target    string   `yaml:"target,omitempty" json:"target,omitempty"`
	Platforms []string `yaml:"platforms,omitempty" json:"platforms,omitempty"`
	Push      string   `yaml:"push,omitempty" json:"push,omitempty"` // repository, e.g. registry.berth.lan/shop/web
	BuildArgs Env      `yaml:"build_args,omitempty" json:"build_args,omitempty"`
}

type Deploy struct {
	Host        string      `yaml:"host" json:"host"`
	Compose     string      `yaml:"compose" json:"compose"` // path of the compose file on that host
	Service     string      `yaml:"service" json:"service"`
	Healthcheck Healthcheck `yaml:"healthcheck,omitempty" json:"healthcheck,omitempty"`
	Rollback    string      `yaml:"rollback,omitempty" json:"rollback,omitempty"` // auto (default) | manual
	PreDeploy   string      `yaml:"pre_deploy,omitempty" json:"pre_deploy,omitempty"`
	PostDeploy  string      `yaml:"post_deploy,omitempty" json:"post_deploy,omitempty"`
}

type Healthcheck struct {
	URL     string `yaml:"url,omitempty" json:"url,omitempty"`
	Timeout string `yaml:"timeout,omitempty" json:"timeout,omitempty"`
}

// Problem is a validation error with an i18n code.
type Problem struct {
	Code   string
	Params map[string]any
}

func (p *Problem) Error() string { return fmt.Sprintf("%s %v", p.Code, p.Params) }

func bad(code string, kv ...any) *Problem {
	p := &Problem{Code: code, Params: map[string]any{}}
	for i := 0; i+1 < len(kv); i += 2 {
		p.Params[kv[i].(string)] = kv[i+1]
	}
	return p
}

var (
	nameRe     = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,47}$`)
	stepRe     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 _.-]{0,47}$`)
	envKeyRe   = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,63}$`)
	imageRe    = regexp.MustCompile(`^[a-z0-9][a-z0-9._/:@-]{0,254}$`)
	platformRe = regexp.MustCompile(`^linux/(amd64|arm64|arm/v7|386)$`)
	memRe      = regexp.MustCompile(`^([0-9]+)([kmg]?)$`)
)

// Parse reads and validates a pipeline definition.
func Parse(b []byte) (*Spec, error) {
	var s Spec
	dec := yaml.NewDecoder(strings.NewReader(string(b)))
	dec.KnownFields(true)
	if err := dec.Decode(&s); err != nil {
		return nil, bad("pipeline.bad_yaml", "detail", err.Error())
	}
	return &s, s.Validate()
}

// MemoryBytes parses runner.memory (default 2g).
func (r Runner) MemoryBytes() int64 {
	m := memRe.FindStringSubmatch(strings.ToLower(r.Memory))
	if m == nil {
		return 2 << 30
	}
	n, _ := strconv.ParseInt(m[1], 10, 64)
	switch m[2] {
	case "k":
		n <<= 10
	case "m":
		n <<= 20
	case "g":
		n <<= 30
	}
	return n
}

func safeRel(p string) bool {
	c := path.Clean(p)
	return p != "" && !path.IsAbs(p) && c != ".." && !strings.HasPrefix(c, "../")
}

func (s *Spec) Validate() error {
	if !nameRe.MatchString(s.Name) {
		return bad("pipeline.bad_name")
	}
	if s.Runner.Memory != "" && !memRe.MatchString(strings.ToLower(s.Runner.Memory)) {
		return bad("pipeline.bad_memory", "value", s.Runner.Memory)
	}
	if mb := s.Runner.MemoryBytes(); mb < 64<<20 || mb > 256<<30 {
		return bad("pipeline.bad_memory", "value", s.Runner.Memory)
	}
	if s.Runner.CPUs < 0 || s.Runner.CPUs > 256 {
		return bad("pipeline.bad_cpus")
	}
	if len(s.Steps) == 0 || len(s.Steps) > 50 {
		return bad("pipeline.no_steps")
	}
	for k := range s.Env {
		if !envKeyRe.MatchString(k) {
			return bad("pipeline.bad_env", "name", k)
		}
	}
	seen := map[string]bool{}
	for i := range s.Steps {
		st := &s.Steps[i]
		if st.Name == "" {
			st.Name = fmt.Sprintf("step %d", i+1)
		}
		if !stepRe.MatchString(st.Name) || seen[st.Name] {
			return bad("pipeline.bad_step_name", "step", st.Name)
		}
		seen[st.Name] = true
		for k := range st.Env {
			if !envKeyRe.MatchString(k) {
				return bad("pipeline.bad_env", "name", k)
			}
		}
		if st.Timeout != "" {
			if d, err := time.ParseDuration(st.Timeout); err != nil || d <= 0 {
				return bad("pipeline.bad_timeout", "step", st.Name)
			}
		}
		if st.Platform != "" && !platformRe.MatchString(st.Platform) {
			return bad("pipeline.bad_platform", "value", st.Platform)
		}
		for _, c := range st.Cache {
			if !safeRel(c) {
				return bad("pipeline.bad_cache", "path", c)
			}
		}
		switch {
		case st.DockerBuild != nil && (st.Run != "" || st.Image != ""):
			return bad("pipeline.step_kind", "step", st.Name)
		case st.DockerBuild != nil:
			db := st.DockerBuild
			if db.Context == "" {
				db.Context = "."
			}
			if !safeRel(db.Context) || (db.File != "" && !safeRel(db.File)) {
				return bad("pipeline.bad_context", "step", st.Name)
			}
			for _, p := range db.Platforms {
				if !platformRe.MatchString(p) {
					return bad("pipeline.bad_platform", "value", p)
				}
			}
			if db.Push != "" && !imageRe.MatchString(db.Push) {
				return bad("pipeline.bad_image", "value", db.Push)
			}
		case st.Run == "" || st.Image == "":
			return bad("pipeline.step_kind", "step", st.Name)
		default:
			if !imageRe.MatchString(st.Image) {
				return bad("pipeline.bad_image", "value", st.Image)
			}
		}
	}
	for i, d := range s.Deploy {
		if d.Host == "" || !path.IsAbs(d.Compose) || d.Service == "" {
			return bad("pipeline.bad_deploy", "index", i+1)
		}
		if d.Rollback != "" && d.Rollback != "auto" && d.Rollback != "manual" {
			return bad("pipeline.bad_deploy", "index", i+1)
		}
		if d.Healthcheck.Timeout != "" {
			if _, err := time.ParseDuration(d.Healthcheck.Timeout); err != nil {
				return bad("pipeline.bad_deploy", "index", i+1)
			}
		}
		if d.Healthcheck.URL != "" && !strings.HasPrefix(d.Healthcheck.URL, "http://") && !strings.HasPrefix(d.Healthcheck.URL, "https://") {
			return bad("pipeline.bad_deploy", "index", i+1)
		}
		if !hasPush(s) {
			return bad("pipeline.deploy_needs_image")
		}
	}
	return nil
}

func hasPush(s *Spec) bool {
	for _, st := range s.Steps {
		if st.DockerBuild != nil && st.DockerBuild.Push != "" {
			return true
		}
	}
	return false
}

// Image returns the repository the pipeline pushes (the last docker_build with push).
func (s *Spec) Image() string {
	img := ""
	for _, st := range s.Steps {
		if st.DockerBuild != nil && st.DockerBuild.Push != "" {
			img = st.DockerBuild.Push
		}
	}
	return img
}
