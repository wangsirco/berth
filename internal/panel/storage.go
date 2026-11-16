package panel

// Disks (方案 §5.14 硬盘): SMART, mounts written to fstab, formatting, md and Btrfs RAID,
// space analysis and clean-up. Everything that can lose data needs disk.manage and the
// disk's name typed to confirm.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/wangsirco/berth/internal/change"
	"github.com/wangsirco/berth/internal/errs"
)

type storageView struct {
	Disks []struct {
		Path   string `json:"path"`
		Model  string `json:"model"`
		Size   int64  `json:"size"`
		System bool   `json:"system"`
		Parts  []struct {
			Path   string   `json:"path"`
			FSType string   `json:"fstype"`
			Mounts []string `json:"mounts"`
			UUID   string   `json:"uuid"`
		} `json:"parts"`
		FSType string   `json:"fstype"`
		Mounts []string `json:"mounts"`
	} `json:"disks"`
	Arrays []struct {
		Name    string   `json:"name"`
		Path    string   `json:"path"`
		Level   string   `json:"level"`
		Devices []string `json:"devices"`
		Mounts  []string `json:"mounts"`
		FSType  string   `json:"fstype"`
	} `json:"arrays"`
}

func (a *App) storageNow(env change.Env) (storageView, error) {
	var s storageView
	return s, a.call(env.Ctx, env.Host, "GET", "/v1/system/storage", nil, &s)
}

// describe gives a human name for a device in previews: model and size for whole disks.
func (s storageView) describe(path string) string {
	for _, d := range s.Disks {
		if d.Path == path {
			return fmt.Sprintf("%s (%s, %s)", path, firstNonEmpty(d.Model, "disk"), humanBytes(d.Size))
		}
	}
	return path
}

// mountedAt: what device is mounted at path, for undo.
func (s storageView) mountedAt(path string) (dev, fstype string) {
	for _, d := range s.Disks {
		if contains(d.Mounts, path) {
			return d.Path, d.FSType
		}
		for _, p := range d.Parts {
			if contains(p.Mounts, path) {
				return p.Path, p.FSType
			}
		}
	}
	for _, ar := range s.Arrays {
		if contains(ar.Mounts, path) {
			return ar.Path, ar.FSType
		}
	}
	return "", ""
}

func init() {
	routeHooks = append(routeHooks, func(a *App, mux *http.ServeMux) {
		for _, p := range []string{"storage", "cleanup"} {
			mux.HandleFunc("GET /api/v1/hosts/{host}/system/"+p, a.authed("system.view", a.agentGet("/v1/system/"+p)))
		}
		mux.HandleFunc("GET /api/v1/hosts/{host}/system/space", a.authed("system.view", func(w http.ResponseWriter, r *http.Request) {
			// the space view walks the whole tree: only for people who may read files anywhere
			u := currentUser(r)
			if len(u.Scope.Paths) > 0 && u.Role != RoleSuper {
				a.fail(w, r, errs.New(403, "auth.forbidden", "perm", "files.read"))
				return
			}
			a.agentGet("/v1/system/space")(w, r)
		}))
	})
	str := func(d string) change.Prop { return change.Prop{Type: "string", Desc: d} }
	reg := func(k change.Kind) {
		if k.Perm == "" {
			k.Perm = "disk.manage"
		}
		k.Scope, k.Title = "system:write", "kind."+k.Name
		change.Register(k)
	}

	reg(change.Kind{Name: "disk.mount", CLI: "berth disk mount {{.device}} {{.path}}",
		Params: change.ParamSchema{Props: map[string]change.Prop{"device": str("/dev/sdb1"), "path": str("mount point, e.g. /mnt/data"), "options": str("mount options (default: defaults)"),
			"persist": {Type: "boolean", Desc: "also at boot (writes /etc/fstab by UUID); default true"}}, Required: []string{"device", "path"}},
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			var p struct {
				Device, Path, Options string
				Persist               *bool
			}
			json.Unmarshal(raw, &p)
			persist := p.Persist == nil || *p.Persist
			cmds := []string{"mkdir -p " + p.Path, "mount " + p.Device + " " + p.Path}
			if persist {
				cmds = append(cmds, "/etc/fstab: + UUID=… "+p.Path+" … nofail  # berth  (checked with findmnt --verify)")
			}
			return change.Plan{Summary: map[bool]string{true: "plan.disk.mount_boot", false: "plan.disk.mount"}[persist], SummaryP: map[string]any{"dev": p.Device, "path": p.Path},
				Risk: change.RiskMedium, Undoable: true, Commands: cmds}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			var p struct {
				Device, Path, Options string
				Persist               *bool
			}
			json.Unmarshal(raw, &p)
			persist := p.Persist == nil || *p.Persist
			if err := svc(env).call(env.Ctx, env.Host, "POST", "/v1/system/storage/mount", map[string]any{"device": p.Device, "path": p.Path, "options": p.Options, "persist": persist}, nil); err != nil {
				return nil, nil, err
			}
			u, _ := json.Marshal(map[string]any{"path": p.Path, "persist": persist})
			return nil, &change.UndoRecipe{Kind: "disk.unmount", Params: u}, nil
		}})
	reg(change.Kind{Name: "disk.unmount", CLI: "berth disk unmount {{.path}}",
		Params: change.ParamSchema{Props: map[string]change.Prop{"path": str("mount point"), "persist": {Type: "boolean", Desc: "also remove it from /etc/fstab; default true"}}, Required: []string{"path"}},
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			var p struct {
				Path    string
				Persist *bool
			}
			json.Unmarshal(raw, &p)
			st, err := svc(env).storageNow(env)
			if err != nil {
				return change.Plan{}, err
			}
			dev, _ := st.mountedAt(p.Path)
			return change.Plan{Summary: "plan.disk.unmount", SummaryP: map[string]any{"path": p.Path, "dev": firstNonEmpty(dev, "—")}, Risk: change.RiskMedium, Undoable: dev != "",
				Commands: []string{"umount " + p.Path}}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			a := svc(env)
			var p struct {
				Path    string
				Persist *bool
			}
			json.Unmarshal(raw, &p)
			persist := p.Persist == nil || *p.Persist
			st, _ := a.storageNow(env)
			dev, _ := st.mountedAt(p.Path)
			if err := a.call(env.Ctx, env.Host, "POST", "/v1/system/storage/unmount", map[string]any{"path": p.Path, "persist": persist}, nil); err != nil {
				return nil, nil, err
			}
			if dev == "" {
				return nil, nil, nil
			}
			u, _ := json.Marshal(map[string]any{"device": dev, "path": p.Path, "persist": persist})
			return nil, &change.UndoRecipe{Kind: "disk.mount", Params: u}, nil
		}})
	reg(change.Kind{Name: "disk.format", CLI: "berth disk format {{.device}} {{.fstype}}", Heavy: true,
		Params: change.ParamSchema{Props: map[string]change.Prop{"device": str("/dev/sdb or /dev/sdb1 or /dev/md/name"), "fstype": {Type: "string", Enum: []string{"ext4", "xfs", "btrfs", "vfat", "exfat"}},
			"label": str("optional")}, Required: []string{"device", "fstype"}},
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			var p struct{ Device, FSType, Label string }
			json.Unmarshal(raw, &p)
			st, err := svc(env).storageNow(env)
			if err != nil {
				return change.Plan{}, err
			}
			for _, d := range st.Disks {
				if d.Path == p.Device && d.System {
					return change.Plan{}, errs.New(409, "system.disk_system")
				}
			}
			return change.Plan{Summary: "plan.disk.format", SummaryP: map[string]any{"dev": st.describe(p.Device), "fs": p.FSType}, Risk: change.RiskDestructive,
				Commands: []string{"wipefs -a " + p.Device, "mkfs." + p.FSType + " " + p.Device}}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			var p struct{ Device, FSType, Label string }
			json.Unmarshal(raw, &p)
			return nil, nil, agentOp(env, "/v1/system/storage/format", map[string]string{"device": p.Device, "fstype": p.FSType, "label": p.Label})
		}})
	reg(change.Kind{Name: "raid.create", CLI: "berth disk raid create {{.name}}", Heavy: true,
		Params: change.ParamSchema{Props: map[string]change.Prop{"kind": {Type: "string", Enum: []string{"md", "btrfs"}}, "name": str("array name"),
			"level": str("raid0 | raid1 | raid5 | raid6 | raid10 (btrfs: raid0 | raid1 | raid10 | raid1c3)"), "devices": {Type: "array", Desc: "member devices; all their data is lost"}},
			Required: []string{"kind", "name", "level", "devices"}},
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			var p struct {
				Kind, Name, Level string
				Devices           []string
			}
			json.Unmarshal(raw, &p)
			st, err := svc(env).storageNow(env)
			if err != nil {
				return change.Plan{}, err
			}
			names := []string{}
			for _, d := range p.Devices {
				names = append(names, st.describe(d))
			}
			cmd := fmt.Sprintf("mdadm --create /dev/md/%s --level=%s --raid-devices=%d %s", p.Name, strings.TrimPrefix(p.Level, "raid"), len(p.Devices), strings.Join(p.Devices, " "))
			if p.Kind == "btrfs" {
				cmd = fmt.Sprintf("mkfs.btrfs -L %s -d %s %s", p.Name, p.Level, strings.Join(p.Devices, " "))
			}
			return change.Plan{Summary: "plan.raid.create_" + p.Kind, SummaryP: map[string]any{"name": p.Name, "level": p.Level, "devs": strings.Join(names, "、")},
				Risk: change.RiskDestructive, Commands: []string{cmd}}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			var p map[string]any
			json.Unmarshal(raw, &p)
			p["op"] = "create"
			var out map[string]any
			return out, nil, svc(env).streamAgent(env, env.Host, "/v1/system/storage/raid", p, &out)
		}})
	reg(change.Kind{Name: "raid.repair", CLI: "berth disk raid {{.op}} {{.array}} {{.device}}", Heavy: true,
		Params: change.ParamSchema{Props: map[string]change.Prop{"array": str("/dev/md/name"), "op": {Type: "string", Enum: []string{"add", "remove"}}, "device": str("member device")},
			Required: []string{"array", "op", "device"}},
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			var p struct{ Array, Op, Device string }
			json.Unmarshal(raw, &p)
			risk := change.RiskHigh
			if p.Op == "add" {
				risk = change.RiskDestructive // the new member is wiped
			}
			return change.Plan{Summary: "plan.raid." + p.Op, SummaryP: map[string]any{"array": p.Array, "dev": p.Device}, Risk: risk,
				Commands: []string{"mdadm --manage " + p.Array + " --" + p.Op + " " + p.Device}}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			var p map[string]any
			json.Unmarshal(raw, &p)
			return nil, nil, agentOp(env, "/v1/system/storage/raid", p)
		}})
	reg(change.Kind{Name: "raid.scrub", CLI: "berth disk scrub {{.target}}", Perm: "system.manage",
		Params: change.ParamSchema{Props: map[string]change.Prop{"target": str("/dev/md/name, or a Btrfs mount point")}, Required: []string{"target"}},
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			var p struct{ Target string }
			json.Unmarshal(raw, &p)
			return change.Plan{Summary: "plan.raid.scrub", SummaryP: map[string]any{"target": p.Target}, Risk: change.RiskLow}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			var p struct{ Target string }
			json.Unmarshal(raw, &p)
			return nil, nil, agentOp(env, "/v1/system/storage/raid", map[string]string{"op": "scrub", "target": p.Target})
		}})
	reg(change.Kind{Name: "raid.delete", CLI: "berth disk raid delete {{.array}}", Heavy: true,
		Params: change.ParamSchema{Props: map[string]change.Prop{"array": str("/dev/md/name")}, Required: []string{"array"}},
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			var p struct{ Array string }
			json.Unmarshal(raw, &p)
			return change.Plan{Summary: "plan.raid.delete", SummaryP: map[string]any{"array": p.Array}, Risk: change.RiskDestructive,
				Commands: []string{"mdadm --stop " + p.Array, "mdadm --zero-superblock <members>"}}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			var p struct{ Array string }
			json.Unmarshal(raw, &p)
			return nil, nil, agentOp(env, "/v1/system/storage/raid", map[string]string{"op": "delete", "array": p.Array})
		}})
	reg(change.Kind{Name: "disk.cleanup", CLI: "berth disk cleanup {{.key}}", Perm: "system.manage", Heavy: true,
		Params: change.ParamSchema{Props: map[string]change.Prop{"key": {Type: "string", Enum: []string{"images", "buildcache", "journal", "pkgcache"}}}, Required: []string{"key"}},
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			var p struct{ Key string }
			json.Unmarshal(raw, &p)
			cmd := map[string]string{"images": "docker image prune (dangling only)", "buildcache": "docker builder prune", "journal": "journalctl --vacuum-time=14d --vacuum-size=200M",
				"pkgcache": "dnf clean packages / apt-get clean"}[p.Key]
			if cmd == "" {
				return change.Plan{}, errs.New(400, "params.enum", "name", "key", "allowed", "images, buildcache, journal, pkgcache")
			}
			return change.Plan{Summary: "plan.disk.cleanup_" + p.Key, Risk: change.RiskLow, Commands: []string{cmd}}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			var out map[string]any
			return out, nil, svc(env).streamAgent(env, env.Host, "/v1/system/cleanup", raw, &out)
		}})
	_ = url.PathEscape
}
