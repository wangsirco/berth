package panel

import (
	"testing"
	"time"

	"github.com/wangsirco/berth/internal/agent/metrics"
)

func TestOverviewAndStatusbar(t *testing.T) {
	c := newTC(t)
	c.withFakeAgent()
	c.mkUser("iwsheng", "correct horse 9", "super")
	c.login("iwsheng", "correct horse 9")
	c.app.metrics.observe("local", metrics.Host{TS: time.Now().Unix(), CPU: 12.5, MemTotal: 8 << 30, MemUsed: 2 << 30, MemAvail: 6 << 30})
	r, out := c.do("GET", "/api/v1/hosts/local/overview", nil)
	if r.StatusCode != 200 || out["host"].(map[string]any)["cpu"] != 12.5 {
		t.Fatalf("%d %v", r.StatusCode, out)
	}
	_, sb := c.do("GET", "/api/v1/statusbar", nil)
	if sb["online"] != true || sb["wharf"] != nil {
		t.Fatalf("%v", sb)
	}
	_, m := c.do("GET", "/api/v1/hosts/local/metrics?series=cpu&range=1h", nil)
	if len(m["series"].(map[string]any)["cpu"].([]any)) != 1 {
		t.Fatalf("%v", m)
	}
}
