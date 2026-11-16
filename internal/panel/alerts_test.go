package panel

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wangsirco/berth/internal/panel/notify"
)

func TestInQuiet(t *testing.T) {
	at := func(h, m int) time.Time { return time.Date(2026, 1, 1, h, m, 0, 0, time.Local) }
	cases := []struct {
		spec string
		t    time.Time
		want bool
	}{
		{"22:00-07:00", at(23, 0), true},
		{"22:00-07:00", at(6, 59), true},
		{"22:00-07:00", at(7, 0), false},
		{"22:00-07:00", at(12, 0), false},
		{"12:00-13:30", at(13, 0), true},
		{"12:00-13:30", at(14, 0), false},
		{"", at(1, 0), false},
	}
	for _, c := range cases {
		if got := inQuiet(c.spec, c.t); got != c.want {
			t.Errorf("inQuiet(%q, %v) = %v", c.spec, c.t.Format("15:04"), got)
		}
	}
}

func TestJustStarted(t *testing.T) {
	for st, want := range map[[2]string]bool{
		{"restarting", "Restarting (1) 2 seconds ago"}: true,
		{"running", "Up Less than a second"}:           true,
		{"running", "Up 4 seconds"}:                    true,
		{"running", "Up 40 seconds"}:                   false,
		{"running", "Up 3 minutes"}:                    false,
		{"exited", "Exited (1) 3 seconds ago"}:         false,
	} {
		if justStarted(st[0], st[1]) != want {
			t.Errorf("%v: want %v", st, want)
		}
	}
}

func TestAlertFireResolveDeliver(t *testing.T) {
	c := newTC(t)
	a := c.app
	u := c.mkUser("ops", "pw-ops-123456", RoleSuper)

	var mu sync.Mutex
	var got []string
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		got = append(got, string(b))
		mu.Unlock()
	}))
	defer hook.Close()
	cfg, _ := json.Marshal(notify.Config{Kind: "webhook", URL: hook.URL})
	enc, _ := a.seal(cfg)
	if _, err := a.db.Exec(`INSERT INTO notify_channels(user_id, kind, name, cfg_enc, categories, created_at) VALUES(?,?,?,?,?,?)`,
		u.ID, "webhook", "hook", enc, `["containers"]`, time.Now().Unix()); err != nil {
		t.Fatal(err)
	}
	old := groupWindow
	groupWindow = 50 * time.Millisecond
	defer func() { groupWindow = old }()

	r, _ := a.alertRule("container_down")
	a.fire(r, "local/web", "local", map[string]any{"container": "web", "host": "local", "state": "exited"})
	a.fire(r, "local/web", "local", map[string]any{"container": "web"}) // already firing: nothing new
	var n int
	a.db.QueryRow(`SELECT COUNT(*) FROM alert_events WHERE state='firing'`).Scan(&n)
	if n != 1 {
		t.Fatalf("firing events = %d", n)
	}
	// a category the channel did not pick is not delivered
	a.fire(alertRule{Key: "disk_high", Category: "resources", Severity: "warn", Enabled: true}, "local:/", "local", map[string]any{"mount": "/", "pct": 95})

	deadline := time.Now().Add(3 * time.Second)
	for {
		mu.Lock()
		k := len(got)
		mu.Unlock()
		if k > 0 || time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	mu.Lock()
	if len(got) != 1 || !strings.Contains(got[0], "web") || strings.Contains(got[0], "95") {
		t.Fatalf("delivered %q", got)
	}
	mu.Unlock()

	a.resolve("container_down", "local/web")
	a.db.QueryRow(`SELECT COUNT(*) FROM alert_events WHERE state='firing' AND rule='container_down'`).Scan(&n)
	if n != 0 {
		t.Fatal("not resolved")
	}

	// a disabled rule never fires
	r.Enabled = false
	a.fire(r, "local/db", "local", nil)
	a.db.QueryRow(`SELECT COUNT(*) FROM alert_events WHERE key='container_down:local/db'`).Scan(&n)
	if n != 0 {
		t.Fatal("disabled rule fired")
	}
}

func TestAlertHeldNeedsTwo(t *testing.T) {
	st := &alertState{breach: map[string]int{}}
	if st.held("k", true, 0) {
		t.Fatal("fired on first breach")
	}
	if !st.held("k", true, 0) {
		t.Fatal("did not fire on second")
	}
	st.held("k", false, 0)
	if st.held("k", true, 0) {
		t.Fatal("blip reset failed")
	}
	// 1 minute at 15 s = 4 evaluations
	for i := 0; i < 3; i++ {
		if st.held("m", true, 1) {
			t.Fatalf("fired early at %d", i)
		}
	}
	if !st.held("m", true, 1) {
		t.Fatal("did not fire after a minute")
	}
}
