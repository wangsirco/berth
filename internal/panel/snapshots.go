package panel

// System snapshots (btrfs): taken by hand or automatically before system updates and package
// changes; rolling back to one takes effect at the next boot and can be undone until then.

import (
	"encoding/json"
	"net/http"

	"github.com/wangsirco/berth/internal/agent/system"
	"github.com/wangsirco/berth/internal/change"
	"github.com/wangsirco/berth/internal/errs"
)

func (a *App) snapStatus(env change.Env) (system.SnapStatus, error) {
	var st system.SnapStatus
	err := a.call(env.Ctx, env.Host, "GET", "/v1/system/snapshots", nil, &st)
	return st, err
}

// autoSnapshot takes a snapshot before a system change when the host can; a host without
// btrfs simply goes ahead (its plan said so).
func (a *App) autoSnapshot(env change.Env, reason string) {
	st, err := a.snapStatus(env)
	if err != nil || !st.Supported {
		return
	}
	var s system.SysSnapshot
	if err := a.call(env.Ctx, env.Host, "POST", "/v1/system/snapshots", map[string]any{"reason": reason, "auto": true}, &s); err != nil {
		env.Log("system snapshot failed (%s); going ahead", errText(err))
		return
	}
	env.Log("system snapshot %s taken (roll back under System → Snapshots if this goes wrong)", s.ID)
}

// snapLine: the plan's line about the snapshot taken first.
func (a *App) snapLine(env change.Env) string {
	if st, err := a.snapStatus(env); err == nil && st.Supported {
		return "btrfs snapshot of / and /boot first (automatic; the newest 5 are kept)"
	}
	return ""
}

func init() {
	routeHooks = append(routeHooks, func(a *App, mux *http.ServeMux) {
		mux.HandleFunc("GET /api/v1/hosts/{host}/system/snapshots", a.authed("system.view", a.agentGet("/v1/system/snapshots")))
	})
	idProp := change.ParamSchema{Props: map[string]change.Prop{"id": {Type: "string", Desc: "snapshot id, e.g. 20261115-203000"}}, Required: []string{"id"}}
	op := func(env change.Env, name string, body any) error {
		return svc(env).call(env.Ctx, env.Host, "POST", "/v1/system/snapshots/"+name, body, nil)
	}
	find := func(env change.Env, id string) (system.SysSnapshot, error) {
		st, err := svc(env).snapStatus(env)
		if err != nil {
			return system.SysSnapshot{}, err
		}
		if !st.Supported {
			return system.SysSnapshot{}, errs.New(409, st.Why)
		}
		for _, s := range st.Snapshots {
			if s.ID == id {
				return s, nil
			}
		}
		return system.SysSnapshot{}, errs.New(404, "snap.not_found", "id", id)
	}

	change.Register(change.Kind{
		Name: "system.snapshot.create", Perm: "system.manage", Scope: "system:write", Title: "kind.system.snapshot.create", CLI: "berth system snapshot",
		Params: change.ParamSchema{Props: map[string]change.Prop{"reason": {Type: "string"}}},
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			st, err := svc(env).snapStatus(env)
			if err != nil {
				return change.Plan{}, err
			}
			if !st.Supported {
				return change.Plan{}, errs.New(409, st.Why)
			}
			return change.Plan{Summary: "plan.system.snapshot.create", Risk: change.RiskLow, Undoable: true,
				Commands: []string{"btrfs subvolume snapshot -r <top>/" + st.RootSub + " <top>/berth-snapshots/<time>/root", "tar -czf …/boot.tar.gz -C /boot ."}}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			var p struct{ Reason string }
			json.Unmarshal(raw, &p)
			var s system.SysSnapshot
			if err := svc(env).call(env.Ctx, env.Host, "POST", "/v1/system/snapshots", map[string]any{"reason": p.Reason}, &s); err != nil {
				return nil, nil, err
			}
			u, _ := json.Marshal(map[string]string{"id": s.ID})
			return s, &change.UndoRecipe{Kind: "system.snapshot.delete", Params: u}, nil
		},
	})
	change.Register(change.Kind{
		Name: "system.snapshot.delete", Perm: "system.manage", Scope: "system:write", Title: "kind.system.snapshot.delete", CLI: "berth system snapshot delete {{.id}}",
		Params: idProp,
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			var p struct{ ID string }
			json.Unmarshal(raw, &p)
			s, err := find(env, p.ID)
			if err != nil {
				return change.Plan{}, err
			}
			return change.Plan{Summary: "plan.system.snapshot.delete", SummaryP: map[string]any{"id": s.ID}, Risk: change.RiskMedium,
				Commands: []string{"btrfs subvolume delete <top>/berth-snapshots/" + s.ID + "/root"}}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			var p struct{ ID string }
			json.Unmarshal(raw, &p)
			return nil, nil, op(env, "delete", map[string]string{"id": p.ID})
		},
	})
	change.Register(change.Kind{
		Name: "system.snapshot.rollback", Perm: "system.manage", Scope: "system:write", Title: "kind.system.snapshot.rollback", CLI: "berth system snapshot rollback {{.id}}",
		Params: idProp,
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			var p struct{ ID string }
			json.Unmarshal(raw, &p)
			s, err := find(env, p.ID)
			if err != nil {
				return change.Plan{}, err
			}
			st, _ := svc(env).snapStatus(env)
			if st.Pending != "" {
				return change.Plan{}, errs.New(409, "snap.pending")
			}
			cmds := []string{
				"keep the current system as " + st.RootSub + ".before-<time> (nothing is deleted)",
				"btrfs subvolume snapshot <top>/berth-snapshots/" + s.ID + "/root <top>/" + st.RootSub,
			}
			if s.Boot {
				cmds = append(cmds, "put /boot back as it was then (kernels and boot entries; the current /boot is saved first)")
			}
			cmds = append(cmds, "takes effect at the next reboot; /var and /home (sites, databases, containers, VMs) are not touched")
			return change.Plan{Summary: "plan.system.snapshot.rollback", SummaryP: map[string]any{"id": s.ID, "reason": s.Reason, "kernel": s.Kernel},
				Risk: change.RiskHigh, Undoable: true, Commands: cmds}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			var p struct{ ID string }
			json.Unmarshal(raw, &p)
			if err := op(env, "rollback", map[string]string{"id": p.ID}); err != nil {
				return nil, nil, err
			}
			return map[string]bool{"reboot": true}, &change.UndoRecipe{Kind: "system.snapshot.undo", Params: json.RawMessage(`{}`)}, nil
		},
	})
	change.Register(change.Kind{
		Name: "system.snapshot.undo", Perm: "system.manage", Scope: "system:write", Title: "kind.system.snapshot.undo", CLI: "berth system snapshot undo",
		Params: change.ParamSchema{Props: map[string]change.Prop{}},
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			st, err := svc(env).snapStatus(env)
			if err != nil {
				return change.Plan{}, err
			}
			if st.Pending == "" {
				return change.Plan{}, errs.New(409, "snap.nothing_to_undo")
			}
			return change.Plan{Summary: "plan.system.snapshot.undo", SummaryP: map[string]any{"id": st.Pending}, Risk: change.RiskMedium,
				Commands: []string{"put the current system back in place of the rolled-back one, and /boot as it was"}}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			return nil, nil, op(env, "undo", map[string]any{})
		},
	})
	change.Register(change.Kind{
		Name: "system.snapshot.delete_previous", Perm: "system.manage", Scope: "system:write", Title: "kind.system.snapshot.delete_previous", CLI: "berth system snapshot delete-previous {{.name}}",
		Params: change.ParamSchema{Props: map[string]change.Prop{"name": {Type: "string"}}, Required: []string{"name"}},
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			var p struct{ Name string }
			json.Unmarshal(raw, &p)
			st, err := svc(env).snapStatus(env)
			if err != nil {
				return change.Plan{}, err
			}
			if !contains(st.Previous, p.Name) {
				return change.Plan{}, errs.New(404, "snap.not_found", "id", p.Name)
			}
			return change.Plan{Summary: "plan.system.snapshot.delete_previous", SummaryP: map[string]any{"name": p.Name}, Risk: change.RiskDestructive,
				Commands: []string{"btrfs subvolume delete <top>/" + p.Name}}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			var p struct{ Name string }
			json.Unmarshal(raw, &p)
			return nil, nil, op(env, "delete-previous", map[string]string{"name": p.Name})
		},
	})
}
