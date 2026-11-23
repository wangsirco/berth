package panel

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/wangsirco/berth/internal/panel/wharfapi/wharftest"
	"github.com/wangsirco/berth/internal/sign"
)

func TestWharfLink(t *testing.T) {
	c := newTC(t)
	c.withFakeAgent()
	wf := wharftest.New()
	defer wf.Close()
	c.mkUser("root", "correct horse 9", "super")
	c.login("root", "correct horse 9")
	run := func(kind string, p any) map[string]any {
		t.Helper()
		r, out := c.do("POST", "/api/v1/hosts/local/changes/"+kind, p)
		if r.StatusCode != 200 {
			t.Fatalf("%s: %d %v", kind, r.StatusCode, out)
		}
		id, _ := out["id"].(string)
		for i := 0; i < 60 && out["status"] != "done" && out["status"] != "failed"; i++ {
			time.Sleep(50 * time.Millisecond)
			_, out = c.do("GET", "/api/v1/changes/"+id, nil)
		}
		if out["status"] != "done" {
			t.Fatalf("%s: %v %v", kind, out["status"], out["result"])
		}
		return out
	}

	// ---- pairing ----
	_, pr := c.do("POST", "/api/v1/wharf/probe", map[string]string{"url": wf.URL})
	chain := pr["chain"].([]any)
	if chain[0].(map[string]any)["fp"] != wf.FP {
		t.Fatalf("probe: %v", pr)
	}
	_, st := c.do("POST", "/api/v1/wharf/pair/start", map[string]string{"url": wf.URL, "fp": wf.FP})
	red, _ := url.Parse(st["redirect"].(string))
	code := red.Query().Get("code")
	if cb, bu := red.Query().Get("cb"), red.Query().Get("berth_url"); !strings.HasPrefix(cb, bu+"/") { // Wharf requires the same host and port
		t.Fatalf("callback %s is not on berth_url %s", cb, bu)
	}
	if !strings.Contains(red.Query().Get("berth_urls"), red.Query().Get("berth_url")) {
		t.Fatalf("berth_urls: %v", red.Query())
	}
	if !strings.HasPrefix(st["redirect"].(string), wf.URL+"/pair/berth?") || red.Query().Get("berth_fp") != c.app.caFP() || code == "" {
		t.Fatalf("redirect: %v", st)
	}
	wf.Code = code // Wharf's approval page hands the same code back
	if r, _ := c.do("POST", "/api/v1/wharf/pair/finish", map[string]string{"code": "wrong"}); r.StatusCode != 400 {
		t.Fatal("wrong code accepted")
	}
	// Wharf sends the browser back to cb: a browser with no session there (another address);
	// Berth finishes by itself and sends it home
	plain := &http.Client{Transport: c.srv.Client().Transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := plain.Get(red.Query().Get("cb") + "?code=" + url.QueryEscape(code))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if loc := resp.Header.Get("Location"); resp.StatusCode != 302 || !strings.HasPrefix(loc, c.srv.URL+"/local/wharf?paired=") || strings.Contains(loc, "error") {
		t.Fatalf("callback: %d %s", resp.StatusCode, loc)
	}
	if !strings.HasPrefix(wf.BerthToken, "berth_wrf_") || wf.BerthFP != c.app.caFP() {
		t.Fatalf("Wharf got %q %q", wf.BerthToken, wf.BerthFP)
	}
	if r, _ := c.do("POST", "/api/v1/wharf/pair/start", map[string]string{"url": wf.URL, "fp": wf.FP}); r.StatusCode != 409 {
		t.Fatal("paired twice")
	}
	_, sb := c.do("GET", "/api/v1/statusbar?host=local", nil)
	if sb["wharf"] == nil {
		t.Fatal("status bar does not show Wharf")
	}

	// ---- publishing a site through a mapping, undone when the site goes ----
	site := run("site.create", map[string]any{"domains": []string{"vault.example.net"}, "target": map[string]any{"type": "static"}, "tls": map[string]any{"mode": "internal"}})
	sid := site["result"].(map[string]any)["id"].(float64)
	run("wharf.publish", map[string]any{"site_id": sid, "mode": "forward", "wan_port": "8443"})
	if len(wf.Forwards) != 1 {
		t.Fatal("no forward at Wharf")
	}
	for _, f := range wf.Forwards {
		if f.TargetIP != "192.168.1.20" || f.TargetPort != "443" || f.BerthRef != "site:vault.example.net" || f.Kind != "berth" {
			t.Fatalf("%+v", f)
		}
	}
	if len(wf.Actors) == 0 || !strings.Contains(wf.Actors[len(wf.Actors)-1], "vault.example.net") {
		t.Errorf("actor not passed: %v", wf.Actors)
	}
	// a second site on the same WAN port: Wharf's conflict shows in the preview
	site2 := run("site.create", map[string]any{"domains": []string{"wiki.example.net"}, "target": map[string]any{"type": "static"}, "tls": map[string]any{"mode": "internal"}})
	sid2 := site2["result"].(map[string]any)["id"].(float64)
	if r, out := c.do("POST", "/api/v1/hosts/local/changes/wharf.publish?plan=1", map[string]any{"site_id": sid2, "mode": "forward", "wan_port": "8443"}); r.StatusCode == 200 || !strings.Contains(out["error"].(map[string]any)["message"].(string), "已被映射") {
		t.Fatalf("conflict not shown: %d %v", r.StatusCode, out)
	}
	run("site.delete", map[string]any{"id": sid})
	nfw := func() int { wf.Lock(); defer wf.Unlock(); return len(wf.Forwards) }
	for i := 0; i < 40 && nfw() > 0; i++ {
		time.Sleep(50 * time.Millisecond)
	}
	if nfw() != 0 {
		t.Fatal("mapping left after deleting the site")
	}

	// ---- port check, and mapping a bare port ----
	_, pc := c.do("GET", "/api/v1/wharf/port-check?port=8000", nil)
	if len(pc["forwards"].([]any)) != 0 || pc["ok"] != true || pc["target_ip"] != "192.168.1.20" {
		t.Fatalf("free port: %v", pc)
	}
	nf := len(wf.Forwards)
	fwd := run("wharf.forward", map[string]any{"wan_port": "8000"})
	if len(wf.Forwards) != nf+1 {
		t.Fatalf("not mapped: %v", wf.Forwards)
	}
	_, pc = c.do("GET", "/api/v1/wharf/port-check?port=8000", nil)
	if fw := pc["forwards"].([]any); len(fw) != 1 || fw[0].(map[string]any)["mine"] != true || pc["ok"] != false || len(pc["issues"].([]any)) != 1 {
		t.Fatalf("taken port: %v", pc)
	}
	c.do("POST", "/api/v1/changes/"+fwd["id"].(string)+"/revert", nil)
	for i := 0; i < 40 && len(wf.Forwards) != nf; i++ {
		time.Sleep(50 * time.Millisecond)
	}
	if len(wf.Forwards) != nf {
		t.Fatalf("undo left the mapping: %v", wf.Forwards)
	}

	// ---- 443 SNI registration ----
	if r, _ := c.do("POST", "/api/v1/hosts/local/changes/wharf.publish", map[string]any{"site_id": sid2, "mode": "sni"}); r.StatusCode == 200 {
		time.Sleep(200 * time.Millisecond)
		if len(wf.SNI.Domains) != 0 {
			t.Fatal("registered while 443 is free")
		}
	}
	wf.ProxyFront = true
	pub := run("wharf.publish", map[string]any{"site_id": sid2, "mode": "sni"})
	if len(wf.SNI.Domains) != 1 || wf.SNI.Domains[0] != "wiki.example.net" || wf.SNI.Target != "192.168.1.20:443" {
		t.Fatalf("%+v", wf.SNI)
	}
	if r, _ := c.do("POST", "/api/v1/changes/"+pub["id"].(string)+"/revert", nil); r.StatusCode != 200 {
		t.Fatal("revert")
	}
	time.Sleep(300 * time.Millisecond)
	if len(wf.SNI.Domains) != 0 {
		t.Fatalf("SNI left after undo: %+v", wf.SNI)
	}

	// ---- an app's public ports (RustDesk relay): one forward per port and protocol, undoable ----
	run("app.install", map[string]any{"app": "rustdesk-server", "name": "rd"})
	before := len(wf.Forwards)
	ap := run("app.publish", map[string]any{"name": "rd"})
	got := map[string]bool{}
	for _, f := range wf.Forwards {
		if f.BerthRef == "app:rd" {
			got[f.WANPort+"/"+f.Proto] = f.TargetPort == f.WANPort && f.TargetIP == "192.168.1.20"
		}
	}
	if len(wf.Forwards) != before+4 || !got["21116/tcp"] || !got["21116/udp"] || !got["21117/tcp"] || !got["21115/tcp"] {
		t.Fatalf("app forwards: %v", got)
	}
	_, g := c.do("GET", "/api/v1/hosts/local/apps/rd/guide", nil)
	if pubs, _ := g["published"].([]any); len(pubs) != 4 {
		t.Fatalf("guide: %v", g)
	}
	if r, _ := c.do("POST", "/api/v1/changes/"+ap["id"].(string)+"/revert", nil); r.StatusCode != 200 {
		t.Fatal("revert app.publish")
	}
	time.Sleep(300 * time.Millisecond)
	if len(wf.Forwards) != before {
		t.Fatalf("app forwards left after undo: %d", len(wf.Forwards))
	}

	// ---- bans: Berth → Wharf, withdrawn when lifted ----
	b := run("ban.add", map[string]any{"addr": "203.0.113.77", "minutes": 60, "reason": "SSH"})
	bid := b["result"].(map[string]any)["id"].(float64)
	run("ban.sync", map[string]any{"id": bid, "wharf": true})
	if len(wf.Bans) != 1 {
		t.Fatal("ban not at Wharf")
	}
	for _, x := range wf.Bans {
		if x.CIDR != "203.0.113.77/32" || !strings.HasPrefix(x.Source, "berth:") || x.ExpiresAt == "" {
			t.Fatalf("%+v", x)
		}
	}
	run("ban.remove", map[string]any{"id": bid})
	c.app.wharfBanSweep(t.Context())
	if len(wf.Bans) != 0 {
		t.Fatal("Wharf ban left after unban")
	}

	// ---- bans: Wharf → Berth (token, signature, replay, safety) ----
	hc := c.srv.Client()
	code2, out := wf.CallBerth(hc, c.srv.URL, "POST", "/bans", map[string]any{"cidr": "198.51.100.9/32", "reason": "扫描", "source": "wharf:fwd"})
	if code2 != 200 {
		t.Fatalf("ban in: %d %v", code2, out)
	}
	inID := out["id"].(string)
	var n int
	c.app.db.QueryRow(`SELECT COUNT(*) FROM bans WHERE source='wharf' AND addr='198.51.100.9' AND revoked_at=0`).Scan(&n)
	if n != 1 {
		t.Fatal("ban from Wharf not stored")
	}
	if code2, _ := wf.CallBerth(hc, c.srv.URL, "POST", "/bans", map[string]any{"cidr": "192.168.1.0/24"}); code2 != 400 {
		t.Fatal("private range accepted")
	}
	if code2, _ := wf.CallBerth(hc, c.srv.URL, "DELETE", "/bans/"+inID, nil); code2 != 200 {
		t.Fatal("unban from Wharf")
	}
	// forged token / replayed request
	forge := func(tok string, again bool) int {
		body := []byte(`{"cidr":"198.51.100.10"}`)
		req, _ := http.NewRequest("POST", c.srv.URL+"/api/ext/wharf/v1/bans", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+tok)
		sign.SignPrefixed(req, "X-Wharf", sign.DeriveKey(tok), body)
		resp, _ := hc.Do(req)
		resp.Body.Close()
		if !again {
			return resp.StatusCode
		}
		req2, _ := http.NewRequest("POST", c.srv.URL+"/api/ext/wharf/v1/bans", bytes.NewReader(body))
		req2.Header = req.Header.Clone()
		resp2, _ := hc.Do(req2)
		resp2.Body.Close()
		return resp2.StatusCode
	}
	if forge("berth_wrf_forged", false) != 401 {
		t.Fatal("forged token accepted")
	}
	if forge(wf.BerthToken, true) != 401 {
		t.Fatal("replay accepted")
	}

	// ---- alerts both ways ----
	run("wharf.alerts_primary", map[string]any{"primary": "wharf"})
	r, _ := c.app.alertRule("container_down")
	c.app.fire(r, "local/web", "local", map[string]any{"container": "web", "host": "local", "state": "exited"})
	if len(wf.Alerts) != 1 || !strings.HasPrefix(wf.Alerts[0].Title, "[Berth") || wf.Alerts[0].Level != "critical" {
		t.Fatalf("handover: %+v", wf.Alerts)
	}
	run("wharf.alerts_primary", map[string]any{"primary": "berth"})
	for i := 0; i < 2; i++ {
		if code2, out := wf.CallBerth(hc, c.srv.URL, "POST", "/alerts", map[string]any{"level": "warn", "title": "WAN 掉线", "body": "PPPoE 重拨", "dedupe_key": "wan"}); code2 != 200 {
			t.Fatalf("alert in: %v", out)
		}
	}
	c.app.db.QueryRow(`SELECT COUNT(*) FROM alert_events WHERE rule='wharf_alert'`).Scan(&n)
	if n != 1 {
		t.Fatalf("%d Wharf alerts, want 1 (deduped)", n)
	}

	// ---- one-time sign-in both ways ----
	_, sso := c.do("POST", "/api/v1/wharf/sso", map[string]string{"return_to": "/forwards"})
	if !strings.HasPrefix(sso["url"].(string), wf.URL+"/sso/berth?t=") {
		t.Fatalf("%v", sso)
	}
	if code2, _ := wf.CallBerth(hc, c.srv.URL, "POST", "/sso/ticket", map[string]string{"wharf_user": "admin", "return_to": "/local/sites"}); code2 != 403 {
		t.Fatal("ticket without a user mapping")
	}
	c.do("PUT", "/api/v1/wharf/users", map[string]string{"wharf_user": "admin", "berth_user": "root"})
	_, tk := wf.CallBerth(hc, c.srv.URL, "POST", "/sso/ticket", map[string]string{"wharf_user": "admin", "return_to": "/local/sites"})
	link := tk["url"].(string)
	jar, _ := cookiejar.New(nil)
	fresh := &http.Client{Transport: hc.Transport, Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err = fresh.Get(link)
	if err != nil || resp.StatusCode != 302 || resp.Header.Get("Location") != "/local/sites" {
		t.Fatalf("sso: %v %v", err, resp)
	}
	resp, _ = fresh.Get(c.srv.URL + "/api/v1/me")
	var me map[string]any
	json.NewDecoder(resp.Body).Decode(&me)
	resp.Body.Close()
	if me["username"] != "root" {
		t.Fatalf("not signed in after SSO: %v", me)
	}
	resp, _ = fresh.Get(link)
	if resp.Header.Get("Location") != "/login?sso=expired" {
		t.Fatal("ticket used twice")
	}

	// ---- device page data, then unpair ----
	if code2, out := wf.CallBerth(hc, c.srv.URL, "GET", "/host", nil); code2 != 200 || out["sites"] == nil {
		t.Fatalf("host: %v", out)
	}
	run("wharf.unpair", map[string]any{"keep": false})
	if wf.Paired || wf.Unpaired != "false" {
		t.Fatal("Wharf not told")
	}
	if code2, _ := wf.CallBerth(hc, c.srv.URL, "GET", "/host", nil); code2 != 401 {
		t.Fatal("old token still works")
	}
}
