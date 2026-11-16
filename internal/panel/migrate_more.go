package panel

// Server migration (R6), the rest: SFTP sub-accounts (their password hashes and keys, so the
// same logins keep working; the folders they share move with the sites) and dev workspaces
// (settings, the project folder, the home volume with extensions and sign-ins).

import (
	"context"
	"encoding/json"
	"path"
	"strings"

	"github.com/wangsirco/berth/internal/agent/migrate"
	"github.com/wangsirco/berth/internal/agent/system"
	"github.com/wangsirco/berth/internal/change"
)

// migMoreItems: SFTP accounts and dev workspaces of host, sized from the same scan.
func (a *App) migMoreItems(ctx context.Context, host string, sc migrate.ScanResult) []migItem {
	var out []migItem
	var accts []system.SFTPExport
	if a.call(ctx, host, "GET", "/v1/migrate/sftp", nil, &accts) == nil && len(accts) > 0 {
		names := []string{}
		for _, x := range accts {
			names = append(names, x.Name)
		}
		out = append(out, migItem{ID: "sftp", Type: "sftp", Name: "sftp", Default: true, Info: map[string]any{"count": len(accts), "names": names},
			meta: func() (any, error) {
				var fresh []system.SFTPExport // read again when the pack is written: hashes may have changed
				if err := a.call(ctx, host, "GET", "/v1/migrate/sftp", nil, &fresh); err != nil {
					return nil, err
				}
				return map[string]any{"accounts": fresh}, nil
			}})
	}
	vols := map[string]migrate.ComposeProject{}
	for _, c := range sc.Compose {
		vols[c.Name] = c
	}
	for _, w := range a.devWorkspaces(host) {
		w := w
		it := migItem{ID: "dev:" + w.Name, Type: "dev", Name: w.Name, Default: true, Need: []migNeed{},
			Info: map[string]any{"dir": w.Dir, "access": w.Access, "domains": w.Domains}}
		if w.Dir != "" {
			it.exp.Files = []string{w.Dir}
			n := sc.Paths[w.Dir]
			it.Bytes += n
			it.Need = append(it.Need, migNeed{w.Dir, n})
		}
		labels := map[string]map[string]string{}
		if cp, ok := vols[devProject(w.Name)]; ok {
			for _, v := range cp.Volumes {
				it.exp.Volumes = append(it.exp.Volumes, v.Name)
				labels[v.Name] = v.Labels
				it.Bytes += v.Bytes
				it.Need = append(it.Need, migNeed{"@docker", v.Bytes})
			}
			for _, im := range cp.Images {
				it.Need = append(it.Need, migNeed{"@docker", im.Bytes}) // pulled or built again there
			}
			it.exp.Stop = []string{devContainer(w.Name)} // the home volume is read while nothing writes to it
		}
		it.meta = func() (any, error) {
			rows, err := a.rowsOf("dev_workspaces", "host=? AND name=?", host, w.Name)
			return map[string]any{"rows": rows, "volumes": labels}, err
		}
		out = append(out, it)
	}
	return out
}

// migMorePaths: what to size for them.
func (a *App) migMorePaths(host string) []string {
	var out []string
	for _, w := range a.devWorkspaces(host) {
		if w.Dir != "" {
			out = append(out, w.Dir)
		}
	}
	return out
}

// migMoreCheck: does the target already have it?
func (a *App) migMoreCheck(ctx context.Context, host string, it migItem, c *migCheck) {
	switch it.Type {
	case "dev":
		if _, err := a.devWorkspace(host, it.Name); err == nil {
			c.Status, c.Note = "exists", "migrate.chk.dev_exists"
		}
	case "sftp":
		c.Status, c.Note = "merge", "migrate.chk.sftp_merge"
	}
}

func (a *App) migPrepareMore(st *migState, cur *migItemRun, meta []byte) error {
	env := st.env
	host := env.Host
	switch cur.it.Type {
	case "sftp":
		var m struct{ Accounts []system.SFTPExport }
		json.Unmarshal(meta, &m)
		cur.after = func(migrate.ImportResult) error {
			here := map[string]bool{}
			var list struct {
				Items []system.SFTPAccount `json:"items"`
			}
			a.call(env.Ctx, host, "GET", "/v1/system/sftp", nil, &list)
			for _, x := range list.Items {
				here[x.Name] = true
			}
			for _, x := range m.Accounts {
				if here[x.Name] {
					st.warn(cur.it.ID, "migrate.warn.sftp_exists:"+x.Name)
					continue
				}
				var stat map[string]any // the folder it shares must be here (it moves with its site)
				if a.call(env.Ctx, host, "POST", "/v1/files/stat", map[string]any{"root": "/", "path": x.Dir}, &stat) != nil {
					st.warn(cur.it.ID, "migrate.warn.sftp_no_dir:"+x.Name+" "+x.Dir)
					continue
				}
				acc := system.SFTPAccount{Name: x.Name, Dir: x.Dir, ReadOnly: x.ReadOnly, PasswordHash: x.Hash, Keys: x.Keys}
				if acc.PasswordHash == "" && len(acc.Keys) == 0 {
					st.warn(cur.it.ID, "migrate.warn.sftp_no_login:"+x.Name)
					continue
				}
				env.Log("  SFTP account %s → %s", x.Name, x.Dir)
				err := a.streamAgent(env, host, "/v1/system/sftp", acc, nil)
				if err != nil && strings.Contains(errText(err), "setfacl is missing") {
					// a stock Debian has no acl tools: install them, then try again
					env.Log("  installing acl (SFTP accounts share folders through ACLs)")
					if _, ierr := a.subApply(env, "pkg.install", map[string]any{"names": []string{"acl"}}); ierr == nil {
						err = a.streamAgent(env, host, "/v1/system/sftp", acc, nil)
					}
				}
				if err != nil {
					st.warn(cur.it.ID, "migrate.warn.sftp_failed:"+x.Name+": "+errText(err))
				}
			}
			return nil
		}

	case "dev":
		var m struct {
			Rows    []row
			Volumes map[string]map[string]string
		}
		if err := json.Unmarshal(meta, &m); err != nil || len(m.Rows) == 0 {
			return errText2("no workspace record in the pack")
		}
		var s devSpec
		json.Unmarshal([]byte(strings.TrimSpace(asString(m.Rows[0]["spec"]))), &s)
		if s.Dir != "" {
			cur.req.Allow = []string{path.Clean(s.Dir)}
		}
		cur.req.Volumes = m.Volumes
		cur.after = func(migrate.ImportResult) error {
			// ports: the same ones when free here
			sshP, webP, err := a.devPorts(env.Ctx, host, s.SSHPort, s.WebPort)
			if err != nil {
				return err
			}
			if sshP != s.SSHPort || webP != s.WebPort {
				st.warn(cur.it.ID, "migrate.warn.dev_ports:"+s.Name)
			}
			s.SSHPort, s.WebPort = sshP, webP
			var n int
			a.db.QueryRow(`SELECT COUNT(*) FROM users WHERE id=?`, s.OwnerID).Scan(&n)
			if n == 0 { // another panel's user: the keys of whoever moves it go in
				s.OwnerID = actorUID(env)
			}
			s.SiteID = 0
			b, _ := json.Marshal(s)
			if _, err := a.insertRow("dev_workspaces", m.Rows[0], map[string]any{"host": host, "spec": string(b)}); err != nil {
				return err
			}
			if s.Claude {
				if err := agentOp(env, "/v1/dev/claude/install", nil); err != nil {
					return err
				}
			}
			if s.Code {
				if err := agentOp(env, "/v1/dev/tools/install", nil); err != nil {
					return err
				}
			}
			if err := a.devState(env, s, false, false); err != nil {
				return err
			}
			if err := a.devApplyCompose(env, s, true, false); err != nil {
				return err
			}
			a.relate(host, change.Object{Type: "host", ID: host}, devObj(s.Name, host), "manages", "module")
			if s.Code && s.Access == "public" {
				// the site came along as its own item: point the workspace at it, or make it
				for _, site := range a.devSites(host, s.Name) {
					s.SiteID = site.ID
				}
				if s.SiteID == 0 {
					if err := a.devSite(env, &s); err != nil {
						st.warn(cur.it.ID, "migrate.warn.dev_site:"+errText(err))
					}
				}
				a.saveDev(host, s)
			}
			return nil
		}
	}
	return nil
}

func asString(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case []byte:
		return string(x)
	}
	return ""
}
