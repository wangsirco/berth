package panel

// Site statistics (方案 §5.2.7): every minute the panel collects closed per-minute
// aggregates from each host's agent, stores them per minute (2 days) and merged per
// hour (90 days, with the unique-visitor registers so any range can be counted).

import (
	"context"
	"database/sql"
	"encoding/json"
	"log"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/wangsirco/berth/internal/agent/accesslog"
)

const (
	statsMinuteKeep = 2 * 24 * time.Hour
	statsHourKeep   = 90 * 24 * time.Hour
)

func (a *App) statsLoop(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			for _, h := range a.connectedHosts() {
				c, cancel := context.WithTimeout(ctx, 30*time.Second)
				if err := a.collectStats(c, h); err != nil {
					log.Printf("panel: site stats from %s: %v", h, err)
				}
				cancel()
			}
			a.pruneStats()
		}
	}
}

// collectStats pulls one batch and stores it; the agent forgets it on the next call.
func (a *App) collectStats(ctx context.Context, host string) error {
	var boot string
	var seq int64
	a.db.QueryRow(`SELECT boot, seq FROM site_stats_ack WHERE host=?`, host).Scan(&boot, &seq)
	var b accesslog.Batch
	if err := a.call(ctx, host, "GET", "/v1/edge/stats?boot="+boot+"&ack="+strconv.FormatInt(seq, 10), nil, &b); err != nil {
		return err
	}
	if len(b.Minutes) == 0 {
		if b.Boot != boot {
			_, err := a.db.Exec(`INSERT INTO site_stats_ack(host, boot, seq) VALUES(?,?,0) ON CONFLICT(host) DO UPDATE SET boot=excluded.boot, seq=0`, host, b.Boot)
			return err
		}
		return nil
	}
	tx, err := a.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, m := range b.Minutes {
		if err := mergeStat(tx, "site_stats_min", host, m.Site, m.TS, m, false); err != nil {
			return err
		}
		if err := mergeStat(tx, "site_stats_hour", host, m.Site, m.TS-m.TS%3600, m, true); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`INSERT INTO site_stats_ack(host, boot, seq) VALUES(?,?,?) ON CONFLICT(host) DO UPDATE SET boot=excluded.boot, seq=excluded.seq`,
		host, b.Boot, b.Seq); err != nil {
		return err
	}
	return tx.Commit()
}

// mergeStat adds m into the row (host, site, ts). Minute rows drop the registers.
func mergeStat(tx *sql.Tx, table, host, site string, ts int64, m *accesslog.Minute, keepHLL bool) error {
	var raw string
	cur := &accesslog.Minute{TS: ts, Site: site}
	if err := tx.QueryRow(`SELECT data FROM `+table+` WHERE host=? AND site=? AND ts=?`, host, site, ts).Scan(&raw); err == nil {
		json.Unmarshal([]byte(raw), cur)
	}
	cur.Merge(m)
	if !keepHLL {
		cur.HLL, cur.HLLHuman = nil, nil
	}
	b, _ := json.Marshal(cur)
	_, err := tx.Exec(`INSERT INTO `+table+`(host, site, ts, data) VALUES(?,?,?,?) ON CONFLICT(host, site, ts) DO UPDATE SET data=excluded.data`,
		host, site, ts, string(b))
	return err
}

func (a *App) pruneStats() {
	now := time.Now()
	a.db.Exec(`DELETE FROM site_stats_min WHERE ts < ?`, now.Add(-statsMinuteKeep).Unix())
	a.db.Exec(`DELETE FROM site_stats_hour WHERE ts < ?`, now.Add(-statsHourKeep).Unix())
}

type statPoint struct {
	TS    int64 `json:"ts"`
	Req   int64 `json:"req"`
	Bytes int64 `json:"bytes"`
	E4    int64 `json:"e4"`
	E5    int64 `json:"e5"`
	P95   int64 `json:"p95"`
	Uniq  int64 `json:"uniq"`
	Human int64 `json:"humans"` // unique people
	Bots  int64 `json:"bots"`   // requests from programs
	New   int64 `json:"new"`    // first-time people
}

type kvCount struct {
	K string `json:"k"`
	N int64  `json:"n"`
}

func sortedTop(m map[string]int64, n int) []kvCount {
	out := make([]kvCount, 0, len(m))
	for k, v := range m {
		out = append(out, kvCount{k, v})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].N > out[j].N || (out[i].N == out[j].N && out[i].K < out[j].K) })
	if len(out) > n {
		out = out[:n]
	}
	return out
}

var statRanges = map[string]struct {
	span, step time.Duration
	hourly     bool
}{
	"1h":  {time.Hour, time.Minute, false},
	"24h": {24 * time.Hour, 15 * time.Minute, false},
	"7d":  {7 * 24 * time.Hour, time.Hour, true},
	"30d": {30 * 24 * time.Hour, 6 * time.Hour, true},
}

// siteStats returns a series and totals for one site over a range.
func (a *App) siteStats(host, site, rng string, now time.Time) (map[string]any, error) {
	r, ok := statRanges[rng]
	if !ok {
		r, rng = statRanges["24h"], "24h"
	}
	step := int64(r.step.Seconds())
	end := now.Unix() - now.Unix()%step + step
	start := end - int64(r.span.Seconds())
	table := "site_stats_min"
	if r.hourly {
		table = "site_stats_hour"
	}
	rows, err := a.db.Query(`SELECT ts, data FROM `+table+` WHERE host=? AND site=? AND ts>=? AND ts<? ORDER BY ts`, host, site, start, end)
	if err != nil {
		return nil, err
	}
	buckets := map[int64]*accesslog.Minute{}
	total := &accesslog.Minute{Site: site}
	for rows.Next() {
		var ts int64
		var raw string
		rows.Scan(&ts, &raw)
		var m accesslog.Minute
		if json.Unmarshal([]byte(raw), &m) != nil {
			continue
		}
		bt := ts - ts%step
		if buckets[bt] == nil {
			buckets[bt] = &accesslog.Minute{TS: bt, Site: site}
		}
		buckets[bt].Merge(&m)
		hll, hh := m.HLL, m.HLLHuman
		m.HLL, m.HLLHuman = nil, nil // totals' visitors come from the hour rows below
		total.Merge(&m)
		m.HLL, m.HLLHuman = hll, hh
	}
	rows.Close()
	// unique visitors over the whole range: merge the hourly registers
	uniq, humans := accesslog.NewHLL(), accesslog.NewHLL()
	hr, err := a.db.Query(`SELECT data FROM site_stats_hour WHERE host=? AND site=? AND ts>=? AND ts<?`, host, site, start-start%3600, end)
	if err == nil {
		for hr.Next() {
			var raw string
			hr.Scan(&raw)
			var m accesslog.Minute
			if json.Unmarshal([]byte(raw), &m) == nil && len(m.HLL) > 0 {
				uniq.Merge(accesslog.Unpack(m.HLL))
			}
			if len(m.HLLHuman) > 0 {
				humans.Merge(accesslog.Unpack(m.HLLHuman))
			}
		}
		hr.Close()
	}
	series := []statPoint{}
	for ts := start; ts < end; ts += step {
		p := statPoint{TS: ts}
		if m := buckets[ts]; m != nil {
			p = statPoint{TS: ts, Req: m.Req, Bytes: m.Bytes, E4: m.Status["4xx"], E5: m.Status["5xx"], P95: accesslog.Percentile(m.Lat, 0.95), Uniq: m.Uniq, Human: m.Humans, Bots: m.Bots, New: m.New}
		}
		series = append(series, p)
	}
	return map[string]any{
		"range": rng, "step": step, "series": series,
		"total": map[string]any{
			"req": total.Req, "bytes": total.Bytes, "uniq": uniq.Count(), "status": total.Status, "bots": total.Bots, "scans": total.Scans,
			"humans": humans.Count(), "human_req": total.HumanReq, "new": total.New, "returning": max(humans.Count()-total.New, 0), "bot_kinds": total.BotKinds,
			"p50": accesslog.Percentile(total.Lat, 0.5), "p95": accesslog.Percentile(total.Lat, 0.95), "lat": total.Lat, "lat_bounds": accesslog.LatencyBounds,
		},
		"countries": sortedTop(total.Countries, 15), "paths": sortedTop(total.Paths, 20), "refs": sortedTop(total.Refs, 20),
		"scan_paths": sortedTop(total.ScanPaths, 10), "slow": total.Slow,
		"geo_credit": "IP geolocation by DB-IP (https://db-ip.com), CC BY 4.0",
	}, nil
}

func init() {
	routeHooks = append(routeHooks, func(a *App, mux *http.ServeMux) {
		mux.HandleFunc("GET /api/v1/hosts/{host}/sites/{id}/stats", a.authed("sites.view", func(w http.ResponseWriter, r *http.Request) {
			host := r.PathValue("host")
			s, err := a.siteByRef(host, r.PathValue("id"))
			if err != nil {
				a.fail(w, r, err)
				return
			}
			out, err := a.siteStats(host, s.Primary(), r.URL.Query().Get("range"), time.Now())
			if err != nil {
				a.fail(w, r, err)
				return
			}
			writeJSON(w, 200, out)
		}))
	})
}
