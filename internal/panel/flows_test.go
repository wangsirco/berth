package panel

import (
	"testing"
	"time"
)

func TestFlowsAPI(t *testing.T) {
	c := newTC(t)
	c.withFakeAgent()
	c.mkUser("root", "correct horse 9", "super")
	c.login("root", "correct horse 9")
	h := hourOf(time.Now())
	for _, r := range []struct {
		dir, peer string
		port      int
		bytes     int64
	}{{"out", "1.1.1.1", 443, 5000}, {"out", "1.1.1.1", 80, 100}, {"out", "8.8.8.8", 53, 300}, {"in", "203.0.113.7", 80, 900}} {
		c.app.db.Exec(`INSERT INTO flows_hourly(host, obj, dir, peer, proto, port, hour, conns, bytes) VALUES('local','container:app',?,?,'tcp',?,?,1,?)`, r.dir, r.peer, r.port, h, r.bytes)
	}
	c.app.db.Exec(`INSERT INTO flow_peers(ip, ptr, country, seen) VALUES('1.1.1.1','one.one.one.one','AU',0)`)
	_, out := c.do("GET", "/api/v1/hosts/local/flows?obj=container:app&range=24h", nil)
	o := out["out"].([]any)
	first := o[0].(map[string]any)
	if len(o) != 2 || first["peer"] != "1.1.1.1" || first["bytes"].(float64) != 5100 || first["ptr"] != "one.one.one.one" || first["country"] != "AU" {
		t.Fatalf("out: %v", out)
	}
	if ports := first["ports"].([]any); ports[0] != "443/tcp" {
		t.Fatalf("ports order: %v", ports)
	}
	if in := out["in"].([]any); len(in) != 1 {
		t.Fatalf("in: %v", out)
	}
	_, all := c.do("GET", "/api/v1/hosts/local/flows?range=24h", nil)
	if objs := all["objects"].([]any); len(objs) != 2 {
		t.Fatalf("objects: %v", all)
	}
}
