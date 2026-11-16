package panel

// WAF events (方案 §5.12.3): requests Coraza flagged or blocked on a host's edge, kept for
// 14 days; blocked ones can count toward automatic bans.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type wafEvent struct {
	ID      int64           `json:"id"`
	Site    int64           `json:"site"`
	Domain  string          `json:"domain,omitempty"`
	TS      int64           `json:"ts"`
	IP      string          `json:"ip"`
	URI     string          `json:"uri"`
	Blocked bool            `json:"blocked"`
	Rules   json.RawMessage `json:"rules"`
}

func (a *App) collectWAF(ctx context.Context, host string) {
	key := "security.wafpos." + host
	var res struct {
		Events []struct {
			TS      int64           `json:"ts"`
			IP      string          `json:"ip"`
			Host    string          `json:"host"`
			Site    int64           `json:"site"`
			URI     string          `json:"uri"`
			Blocked bool            `json:"blocked"`
			Rules   json.RawMessage `json:"rules"`
		} `json:"events"`
		Pos string `json:"pos"`
	}
	if err := a.call(ctx, host, "GET", "/v1/security/waf?pos="+url.QueryEscape(a.setting(key)), nil, &res); err != nil {
		return
	}
	if res.Pos != "" {
		a.setSetting(key, res.Pos)
	}
	if len(res.Events) == 0 {
		return
	}
	var byDomain map[string]int64
	countBans := a.setting("security.waf_ban") == "1"
	var fails []authFailure
	for _, e := range res.Events {
		site := e.Site
		if site == 0 && e.Host != "" {
			if byDomain == nil {
				byDomain = map[string]int64{}
				if ss, err := a.sites(host); err == nil {
					for _, s := range ss {
						for _, d := range s.Domains {
							byDomain[d] = s.ID
						}
					}
				}
			}
			site = byDomain[strings.ToLower(e.Host)]
		}
		a.db.Exec(`INSERT INTO waf_events(host, site_id, ts, ip, uri, blocked, rules) VALUES(?,?,?,?,?,?,?)`,
			host, site, e.TS, e.IP, truncate(e.URI, 500), e.Blocked, string(e.Rules))
		if countBans && e.Blocked {
			fails = append(fails, authFailure{TS: e.TS, IP: e.IP, Count: 1})
		}
	}
	if len(fails) > 0 {
		a.recordFailures(host, "waf", fails)
	}
	a.hub.publish(event{Type: "waf", Data: map[string]any{"host": host, "count": len(res.Events)}})
}

func init() {
	routeHooks = append(routeHooks, func(a *App, mux *http.ServeMux) {
		mux.HandleFunc("GET /api/v1/hosts/{host}/waf", a.authed("network.view", func(w http.ResponseWriter, r *http.Request) {
			host := r.PathValue("host")
			q := `SELECT id, site_id, ts, ip, uri, blocked, rules FROM waf_events WHERE host=?`
			args := []any{host}
			if s, _ := strconv.ParseInt(r.URL.Query().Get("site"), 10, 64); s > 0 {
				q += ` AND site_id=?`
				args = append(args, s)
			}
			rows, err := a.db.Query(q+` ORDER BY id DESC LIMIT 200`, args...)
			if err != nil {
				a.fail(w, r, err)
				return
			}
			names := map[int64]string{}
			sitesOn := []map[string]any{}
			if ss, err := a.sites(host); err == nil {
				for _, s := range ss {
					names[s.ID] = s.Primary()
					mode := ""
					if s.Options.WAF != nil {
						mode = s.Options.WAF.Mode
					}
					sitesOn = append(sitesOn, map[string]any{"id": s.ID, "domain": s.Primary(), "mode": mode})
				}
			}
			out := []wafEvent{}
			for rows.Next() {
				var e wafEvent
				var rules string
				rows.Scan(&e.ID, &e.Site, &e.TS, &e.IP, &e.URI, &e.Blocked, &rules)
				e.Rules, e.Domain = json.RawMessage(rules), names[e.Site]
				out = append(out, e)
			}
			rows.Close()
			writeJSON(w, 200, map[string]any{"items": out, "sites": sitesOn, "ban": a.setting("security.waf_ban") == "1"})
		}))
	})
}

func (a *App) pruneWAF() {
	a.db.Exec(`DELETE FROM waf_events WHERE ts < ?`, time.Now().AddDate(0, 0, -14).Unix())
}
