package panel

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestTrafficQuota(t *testing.T) {
	c := newTC(t)
	a := c.app
	ctx := context.Background()
	month := time.Now().Format("2006-01")
	add := func(name string, bytes int64) {
		a.db.Exec(`INSERT INTO traffic_hourly(host, key, hour, rx, tx) VALUES('local',?,?,?,0)
			ON CONFLICT(host, key, hour) DO UPDATE SET rx=rx+excluded.rx`, name, hourOf(time.Now()), bytes)
	}
	set := func(l trafficLimit, state, m string) {
		b, _ := json.Marshal(l)
		a.db.Exec(`INSERT OR REPLACE INTO traffic_limits(host, container, spec, state, month) VALUES('local',?,?,?,?)`, l.Container, string(b), state, m)
	}
	state := func(name string) string {
		var s string
		a.db.QueryRow(`SELECT state FROM traffic_limits WHERE container=?`, name).Scan(&s)
		return s
	}
	set(trafficLimit{Container: "web", QuotaGB: 10, QuotaAction: "limit", QuotaKbit: 1000}, "ok", month)
	add("web", 7<<30)
	a.enforceLimits(ctx, "local")
	if state("web") != "ok" {
		t.Fatal("under 80%:", state("web"))
	}
	add("web", 1<<30) // 8 of 10
	a.enforceLimits(ctx, "local")
	if state("web") != "warned" {
		t.Fatal("at 80%:", state("web"))
	}
	add("web", 3<<30) // over
	a.enforceLimits(ctx, "local")
	if state("web") != "throttled" {
		t.Fatal("over:", state("web"))
	}
	var n int
	a.db.QueryRow(`SELECT COUNT(*) FROM alert_events WHERE rule='traffic_quota'`).Scan(&n)
	if n != 2 {
		t.Errorf("%d quota alerts, want 2 (80%% and 100%%)", n)
	}
	// a new month starts over
	a.db.Exec(`UPDATE traffic_limits SET month='2000-01' WHERE container='web'`)
	a.db.Exec(`DELETE FROM traffic_hourly`)
	a.enforceLimits(ctx, "local")
	if state("web") != "ok" {
		t.Fatal("new month:", state("web"))
	}
	// stop action
	set(trafficLimit{Container: "dl", QuotaGB: 1, QuotaAction: "stop"}, "ok", month)
	add("dl", 2<<30)
	a.enforceLimits(ctx, "local")
	if state("dl") != "stopped" {
		t.Fatal("stop:", state("dl"))
	}
}

func TestTrafficLimitValidate(t *testing.T) {
	good := []trafficLimit{{Container: "web", DownKbit: 8000}, {Container: "web", QuotaGB: 100, QuotaAction: "stop"}, {Container: "a.b-c_1", UpKbit: 1, QuotaGB: 1, QuotaAction: "limit", QuotaKbit: 512}}
	for _, l := range good {
		if err := l.validate(); err != nil {
			t.Errorf("%+v: %v", l, err)
		}
	}
	bad := []trafficLimit{{Container: "web"}, {Container: "web;rm", DownKbit: 1}, {Container: "web", DownKbit: -1}, {Container: "web", QuotaGB: 1}, {Container: "web", QuotaGB: 1, QuotaAction: "limit"}}
	for _, l := range bad {
		if l.validate() == nil {
			t.Errorf("accepted %+v", l)
		}
	}
	if minPos(0, 5) != 5 || minPos(3, 5) != 3 || minPos(8, 5) != 5 || minPos(7, 0) != 7 {
		t.Error("minPos")
	}
}
