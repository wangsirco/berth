package panel

// Moving off 宝塔 (R6): a 宝塔 server joined as a host (its agent sits beside 宝塔 and changes
// nothing there) lists its websites, MySQL databases and scheduled jobs as migration items.
// Site files go into Berth's site folders, databases into a native MariaDB/MySQL with the same
// logins and passwords (so wp-config and .env keep working), jobs become Berth jobs.

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strconv"

	"github.com/wangsirco/berth/internal/agent/files"
	"github.com/wangsirco/berth/internal/agent/migrate"
	"github.com/wangsirco/berth/internal/agent/system"
	"github.com/wangsirco/berth/internal/panel/caddycfg"
	"github.com/wangsirco/berth/internal/panel/importer"
)

func (a *App) hostText(ctx context.Context, host, path string) string {
	var r files.ReadResult
	if a.call(ctx, host, "POST", "/v1/files/read", map[string]any{"root": "/", "path": path, "max": 1 << 20}, &r) != nil {
		return ""
	}
	return string(r.Content)
}

// btData reads a 宝塔 server; nil when the host has no 宝塔.
func (a *App) btData(ctx context.Context, host string) *importer.BTData {
	var st files.Entry
	if a.call(ctx, host, "POST", "/v1/files/stat", map[string]any{"root": "/", "path": importer.BTDB}, &st) != nil {
		return nil
	}
	ag, err := a.agent(host)
	if err != nil {
		return nil
	}
	rc, err := ag.Stream(ctx, "POST", "/v1/files/download", map[string]any{"root": "/", "path": importer.BTDB})
	if err != nil {
		return nil
	}
	defer rc.Close()
	f, err := os.CreateTemp("", "berth-bt-*.db")
	if err != nil {
		return nil
	}
	defer os.Remove(f.Name())
	_, err = io.Copy(f, io.LimitReader(rc, 512<<20))
	f.Close()
	if err != nil {
		return nil
	}
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(f.Name())+"?mode=ro")
	if err != nil {
		return nil
	}
	defer db.Close()
	d, err := importer.BT(importer.BTSource{DB: db,
		Read: func(p string) string { return a.hostText(ctx, host, p) },
		List: func(dir string) []string {
			var es []files.Entry
			a.call(ctx, host, "POST", "/v1/files/list", map[string]any{"root": "/", "path": dir}, &es)
			out := []string{}
			for _, e := range es {
				if !e.Dir {
					out = append(out, e.Name)
				}
			}
			return out
		}})
	if err != nil {
		return nil
	}
	return &d
}

// btPHP picks where a 宝塔 PHP version runs under Berth: natively for 8.2 and up (like 宝塔,
// PHP-FPM on the machine), otherwise Berth's PHP container of the nearest version.
func btPHP(v string) (tag string, native bool, note string) {
	if contains(system.PHPNativeVersions, v) {
		return phpTag(v), true, ""
	}
	if contains(PHPVersions, v) {
		return phpTag(v), false, ""
	}
	if v == "8.0" {
		return "81", false, "bt.php_up:8.1"
	}
	f, _ := strconv.ParseFloat(v, 64)
	if f > 8.4 {
		return "84", true, "bt.php_up:8.4"
	}
	return "74", false, "bt.php_up:7.4"
}

// btItems adds the 宝塔 items to an inventory; paths (sizes) come from the same scan.
func (a *App) btItems(bt *importer.BTData, sc migrate.ScanResult) []migItem {
	var out []migItem
	for _, s := range bt.Sites {
		s := s
		site := Site{Domains: s.Domains, Enabled: true, TLS: caddycfg.TLS{Mode: "auto"}}
		notes := append([]string{}, s.Notes...)
		dest := caddyRootSites + "/" + s.Name
		m := map[string]any{}
		it := migItem{ID: "bt-site:" + strconv.Itoa(s.ID), Type: "site", Name: s.Name, Default: true}
		switch s.Kind {
		case "php", "static":
			if s.Kind == "php" {
				tag, native, note := btPHP(s.PHP)
				site.Target = caddycfg.Target{Type: "php", PHP: tag, Native: native, RootSub: s.RootSub}
				if note != "" {
					notes = append(notes, note)
				}
			} else {
				site.Target = caddycfg.Target{Type: "static"}
			}
			it.exp.Files, it.exp.Exclude = []string{s.Path}, []string{s.Path + "/.user.ini"} // 宝塔's open_basedir file names its own paths
			m["map"], m["allow"] = map[string]string{s.Path: dest}, []string{dest}
			it.Bytes, it.Need = sc.Paths[s.Path], []migNeed{{dest, sc.Paths[s.Path]}}
		case "project":
			site.Target = caddycfg.Target{Type: "address", Upstreams: []string{s.Upstream}}
			it.exp.Files = []string{s.Path}
			m["allow"] = []string{s.Path}
			it.Bytes, it.Need = sc.Paths[s.Path], []migNeed{{s.Path, sc.Paths[s.Path]}}
			if s.Upstream == "" {
				it.Default = false
				notes = append(notes, "bt.no_port")
			}
		default: // proxy
			if s.Upstream == "" {
				it.Default = false
				site.Target = caddycfg.Target{Type: "address"}
			} else {
				site.Target = caddycfg.Target{Type: "address", Upstreams: []string{s.Upstream}}
			}
		}
		m["site"] = site
		it.Info = map[string]any{"bt": true, "kind": s.Kind, "php": s.PHP, "domains": s.Domains, "notes": notes, "target": site.Target.Type}
		it.meta = func() (any, error) { return m, nil }
		out = append(out, it)
	}
	if len(bt.Databases) > 0 {
		it := migItem{ID: "bt-db", Type: "btdb", Name: "MySQL", Default: true, Info: map[string]any{"bt": true}}
		sizes := map[string]int64{}
		var total int64
		for _, d := range bt.Databases {
			n := sc.Paths["/www/server/data/"+d.Name]
			sizes[d.Name] = n
			total += n
			it.exp.Streams = append(it.exp.Streams, migrate.Stream{Name: "db-" + d.Name, Kind: "btdb", Arg: d.Name, Secret: bt.RootPass})
		}
		it.Info["databases"] = sizes
		it.Bytes = total
		it.Need = []migNeed{{"/var/lib/mysql", total + total/3}, {"/usr", 600 << 20}}
		it.extra = map[string][]byte{"users": []byte(importer.BTUsersSQL(bt.Databases))}
		it.extraStreams = []migrate.Stream{{Name: "users", Kind: "sql"}}
		out = append(out, it)
	}
	jobs := []map[string]any{}
	skipped := 0
	for _, c := range bt.Crons {
		if !c.OK {
			skipped++
			continue
		}
		jobs = append(jobs, map[string]any{"name": c.Name, "cron": c.Spec, "action": map[string]any{"type": "script", "script": c.Script}})
	}
	if len(bt.Crons) > 0 {
		out = append(out, migItem{ID: "bt-cron", Type: "btcron", Name: "cron", Default: true, Info: map[string]any{"bt": true, "count": len(jobs), "skipped": skipped},
			meta: func() (any, error) { return map[string]any{"jobs": jobs}, nil }})
	}
	return out
}

const caddyRootSites = "/srv/berth/caddy/sites"

// btPaths: what to size on a 宝塔 server.
func btPaths(bt *importer.BTData) []string {
	var out []string
	for _, s := range bt.Sites {
		if s.Path != "" {
			out = append(out, s.Path)
		}
	}
	for _, d := range bt.Databases {
		out = append(out, "/www/server/data/"+d.Name)
	}
	return out
}

// restoring the 宝塔 kinds
func (a *App) migPrepareBT(st *migState, cur *migItemRun, meta []byte) error {
	env := st.env
	host := env.Host
	switch cur.it.Type {
	case "btdb":
		var have *system.DBNative
		find := func() {
			for _, n := range a.dbNatives(env) {
				if dbFam(n.Engine) == "mysql" {
					n := n
					have = &n
				}
			}
		}
		find()
		if have == nil {
			env.Log("  installing MariaDB for the 宝塔 databases")
			if _, err := a.subApply(env, "db.native.install", map[string]any{"engine": "mariadb"}); err != nil {
				return err
			}
			find()
			if have == nil {
				return errText2("MariaDB did not come up")
			}
		}
		var tsc migrate.ScanResult
		a.call(env.Ctx, host, "POST", "/v1/migrate/scan", migrate.ScanReq{DBs: []string{have.Engine}}, &tsc)
		for name, s := range cur.req.Streams {
			switch s.Kind {
			case "btdb":
				if _, exists := tsc.DBs[have.Engine][s.Arg]; exists {
					delete(cur.req.Streams, name)
					st.warn(cur.it.ID, "migrate.warn.db_exists:"+s.Arg)
					continue
				}
				s.Kind, s.Arg = "db", have.Engine+":"+s.Arg
			case "sql":
				s.Arg = have.Engine
			}
			cur.req.Streams[name] = s
		}
	case "btcron":
		var m struct{ Jobs []json.RawMessage }
		json.Unmarshal(meta, &m)
		cur.after = func(migrate.ImportResult) error {
			names := map[string]bool{}
			for _, j := range a.jobs(host) {
				names[j.Name] = true
			}
			for _, j := range m.Jobs {
				var p struct{ Name string }
				json.Unmarshal(j, &p)
				if names[p.Name] {
					continue
				}
				if _, err := a.subApply(env, "cron.set", j); err != nil {
					st.warn(cur.it.ID, "migrate.warn.cron:"+p.Name+": "+errText(err))
				}
			}
			return nil
		}
	}
	return nil
}

type errText2 string

func (e errText2) Error() string { return string(e) }
