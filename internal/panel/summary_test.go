package panel

import "testing"

func TestSummary(t *testing.T) {
	c := newTC(t)
	c.withFakeAgent()
	c.mkUser("root", "correct horse 9", "super")
	c.login("root", "correct horse 9")
	r, out := c.do("GET", "/api/v1/hosts/local/summary", nil)
	if r.StatusCode != 200 {
		t.Fatalf("%d %v", r.StatusCode, out)
	}
	for _, k := range []string{"sites", "containers", "traffic_today", "security", "uptime"} {
		if _, ok := out[k]; !ok {
			t.Fatalf("missing %s: %v", k, out)
		}
	}
}
