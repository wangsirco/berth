package panel

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/wangsirco/berth/internal/change"
)

// Proxy activity (ported from Wharf): who is online from which addresses, and which sites
// each user reached through which node, from Xray's access log. Records are kept 30 days.

type proxyOnlineIP struct {
	IP       string `json:"ip"`
	LastSeen int64  `json:"last_seen"`
	Country  string `json:"country,omitempty"`
}

var proxyOnlineNow struct {
	sync.Mutex
	at    int64
	users map[int64][]proxyOnlineIP // user → addresses
	nodes map[int64]map[int64]bool  // user → nodes in use
}

const proxyAccessKeep = 30 * 24 * time.Hour

// proxyActivity pulls the online list and the new access log lines from the agent.
func (a *App) proxyActivity(ctx context.Context) {
	var out struct {
		Online map[string][]proxyOnlineIP `json:"online"`
		Access []struct {
			Email   string `json:"email"`
			IP      string `json:"ip"`
			Country string `json:"country"`
			Dest    string `json:"dest"`
			Port    int    `json:"port"`
			Net     string `json:"net"`
			Status  string `json:"status"`
			Count   int    `json:"count"`
			First   int64  `json:"first"`
			Last    int64  `json:"last"`
		} `json:"access"`
	}
	c, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if err := a.call(c, "local", "GET", "/v1/proxy/activity", nil, &out); err != nil {
		log.Printf("proxy: activity: %v", err)
		return
	}
	users, nodes := map[int64][]proxyOnlineIP{}, map[int64]map[int64]bool{}
	for email, ips := range out.Online {
		uid, nid, ok := parseProxyEmail(email)
		if !ok {
			continue
		}
		if nodes[uid] == nil {
			nodes[uid] = map[int64]bool{}
		}
		nodes[uid][nid] = true
		for _, ip := range ips {
			dup := false
			for i, x := range users[uid] {
				if x.IP == ip.IP {
					dup = true
					users[uid][i].LastSeen = max(x.LastSeen, ip.LastSeen)
				}
			}
			if !dup {
				users[uid] = append(users[uid], ip)
			}
		}
	}
	proxyOnlineNow.Lock()
	proxyOnlineNow.at, proxyOnlineNow.users, proxyOnlineNow.nodes = time.Now().Unix(), users, nodes
	proxyOnlineNow.Unlock()
	if len(out.Access) == 0 {
		return
	}
	tx, err := a.db.Begin()
	if err != nil {
		return
	}
	for _, x := range out.Access {
		uid, nid, _ := parseProxyEmail(x.Email)
		tx.Exec(`INSERT INTO proxy_access(ts, last, user_id, node_id, ip, country, dest, port, net, status, count) VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
			x.First, x.Last, uid, nid, x.IP, x.Country, x.Dest, x.Port, x.Net, x.Status, x.Count)
	}
	tx.Exec(`DELETE FROM proxy_access WHERE ts<?`, time.Now().Add(-proxyAccessKeep).Unix())
	tx.Commit()
}

func (a *App) handleProxyOnline(w http.ResponseWriter, r *http.Request) {
	proxyOnlineNow.Lock()
	defer proxyOnlineNow.Unlock()
	type row struct {
		UserID int64           `json:"user_id"`
		User   string          `json:"user"`
		Nodes  []int64         `json:"nodes"`
		IPs    []proxyOnlineIP `json:"ips"`
	}
	names := map[int64]string{}
	for _, u := range a.proxyUsers() {
		names[u.ID] = u.Name
	}
	out := []row{}
	for uid, ips := range proxyOnlineNow.users {
		x := row{UserID: uid, User: names[uid], IPs: ips, Nodes: []int64{}}
		for nid := range proxyOnlineNow.nodes[uid] {
			x.Nodes = append(x.Nodes, nid)
		}
		sort.Slice(x.Nodes, func(i, j int) bool { return x.Nodes[i] < x.Nodes[j] })
		sort.Slice(x.IPs, func(i, j int) bool { return x.IPs[i].LastSeen > x.IPs[j].LastSeen })
		out = append(out, x)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].User < out[j].User })
	writeJSON(w, 200, map[string]any{"items": out, "at": proxyOnlineNow.at})
}

func (a *App) handleProxyAccess(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	where, args := []string{"1=1"}, []any{}
	if id, _ := strconv.ParseInt(q.Get("user"), 10, 64); id > 0 {
		where, args = append(where, "user_id=?"), append(args, id)
	}
	if id, _ := strconv.ParseInt(q.Get("node"), 10, 64); id > 0 {
		where, args = append(where, "node_id=?"), append(args, id)
	}
	if before, _ := strconv.ParseInt(q.Get("before"), 10, 64); before > 0 {
		where, args = append(where, "id<?"), append(args, before)
	}
	if s := strings.TrimSpace(q.Get("q")); s != "" {
		where, args = append(where, "(dest LIKE ? OR ip LIKE ?)"), append(args, "%"+s+"%", "%"+s+"%")
	}
	if q.Get("status") == "blocked" {
		where = append(where, "status IN ('rejected','blocked')")
	}
	rows, err := a.db.Query(`SELECT id, ts, last, user_id, node_id, ip, country, dest, port, net, status, count FROM proxy_access WHERE `+
		strings.Join(where, " AND ")+` ORDER BY id DESC LIMIT 300`, args...)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	defer rows.Close()
	users, nodes := map[int64]string{}, map[int64]string{}
	for _, u := range a.proxyUsers() {
		users[u.ID] = u.Name
	}
	for _, n := range a.proxyNodes() {
		nodes[n.ID] = n.Name
	}
	type rec struct {
		ID      int64  `json:"id"`
		TS      int64  `json:"ts"`
		Last    int64  `json:"last"`
		UserID  int64  `json:"user_id"`
		User    string `json:"user"`
		NodeID  int64  `json:"node_id"`
		Node    string `json:"node"`
		IP      string `json:"ip"`
		Country string `json:"country"`
		Dest    string `json:"dest"`
		Port    int    `json:"port"`
		Net     string `json:"net"`
		Status  string `json:"status"`
		Count   int    `json:"count"`
	}
	out := []rec{}
	for rows.Next() {
		var x rec
		if rows.Scan(&x.ID, &x.TS, &x.Last, &x.UserID, &x.NodeID, &x.IP, &x.Country, &x.Dest, &x.Port, &x.Net, &x.Status, &x.Count) == nil {
			x.User, x.Node = users[x.UserID], nodes[x.NodeID]
			out = append(out, x)
		}
	}
	// the sites reached most in the last 24 hours
	top := []kvCount{}
	tr, err := a.db.Query(`SELECT dest, SUM(count) n FROM proxy_access WHERE ts>? AND status='accepted' GROUP BY dest ORDER BY n DESC LIMIT 15`, time.Now().Add(-24*time.Hour).Unix())
	if err == nil {
		for tr.Next() {
			var k kvCount
			tr.Scan(&k.K, &k.N)
			top = append(top, k)
		}
		tr.Close()
	}
	writeJSON(w, 200, map[string]any{"items": out, "top": top})
}

// ---- Xray versions ----

var xrayVersions struct {
	sync.Mutex
	at   time.Time
	list []xrayRelease
}

type xrayRelease struct {
	Tag        string `json:"tag"` // 26.11.9 (the image tag; GitHub's has a leading v)
	Prerelease bool   `json:"prerelease"`
	Date       string `json:"date"`
}

var xrayTagRe = regexp.MustCompile(`^\d+\.\d+\.\d+$`)

func (a *App) xrayReleases(ctx context.Context) []xrayRelease {
	xrayVersions.Lock()
	defer xrayVersions.Unlock()
	if time.Since(xrayVersions.at) < 6*time.Hour && len(xrayVersions.list) > 0 {
		return xrayVersions.list
	}
	req, _ := http.NewRequestWithContext(ctx, "GET", "https://api.github.com/repos/XTLS/Xray-core/releases?per_page=15", nil)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return xrayVersions.list
	}
	defer resp.Body.Close()
	var rel []struct {
		Tag        string `json:"tag_name"`
		Prerelease bool   `json:"prerelease"`
		Published  string `json:"published_at"`
	}
	if json.NewDecoder(resp.Body).Decode(&rel) != nil {
		return xrayVersions.list
	}
	var list []xrayRelease
	for _, r := range rel {
		tag := strings.TrimPrefix(r.Tag, "v")
		if xrayTagRe.MatchString(tag) {
			d := r.Published
			if len(d) > 10 {
				d = d[:10]
			}
			list = append(list, xrayRelease{Tag: tag, Prerelease: r.Prerelease, Date: d})
		}
	}
	xrayVersions.at, xrayVersions.list = time.Now(), list
	return list
}

func init() {
	routeHooks = append(routeHooks, func(a *App, mux *http.ServeMux) {
		mux.HandleFunc("GET /api/v1/proxy/online", a.authed("network.manage", a.handleProxyOnline))
		mux.HandleFunc("GET /api/v1/proxy/access", a.authed("network.manage", a.handleProxyAccess))
		mux.HandleFunc("GET /api/v1/proxy/versions", a.authed("network.manage", func(w http.ResponseWriter, r *http.Request) {
			ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
			defer cancel()
			writeJSON(w, 200, map[string]any{"items": a.xrayReleases(ctx), "current": firstNonEmpty(a.setting("proxy.version"), "latest")})
		}))
	})
	change.Register(change.Kind{
		Name: "proxy.version", Perm: "network.manage", Scope: "network:write", Title: "kind.proxy.version", CLI: "berth proxy version {{.version}}",
		Params: change.ParamSchema{Props: map[string]change.Prop{"version": {Type: "string", Desc: "Xray version, e.g. 26.11.9, or latest"}}, Required: []string{"version"}},
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			var p struct{ Version string }
			json.Unmarshal(raw, &p)
			if p.Version != "latest" && !xrayTagRe.MatchString(p.Version) {
				return change.Plan{}, pxErr("bad_version")
			}
			a := svc(env)
			return change.Plan{Summary: "plan.proxy.version", SummaryP: map[string]any{"from": firstNonEmpty(a.setting("proxy.version"), "latest"), "to": p.Version},
				Risk: change.RiskMedium, Undoable: true, Files: []change.FileDiff{{Path: "/srv/berth/stacks/" + proxyProject + "/compose.yaml", Before: proxyCompose(a.setting("proxy.version")), After: proxyCompose(p.Version)}},
				Affected: []change.Object{projObj(proxyProject, "local")}}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			var p struct{ Version string }
			json.Unmarshal(raw, &p)
			a := svc(env)
			old := firstNonEmpty(a.setting("proxy.version"), "latest")
			v := p.Version
			if v == "latest" {
				v = ""
			}
			a.setSetting("proxy.version", v)
			if a.proxyInstalled() {
				if err := a.call(env.Ctx, "local", "PUT", "/v1/compose/projects/"+proxyProject+"/files", map[string]any{"compose": proxyCompose(v), "env": ""}, nil); err != nil {
					a.setSetting("proxy.version", strings.TrimPrefix(old, "latest"))
					return nil, nil, err
				}
				env.Log("::step 1 pull %s", proxyImage(v))
				if err := agentOp(env, "/v1/compose/projects/"+proxyProject+"/pull", nil); err != nil {
					env.Log("pull: %v", err)
				}
				env.Log("::step 2 start")
				if err := agentOp(env, "/v1/compose/projects/"+proxyProject+"/up", nil); err != nil {
					return nil, nil, err
				}
			}
			u, _ := json.Marshal(map[string]string{"version": old})
			return nil, &change.UndoRecipe{Kind: "proxy.version", Params: u}, nil
		},
	})
}
