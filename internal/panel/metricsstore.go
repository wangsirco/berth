package panel

import (
	"context"
	"database/sql"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/wangsirco/berth/internal/agent/metrics"
)

// metricStore keeps 1 s samples for the last hour in memory and writes minute and hour
// averages to SQLite (方案 §5.13.1). Retention comes from settings.
type metricStore struct {
	db *sql.DB
	mu sync.Mutex
	// host -> series -> ring of the last hour
	rings map[string]map[string]*ring
	// host|series -> running sum for the current minute / hour
	minAcc  map[string]*accum
	hourAcc map[string]*accum
	latest  map[string]metrics.Host
	seenAt  map[string]time.Time
}

const ringSize = 3600

type point struct {
	TS int64   `json:"t"`
	V  float64 `json:"v"`
}

type ring struct {
	pts  [ringSize]point
	next int
	n    int
}

func (r *ring) push(p point) {
	r.pts[r.next] = p
	r.next = (r.next + 1) % ringSize
	r.n = min(r.n+1, ringSize)
}

func (r *ring) since(ts int64) []point {
	out := make([]point, 0, r.n)
	start := (r.next - r.n + ringSize) % ringSize
	for i := 0; i < r.n; i++ {
		if p := r.pts[(start+i)%ringSize]; p.TS >= ts {
			out = append(out, p)
		}
	}
	return out
}

type accum struct {
	bucket int64
	sum    float64
	n      int
}

func newMetricStore(db *sql.DB) *metricStore {
	return &metricStore{db: db, rings: map[string]map[string]*ring{}, minAcc: map[string]*accum{}, hourAcc: map[string]*accum{},
		latest: map[string]metrics.Host{}, seenAt: map[string]time.Time{}}
}

// seriesOf flattens a host sample into named series.
func seriesOf(h metrics.Host) map[string]float64 {
	s := map[string]float64{
		"cpu": h.CPU, "load1": h.Load[0], "mem_used": float64(h.MemUsed), "mem_avail": float64(h.MemAvail),
		"swap_used": float64(h.SwapUsed), "zram_used": float64(h.ZramUsed), "disk_r": float64(h.DiskRBps), "disk_w": float64(h.DiskWBps),
	}
	var rx, tx int64
	for _, n := range h.Net {
		rx, tx = rx+n.RxBps, tx+n.TxBps
	}
	s["net_rx"], s["net_tx"] = float64(rx), float64(tx)
	for _, d := range h.Disks {
		s["disk_used:"+d.Mount] = float64(d.Used)
	}
	return s
}

func (m *metricStore) observe(host string, h metrics.Host) {
	m.mu.Lock()
	m.latest[host] = h
	m.seenAt[host] = time.Now()
	m.mu.Unlock()
	m.add(host, h.TS, seriesOf(h))
}

// add records one sample per series; crossing a minute (hour) boundary flushes the average.
func (m *metricStore) add(host string, ts int64, vals map[string]float64) {
	m.mu.Lock()
	rs := m.rings[host]
	if rs == nil {
		rs = map[string]*ring{}
		m.rings[host] = rs
	}
	var minRows, hourRows [][4]any
	for name, v := range vals {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			continue
		}
		r := rs[name]
		if r == nil {
			r = &ring{}
			rs[name] = r
		}
		r.push(point{TS: ts, V: v})
		key := host + "|" + name
		if row, ok := roll(m.minAcc, key, ts/60*60, v); ok {
			minRows = append(minRows, row)
			if hrow, ok := roll(m.hourAcc, key, row[1].(int64)/3600*3600, row[3].(float64)); ok {
				hourRows = append(hourRows, hrow)
			}
		}
	}
	m.mu.Unlock()
	m.write("metrics_min", minRows)
	m.write("metrics_hour", hourRows)
}

// roll adds v to the accumulator for bucket; when the bucket changes it returns the
// finished bucket as a row {key, bucket, series, avg}.
func roll(accs map[string]*accum, key string, bucket int64, v float64) ([4]any, bool) {
	a := accs[key]
	if a == nil {
		accs[key] = &accum{bucket: bucket, sum: v, n: 1}
		return [4]any{}, false
	}
	if a.bucket == bucket {
		a.sum += v
		a.n++
		return [4]any{}, false
	}
	row := [4]any{key, a.bucket, "", a.sum / float64(a.n)}
	*a = accum{bucket: bucket, sum: v, n: 1}
	return row, true
}

// flushAll writes every unfinished minute and hour (on shutdown, and in tests).
func (m *metricStore) flushAll() {
	m.mu.Lock()
	var minRows, hourRows [][4]any
	for k, a := range m.minAcc {
		minRows = append(minRows, [4]any{k, a.bucket, "", a.sum / float64(a.n)})
	}
	for k, a := range m.hourAcc {
		hourRows = append(hourRows, [4]any{k, a.bucket, "", a.sum / float64(a.n)})
	}
	m.minAcc, m.hourAcc = map[string]*accum{}, map[string]*accum{}
	m.mu.Unlock()
	m.write("metrics_min", minRows)
	m.write("metrics_hour", hourRows)
}

func (m *metricStore) write(table string, rows [][4]any) {
	if len(rows) == 0 {
		return
	}
	tx, err := m.db.Begin()
	if err != nil {
		return
	}
	defer tx.Rollback()
	st, err := tx.Prepare(`INSERT OR REPLACE INTO ` + table + `(host, ts, series, v) VALUES(?,?,?,?)`)
	if err != nil {
		return
	}
	for _, r := range rows {
		host, series, _ := strings.Cut(r[0].(string), "|")
		if _, err := st.Exec(host, r[1], series, r[3]); err != nil {
			return
		}
	}
	_ = tx.Commit()
}

// prune drops data older than the retention settings.
func (m *metricStore) prune(now time.Time, minDays, hourDays int) {
	_, _ = m.db.Exec(`DELETE FROM metrics_min WHERE ts < ?`, now.AddDate(0, 0, -minDays).Unix())
	_, _ = m.db.Exec(`DELETE FROM metrics_hour WHERE ts < ?`, now.AddDate(0, 0, -hourDays).Unix())
}

var ranges = map[string]time.Duration{"1h": time.Hour, "24h": 24 * time.Hour, "7d": 7 * 24 * time.Hour, "30d": 30 * 24 * time.Hour}

// query returns up to maxPoints averaged points for series over rng.
func (m *metricStore) query(host, series, rng string, now time.Time, maxPoints int) []point {
	d, ok := ranges[rng]
	if !ok {
		d, rng = time.Hour, "1h"
	}
	from := now.Add(-d).Unix()
	var pts []point
	if rng == "1h" {
		m.mu.Lock()
		if r := m.rings[host][series]; r != nil {
			pts = r.since(from)
		}
		m.mu.Unlock()
	} else {
		table := "metrics_min"
		if rng == "30d" {
			table = "metrics_hour"
		}
		rows, err := m.db.Query(`SELECT ts, v FROM `+table+` WHERE host=? AND series=? AND ts>=? ORDER BY ts`, host, series, from)
		if err == nil {
			for rows.Next() {
				var p point
				if rows.Scan(&p.TS, &p.V) == nil {
					pts = append(pts, p)
				}
			}
			rows.Close()
		}
	}
	return downsample(pts, from, now.Unix(), maxPoints)
}

// downsample averages points into at most n equal time buckets.
func downsample(pts []point, from, to int64, n int) []point {
	if n <= 0 || len(pts) <= n {
		return pts
	}
	width := float64(to-from) / float64(n)
	sums := make([]float64, n)
	counts := make([]int, n)
	for _, p := range pts {
		i := int(float64(p.TS-from) / width)
		if i < 0 || i >= n {
			continue
		}
		sums[i] += p.V
		counts[i]++
	}
	out := make([]point, 0, n)
	for i := range sums {
		if counts[i] > 0 {
			out = append(out, point{TS: from + int64(float64(i)*width+width/2), V: sums[i] / float64(counts[i])})
		}
	}
	return out
}

func (m *metricStore) latestOf(host string) (metrics.Host, time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.latest[host], m.seenAt[host]
}

// collect polls every host agent once a second until ctx ends.
func (a *App) collect(ctx context.Context) {
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	lastPrune := time.Time{}
	for {
		select {
		case <-ctx.Done():
			a.metrics.flushAll()
			return
		case now := <-tick.C:
			for _, host := range a.connectedHosts() {
				c, cancel := context.WithTimeout(ctx, 900*time.Millisecond)
				var h metrics.Host
				err := a.call(c, host, "GET", "/v1/metrics/host", nil, &h)
				cancel()
				if err == nil {
					a.metrics.observe(host, h)
					if a.hub.count() > 0 {
						a.hub.publish(event{Type: "metrics.tick", Data: map[string]any{"host": host, "sample": h}})
					}
				}
			}
			if now.Sub(lastPrune) > 6*time.Hour {
				a.metrics.prune(now, a.settingInt("metrics.keep_min_days", 7), a.settingInt("metrics.keep_hour_days", 365))
				lastPrune = now
			}
		}
	}
}
