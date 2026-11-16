package panel

// Traffic (方案 §5.12.2): per-container bytes by the hour (from the agent's counters),
// sites (from the access logs), live connections, bandwidth limits and monthly quotas.

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"sync"
	"time"

	"github.com/wangsirco/berth/internal/agent/traffic"
	"github.com/wangsirco/berth/internal/change"
	"github.com/wangsirco/berth/internal/errs"
)

// ---- counting ----

type ctrCount struct {
	id     string
	rx, tx uint64
}

var trafficLast sync.Map // host -> map[name]ctrCount

func hourOf(t time.Time) int64 { return t.Unix() / 3600 * 3600 }

// sampleTraffic adds what each container moved since the last sample to this hour.
func (a *App) sampleTraffic(ctx context.Context, host string) {
	var cs []traffic.Counters
	if err := a.call(ctx, host, "GET", "/v1/traffic/containers", nil, &cs); err != nil {
		return
	}
	v, _ := trafficLast.LoadOrStore(host, map[string]ctrCount{})
	last := v.(map[string]ctrCount)
	next := map[string]ctrCount{}
	hour := hourOf(time.Now())
	for _, c := range cs {
		next[c.Name] = ctrCount{c.ID, c.RX, c.TX}
		prev, seen := last[c.Name]
		if !seen {
			continue // first look: a baseline, not traffic
		}
		drx, dtx := c.RX-prev.rx, c.TX-prev.tx
		if prev.id != c.ID || c.RX < prev.rx || c.TX < prev.tx { // recreated or restarted: counters began again
			drx, dtx = c.RX, c.TX
		}
		if drx == 0 && dtx == 0 {
			continue
		}
		a.db.Exec(`INSERT INTO traffic_hourly(host, key, hour, rx, tx) VALUES(?,?,?,?,?)
			ON CONFLICT(host, key, hour) DO UPDATE SET rx=rx+excluded.rx, tx=tx+excluded.tx`, host, c.Name, hour, drx, dtx)
	}
	trafficLast.Store(host, next)
}

// ---- limits and quotas ----

type trafficLimit struct {
	Container   string `json:"container"`
	UpKbit      int    `json:"up_kbit,omitempty"`
	DownKbit    int    `json:"down_kbit,omitempty"`
	QuotaGB     int    `json:"quota_gb,omitempty"`     // per calendar month, both directions
	QuotaAction string `json:"quota_action,omitempty"` // limit | stop
	QuotaKbit   int    `json:"quota_kbit,omitempty"`   // the speed once over quota (action limit)
	State       string `json:"state,omitempty"`        // ok | warned | throttled | stopped
	Month       string `json:"month,omitempty"`        // the month State belongs to
	UsedBytes   int64  `json:"used_bytes"`
}

var ctrNameRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,127}$`)

func (l trafficLimit) validate() error {
	if !ctrNameRe.MatchString(l.Container) {
		return errs.New(400, "traffic.bad_limit")
	}
	if l.UpKbit < 0 || l.DownKbit < 0 || l.UpKbit > 100_000_000 || l.DownKbit > 100_000_000 || l.QuotaGB < 0 || l.QuotaGB > 1_000_000 {
		return errs.New(400, "traffic.bad_limit")
	}
	if l.QuotaGB > 0 {
		switch l.QuotaAction {
		case "stop":
		case "limit":
			if l.QuotaKbit <= 0 {
				return errs.New(400, "traffic.bad_limit")
			}
		default:
			return errs.New(400, "traffic.bad_limit")
		}
	}
	if l.UpKbit == 0 && l.DownKbit == 0 && l.QuotaGB == 0 {
		return errs.New(400, "traffic.bad_limit")
	}
	return nil
}

func (a *App) trafficLimits(host string) []trafficLimit {
	rows, err := a.db.Query(`SELECT spec, state, month FROM traffic_limits WHERE host=? ORDER BY container`, host)
	if err != nil {
		return nil
	}
	out := []trafficLimit{}
	for rows.Next() {
		var spec string
		var l trafficLimit
		var st, m string
		rows.Scan(&spec, &st, &m)
		json.Unmarshal([]byte(spec), &l)
		l.State, l.Month = st, m
		out = append(out, l)
	}
	rows.Close()
	start := monthStart(time.Now())
	for i := range out {
		a.db.QueryRow(`SELECT COALESCE(SUM(rx+tx),0) FROM traffic_hourly WHERE host=? AND key=? AND hour>=?`, host, out[i].Container, start).Scan(&out[i].UsedBytes)
	}
	return out
}

func monthStart(t time.Time) int64 {
	y, m, _ := t.Date()
	return time.Date(y, m, 1, 0, 0, 0, 0, t.Location()).Unix()
}

// enforceLimits checks quotas and puts the effective limits on the agent.
func (a *App) enforceLimits(ctx context.Context, host string) {
	limits := a.trafficLimits(host)
	month := time.Now().Format("2006-01")
	var eff []traffic.Limit
	for _, l := range limits {
		st := l.State
		if l.Month != month { // a new month: quotas start over
			if st == "stopped" {
				a.call(ctx, host, "POST", "/v1/docker/containers/"+l.Container+"/start", nil, nil)
				a.expectRunning(host, l.Container)
			}
			st = "ok"
		}
		if l.QuotaGB > 0 {
			quota := int64(l.QuotaGB) << 30
			switch {
			case l.UsedBytes >= quota && st != "throttled" && st != "stopped":
				if l.QuotaAction == "stop" {
					a.expectStopped(host, l.Container)
					a.call(ctx, host, "POST", "/v1/docker/containers/"+l.Container+"/stop", nil, nil)
					st = "stopped"
				} else {
					st = "throttled"
				}
				if r, ok := a.alertRule("traffic_quota"); ok {
					a.once(r, host+"/"+l.Container+"/"+month+"/over", host, map[string]any{"container": l.Container, "gb": l.QuotaGB, "pct": 100, "action": l.QuotaAction})
				}
			case l.UsedBytes >= quota*8/10 && st == "ok":
				st = "warned"
				if r, ok := a.alertRule("traffic_quota"); ok {
					a.once(r, host+"/"+l.Container+"/"+month+"/80", host, map[string]any{"container": l.Container, "gb": l.QuotaGB, "pct": 80, "action": l.QuotaAction})
				}
			}
		}
		if st != l.State || l.Month != month {
			a.db.Exec(`UPDATE traffic_limits SET state=?, month=? WHERE host=? AND container=?`, st, month, host, l.Container)
		}
		up, down := l.UpKbit, l.DownKbit
		if st == "throttled" {
			up, down = minPos(up, l.QuotaKbit), minPos(down, l.QuotaKbit)
		}
		if up > 0 || down > 0 {
			eff = append(eff, traffic.Limit{Container: l.Container, UpKbit: up, DownKbit: down})
		}
	}
	// nothing to keep and nothing left to clear: leave the agent alone
	key := "traffic.pushed." + host
	if len(eff) == 0 && a.setting(key) == "" {
		return
	}
	if eff == nil {
		eff = []traffic.Limit{}
	}
	if len(eff) > 0 {
		a.setSetting(key, "1")
	} else {
		a.setSetting(key, "")
	}
	var res struct {
		Errors map[string]string `json:"errors"`
	}
	if err := a.call(ctx, host, "POST", "/v1/traffic/limits", eff, &res); err == nil {
		b, _ := json.Marshal(res.Errors)
		a.setSetting("traffic.limit_errors."+host, string(b))
	}
}

func minPos(a, b int) int {
	if a == 0 {
		return b
	}
	if b == 0 || a < b {
		return a
	}
	return b
}

func (a *App) trafficLoop(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			for _, h := range a.connectedHosts() {
				c, cancel := context.WithTimeout(ctx, 30*time.Second)
				a.sampleTraffic(c, h)
				a.enforceLimits(c, h)
				cancel()
			}
			a.checkUserTraffic()
			if now.Minute() == 7 {
				a.db.Exec(`DELETE FROM traffic_hourly WHERE hour < ?`, now.AddDate(0, 0, -400).Unix())
			}
		}
	}
}

// ---- API ----

type trafficPoint struct {
	T  int64 `json:"t"`
	RX int64 `json:"rx"`
	TX int64 `json:"tx"`
}

func rangeStart(r string, now time.Time) (int64, int64) { // start, bucket size
	switch r {
	case "7d":
		return now.Add(-7 * 24 * time.Hour).Unix(), 3600 * 6
	case "30d":
		return now.Add(-30 * 24 * time.Hour).Unix(), 86400
	case "month":
		return monthStart(now), 86400
	}
	return now.Add(-24 * time.Hour).Unix(), 3600
}

func init() {
	routeHooks = append(routeHooks, func(a *App, mux *http.ServeMux) {
		mux.HandleFunc("GET /api/v1/hosts/{host}/traffic", a.authed("network.view", func(w http.ResponseWriter, r *http.Request) {
			host := r.PathValue("host")
			start, bucket := rangeStart(r.URL.Query().Get("range"), time.Now())
			type rank struct {
				Name string `json:"name"`
				RX   int64  `json:"rx"`
				TX   int64  `json:"tx"`
			}
			ctrs := []rank{}
			rows, err := a.db.Query(`SELECT key, SUM(rx), SUM(tx) FROM traffic_hourly WHERE host=? AND hour>=? GROUP BY key ORDER BY SUM(rx+tx) DESC LIMIT 50`, host, start-start%3600)
			if err == nil {
				for rows.Next() {
					var x rank
					rows.Scan(&x.Name, &x.RX, &x.TX)
					ctrs = append(ctrs, x)
				}
				rows.Close()
			}
			// every running container is listed (so limits can be set before any traffic)
			seen := map[string]bool{}
			for _, c := range ctrs {
				seen[c.Name] = true
			}
			if cs, err := a.containers(r.Context(), host); err == nil {
				for _, c := range cs {
					if c.State == "running" && !seen[c.Name] {
						ctrs = append(ctrs, rank{Name: c.Name})
						seen[c.Name] = true
					}
				}
			}
			trend := []trafficPoint{}
			rows, err = a.db.Query(`SELECT (hour/?)*?, SUM(rx), SUM(tx) FROM traffic_hourly WHERE host=? AND hour>=? GROUP BY 1 ORDER BY 1`, bucket, bucket, host, start-start%3600)
			if err == nil {
				for rows.Next() {
					var p trafficPoint
					rows.Scan(&p.T, &p.RX, &p.TX)
					trend = append(trend, p)
				}
				rows.Close()
			}
			type siteRank struct {
				Site  string `json:"site"`
				Bytes int64  `json:"bytes"`
			}
			sites := []siteRank{}
			rows, err = a.db.Query(`SELECT site, SUM(json_extract(data,'$.bytes')) FROM site_stats_hour WHERE host=? AND ts>=? GROUP BY site ORDER BY 2 DESC LIMIT 50`, host, start-start%3600)
			if err == nil {
				for rows.Next() {
					var x siteRank
					rows.Scan(&x.Site, &x.Bytes)
					sites = append(sites, x)
				}
				rows.Close()
			}
			var tools map[string]bool
			a.call(r.Context(), host, "GET", "/v1/traffic/tools", nil, &tools)
			var limErr map[string]string
			json.Unmarshal([]byte(a.setting("traffic.limit_errors."+host)), &limErr)
			writeJSON(w, 200, map[string]any{"containers": ctrs, "trend": trend, "bucket": bucket, "sites": sites,
				"limits": a.trafficLimits(host), "limit_errors": limErr, "tools": tools})
		}))
		mux.HandleFunc("GET /api/v1/hosts/{host}/traffic/live", a.authed("network.view", func(w http.ResponseWriter, r *http.Request) {
			var s traffic.Summary
			if err := a.call(r.Context(), r.PathValue("host"), "GET", "/v1/traffic/live", nil, &s); err != nil {
				a.fail(w, r, err)
				return
			}
			writeJSON(w, 200, s)
		}))
		mux.HandleFunc("GET /api/v1/hosts/{host}/traffic.csv", a.authed("network.view", func(w http.ResponseWriter, r *http.Request) {
			host := r.PathValue("host")
			start, _ := rangeStart(r.URL.Query().Get("range"), time.Now())
			rows, err := a.db.Query(`SELECT hour, key, rx, tx FROM traffic_hourly WHERE host=? AND hour>=? ORDER BY hour, key`, host, start-start%3600)
			if err != nil {
				a.fail(w, r, err)
				return
			}
			defer rows.Close()
			w.Header().Set("Content-Type", "text/csv; charset=utf-8")
			w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="traffic-%s-%s.csv"`, host, time.Now().Format("20060102")))
			cw := csv.NewWriter(w)
			cw.Write([]string{"hour_utc", "container", "rx_bytes", "tx_bytes"})
			for rows.Next() {
				var h, rx, tx int64
				var k string
				rows.Scan(&h, &k, &rx, &tx)
				cw.Write([]string{time.Unix(h, 0).UTC().Format("2006-01-02 15:00"), k, strconv.FormatInt(rx, 10), strconv.FormatInt(tx, 10)})
			}
			cw.Flush()
		}))
	})

	change.Register(change.Kind{
		Name: "traffic.tools.install", Perm: "network.manage", Scope: "system:write", Title: "kind.traffic.tools.install",
		Params: change.ParamSchema{Props: map[string]change.Prop{}}, CLI: "berth traffic tools install", Heavy: true, MemMB: 64,
		Plan: func(env change.Env, _ json.RawMessage) (change.Plan, error) {
			return change.Plan{Summary: "plan.traffic.tools.install", Risk: change.RiskLow,
				Commands: []string{"dnf install -y iproute-tc", "sysctl -w net.netfilter.nf_conntrack_acct=1", "# and " + traffic.SysctlFile}}, nil
		},
		Apply: func(env change.Env, _ json.RawMessage) (any, *change.UndoRecipe, error) {
			var out map[string]bool
			return out, nil, svc(env).streamAgent(env, env.Host, "/v1/traffic/tools/install", nil, &out)
		},
	})

	limProps := map[string]change.Prop{"container": {Type: "string"}, "up_kbit": {Type: "integer"}, "down_kbit": {Type: "integer"},
		"quota_gb": {Type: "integer"}, "quota_action": {Type: "string", Enum: []string{"", "limit", "stop"}}, "quota_kbit": {Type: "integer"}}
	change.Register(change.Kind{
		Name: "traffic.limit.set", Perm: "network.manage", Scope: "system:write", Title: "kind.traffic.limit.set",
		Params: change.ParamSchema{Props: limProps, Required: []string{"container"}}, CLI: "berth traffic limit {{.container}}",
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			var l trafficLimit
			json.Unmarshal(raw, &l)
			if err := l.validate(); err != nil {
				return change.Plan{}, err
			}
			var cmds []string
			for _, c := range traffic.TCCommands("<veth of "+l.Container+">", traffic.Limit{Container: l.Container, UpKbit: l.UpKbit, DownKbit: l.DownKbit}) {
				cmds = append(cmds, "tc "+joinArgs(c))
			}
			return change.Plan{Summary: "plan.traffic.limit.set", SummaryP: map[string]any{"container": l.Container, "up": l.UpKbit, "down": l.DownKbit, "quota": l.QuotaGB},
				Risk: change.RiskLow, Undoable: true, Commands: cmds}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			a := svc(env)
			var l trafficLimit
			json.Unmarshal(raw, &l)
			var old string
			had := a.db.QueryRow(`SELECT spec FROM traffic_limits WHERE host=? AND container=?`, env.Host, l.Container).Scan(&old) == nil
			spec, _ := json.Marshal(trafficLimit{Container: l.Container, UpKbit: l.UpKbit, DownKbit: l.DownKbit, QuotaGB: l.QuotaGB, QuotaAction: l.QuotaAction, QuotaKbit: l.QuotaKbit})
			if _, err := a.db.Exec(`INSERT INTO traffic_limits(host, container, spec, state, month) VALUES(?,?,?,'ok',?)
				ON CONFLICT(host, container) DO UPDATE SET spec=excluded.spec`, env.Host, l.Container, string(spec), time.Now().Format("2006-01")); err != nil {
				return nil, nil, err
			}
			a.enforceLimits(env.Ctx, env.Host)
			var undo *change.UndoRecipe
			if had {
				undo = &change.UndoRecipe{Kind: "traffic.limit.set", Params: json.RawMessage(old)}
			} else {
				u, _ := json.Marshal(map[string]string{"container": l.Container})
				undo = &change.UndoRecipe{Kind: "traffic.limit.delete", Params: u}
			}
			var errsBy map[string]string
			json.Unmarshal([]byte(a.setting("traffic.limit_errors."+env.Host)), &errsBy)
			return map[string]any{"error": errsBy[l.Container]}, undo, nil
		},
	})
	change.Register(change.Kind{
		Name: "traffic.limit.delete", Perm: "network.manage", Scope: "system:write", Title: "kind.traffic.limit.delete",
		Params: change.ParamSchema{Props: map[string]change.Prop{"container": {Type: "string"}}, Required: []string{"container"}}, CLI: "berth traffic limit delete {{.container}}",
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			var p struct{ Container string }
			json.Unmarshal(raw, &p)
			return change.Plan{Summary: "plan.traffic.limit.delete", SummaryP: map[string]any{"container": p.Container}, Risk: change.RiskLow, Undoable: true,
				Commands: []string{"tc qdisc del dev <veth of " + p.Container + "> root", "tc qdisc del dev <veth of " + p.Container + "> ingress"}}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			a := svc(env)
			var p struct{ Container string }
			json.Unmarshal(raw, &p)
			var old, state string
			if a.db.QueryRow(`SELECT spec, state FROM traffic_limits WHERE host=? AND container=?`, env.Host, p.Container).Scan(&old, &state) != nil {
				return nil, nil, errs.New(404, "traffic.no_limit")
			}
			a.db.Exec(`DELETE FROM traffic_limits WHERE host=? AND container=?`, env.Host, p.Container)
			if state == "stopped" { // stopped for its quota: the quota is gone, so it may run again
				a.call(env.Ctx, env.Host, "POST", "/v1/docker/containers/"+p.Container+"/start", nil, nil)
				a.expectRunning(env.Host, p.Container)
			}
			a.enforceLimits(env.Ctx, env.Host)
			return nil, &change.UndoRecipe{Kind: "traffic.limit.set", Params: json.RawMessage(old)}, nil
		},
	})
}

func joinArgs(c []string) string {
	s := ""
	for i, x := range c {
		if i > 0 {
			s += " "
		}
		s += x
	}
	return s
}
