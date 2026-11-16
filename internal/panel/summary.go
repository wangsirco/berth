package panel

// The overview's "what is this server doing" strip (方案 §5.1): counts that would otherwise
// take a visit to six pages. Every part is best effort: a slow or missing piece leaves its
// field out instead of failing the page.

import (
	"context"
	"net/http"
	"time"
)

type topTraffic struct {
	Name string `json:"name"`
	RX   int64  `json:"rx"`
	TX   int64  `json:"tx"`
}

func init() {
	routeHooks = append(routeHooks, func(a *App, mux *http.ServeMux) {
		mux.HandleFunc("GET /api/v1/hosts/{host}/summary", a.authed("overview.view", a.handleSummary))
	})
}

func (a *App) handleSummary(w http.ResponseWriter, r *http.Request) {
	host := r.PathValue("host")
	ctx, cancel := context.WithTimeout(r.Context(), 4*time.Second)
	defer cancel()
	now := time.Now()
	out := map[string]any{}

	if sites, err := a.sites(host); err == nil {
		on := 0
		for _, s := range sites {
			if s.Enabled {
				on++
			}
		}
		out["sites"] = map[string]int{"total": len(sites), "enabled": on}
	}
	if cs, err := a.containers(ctx, host); err == nil {
		run, open := 0, 0
		for _, c := range cs {
			if c.State == "running" {
				run++
			}
			if exposureOf(c, nil) == "open" {
				open++
			}
		}
		out["containers"] = map[string]int{"total": len(cs), "running": run, "open_ports": open}
	}
	var vms []struct {
		Status string `json:"status"`
	}
	if a.call(ctx, host, "GET", "/v1/vms", nil, &vms) == nil {
		run := 0
		for _, v := range vms {
			if v.Status == "Running" {
				run++
			}
		}
		out["vms"] = map[string]int{"total": len(vms), "running": run}
	}

	// container traffic since local midnight, and who moved the most
	y, m, d := now.Date()
	day := time.Date(y, m, d, 0, 0, 0, 0, now.Location()).Unix()
	var rx, tx int64
	a.db.QueryRow(`SELECT COALESCE(SUM(rx),0), COALESCE(SUM(tx),0) FROM traffic_hourly WHERE host=? AND hour>=?`, host, hourOf(time.Unix(day, 0))).Scan(&rx, &tx)
	top := []topTraffic{}
	if rows, err := a.db.Query(`SELECT key, SUM(rx), SUM(tx) FROM traffic_hourly WHERE host=? AND hour>=? GROUP BY key ORDER BY SUM(rx)+SUM(tx) DESC LIMIT 3`, host, hourOf(time.Unix(day, 0))); err == nil {
		for rows.Next() {
			var t topTraffic
			rows.Scan(&t.Name, &t.RX, &t.TX)
			top = append(top, t)
		}
		rows.Close()
	}
	out["traffic_today"] = map[string]any{"rx": rx, "tx": tx, "top": top}

	// security in the last 24 hours
	since := now.Add(-24 * time.Hour).Unix()
	var bans, fails, waf int
	a.db.QueryRow(`SELECT COUNT(*) FROM bans WHERE host=? AND revoked_at=0 AND (expires_at=0 OR expires_at>?)`, host, now.Unix()).Scan(&bans)
	a.db.QueryRow(`SELECT COALESCE(SUM(count),0) FROM auth_failures WHERE host=? AND ts>=?`, host, since).Scan(&fails)
	a.db.QueryRow(`SELECT COUNT(*) FROM waf_events WHERE host=? AND ts>=? AND blocked=1`, host, since).Scan(&waf)
	out["security"] = map[string]int{"bans": bans, "auth_failures": fails, "waf_blocked": waf}

	var expiring int
	a.db.QueryRow(`SELECT COUNT(*) FROM certs WHERE not_after < ?`, now.Add(14*24*time.Hour).Unix()).Scan(&expiring)
	out["certs_expiring"] = expiring

	var status string
	var finished int64
	if a.db.QueryRow(`SELECT status, finished FROM backup_runs ORDER BY id DESC LIMIT 1`).Scan(&status, &finished) == nil {
		out["backup_last"] = map[string]any{"status": status, "finished": finished}
	}

	// uptime monitors: the latest result of each
	var up, down int
	if rows, err := a.db.Query(`SELECT r.ok FROM uptime_results r JOIN (SELECT check_id, MAX(ts) ts FROM uptime_results GROUP BY check_id) l
		ON r.check_id=l.check_id AND r.ts=l.ts`); err == nil {
		for rows.Next() {
			var ok int
			rows.Scan(&ok)
			if ok == 1 {
				up++
			} else {
				down++
			}
		}
		rows.Close()
	}
	out["uptime"] = map[string]int{"up": up, "down": down}
	writeJSON(w, 200, out)
}
