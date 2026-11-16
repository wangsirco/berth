package panel

// Quotas and ownership (方案 §5.15 配额). Berth remembers who created each site, compose
// project (and store app), virtual machine and workspace, with its size. A user's quota
// caps how many of each they may have and the CPU, memory and disk their machines and
// workspaces may add up to. The check runs in the change pipeline before the preview, so
// it applies to the panel, API, MCP and CLI alike. Super administrators have no quota.
// Site scope: a user limited to some sites may manage those and the ones they created.

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/wangsirco/berth/internal/change"
	"github.com/wangsirco/berth/internal/errs"
)

type Quota struct {
	Sites      int     `json:"sites,omitempty"`      // 0 = no limit
	Containers int     `json:"containers,omitempty"` // compose projects and store apps
	VMs        int     `json:"vms,omitempty"`
	CPUs       float64 `json:"cpus,omitempty"`    // machines + workspaces
	MemMB      int     `json:"mem_mb,omitempty"`  // machines + workspaces
	DiskGB     int     `json:"disk_gb,omitempty"` // machines
	TrafficGB  int     `json:"traffic_gb,omitempty"` // per calendar month, all their containers together
}

type Usage struct {
	Sites      int     `json:"sites"`
	Containers int     `json:"containers"`
	VMs        int     `json:"vms"`
	CPUs       float64 `json:"cpus"`
	MemMB      int     `json:"mem_mb"`
	DiskGB     int     `json:"disk_gb"`
	Traffic    int64   `json:"traffic_bytes"` // this month
}

func (a *App) quotaOf(uid int64) Quota {
	var s string
	a.db.QueryRow(`SELECT quota FROM users WHERE id=?`, uid).Scan(&s)
	var q Quota
	json.Unmarshal([]byte(s), &q)
	return q
}

func (a *App) usageOf(uid int64) Usage {
	var u Usage
	rows, err := a.db.Query(`SELECT type, COUNT(*), SUM(cpus), SUM(mem_mb), SUM(disk_gb) FROM owners WHERE user_id=? GROUP BY type`, uid)
	if err != nil {
		return u
	}
	defer rows.Close()
	for rows.Next() {
		var typ string
		var n, mem, disk int
		var cpus float64
		rows.Scan(&typ, &n, &cpus, &mem, &disk)
		switch typ {
		case "site":
			u.Sites = n
		case "compose":
			u.Containers = n
		case "vm":
			u.VMs = n
		}
		u.CPUs += cpus
		u.MemMB += mem
		u.DiskGB += disk
	}
	rows.Close()
	u.Traffic = a.userTraffic(uid)
	return u
}

// userTraffic: what the containers of everything a user owns moved this month — their
// compose projects and store apps (containers "<project>-…") and workspaces.
func (a *App) userTraffic(uid int64) int64 {
	rows, err := a.db.Query(`SELECT host, type, id FROM owners WHERE user_id=? AND type IN ('compose','dev')`, uid)
	if err != nil {
		return 0
	}
	type obj struct{ host, typ, id string }
	var objs []obj
	for rows.Next() {
		var o obj
		rows.Scan(&o.host, &o.typ, &o.id)
		objs = append(objs, o)
	}
	rows.Close()
	start := monthStart(time.Now())
	var total int64
	for _, o := range objs {
		var n int64
		if o.typ == "dev" {
			a.db.QueryRow(`SELECT COALESCE(SUM(rx+tx),0) FROM traffic_hourly WHERE host=? AND key=? AND hour>=?`, o.host, "berth-dev-"+o.id, start).Scan(&n)
		} else {
			a.db.QueryRow(`SELECT COALESCE(SUM(rx+tx),0) FROM traffic_hourly WHERE host=? AND substr(key, 1, ?)=? AND hour>=?`, o.host, len(o.id)+1, o.id+"-", start).Scan(&n)
		}
		total += n
	}
	return total
}

// checkUserTraffic warns at 80% and 100% of a user's monthly traffic (once each a month).
func (a *App) checkUserTraffic() {
	rows, err := a.db.Query(`SELECT id, username, quota FROM users WHERE quota LIKE '%traffic_gb%'`)
	if err != nil {
		return
	}
	type row struct {
		id   int64
		name string
		q    Quota
	}
	var list []row
	for rows.Next() {
		var r row
		var qs string
		rows.Scan(&r.id, &r.name, &qs)
		json.Unmarshal([]byte(qs), &r.q)
		if r.q.TrafficGB > 0 {
			list = append(list, r)
		}
	}
	rows.Close()
	month := time.Now().Format("2006-01")
	for _, r := range list {
		used := a.userTraffic(r.id)
		quota := int64(r.q.TrafficGB) << 30
		key := "user_traffic." + strconv.FormatInt(r.id, 10)
		prev := a.setting(key) // "2026-11:80" | "2026-11:100"
		level := ""
		switch {
		case used >= quota:
			level = "100"
		case used >= quota*8/10:
			level = "80"
		}
		if level == "" || prev == month+":100" || prev == month+":"+level {
			continue
		}
		a.setSetting(key, month+":"+level)
		if rule, ok := a.alertRule("user_traffic_quota"); ok {
			a.once(rule, "user/"+r.name+"/"+month+"/"+level, "local", map[string]any{"user": r.name, "gb": r.q.TrafficGB, "pct": level, "used_gb": strconv.FormatFloat(float64(used)/(1<<30), 'f', 1, 64)})
		}
	}
}

// personOf is the person behind an actor (a token's owner for tokens).
func (a *App) personOf(ac change.Actor) *User {
	var uid int64
	switch {
	case ac.Type == "user":
		uid, _ = strconv.ParseInt(ac.ID, 10, 64)
	case strings.HasPrefix(ac.ID, "token:"):
		a.db.QueryRow(`SELECT user_id FROM tokens WHERE id=?`, ac.ID[6:]).Scan(&uid)
	}
	if uid == 0 {
		return nil
	}
	u, err := a.userByID(uid)
	if err != nil {
		return nil
	}
	return u
}

// ownedObject describes what a create kind adds (type, id, size) — read from its params.
type ownedObject struct {
	Type   string
	ID     string
	CPUs   float64
	MemMB  int
	DiskGB int
}

func ownedBy(kind string, params, result json.RawMessage) (ownedObject, bool) {
	var p struct {
		Name     string  `json:"name"`
		App      string  `json:"app"`
		ID       int64   `json:"id"`
		Size     string  `json:"size"`
		CPUs     float64 `json:"cpus"`
		MemoryMB int     `json:"memory_mb"`
		DiskGB   int     `json:"disk_gb"`
	}
	json.Unmarshal(params, &p)
	switch kind {
	case "site.create":
		var r struct{ ID int64 }
		json.Unmarshal(result, &r)
		if r.ID == 0 {
			r.ID = p.ID
		}
		return ownedObject{Type: "site", ID: strconv.FormatInt(r.ID, 10)}, true
	case "compose.create":
		return ownedObject{Type: "compose", ID: p.Name}, true
	case "app.install":
		return ownedObject{Type: "compose", ID: firstNonEmpty(p.Name, p.App)}, true
	case "vm.create", "vm.resize":
		o := ownedObject{Type: "vm", ID: strings.ToLower(strings.TrimSpace(p.Name)), CPUs: p.CPUs, MemMB: p.MemoryMB, DiskGB: p.DiskGB}
		if sz, ok := vmSizes[p.Size]; ok && o.CPUs == 0 {
			o.CPUs, o.MemMB, o.DiskGB = float64(sz[0]), sz[1], sz[2]
		}
		return o, true
	case "dev.create":
		o := ownedObject{Type: "dev", ID: p.Name, CPUs: p.CPUs, MemMB: p.MemoryMB}
		for _, s := range devSizes {
			if s["id"] == p.Size && o.CPUs == 0 {
				o.CPUs, o.MemMB = float64(s["cpus"].(int)), s["memory_mb"].(int)
			}
		}
		return o, true
	}
	return ownedObject{}, false
}

var ownerDeletes = map[string]string{"site.delete": "site", "compose.remove": "compose", "app.remove": "compose", "vm.delete": "vm", "dev.delete": "dev"}

// recordOwner keeps the owners table in step with finished changes.
func (a *App) recordOwner(c *change.Change) {
	if c.Status != "done" {
		return
	}
	if typ, ok := ownerDeletes[c.Kind]; ok {
		var p struct {
			ID   int64  `json:"id"`
			Name string `json:"name"`
		}
		json.Unmarshal(c.Params, &p)
		id := p.Name
		if typ == "site" {
			id = strconv.FormatInt(p.ID, 10)
		}
		a.db.Exec(`DELETE FROM owners WHERE host=? AND type=? AND id=?`, c.Host, typ, id)
		return
	}
	o, ok := ownedBy(c.Kind, c.Params, c.Result)
	if !ok || o.ID == "" || o.ID == "0" {
		return
	}
	u := a.personOf(c.Actor)
	if c.Kind == "vm.resize" {
		a.db.Exec(`UPDATE owners SET cpus=?, mem_mb=? WHERE host=? AND type='vm' AND id=?`, o.CPUs, o.MemMB, c.Host, o.ID)
		return
	}
	if u == nil {
		return
	}
	a.db.Exec(`INSERT INTO owners(host, type, id, user_id, cpus, mem_mb, disk_gb, created_at) VALUES(?,?,?,?,?,?,?,?)
		ON CONFLICT(host, type, id) DO UPDATE SET user_id=excluded.user_id, cpus=excluded.cpus, mem_mb=excluded.mem_mb, disk_gb=excluded.disk_gb`,
		c.Host, o.Type, o.ID, u.ID, o.CPUs, o.MemMB, o.DiskGB, time.Now().Unix())
}

// checkQuota refuses a create (or resize) that would go over the actor's quota.
func (a *App) checkQuota(ac change.Actor, host, kind string, params json.RawMessage) error {
	if strings.HasPrefix(kind, "site.") && kind != "site.create" {
		var p struct {
			ID int64 `json:"id"`
		}
		json.Unmarshal(params, &p)
		if u := a.personOf(ac); u != nil && p.ID != 0 && !a.siteAllowed(u, host, p.ID) {
			return errs.New(403, "auth.site_forbidden")
		}
	}
	o, ok := ownedBy(kind, params, nil)
	if !ok {
		return nil
	}
	u := a.personOf(ac)
	if u == nil || u.Role == RoleSuper {
		return nil
	}
	q := a.quotaOf(u.ID)
	if q == (Quota{}) {
		return nil
	}
	use := a.usageOf(u.ID)
	if q.TrafficGB > 0 && use.Traffic >= int64(q.TrafficGB)<<30 && kind != "vm.resize" {
		return errs.New(403, "quota.over_traffic", "limit", strconv.Itoa(q.TrafficGB))
	}
	over := func(what string, used, add, limit float64) error {
		if limit > 0 && used+add > limit {
			return errs.New(403, "quota.over_"+what, "used", strconv.FormatFloat(used, 'f', -1, 64), "limit", strconv.FormatFloat(limit, 'f', -1, 64))
		}
		return nil
	}
	if kind == "vm.resize" { // only the growth counts
		var cur ownedObject
		a.db.QueryRow(`SELECT cpus, mem_mb FROM owners WHERE host=? AND type='vm' AND id=?`, host, o.ID).Scan(&cur.CPUs, &cur.MemMB)
		o.CPUs, o.MemMB, o.DiskGB = o.CPUs-cur.CPUs, o.MemMB-cur.MemMB, 0
	} else {
		count := map[string][2]int{"site": {use.Sites, q.Sites}, "compose": {use.Containers, q.Containers}, "vm": {use.VMs, q.VMs}}[o.Type]
		if err := over(o.Type, float64(count[0]), 1, float64(count[1])); err != nil {
			return err
		}
	}
	if err := over("cpus", use.CPUs, o.CPUs, q.CPUs); err != nil {
		return err
	}
	if err := over("mem_mb", float64(use.MemMB), float64(o.MemMB), float64(q.MemMB)); err != nil {
		return err
	}
	return over("disk_gb", float64(use.DiskGB), float64(o.DiskGB), float64(q.DiskGB))
}

// siteAllowed: a user with a site scope manages only those sites and the ones they made.
func (a *App) siteAllowed(u *User, host string, id int64) bool {
	if u.Role == RoleSuper || len(u.Scope.Sites) == 0 {
		return true
	}
	for _, s := range u.Scope.Sites {
		if s == id {
			return true
		}
	}
	var owner int64
	a.db.QueryRow(`SELECT user_id FROM owners WHERE host=? AND type='site' AND id=?`, host, strconv.FormatInt(id, 10)).Scan(&owner)
	return owner == u.ID
}

func init() {
	routeHooks = append(routeHooks, func(a *App, mux *http.ServeMux) {
		mux.HandleFunc("GET /api/v1/me/usage", a.authed("", func(w http.ResponseWriter, r *http.Request) {
			u := currentUser(r)
			writeJSON(w, 200, map[string]any{"quota": a.quotaOf(u.ID), "usage": a.usageOf(u.ID), "unlimited": u.Role == RoleSuper})
		}))
		mux.HandleFunc("GET /api/v1/users/{id}/usage", a.superOnly(func(w http.ResponseWriter, r *http.Request) {
			id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
			writeJSON(w, 200, map[string]any{"quota": a.quotaOf(id), "usage": a.usageOf(id)})
		}))
	})
	change.Register(change.Kind{
		Name: "user.quota", Perm: "users.manage", Scope: "users:write", Title: "kind.user.quota", CLI: "berth user quota {{.id}}",
		Params: change.ParamSchema{Props: map[string]change.Prop{"id": {Type: "integer"}, "quota": {Type: "object",
			Desc: `{"sites":5,"containers":10,"vms":2,"cpus":4,"mem_mb":8192,"disk_gb":100,"traffic_gb":500}; 0 or missing = no limit`}}, Required: []string{"id", "quota"}},
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			var p struct {
				ID    int64
				Quota Quota
			}
			json.Unmarshal(raw, &p)
			u, err := svc(env).getUser(p.ID)
			if err != nil {
				return change.Plan{}, err
			}
			q := p.Quota
			if q.Sites < 0 || q.Containers < 0 || q.VMs < 0 || q.CPUs < 0 || q.MemMB < 0 || q.DiskGB < 0 || q.TrafficGB < 0 {
				return change.Plan{}, errs.New(400, "quota.bad")
			}
			b, _ := json.Marshal(q)
			return change.Plan{Summary: "plan.user.quota", SummaryP: map[string]any{"username": u.Username}, Risk: change.RiskLow, Undoable: true,
				Commands: []string{string(b)}, Affected: []change.Object{userObj(u)}}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			a := svc(env)
			var p struct {
				ID    int64
				Quota Quota
			}
			json.Unmarshal(raw, &p)
			old := a.quotaOf(p.ID)
			b, _ := json.Marshal(p.Quota)
			if p.Quota == (Quota{}) {
				b = nil
			}
			if _, err := a.db.Exec(`UPDATE users SET quota=? WHERE id=?`, string(b), p.ID); err != nil {
				return nil, nil, err
			}
			u, _ := json.Marshal(map[string]any{"id": p.ID, "quota": old})
			return nil, &change.UndoRecipe{Kind: "user.quota", Params: u}, nil
		},
	})
}
