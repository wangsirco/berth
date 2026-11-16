package panel

// Server migration (R6, 整机迁移): everything Berth manages on one host — sites, compose apps
// with their volumes, native projects, native databases, VMs, scheduled jobs, firewall rules,
// certificates, chosen folders and the panel settings they rely on — as one stream that is
// written into place on another host as it arrives.
//
// What makes it fit a smaller disk (1Panel's failure): only data travels, never the machine's
// own layout (partitions, mounts, swap, VM disk sizes); every item says how much room it takes
// and where, measured as blocks in use; the target checks that per filesystem before starting
// and again before each item; and nothing is kept as an extra copy on the target.

import (
	"archive/tar"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/wangsirco/berth/internal/agent/caddy"
	"github.com/wangsirco/berth/internal/agent/migrate"
	"github.com/wangsirco/berth/internal/agent/system"
	"github.com/wangsirco/berth/internal/agentproto"
)

const migFormat = 1

type migNeed struct {
	Path  string `json:"path"` // where on the target; "@docker", "@incus" for those tools' own storage
	Bytes int64  `json:"bytes"`
}

type migItem struct {
	ID      string           `json:"id"`
	Type    string           `json:"type"` // panel certs db stack project appproject site vm cron firewall folder
	Name    string           `json:"name"`
	Bytes   int64            `json:"bytes"` // what travels
	Need    []migNeed        `json:"need"`  // room it takes on the target
	Deps    []string         `json:"deps,omitempty"`
	Default bool             `json:"default"`
	Info    map[string]any   `json:"info,omitempty"`
	Streams []migrate.Stream `json:"streams,omitempty"` // data that comes as streams, restored by kind

	exp          migrate.ExportReq
	meta         func() (any, error)
	extra        map[string][]byte // streams the panel writes itself (宝塔 logins as SQL)
	extraStreams []migrate.Stream
}

type migManifest struct {
	Format  int       `json:"format"`
	Host    string    `json:"host"`
	Created int64     `json:"created"`
	Items   []migItem `json:"items"`
}

// restore order: what others depend on comes first
var migOrder = map[string]int{"panel": 0, "certs": 1, "db": 2, "btdb": 2, "stack": 3, "project": 4, "appproject": 5, "site": 6, "vm": 7, "dev": 7, "cron": 8, "btcron": 8, "firewall": 9, "folder": 10, "sftp": 11}

// Berth's own stacks: rebuilt on the target by Berth itself, never copied.
func migInfra(name string) bool {
	switch name {
	case caddy.Project, registryProject, guacdProject, tunnelProject:
		return true
	}
	return strings.HasPrefix(name, "berth-php") || strings.HasPrefix(name, "berth-dev-") || strings.HasPrefix(name, "dev-")
}

func (a *App) migFolders(host string) []string {
	out := []string{}
	json.Unmarshal([]byte(a.setting("migrate.folders."+host)), &out)
	return out
}

func siteRoot(s Site) string { return caddy.Root + "/sites/" + s.Primary() }

func hasFiles(s Site) bool { return s.Target.Type == "static" || s.Target.Type == "php" }

// migInventory lists what can move from host, with sizes.
func (a *App) migInventory(ctx context.Context, host string) (migManifest, error) {
	man := migManifest{Format: migFormat, Host: host, Created: time.Now().Unix(), Items: []migItem{}}
	sites, err := a.sites(host)
	if err != nil {
		return man, err
	}
	natives := a.nativeProjects(host)
	olds := a.appProjects(host)
	dbs, _ := a.dbInstances(host)
	folders := a.migFolders(host)

	req := migrate.ScanReq{Compose: true, VMs: true, Paths: []string{caddy.Root + "/data"}}
	for _, s := range sites {
		if hasFiles(s) {
			req.Paths = append(req.Paths, siteRoot(s))
		}
	}
	for _, p := range natives {
		base := path.Join(system.NativeRoot, p.Name)
		req.Paths = append(req.Paths, base+"/shared")
		if p.Current != "" {
			req.Paths = append(req.Paths, base+"/releases/"+p.Current)
		}
	}
	for _, p := range olds {
		if p.Dir != "" {
			req.Paths = append(req.Paths, p.Dir)
		}
	}
	req.Paths = append(req.Paths, folders...)
	natByFam := map[string]dbInstance{}
	for _, d := range dbs {
		if d.Native != "" {
			natByFam[dbFam(d.Engine)] = d
			req.DBs = append(req.DBs, d.Engine)
		}
	}
	req.Paths = append(req.Paths, a.migMorePaths(host)...)
	bt := a.btData(ctx, host)
	if bt != nil {
		req.Paths = append(req.Paths, btPaths(bt)...)
	}
	var sc migrate.ScanResult
	if err := a.call(ctx, host, "POST", "/v1/migrate/scan", req, &sc); err != nil {
		return man, err
	}
	if bt != nil {
		man.Items = append(man.Items, a.btItems(bt, sc)...)
	}
	man.Items = append(man.Items, a.migMoreItems(ctx, host, sc)...)

	ctrStack := map[string]string{}
	for _, cp := range sc.Compose {
		if migInfra(cp.Name) {
			continue
		}
		cp := cp
		it := migItem{ID: "stack:" + cp.Name, Type: "stack", Name: cp.Name, Default: true, Need: []migNeed{}, Info: map[string]any{"dir": cp.Dir, "running": cp.Running}}
		it.exp.Files = []string{cp.Dir}
		it.Bytes += cp.Bytes
		it.Need = append(it.Need, migNeed{cp.Dir, cp.Bytes})
		vols := map[string]map[string]string{}
		for _, v := range cp.Volumes {
			it.exp.Volumes = append(it.exp.Volumes, v.Name)
			vols[v.Name] = v.Labels
			it.Bytes += v.Bytes
			it.Need = append(it.Need, migNeed{"@docker", v.Bytes})
		}
		for _, b := range cp.Binds {
			it.exp.Files = append(it.exp.Files, b.Path)
			it.Bytes += b.Bytes
			it.Need = append(it.Need, migNeed{b.Path, b.Bytes})
		}
		imgs := []string{}
		for i, im := range cp.Images {
			it.Need = append(it.Need, migNeed{"@docker", im.Bytes}) // pulled or loaded, it takes room
			if im.Local {
				it.exp.Streams = append(it.exp.Streams, migrate.Stream{Name: "img" + strconv.Itoa(i), Kind: "image", Arg: im.Ref})
				it.Bytes += im.Bytes
			}
			imgs = append(imgs, im.Ref)
		}
		it.exp.Stop = cp.Stop
		it.Info["images"], it.Info["stop"] = imgs, cp.Stop
		for _, c := range cp.Containers {
			ctrStack[c] = it.ID
		}
		ctrs := cp.Containers
		it.meta = func() (any, error) {
			m := map[string]any{"dir": cp.Dir, "files": cp.Files, "volumes": vols, "binds": cp.Binds}
			m["apps"], _ = a.rowsOf("apps", "host=? AND name=?", host, cp.Name)
			m["owners"], _ = a.rowsOf("owners", "host=? AND type IN ('compose','project') AND id=?", host, cp.Name)
			var dbRows, lim []row
			for _, c := range ctrs {
				r, _ := a.rowsOf("databases", "host=? AND container=?", host, c)
				dbRows = append(dbRows, r...)
				l, _ := a.rowsOf("traffic_limits", "host=? AND container=?", host, c)
				lim = append(lim, l...)
			}
			m["databases"], m["limits"] = dbRows, lim
			return m, nil
		}
		man.Items = append(man.Items, it)
	}

	for _, p := range natives {
		p := p
		base := path.Join(system.NativeRoot, p.Name)
		it := migItem{ID: "project:" + p.Name, Type: "project", Name: p.Name, Default: true, Info: map[string]any{"kind": p.Kind, "version": p.Version, "release": p.Current}}
		it.exp.Files = []string{base}
		it.exp.Exclude = []string{base + "/releases"}
		if p.Current != "" {
			it.exp.Files = append(it.exp.Files, base+"/releases/"+p.Current)
		}
		n := sc.Paths[base+"/shared"] + sc.Paths[base+"/releases/"+p.Current]
		it.Bytes, it.Need = n, []migNeed{{base, n}}
		it.meta = func() (any, error) {
			q := p
			env := a.nativeEnv(q)
			q.EnvEnc, q.HookEnc, q.Port = "", "", 0
			return map[string]any{"project": q, "env": env}, nil
		}
		man.Items = append(man.Items, it)
	}
	for _, p := range olds {
		p := p
		it := migItem{ID: "appproject:" + p.Name, Type: "appproject", Name: p.Name, Default: true, Info: map[string]any{"kind": p.Kind, "dir": p.Dir}}
		if p.Dir != "" {
			it.exp.Files = []string{p.Dir}
			it.Bytes, it.Need = sc.Paths[p.Dir], []migNeed{{p.Dir, sc.Paths[p.Dir]}}
		}
		it.meta = func() (any, error) { return map[string]any{"spec": p}, nil }
		man.Items = append(man.Items, it)
	}

	for fam, d := range natByFam {
		d := d
		sizes := sc.DBs[d.Engine]
		it := migItem{ID: "db:" + fam, Type: "db", Name: d.Name, Default: true, Info: map[string]any{"engine": d.Engine, "native": d.Native, "databases": sizes}}
		var total int64
		names := sortedKeys64(sizes)
		if fam == "redis" {
			it.exp.Streams = []migrate.Stream{{Name: "redis", Kind: "redis"}}
		} else {
			it.exp.Streams = []migrate.Stream{{Name: "users", Kind: "dbusers", Arg: d.Engine}}
			for _, n := range names {
				it.exp.Streams = append(it.exp.Streams, migrate.Stream{Name: "db-" + n, Kind: "db", Arg: d.Engine + ":" + n})
			}
		}
		for _, n := range names {
			total += sizes[n]
		}
		it.Bytes = total
		// rebuilt indexes and the server's own files: a third on top, and the packages
		it.Need = []migNeed{{dbDataDir(fam), total + total/3}, {"/usr", 600 << 20}}
		it.meta = func() (any, error) {
			rows, err := a.rowsOf("databases", "id=?", d.ID)
			return map[string]any{"rows": rows, "engine": d.Engine, "native": d.Native}, err
		}
		man.Items = append(man.Items, it)
	}

	for _, s := range sites {
		s := s
		it := migItem{ID: "site:" + strconv.FormatInt(s.ID, 10), Type: "site", Name: s.Primary(), Default: true, Info: map[string]any{"target": s.Target.Type, "domains": s.Domains}}
		if hasFiles(s) {
			it.exp.Files = []string{siteRoot(s)}
			n := sc.Paths[siteRoot(s)]
			it.Bytes, it.Need = n, []migNeed{{siteRoot(s), n}}
		}
		switch s.Target.Type {
		case "container":
			if st, ok := ctrStack[s.Target.Container]; ok {
				it.Deps = append(it.Deps, st)
			}
		case "project":
			it.Deps = append(it.Deps, "project:"+s.Target.Project)
		}
		it.meta = func() (any, error) {
			m := map[string]any{"site": s}
			if s.TLS.Mode == "custom" && s.TLS.CertID > 0 {
				m["certs"], _ = a.rowsOf("certs", "id=?", s.TLS.CertID)
			}
			if s.Target.Type == "php" && !s.Target.Native {
				m["php"] = a.setting("php." + host + "." + s.Target.PHP)
			}
			return m, nil
		}
		man.Items = append(man.Items, it)
	}

	for _, v := range sc.VMs {
		v := v
		it := migItem{ID: "vm:" + v.Name, Type: "vm", Name: v.Name, Default: true, Bytes: v.Used, Need: []migNeed{{"@incus", v.Used}},
			Info: map[string]any{"type": v.Type, "state": v.State, "used": v.Used, "limit": v.Limit}}
		it.exp.Streams = []migrate.Stream{{Name: "vm", Kind: "incus", Arg: v.Name}}
		it.meta = func() (any, error) {
			r, err := a.rowsOf("vm_secrets", "host=? AND name=?", host, v.Name)
			return map[string]any{"secrets": r, "type": v.Type, "limit": v.Limit, "state": v.State}, err
		}
		man.Items = append(man.Items, it)
	}

	if n := len(a.jobs(host)); n > 0 {
		man.Items = append(man.Items, migItem{ID: "cron", Type: "cron", Name: "cron", Default: true, Info: map[string]any{"count": n},
			meta: func() (any, error) {
				r, err := a.rowsOf("cron_jobs", "host=?", host)
				return map[string]any{"rows": r}, err
			}})
	}
	if rs := a.fwRules(host); len(rs) > 0 {
		man.Items = append(man.Items, migItem{ID: "firewall", Type: "firewall", Name: "firewall", Default: true, Info: map[string]any{"count": len(rs)},
			meta: func() (any, error) { return map[string]any{"rules": rs}, nil }})
	}
	if n := sc.Paths[caddy.Root+"/data"]; n > 0 && len(sites) > 0 {
		it := migItem{ID: "certs", Type: "certs", Name: "certs", Default: true, Bytes: n, Need: []migNeed{{caddy.Root + "/data", n}}}
		it.exp.Files = []string{caddy.Root + "/data"}
		it.exp.Exclude = []string{caddy.Root + "/data/caddy/locks"}
		man.Items = append(man.Items, it)
	}
	for _, f := range folders {
		n := sc.Paths[f]
		it := migItem{ID: "folder:" + f, Type: "folder", Name: f, Default: true, Bytes: n, Need: []migNeed{{f, n}}}
		it.exp.Files = []string{f}
		man.Items = append(man.Items, it)
	}
	man.Items = append(man.Items, migItem{ID: "panel", Type: "panel", Name: "panel", Default: true,
		meta: func() (any, error) {
			m := map[string]any{}
			m["integrations"], _ = a.rowsOf("integrations", "1=1")
			m["backup_targets"], _ = a.rowsOf("backup_targets", "1=1")
			m["backup_jobs"], _ = a.rowsOf("backup_jobs", "host=?", host)
			m["ddns"], _ = a.rowsOf("ddns", "host=?", host)
			return m, nil
		}})
	for i := range man.Items {
		it := &man.Items[i]
		for _, st := range append(append([]migrate.Stream{}, it.exp.Streams...), it.extraStreams...) {
			st.Secret = "" // the manifest is shown and sent: passwords stay in the export request
			it.Streams = append(it.Streams, st)
		}
		if man.Items[i].Need == nil {
			man.Items[i].Need = []migNeed{}
		}
	}
	sort.SliceStable(man.Items, func(i, j int) bool { return migOrder[man.Items[i].Type] < migOrder[man.Items[j].Type] })
	return man, nil
}

func dbDataDir(fam string) string {
	switch fam {
	case "postgres":
		return "/var/lib/pgsql"
	case "redis":
		return "/var/lib/valkey"
	}
	return "/var/lib/mysql"
}

func sortedKeys64(m map[string]int64) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// migSelect keeps the chosen items and what they need; none chosen means the defaults.
func migSelect(man migManifest, ids []string) []migItem {
	want := map[string]bool{}
	for _, id := range ids {
		want[id] = true
	}
	if len(ids) == 0 {
		for _, it := range man.Items {
			if it.Default {
				want[it.ID] = true
			}
		}
	}
	byID := map[string]migItem{}
	for _, it := range man.Items {
		byID[it.ID] = it
	}
	var add func(string)
	add = func(id string) {
		it, ok := byID[id]
		if !ok {
			return
		}
		want[id] = true
		for _, d := range it.Deps {
			if !want[d] {
				add(d)
			}
		}
	}
	for id := range want {
		add(id)
	}
	out := []migItem{}
	for _, it := range man.Items {
		if want[it.ID] {
			out = append(out, it)
		}
	}
	return out
}

// ---- writing a pack ----

// migWritePack writes the chosen items of host as a tar: manifest.json, then for each item
// items/<n>/meta.json and its data entries, ending with items/<n>/x/status.
func (a *App) migWritePack(ctx context.Context, host string, ids []string, w io.Writer, log func(string, ...any)) error {
	man, err := a.migInventory(ctx, host)
	if err != nil {
		return err
	}
	man.Items = migSelect(man, ids)
	tw := tar.NewWriter(w)
	put := func(name string, v any) error {
		b, _ := json.Marshal(v)
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: int64(len(b)), Typeflag: tar.TypeReg, ModTime: time.Now()}); err != nil {
			return err
		}
		_, err := tw.Write(b)
		return err
	}
	if err := put("manifest.json", man); err != nil {
		return err
	}
	ag, err := a.agent(host)
	if err != nil {
		return err
	}
	for n, it := range man.Items {
		pre := "items/" + strconv.Itoa(n) + "/"
		log("%s %s", it.Type, it.Name)
		var meta any = map[string]any{}
		if it.meta != nil {
			if meta, err = it.meta(); err != nil {
				return err
			}
		}
		if err := put(pre+"meta.json", meta); err != nil {
			return err
		}
		for name, b := range it.extra {
			if err := tw.WriteHeader(&tar.Header{Name: pre + "s/" + name + "/000001", Mode: 0o600, Size: int64(len(b)), Typeflag: tar.TypeReg}); err != nil {
				return err
			}
			if _, err := tw.Write(b); err != nil {
				return err
			}
		}
		status := "ok"
		if len(it.exp.Files)+len(it.exp.Volumes)+len(it.exp.Streams) > 0 {
			status = a.migCopyExport(ctx, ag, it.exp, tw, pre)
		}
		if status == "" { // the agent's own x/status came through
			continue
		}
		if err := put(pre+"x/status", status); err != nil {
			return err
		}
	}
	return tw.Close()
}

type streamer interface {
	Stream(ctx context.Context, method, path string, in any) (io.ReadCloser, error)
}

// migCopyExport copies the agent's export of one item into tw under pre. It returns "" when the
// agent's own status entry was copied, otherwise the failure to record.
func (a *App) migCopyExport(ctx context.Context, ag streamer, req migrate.ExportReq, tw *tar.Writer, pre string) string {
	rc, err := ag.Stream(ctx, "POST", "/v1/migrate/export", req)
	if err != nil {
		return err.Error()
	}
	defer rc.Close()
	tr := tar.NewReader(rc)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return "the source stopped before the end of this item"
		}
		if err != nil {
			return err.Error()
		}
		last := h.Name == "x/status"
		h.Name = pre + h.Name
		if err := tw.WriteHeader(h); err != nil {
			return err.Error()
		}
		if _, err := io.Copy(tw, tr); err != nil {
			return err.Error()
		}
		if last {
			return ""
		}
	}
}

// ---- checking the target ----

type migCheck struct {
	ID     string         `json:"id"`
	Status string         `json:"status"` // ok | exists (skipped) | missing (target lacks a tool: skipped) | merge | install
	Note   string         `json:"note,omitempty"`
	Params map[string]any `json:"params,omitempty"`
}

type migFS struct {
	Mount string   `json:"mount"`
	Free  int64    `json:"free"`
	Total int64    `json:"total"`
	Need  int64    `json:"need"`
	Room  int64    `json:"room"` // need plus what stays free
	OK    bool     `json:"ok"`
	Items []string `json:"items"`
}

type migPreflight struct {
	Manifest    migManifest `json:"manifest"`
	Checks      []migCheck  `json:"checks"`
	Filesystems []migFS     `json:"filesystems"`
	Fits        bool        `json:"fits"`
}

const migReserve = 1 << 30

// migRoom: the room an amount of data needs, with headroom.
func migRoom(need int64) int64 { return need + need/20 }

// migCheckTarget says, for the chosen items, what already exists on host and whether each
// filesystem they land on has room.
func (a *App) migCheckTarget(ctx context.Context, host string, man migManifest, items []migItem) (migPreflight, error) {
	pf := migPreflight{Manifest: man, Checks: []migCheck{}, Filesystems: []migFS{}}
	var tsc migrate.ScanResult
	if err := a.call(ctx, host, "POST", "/v1/migrate/scan", migrate.ScanReq{Compose: true, VMs: true}, &tsc); err != nil {
		return pf, err
	}
	sites, _ := a.sites(host)
	domains := map[string]bool{}
	for _, s := range sites {
		for _, d := range s.Domains {
			domains[strings.ToLower(d)] = true
		}
	}
	stacks := map[string]bool{}
	for _, c := range tsc.Compose {
		stacks[c.Name] = true
	}
	vmsHere := map[string]bool{}
	for _, v := range tsc.VMs {
		vmsHere[v.Name] = true
	}
	natFam := map[string]string{}
	dbs, _ := a.dbInstances(host)
	for _, d := range dbs {
		if d.Native != "" {
			natFam[dbFam(d.Engine)] = d.Engine
		}
	}
	edge, _ := a.edgeStatus(ctx, host)
	var caps agentproto.Caps
	a.call(ctx, host, "GET", "/v1/caps?fresh=1", nil, &caps)
	noCompose := !caps.Docker || !caps.Compose
	vst, verr := a.vmStatus(ctx, host)

	extra := []migNeed{}
	for _, it := range items {
		c := migCheck{ID: it.ID, Status: "ok"}
		switch it.Type {
		case "site":
			for _, d := range anyStrings(it.Info["domains"]) {
				if domains[strings.ToLower(d)] {
					c.Status, c.Note = "exists", "migrate.chk.site_exists"
				}
			}
			if c.Status == "ok" && !edge.Installed {
				c.Status, c.Note = "install", "migrate.chk.edge"
				if noCompose {
					c.Status, c.Note = "missing", "migrate.chk.no_compose"
				}
			}
		case "stack":
			if stacks[it.Name] {
				c.Status, c.Note = "exists", "migrate.chk.stack_exists"
			} else if noCompose {
				c.Status, c.Note = "missing", "migrate.chk.no_compose"
			}
		case "project":
			if _, ok := a.nativeProject(host, it.Name); ok {
				c.Status, c.Note = "exists", "migrate.chk.project_exists"
			}
		case "appproject":
			for _, p := range a.appProjects(host) {
				if p.Name == it.Name {
					c.Status, c.Note = "exists", "migrate.chk.project_exists"
				}
			}
		case "db":
			fam := strings.TrimPrefix(it.ID, "db:")
			if e, ok := natFam[fam]; ok {
				c.Status, c.Note, c.Params = "merge", "migrate.chk.db_merge", map[string]any{"engine": e}
			} else {
				c.Status, c.Note = "install", "migrate.chk.db_install"
			}
		case "vm":
			if vmsHere[it.Name] {
				c.Status, c.Note = "exists", "migrate.chk.vm_exists"
			} else if verr != nil || !vst.Status.Installed {
				c.Status, c.Note = "install", "migrate.chk.incus"
				extra = append(extra, migNeed{"/usr", 300 << 20})
			}
			if lim := int64num(it.Info["limit"]); lim > 0 {
				c.Params = map[string]any{"limit": lim, "used": int64num(it.Info["used"])}
			}
		case "btdb":
			if e, ok := natFam["mysql"]; ok {
				c.Status, c.Note, c.Params = "merge", "migrate.chk.db_merge", map[string]any{"engine": e}
			} else {
				c.Status, c.Note = "install", "migrate.chk.db_install"
			}
		case "btcron":
			c.Status, c.Note = "merge", "migrate.chk.merge"
		case "folder":
			c.Status, c.Note = "merge", "migrate.chk.folder_merge"
		case "cron", "firewall", "panel":
			c.Status, c.Note = "merge", "migrate.chk.merge"
		default:
			a.migMoreCheck(ctx, host, it, &c)
		}
		pf.Checks = append(pf.Checks, c)
	}

	// room, per filesystem the data would land on
	resolve := func(p string) string {
		switch p {
		case "@docker":
			return tsc.DockerRoot
		case "@incus":
			return tsc.IncusPool
		}
		return p
	}
	var paths []string
	type need struct {
		path  string
		bytes int64
		item  string
	}
	var needs []need
	skip := map[string]bool{}
	for _, c := range pf.Checks {
		if c.Status == "exists" || c.Status == "missing" {
			skip[c.ID] = true
		}
	}
	for _, it := range items {
		if skip[it.ID] {
			continue
		}
		for _, n := range it.Need {
			p := resolve(n.Path)
			paths = append(paths, p)
			needs = append(needs, need{p, n.Bytes, it.ID})
		}
	}
	for _, n := range extra {
		paths = append(paths, n.Path)
		needs = append(needs, need{n.Path, n.Bytes, ""})
	}
	var sp migrate.SpaceResult
	if err := a.call(ctx, host, "POST", "/v1/migrate/space", map[string]any{"paths": paths}, &sp); err != nil {
		return pf, err
	}
	byDev := map[string]*migFS{}
	for _, n := range needs {
		dev, ok := sp.Paths[n.path]
		if !ok {
			continue
		}
		fs, ok := byDev[dev]
		if !ok {
			f := sp.Filesystems[dev]
			fs = &migFS{Mount: f.Mount, Free: f.Free, Total: f.Total, Items: []string{}}
			byDev[dev] = fs
		}
		fs.Need += n.bytes
		if n.item != "" && !containsStr(fs.Items, n.item) && n.bytes > 0 {
			fs.Items = append(fs.Items, n.item)
		}
	}
	pf.Fits = true
	for _, fs := range byDev {
		fs.Room = migRoom(fs.Need) + migReserve
		fs.OK = fs.Room <= fs.Free
		if !fs.OK {
			pf.Fits = false
		}
		pf.Filesystems = append(pf.Filesystems, *fs)
	}
	sort.Slice(pf.Filesystems, func(i, j int) bool { return pf.Filesystems[i].Mount < pf.Filesystems[j].Mount })
	return pf, nil
}

func anyStrings(v any) []string {
	out := []string{}
	switch l := v.(type) {
	case []string:
		return l
	case []any:
		for _, x := range l {
			if s, ok := x.(string); ok {
				out = append(out, s)
			}
		}
	}
	return out
}

func int64num(v any) int64 {
	switch n := v.(type) {
	case int64:
		return n
	case int:
		return int64(n)
	case float64:
		return int64(n)
	}
	return 0
}

func containsStr(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

func fmtBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.0f MB", float64(n)/(1<<20))
	}
	return fmt.Sprintf("%d KB", n>>10)
}
