package docker

// Switching one compose service to another container runtime (the 4K-page "krun"
// compatibility mode on Asahi) by editing only that service's lines.

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/wangsirco/berth/internal/agent/drift"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/wangsirco/berth/internal/agent"
	"github.com/wangsirco/berth/internal/errs"
)

var runtimeRe = regexp.MustCompile(`^(|krun|runc)$`)

// SetRuntime sets (or, with "", removes) `runtime:` on a service. It returns the new text
// and the previous value.
func SetRuntime(src []byte, service, rt string) ([]byte, string, error) {
	return SetServiceKey(src, service, "runtime", rt)
}

// SetServiceKey sets (or, with "", removes) a one-line key on a compose service, leaving
// every other line as it was.
func SetServiceKey(src []byte, service, key, rt string) ([]byte, string, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(src, &doc); err != nil || len(doc.Content) == 0 {
		return nil, "", errs.New(400, "compose.invalid", "detail", fmt.Sprint(err))
	}
	find := func(m *yaml.Node, key string) (*yaml.Node, *yaml.Node) {
		if m == nil || m.Kind != yaml.MappingNode {
			return nil, nil
		}
		for i := 0; i+1 < len(m.Content); i += 2 {
			if m.Content[i].Value == key {
				return m.Content[i], m.Content[i+1]
			}
		}
		return nil, nil
	}
	_, services := find(doc.Content[0], "services")
	_, svc := find(services, service)
	if svc == nil || svc.Kind != yaml.MappingNode || len(svc.Content) == 0 {
		return nil, "", errs.New(404, "deploy.no_service", "service", service)
	}
	lines := strings.SplitAfter(string(src), "\n")
	k, v := find(svc, key)
	old := ""
	if v != nil {
		old = v.Value
		if k.Line != v.Line {
			return nil, "", errs.New(400, "compose.invalid", "detail", key+" spans lines")
		}
		if rt == "" {
			lines = append(lines[:k.Line-1], lines[k.Line:]...)
		} else {
			l := lines[k.Line-1]
			nl := l[len(strings.TrimRight(l, "\r\n")):]
			lines[k.Line-1] = strings.Repeat(" ", k.Column-1) + key + ": " + rt + nl
		}
		return []byte(strings.Join(lines, "")), old, nil
	}
	if rt == "" {
		return src, "", nil
	}
	first := svc.Content[0] // the service's first key: same indentation, insert before it
	if first.Line < 1 || first.Line > len(lines) || svc.Style&yaml.FlowStyle != 0 {
		return nil, "", errs.New(400, "compose.invalid", "detail", "service is not in block style")
	}
	ins := strings.Repeat(" ", first.Column-1) + key + ": " + rt + "\n"
	out := append([]string{}, lines[:first.Line-1]...)
	out = append(out, ins)
	out = append(out, lines[first.Line-1:]...)
	return []byte(strings.Join(out, "")), old, nil
}

func registerRuntime(s *agent.Server, c *Client) {
	s.Handle("POST /v1/compose/projects/{name}/runtime", func(w http.ResponseWriter, r *http.Request, body []byte) {
		var in struct {
			Service string `json:"service"`
			Runtime string `json:"runtime"`
			Check   bool   `json:"check"` // only report the current value
		}
		json.Unmarshal(body, &in)
		if !runtimeRe.MatchString(in.Runtime) {
			agent.WriteErr(w, errs.New(400, "params.invalid_json"))
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
		out, old, err := SetRuntime(src, in.Service, in.Runtime)
		if err != nil {
			agent.WriteErr(w, err)
			return
		}
		if in.Check {
			agent.WriteJSON(w, 200, map[string]any{"file": file, "runtime": old, "before": string(src), "after": string(out)})
			return
		}
		st := agent.NewStream(w)
		os.WriteFile(file+".berth-prev", src, 0o600)
		if err := writeAtomic(file, out, 0o644); err != nil {
			st.End(err)
			return
		}
		drift.Track(file, "compose", p.Name)
		args := []string{"compose", "--project-directory", p.Dir, "-p", p.Name}
		for _, f := range p.ConfigFiles {
			args = append(args, "-f", f)
		}
		args = append(args, "up", "-d", "--no-deps", "--force-recreate", in.Service)
		cmd := exec.CommandContext(context.WithoutCancel(r.Context()), "docker", args...)
		cmd.Env = append(os.Environ(), "COMPOSE_ANSI=never", "COMPOSE_PROGRESS=plain")
		o, err := cmd.CombinedOutput()
		for _, l := range strings.Split(strings.TrimSpace(string(o)), "\n") {
			st.Send(map[string]string{"l": l})
		}
		if err != nil {
			os.WriteFile(file, src, 0o644) // keep the file matching what runs
			drift.Track(file, "compose", p.Name)
			st.End(errs.New(500, "compose.failed", "cmd", "up", "name", p.Name, "detail", err.Error()))
			return
		}
		st.Send(map[string]string{"l": "::result " + old})
		st.End(nil)
	})
}

// SetPortBind changes which host address a service's published port listens on:
// "127.0.0.1" (this machine only), a LAN address, or "0.0.0.0" (every address). The port
// entry is edited in place, so the rest of the file keeps its layout and comments.
func SetPortBind(src []byte, service string, inner int, bind string) ([]byte, string, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(src, &doc); err != nil || len(doc.Content) == 0 {
		return nil, "", errs.New(400, "compose.invalid", "detail", fmt.Sprint(err))
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
	ports := find(svc, "ports")
	if ports == nil || ports.Kind != yaml.SequenceNode {
		return nil, "", errs.New(404, "compose.port_not_found", "port", inner)
	}
	want := strconv.Itoa(inner)
	for _, it := range ports.Content {
		if it.Kind != yaml.ScalarNode {
			continue
		}
		spec, proto, _ := strings.Cut(it.Value, "/")
		parts := strings.Split(spec, ":")
		if len(parts) < 2 || len(parts) > 3 || parts[len(parts)-1] != want {
			continue
		}
		old, host := "0.0.0.0", parts[0]
		if len(parts) == 3 {
			old, host = parts[0], parts[1]
		}
		nv := host + ":" + want
		if bind != "0.0.0.0" {
			nv = bind + ":" + nv
		}
		if proto != "" {
			nv += "/" + proto
		}
		lines := strings.SplitAfter(string(src), "\n")
		l := lines[it.Line-1]
		at := strings.Index(l[it.Column-1:], it.Value)
		if at < 0 {
			return nil, "", errs.New(400, "compose.invalid", "detail", "port entry not found on its line")
		}
		at += it.Column - 1
		lines[it.Line-1] = l[:at] + nv + l[at+len(it.Value):]
		return []byte(strings.Join(lines, "")), old, nil
	}
	return nil, "", errs.New(404, "compose.port_not_found", "port", inner)
}
