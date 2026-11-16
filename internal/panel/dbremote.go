package panel

// Database remote access (宝塔/1Panel 的“远程访问”开关): which address the instance's
// published port listens on — this machine only, one LAN address, or every address.
// Choosing where it listens (rather than leaning on a firewall rule with a rollback timer)
// means it can never end up more open than what was chosen.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/wangsirco/berth/internal/change"
	"github.com/wangsirco/berth/internal/errs"
)

type dbRemote struct {
	Container string   `json:"container"`
	Project   string   `json:"project"`
	Service   string   `json:"service"`
	Inner     int      `json:"inner"` // the container's port (3306, 5432)
	Port      int      `json:"port"`  // published on the host
	Bind      string   `json:"bind"`  // 127.0.0.1 | 0.0.0.0 | a LAN address
	Addrs     []string `json:"addrs"` // this host's IPv4 addresses to choose from
}

var dbInnerPort = map[string]int{"mysql": 3306, "mariadb": 3306, "postgres": 5432}

func (a *App) dbRemoteState(ctx context.Context, host string, id int64) (dbRemote, error) {
	d, err := a.dbInstance(host, id)
	if err != nil {
		return dbRemote{}, err
	}
	if d.Container == "" {
		return dbRemote{}, errs.New(400, "db.remote_external")
	}
	ct, err := a.findContainer(ctx, host, d.Container)
	if err != nil {
		return dbRemote{}, err
	}
	if ct.Project == "" {
		return dbRemote{}, errs.New(400, "db.remote_not_compose")
	}
	st := dbRemote{Container: ct.Name, Project: ct.Project, Service: ct.Service, Inner: dbInnerPort[d.Engine], Addrs: []string{}}
	for _, p := range ct.Ports {
		if p.Private == st.Inner && p.Public > 0 {
			st.Port, st.Bind = p.Public, p.IP
			if st.Bind == "" || st.Bind == "::" {
				st.Bind = "0.0.0.0"
			}
			if p.IP == "0.0.0.0" || p.IP == "127.0.0.1" || p.IP == "" {
				break
			}
		}
	}
	if st.Port == 0 {
		return st, errs.New(400, "db.remote_not_published", "port", st.Inner)
	}
	var nics struct {
		Items []struct {
			Kind  string `json:"kind"`
			Addrs []struct {
				IP, Family, Scope string
			} `json:"addrs"`
		} `json:"items"`
	}
	a.call(ctx, host, "GET", "/v1/system/nics", nil, &nics)
	for _, n := range nics.Items {
		if n.Kind == "docker" || n.Kind == "veth" || n.Kind == "loopback" || n.Kind == "incus" {
			continue
		}
		for _, ad := range n.Addrs {
			if ad.Family == "4" && ad.Scope == "global" {
				st.Addrs = append(st.Addrs, ad.IP)
			}
		}
	}
	return st, nil
}

func init() {
	routeHooks = append(routeHooks, func(a *App, mux *http.ServeMux) {
		mux.HandleFunc("GET /api/v1/hosts/{host}/databases/{id}/remote", a.authed("db.view", func(w http.ResponseWriter, r *http.Request) {
			id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
			st, err := a.dbRemoteState(r.Context(), r.PathValue("host"), id)
			if err != nil {
				a.fail(w, r, err)
				return
			}
			writeJSON(w, 200, st)
		}))
	})
	change.Register(change.Kind{
		Name: "db.remote", Perm: "db.manage", Scope: "system:write", Title: "kind.db.remote", CLI: "berth db remote {{.instance}} {{.bind}}",
		Params: change.ParamSchema{Props: map[string]change.Prop{"instance": {Type: "integer"},
			"bind": {Type: "string", Desc: "127.0.0.1 (this server only) | a LAN address of this host | 0.0.0.0 (every address)"}}, Required: []string{"instance", "bind"}},
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			a := svc(env)
			var p struct {
				Instance int64  `json:"instance"`
				Bind     string `json:"bind"`
			}
			json.Unmarshal(raw, &p)
			st, err := a.dbRemoteState(env.Ctx, env.Host, p.Instance)
			if err != nil {
				return change.Plan{}, err
			}
			var cur struct{ File, Before, After string }
			if err := a.call(env.Ctx, env.Host, "POST", "/v1/compose/projects/"+url.PathEscape(st.Project)+"/bind",
				map[string]any{"service": st.Service, "port": st.Inner, "bind": p.Bind, "check": true}, &cur); err != nil {
				return change.Plan{}, err
			}
			sum, risk := "plan.db.remote_lan", change.RiskMedium
			switch p.Bind {
			case "127.0.0.1":
				sum, risk = "plan.db.remote_off", change.RiskLow
			case "0.0.0.0":
				sum, risk = "plan.db.remote_all", change.RiskHigh
			}
			cmds := []string{"docker compose up -d --no-deps " + st.Service + "  (a few seconds without the database)"}
			if p.Bind == "0.0.0.0" {
				cmds = append(cmds, "# "+T("zh-CN", "db.remote_all_note", map[string]any{"port": st.Port}))
			}
			return change.Plan{Summary: sum, SummaryP: map[string]any{"name": st.Container, "addr": p.Bind, "port": st.Port}, Risk: risk, Undoable: true,
				Files: []change.FileDiff{{Path: cur.File, Before: cur.Before, After: cur.After}}, Commands: cmds,
				Affected: []change.Object{projObj(st.Project, env.Host)}}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			a := svc(env)
			var p struct {
				Instance int64  `json:"instance"`
				Bind     string `json:"bind"`
			}
			json.Unmarshal(raw, &p)
			st, err := a.dbRemoteState(env.Ctx, env.Host, p.Instance)
			if err != nil {
				return nil, nil, err
			}
			if err := agentOp(env, "/v1/compose/projects/"+url.PathEscape(st.Project)+"/bind", map[string]any{"service": st.Service, "port": st.Inner, "bind": p.Bind}); err != nil {
				return nil, nil, err
			}
			// the container was recreated: done once the database answers again
			if d, err := a.dbInstance(env.Host, p.Instance); err == nil {
				for i := 0; i < 60; i++ {
					if a.dbPing(env.Ctx, d) == nil {
						break
					}
					time.Sleep(time.Second)
				}
			}
			u, _ := json.Marshal(map[string]any{"instance": p.Instance, "bind": st.Bind})
			return map[string]any{"port": st.Port}, &change.UndoRecipe{Kind: "db.remote", Params: u}, nil
		},
	})
}
