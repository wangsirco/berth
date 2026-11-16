package panel

// Virtual machines' networks, firewall and permissions (see agent/incus/netperm.go).

import (
	"context"
	"encoding/json"
	"net/http"
	"regexp"
	"strconv"
	"time"

	"github.com/wangsirco/berth/internal/agent/incus"
	"github.com/wangsirco/berth/internal/change"
	"github.com/wangsirco/berth/internal/errs"
)

type vmNetParams struct {
	Name     string          `json:"name"`
	Mode     string          `json:"mode"`
	Parent   string          `json:"parent,omitempty"`
	Egress   string          `json:"egress,omitempty"`
	Forwards []incus.Forward `json:"forwards,omitempty"` // undo: forwards to put back in NAT mode
}

var vmShareIDRe = regexp.MustCompile(`^[a-z0-9]{1,16}$`)

func init() {
	routeHooks = append(routeHooks, func(a *App, mux *http.ServeMux) {
		mux.HandleFunc("GET /api/v1/hosts/{host}/vms-lan", a.authed("vms.view", func(w http.ResponseWriter, r *http.Request) {
			var out any
			if err := a.call(r.Context(), r.PathValue("host"), "GET", "/v1/vms/lan-ports", nil, &out); err != nil {
				a.fail(w, r, err)
				return
			}
			writeJSON(w, 200, out)
		}))
	})

	change.Register(change.Kind{
		Name: "vm.network", Perm: "vms.manage", Scope: "system:write", Title: "kind.vm.network",
		Params: change.ParamSchema{Props: map[string]change.Prop{"name": {Type: "string"}, "mode": {Type: "string", Enum: []string{"nat", "lan", "isolated"}},
			"parent": {Type: "string", Desc: "lan: the host's wired port or bridge"}, "egress": {Type: "string", Enum: []string{"all", "lan", "none"}, Desc: "nat: where it may connect"}},
			Required: []string{"name", "mode"}},
		CLI: "berth vm network {{.name}} {{.mode}}",
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			var p vmNetParams
			json.Unmarshal(raw, &p)
			in, err := svc(env).vm(env.Ctx, env.Host, p.Name)
			if err != nil {
				return change.Plan{}, err
			}
			if p.Egress == "" {
				p.Egress = "all"
			}
			cmds := []string{}
			switch p.Mode {
			case "nat":
				cmds = append(cmds, "incus config device override "+p.Name+" eth0 network=incusbr0")
				if p.Egress != "all" {
					cmds = append(cmds, "incus network acl "+"berth-"+p.Name+": outbound "+p.Egress+" only", "eth0 security.acls=berth-"+p.Name)
				}
			case "lan":
				if p.Parent == "" {
					return change.Plan{}, errs.New(400, "vm.need_lan_port")
				}
				cmds = append(cmds, "incus config device override "+p.Name+" eth0 nictype=macvlan parent="+p.Parent)
			case "isolated":
				cmds = append(cmds, "incus config device override "+p.Name+" eth0 type=none")
			default:
				return change.Plan{}, errs.New(400, "params.enum", "name", "mode", "allowed", "nat, lan, isolated")
			}
			if p.Mode != "nat" && len(in.Forwards) > 0 {
				cmds = append(cmds, "remove "+strconv.Itoa(len(in.Forwards))+" port forward(s) (they only work in NAT mode)")
			}
			if in.Status == "Running" {
				cmds = append(cmds, "incus restart "+p.Name)
			}
			return change.Plan{Summary: "plan.vm.network_" + p.Mode, SummaryP: map[string]any{"name": p.Name, "parent": p.Parent, "egress": p.Egress, "n": len(in.Forwards)},
				Risk: change.RiskMedium, Undoable: true, Commands: cmds, Affected: []change.Object{{Type: "vm", ID: p.Name, Name: p.Name, Host: env.Host}}}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			a := svc(env)
			var p vmNetParams
			json.Unmarshal(raw, &p)
			before, err := a.vm(env.Ctx, env.Host, p.Name)
			if err != nil {
				return nil, nil, err
			}
			var out struct {
				Removed []incus.Forward `json:"removed_forwards"`
			}
			if err := a.call(env.Ctx, env.Host, "POST", "/v1/vms/"+p.Name+"/network", map[string]any{"mode": p.Mode, "parent": p.Parent, "egress": p.Egress}, &out); err != nil {
				return nil, nil, err
			}
			// back in NAT: the forwards an earlier switch removed come back
			if p.Mode == "nat" && len(p.Forwards) > 0 {
				after, _ := a.vm(env.Ctx, env.Host, p.Name)
				for i := 0; i < 30 && len(after.IPv4) == 0 && after.Type == "virtual-machine"; i++ {
					env.Log("waiting for %s to get an address", p.Name)
					sleepCtx(env.Ctx, 2)
					after, _ = a.vm(env.Ctx, env.Host, p.Name)
				}
				for _, f := range p.Forwards {
					if err := a.call(env.Ctx, env.Host, "POST", "/v1/vms/"+p.Name+"/forward", map[string]any{"proto": f.Proto, "host_port": f.HostPort, "target_port": f.TargetPort}, nil); err != nil {
						env.Log("forward %d: %v", f.HostPort, err)
					}
				}
			}
			u, _ := json.Marshal(vmNetParams{Name: p.Name, Mode: before.Net.Mode, Parent: before.Net.Parent, Egress: before.Net.Egress, Forwards: out.Removed})
			if before.Net.Mode == "custom" {
				return out, nil, nil // nothing Berth made to go back to
			}
			return out, &change.UndoRecipe{Kind: "vm.network", Params: u}, nil
		},
	})

	change.Register(change.Kind{
		Name: "vm.perms", Perm: "vms.manage", Scope: "system:write", Title: "kind.vm.perms",
		Params: change.ParamSchema{Props: map[string]change.Prop{"name": {Type: "string"}, "autostart": {Type: "boolean"}, "nesting": {Type: "boolean", Desc: "containers: may run Docker inside"},
			"privileged": {Type: "boolean", Desc: "containers: root inside is root on the host"}}, Required: []string{"name"}},
		CLI: "berth vm perms {{.name}}",
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			var p struct {
				Name                           string
				Autostart, Nesting, Privileged bool
			}
			json.Unmarshal(raw, &p)
			in, err := svc(env).vm(env.Ctx, env.Host, p.Name)
			if err != nil {
				return change.Plan{}, err
			}
			risk := change.RiskLow
			if p.Privileged && !in.Perms.Privileged {
				risk = change.RiskHigh
			}
			cmds := []string{"boot.autostart=" + strconv.FormatBool(p.Autostart)}
			if in.Type == "container" {
				cmds = append(cmds, "security.nesting="+strconv.FormatBool(p.Nesting), "security.privileged="+strconv.FormatBool(p.Privileged))
				if (p.Nesting != in.Perms.Nesting || p.Privileged != in.Perms.Privileged) && in.Status == "Running" {
					cmds = append(cmds, "incus restart "+p.Name)
				}
			}
			return change.Plan{Summary: "plan.vm.perms", SummaryP: map[string]any{"name": p.Name}, Risk: risk, Undoable: true, Commands: cmds,
				Affected: []change.Object{{Type: "vm", ID: p.Name, Name: p.Name, Host: env.Host}}}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			a := svc(env)
			var p struct {
				Name                           string
				Autostart, Nesting, Privileged bool
			}
			json.Unmarshal(raw, &p)
			before, err := a.vm(env.Ctx, env.Host, p.Name)
			if err != nil {
				return nil, nil, err
			}
			if err := a.call(env.Ctx, env.Host, "POST", "/v1/vms/"+p.Name+"/perms", map[string]any{"autostart": p.Autostart, "nesting": p.Nesting, "privileged": p.Privileged}, nil); err != nil {
				return nil, nil, err
			}
			u, _ := json.Marshal(map[string]any{"name": p.Name, "autostart": before.Perms.Autostart, "nesting": before.Perms.Nesting, "privileged": before.Perms.Privileged})
			return nil, &change.UndoRecipe{Kind: "vm.perms", Params: u}, nil
		},
	})

	change.Register(change.Kind{
		Name: "vm.share", Perm: "vms.manage", Scope: "system:write", Title: "kind.vm.share",
		Params: change.ParamSchema{Props: map[string]change.Prop{"name": {Type: "string"}, "id": {Type: "string"}, "source": {Type: "string", Desc: "host folder; empty removes the share"},
			"path": {Type: "string", Desc: "where it appears inside"}, "readonly": {Type: "boolean"}}, Required: []string{"name", "id"}},
		CLI: "berth vm share {{.name}} {{.source}} {{.path}}",
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			var p struct {
				Name, ID, Source, Path string
				ReadOnly               bool
			}
			json.Unmarshal(raw, &p)
			if !vmShareIDRe.MatchString(p.ID) {
				return change.Plan{}, errs.New(400, "vm.bad_share")
			}
			sum, risk := "plan.vm.share", change.RiskMedium
			if p.Source == "" {
				sum, risk = "plan.vm.share_remove", change.RiskLow
			} else if !p.ReadOnly {
				risk = change.RiskHigh
			}
			return change.Plan{Summary: sum, SummaryP: map[string]any{"name": p.Name, "source": p.Source, "path": p.Path}, Risk: risk, Undoable: true,
				Affected: []change.Object{{Type: "vm", ID: p.Name, Name: p.Name, Host: env.Host}}}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			a := svc(env)
			var p struct {
				Name, ID, Source, Path string
				ReadOnly               bool `json:"readonly"`
			}
			json.Unmarshal(raw, &p)
			before, err := a.vm(env.Ctx, env.Host, p.Name)
			if err != nil {
				return nil, nil, err
			}
			if err := a.call(env.Ctx, env.Host, "POST", "/v1/vms/"+p.Name+"/share", map[string]any{"id": p.ID, "source": p.Source, "path": p.Path, "readonly": p.ReadOnly}, nil); err != nil {
				return nil, nil, err
			}
			undo := map[string]any{"name": p.Name, "id": p.ID, "source": ""}
			for _, s := range before.Perms.Shares {
				if s.ID == p.ID {
					undo = map[string]any{"name": p.Name, "id": p.ID, "source": s.Source, "path": s.Path, "readonly": s.ReadOnly}
				}
			}
			u, _ := json.Marshal(undo)
			return nil, &change.UndoRecipe{Kind: "vm.share", Params: u}, nil
		},
	})
}

func sleepCtx(ctx context.Context, secs int) {
	select {
	case <-ctx.Done():
	case <-time.After(time.Duration(secs) * time.Second):
	}
}
