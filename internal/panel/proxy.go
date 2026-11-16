package panel

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/wangsirco/berth/internal/change"
	"github.com/wangsirco/berth/internal/errs"
)

// Proxy extension: Xray in a container next to the panel, for people whose only server is
// this one (a cloud server, a home server). Nodes (VLESS / VMess / Trojan / Shadowsocks /
// Hysteria 2 over REALITY, TLS or plain) and users (traffic quota, monthly reset, expiry,
// which nodes) live in the panel; every edit rebuilds the config and restarts xray.
// Users get a subscription link that works from anywhere.

const (
	proxyProject = "berth-proxy"
	proxyImage   = "ghcr.io/xtls/xray-core:latest"
)

func proxyCompose() string {
	return `# Berth: Xray proxy (managed by the panel's 代理 page; edit nodes and users there)
services:
  xray:
    image: ` + proxyImage + `
    restart: unless-stopped
    # host network: every node listens on its own port of this server
    network_mode: host
    command: ["run", "-c", "/etc/xray/config.json"]
    volumes:
      # z: SELinux lets the container read them
      - ./config.json:/etc/xray/config.json:ro,z
      - ./certs:/etc/xray/certs:ro,z
`
}

var proxyMu sync.Mutex // one rebuild at a time (changes and the traffic loop)

func (a *App) proxyInstalled() bool { return a.setting("proxy.installed") == "1" }

func (a *App) proxyNodes() []*ProxyNode {
	rows, err := a.db.Query(`SELECT id, spec FROM proxy_nodes ORDER BY id`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []*ProxyNode
	for rows.Next() {
		var id int64
		var spec string
		rows.Scan(&id, &spec)
		n := &ProxyNode{}
		if json.Unmarshal([]byte(spec), n) == nil {
			n.ID = id
			out = append(out, n)
		}
	}
	return out
}

func (a *App) proxyUsers() []*ProxyUser {
	rows, err := a.db.Query(`SELECT id, spec FROM proxy_users ORDER BY id`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []*ProxyUser
	for rows.Next() {
		var id int64
		var spec string
		rows.Scan(&id, &spec)
		u := &ProxyUser{}
		if json.Unmarshal([]byte(spec), u) == nil {
			u.ID = id
			if u.Nodes == nil {
				u.Nodes = []int64{}
			}
			out = append(out, u)
		}
	}
	return out
}

func (a *App) proxyNode(id int64) *ProxyNode {
	for _, n := range a.proxyNodes() {
		if n.ID == id {
			return n
		}
	}
	return nil
}

func (a *App) proxyUser(id int64) *ProxyUser {
	for _, u := range a.proxyUsers() {
		if u.ID == id {
			return u
		}
	}
	return nil
}

func (a *App) saveProxyNode(n *ProxyNode) error {
	now := time.Now().Unix()
	if n.Created == 0 {
		n.Created = now
	}
	n.Updated = now
	b, _ := json.Marshal(n)
	if n.ID == 0 {
		res, err := a.db.Exec(`INSERT INTO proxy_nodes(spec) VALUES(?)`, string(b))
		if err != nil {
			return err
		}
		n.ID, _ = res.LastInsertId()
		return nil
	}
	_, err := a.db.Exec(`INSERT INTO proxy_nodes(id, spec) VALUES(?,?) ON CONFLICT(id) DO UPDATE SET spec=excluded.spec`, n.ID, string(b))
	return err
}

func (a *App) saveProxyUser(u *ProxyUser) error {
	if u.Created == 0 {
		u.Created = time.Now().Unix()
	}
	b, _ := json.Marshal(u)
	if u.ID == 0 {
		res, err := a.db.Exec(`INSERT INTO proxy_users(spec) VALUES(?)`, string(b))
		if err != nil {
			return err
		}
		u.ID, _ = res.LastInsertId()
		return nil
	}
	_, err := a.db.Exec(`INSERT INTO proxy_users(id, spec) VALUES(?,?) ON CONFLICT(id) DO UPDATE SET spec=excluded.spec`, u.ID, string(b))
	return err
}

func (a *App) proxyPolicy() proxyPolicy {
	p := proxyPolicy{BlockBT: true}
	if s := a.setting("proxy.policy"); s != "" {
		json.Unmarshal([]byte(s), &p)
	}
	return p
}

// proxyHost: the address in links: the setting, else the panel's external address, else
// this server's public IP.
func (a *App) proxyHost(ctx context.Context) string {
	if h := a.setting("proxy.host"); h != "" {
		return h
	}
	if h := a.setting("panel.public_host"); h != "" {
		return h
	}
	if ip, err := a.publicIP(ctx, "local", "A"); err == nil && ip != "" {
		return ip
	}
	return "127.0.0.1"
}

// proxyApply writes the config and certificates and restarts xray (when installed).
func (a *App) proxyApply(ctx context.Context) error {
	if !a.proxyInstalled() {
		return nil
	}
	proxyMu.Lock()
	defer proxyMu.Unlock()
	nodes, users := a.proxyNodes(), a.proxyUsers()
	cfg, err := buildXray(nodes, users, a.proxyPolicy(), time.Now().Unix())
	if err != nil {
		return err
	}
	certs := map[string]string{}
	var site []string
	for _, n := range nodes {
		if !n.Enabled || n.Security != "tls" {
			continue
		}
		if n.Params.Cert == "site" {
			site = append(site, n.Domain)
		} else {
			certs[n.Domain+".crt"], certs[n.Domain+".key"] = n.Params.CertPEM, n.Params.KeyPEM
		}
	}
	if err := a.call(ctx, "local", "PUT", "/v1/proxy/files", map[string]any{"config": cfg, "certs": certs, "site_certs": site}, nil); err != nil {
		return err
	}
	var st struct {
		Running bool   `json:"running"`
		Log     string `json:"log"`
	}
	if err := a.call(ctx, "local", "POST", "/v1/proxy/restart", nil, &st); err != nil {
		return err
	}
	if !st.Running {
		return errs.New(502, "proxy.not_running", "log", lastLines(st.Log, 6))
	}
	return nil
}

// ---- traffic: counters every minute, quotas, expiry, monthly reset ----

func (a *App) proxyLoop(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if a.proxyInstalled() {
				a.proxyTick(ctx)
			}
		}
	}
}

func (a *App) proxyTick(ctx context.Context) {
	var out struct {
		Stats []struct {
			Name  string `json:"name"`
			Value int64  `json:"value,string"`
		} `json:"stats"`
	}
	c, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	_ = a.call(c, "local", "GET", "/v1/proxy/stats?reset=1", nil, &out)
	up, down := map[int64]int64{}, map[int64]int64{}
	for _, s := range out.Stats {
		parts := strings.Split(s.Name, ">>>") // user>>>u3@n1>>>traffic>>>uplink
		if len(parts) != 4 || parts[0] != "user" {
			continue
		}
		uid, _, ok := parseProxyEmail(parts[1])
		if !ok {
			continue
		}
		if parts[3] == "uplink" {
			up[uid] += s.Value
		} else {
			down[uid] += s.Value
		}
	}
	now := time.Now()
	changed := false
	for _, u := range a.proxyUsers() {
		dirty := false
		if up[u.ID] != 0 || down[u.ID] != 0 {
			u.UsedUp += up[u.ID]
			u.UsedDown += down[u.ID]
			dirty = true
		}
		// monthly reset on the chosen day
		if u.ResetDay > 0 {
			start := periodStart(now, u.ResetDay)
			if u.PeriodAt < start.Unix() {
				u.UsedUp, u.UsedDown, u.PeriodAt = 0, 0, start.Unix()
				if u.Stopped == "quota" {
					u.Stopped = ""
					changed = true
				}
				dirty = true
			}
		}
		stop := ""
		switch {
		case u.LimitBytes > 0 && u.UsedUp+u.UsedDown >= u.LimitBytes:
			stop = "quota"
		case u.ExpireAt > 0 && u.ExpireAt <= now.Unix():
			stop = "expired"
		}
		if stop != u.Stopped && (stop != "" || u.Stopped == "expired") {
			u.Stopped = stop
			dirty, changed = true, true
		}
		if dirty {
			a.saveProxyUser(u)
		}
	}
	if changed {
		_ = a.proxyApply(ctx)
	}
}

// periodStart: the latest day-th of a month at or before now.
func periodStart(now time.Time, day int) time.Time {
	t := time.Date(now.Year(), now.Month(), day, 0, 0, 0, 0, now.Location())
	if t.After(now) {
		t = t.AddDate(0, -1, 0)
	}
	return t
}

// ---- views ----

type proxyNodeView struct {
	*ProxyNode
	Pin string `json:"pin,omitempty"`
}

type proxyUserView struct {
	*ProxyUser
	SubURL string            `json:"sub_url"`
	Links  map[string]string `json:"links"` // node id → link
}

// nodeView hides the keys the browser never needs.
func nodeView(n *ProxyNode) proxyNodeView {
	c := *n
	c.Params.KeyPEM, c.Params.PrivateKey = "", ""
	v := proxyNodeView{ProxyNode: &c}
	if n.Params.Cert == "self" {
		v.Pin = certPin(n.Params.CertPEM)
	}
	return v
}

func (a *App) handleProxyGet(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	host := a.proxyHost(ctx)
	nodes, users := a.proxyNodes(), a.proxyUsers()
	nv := []proxyNodeView{}
	for _, n := range nodes {
		nv = append(nv, nodeView(n))
	}
	uv := []proxyUserView{}
	base := a.panelLinkBase(r)
	now := time.Now().Unix()
	for _, u := range users {
		v := proxyUserView{ProxyUser: u, SubURL: base + "/sub/" + u.SubToken, Links: map[string]string{}}
		for _, n := range nodes {
			if slices.Contains(u.Nodes, n.ID) && n.Enabled && u.active(now) {
				v.Links[strconv.FormatInt(n.ID, 10)] = shareLink(n, u, nodeHost(n, host))
			}
		}
		uv = append(uv, v)
	}
	out := map[string]any{"installed": a.proxyInstalled(), "host": host, "host_set": a.setting("proxy.host"), "policy": a.proxyPolicy(), "nodes": nv, "users": uv}
	if a.proxyInstalled() {
		var st map[string]any
		if a.call(ctx, "local", "GET", "/v1/proxy/status", nil, &st) == nil {
			out["status"] = st
		}
	}
	var fw struct {
		State struct {
			Firewalld bool     `json:"firewalld"`
			OpenPorts []string `json:"open_ports"`
		} `json:"state"`
	}
	if a.call(ctx, "local", "GET", "/v1/firewall", nil, &fw) == nil {
		out["firewall"] = fw.State
	}
	writeJSON(w, 200, out)
}

// GET /sub/{token}: a user's subscription (base64 links), for proxy clients anywhere.
func (a *App) handleProxySub(w http.ResponseWriter, r *http.Request) {
	tok := r.PathValue("token")
	ip := a.clientIP(r)
	if a.limiter.blocked("sub:" + ip) {
		blank404(w)
		return
	}
	for _, u := range a.proxyUsers() {
		if u.SubToken != "" && u.SubToken == tok && a.proxyInstalled() {
			ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
			defer cancel()
			body := subscription(a.proxyNodes(), u, a.proxyHost(ctx), time.Now().Unix())
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.Header().Set("Subscription-Userinfo", subInfo(u))
			w.Header().Set("Profile-Update-Interval", "12")
			w.Header().Set("Content-Disposition", `attachment; filename="berth.txt"`)
			w.Write([]byte(body))
			return
		}
	}
	a.limiter.record("sub:" + ip)
	blank404(w)
}

// ---- changes ----

type proxyNodeParams struct {
	ProxyNode
	Delete bool `json:"-"`
}

type proxyUserParams struct {
	ID         int64      `json:"id,omitempty"`
	Name       string     `json:"name"`
	Nodes      []int64    `json:"nodes"`
	LimitGB    float64    `json:"limit_gb"`
	ResetDay   int        `json:"reset_day"`
	ExpireAt   int64      `json:"expire_at"`
	AllowLAN   bool       `json:"allow_lan"`
	Enabled    *bool      `json:"enabled,omitempty"`
	Note       string     `json:"note"`
	NewSecret  bool       `json:"new_secret,omitempty"` // a fresh UUID and subscription link (the old ones stop working)
	ResetUsage bool       `json:"reset_usage,omitempty"`
	Restore    *ProxyUser `json:"restore,omitempty"` // undo: put this exact user back
}

func proxyApplyEnv(env change.Env) error {
	return svc(env).proxyApply(env.Ctx)
}

func init() {
	routeHooks = append(routeHooks, func(a *App, mux *http.ServeMux) {
		mux.HandleFunc("GET /api/v1/proxy", a.authed("network.manage", a.handleProxyGet))
		mux.HandleFunc("GET /sub/{token}", a.handleProxySub)
	})
	startHooks = append(startHooks, func(a *App, ctx context.Context) { go a.proxyLoop(ctx) })

	change.Register(change.Kind{
		Name: "proxy.install", Perm: "network.manage", Scope: "network:write", Title: "kind.proxy.install", CLI: "berth proxy install",
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			return change.Plan{Summary: "plan.proxy.install", SummaryP: map[string]any{"image": proxyImage}, Risk: change.RiskMedium, Undoable: true,
				Files:    []change.FileDiff{{Path: "/srv/berth/stacks/" + proxyProject + "/compose.yaml", After: proxyCompose()}},
				Affected: []change.Object{projObj(proxyProject, "local")}}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			a := svc(env)
			cfg, _ := buildXray(a.proxyNodes(), a.proxyUsers(), a.proxyPolicy(), time.Now().Unix())
			if err := a.call(env.Ctx, "local", "PUT", "/v1/proxy/files", map[string]any{"config": cfg}, nil); err != nil {
				return nil, nil, err
			}
			if err := a.call(env.Ctx, "local", "PUT", "/v1/compose/projects/"+proxyProject+"/files", map[string]any{"compose": proxyCompose(), "env": "", "create": true}, nil); err != nil && !strings.Contains(err.Error(), "exists") {
				return nil, nil, err
			}
			if err := agentOp(env, "/v1/compose/projects/"+proxyProject+"/up", nil); err != nil {
				return nil, nil, err
			}
			a.setSetting("proxy.installed", "1")
			a.relate("local", change.Object{Type: "host", ID: "local"}, projObj(proxyProject, "local"), "manages", "module")
			if err := a.proxyApply(env.Ctx); err != nil {
				return nil, nil, err
			}
			return nil, &change.UndoRecipe{Kind: "proxy.uninstall", Params: json.RawMessage(`{}`)}, nil
		},
	})
	change.Register(change.Kind{
		Name: "proxy.uninstall", Perm: "network.manage", Scope: "network:write", Title: "kind.proxy.uninstall", CLI: "berth proxy uninstall",
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			return change.Plan{Summary: "plan.proxy.uninstall", Risk: change.RiskMedium, Undoable: true, Affected: []change.Object{projObj(proxyProject, "local")}}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			a := svc(env)
			if err := agentOp(env, "/v1/compose/projects/"+proxyProject+"/remove", map[string]any{"volumes": false}); err != nil && !strings.Contains(err.Error(), "not found") {
				return nil, nil, err
			}
			a.setSetting("proxy.installed", "")
			return nil, &change.UndoRecipe{Kind: "proxy.install", Params: json.RawMessage(`{}`)}, nil
		},
	})
	nodeProps := map[string]change.Prop{"id": {Type: "integer"}, "name": {Type: "string"}, "protocol": {Type: "string", Enum: proxyProtocols},
		"transport": {Type: "string"}, "security": {Type: "string", Enum: []string{"reality", "tls", "none"}}, "port": {Type: "integer"},
		"domain": {Type: "string"}, "params": {Type: "object"}, "enabled": {Type: "boolean"}, "note": {Type: "string"}}
	change.Register(change.Kind{
		Name: "proxy.node.set", Perm: "network.manage", Scope: "network:write", Title: "kind.proxy.node.set", CLI: "berth proxy node set {{.name}}",
		Params: change.ParamSchema{Props: nodeProps, Required: []string{"name", "protocol", "port"}}, Redact: []string{"params"},
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			var n ProxyNode
			json.Unmarshal(raw, &n)
			a := svc(env)
			if n.ID != 0 {
				old := a.proxyNode(n.ID)
				if old == nil {
					return change.Plan{}, errs.New(404, "proxy.no_node")
				}
				keepSecrets(&n, old)
			}
			if err := n.normalize(); err != nil {
				return change.Plan{}, err
			}
			for _, o := range a.proxyNodes() {
				if o.ID != n.ID && o.Port == n.Port {
					return change.Plan{}, pxErr("port_taken", "port", n.Port, "name", o.Name)
				}
			}
			return change.Plan{Summary: "plan.proxy.node.set", SummaryP: map[string]any{"name": n.Name, "protocol": n.Protocol, "port": n.Port},
				Risk: change.RiskMedium, Undoable: true, Affected: []change.Object{{Type: "proxy_node", ID: strconv.FormatInt(n.ID, 10), Name: n.Name, Host: "local"}}}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			var n ProxyNode
			json.Unmarshal(raw, &n)
			a := svc(env)
			var undo *change.UndoRecipe
			if n.ID != 0 {
				old := a.proxyNode(n.ID)
				if old == nil {
					return nil, nil, errs.New(404, "proxy.no_node")
				}
				keepSecrets(&n, old)
				b, _ := json.Marshal(old)
				undo = &change.UndoRecipe{Kind: "proxy.node.set", Params: b}
			}
			if err := n.normalize(); err != nil {
				return nil, nil, err
			}
			if err := a.saveProxyNode(&n); err != nil {
				return nil, nil, err
			}
			if undo == nil {
				b, _ := json.Marshal(map[string]any{"id": n.ID})
				undo = &change.UndoRecipe{Kind: "proxy.node.delete", Params: b}
			}
			return nodeView(&n), undo, proxyApplyEnv(env)
		},
	})
	change.Register(change.Kind{
		Name: "proxy.node.delete", Perm: "network.manage", Scope: "network:write", Title: "kind.proxy.node.delete", CLI: "berth proxy node delete {{.id}}",
		Params: change.ParamSchema{Props: map[string]change.Prop{"id": {Type: "integer"}}, Required: []string{"id"}},
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			var p struct{ ID int64 }
			json.Unmarshal(raw, &p)
			n := svc(env).proxyNode(p.ID)
			if n == nil {
				return change.Plan{}, errs.New(404, "proxy.no_node")
			}
			return change.Plan{Summary: "plan.proxy.node.delete", SummaryP: map[string]any{"name": n.Name}, Risk: change.RiskMedium, Undoable: true,
				Affected: []change.Object{{Type: "proxy_node", ID: strconv.FormatInt(n.ID, 10), Name: n.Name, Host: "local"}}}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			var p struct{ ID int64 }
			json.Unmarshal(raw, &p)
			a := svc(env)
			n := a.proxyNode(p.ID)
			if n == nil {
				return nil, nil, errs.New(404, "proxy.no_node")
			}
			a.db.Exec(`DELETE FROM proxy_nodes WHERE id=?`, p.ID)
			b, _ := json.Marshal(n)
			return nil, &change.UndoRecipe{Kind: "proxy.node.set", Params: b}, proxyApplyEnv(env)
		},
	})
	userProps := map[string]change.Prop{"id": {Type: "integer"}, "name": {Type: "string"}, "nodes": {Type: "array"}, "limit_gb": {Type: "number"},
		"reset_day": {Type: "integer"}, "expire_at": {Type: "integer"}, "allow_lan": {Type: "boolean"}, "enabled": {Type: "boolean"}, "note": {Type: "string"},
		"new_secret": {Type: "boolean"}, "reset_usage": {Type: "boolean"}}
	change.Register(change.Kind{
		Name: "proxy.user.set", Perm: "network.manage", Scope: "network:write", Title: "kind.proxy.user.set", CLI: "berth proxy user set {{.name}}",
		Params: change.ParamSchema{Props: userProps, Required: []string{"name"}}, Redact: []string{"restore"},
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			var p proxyUserParams
			json.Unmarshal(raw, &p)
			if p.Restore == nil && proxyClean(p.Name, 40) == "" {
				return change.Plan{}, pxErr("need_name")
			}
			if p.ResetDay < 0 || p.ResetDay > 28 || p.LimitGB < 0 {
				return change.Plan{}, pxErr("bad_user")
			}
			return change.Plan{Summary: "plan.proxy.user.set", SummaryP: map[string]any{"name": p.Name}, Risk: change.RiskLow, Undoable: true,
				Affected: []change.Object{{Type: "proxy_user", ID: strconv.FormatInt(p.ID, 10), Name: p.Name, Host: "local"}}}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			var p proxyUserParams
			json.Unmarshal(raw, &p)
			a := svc(env)
			var u *ProxyUser
			var undo *change.UndoRecipe
			if p.Restore != nil {
				u = p.Restore
			} else {
				if p.ID != 0 {
					if u = a.proxyUser(p.ID); u == nil {
						return nil, nil, errs.New(404, "proxy.no_user")
					}
					old := *u
					b, _ := json.Marshal(proxyUserParams{ID: u.ID, Name: u.Name, Restore: &old})
					undo = &change.UndoRecipe{Kind: "proxy.user.set", Params: b}
				} else {
					u = &ProxyUser{Secret: newUUID(), SubToken: newSubToken(), Enabled: true, PeriodAt: time.Now().Unix()}
				}
				u.Name, u.Note, u.AllowLAN, u.ResetDay, u.ExpireAt = proxyClean(p.Name, 40), proxyClean(p.Note, 200), p.AllowLAN, p.ResetDay, p.ExpireAt
				u.LimitBytes = int64(p.LimitGB * (1 << 30))
				u.Nodes = []int64{}
				for _, id := range p.Nodes {
					if a.proxyNode(id) != nil && !slices.Contains(u.Nodes, id) {
						u.Nodes = append(u.Nodes, id)
					}
				}
				if p.Enabled != nil {
					u.Enabled = *p.Enabled
				}
				if p.NewSecret {
					u.Secret, u.SubToken = newUUID(), newSubToken()
				}
				if p.ResetUsage {
					u.UsedUp, u.UsedDown, u.PeriodAt = 0, 0, time.Now().Unix()
				}
				// limits that no longer apply let a paused user go again
				if u.Stopped == "quota" && (u.LimitBytes == 0 || u.UsedUp+u.UsedDown < u.LimitBytes) {
					u.Stopped = ""
				}
				if u.Stopped == "expired" && (u.ExpireAt == 0 || u.ExpireAt > time.Now().Unix()) {
					u.Stopped = ""
				}
			}
			if err := a.saveProxyUser(u); err != nil {
				return nil, nil, err
			}
			if undo == nil && p.Restore == nil {
				b, _ := json.Marshal(map[string]any{"id": u.ID})
				undo = &change.UndoRecipe{Kind: "proxy.user.delete", Params: b}
			}
			return map[string]any{"id": u.ID}, undo, proxyApplyEnv(env)
		},
	})
	change.Register(change.Kind{
		Name: "proxy.user.delete", Perm: "network.manage", Scope: "network:write", Title: "kind.proxy.user.delete", CLI: "berth proxy user delete {{.id}}",
		Params: change.ParamSchema{Props: map[string]change.Prop{"id": {Type: "integer"}}, Required: []string{"id"}},
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			var p struct{ ID int64 }
			json.Unmarshal(raw, &p)
			u := svc(env).proxyUser(p.ID)
			if u == nil {
				return change.Plan{}, errs.New(404, "proxy.no_user")
			}
			return change.Plan{Summary: "plan.proxy.user.delete", SummaryP: map[string]any{"name": u.Name}, Risk: change.RiskMedium, Undoable: true,
				Affected: []change.Object{{Type: "proxy_user", ID: strconv.FormatInt(u.ID, 10), Name: u.Name, Host: "local"}}}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			var p struct{ ID int64 }
			json.Unmarshal(raw, &p)
			a := svc(env)
			u := a.proxyUser(p.ID)
			if u == nil {
				return nil, nil, errs.New(404, "proxy.no_user")
			}
			a.db.Exec(`DELETE FROM proxy_users WHERE id=?`, p.ID)
			b, _ := json.Marshal(proxyUserParams{ID: u.ID, Name: u.Name, Restore: u})
			return nil, &change.UndoRecipe{Kind: "proxy.user.set", Params: b}, proxyApplyEnv(env)
		},
	})
	change.Register(change.Kind{
		Name: "proxy.settings", Perm: "network.manage", Scope: "network:write", Title: "kind.proxy.settings", CLI: "berth proxy settings",
		Params: change.ParamSchema{Props: map[string]change.Prop{"host": {Type: "string"}, "policy": {Type: "object"}}},
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			var p struct {
				Host string `json:"host"`
			}
			json.Unmarshal(raw, &p)
			if p.Host != "" && validPublicHost(p.Host) != nil && validLanHost(p.Host) != nil {
				return change.Plan{}, pxErr("bad_host")
			}
			return change.Plan{Summary: "plan.proxy.settings", Risk: change.RiskLow, Undoable: true}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			var p struct {
				Host   string       `json:"host"`
				Policy *proxyPolicy `json:"policy"`
			}
			json.Unmarshal(raw, &p)
			a := svc(env)
			old := map[string]any{"host": a.setting("proxy.host"), "policy": a.proxyPolicy()}
			a.setSetting("proxy.host", strings.TrimSpace(p.Host))
			if p.Policy != nil {
				b, _ := json.Marshal(p.Policy)
				a.setSetting("proxy.policy", string(b))
			}
			u, _ := json.Marshal(old)
			return nil, &change.UndoRecipe{Kind: "proxy.settings", Params: u}, proxyApplyEnv(env)
		},
	})
}

// keepSecrets: an edit from the browser carries no keys; keep the stored ones.
func keepSecrets(n, old *ProxyNode) {
	if n.Params.KeyPEM == "" && n.Params.CertPEM == old.Params.CertPEM {
		n.Params.KeyPEM = old.Params.KeyPEM
	}
	if n.Params.CertPEM == "" {
		n.Params.CertPEM, n.Params.KeyPEM = old.Params.CertPEM, old.Params.KeyPEM
	}
	if n.Params.PrivateKey == "" && n.Params.PublicKey == old.Params.PublicKey {
		n.Params.PrivateKey = old.Params.PrivateKey
	}
	if n.Created == 0 {
		n.Created = old.Created
	}
}
