package panel

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSiteStatsCollectAndQuery(t *testing.T) {
	c := newTC(t)
	fa := c.withFakeAgent()
	c.mkUser("root", "correct horse 9", "super")
	c.login("root", "correct horse 9")
	c.do("POST", "/api/v1/hosts/local/changes/site.create", map[string]any{"domains": []string{"a.example.com"}, "target": map[string]any{"type": "static"}})

	now := time.Now()
	base := now.Add(-10 * time.Minute).Unix()
	base -= base % 60
	var lines strings.Builder
	for i := 0; i < 30; i++ {
		status := 200
		if i%10 == 0 {
			status = 500
		}
		fmt.Fprintf(&lines, `{"ts":%d.5,"request":{"client_ip":"203.0.113.%d","method":"GET","host":"a.example.com","uri":"/p%d","headers":{"User-Agent":["Mozilla"]}},"duration":0.04,"size":1000,"status":%d}`+"\n",
			base+int64(i*10), i%7, i%3, status)
	}
	os.WriteFile(filepath.Join(fa.logsDir, "a.example.com.log"), []byte(lines.String()), 0o644)
	fa.collector.Poll()

	ctx := context.Background()
	if err := c.app.collectStats(ctx, "local"); err != nil {
		t.Fatal(err)
	}
	if err := c.app.collectStats(ctx, "local"); err != nil { // acknowledges; nothing new
		t.Fatal(err)
	}
	var n int
	c.app.db.QueryRow(`SELECT COUNT(*) FROM site_stats_min`).Scan(&n)
	if n != 5 {
		t.Fatalf("%d minute rows, want 5", n)
	}

	_, out := c.do("GET", "/api/v1/hosts/local/sites/a.example.com/stats?range=1h", nil)
	tot := out["total"].(map[string]any)
	if tot["req"].(float64) != 30 || tot["bytes"].(float64) != 30000 || tot["uniq"].(float64) != 7 || tot["p95"].(float64) != 50 {
		t.Fatalf("total: %v", tot)
	}
	if tot["status"].(map[string]any)["5xx"].(float64) != 3 {
		t.Fatalf("status: %v", tot["status"])
	}
	if len(out["series"].([]any)) != 60 || !strings.Contains(toJSON(out["paths"]), `"k":"/p0"`) {
		t.Fatalf("series/paths: %v", out["paths"])
	}
	_, week := c.do("GET", "/api/v1/hosts/local/sites/1/stats?range=7d", nil)
	if week["total"].(map[string]any)["req"].(float64) != 30 || len(week["series"].([]any)) != 168 {
		t.Fatalf("7d: %v", week["total"])
	}
}
