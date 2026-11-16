package panel

// Bans and brute-force protection (方案 §5.12.3). A ban drops one address or network on
// one host (nft set with a timeout, so it expires on its own). Failed sign-ins (SSH from
// the agent's journal, the panel's own logins) are counted; too many in a window bans the
// source, for longer each time it comes back.

import (
	"context"
	"database/sql"
	"encoding/json"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/wangsirco/berth/internal/change"
	"github.com/wangsirco/berth/internal/errs"
)

type ban struct {
	ID        int64    `json:"id"`
	Host      string   `json:"host"`
	Addr      string   `json:"addr"`
	Reason    string   `json:"reason"`
	Source    string   `json:"source"` // manual | bruteforce | waf | wharf | cloudflare
	Layers    []string `json:"layers"` // local (later: wharf, cloudflare)
	CreatedBy string   `json:"created_by"`
	CreatedAt int64    `json:"created_at"`
	ExpiresAt int64    `json:"expires_at,omitempty"` // 0: permanent
	RevokedAt int64    `json:"revoked_at,omitempty"`
	Strike    int      `json:"strike,omitempty"` // which repeat offence (auto bans)
}

const banCols = `id, host, addr, reason, source, layers, created_by, created_at, expires_at, revoked_at, strike`

func scanBan(r interface{ Scan(...any) error }) (ban, error) {
	var b ban
	var layers string
	err := r.Scan(&b.ID, &b.Host, &b.Addr, &b.Reason, &b.Source, &layers, &b.CreatedBy, &b.CreatedAt, &b.ExpiresAt, &b.RevokedAt, &b.Strike)
	json.Unmarshal([]byte(layers), &b.Layers)
	return b, err
}

func (a *App) bans(q string, args ...any) []ban {
	rows, err := a.db.Query(`SELECT `+banCols+` FROM bans `+q, args...)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := []ban{}
	for rows.Next() {
		if b, err := scanBan(rows); err == nil {
			out = append(out, b)
		}
	}
	return out
}

const activeBan = `revoked_at = 0 AND (expires_at = 0 OR expires_at > ?)`

func (a *App) activeBans(host string) []ban {
	return a.bans(`WHERE host=? AND `+activeBan+` ORDER BY id DESC`, host, time.Now().Unix())
}

// normAddr accepts an address or a network and returns its canonical form.
func normAddr(s string) (string, error) {
	s = strings.TrimSpace(s)
	if ip := net.ParseIP(s); ip != nil {
		return ip.String(), nil
	}
	if _, n, err := net.ParseCIDR(s); err == nil {
		ones, bits := n.Mask.Size()
		if (bits == 32 && ones < 8) || (bits == 128 && ones < 16) {
			return "", errs.New(400, "ban.too_wide")
		}
		if ones == bits { // a /32 or /128 is one address
			return n.IP.String(), nil
		}
		return n.String(), nil
	}
	return "", errs.New(400, "ban.bad_addr", "addr", s)
}

// pushBans sends a host's active bans to its agent.
func (a *App) pushBans(ctx context.Context, host string) error {
	now := time.Now().Unix()
	m := map[string]int64{}
	for _, b := range a.activeBans(host) {
		if b.ExpiresAt == 0 {
			m[b.Addr] = -1
		} else if left := b.ExpiresAt - now; left > 0 {
			if cur, ok := m[b.Addr]; !ok || (cur > 0 && left > cur) {
				m[b.Addr] = left
			}
		}
	}
	// nothing banned now or before: leave the agent's set alone
	key := "security.bans_pushed." + host
	if len(m) == 0 && a.setting(key) == "" {
		return nil
	}
	if err := a.call(ctx, host, "POST", "/v1/firewall/bans", m, nil); err != nil {
		return err
	}
	if len(m) > 0 {
		a.setSetting(key, "1")
	} else {
		a.setSetting(key, "")
	}
	return nil
}

func init() {
	fwPushBansHook = func(a *App, ctx context.Context, host string) { a.pushBans(ctx, host) }
}

// ---- brute force ----

// whitelisted: never banned automatically (loopback, the configured list).
func (a *App) whitelisted(ip net.IP) bool {
	if ip == nil || ip.IsLoopback() || ip.IsUnspecified() {
		return true
	}
	for _, c := range splitList(a.setting("security.bf_whitelist")) {
		if _, n, err := net.ParseCIDR(c); err == nil && n.Contains(ip) {
			return true
		}
		if x := net.ParseIP(c); x != nil && x.Equal(ip) {
			return true
		}
	}
	return false
}

// banLadder: 10 minutes, 1 hour, 1 day, then 7 days for repeat offenders (30-day memory).
var banLadder = []time.Duration{10 * time.Minute, time.Hour, 24 * time.Hour, 7 * 24 * time.Hour}

func (a *App) recordFailures(host, kind string, fs []authFailure) {
	for _, f := range fs {
		if net.ParseIP(f.IP) == nil {
			continue
		}
		a.db.Exec(`INSERT INTO auth_failures(host, ip, kind, user, ts, count) VALUES(?,?,?,?,?,?)`, host, f.IP, kind, truncate(f.User, 64), f.TS, max(f.Count, 1))
	}
}

type authFailure struct {
	TS    int64  `json:"ts"`
	IP    string `json:"ip"`
	User  string `json:"user"`
	Kind  string `json:"kind"`
	Count int    `json:"count"`
}

// judge bans sources over the threshold on host.
func (a *App) judge(ctx context.Context, host string) {
	if a.setting("security.bf_enabled") == "0" {
		return
	}
	attempts := a.settingInt("security.bf_attempts", 5)
	window := time.Duration(a.settingInt("security.bf_window_min", 10)) * time.Minute
	now := time.Now()
	rows, err := a.db.Query(`SELECT ip, SUM(count), GROUP_CONCAT(DISTINCT kind) FROM auth_failures WHERE host=? AND ts > ? GROUP BY ip HAVING SUM(count) >= ?`,
		host, now.Add(-window).Unix(), attempts)
	if err != nil {
		return
	}
	type hit struct {
		ip    string
		n     int
		kinds string
	}
	var hits []hit
	for rows.Next() {
		var h hit
		rows.Scan(&h.ip, &h.n, &h.kinds)
		hits = append(hits, h)
	}
	rows.Close()
	changed := false
	for _, h := range hits {
		ip := net.ParseIP(h.ip)
		if a.whitelisted(ip) {
			continue
		}
		var n int
		a.db.QueryRow(`SELECT COUNT(*) FROM bans WHERE host=? AND addr=? AND `+activeBan, host, ip.String(), now.Unix()).Scan(&n)
		if n > 0 {
			continue
		}
		var strikes int
		a.db.QueryRow(`SELECT COUNT(*) FROM bans WHERE host=? AND addr=? AND source IN ('bruteforce','waf') AND created_at > ?`, host, ip.String(), now.AddDate(0, 0, -30).Unix()).Scan(&strikes)
		d := banLadder[min(strikes, len(banLadder)-1)]
		source := "bruteforce"
		if h.kinds == "waf" {
			source = "waf"
		}
		reason := source + ":" + h.kinds + ":" + strconv.Itoa(h.n)
		res, err := a.db.Exec(`INSERT INTO bans(host, addr, reason, source, layers, created_by, created_at, expires_at, strike) VALUES(?,?,?,?,?,?,?,?,?)`,
			host, ip.String(), reason, source, `["local"]`, "berth", now.Unix(), now.Add(d).Unix(), strikes+1)
		if err != nil {
			continue
		}
		id, _ := res.LastInsertId()
		changed = true
		// on record, like any other change
		params := map[string]any{"addr": ip.String(), "minutes": int(d / time.Minute), "reason": reason}
		a.pipe.Record(change.Actor{Type: "system", ID: "bruteforce", Name: "berth", Via: "system", IP: "127.0.0.1"}, host, "ban.add", params, change.Plan{
			Summary: "plan.ban.auto", SummaryP: map[string]any{"addr": ip.String(), "count": h.n, "kinds": h.kinds, "minutes": int(d / time.Minute)}, Risk: change.RiskLow})
		if r, ok := a.alertRule("ban_added"); ok {
			a.once(r, strconv.FormatInt(id, 10), host, map[string]any{"addr": ip.String(), "count": h.n, "kinds": h.kinds, "minutes": int(d / time.Minute)})
		}
	}
	if changed {
		a.pushBans(ctx, host)
		a.hub.publish(event{Type: "ban", Data: map[string]any{"host": host}})
	}
}

// securityLoop collects failures, judges, and keeps every agent's ban set in step.
func (a *App) securityLoop(ctx context.Context) {
	t := time.NewTicker(15 * time.Second)
	defer t.Stop()
	var tick int
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		tick++
		c, cancel := context.WithTimeout(ctx, 12*time.Second)
		a.collectLoginFailures()
		if tick%4 == 1 {
			a.cfBanSweep(c)
			a.wharfBanSweep(c)
		}
		for _, h := range a.connectedHosts() {
			a.collectSSHFailures(c, h)
			a.collectWAF(c, h)
			a.judge(c, h)
			if tick%4 == 0 { // every minute: rollbacks and reboots drop the set's contents
				a.pushBans(c, h)
			}
		}
		if tick%240 == 0 {
			a.db.Exec(`DELETE FROM auth_failures WHERE ts < ?`, time.Now().Add(-48*time.Hour).Unix())
			a.pruneWAF()
			a.db.Exec(`DELETE FROM wharf_nonces WHERE seen_at < ?`, time.Now().Add(-10*time.Minute).Unix())
		}
		cancel()
	}
}

func (a *App) collectSSHFailures(ctx context.Context, host string) {
	key := "security.cursor." + host
	var res struct {
		Failures []authFailure `json:"failures"`
		Cursor   string        `json:"cursor"`
	}
	if err := a.call(ctx, host, "GET", "/v1/security/failures?cursor="+url.QueryEscape(a.setting(key)), nil, &res); err != nil {
		return
	}
	a.recordFailures(host, "ssh", res.Failures)
	if res.Cursor != "" {
		a.setSetting(key, res.Cursor)
	}
}

// collectLoginFailures counts failed panel sign-ins (on the panel's own host).
func (a *App) collectLoginFailures() {
	last := int64(a.settingInt("security.login_last_id", 0))
	rows, err := a.db.Query(`SELECT id, ts, ip, username FROM logins WHERE id > ? AND ok = 0 AND reason NOT IN ('rate_limited', 'second_step') ORDER BY id LIMIT 5000`, last)
	if err != nil {
		return
	}
	var fs []authFailure
	for rows.Next() {
		var f authFailure
		rows.Scan(&last, &f.TS, &f.IP, &f.User)
		f.Count = 1
		fs = append(fs, f)
	}
	rows.Close()
	if len(fs) > 0 {
		a.recordFailures("local", "panel", fs)
		a.setSetting("security.login_last_id", strconv.FormatInt(last, 10))
	} else if last == 0 { // first run: start from now, not from history
		var max sql.NullInt64
		a.db.QueryRow(`SELECT MAX(id) FROM logins`).Scan(&max)
		a.setSetting("security.login_last_id", strconv.FormatInt(max.Int64, 10))
	}
}

// ---- API and kinds ----

func init() {
	routeHooks = append(routeHooks, func(a *App, mux *http.ServeMux) {
		mux.HandleFunc("GET /api/v1/hosts/{host}/bans", a.authed("network.view", func(w http.ResponseWriter, r *http.Request) {
			host := r.PathValue("host")
			now := time.Now().Unix()
			recent := a.bans(`WHERE host=? AND NOT (`+activeBan+`) ORDER BY id DESC LIMIT 50`, host, now)
			type top struct {
				IP    string `json:"ip"`
				Count int    `json:"count"`
				Kinds string `json:"kinds"`
				Last  int64  `json:"last"`
			}
			tops := []top{}
			rows, err := a.db.Query(`SELECT ip, SUM(count), GROUP_CONCAT(DISTINCT kind), MAX(ts) FROM auth_failures WHERE host=? AND ts > ? GROUP BY ip ORDER BY SUM(count) DESC LIMIT 20`, host, now-86400)
			if err == nil {
				for rows.Next() {
					var t top
					rows.Scan(&t.IP, &t.Count, &t.Kinds, &t.Last)
					tops = append(tops, t)
				}
				rows.Close()
			}
			writeJSON(w, 200, map[string]any{"active": a.activeBans(host), "recent": recent, "failures": tops, "your_ip": a.clientIP(r),
				"settings": map[string]string{
					"enabled":    firstNonEmpty(a.setting("security.bf_enabled"), "1"),
					"attempts":   strconv.Itoa(a.settingInt("security.bf_attempts", 5)),
					"window":     strconv.Itoa(a.settingInt("security.bf_window_min", 10)),
					"whitelist":  a.setting("security.bf_whitelist"),
					"waf_ban":    firstNonEmpty(a.setting("security.waf_ban"), "0"),
					"wharf_sync": map[bool]string{true: "1", false: "0"}[a.wharfLinked()],
					"cf_sync":    map[bool]string{true: "1", false: "0"}[a.setting("cloudflare.ban_integration") != ""],
				}})
		}))
	})

	change.Register(change.Kind{
		Name: "ban.add", Perm: "network.manage", Scope: "system:write", Title: "kind.ban.add",
		Params: change.ParamSchema{Props: map[string]change.Prop{"addr": {Type: "string", Desc: "IP or network"}, "minutes": {Type: "integer", Desc: "0 = permanent"},
			"reason": {Type: "string"}, "id": {Type: "integer"}}, Required: []string{"addr"}},
		CLI: "berth ban add {{.addr}}",
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			var p struct {
				Addr    string
				Minutes int
				Reason  string
			}
			json.Unmarshal(raw, &p)
			addr, err := normAddr(p.Addr)
			if err != nil {
				return change.Plan{}, err
			}
			if p.Minutes < 0 {
				return change.Plan{}, errs.New(400, "ban.bad_addr", "addr", p.Addr)
			}
			sum := "plan.ban.add"
			if ip := net.ParseIP(env.ActorIP); ip != nil && banCovers(addr, ip) {
				sum = "plan.ban.add_self"
			}
			return change.Plan{Summary: sum, SummaryP: map[string]any{"addr": addr, "minutes": p.Minutes}, Risk: change.RiskMedium, Undoable: true,
				Commands: []string{"nft add element inet berth ban_v4 { " + addr + " }"}}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			a := svc(env)
			var p struct {
				Addr    string
				Minutes int
				Reason  string
				ID      int64
			}
			json.Unmarshal(raw, &p)
			addr, _ := normAddr(p.Addr)
			var exp int64
			if p.Minutes > 0 {
				exp = time.Now().Add(time.Duration(p.Minutes) * time.Minute).Unix()
			}
			var id int64
			if p.ID != 0 { // undoing a removal: revive the same ban
				a.db.Exec(`UPDATE bans SET revoked_at=0, expires_at=? WHERE id=? AND host=?`, exp, p.ID, env.Host)
				id = p.ID
			} else {
				res, err := a.db.Exec(`INSERT INTO bans(host, addr, reason, source, layers, created_by, created_at, expires_at) VALUES(?,?,?,?,?,?,?,?)`,
					env.Host, addr, truncate(p.Reason, 200), "manual", `["local"]`, env.ActorName, time.Now().Unix(), exp)
				if err != nil {
					return nil, nil, err
				}
				id, _ = res.LastInsertId()
			}
			if err := a.pushBans(env.Ctx, env.Host); err != nil {
				a.db.Exec(`UPDATE bans SET revoked_at=? WHERE id=?`, time.Now().Unix(), id)
				return nil, nil, err
			}
			a.hub.publish(event{Type: "ban", Data: map[string]any{"host": env.Host}})
			u, _ := json.Marshal(map[string]int64{"id": id})
			return map[string]int64{"id": id}, &change.UndoRecipe{Kind: "ban.remove", Params: u}, nil
		},
	})

	change.Register(change.Kind{
		Name: "ban.remove", Perm: "network.manage", Scope: "system:write", Title: "kind.ban.remove",
		Params: change.ParamSchema{Props: map[string]change.Prop{"id": {Type: "integer"}}, Required: []string{"id"}}, CLI: "berth ban remove {{.id}}",
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			var p struct{ ID int64 }
			json.Unmarshal(raw, &p)
			b, err := scanBan(svc(env).db.QueryRow(`SELECT `+banCols+` FROM bans WHERE id=? AND host=?`, p.ID, env.Host))
			if err != nil {
				return change.Plan{}, errs.New(404, "ban.not_found")
			}
			return change.Plan{Summary: "plan.ban.remove", SummaryP: map[string]any{"addr": b.Addr}, Risk: change.RiskLow, Undoable: true,
				Commands: []string{"nft delete element inet berth ban_v4 { " + b.Addr + " }"}}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			a := svc(env)
			var p struct{ ID int64 }
			json.Unmarshal(raw, &p)
			b, err := scanBan(a.db.QueryRow(`SELECT `+banCols+` FROM bans WHERE id=? AND host=?`, p.ID, env.Host))
			if err != nil {
				return nil, nil, errs.New(404, "ban.not_found")
			}
			a.db.Exec(`UPDATE bans SET revoked_at=? WHERE id=?`, time.Now().Unix(), p.ID)
			// forget the failures too, or the source is banned again on the next pass
			a.db.Exec(`DELETE FROM auth_failures WHERE host=? AND ip=?`, env.Host, b.Addr)
			if err := a.pushBans(env.Ctx, env.Host); err != nil {
				a.db.Exec(`UPDATE bans SET revoked_at=0 WHERE id=?`, p.ID)
				return nil, nil, err
			}
			a.hub.publish(event{Type: "ban", Data: map[string]any{"host": env.Host}})
			mins := 0
			if b.ExpiresAt > 0 {
				mins = max(1, int(time.Until(time.Unix(b.ExpiresAt, 0)).Minutes()))
			}
			u, _ := json.Marshal(map[string]any{"id": b.ID, "addr": b.Addr, "minutes": mins, "reason": b.Reason})
			return nil, &change.UndoRecipe{Kind: "ban.add", Params: u}, nil
		},
	})
}

func banCovers(addr string, ip net.IP) bool {
	if x := net.ParseIP(addr); x != nil {
		return x.Equal(ip)
	}
	_, n, err := net.ParseCIDR(addr)
	return err == nil && n.Contains(ip)
}

func (a *App) wharfLinked() bool {
	l, err := a.wharf()
	return err == nil && l.has("bans")
}
