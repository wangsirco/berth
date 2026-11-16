package panel

// What to do after installing an app whose ports other devices connect to directly (方案
// §5.8, RustDesk relay first): addresses to type into clients, the key, the devices that
// signed up, and putting the ports on the internet through Wharf when it is paired.

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/wangsirco/berth/internal/change"
	"github.com/wangsirco/berth/internal/errs"
	"github.com/wangsirco/berth/internal/panel/wharfapi"
)

type guidePort struct {
	Name    string `json:"name"`
	Proto   string `json:"proto"`
	Port    int    `json:"port"` // on the host
	Public  bool   `json:"public"`
	WANPort int    `json:"wan_port,omitempty"` // when published through Wharf
}

type rdDevice struct {
	ID      string `json:"id"`
	IP      string `json:"ip,omitempty"`
	Created string `json:"created"`
}

func appPorts(in appInstance) []guidePort {
	app, err := findApp(in.AppID)
	if err != nil {
		return nil
	}
	out := []guidePort{}
	for _, p := range app.Ports {
		n, _ := strconv.Atoi(in.Values[p.Field])
		if n == 0 {
			n = p.Port
		}
		proto := p.Proto
		if proto == "" {
			proto = "tcp"
		}
		out = append(out, guidePort{Name: p.Name, Proto: proto, Port: n, Public: p.Public})
	}
	return out
}

// appPublished: Wharf forwards made for an app (wharf_app_publish rows).
func (a *App) appPublished(host, name string) []wharfapi.Forward {
	var spec string
	if a.db.QueryRow(`SELECT spec FROM wharf_app_publish WHERE host=? AND name=?`, host, name).Scan(&spec) != nil {
		return nil
	}
	var fs []wharfapi.Forward
	json.Unmarshal([]byte(spec), &fs)
	return fs
}

// rustdeskInfo reads the relay's public key and its peer table through the agent.
func (a *App) rustdeskInfo(ctx context.Context, host, name string) (key string, devices []rdDevice) {
	root := appsRoot + "/" + name + "/data"
	var r struct {
		Content []byte `json:"content"`
	}
	if a.call(ctx, host, "POST", "/v1/files/read", map[string]any{"root": root, "path": root + "/id_ed25519.pub", "max": 4096}, &r) == nil {
		key = strings.TrimSpace(string(r.Content))
	}
	// the peer table: copy db (and its write-ahead log) here and read it
	dir, err := os.MkdirTemp("", "berth-rd-")
	if err != nil {
		return key, nil
	}
	defer os.RemoveAll(dir)
	for _, f := range []string{"db_v2.sqlite3", "db_v2.sqlite3-wal"} {
		var r struct {
			Content []byte `json:"content"`
		}
		if a.call(ctx, host, "POST", "/v1/files/read", map[string]any{"root": root, "path": root + "/" + f, "max": 8 << 20}, &r) == nil {
			os.WriteFile(filepath.Join(dir, f), r.Content, 0o600)
		}
	}
	db, err := sql.Open("sqlite", filepath.Join(dir, "db_v2.sqlite3"))
	if err != nil {
		return key, nil
	}
	defer db.Close()
	rows, err := db.QueryContext(ctx, `SELECT id, COALESCE(info,''), COALESCE(created_at,'') FROM peer ORDER BY created_at DESC LIMIT 200`)
	if err != nil {
		return key, []rdDevice{}
	}
	defer rows.Close()
	devices = []rdDevice{}
	for rows.Next() {
		var d rdDevice
		var info string
		rows.Scan(&d.ID, &info, &d.Created)
		var i struct {
			IP string `json:"ip"`
		}
		json.Unmarshal([]byte(info), &i)
		d.IP = strings.TrimPrefix(i.IP, "::ffff:")
		devices = append(devices, d)
	}
	return key, devices
}

// GET /api/v1/hosts/{host}/apps/{name}/guide
func (a *App) handleAppGuide(w http.ResponseWriter, r *http.Request) {
	host := r.PathValue("host")
	in, err := a.appInstance(host, r.PathValue("name"))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	out := map[string]any{"app_id": in.AppID, "ports": appPorts(in)}
	// addresses: the one the browser used to reach the panel (for this server), the LAN
	// address Wharf knows, and the public one
	addrs := []string{}
	add := func(ip string) {
		if p := net.ParseIP(ip); p != nil && !p.IsLoopback() && !p.IsUnspecified() && !contains(addrs, ip) {
			addrs = append(addrs, ip)
		}
	}
	if host == "local" {
		if h, _, err := net.SplitHostPort(r.Host); err == nil {
			add(h)
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 6*time.Second)
	defer cancel()
	var lan struct {
		IP string `json:"ip"`
	}
	if a.call(ctx, host, "GET", "/v1/net/lan-ip", nil, &lan) == nil {
		add(lan.IP)
	}
	var pub struct {
		IP string `json:"ip"`
	}
	if a.call(ctx, host, "GET", "/v1/net/public-ip", nil, &pub) == nil && pub.IP != "" {
		out["public_ip"] = pub.IP
	}
	out["addresses"] = addrs
	if l, err := a.wharf(); err == nil && l != nil && l.State == "active" {
		out["wharf"] = map[string]any{"name": l.Name, "can_forward": l.has("forwards")}
	}
	out["published"] = a.appPublished(host, in.Name)
	if in.AppID == "rustdesk-server" {
		key, devs := a.rustdeskInfo(ctx, host, in.Name)
		out["key"], out["devices"] = key, devs
	}
	writeJSON(w, 200, out)
}

func init() {
	routeHooks = append(routeHooks, func(a *App, mux *http.ServeMux) {
		mux.HandleFunc("GET /api/v1/hosts/{host}/apps/{name}/guide", a.authed("store.install", a.handleAppGuide))
	})

	type pubParams struct {
		Name     string `json:"name"`
		TargetIP string `json:"target_ip"`
	}
	props := map[string]change.Prop{"name": {Type: "string"}, "target_ip": {Type: "string", Desc: "LAN address Wharf forwards to (default: the first one Wharf knows for this server)"}}
	plan := func(env change.Env, raw json.RawMessage) (appInstance, []guidePort, *wharfapi.Client, string, string, error) {
		a := svc(env)
		var p pubParams
		json.Unmarshal(raw, &p)
		in, err := a.appInstance(env.Host, p.Name)
		if err != nil {
			return in, nil, nil, "", "", err
		}
		var pub []guidePort
		for _, gp := range appPorts(in) {
			if gp.Public {
				pub = append(pub, gp)
			}
		}
		if len(pub) == 0 {
			return in, nil, nil, "", "", errs.New(400, "app.no_public_ports")
		}
		c, l, err := a.wharfClient("forwards")
		if err != nil {
			return in, nil, nil, "", "", err
		}
		if p.TargetIP == "" && len(l.TargetIPs) > 0 {
			p.TargetIP = l.TargetIPs[0]
		}
		if net.ParseIP(p.TargetIP) == nil {
			return in, nil, nil, "", "", errs.New(400, "wharf.bad_publish")
		}
		return in, pub, c, p.TargetIP, l.Name, nil
	}
	change.Register(change.Kind{
		Name: "app.publish", Perm: "network.manage", Scope: "system:write", Title: "kind.app.publish",
		Params: change.ParamSchema{Props: props, Required: []string{"name"}}, CLI: "berth app publish {{.name}}",
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			in, pub, _, ip, wname, err := plan(env, raw)
			if err != nil {
				return change.Plan{}, err
			}
			cmds := []string{}
			for _, p := range pub {
				cmds = append(cmds, fmt.Sprintf("Wharf %s: WAN %d/%s → %s:%d", wname, p.Port, p.Proto, ip, p.Port))
			}
			return change.Plan{Summary: "plan.app.publish", SummaryP: map[string]any{"name": in.Name, "wharf": wname, "n": len(pub)},
				Risk: change.RiskMedium, Undoable: true, Commands: cmds, Affected: []change.Object{projObj(in.Name, env.Host)}}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			a := svc(env)
			in, pub, c, ip, _, err := plan(env, raw)
			if err != nil {
				return nil, nil, err
			}
			actor := actorOf(env, "应用 "+in.Name)
			made := []wharfapi.Forward{}
			undo := func() {
				for _, f := range made {
					c.DeleteForward(env.Ctx, actor, f.ID)
				}
			}
			for _, old := range a.appPublished(env.Host, in.Name) { // publishing again replaces
				c.DeleteForward(env.Ctx, actor, old.ID)
			}
			for _, p := range pub {
				port := strconv.Itoa(p.Port)
				f, err := c.CreateForward(env.Ctx, actor, wharfapi.Forward{Name: "berth: " + in.Name + " " + p.Name, Proto: p.Proto, WANPort: port, TargetIP: ip, TargetPort: port,
					BerthRef: "app:" + in.Name, BerthHost: env.Host, Note: "由 Berth 为应用 " + in.Name + " 创建"})
				if err != nil {
					undo()
					return nil, nil, a.wharfErr(err)
				}
				env.Log("WAN %s/%s → %s:%s", port, p.Proto, ip, port)
				made = append(made, f)
			}
			spec, _ := json.Marshal(made)
			a.db.Exec(`INSERT OR REPLACE INTO wharf_app_publish(host, name, spec) VALUES(?,?,?)`, env.Host, in.Name, string(spec))
			u, _ := json.Marshal(map[string]string{"name": in.Name})
			return made, &change.UndoRecipe{Kind: "app.unpublish", Params: u}, nil
		},
	})
	change.Register(change.Kind{
		Name: "app.unpublish", Perm: "network.manage", Scope: "system:write", Title: "kind.app.unpublish",
		Params: change.ParamSchema{Props: map[string]change.Prop{"name": {Type: "string"}}, Required: []string{"name"}}, CLI: "berth app unpublish {{.name}}",
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			var p pubParams
			json.Unmarshal(raw, &p)
			fs := svc(env).appPublished(env.Host, p.Name)
			if len(fs) == 0 {
				return change.Plan{}, errs.New(404, "app.not_published")
			}
			return change.Plan{Summary: "plan.app.unpublish", SummaryP: map[string]any{"name": p.Name, "n": len(fs)}, Risk: change.RiskLow}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			a := svc(env)
			var p pubParams
			json.Unmarshal(raw, &p)
			c, _, err := a.wharfClient("forwards")
			if err != nil {
				return nil, nil, err
			}
			for _, f := range a.appPublished(env.Host, p.Name) {
				if err := c.DeleteForward(env.Ctx, actorOf(env, "应用 "+p.Name), f.ID); err != nil && !strings.Contains(err.Error(), "404") {
					return nil, nil, a.wharfErr(err)
				}
			}
			a.db.Exec(`DELETE FROM wharf_app_publish WHERE host=? AND name=?`, env.Host, p.Name)
			u, _ := json.Marshal(map[string]string{"name": p.Name})
			return nil, &change.UndoRecipe{Kind: "app.publish", Params: u}, nil
		},
	})
}
