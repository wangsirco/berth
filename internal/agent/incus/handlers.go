package incus

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"runtime"

	"github.com/wangsirco/berth/internal/agent"
	"github.com/wangsirco/berth/internal/errs"
)

func goarch() string { return runtime.GOARCH }

// Image is one choice in the "new" form. The /cloud variants run cloud-init, so SSH keys
// and passwords can be put in.
type Image struct {
	Alias   string `json:"alias"`
	OS      string `json:"os"`
	Name    string `json:"name"`
	VM      bool   `json:"vm"` // a virtual-machine image exists for arm64
	Cloud   bool   `json:"cloud"`
	Default bool   `json:"default,omitempty"`
}

// Images checked on images.linuxcontainers.org for arm64 (2026-11-12).
var Images = []Image{
	{Alias: "debian/13/cloud", OS: "debian", Name: "Debian 13", VM: true, Cloud: true, Default: true},
	{Alias: "debian/12/cloud", OS: "debian", Name: "Debian 12", VM: true, Cloud: true},
	{Alias: "ubuntu/24.04/cloud", OS: "ubuntu", Name: "Ubuntu 24.04 LTS", VM: true, Cloud: true},
	{Alias: "fedora/43/cloud", OS: "fedora", Name: "Fedora 43", VM: true, Cloud: true},
	{Alias: "alpine/3.22/cloud", OS: "alpine", Name: "Alpine 3.22", VM: true, Cloud: true},
	{Alias: "rockylinux/9/cloud", OS: "rocky", Name: "Rocky Linux 9", VM: false, Cloud: true},
	{Alias: "archlinux", OS: "arch", Name: "Arch Linux", VM: false, Cloud: false},
}

var nameRe = regexp.MustCompile(`^[a-z][a-z0-9-]{0,61}[a-z0-9]$`)

// ValidName: Incus instance names are host names.
func ValidName(n string) bool { return nameRe.MatchString(n) }

func reply(w http.ResponseWriter, v any, err error) {
	if err != nil {
		var ie *Error
		if errors.As(err, &ie) {
			code := ie.Status
			if code < 400 || code > 599 {
				code = 500
			}
			if code == 404 {
				agent.WriteErr(w, errs.New(404, "vm.not_found", "name", ""))
				return
			}
			agent.WriteErr(w, errs.New(code, "vm.incus", "detail", ie.Message))
			return
		}
		agent.WriteErr(w, err)
		return
	}
	if v == nil {
		v = map[string]bool{"ok": true}
	}
	agent.WriteJSON(w, 200, v)
}

// Register adds /v1/vms/*.
func Register(s *agent.Server) {
	c := New()
	go EnsureNetwork(context.Background(), nil)
	s.Handle("GET /v1/vms/status", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		agent.WriteJSON(w, 200, map[string]any{"status": c.Status(r.Context()), "images": Images})
	})
	s.Handle("POST /v1/vms/install", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		st := agent.NewStream(w)
		log := func(l string) { st.Send(map[string]string{"l": l}) }
		err := c.Install(r.Context(), log)
		if err == nil {
			b, _ := json.Marshal(c.Status(r.Context()))
			log("::result " + string(b))
		} else {
			err = errs.New(500, "vm.install_failed", "detail", err.Error())
		}
		st.End(err)
	})
	s.Handle("GET /v1/vms", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		list, err := c.Instances(r.Context())
		if list == nil {
			list = []Instance{}
		}
		reply(w, list, err)
	})
	s.Handle("GET /v1/vms/{name}", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		in, err := c.Instance(r.Context(), r.PathValue("name"))
		reply(w, in, err)
	})
	s.Handle("POST /v1/vms", func(w http.ResponseWriter, r *http.Request, body []byte) {
		var spec CreateSpec
		json.Unmarshal(body, &spec)
		if !ValidName(spec.Name) {
			agent.WriteErr(w, errs.New(400, "vm.bad_name"))
			return
		}
		st := agent.NewStream(w)
		st.Send(map[string]string{"l": "downloading " + spec.Image + " (first time takes a while) and creating " + spec.Name})
		err := c.Create(r.Context(), spec)
		if err != nil {
			err = errs.New(500, "vm.incus", "detail", err.Error())
		}
		st.End(err)
	})
	s.Handle("POST /v1/vms/{name}/state", func(w http.ResponseWriter, r *http.Request, body []byte) {
		var in struct {
			Action string `json:"action"`
			Force  bool   `json:"force"`
		}
		json.Unmarshal(body, &in)
		switch in.Action {
		case "start", "stop", "restart", "freeze", "unfreeze":
		default:
			agent.WriteErr(w, errs.New(400, "params.enum", "name", "action", "allowed", "start, stop, restart"))
			return
		}
		reply(w, nil, c.SetState(r.Context(), r.PathValue("name"), in.Action, in.Force))
	})
	s.Handle("DELETE /v1/vms/{name}", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		reply(w, nil, c.Delete(r.Context(), r.PathValue("name")))
	})
	s.Handle("POST /v1/vms/{name}/snapshots", func(w http.ResponseWriter, r *http.Request, body []byte) {
		var in struct{ Snapshot string }
		json.Unmarshal(body, &in)
		reply(w, nil, c.Snapshot(r.Context(), r.PathValue("name"), in.Snapshot))
	})
	s.Handle("DELETE /v1/vms/{name}/snapshots/{snap}", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		reply(w, nil, c.DeleteSnapshot(r.Context(), r.PathValue("name"), r.PathValue("snap")))
	})
	s.Handle("POST /v1/vms/{name}/restore", func(w http.ResponseWriter, r *http.Request, body []byte) {
		var in struct{ Snapshot string }
		json.Unmarshal(body, &in)
		reply(w, nil, c.Restore(r.Context(), r.PathValue("name"), in.Snapshot))
	})
	s.Handle("POST /v1/vms/{name}/clone", func(w http.ResponseWriter, r *http.Request, body []byte) {
		var in struct{ To string }
		json.Unmarshal(body, &in)
		if !ValidName(in.To) {
			agent.WriteErr(w, errs.New(400, "vm.bad_name"))
			return
		}
		reply(w, nil, c.Clone(r.Context(), r.PathValue("name"), in.To))
	})
	s.Handle("POST /v1/vms/{name}/resize", func(w http.ResponseWriter, r *http.Request, body []byte) {
		var in struct {
			CPUs     int `json:"cpus"`
			MemoryMB int `json:"memory_mb"`
			DiskGB   int `json:"disk_gb"`
		}
		json.Unmarshal(body, &in)
		reply(w, nil, c.Resize(r.Context(), r.PathValue("name"), in.CPUs, in.MemoryMB, in.DiskGB))
	})
	s.Handle("GET /v1/vms/lan-ports", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		agent.WriteJSON(w, 200, map[string]any{"ports": LANPorts(), "host_ip": hostIP()})
	})
	s.Handle("POST /v1/vms/{name}/network", func(w http.ResponseWriter, r *http.Request, body []byte) {
		var in struct{ Mode, Parent, Egress string }
		json.Unmarshal(body, &in)
		removed, err := c.SetNetwork(r.Context(), r.PathValue("name"), in.Mode, in.Parent, firstNonEmpty(in.Egress, "all"))
		if removed == nil {
			removed = []Forward{}
		}
		reply(w, map[string]any{"removed_forwards": removed}, err)
	})
	s.Handle("POST /v1/vms/{name}/perms", func(w http.ResponseWriter, r *http.Request, body []byte) {
		var in struct {
			Autostart  bool `json:"autostart"`
			Nesting    bool `json:"nesting"`
			Privileged bool `json:"privileged"`
		}
		json.Unmarshal(body, &in)
		reply(w, nil, c.SetPerms(r.Context(), r.PathValue("name"), in.Autostart, in.Nesting, in.Privileged))
	})
	s.Handle("POST /v1/vms/{name}/share", func(w http.ResponseWriter, r *http.Request, body []byte) {
		var in struct {
			ID       string `json:"id"`
			Source   string `json:"source"`
			Path     string `json:"path"`
			ReadOnly bool   `json:"readonly"`
		}
		json.Unmarshal(body, &in)
		reply(w, nil, c.SetShare(r.Context(), r.PathValue("name"), in.ID, in.Source, in.Path, in.ReadOnly))
	})
	s.Handle("POST /v1/vms/{name}/forward", func(w http.ResponseWriter, r *http.Request, body []byte) {
		var in struct {
			Proto      string `json:"proto"`
			HostPort   int    `json:"host_port"`
			TargetPort int    `json:"target_port"`
		}
		json.Unmarshal(body, &in)
		if in.Proto != "tcp" && in.Proto != "udp" || in.HostPort < 1 || in.HostPort > 65535 || in.TargetPort < 0 || in.TargetPort > 65535 {
			agent.WriteErr(w, errs.New(400, "vm.bad_forward"))
			return
		}
		inst, err := c.Instance(r.Context(), r.PathValue("name"))
		if err != nil {
			reply(w, nil, err)
			return
		}
		ip := ""
		if len(inst.IPv4) > 0 {
			ip = inst.IPv4[0]
		}
		reply(w, nil, c.SetForward(r.Context(), inst.Name, in.Proto, in.HostPort, in.TargetPort, inst.Type == "virtual-machine", ip))
	})
}
