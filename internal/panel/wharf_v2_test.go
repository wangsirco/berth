package panel

import (
	"testing"
	"time"

	"github.com/wangsirco/berth/internal/panel/wharfapi"
	"github.com/wangsirco/berth/internal/panel/wharfapi/wharftest"
)

// Wharf protocol v2: pairing from LAN discovery (ask → the router's admin allows → Berth
// finishes by itself), signing without tokens both ways, rotate, and .lan names on the router.
func TestWharfV2(t *testing.T) {
	c := newTC(t)
	c.withFakeAgent()
	wf := wharftest.New()
	defer wf.Close()
	wf.V2 = true
	c.mkUser("root", "correct horse 9", "super")
	c.login("root", "correct horse 9")
	run := func(kind string, p any) map[string]any {
		t.Helper()
		r, out := c.do("POST", "/api/v1/hosts/local/changes/"+kind, p)
		if r.StatusCode != 200 {
			t.Fatalf("%s: %d %v", kind, r.StatusCode, out)
		}
		id, _ := out["id"].(string)
		for i := 0; i < 100 && out["status"] != "done" && out["status"] != "failed"; i++ {
			time.Sleep(50 * time.Millisecond)
			_, out = c.do("GET", "/api/v1/changes/"+id, nil)
		}
		if out["status"] != "done" {
			t.Fatalf("%s: %v %v", kind, out["status"], out["result"])
		}
		return out
	}
	wait := func(what string, ok func() bool) {
		t.Helper()
		for i := 0; i < 200; i++ {
			if ok() {
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
		t.Fatalf("timed out: %s", what)
	}

	// ---- LAN request: the window closed → told to open it on Wharf ----
	if r, out := c.do("POST", "/api/v1/wharf/request", map[string]string{"addr": wf.URL}); r.StatusCode != 409 {
		t.Fatalf("closed window: %d %v", r.StatusCode, out)
	}
	wf.Open = true
	r, ask := c.do("POST", "/api/v1/wharf/request", map[string]string{"addr": wf.URL})
	if r.StatusCode != 200 || ask["state"] != "waiting" || len(ask["sas"].(string)) != 7 {
		t.Fatalf("request: %d %v", r.StatusCode, ask)
	}
	wf.Lock()
	var code string
	for k := range wf.Asks {
		code = k
	}
	urls := wf.AskURLs
	wf.Unlock()
	if ask["sas"] != wharfapi.SAS(code, c.app.caFP(), wf.FP) || len(urls) == 0 {
		t.Fatalf("sas %v / urls %v", ask["sas"], urls)
	}
	wf.Allow(true)
	wait("paired", func() bool { _, a := c.do("GET", "/api/v1/wharf/request", nil); return a["state"] == "paired" })
	l, err := c.app.wharf()
	if err != nil || l.inbound == "" || l.inbound != wf.BerthToken || len(l.TargetIPs) != 1 || l.TargetIPs[0] != "192.168.1.20" {
		t.Fatalf("link: %v %+v", err, l)
	}

	// ---- v2: Berth learns Wharf's protocol, then names its token by key id ----
	wait("key auth", func() bool { wf.Lock(); defer wf.Unlock(); return wf.KeyAuth })
	_, st := c.do("GET", "/api/v1/wharf", nil)
	if lk := st["link"].(map[string]any); lk["key_auth"] != true || lk["protocol"].(float64) != 2 {
		t.Fatalf("status: %v", lk)
	}
	wf.Lock()
	before := wf.Bearers
	wf.Unlock()
	c.app.wharfRefresh(t.Context(), true)
	wf.Lock()
	after := wf.Bearers
	wf.Unlock()
	if after != before {
		t.Fatal("token sent after the switch")
	}
	// Wharf calls back with X-Wharf-Key only
	if code, out := wf.CallBerth(c.srv.Client(), c.srv.URL, "GET", "/ping", nil); code != 200 || out["protocol"].(float64) != 2 {
		t.Fatalf("ping: %d %v", code, out)
	}
	if l, _ := c.app.wharf(); !l.WharfKeyAuth {
		t.Fatal("wharf_key_auth not recorded")
	}

	// ---- rotate: new tokens both ways ----
	newOut := "wharf_bth_" + "0123456789abcdef0123456789"
	code2, out := wf.CallBerth(c.srv.Client(), c.srv.URL, "POST", "/rotate", map[string]string{"token": newOut})
	if code2 != 200 {
		t.Fatalf("rotate: %d %v", code2, out)
	}
	wf.Lock()
	wf.BerthToken, wf.Token = out["token"].(string), newOut
	wf.Unlock()
	if code, out := wf.CallBerth(c.srv.Client(), c.srv.URL, "POST", "/events", map[string]any{"type": "link.updated", "scopes": []string{"forwards", "dns"}}); code != 200 {
		t.Fatalf("event after rotate: %d %v", code, out)
	}
	if l, _ := c.app.wharf(); len(l.Scopes) != 2 || l.token != newOut {
		t.Fatalf("after rotate/link.updated: %+v", l.Scopes)
	}

	// ---- .lan names: registered on the router with the local name, dropped with it ----
	_, ln := c.do("GET", "/api/v1/local-name", nil)
	if ln["wharf_dns"] == nil {
		t.Fatalf("local-name: %v", ln)
	}
	run("panel.local_name", map[string]string{"name": "mini.lan"})
	wf.Lock()
	_, has := wf.DNS["mini.lan"]
	wf.Unlock()
	if l, _ := c.app.wharf(); !has || l.DNSName != "mini.lan" {
		t.Fatalf("dns: %v %q", wf.DNS, l.DNSName)
	}
	run("panel.local_name", map[string]string{"name": "nas.lan"})
	wf.Lock()
	_, old := wf.DNS["mini.lan"]
	_, nw := wf.DNS["nas.lan"]
	wf.Unlock()
	if old || !nw {
		t.Fatalf("rename: %v", wf.DNS)
	}
	run("panel.local_name", map[string]string{"name": ""})
	wf.Lock()
	n := len(wf.DNS)
	wf.Unlock()
	if n != 0 {
		t.Fatalf("clear: %v", wf.DNS)
	}

	// ---- unpair: POST /unpair on Wharf ----
	run("wharf.unpair", map[string]any{"keep": true})
	if wf.Paired || wf.Unpaired != "true" {
		t.Fatalf("unpair: %v %q", wf.Paired, wf.Unpaired)
	}
}
