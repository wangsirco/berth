package panel

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/coder/websocket"
	"github.com/wangsirco/berth/internal/pve"
)

func TestPVEPanel(t *testing.T) {
	fake := pve.NewFake("berth@pve!panel", "s3cret-0000-1111")
	srv := httptest.NewTLSServer(fake)
	defer srv.Close()
	c := newTC(t)
	c.mkUser("admin", "correct-horse-1", RoleSuper)
	c.login("admin", "correct-horse-1")

	// the certificate is shown first; the change pins it
	r, probe := c.do("POST", "/api/v1/pve/probe", map[string]string{"url": srv.URL})
	if r.StatusCode != 200 || len(probe["fingerprint"].(string)) != 64 || probe["trusted"] != false {
		t.Fatalf("probe %d %v", r.StatusCode, probe)
	}
	set := map[string]any{"name": "Home lab", "url": srv.URL, "token_id": "berth@pve!panel", "secret": "wrong", "fingerprint": probe["fingerprint"]}
	if r, out := c.do("POST", "/api/v1/hosts/local/changes/pve.set", set); r.StatusCode == 200 && out["status"] == "done" {
		t.Fatal("a wrong secret was saved")
	}
	set["secret"] = "s3cret-0000-1111"
	r, out := c.do("POST", "/api/v1/hosts/local/changes/pve.set", set)
	if r.StatusCode != 200 || out["status"] != "done" {
		t.Fatalf("pve.set %d %v", r.StatusCode, out)
	}
	_, saved := c.do("GET", "/api/v1/changes/"+out["id"].(string), nil)
	if b, _ := json.Marshal(saved); strings.Contains(string(b), "s3cret") {
		t.Fatal("secret in the change record")
	}
	_, list := c.do("GET", "/api/v1/pve", nil)
	items := list["items"].([]any)
	cl := items[0].(map[string]any)
	if cl["cluster"].(map[string]any)["id"] != "home-lab" || cl["version"] != "9.1.2" || len(cl["guests"].([]any)) != 3 || cl["error"] != nil {
		t.Fatalf("list %v", cl)
	}
	// power with undo, snapshot with undo
	ref := map[string]any{"cluster": "home-lab", "node": "pve1", "type": "lxc", "vmid": 101, "name": "db"}
	with := func(k string, v any) map[string]any {
		m := map[string]any{k: v}
		for a, b := range ref {
			m[a] = b
		}
		return m
	}
	r, out = c.do("POST", "/api/v1/hosts/local/changes/pve.power", with("action", "start"))
	if r.StatusCode != 200 || out["status"] != "done" || fake.GuestStatus(101) != "running" {
		t.Fatalf("start %v %s", out, fake.GuestStatus(101))
	}
	if r, _ := c.do("POST", "/api/v1/changes/"+out["id"].(string)+"/revert", nil); r.StatusCode != 200 || fake.GuestStatus(101) != "stopped" {
		t.Fatalf("undo start: %d %s", r.StatusCode, fake.GuestStatus(101))
	}
	c.do("POST", "/api/v1/hosts/local/changes/pve.power", with("action", "start"))
	_, g := c.do("GET", "/api/v1/pve/home-lab/guests/pve1/lxc/101", nil)
	if ips := g["ips"].([]any); len(ips) != 1 || ips[0] != "10.0.9.101" {
		t.Fatalf("guest %v", g)
	}
	r, out = c.do("POST", "/api/v1/hosts/local/changes/pve.snapshot", with("snapshot", "berth1"))
	if out["status"] != "done" {
		t.Fatalf("snapshot %v", out)
	}
	_, g = c.do("GET", "/api/v1/pve/home-lab/guests/pve1/lxc/101", nil)
	if len(g["snapshots"].([]any)) != 1 {
		t.Fatalf("snapshots %v", g["snapshots"])
	}
	c.do("POST", "/api/v1/changes/"+out["id"].(string)+"/revert", nil)
	_, g = c.do("GET", "/api/v1/pve/home-lab/guests/pve1/lxc/101", nil)
	if len(g["snapshots"].([]any)) != 0 {
		t.Fatalf("snapshot not removed by undo: %v", g["snapshots"])
	}

	// the console: a one-time key, the websocket relayed to PVE's vncwebsocket
	_, tk := c.do("POST", "/api/v1/pve/home-lab/guests/pve1/lxc/101/console", nil)
	key, _ := tk["key"].(string)
	if key == "" || !strings.HasPrefix(tk["password"].(string), "PVEVNC:") {
		t.Fatalf("ticket %v", tk)
	}
	h := http.Header{"Origin": {c.srv.URL}}
	ws, _, err := websocket.Dial(context.Background(), strings.Replace(c.srv.URL, "https", "wss", 1)+"/api/v1/pve/console/"+key,
		&websocket.DialOptions{HTTPClient: c.hc, HTTPHeader: h, Subprotocols: []string{"binary"}})
	if err != nil {
		t.Fatal(err)
	}
	_, b, err := ws.Read(context.Background())
	if err != nil || string(b) != "RFB 003.008\n" {
		t.Fatal(string(b), err)
	}
	ws.Write(context.Background(), websocket.MessageBinary, []byte("RFB 003.008\n"))
	if _, b, _ := ws.Read(context.Background()); string(b) != "\x01\x02" {
		t.Fatalf("no answer through the relay: %q", b)
	}
	ws.Close(websocket.StatusNormalClosure, "")
	// a key works once
	if _, _, err := websocket.Dial(context.Background(), strings.Replace(c.srv.URL, "https", "wss", 1)+"/api/v1/pve/console/"+key,
		&websocket.DialOptions{HTTPClient: c.hc, HTTPHeader: h}); err == nil {
		t.Fatal("console key reused")
	}
}
