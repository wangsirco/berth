package panel

// Remote desktop (方案 §5.8): guacd runs as a container next to the panel; the panel speaks
// the Guacamole protocol to it and relays to guacamole-common-js in the browser. The
// connection library keeps credentials sealed and is shared per user.

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/wangsirco/berth/internal/change"
	"github.com/wangsirco/berth/internal/errs"
	"github.com/wangsirco/berth/internal/panel/guac"
)

const (
	guacdProject = "berth-guacd"
	guacdImage   = "guacamole/guacd:1.6.0"
	guacdAddr    = "127.0.0.1:4822"
)

func guacdCompose() string {
	return `# Berth: guacd (remote desktop proxy); only the panel talks to it
services:
  guacd:
    image: ` + guacdImage + `
    restart: unless-stopped
    # host network: guacd reaches VMs, LAN machines and this server's own desktop (127.0.0.1)
    network_mode: host
    command: ["-b", "127.0.0.1", "-l", "4822"]
    mem_limit: 512m
`
}

type rdConn struct {
	ID         int64   `json:"id"`
	Name       string  `json:"name"`
	Group      string  `json:"group"`
	Protocol   string  `json:"protocol"` // rdp | vnc | ssh | telnet
	Host       string  `json:"host"`
	Port       int     `json:"port"`
	Username   string  `json:"username,omitempty"`
	Domain     string  `json:"domain,omitempty"`
	System     string  `json:"system,omitempty"` // "desktop": this server's own desktop, kept by desktop.*
	Clipboard  bool    `json:"clipboard"`
	Files      bool    `json:"files"`       // file transfer (RDP drive / SFTP)
	IgnoreCert bool    `json:"ignore_cert"` // RDP self-signed certificates (usual for Windows)
	Users      []int64 `json:"users"`
	Owner      int64   `json:"owner"` // who may connect; empty = only remote.use holders with users.manage
	HasSecret  bool    `json:"has_secret"`
	Password   string  `json:"password,omitempty"`    // write only
	Key        string  `json:"private_key,omitempty"` // write only (SSH)
	Recent     int64   `json:"last_used,omitempty"`
}

var defaultPorts = map[string]int{"rdp": 3389, "vnc": 5900, "ssh": 22, "telnet": 23}

func (c rdConn) validate() error {
	if strings.TrimSpace(c.Name) == "" || len(c.Name) > 80 || len(c.Group) > 40 {
		return errs.New(400, "rd.bad_conn")
	}
	if _, ok := defaultPorts[c.Protocol]; !ok {
		return errs.New(400, "rd.bad_conn")
	}
	if c.Host == "" || strings.ContainsAny(c.Host, " /\\") || c.Port < 1 || c.Port > 65535 {
		return errs.New(400, "rd.bad_conn")
	}
	return nil
}

func (a *App) rdConns() []rdConn {
	rows, err := a.db.Query(`SELECT id, spec, secret_enc, last_used FROM rd_connections ORDER BY grp, name`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := []rdConn{}
	for rows.Next() {
		var c rdConn
		var spec string
		var enc []byte
		var last int64
		rows.Scan(&c.ID, &spec, &enc, &last)
		id := c.ID
		json.Unmarshal([]byte(spec), &c)
		c.ID, c.HasSecret, c.Recent = id, len(enc) > 0, last
		c.Password, c.Key = "", ""
		out = append(out, c)
	}
	return out
}

func (a *App) rdConn(id int64) (rdConn, map[string]string, error) {
	var c rdConn
	var spec string
	var enc []byte
	if err := a.db.QueryRow(`SELECT spec, secret_enc FROM rd_connections WHERE id=?`, id).Scan(&spec, &enc); err != nil {
		return c, nil, errs.New(404, "rd.not_found")
	}
	json.Unmarshal([]byte(spec), &c)
	c.ID = id
	secret := map[string]string{}
	if len(enc) > 0 {
		if p, err := a.open(enc); err == nil {
			json.Unmarshal(p, &secret)
		}
	}
	return c, secret, nil
}

// mayUse: listed users, or people who manage users (admins).
func (c rdConn) mayUse(u *User) bool {
	if !u.Can("remote.use") {
		return false
	}
	if u.Role == RoleSuper || u.Can("users.manage") || c.Owner == u.ID {
		return true
	}
	for _, id := range c.Users {
		if id == u.ID {
			return true
		}
	}
	return false
}

// rdAllowed: only people who may use a connection may change or delete it.
func (a *App) rdAllowed(env change.Env, id int64) error {
	c, _, err := a.rdConn(id)
	if err != nil {
		return err
	}
	var uid int64
	fmt.Sscanf(env.ActorID, "%d", &uid)
	u, err := a.userByID(uid)
	if err != nil || u == nil || !c.mayUse(u) {
		return errs.New(403, "auth.forbidden", "perm", "remote.use")
	}
	return nil
}

// ensureGuacd deploys (or starts) the guacd container on this server.
func (a *App) ensureGuacd(env change.Env) error {
	if err := a.call(env.Ctx, "local", "PUT", "/v1/compose/projects/"+guacdProject+"/files", map[string]any{"compose": guacdCompose(), "env": "", "create": true}, nil); err != nil && !strings.Contains(err.Error(), "exists") {
		return err
	}
	if err := agentOp(env, "/v1/compose/projects/"+guacdProject+"/up", nil); err != nil {
		return err
	}
	a.relate("local", change.Object{Type: "host", ID: "local"}, projObj(guacdProject, "local"), "manages", "module")
	return nil
}

func (a *App) guacdRunning(ctx context.Context) bool {
	cs, err := a.containers(ctx, "local")
	if err != nil {
		return false
	}
	for _, c := range cs {
		if c.Project == guacdProject && c.State == "running" {
			return true
		}
	}
	return false
}

// guacArgs: the guacd arguments for a connection.
func guacArgs(c rdConn, secret map[string]string) map[string]string {
	m := map[string]string{"hostname": c.Host, "port": strconv.Itoa(c.Port), "username": c.Username, "password": secret["password"]}
	if !c.Clipboard {
		m["disable-copy"], m["disable-paste"] = "true", "true"
	}
	switch c.Protocol {
	case "rdp":
		m["domain"], m["security"] = c.Domain, "any"
		if c.IgnoreCert {
			m["ignore-cert"] = "true"
		}
		m["resize-method"] = "display-update"
		m["enable-font-smoothing"] = "true"
		if c.Files {
			m["enable-drive"], m["drive-path"], m["drive-name"], m["create-drive-path"] = "true", "/drive/"+strconv.FormatInt(c.ID, 10), "Berth", "true"
		}
	case "ssh":
		m["private-key"] = secret["private_key"]
		m["color-scheme"] = "gray-black"
		if c.Files {
			m["enable-sftp"] = "true"
		}
	case "vnc":
		delete(m, "username")
	}
	return m
}

func init() {
	routeHooks = append(routeHooks, func(a *App, mux *http.ServeMux) {
		mux.HandleFunc("GET /api/v1/remote", a.authed("remote.use", func(w http.ResponseWriter, r *http.Request) {
			u := currentUser(r)
			out := []rdConn{}
			for _, c := range a.rdConns() {
				if c.mayUse(u) {
					out = append(out, c)
				}
			}
			var guacd struct {
				Running bool `json:"running"`
			}
			guacd.Running = a.guacdRunning(r.Context())
			var desk any
			if u.Can("settings.manage") {
				var st map[string]any
				if err := a.call(r.Context(), "local", "GET", "/v1/desktop", nil, &st); err == nil {
					desk = st
				}
			}
			writeJSON(w, 200, map[string]any{"items": out, "guacd": guacd, "desktop": desk, "manage": u.Can("users.manage") || u.Role == RoleSuper})
		}))
		mux.HandleFunc("GET /api/v1/remote/{id}/tunnel", a.authed("remote.use", a.handleRDTunnel))
	})

	change.Register(change.Kind{
		Name: "rd.install", Perm: "remote.use", Scope: "system:write", Title: "kind.rd.install",
		Params: change.ParamSchema{Props: map[string]change.Prop{}}, CLI: "berth remote install", Heavy: true, MemMB: 128,
		Plan: func(env change.Env, _ json.RawMessage) (change.Plan, error) {
			return change.Plan{Summary: "plan.rd.install", Risk: change.RiskLow, Undoable: true,
				Files:    []change.FileDiff{{Path: "/srv/berth/stacks/" + guacdProject + "/compose.yaml", After: guacdCompose()}},
				Commands: []string{"docker compose -p " + guacdProject + " up -d   # listens on 127.0.0.1:4822 only"}}, nil
		},
		Apply: func(env change.Env, _ json.RawMessage) (any, *change.UndoRecipe, error) {
			return nil, nil, svc(env).ensureGuacd(env)
		},
	})

	connProps := map[string]change.Prop{"id": {Type: "integer"}, "name": {Type: "string"}, "group": {Type: "string"}, "protocol": {Type: "string", Enum: []string{"rdp", "vnc", "ssh", "telnet"}},
		"host": {Type: "string"}, "port": {Type: "integer"}, "username": {Type: "string"}, "domain": {Type: "string"}, "password": {Type: "string"}, "private_key": {Type: "string"},
		"clipboard": {Type: "boolean"}, "files": {Type: "boolean"}, "ignore_cert": {Type: "boolean"}, "users": {Type: "array"}}
	change.Register(change.Kind{
		Name: "rd.connection.set", Perm: "remote.use", Scope: "system:write", Title: "kind.rd.connection.set",
		Params: change.ParamSchema{Props: connProps, Required: []string{"name", "protocol", "host"}}, Redact: []string{"password", "private_key"},
		CLI: "berth remote set {{.name}}",
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			var c rdConn
			json.Unmarshal(raw, &c)
			if c.Port == 0 {
				c.Port = defaultPorts[c.Protocol]
			}
			if err := c.validate(); err != nil {
				return change.Plan{}, err
			}
			if c.ID != 0 {
				if err := svc(env).rdAllowed(env, c.ID); err != nil {
					return change.Plan{}, err
				}
			}
			return change.Plan{Summary: "plan.rd.connection.set", SummaryP: map[string]any{"name": c.Name, "target": fmt.Sprintf("%s://%s:%d", c.Protocol, c.Host, c.Port)}, Risk: change.RiskLow, Undoable: true}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			a := svc(env)
			var c rdConn
			json.Unmarshal(raw, &c)
			if c.Port == 0 {
				c.Port = defaultPorts[c.Protocol]
			}
			secret := map[string]string{}
			fmt.Sscanf(env.ActorID, "%d", &c.Owner)
			if c.ID != 0 { // keep the stored secret and owner unless a new one came
				prev, old, err := a.rdConn(c.ID)
				if err == nil {
					c.Owner = prev.Owner
					// a stored password only goes to the target it was typed for: point the
					// connection somewhere else and it has to be entered again
					if prev.Host == c.Host && prev.Port == c.Port && prev.Protocol == c.Protocol {
						secret = old
					}
				}
			}
			if c.Password != "" {
				secret["password"] = c.Password
			}
			if c.Key != "" {
				secret["private_key"] = c.Key
			}
			var enc []byte
			if len(secret) > 0 {
				b, _ := json.Marshal(secret)
				var err error
				if enc, err = a.seal(b); err != nil {
					return nil, nil, err
				}
			}
			c.Password, c.Key, c.HasSecret = "", "", false
			spec, _ := json.Marshal(c)
			if c.ID != 0 {
				var oldSpec string
				var oldEnc []byte
				if a.db.QueryRow(`SELECT spec, secret_enc FROM rd_connections WHERE id=?`, c.ID).Scan(&oldSpec, &oldEnc) == nil {
					a.db.Exec(`UPDATE rd_connections SET spec=?, grp=?, name=?, secret_enc=? WHERE id=?`, string(spec), c.Group, c.Name, enc, c.ID)
					a.db.Exec(`INSERT OR REPLACE INTO rd_undo(id, spec, secret_enc) VALUES(?,?,?)`, c.ID, oldSpec, oldEnc)
					u, _ := json.Marshal(map[string]any{"id": c.ID})
					return map[string]int64{"id": c.ID}, &change.UndoRecipe{Kind: "rd.connection.restore", Params: u}, nil
				}
			}
			res, err := a.db.Exec(`INSERT INTO rd_connections(name, grp, spec, secret_enc, created_by, created_at) VALUES(?,?,?,?,?,?)`, c.Name, c.Group, string(spec), enc, env.ActorName, time.Now().Unix())
			if err != nil {
				return nil, nil, err
			}
			id, _ := res.LastInsertId()
			u, _ := json.Marshal(map[string]int64{"id": id})
			return map[string]int64{"id": id}, &change.UndoRecipe{Kind: "rd.connection.delete", Params: u}, nil
		},
	})
	change.Register(change.Kind{
		Name: "rd.connection.delete", Perm: "remote.use", Scope: "system:write", Title: "kind.rd.connection.delete",
		Params: change.ParamSchema{Props: map[string]change.Prop{"id": {Type: "integer"}}, Required: []string{"id"}}, CLI: "berth remote delete {{.id}}",
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			var p struct{ ID int64 }
			json.Unmarshal(raw, &p)
			c, _, err := svc(env).rdConn(p.ID)
			if err != nil {
				return change.Plan{}, err
			}
			if err := svc(env).rdAllowed(env, p.ID); err != nil {
				return change.Plan{}, err
			}
			return change.Plan{Summary: "plan.rd.connection.delete", SummaryP: map[string]any{"name": c.Name}, Risk: change.RiskLow, Undoable: true}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			a := svc(env)
			var p struct{ ID int64 }
			json.Unmarshal(raw, &p)
			var spec string
			var enc []byte
			if a.db.QueryRow(`SELECT spec, secret_enc FROM rd_connections WHERE id=?`, p.ID).Scan(&spec, &enc) != nil {
				return nil, nil, errs.New(404, "rd.not_found")
			}
			a.db.Exec(`INSERT OR REPLACE INTO rd_undo(id, spec, secret_enc) VALUES(?,?,?)`, p.ID, spec, enc)
			a.db.Exec(`DELETE FROM rd_connections WHERE id=?`, p.ID)
			return nil, &change.UndoRecipe{Kind: "rd.connection.restore", Params: raw}, nil
		},
	})
	// undo helper: puts a connection back as it was (secret included, which the audit never shows)
	change.Register(change.Kind{
		Name: "rd.connection.restore", Perm: "remote.use", Scope: "system:write", Title: "kind.rd.connection.restore",
		Params: change.ParamSchema{Props: map[string]change.Prop{"id": {Type: "integer"}}, Required: []string{"id"}}, CLI: "berth remote restore {{.id}}",
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			return change.Plan{Summary: "plan.rd.connection.restore", Risk: change.RiskLow}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			a := svc(env)
			var p struct{ ID int64 }
			json.Unmarshal(raw, &p)
			var spec string
			var enc []byte
			if a.db.QueryRow(`SELECT spec, secret_enc FROM rd_undo WHERE id=?`, p.ID).Scan(&spec, &enc) != nil {
				return nil, nil, errs.New(404, "rd.not_found")
			}
			var c rdConn
			json.Unmarshal([]byte(spec), &c)
			a.db.Exec(`INSERT OR REPLACE INTO rd_connections(id, name, grp, spec, secret_enc, created_by, created_at) VALUES(?,?,?,?,?,?,?)`, p.ID, c.Name, c.Group, spec, enc, env.ActorName, time.Now().Unix())
			return nil, nil, nil
		},
	})
}

// GET /api/v1/remote/{id}/tunnel?width=&height=&dpi= — the Guacamole WebSocket.
func (a *App) handleRDTunnel(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	c, secret, err := a.rdConn(id)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	u := currentUser(r)
	if !c.mayUse(u) {
		a.fail(w, r, errs.New(403, "auth.forbidden", "perm", "remote.use"))
		return
	}
	q := r.URL.Query()
	width, _ := strconv.Atoi(q.Get("width"))
	height, _ := strconv.Atoi(q.Get("height"))
	dpi, _ := strconv.Atoi(q.Get("dpi"))
	width, height = clamp(width, 320, 7680), clamp(height, 240, 4320)
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{"guacamole"}})
	if err != nil {
		return
	}
	ctx := r.Context()
	conn, rd, cid, err := guac.Handshake(ctx, guacdAddr, guac.Params{Protocol: c.Protocol, Args: guacArgs(c, secret), Width: width, Height: height, DPI: dpi, Timezone: tzName(q.Get("tz"))})
	if err != nil {
		// tell guacamole-common-js why, as a Guacamole "error" (519 = upstream not found)
		ws.Write(ctx, websocket.MessageText, []byte(guac.Encode("", "failed")+guac.Encode("error", err.Error(), "519")))
		ws.Close(websocket.StatusNormalClosure, "")
		a.pipe.Record(currentActor(r), "local", "rd.open", map[string]any{"id": id, "name": c.Name, "error": err.Error()},
			change.Plan{Summary: "plan.rd.open_failed", SummaryP: map[string]any{"name": c.Name, "error": err.Error()}, Risk: change.RiskLow})
		return
	}
	a.db.Exec(`UPDATE rd_connections SET last_used=? WHERE id=?`, time.Now().Unix(), id)
	a.pipe.Record(currentActor(r), "local", "rd.open", map[string]any{"id": id, "name": c.Name, "target": net.JoinHostPort(c.Host, strconv.Itoa(c.Port))},
		change.Plan{Summary: "plan.rd.open", SummaryP: map[string]any{"name": c.Name, "protocol": c.Protocol}, Risk: change.RiskHigh})
	guac.Relay(ctx, ws, conn, rd, cid)
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// tzName keeps only something shaped like an IANA zone name.
func tzName(s string) string {
	if len(s) > 64 || !tzRe.MatchString(s) {
		return ""
	}
	return s
}

var tzRe = regexp.MustCompile(`^[A-Za-z_]+(/[A-Za-z0-9_+-]+){0,2}$`)
