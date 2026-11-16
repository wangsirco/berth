package panel

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/wangsirco/berth/internal/store"
)

func TestMetricStoreRingAndRollups(t *testing.T) {
	db, _ := store.Open(filepath.Join(t.TempDir(), "b.db"))
	m := newMetricStore(db)
	base := time.Now().Truncate(time.Hour).Add(-30 * time.Minute).Unix() // minute- and hour-aligned
	for i := 0; i < 120; i++ {
		m.add("local", base+int64(i), map[string]float64{"cpu": float64(i)})
	}
	now := time.Unix(base+120, 0)
	if pts := m.query("local", "cpu", "1h", now, 0); len(pts) != 120 {
		t.Fatalf("1h points %d", len(pts))
	}
	if pts := m.query("local", "cpu", "1h", now, 12); len(pts) > 12 {
		t.Fatalf("downsampled to %d", len(pts))
	}
	m.flushAll()
	rows, _ := db.Query(`SELECT ts, v FROM metrics_min WHERE series='cpu' ORDER BY ts`)
	var got []float64
	for rows.Next() {
		var ts int64
		var v float64
		rows.Scan(&ts, &v)
		got = append(got, v)
	}
	rows.Close()
	if len(got) != 2 || got[0] != 29.5 || got[1] != 89.5 {
		t.Fatalf("minute rows %v", got)
	}
	db.Exec(`INSERT INTO metrics_min(host, ts, series, v) VALUES('local', ?, 'cpu', 1)`, now.AddDate(0, 0, -8).Unix())
	m.prune(now, 7, 365)
	var n int
	db.QueryRow(`SELECT COUNT(*) FROM metrics_min WHERE series='cpu'`).Scan(&n)
	if n != 2 {
		t.Fatalf("prune left %d rows", n)
	}
	if pts := m.query("local", "cpu", "24h", now, 300); len(pts) != 2 {
		t.Fatalf("24h points %d", len(pts))
	}
}
