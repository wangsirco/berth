package panel

// Server migration (R6), the receiving side: read a pack as it arrives and put each item in
// place — data straight into its final folder through an agent session, then the panel's
// records, then start it.

import (
	"archive/tar"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"strconv"
	"strings"

	"github.com/wangsirco/berth/internal/agent/caddy"
	"github.com/wangsirco/berth/internal/agent/migrate"
	"github.com/wangsirco/berth/internal/agent/system"
	"github.com/wangsirco/berth/internal/agentproto"
	"github.com/wangsirco/berth/internal/change"
	"github.com/wangsirco/berth/internal/errs"
)

type migNote struct {
	ID     string `json:"id"`
	Reason string `json:"reason"`
}

type migReport struct {
	Done     []string  `json:"done"`
	Skipped  []migNote `json:"skipped"`
	Failed   []migNote `json:"failed"`
	Warnings []migNote `json:"warnings"`
	Bytes    int64     `json:"bytes"`
}

// subApply runs another change kind's plan and apply inside this change (heavy kinds cannot be
// queued from within a task: the queue would wait for this one).
func (a *App) subApply(env change.Env, kind string, params any) (any, error) {
	k, ok := change.Get(kind)
	if !ok {
		return nil, fmt.Errorf("no change kind %s", kind)
	}
	raw, _ := json.Marshal(params)
	if _, err := k.Plan(env, raw); err != nil {
		return nil, err
	}
	env.Log("· %s", kind)
	v, _, err := k.Apply(env, raw)
	return v, err
}

// agentSink sends what is written to an agent import session in chunks.
type agentSink struct {
	env change.Env
	c   *agentproto.Client
	id  string
	off int64
	buf []byte
}

const sinkChunk = 4 << 20

func (s *agentSink) Write(b []byte) (int, error) {
	s.buf = append(s.buf, b...)
	if len(s.buf) >= sinkChunk {
		if err := s.flush(); err != nil {
			return 0, err
		}
	}
	return len(b), nil
}

func (s *agentSink) flush() error {
	if len(s.buf) == 0 {
		return nil
	}
	var err error
	for try := 0; try < 3; try++ {
		err = s.c.DoRaw(s.env.Ctx, "POST", "/v1/migrate/import/"+s.id+"/chunk?off="+strconv.FormatInt(s.off, 10), s.buf)
		if err == nil {
			break
		}
		var e *errs.E
		if errors.As(err, &e) && e.Status != 502 { // the agent answered: retrying won't help
			break
		}
	}
	if err != nil {
		return err
	}
	s.off += int64(len(s.buf))
	s.buf = s.buf[:0]
	return nil
}

// migItemRun is one item being restored.
type migItemRun struct {
	it      migItem
	skip    string // why it is not restored (nothing is written)
	req     migrate.ImportReq
	session string
	sink    *agentSink
	tw      *tar.Writer
	after   func(res migrate.ImportResult) error
	failed  string
	streams map[string]bool
}

type migState struct {
	env      change.Env
	report   migReport
	checks   map[string]migCheck
	needEdge bool
	scan     migrate.ScanResult // the target's, for docker and incus storage paths
}

func (st *migState) warn(id, reason string) {
	st.report.Warnings = append(st.report.Warnings, migNote{id, reason})
}

// migRestore reads a pack from r and restores the items in want (all when want is nil).
func (a *App) migRestore(env change.Env, r io.Reader, want map[string]bool) (migReport, error) {
	st := &migState{env: env, report: migReport{Done: []string{}, Skipped: []migNote{}, Failed: []migNote{}, Warnings: []migNote{}}, checks: map[string]migCheck{}}
	tr := tar.NewReader(r)
	h, err := tr.Next()
	if err != nil || h.Name != "manifest.json" {
		return st.report, errs.New(400, "migrate.bad_pack")
	}
	var man migManifest
	if err := json.NewDecoder(io.LimitReader(tr, 32<<20)).Decode(&man); err != nil || man.Format != migFormat {
		return st.report, errs.New(400, "migrate.bad_pack")
	}
	chosen := []migItem{}
	for _, it := range man.Items {
		if want == nil || want[it.ID] {
			chosen = append(chosen, it)
		}
	}
	pf, err := a.migCheckTarget(env.Ctx, env.Host, man, chosen)
	if err != nil {
		return st.report, err
	}
	for _, c := range pf.Checks {
		st.checks[c.ID] = c
	}
	a.call(env.Ctx, env.Host, "POST", "/v1/migrate/scan", migrate.ScanReq{}, &st.scan)
	env.Log("pack from %s: %d item(s), restoring %d", man.Host, len(man.Items), len(chosen))

	var cur *migItemRun
	curN := -1
	finish := func() {
		if cur == nil {
			return
		}
		a.migFinishItem(st, cur)
		cur = nil
	}
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			finish()
			return st.report, errs.New(502, "migrate.stream_cut", "detail", err.Error())
		}
		rest, ok := strings.CutPrefix(h.Name, "items/")
		if !ok {
			continue
		}
		ns, rest, _ := strings.Cut(rest, "/")
		n, err := strconv.Atoi(ns)
		if err != nil || n < 0 || n >= len(man.Items) {
			continue
		}
		if n != curN {
			finish()
			curN = n
			cur = &migItemRun{it: man.Items[n]}
			if want != nil && !want[cur.it.ID] {
				cur.skip = "-" // not chosen: passed over silently
			}
		}
		switch {
		case rest == "meta.json":
			meta, _ := io.ReadAll(io.LimitReader(tr, 64<<20))
			if cur.skip == "" {
				a.migStartItem(st, cur, meta)
			}
		case rest == "x/status":
			b, _ := io.ReadAll(io.LimitReader(tr, 64<<10))
			if s := strings.TrimSpace(string(b)); s != "ok" && s != `"ok"` && cur.failed == "" && cur.skip == "" {
				cur.failed = "migrate.fail.source:" + s
			}
		default:
			if cur.session == "" || cur.failed != "" {
				continue // skipped or failed: drain
			}
			if strings.HasPrefix(rest, "s/") {
				sn, _, _ := strings.Cut(strings.TrimPrefix(rest, "s/"), "/")
				if !cur.streams[sn] {
					continue
				}
			}
			h.Name = rest
			if err := cur.tw.WriteHeader(h); err == nil {
				_, err = io.Copy(cur.tw, tr)
				if err != nil {
					cur.failed = errText(err)
				}
			} else {
				cur.failed = errText(err)
			}
		}
	}
	finish()
	if st.needEdge {
		env.Log("loading the sites into the edge")
		if err := a.applyEdge(env); err != nil {
			st.warn("site", "migrate.warn.edge:"+errText(err))
		}
	}
	return st.report, nil
}

func errText(err error) string {
	var e *errs.E
	if errors.As(err, &e) {
		b, _ := json.Marshal(e)
		return string(b)
	}
	return err.Error()
}

// migRoomFor checks, right before an item, that its filesystems still have room.
func (a *App) migRoomFor(st *migState, it migItem) error {
	paths := []string{}
	need := map[string]int64{}
	for _, n := range it.Need {
		p := n.Path
		switch p {
		case "@docker":
			p = st.scan.DockerRoot
		case "@incus":
			p = st.scan.IncusPool
		}
		if p == "" {
			continue
		}
		paths = append(paths, p)
		need[p] += n.Bytes
	}
	if len(paths) == 0 {
		return nil
	}
	var sp migrate.SpaceResult
	if err := a.call(st.env.Ctx, st.env.Host, "POST", "/v1/migrate/space", map[string]any{"paths": paths}, &sp); err != nil {
		return err
	}
	byDev := map[string]int64{}
	for p, b := range need {
		byDev[sp.Paths[p]] += b
	}
	for dev, b := range byDev {
		fs := sp.Filesystems[dev]
		if migRoom(b)+migReserve > fs.Free {
			return errs.New(507, "migrate.no_room", "mount", fs.Mount, "need", fmtBytes(migRoom(b)+migReserve), "free", fmtBytes(fs.Free))
		}
	}
	return nil
}

func (a *App) migStartItem(st *migState, cur *migItemRun, meta []byte) {
	env := st.env
	it := cur.it
	if c, ok := st.checks[it.ID]; ok && (c.Status == "exists" || c.Status == "missing") {
		cur.skip = c.Note
		return
	}
	if err := a.migRoomFor(st, it); err != nil {
		cur.skip = errText(err)
		return
	}
	env.Log("— %s %s (%s)", it.Type, it.Name, fmtBytes(it.Bytes))
	env.Log("@item %s start", it.ID) // read by the migration page: each item's own progress
	if err := a.migPrepare(st, cur, meta); err != nil {
		cur.failed = errText(err)
		return
	}
	if len(cur.req.Allow) == 0 && len(cur.req.Streams) == 0 && len(cur.req.Volumes) == 0 && len(it.Streams) == 0 {
		return // records only
	}
	cur.req.Reserve = migReserve
	var res struct{ ID string }
	if err := a.call(env.Ctx, env.Host, "POST", "/v1/migrate/import/begin", cur.req, &res); err != nil {
		cur.failed = errText(err)
		return
	}
	ag, err := a.agent(env.Host)
	if err != nil {
		cur.failed = errText(err)
		return
	}
	cur.session = res.ID
	cur.sink = &agentSink{env: env, c: ag, id: res.ID}
	cur.tw = tar.NewWriter(cur.sink)
	cur.streams = map[string]bool{}
	for n := range cur.req.Streams {
		cur.streams[n] = true
	}
}

func (a *App) migFinishItem(st *migState, cur *migItemRun) {
	env := st.env
	id := cur.it.ID
	var res migrate.ImportResult
	if cur.session != "" {
		if cur.failed == "" {
			if err := cur.tw.Close(); err != nil {
				cur.failed = errText(err)
			} else if err := cur.sink.flush(); err != nil {
				cur.failed = errText(err)
			}
		}
		if cur.failed != "" {
			a.call(env.Ctx, env.Host, "POST", "/v1/migrate/import/"+cur.session+"/abort", nil, nil)
		} else if err := a.call(env.Ctx, env.Host, "POST", "/v1/migrate/import/"+cur.session+"/finish", nil, &res); err != nil {
			cur.failed = errText(err)
		}
		for _, w := range res.Warnings {
			st.warn(id, w)
		}
	}
	switch {
	case cur.skip == "-":
		return
	case cur.skip != "":
		env.Log("  skipped: %s", cur.skip)
		env.Log("@item %s skipped", id)
		st.report.Skipped = append(st.report.Skipped, migNote{id, cur.skip})
		return
	case cur.failed != "":
		env.Log("  FAILED: %s", cur.failed)
		env.Log("@item %s failed", id)
		st.report.Failed = append(st.report.Failed, migNote{id, cur.failed})
		return
	}
	if cur.after != nil {
		if err := cur.after(res); err != nil {
			env.Log("  FAILED: %s", errText(err))
			env.Log("@item %s failed", id)
			st.report.Failed = append(st.report.Failed, migNote{id, errText(err)})
			return
		}
	}
	st.report.Bytes += res.Bytes
	st.report.Done = append(st.report.Done, id)
	env.Log("  done (%d files, %s)", res.Files, fmtBytes(res.Bytes))
	env.Log("@item %s done %d", id, res.Bytes)
}

// migPrepare sets up one item: what its data may write, what must exist first, and what to do
// once the data is in.
func (a *App) migPrepare(st *migState, cur *migItemRun, meta []byte) error {
	env := st.env
	host := env.Host
	it := cur.it
	cur.req.Streams = map[string]migrate.Stream{}
	for _, s := range it.Streams {
		cur.req.Streams[s.Name] = s
	}
	switch it.Type {
	case "site":
		var m struct {
			Site  Site
			Certs []row
			PHP   string
			Map   map[string]string // 宝塔: its folder → Berth's site folder
			Allow []string
		}
		if err := json.Unmarshal(meta, &m); err != nil {
			return err
		}
		s := m.Site
		edge, _ := a.edgeStatus(env.Ctx, host)
		if !edge.Installed {
			if _, err := a.subApply(env, "edge.install", map[string]any{}); err != nil {
				return err
			}
			edge, _ = a.edgeStatus(env.Ctx, host)
		}
		if s.Options.WAF != nil && !edge.WAF {
			s.Options.WAF = nil
			st.warn(it.ID, "migrate.warn.waf_off")
		}
		if s.Target.Type == "php" {
			v := phpDotted(s.Target.PHP)
			if s.Target.Native {
				if _, err := a.subApply(env, "php.native.install", map[string]any{"version": v}); err != nil && !strings.Contains(err.Error(), "installed") {
					return err
				}
			} else if _, err := a.findProject(env.Ctx, host, phpName(v)); err != nil {
				var rt struct {
					Ext []string          `json:"ext"`
					Ini map[string]string `json:"ini"`
				}
				json.Unmarshal([]byte(m.PHP), &rt)
				p := map[string]any{"version": v}
				if len(rt.Ext) > 0 {
					p["extensions"] = rt.Ext
				}
				if len(rt.Ini) > 0 {
					p["ini"] = rt.Ini
				}
				if _, err := a.subApply(env, "php.set", p); err != nil {
					return err
				}
			}
		}
		if len(m.Allow) > 0 {
			cur.req.Allow, cur.req.Map = m.Allow, m.Map
		} else if hasFiles(s) {
			cur.req.Allow = []string{siteRoot(s)}
		}
		cur.after = func(migrate.ImportResult) error {
			if s.TLS.Mode == "custom" && len(m.Certs) > 0 {
				id, err := a.insertRow("certs", m.Certs[0], nil, "id")
				if err != nil {
					return err
				}
				s.TLS.CertID = id
			}
			s.ID = 0
			if err := a.saveSite(host, &s, env.ActorName); err != nil {
				return err
			}
			a.relateSite(host, s)
			if isNativePHP(&s) {
				if err := a.syncPHPSite(env, &s, nil); err != nil {
					return err
				}
			}
			st.needEdge = true
			return nil
		}

	case "stack":
		var m struct {
			Dir       string
			Files     []string
			Volumes   map[string]map[string]string
			Binds     []migrate.PathSize
			Apps      []row
			Owners    []row
			Databases []row
			Limits    []row
		}
		if err := json.Unmarshal(meta, &m); err != nil {
			return err
		}
		cur.req.Allow = []string{m.Dir}
		for _, b := range m.Binds {
			cur.req.Allow = append(cur.req.Allow, b.Path)
		}
		cur.req.Volumes = m.Volumes
		cur.after = func(migrate.ImportResult) error {
			env.Log("  starting %s (images that are not in the pack are pulled now)", it.Name)
			if err := a.streamAgent(env, host, "/v1/migrate/compose-up", map[string]any{"name": it.Name, "dir": m.Dir, "files": m.Files}, nil); err != nil {
				return err
			}
			for _, r := range m.Apps {
				a.insertRow("apps", r, map[string]any{"host": host})
			}
			for _, r := range m.Owners {
				a.insertRow("owners", r, map[string]any{"host": host})
			}
			for _, r := range m.Databases {
				if _, err := a.insertRow("databases", r, map[string]any{"host": host}, "id"); err != nil {
					st.warn(it.ID, "migrate.warn.db_record:"+fmt.Sprint(r["name"]))
				}
			}
			for _, r := range m.Limits {
				a.insertRow("traffic_limits", r, map[string]any{"host": host})
			}
			a.relate(host, change.Object{Type: "host", ID: host}, projObj(it.Name, host), "manages", "module")
			return nil
		}

	case "project":
		var m struct {
			Project nativeProject
			Env     map[string]string
		}
		if err := json.Unmarshal(meta, &m); err != nil {
			return err
		}
		p := m.Project
		cur.req.Allow = []string{path.Join(system.NativeRoot, p.Name)}
		cur.req.Users = []string{p.Name}
		cur.after = func(migrate.ImportResult) error {
			enc, err := a.sealEnv(m.Env)
			if err != nil {
				return err
			}
			p.EnvEnc, p.Port = enc, 0
			if err := a.saveNative(host, p, env.ActorName); err != nil {
				return err
			}
			if p.Current == "" {
				return nil
			}
			_, err = a.subApply(env, "native.activate", map[string]string{"name": p.Name, "release": p.Current})
			return err
		}

	case "appproject":
		var m struct{ Spec projectSpec }
		if err := json.Unmarshal(meta, &m); err != nil {
			return err
		}
		if m.Spec.Dir != "" {
			cur.req.Allow = []string{m.Spec.Dir}
		}
		cur.after = func(migrate.ImportResult) error {
			_, err := a.subApply(env, "project.set", m.Spec)
			return err
		}

	case "db":
		var m struct {
			Rows   []row
			Engine string
			Native string
		}
		if err := json.Unmarshal(meta, &m); err != nil || len(m.Rows) == 0 {
			return errs.New(400, "migrate.bad_pack")
		}
		fam := dbFam(m.Engine)
		var have *system.DBNative
		for _, n := range a.dbNatives(env) {
			if dbFam(n.Engine) == fam {
				n := n
				have = &n
			}
		}
		if have == nil {
			ver := strings.TrimSpace(strings.TrimPrefix(m.Native, m.Engine))
			if !contains(dbNativeVersions[m.Engine], ver) {
				st.warn(it.ID, "migrate.warn.db_version:"+ver)
				ver = dbNativeVersions[m.Engine][0]
			}
			pass := string(plainOf(m.Rows[0]["admin_pass_enc"]))
			if len(pass) < 16 {
				pass = randomPassword(28)
			}
			env.Log("  installing %s %s", m.Engine, ver)
			var res system.DBInstallResult
			if err := a.streamAgent(env, host, "/v1/dbnative/install", map[string]any{"engine": m.Engine, "version": ver, "password": pass}, &res); err != nil {
				return err
			}
			enc, _ := a.seal([]byte(pass))
			if _, err := a.insertRow("databases", m.Rows[0], map[string]any{"host": host, "addr": res.Addr, "admin_user": res.User, "admin_pass_enc": enc, "native": m.Engine + " " + ver}, "id"); err != nil {
				return err
			}
		} else {
			// merging into the server already here: keep its data, add what it lacks
			var tsc migrate.ScanResult
			a.call(env.Ctx, host, "POST", "/v1/migrate/scan", migrate.ScanReq{DBs: []string{have.Engine}}, &tsc)
			for name := range cur.req.Streams {
				if fam == "redis" {
					delete(cur.req.Streams, name)
					st.warn(it.ID, "migrate.warn.redis_kept")
					continue
				}
				if db, ok := strings.CutPrefix(name, "db-"); ok {
					if _, exists := tsc.DBs[have.Engine][db]; exists {
						delete(cur.req.Streams, name)
						st.warn(it.ID, "migrate.warn.db_exists:"+db)
					}
				}
			}
			for k, s := range cur.req.Streams { // restore with the engine that is here
				switch s.Kind {
				case "dbusers":
					s.Arg = have.Engine
				case "db":
					_, db, _ := strings.Cut(s.Arg, ":")
					s.Arg = have.Engine + ":" + db
				}
				cur.req.Streams[k] = s
			}
		}

	case "vm":
		var m struct {
			Secrets []row
			State   string
		}
		json.Unmarshal(meta, &m)
		if vst, err := a.vmStatus(env.Ctx, host); err != nil || !vst.Status.Installed {
			if _, err := a.subApply(env, "vm.install", map[string]any{}); err != nil {
				return err
			}
		}
		cur.after = func(migrate.ImportResult) error {
			for _, r := range m.Secrets {
				a.insertRow("vm_secrets", r, map[string]any{"host": host})
			}
			if m.State == "running" {
				if err := a.call(env.Ctx, host, "POST", "/v1/vms/"+it.Name+"/state", map[string]any{"action": "start"}, nil); err != nil {
					st.warn(it.ID, "migrate.warn.vm_start:"+errText(err))
				}
			}
			return nil
		}

	case "cron":
		var m struct{ Rows []row }
		json.Unmarshal(meta, &m)
		cur.after = func(migrate.ImportResult) error {
			have := map[string]bool{}
			norm := func(v any) string {
				var spec any
				json.Unmarshal([]byte(fmt.Sprint(v)), &spec)
				b, _ := json.Marshal(spec)
				return string(b)
			}
			here, _ := a.rowsOf("cron_jobs", "host=?", host)
			for _, r := range here {
				have[norm(r["spec"])] = true
			}
			for _, r := range m.Rows {
				if have[norm(r["spec"])] {
					continue
				}
				if _, err := a.insertRow("cron_jobs", r, map[string]any{"host": host}, "id"); err != nil {
					return err
				}
			}
			return nil
		}

	case "firewall":
		var m struct{ Rules []fwRule }
		json.Unmarshal(meta, &m)
		cur.after = func(migrate.ImportResult) error {
			cur := a.fwRules(host)
			seen := map[string]bool{}
			key := func(r fwRule) string { r.ID = 0; b, _ := json.Marshal(r); return string(b) }
			for _, r := range cur {
				seen[key(r)] = true
			}
			merged := append([]fwRule{}, cur...)
			added := 0
			for _, r := range m.Rules {
				if !seen[key(r)] {
					r.ID = 0
					merged = append(merged, r)
					added++
				}
			}
			if added == 0 {
				return nil
			}
			if msg := a.lockout(env, merged); msg != "" {
				st.warn(it.ID, "migrate.warn.fw_lockout:"+msg)
				return nil
			}
			before := cur
			for i, r := range merged[len(cur):] {
				b, _ := json.Marshal(r)
				a.db.Exec(`INSERT INTO fw_rules(host, position, spec, created_at) VALUES(?,?,?,strftime('%s','now'))`, host, len(cur)+i, string(b))
			}
			if err := a.fwApply(env.Ctx, host, a.fwRules(host)); err != nil {
				a.fwRestoreRows(host, before)
				return err
			}
			// the panel still reaches the agent and the rules keep SSH and the panel open for
			// the person who started this: confirm instead of waiting 60 s for them
			if _, err := a.subApply(env, "firewall.confirm", map[string]any{}); err != nil {
				st.warn(it.ID, "migrate.warn.fw_confirm")
			}
			return nil
		}

	case "certs":
		cur.req.Allow = []string{caddy.Root + "/data"}
		cur.after = func(migrate.ImportResult) error { st.needEdge = true; return nil }

	case "folder":
		cur.req.Allow = []string{it.Name}

	case "btdb", "btcron":
		return a.migPrepareBT(st, cur, meta)

	case "sftp", "dev":
		return a.migPrepareMore(st, cur, meta)

	case "panel":
		var m struct {
			Integrations  []row `json:"integrations"`
			BackupTargets []row `json:"backup_targets"`
			BackupJobs    []row `json:"backup_jobs"`
			DDNS          []row `json:"ddns"`
		}
		json.Unmarshal(meta, &m)
		cur.after = func(migrate.ImportResult) error {
			for _, r := range m.Integrations {
				var n int
				a.db.QueryRow(`SELECT COUNT(*) FROM integrations WHERE id=?`, r["id"]).Scan(&n)
				if n == 0 {
					a.insertRow("integrations", r, nil)
				}
			}
			targets := map[int64]int64{}
			for _, r := range m.BackupTargets {
				var id int64
				if a.db.QueryRow(`SELECT id FROM backup_targets WHERE name=?`, r["name"]).Scan(&id) != nil {
					id, _ = a.insertRow("backup_targets", r, nil, "id")
				}
				targets[int64num(r["id"])] = id
			}
			for _, r := range m.BackupJobs {
				t, ok := targets[int64num(r["target_id"])]
				if !ok || t == 0 {
					continue
				}
				if _, err := a.insertRow("backup_jobs", r, map[string]any{"host": host, "target_id": t}, "id"); err != nil {
					st.warn(it.ID, "migrate.warn.backup_job:"+fmt.Sprint(r["name"]))
				}
			}
			for _, r := range m.DDNS {
				a.insertRow("ddns", r, map[string]any{"host": host, "record_id": "", "last_ip": "", "last_check": 0}, "id")
			}
			return nil
		}
	}
	return nil
}
