package agent

// Registry mirrors (daemon.json registry-mirrors) and registry logins (docker login), so
// pulls from Docker Hub go through a mirror and private registries work for compose too.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strings"

	"github.com/wangsirco/berth/internal/agent/drift"
	"github.com/wangsirco/berth/internal/errs"
)

var registryHostRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9.-]*[a-z0-9])?(:[0-9]{1,5})?$`)

func daemonConfig() (map[string]any, error) {
	cfg := map[string]any{}
	if b, err := os.ReadFile(DockerDaemonJSON); err == nil && len(strings.TrimSpace(string(b))) > 0 {
		if err := json.Unmarshal(b, &cfg); err != nil {
			return nil, errs.New(409, "compat.daemon_json", "detail", err.Error())
		}
	}
	return cfg, nil
}

func mirrors() []string {
	cfg, _ := daemonConfig()
	out := []string{}
	if l, ok := cfg["registry-mirrors"].([]any); ok {
		for _, m := range l {
			if s, ok := m.(string); ok {
				out = append(out, s)
			}
		}
	}
	return out
}

func setMirrors(ctx context.Context, list []string, st *StreamWriter) error {
	for _, m := range list {
		u, err := url.Parse(m)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			return errs.New(400, "docker.bad_mirror", "url", m)
		}
	}
	cfg, err := daemonConfig()
	if err != nil {
		return err
	}
	if len(list) == 0 {
		delete(cfg, "registry-mirrors")
	} else {
		cfg["registry-mirrors"] = list
	}
	b, _ := json.MarshalIndent(cfg, "", "  ")
	os.MkdirAll("/etc/docker", 0o755)
	if old, err := os.ReadFile(DockerDaemonJSON); err == nil {
		os.WriteFile(DockerDaemonJSON+".berth-prev", old, 0o644)
	}
	if err := os.WriteFile(DockerDaemonJSON+".tmp", append(b, '\n'), 0o644); err != nil {
		return err
	}
	if err := os.Rename(DockerDaemonJSON+".tmp", DockerDaemonJSON); err != nil {
		return err
	}
	drift.Track(DockerDaemonJSON, "docker", "")
	// a reload is enough for registry-mirrors: running containers are not touched
	return run(st, "systemctl", "reload", "docker")
}

// logOpts reads the default size cap for container logs (json-file driver).
func logOpts() map[string]string {
	cfg, _ := daemonConfig()
	out := map[string]string{}
	if lo, ok := cfg["log-opts"].(map[string]any); ok {
		for k, v := range lo {
			if s, ok := v.(string); ok {
				out[k] = s
			}
		}
	}
	return out
}

var logSizeRe = regexp.MustCompile(`^([0-9]{1,5}[kmg])?$`)

// setLogOpts caps each new container's log (max-size × max-file); "" removes the cap.
func setLogOpts(maxSize, maxFile string, st *StreamWriter) error {
	if !logSizeRe.MatchString(maxSize) || maxFile != "" && !regexp.MustCompile(`^[1-9][0-9]?$`).MatchString(maxFile) {
		return errs.New(400, "docker.bad_log_opts")
	}
	cfg, err := daemonConfig()
	if err != nil {
		return err
	}
	if maxSize == "" {
		delete(cfg, "log-opts")
	} else {
		if maxFile == "" {
			maxFile = "3"
		}
		cfg["log-driver"] = "json-file"
		cfg["log-opts"] = map[string]string{"max-size": maxSize, "max-file": maxFile}
	}
	b, _ := json.MarshalIndent(cfg, "", "  ")
	if old, err := os.ReadFile(DockerDaemonJSON); err == nil {
		os.WriteFile(DockerDaemonJSON+".berth-prev", old, 0o644)
	}
	if err := os.WriteFile(DockerDaemonJSON+".tmp", append(b, '\n'), 0o644); err != nil {
		return err
	}
	if err := os.Rename(DockerDaemonJSON+".tmp", DockerDaemonJSON); err != nil {
		return err
	}
	drift.Track(DockerDaemonJSON, "docker", "")
	return run(st, "systemctl", "reload", "docker")
}

func registryLogins() []string {
	out := []string{}
	b, err := os.ReadFile("/root/.docker/config.json")
	if err != nil {
		return out
	}
	var cfg struct {
		Auths map[string]any `json:"auths"`
	}
	json.Unmarshal(b, &cfg)
	for k := range cfg.Auths {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func registerRegistryAuth(s *Server) {
	s.Handle("GET /v1/docker/registries", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		WriteJSON(w, 200, map[string]any{"mirrors": mirrors(), "logins": registryLogins(), "log_opts": logOpts()})
	})
	s.Handle("POST /v1/docker/mirrors", func(w http.ResponseWriter, r *http.Request, body []byte) {
		var in struct{ Mirrors []string }
		json.Unmarshal(body, &in)
		st := NewStream(w)
		st.End(setMirrors(r.Context(), in.Mirrors, st))
	})
	s.Handle("POST /v1/docker/logopts", func(w http.ResponseWriter, r *http.Request, body []byte) {
		var in struct {
			MaxSize string `json:"max_size"`
			MaxFile string `json:"max_file"`
		}
		json.Unmarshal(body, &in)
		st := NewStream(w)
		st.End(setLogOpts(in.MaxSize, in.MaxFile, st))
	})
	s.Handle("POST /v1/docker/login", func(w http.ResponseWriter, r *http.Request, body []byte) {
		var in struct{ Registry, User, Password string }
		json.Unmarshal(body, &in)
		if !registryHostRe.MatchString(in.Registry) || in.User == "" || strings.ContainsAny(in.User, " \n") {
			WriteErr(w, errs.New(400, "docker.bad_login"))
			return
		}
		cmd := exec.CommandContext(r.Context(), "docker", "login", in.Registry, "-u", in.User, "--password-stdin")
		cmd.Stdin = strings.NewReader(in.Password)
		if out, err := cmd.CombinedOutput(); err != nil {
			WriteErr(w, errs.New(400, "docker.login_failed", "detail", strings.TrimSpace(string(out))))
			return
		}
		WriteJSON(w, 200, map[string]bool{"ok": true})
	})
	s.Handle("POST /v1/docker/logout", func(w http.ResponseWriter, r *http.Request, body []byte) {
		var in struct{ Registry string }
		json.Unmarshal(body, &in)
		if !registryHostRe.MatchString(in.Registry) {
			WriteErr(w, errs.New(400, "docker.bad_login"))
			return
		}
		out, err := exec.CommandContext(r.Context(), "docker", "logout", in.Registry).CombinedOutput()
		if err != nil {
			WriteErr(w, errors.New(strings.TrimSpace(string(out))))
			return
		}
		WriteJSON(w, 200, map[string]bool{"ok": true})
	})
}
