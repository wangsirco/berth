package docker

import (
	"context"
	"encoding/json"
	"github.com/wangsirco/berth/internal/agent/drift"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"time"

	"github.com/wangsirco/berth/internal/agent"
	"github.com/wangsirco/berth/internal/errs"
)

var phpContainerRe = regexp.MustCompile(`^berth-php[0-9]{2}$`)

func Register(s *agent.Server, c *Client) {
	registerRuntime(s, c)
	registerUpkeep(s, c)
	registerHostPorts(s, c)
	registerDBReset(s, c)
	registerNetVol(s, c)
	// garbage-collect the internal registry after manifests were deleted
	s.Handle("POST /v1/registry/gc", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		out, code, err := c.Exec(r.Context(), "berth-registry", []string{"registry", "garbage-collect", "/etc/docker/registry/config.yml", "--delete-untagged"})
		if err == nil && code != 0 {
			err = errs.New(500, "registry.gc_failed", "detail", out)
		}
		reply(w, map[string]any{"output": out}, err)
	})
	s.Handle("GET /v1/docker/containers", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		list, err := c.Containers(r.Context())
		reply(w, list, err)
	})
	s.Handle("GET /v1/docker/containers/{id}", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		d, err := c.Inspect(r.Context(), r.PathValue("id"))
		reply(w, d, err)
	})
	s.Handle("GET /v1/docker/containers/{id}/listening", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		l, err := c.Listening(r.Context(), r.PathValue("id"))
		reply(w, l, err)
	})
	for _, action := range []string{"start", "stop", "restart", "kill", "remove"} {
		s.Handle("POST /v1/docker/containers/{id}/"+action, func(w http.ResponseWriter, r *http.Request, body []byte) {
			var in struct {
				Volumes bool `json:"volumes"`
				Force   bool `json:"force"`
			}
			_ = json.Unmarshal(body, &in)
			q := url.Values{}
			if action == "remove" {
				if in.Volumes {
					q.Set("v", "1")
				}
				q.Set("force", strconv.FormatBool(in.Force))
			}
			reply(w, map[string]bool{"ok": true}, c.Action(r.Context(), r.PathValue("id"), action, q))
		})
	}
	s.Handle("GET /v1/docker/containers/{id}/logs", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		q := r.URL.Query()
		tail, _ := strconv.Atoi(q.Get("tail"))
		if tail <= 0 || tail > 10000 {
			tail = 500
		}
		if _, err := c.Inspect(r.Context(), r.PathValue("id")); err != nil {
			agent.WriteErr(w, err)
			return
		}
		st := agent.NewStream(w)
		err := c.Logs(r.Context(), r.PathValue("id"), tail, q.Get("follow") == "1", q.Get("since"), func(l LogLine) { st.Send(l) })
		st.End(err)
	})
	s.Handle("GET /v1/docker/stats", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		st, err := c.Stats(r.Context())
		reply(w, st, err)
	})
	s.Handle("GET /v1/docker/images", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		l, err := c.Images(r.Context())
		reply(w, l, err)
	})
	s.Handle("GET /v1/docker/networks", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		l, err := c.Networks(r.Context())
		reply(w, l, err)
	})
	s.Handle("GET /v1/docker/volumes", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		l, err := c.Volumes(r.Context())
		reply(w, l, err)
	})
	s.Handle("POST /v1/docker/images/remove", func(w http.ResponseWriter, r *http.Request, body []byte) {
		var in struct{ Ref string }
		_ = json.Unmarshal(body, &in)
		reply(w, map[string]bool{"ok": true}, c.RemoveImage(r.Context(), in.Ref))
	})
	s.Handle("POST /v1/docker/images/prune", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		n, err := c.PruneImages(r.Context())
		reply(w, map[string]int64{"reclaimed": n}, err)
	})
	s.Handle("POST /v1/docker/images/pull", func(w http.ResponseWriter, r *http.Request, body []byte) {
		var in struct{ Ref string }
		_ = json.Unmarshal(body, &in)
		st := agent.NewStream(w)
		st.End(c.Pull(r.Context(), in.Ref, func(l string) { st.Send(map[string]string{"l": l}) }))
	})
	s.Handle("POST /v1/docker/volumes/remove", func(w http.ResponseWriter, r *http.Request, body []byte) {
		var in struct{ Name string }
		_ = json.Unmarshal(body, &in)
		reply(w, map[string]bool{"ok": true}, c.RemoveVolume(r.Context(), in.Name))
	})
	s.Handle("POST /v1/docker/networks/remove", func(w http.ResponseWriter, r *http.Request, body []byte) {
		var in struct{ Name string }
		_ = json.Unmarshal(body, &in)
		reply(w, map[string]bool{"ok": true}, c.RemoveNetwork(r.Context(), in.Name))
	})

	// ---- compose ----
	s.Handle("GET /v1/compose/projects", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		l, err := c.Projects(r.Context())
		reply(w, l, err)
	})
	s.Handle("GET /v1/compose/projects/{name}/files", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		p, err := c.project(r.Context(), r.PathValue("name"))
		if err != nil {
			agent.WriteErr(w, err)
			return
		}
		f, err := ReadProject(p.Dir, first(p.ConfigFiles))
		reply(w, f, err)
	})
	s.Handle("PUT /v1/compose/projects/{name}/files", func(w http.ResponseWriter, r *http.Request, body []byte) {
		var in struct {
			Compose string    `json:"compose"`
			Env     string    `json:"env"`
			Create  bool      `json:"create"`
			Data    *DataSpec `json:"data"`
		}
		_ = json.Unmarshal(body, &in)
		name := r.PathValue("name")
		dir, file := filepath.Join(StacksDir, name), ""
		p, err := c.project(r.Context(), name)
		switch {
		case err == nil && in.Create:
			agent.WriteErr(w, errs.New(409, "compose.exists", "name", name))
			return
		case err == nil:
			dir, file = p.Dir, first(p.ConfigFiles)
		case !in.Create:
			agent.WriteErr(w, err)
			return
		case !ValidProjectName(name):
			agent.WriteErr(w, errs.New(400, "compose.bad_name"))
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		if err := Validate(ctx, dir, in.Compose, in.Env); err != nil {
			agent.WriteErr(w, err)
			return
		}
		if err := WriteProject(dir, file, in.Compose, in.Env); err != nil {
			agent.WriteErr(w, err)
			return
		}
		if in.Data != nil {
			if err := PrepareData(*in.Data); err != nil {
				agent.WriteErr(w, err)
				return
			}
		}
		reply(w, map[string]string{"dir": dir}, nil)
	})
	s.Handle("POST /v1/compose/projects/{name}/validate", func(w http.ResponseWriter, r *http.Request, body []byte) {
		var in struct{ Compose, Env string }
		_ = json.Unmarshal(body, &in)
		dir := filepath.Join(StacksDir, r.PathValue("name"))
		if p, err := c.project(r.Context(), r.PathValue("name")); err == nil {
			dir = p.Dir
		}
		reply(w, map[string]bool{"ok": true}, Validate(r.Context(), dir, in.Compose, in.Env))
	})
	for _, cmd := range []string{"up", "down", "pull", "restart", "build"} {
		s.Handle("POST /v1/compose/projects/{name}/"+cmd, func(w http.ResponseWriter, r *http.Request, _ []byte) {
			p, err := c.project(r.Context(), r.PathValue("name"))
			if err != nil {
				agent.WriteErr(w, err)
				return
			}
			st := agent.NewStream(w)
			st.End(Compose(r.Context(), p.Name, p.Dir, p.ConfigFiles, cmd, func(l string) { st.Send(map[string]string{"l": l}) }))
		})
	}
	// a command inside one of Berth's PHP runtimes (download a CMS, fix ownership); only those
	s.Handle("POST /v1/php/exec", func(w http.ResponseWriter, r *http.Request, body []byte) {
		var in struct {
			Container string   `json:"container"`
			Cmd       []string `json:"cmd"`
		}
		json.Unmarshal(body, &in)
		if !phpContainerRe.MatchString(in.Container) || len(in.Cmd) == 0 {
			agent.WriteErr(w, errs.New(400, "params.invalid_json"))
			return
		}
		out, code, err := c.Exec(r.Context(), in.Container, in.Cmd)
		reply(w, map[string]any{"output": out, "code": code}, err)
	})
	// remove: down, then move a Berth-created stack directory aside (never deleted outright)
	s.Handle("POST /v1/compose/projects/{name}/remove", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		p, err := c.project(r.Context(), r.PathValue("name"))
		if err != nil {
			agent.WriteErr(w, err)
			return
		}
		st := agent.NewStream(w)
		if p.Total > 0 {
			if err := Compose(r.Context(), p.Name, p.Dir, p.ConfigFiles, "down", func(l string) { st.Send(map[string]string{"l": l}) }); err != nil {
				st.End(err)
				return
			}
		}
		if p.InStacks {
			dst := filepath.Join(StacksDir, ".removed", p.Name+"-"+time.Now().Format("20060102-150405"))
			if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
				st.End(err)
				return
			}
			if err := os.Rename(p.Dir, dst); err != nil {
				st.End(err)
				return
			}
			st.Send(map[string]string{"l": "moved " + p.Dir + " to " + dst})
			drift.UntrackPrefix(p.Dir)
		}
		st.End(nil)
	})
}

func first(l []string) string {
	if len(l) == 0 {
		return ""
	}
	return l[0]
}

func reply(w http.ResponseWriter, v any, err error) {
	if err != nil {
		agent.WriteErr(w, err)
		return
	}
	agent.WriteJSON(w, 200, v)
}
