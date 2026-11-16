package panel

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/wangsirco/berth/internal/change"
)

func TestUserTrafficQuota(t *testing.T) {
	c := newTC(t)
	a := c.app
	u := c.mkUser("bob", "correct horse 9", "admin")
	q, _ := json.Marshal(Quota{TrafficGB: 10})
	a.db.Exec(`UPDATE users SET quota=? WHERE id=?`, string(q), u.ID)
	now := time.Now().Unix()
	a.db.Exec(`INSERT INTO owners(host, type, id, user_id, created_at) VALUES('local','compose','shop',?,?), ('local','dev','ws1',?,?)`, u.ID, now, u.ID, now)
	add := func(key string, b int64) {
		a.db.Exec(`INSERT INTO traffic_hourly(host, key, hour, rx, tx) VALUES('local',?,?,?,0)
			ON CONFLICT(host, key, hour) DO UPDATE SET rx=rx+excluded.rx`, key, hourOf(time.Now()), b)
	}
	add("shop-web-1", 4<<30)
	add("shop-db-1", 1<<30)
	add("shopping-1", 50<<30) // another project whose name only starts the same
	add("berth-dev-ws1", 4<<30)
	add("berth-dev-ws1", 0)
	add("someone-else", 9<<30)
	if got := a.userTraffic(u.ID); got != 9<<30 {
		t.Fatalf("traffic %d GB", got>>30)
	}
	a.checkUserTraffic()
	if s := a.setting("user_traffic." + itoa(u.ID)); !strings.HasSuffix(s, ":80") {
		t.Fatalf("80%% not noted: %q", s)
	}
	actor := change.Actor{Type: "user", ID: itoa(u.ID), Name: "bob"}
	if err := a.checkQuota(actor, "local", "compose.create", json.RawMessage(`{"name":"more"}`)); err != nil {
		t.Fatalf("under quota refused: %v", err)
	}
	add("shop-web-1", 2<<30)
	a.checkUserTraffic()
	if s := a.setting("user_traffic." + itoa(u.ID)); !strings.HasSuffix(s, ":100") {
		t.Fatalf("100%% not noted: %q", s)
	}
	err := a.checkQuota(actor, "local", "compose.create", json.RawMessage(`{"name":"more"}`))
	if err == nil || !strings.Contains(err.Error(), "over_traffic") {
		t.Fatalf("over quota allowed: %v", err)
	}
	var n int
	a.db.QueryRow(`SELECT COUNT(*) FROM alert_events WHERE rule='user_traffic_quota'`).Scan(&n)
	if n != 2 {
		t.Fatalf("%d alerts, want 2", n)
	}
	a.checkUserTraffic() // nothing new
	a.db.QueryRow(`SELECT COUNT(*) FROM alert_events WHERE rule='user_traffic_quota'`).Scan(&n)
	if n != 2 {
		t.Fatalf("repeated alert: %d", n)
	}
}
