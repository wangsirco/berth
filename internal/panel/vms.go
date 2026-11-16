package panel

// Virtual machines and system containers on Incus (方案 §5.4.1). The agent talks to Incus;
// here are the changes (preview, audit, undo), personal SSH keys and generated passwords.

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/wangsirco/berth/internal/agent/incus"
	"github.com/wangsirco/berth/internal/change"
	"github.com/wangsirco/berth/internal/errs"
)

type vmStatus struct {
	Status incus.Status  `json:"status"`
	Images []incus.Image `json:"images"`
}

func (a *App) vmStatus(ctx context.Context, host string) (vmStatus, error) {
	var st vmStatus
	err := a.call(ctx, host, "GET", "/v1/vms/status", nil, &st)
	return st, err
}

func (a *App) vm(ctx context.Context, host, name string) (incus.Instance, error) {
	var in incus.Instance
	err := a.call(ctx, host, "GET", "/v1/vms/"+name, nil, &in)
	return in, err
}

// sizes are the presets the form offers (custom is allowed too).
var vmSizes = map[string][3]int{"small": {1, 512, 10}, "medium": {2, 2048, 20}, "large": {4, 4096, 40}}

// ---- SSH keys (per person; used for new machines, dev containers, workspaces) ----

var sshKeyRe = regexp.MustCompile(`^(ssh-(ed25519|rsa)|ecdsa-sha2-nistp(256|384|521)|sk-ssh-ed25519@openssh\.com) [A-Za-z0-9+/=]{40,}( [^\r\n]{0,200})?$`)

func (a *App) sshKeys(uid int64) []map[string]any {
	rows, err := a.db.Query(`SELECT id, name, key, created_at FROM user_ssh_keys WHERE user_id=? ORDER BY id`, uid)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id, at int64
		var name, key string
		rows.Scan(&id, &name, &key, &at)
		f := strings.Fields(key)
		short := key
		if len(f) >= 2 && len(f[1]) > 16 {
			short = f[0] + " …" + f[1][len(f[1])-12:]
		}
		out = append(out, map[string]any{"id": id, "name": name, "key": short, "full": key, "created_at": at})
	}
	return out
}

func (a *App) sshKeyLines(uid int64) []string {
	var out []string
	for _, k := range a.sshKeys(uid) {
		out = append(out, k["full"].(string))
	}
	return out
}

func randomPassword(n int) string {
	const abc = "abcdefghijkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	b := make([]byte, n)
	for i := range b {
		x, _ := rand.Int(rand.Reader, big.NewInt(int64(len(abc))))
		b[i] = abc[x.Int64()]
	}
	return string(b)
}

// cloudInit builds user-data: SSH server, the person's keys for root, and a password.
func cloudInit(osName string, keys []string, password string) string {
	pkg := "openssh-server"
	if osName == "alpine" {
		pkg = "openssh"
	}
	var b strings.Builder
	b.WriteString("#cloud-config\n")
	b.WriteString("packages: [" + pkg + "]\n")
	b.WriteString("disable_root: false\n")
	if len(keys) > 0 {
		b.WriteString("ssh_pwauth: false\n")
		b.WriteString("users:\n  - name: root\n    ssh_authorized_keys:\n")
		for _, k := range keys {
			b.WriteString("      - " + jsonString(k) + "\n")
		}
	} else {
		b.WriteString("ssh_pwauth: true\n")
	}
	if password != "" {
		b.WriteString("chpasswd:\n  expire: false\n  users:\n    - name: root\n      password: " + jsonString(password) + "\n      type: text\n")
	}
	// the SSH service has different names; enable whichever exists
	b.WriteString("runcmd:\n  - [sh, -c, \"(systemctl enable --now ssh || systemctl enable --now sshd || (rc-update add sshd && rc-service sshd start)) 2>/dev/null; true\"]\n")
	if len(keys) == 0 {
		b.WriteString("  - [sh, -c, \"sed -i 's/^#\\\\?PermitRootLogin.*/PermitRootLogin yes/' /etc/ssh/sshd_config; (systemctl restart ssh || systemctl restart sshd || rc-service sshd restart) 2>/dev/null; true\"]\n")
	}
	return b.String()
}

func jsonString(s string) string { b, _ := json.Marshal(s); return string(b) }

// ---- API ----

func init() {
	routeHooks = append(routeHooks, func(a *App, mux *http.ServeMux) {
		mux.HandleFunc("GET /api/v1/hosts/{host}/vms", a.authed("vms.view", func(w http.ResponseWriter, r *http.Request) {
			host := r.PathValue("host")
			st, err := a.vmStatus(r.Context(), host)
			if err != nil {
				a.fail(w, r, err)
				return
			}
			items := []incus.Instance{}
			if st.Status.Running {
				a.call(r.Context(), host, "GET", "/v1/vms", nil, &items)
			}
			pw := map[string]bool{}
			rows, _ := a.db.Query(`SELECT name FROM vm_secrets WHERE host=?`, host)
			for rows != nil && rows.Next() {
				var n string
				rows.Scan(&n)
				pw[n] = true
			}
			if rows != nil {
				rows.Close()
			}
			writeJSON(w, 200, map[string]any{"status": st.Status, "images": st.Images, "items": items, "has_password": pw,
				"sizes": vmSizes, "my_keys": len(a.sshKeys(currentUser(r).ID))})
		}))
		mux.HandleFunc("POST /api/v1/hosts/{host}/vms/{name}/password", a.authed("vms.manage", func(w http.ResponseWriter, r *http.Request) {
			if err := a.requireReauth(r); err != nil {
				a.fail(w, r, err)
				return
			}
			var enc []byte
			if a.db.QueryRow(`SELECT password_enc FROM vm_secrets WHERE host=? AND name=?`, r.PathValue("host"), r.PathValue("name")).Scan(&enc) != nil {
				a.fail(w, r, errs.New(404, "vm.no_password"))
				return
			}
			p, err := a.open(enc)
			if err != nil {
				a.fail(w, r, err)
				return
			}
			a.pipe.Record(currentActor(r), r.PathValue("host"), "vm.password_view", map[string]any{"name": r.PathValue("name")}, change.Plan{
				Summary: "plan.vm.password_view", SummaryP: map[string]any{"name": r.PathValue("name")}, Risk: change.RiskLow})
			writeJSON(w, 200, map[string]string{"password": string(p), "user": "root"})
		}))
		// personal SSH keys
		mux.HandleFunc("GET /api/v1/me/ssh-keys", a.authed("", func(w http.ResponseWriter, r *http.Request) {
			keys := a.sshKeys(currentUser(r).ID)
			for _, k := range keys {
				delete(k, "full")
			}
			writeJSON(w, 200, map[string]any{"items": keys})
		}))
		mux.HandleFunc("POST /api/v1/me/ssh-keys", a.authed("", func(w http.ResponseWriter, r *http.Request) {
			var in struct{ Name, Key string }
			readJSON(r, &in)
			in.Key = strings.TrimSpace(strings.ReplaceAll(in.Key, "\r", ""))
			if !sshKeyRe.MatchString(in.Key) {
				a.fail(w, r, errs.New(400, "vm.bad_ssh_key"))
				return
			}
			if in.Name == "" {
				if f := strings.Fields(in.Key); len(f) > 2 {
					in.Name = f[2]
				} else {
					in.Name = f[0]
				}
			}
			a.db.Exec(`INSERT INTO user_ssh_keys(user_id, name, key, created_at) VALUES(?,?,?,?)`, currentUser(r).ID, truncate(in.Name, 80), in.Key, time.Now().Unix())
			writeJSON(w, 200, map[string]bool{"ok": true})
		}))
		mux.HandleFunc("DELETE /api/v1/me/ssh-keys/{id}", a.authed("", func(w http.ResponseWriter, r *http.Request) {
			a.db.Exec(`DELETE FROM user_ssh_keys WHERE id=? AND user_id=?`, r.PathValue("id"), currentUser(r).ID)
			writeJSON(w, 200, map[string]bool{"ok": true})
		}))
	})

	nameProp := map[string]change.Prop{"name": {Type: "string"}}
	nameOnly := change.ParamSchema{Props: nameProp, Required: []string{"name"}}

	change.Register(change.Kind{
		Name: "vm.install", Perm: "vms.manage", Scope: "system:write", Title: "kind.vm.install",
		Params: change.ParamSchema{Props: map[string]change.Prop{}}, CLI: "berth vm install", Heavy: true, MemMB: 300,
		Plan: func(env change.Env, _ json.RawMessage) (change.Plan, error) {
			return change.Plan{Summary: "plan.vm.install", Risk: change.RiskMedium, Commands: []string{
				"dnf install -y " + strings.Join(incus.Packages, " "),
				"echo root:1000000:1000000000 >> /etc/subuid /etc/subgid",
				"# ARM: /etc/systemd/system/incus.service.d/berth-edk2.conf → INCUS_EDK2_PATH=/usr/share/AAVMF",
				"systemctl enable --now incus; incus admin init --minimal",
				"firewall-cmd --zone=trusted --change-interface=incusbr0 (--permanent)",
				"iptables -I DOCKER-USER -i/-o incusbr0 -j ACCEPT",
			}}, nil
		},
		Apply: func(env change.Env, _ json.RawMessage) (any, *change.UndoRecipe, error) {
			var out any
			return out, nil, svc(env).streamAgent(env, env.Host, "/v1/vms/install", nil, &out)
		},
	})

	createProps := map[string]change.Prop{"name": {Type: "string"}, "image": {Type: "string"}, "vm": {Type: "boolean", Desc: "full virtual machine instead of a system container"},
		"size": {Type: "string", Enum: []string{"small", "medium", "large", "custom"}}, "cpus": {Type: "integer"}, "memory_mb": {Type: "integer"}, "disk_gb": {Type: "integer"},
		"note": {Type: "string"}}
	type createParams struct {
		Name     string `json:"name"`
		Image    string `json:"image"`
		VM       bool   `json:"vm"`
		Size     string `json:"size"`
		CPUs     int    `json:"cpus"`
		MemoryMB int    `json:"memory_mb"`
		DiskGB   int    `json:"disk_gb"`
		Note     string `json:"note"`
	}
	normalize := func(a *App, env change.Env, raw json.RawMessage) (createParams, incus.Image, error) {
		var p createParams
		json.Unmarshal(raw, &p)
		p.Name = strings.ToLower(strings.TrimSpace(p.Name))
		if !incus.ValidName(p.Name) {
			return p, incus.Image{}, errs.New(400, "vm.bad_name")
		}
		var img incus.Image
		for _, i := range incus.Images {
			if i.Alias == p.Image {
				img = i
			}
		}
		if img.Alias == "" {
			return p, img, errs.New(400, "vm.bad_image")
		}
		if p.VM && !img.VM {
			return p, img, errs.New(400, "vm.no_vm_image", "image", img.Name)
		}
		if sz, ok := vmSizes[p.Size]; ok {
			p.CPUs, p.MemoryMB, p.DiskGB = sz[0], sz[1], sz[2]
		}
		if p.CPUs < 1 || p.CPUs > 64 || p.MemoryMB < 128 || p.MemoryMB > 262144 || p.DiskGB < 2 || p.DiskGB > 4096 {
			return p, img, errs.New(400, "vm.bad_size")
		}
		if p.VM && p.MemoryMB < 512 {
			return p, img, errs.New(400, "vm.bad_size")
		}
		return p, img, nil
	}

	change.Register(change.Kind{
		Name: "vm.create", Perm: "vms.manage", Scope: "system:write", Title: "kind.vm.create",
		Params: change.ParamSchema{Props: createProps, Required: []string{"name", "image"}}, CLI: "berth vm create {{.name}} --image {{.image}}",
		Heavy: true, MemMB: 128,
		MemFor: func(raw json.RawMessage) int { // the new machine's memory must fit too
			var p createParams
			json.Unmarshal(raw, &p)
			if sz, ok := vmSizes[p.Size]; ok {
				p.MemoryMB = sz[1]
			}
			if p.VM {
				return p.MemoryMB / 2
			}
			return 64
		},
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			a := svc(env)
			p, img, err := normalize(a, env, raw)
			if err != nil {
				return change.Plan{}, err
			}
			st, err := a.vmStatus(env.Ctx, env.Host)
			if err != nil {
				return change.Plan{}, err
			}
			if !st.Status.Running {
				return change.Plan{}, errs.New(409, "vm.not_installed")
			}
			if p.VM && !st.Status.VMs {
				return change.Plan{}, errs.New(409, "vm.no_kvm")
			}
			if _, err := a.vm(env.Ctx, env.Host, p.Name); err == nil {
				return change.Plan{}, errs.New(409, "vm.exists", "name", p.Name)
			}
			kind := "plan.vm.create_ct"
			if p.VM {
				kind = "plan.vm.create_vm"
			}
			return change.Plan{Summary: kind, SummaryP: map[string]any{"name": p.Name, "image": img.Name, "cpus": p.CPUs, "mem": p.MemoryMB, "disk": p.DiskGB},
				Risk: change.RiskLow, Undoable: true,
				Commands: []string{fmt.Sprintf("incus launch images:%s %s%s -c limits.cpu=%d -c limits.memory=%dMiB -d root,size=%dGiB",
					img.Alias, p.Name, map[bool]string{true: " --vm"}[p.VM], p.CPUs, p.MemoryMB, p.DiskGB)}}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			a := svc(env)
			p, img, err := normalize(a, env, raw)
			if err != nil {
				return nil, nil, err
			}
			var keys []string
			if env.ActorType == "user" {
				var uid int64
				fmt.Sscanf(env.ActorID, "%d", &uid)
				keys = a.sshKeyLines(uid)
			}
			password := ""
			if img.Cloud {
				password = randomPassword(16)
			}
			spec := incus.CreateSpec{Name: p.Name, Image: img.Alias, VM: p.VM, CPUs: p.CPUs, MemoryMB: p.MemoryMB, DiskGB: p.DiskGB, Note: p.Note, Start: true}
			if img.Cloud {
				spec.UserData = cloudInit(img.OS, keys, password)
			}
			if err := a.streamAgent(env, env.Host, "/v1/vms", spec, nil); err != nil {
				return nil, nil, err
			}
			if password != "" {
				enc, _ := a.seal([]byte(password))
				a.db.Exec(`INSERT OR REPLACE INTO vm_secrets(host, name, password_enc, created_at) VALUES(?,?,?,?)`, env.Host, p.Name, enc, time.Now().Unix())
			}
			a.relate(env.Host, change.Object{Type: "host", ID: env.Host}, change.Object{Type: "vm", ID: p.Name, Name: p.Name}, "manages", "module")
			u, _ := json.Marshal(map[string]string{"name": p.Name})
			return map[string]any{"name": p.Name, "ssh_keys": len(keys), "password": password != ""}, &change.UndoRecipe{Kind: "vm.delete", Params: u}, nil
		},
	})

	stateKind := func(name, action, undo string, risk change.Risk, force bool) change.Kind {
		return change.Kind{
			Name: name, Perm: "vms.manage", Scope: "system:write", Title: "kind." + name, Params: nameOnly, CLI: "berth vm " + action + " {{.name}}",
			Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
				var p struct{ Name string }
				json.Unmarshal(raw, &p)
				if _, err := svc(env).vm(env.Ctx, env.Host, p.Name); err != nil {
					return change.Plan{}, err
				}
				return change.Plan{Summary: "plan." + name, SummaryP: map[string]any{"name": p.Name}, Risk: risk, Undoable: undo != "",
					Commands: []string{"incus " + action + " " + p.Name + map[bool]string{true: " --force"}[force]}}, nil
			},
			Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
				var p struct{ Name string }
				json.Unmarshal(raw, &p)
				act := action
				if action == "kill" {
					act = "stop"
				}
				if err := svc(env).call(env.Ctx, env.Host, "POST", "/v1/vms/"+p.Name+"/state", map[string]any{"action": act, "force": force}, nil); err != nil {
					return nil, nil, err
				}
				if undo == "" {
					return nil, nil, nil
				}
				return nil, &change.UndoRecipe{Kind: undo, Params: raw}, nil
			},
		}
	}
	change.Register(stateKind("vm.start", "start", "vm.stop", change.RiskLow, false))
	change.Register(stateKind("vm.stop", "stop", "vm.start", change.RiskMedium, false))
	change.Register(stateKind("vm.restart", "restart", "", change.RiskMedium, false))
	change.Register(stateKind("vm.kill", "kill", "vm.start", change.RiskHigh, true))

	change.Register(change.Kind{
		Name: "vm.delete", Perm: "vms.manage", Scope: "system:write", Title: "kind.vm.delete", Params: nameOnly, CLI: "berth vm delete {{.name}}",
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			var p struct{ Name string }
			json.Unmarshal(raw, &p)
			in, err := svc(env).vm(env.Ctx, env.Host, p.Name)
			if err != nil {
				return change.Plan{}, err
			}
			return change.Plan{Summary: "plan.vm.delete", SummaryP: map[string]any{"name": p.Name, "snapshots": len(in.Snapshots)}, Risk: change.RiskHigh,
				Commands: []string{"incus delete " + p.Name + " --force   # disk and " + fmt.Sprint(len(in.Snapshots)) + " snapshots"}}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			a := svc(env)
			var p struct{ Name string }
			json.Unmarshal(raw, &p)
			a.call(env.Ctx, env.Host, "POST", "/v1/vms/"+p.Name+"/state", map[string]any{"action": "stop", "force": true}, nil)
			if err := a.call(env.Ctx, env.Host, "DELETE", "/v1/vms/"+p.Name, nil, nil); err != nil {
				return nil, nil, err
			}
			a.db.Exec(`DELETE FROM vm_secrets WHERE host=? AND name=?`, env.Host, p.Name)
			a.unrelate(env.Host, change.Object{Type: "host", ID: env.Host}, change.Object{Type: "vm", ID: p.Name}, "manages")
			return nil, nil, nil
		},
	})

	snapProps := change.ParamSchema{Props: map[string]change.Prop{"name": {Type: "string"}, "snapshot": {Type: "string"}}, Required: []string{"name"}}
	change.Register(change.Kind{
		Name: "vm.snapshot", Perm: "vms.manage", Scope: "system:write", Title: "kind.vm.snapshot", Params: snapProps, CLI: "berth vm snapshot {{.name}}",
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			var p struct{ Name, Snapshot string }
			json.Unmarshal(raw, &p)
			if p.Snapshot == "" {
				p.Snapshot = "snap-" + time.Now().Format("20060102-1504")
			}
			if !incus.ValidName(p.Snapshot) {
				return change.Plan{}, errs.New(400, "vm.bad_name")
			}
			return change.Plan{Summary: "plan.vm.snapshot", SummaryP: map[string]any{"name": p.Name, "snapshot": p.Snapshot}, Risk: change.RiskLow, Undoable: true}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			var p struct{ Name, Snapshot string }
			json.Unmarshal(raw, &p)
			if p.Snapshot == "" {
				p.Snapshot = "snap-" + time.Now().Format("20060102-1504")
			}
			if err := svc(env).call(env.Ctx, env.Host, "POST", "/v1/vms/"+p.Name+"/snapshots", map[string]string{"snapshot": p.Snapshot}, nil); err != nil {
				return nil, nil, err
			}
			u, _ := json.Marshal(map[string]string{"name": p.Name, "snapshot": p.Snapshot})
			return map[string]string{"snapshot": p.Snapshot}, &change.UndoRecipe{Kind: "vm.snapshot.delete", Params: u}, nil
		},
	})
	change.Register(change.Kind{
		Name: "vm.snapshot.delete", Perm: "vms.manage", Scope: "system:write", Title: "kind.vm.snapshot.delete", Params: snapProps, CLI: "berth vm snapshot delete {{.name}} {{.snapshot}}",
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			var p struct{ Name, Snapshot string }
			json.Unmarshal(raw, &p)
			return change.Plan{Summary: "plan.vm.snapshot.delete", SummaryP: map[string]any{"name": p.Name, "snapshot": p.Snapshot}, Risk: change.RiskMedium}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			var p struct{ Name, Snapshot string }
			json.Unmarshal(raw, &p)
			return nil, nil, svc(env).call(env.Ctx, env.Host, "DELETE", "/v1/vms/"+p.Name+"/snapshots/"+p.Snapshot, nil, nil)
		},
	})
	change.Register(change.Kind{
		Name: "vm.restore", Perm: "vms.manage", Scope: "system:write", Title: "kind.vm.restore", Params: snapProps, CLI: "berth vm restore {{.name}} {{.snapshot}}",
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			var p struct{ Name, Snapshot string }
			json.Unmarshal(raw, &p)
			return change.Plan{Summary: "plan.vm.restore", SummaryP: map[string]any{"name": p.Name, "snapshot": p.Snapshot}, Risk: change.RiskHigh, Undoable: true,
				Commands: []string{"incus snapshot create " + p.Name + " before-restore-…   # so this can be undone", "incus snapshot restore " + p.Name + " " + p.Snapshot}}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			a := svc(env)
			var p struct{ Name, Snapshot string }
			json.Unmarshal(raw, &p)
			safety := "before-restore-" + time.Now().Format("20060102-150405")
			if err := a.call(env.Ctx, env.Host, "POST", "/v1/vms/"+p.Name+"/snapshots", map[string]string{"snapshot": safety}, nil); err != nil {
				return nil, nil, err
			}
			if err := a.call(env.Ctx, env.Host, "POST", "/v1/vms/"+p.Name+"/restore", map[string]string{"snapshot": p.Snapshot}, nil); err != nil {
				return nil, nil, err
			}
			u, _ := json.Marshal(map[string]string{"name": p.Name, "snapshot": safety})
			return map[string]string{"safety_snapshot": safety}, &change.UndoRecipe{Kind: "vm.restore", Params: u}, nil
		},
	})
	change.Register(change.Kind{
		Name: "vm.clone", Perm: "vms.manage", Scope: "system:write", Title: "kind.vm.clone",
		Params: change.ParamSchema{Props: map[string]change.Prop{"name": {Type: "string"}, "to": {Type: "string"}}, Required: []string{"name", "to"}},
		CLI:    "berth vm clone {{.name}} {{.to}}", Heavy: true, MemMB: 64,
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			var p struct{ Name, To string }
			json.Unmarshal(raw, &p)
			if !incus.ValidName(p.To) {
				return change.Plan{}, errs.New(400, "vm.bad_name")
			}
			return change.Plan{Summary: "plan.vm.clone", SummaryP: map[string]any{"name": p.Name, "to": p.To}, Risk: change.RiskLow, Undoable: true}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			a := svc(env)
			var p struct{ Name, To string }
			json.Unmarshal(raw, &p)
			if err := a.call(env.Ctx, env.Host, "POST", "/v1/vms/"+p.Name+"/clone", map[string]string{"to": p.To}, nil); err != nil {
				return nil, nil, err
			}
			var enc []byte // the copy has the same root password
			if a.db.QueryRow(`SELECT password_enc FROM vm_secrets WHERE host=? AND name=?`, env.Host, p.Name).Scan(&enc) == nil {
				a.db.Exec(`INSERT OR REPLACE INTO vm_secrets(host, name, password_enc, created_at) VALUES(?,?,?,?)`, env.Host, p.To, enc, time.Now().Unix())
			}
			u, _ := json.Marshal(map[string]string{"name": p.To})
			return nil, &change.UndoRecipe{Kind: "vm.delete", Params: u}, nil
		},
	})
	change.Register(change.Kind{
		Name: "vm.resize", Perm: "vms.manage", Scope: "system:write", Title: "kind.vm.resize",
		Params: change.ParamSchema{Props: map[string]change.Prop{"name": {Type: "string"}, "cpus": {Type: "integer"}, "memory_mb": {Type: "integer"}, "disk_gb": {Type: "integer"}}, Required: []string{"name", "cpus", "memory_mb"}},
		CLI:    "berth vm resize {{.name}}",
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			var p struct {
				Name     string
				CPUs     int `json:"cpus"`
				MemoryMB int `json:"memory_mb"`
				DiskGB   int `json:"disk_gb"`
			}
			json.Unmarshal(raw, &p)
			in, err := svc(env).vm(env.Ctx, env.Host, p.Name)
			if err != nil {
				return change.Plan{}, err
			}
			if p.CPUs < 1 || p.MemoryMB < 128 {
				return change.Plan{}, errs.New(400, "vm.bad_size")
			}
			sum := "plan.vm.resize"
			if in.Type == "virtual-machine" && in.Status == "Running" {
				sum = "plan.vm.resize_restart"
			}
			return change.Plan{Summary: sum, SummaryP: map[string]any{"name": p.Name, "cpus": p.CPUs, "mem": p.MemoryMB, "disk": p.DiskGB}, Risk: change.RiskMedium, Undoable: true}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			a := svc(env)
			var p struct {
				Name     string
				CPUs     int `json:"cpus"`
				MemoryMB int `json:"memory_mb"`
				DiskGB   int `json:"disk_gb"`
			}
			json.Unmarshal(raw, &p)
			before, err := a.vm(env.Ctx, env.Host, p.Name)
			if err != nil {
				return nil, nil, err
			}
			if err := a.call(env.Ctx, env.Host, "POST", "/v1/vms/"+p.Name+"/resize", map[string]int{"cpus": p.CPUs, "memory_mb": p.MemoryMB, "disk_gb": p.DiskGB}, nil); err != nil {
				return nil, nil, err
			}
			var cpus, mem int
			fmt.Sscanf(before.CPUs, "%d", &cpus)
			fmt.Sscanf(strings.TrimSuffix(before.Memory, "MiB"), "%d", &mem)
			if strings.HasSuffix(before.Memory, "GiB") {
				fmt.Sscanf(strings.TrimSuffix(before.Memory, "GiB"), "%d", &mem)
				mem *= 1024
			}
			if cpus == 0 || mem == 0 {
				return nil, nil, nil // no earlier limits to go back to
			}
			u, _ := json.Marshal(map[string]any{"name": p.Name, "cpus": cpus, "memory_mb": mem})
			return nil, &change.UndoRecipe{Kind: "vm.resize", Params: u}, nil
		},
	})
	change.Register(change.Kind{
		Name: "vm.forward", Perm: "vms.manage", Scope: "system:write", Title: "kind.vm.forward",
		Params: change.ParamSchema{Props: map[string]change.Prop{"name": {Type: "string"}, "proto": {Type: "string", Enum: []string{"tcp", "udp"}},
			"host_port": {Type: "integer"}, "target_port": {Type: "integer", Desc: "0 removes the forward"}}, Required: []string{"name", "proto", "host_port", "target_port"}},
		CLI: "berth vm forward {{.name}} {{.host_port}}:{{.target_port}}",
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			a := svc(env)
			var p struct {
				Name       string
				Proto      string
				HostPort   int `json:"host_port"`
				TargetPort int `json:"target_port"`
			}
			json.Unmarshal(raw, &p)
			if p.HostPort < 1 || p.HostPort > 65535 || p.TargetPort < 0 || p.TargetPort > 65535 {
				return change.Plan{}, errs.New(400, "vm.bad_forward")
			}
			if p.TargetPort > 0 {
				if used, _ := a.hostPortsInUse(env.Ctx, env.Host); used[p.HostPort] {
					return change.Plan{}, errs.New(409, "vm.port_in_use", "port", p.HostPort)
				}
			}
			sum := "plan.vm.forward"
			if p.TargetPort == 0 {
				sum = "plan.vm.forward_remove"
			}
			return change.Plan{Summary: sum, SummaryP: map[string]any{"name": p.Name, "host": p.HostPort, "target": p.TargetPort, "proto": p.Proto}, Risk: change.RiskMedium, Undoable: true}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			a := svc(env)
			var p struct {
				Name       string
				Proto      string
				HostPort   int `json:"host_port"`
				TargetPort int `json:"target_port"`
			}
			json.Unmarshal(raw, &p)
			before, _ := a.vm(env.Ctx, env.Host, p.Name)
			old := 0
			for _, f := range before.Forwards {
				if f.HostPort == p.HostPort && f.Proto == p.Proto {
					old = f.TargetPort
				}
			}
			if err := a.call(env.Ctx, env.Host, "POST", "/v1/vms/"+p.Name+"/forward", map[string]any{"proto": p.Proto, "host_port": p.HostPort, "target_port": p.TargetPort}, nil); err != nil {
				return nil, nil, err
			}
			u, _ := json.Marshal(map[string]any{"name": p.Name, "proto": p.Proto, "host_port": p.HostPort, "target_port": old})
			return nil, &change.UndoRecipe{Kind: "vm.forward", Params: u}, nil
		},
	})
}

// hostPortsInUse: what already listens on the host (and what Docker publishes).
func (a *App) hostPortsInUse(ctx context.Context, host string) (map[int]bool, error) {
	var ports []int
	if err := a.call(ctx, host, "GET", "/v1/net/listening", nil, &ports); err != nil {
		return nil, err
	}
	out := map[int]bool{}
	for _, p := range ports {
		out[p] = true
	}
	return out, nil
}
