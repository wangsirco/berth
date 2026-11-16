package docker

// Image updates and resource limits for compose projects (方案 §5.3).
//
// Updates: the daemon asks the registry for the current digest of each image a container
// runs (a HEAD-style lookup, not a pull) and compares it with the image's own digests.
// Upgrading pulls, recreates, waits, and when a service does not come up running, tags the
// previous images back and recreates again — an automatic rollback.

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/wangsirco/berth/internal/agent"
	"github.com/wangsirco/berth/internal/agent/drift"
	"github.com/wangsirco/berth/internal/errs"
)

type ImageUpdate struct {
	Container string `json:"container"`
	Project   string `json:"project,omitempty"`
	Service   string `json:"service,omitempty"`
	Image     string `json:"image"`
	Current   string `json:"current,omitempty"` // digest in use
	Latest    string `json:"latest,omitempty"`  // digest in the registry
	Update    bool   `json:"update"`
	Error     string `json:"error,omitempty"`
}

var (
	updMu    sync.Mutex
	updCache []ImageUpdate
	updAt    time.Time
)

// remoteDigest asks the daemon what the registry serves for ref now.
func (c *Client) remoteDigest(ctx context.Context, ref string) (string, error) {
	var out struct {
		Descriptor struct {
			Digest string `json:"digest"`
		} `json:"Descriptor"`
	}
	if err := c.getJSON(ctx, "/distribution/"+ref+"/json", nil, &out); err != nil {
		return "", err
	}
	return out.Descriptor.Digest, nil
}

func (c *Client) repoDigests(ctx context.Context, imageID string) []string {
	var img struct {
		RepoDigests []string `json:"RepoDigests"`
	}
	c.getJSON(ctx, "/images/"+url.PathEscape(imageID)+"/json", nil, &img)
	return img.RepoDigests
}

// Updates lists running containers whose image has a newer digest upstream (cached for 6 h).
func (c *Client) Updates(ctx context.Context, refresh bool) ([]ImageUpdate, int64, error) {
	updMu.Lock()
	defer updMu.Unlock()
	if !refresh && updCache != nil && time.Since(updAt) < 6*time.Hour {
		return updCache, updAt.Unix(), nil
	}
	cs, err := c.Containers(ctx)
	if err != nil {
		return nil, 0, err
	}
	out := []ImageUpdate{}
	refs := map[string]bool{}
	for _, ct := range cs {
		if ct.State == "running" && !strings.Contains(ct.Image, "@sha256:") && !strings.HasPrefix(ct.Image, "sha256:") {
			refs[ct.Image] = true
		}
	}
	// ask the registries in parallel (a few at a time)
	type answer struct {
		digest string
		err    error
	}
	remote := map[string]answer{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, 6)
	for ref := range refs {
		wg.Add(1)
		go func(ref string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
			d, err := c.remoteDigest(cctx, ref)
			cancel()
			mu.Lock()
			remote[ref] = answer{d, err}
			mu.Unlock()
		}(ref)
	}
	wg.Wait()
	for _, ct := range cs {
		if !refs[ct.Image] || ct.State != "running" {
			continue
		}
		local := c.repoDigests(ctx, ct.ImageID)
		if len(local) == 0 {
			continue // built here, nothing upstream to compare with
		}
		u := ImageUpdate{Container: ct.Name, Project: ct.Project, Service: ct.Service, Image: ct.Image}
		if i := strings.Index(local[0], "@"); i >= 0 {
			u.Current = local[0][i+1:]
		}
		ans := remote[ct.Image]
		if ans.err != nil {
			u.Error = errs.From(ans.err).Error()
		}
		if ans.digest != "" {
			u.Latest = ans.digest
			u.Update = true
			for _, d := range local {
				if strings.HasSuffix(d, "@"+ans.digest) {
					u.Update = false
				}
			}
		}
		out = append(out, u)
	}
	updCache, updAt = out, time.Now()
	return out, updAt.Unix(), nil
}

func forgetUpdates() {
	updMu.Lock()
	updCache = nil
	updMu.Unlock()
}

// tag points ref at an image id (to roll back).
func (c *Client) tag(ctx context.Context, id, ref string) error {
	repo, tg := ref, "latest"
	if i := strings.LastIndex(ref, ":"); i > strings.LastIndex(ref, "/") {
		repo, tg = ref[:i], ref[i+1:]
	}
	resp, err := c.do(ctx, c.hc, "POST", "/images/"+url.PathEscape(id)+"/tag", url.Values{"repo": {repo}, "tag": {tg}}, nil)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

// Upgrade pulls a project's images, recreates it and rolls back if a service fails to run.
func (c *Client) Upgrade(ctx context.Context, p *Project, log func(string)) error {
	cs, err := c.Containers(ctx)
	if err != nil {
		return err
	}
	type was struct{ ref, id, name string }
	var before []was
	for _, ct := range cs {
		if ct.Project == p.Name {
			before = append(before, was{ct.Image, ct.ImageID, ct.Name})
		}
	}
	if err := Compose(ctx, p.Name, p.Dir, p.ConfigFiles, "pull", log); err != nil {
		return err
	}
	if err := Compose(ctx, p.Name, p.Dir, p.ConfigFiles, "up", log); err != nil {
		return err
	}
	forgetUpdates()
	log("waiting 20 s to see that every service stays up")
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(20 * time.Second):
	}
	cs, _ = c.Containers(ctx)
	var bad []string
	for _, ct := range cs {
		if ct.Project == p.Name && (ct.State != "running" || strings.Contains(ct.Status, "Restarting")) {
			bad = append(bad, ct.Name)
		}
	}
	if len(bad) == 0 {
		return nil
	}
	log("not running after the update: " + strings.Join(bad, ", ") + " — rolling back")
	for _, b := range before {
		if err := c.tag(ctx, b.id, b.ref); err != nil {
			log("re-tagging " + b.ref + ": " + err.Error())
		}
	}
	if err := Compose(ctx, p.Name, p.Dir, p.ConfigFiles, "up", log); err != nil {
		return errs.New(500, "compose.rollback_failed", "detail", err.Error())
	}
	return errs.New(409, "compose.update_rolled_back", "services", strings.Join(bad, ", "))
}

var cpusRe = regexp.MustCompile(`^([0-9]{1,2}(\.[0-9]{1,2})?)?$`)
var memRe = regexp.MustCompile(`^([0-9]{1,6}[mMgG])?$`)

// hostAddr: loopback, every address, or an IPv4 address this machine has.
func hostAddr(a string) bool {
	if a == "127.0.0.1" || a == "0.0.0.0" {
		return true
	}
	ip := net.ParseIP(a)
	if ip == nil || ip.To4() == nil {
		return false
	}
	addrs, _ := net.InterfaceAddrs()
	for _, x := range addrs {
		if n, ok := x.(*net.IPNet); ok && n.IP.Equal(ip) {
			return true
		}
	}
	return false
}

func registerUpkeep(s *agent.Server, c *Client) {
	s.Handle("GET /v1/docker/updates", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		list, at, err := c.Updates(r.Context(), r.URL.Query().Get("refresh") == "1")
		reply(w, map[string]any{"items": list, "checked_at": at}, err)
	})
	s.Handle("POST /v1/compose/projects/{name}/upgrade", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		p, err := c.project(r.Context(), r.PathValue("name"))
		if err != nil {
			agent.WriteErr(w, err)
			return
		}
		st := agent.NewStream(w)
		st.End(c.Upgrade(r.Context(), p, func(l string) { st.Send(map[string]string{"l": l}) }))
	})
	// which address a published port listens on (数据库远程访问), then recreate the service
	s.Handle("POST /v1/compose/projects/{name}/bind", func(w http.ResponseWriter, r *http.Request, body []byte) {
		var in struct {
			Service string `json:"service"`
			Port    int    `json:"port"` // the container's port
			Bind    string `json:"bind"` // 127.0.0.1 | 0.0.0.0 | an address of this host
			Check   bool   `json:"check"`
		}
		json.Unmarshal(body, &in)
		if !hostAddr(in.Bind) {
			agent.WriteErr(w, errs.New(400, "compose.bad_bind", "addr", in.Bind))
			return
		}
		p, err := c.project(r.Context(), r.PathValue("name"))
		if err != nil {
			agent.WriteErr(w, err)
			return
		}
		if len(p.ConfigFiles) == 0 {
			agent.WriteErr(w, errs.New(404, "compose.not_found", "name", p.Name))
			return
		}
		file := p.ConfigFiles[0]
		src, err := os.ReadFile(file)
		if err != nil {
			agent.WriteErr(w, err)
			return
		}
		out, old, err := SetPortBind(src, in.Service, in.Port, in.Bind)
		if err != nil {
			agent.WriteErr(w, err)
			return
		}
		if in.Check {
			agent.WriteJSON(w, 200, map[string]any{"file": file, "bind": old, "before": string(src), "after": string(out)})
			return
		}
		st := agent.NewStream(w)
		if err := writeAtomic(file, out, 0o644); err != nil {
			st.End(err)
			return
		}
		drift.Track(file, "compose", p.Name)
		err = ComposeArgs(r.Context(), p, []string{"up", "-d", "--no-deps", in.Service}, func(l string) { st.Send(map[string]string{"l": l}) })
		if err != nil {
			os.WriteFile(file, src, 0o644)
			drift.Track(file, "compose", p.Name)
			ComposeArgs(r.Context(), p, []string{"up", "-d", "--no-deps", in.Service}, func(string) {})
			st.End(errs.New(500, "compose.failed", "cmd", "up", "name", p.Name, "detail", fmt.Sprint(err)))
			return
		}
		st.End(nil)
	})
	// cpus / mem_limit on one service, then recreate just that service
	s.Handle("POST /v1/compose/projects/{name}/limits", func(w http.ResponseWriter, r *http.Request, body []byte) {
		var in struct {
			Service string `json:"service"`
			CPUs    string `json:"cpus"`
			Memory  string `json:"memory"`
			Check   bool   `json:"check"`
		}
		json.Unmarshal(body, &in)
		if !cpusRe.MatchString(in.CPUs) || !memRe.MatchString(in.Memory) {
			agent.WriteErr(w, errs.New(400, "compose.bad_limits"))
			return
		}
		p, err := c.project(r.Context(), r.PathValue("name"))
		if err != nil {
			agent.WriteErr(w, err)
			return
		}
		if len(p.ConfigFiles) == 0 {
			agent.WriteErr(w, errs.New(404, "compose.not_found", "name", p.Name))
			return
		}
		file := p.ConfigFiles[0]
		src, err := os.ReadFile(file)
		if err != nil {
			agent.WriteErr(w, err)
			return
		}
		out, oldCPU, err := SetServiceKey(src, in.Service, "cpus", in.CPUs)
		if err != nil {
			agent.WriteErr(w, err)
			return
		}
		out, oldMem, err := SetServiceKey(out, in.Service, "mem_limit", in.Memory)
		if err != nil {
			agent.WriteErr(w, err)
			return
		}
		if in.Check {
			agent.WriteJSON(w, 200, map[string]any{"file": file, "cpus": oldCPU, "memory": oldMem, "before": string(src), "after": string(out)})
			return
		}
		st := agent.NewStream(w)
		os.WriteFile(file+".berth-prev", src, 0o600)
		if err := writeAtomic(file, out, 0o644); err != nil {
			st.End(err)
			return
		}
		drift.Track(file, "compose", p.Name)
		args := []string{"--no-deps", in.Service}
		err = ComposeArgs(r.Context(), p, append([]string{"up", "-d"}, args...), func(l string) { st.Send(map[string]string{"l": l}) })
		if err != nil {
			os.WriteFile(file, src, 0o644)
			drift.Track(file, "compose", p.Name)
			st.End(errs.New(500, "compose.failed", "cmd", "up", "name", p.Name, "detail", fmt.Sprint(err)))
			return
		}
		st.End(nil)
	})
}
