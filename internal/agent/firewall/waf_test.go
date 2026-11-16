package firewall

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const wafSample = `{"level":"warn","ts":1794391735.69,"logger":"http.handlers.waf","msg":"key not found in collection, returning the original text","tx_id":"A","variable":"ARGS","key":"_charset_"}
{"level":"error","ts":1794391735.70,"logger":"http.handlers.waf","msg":"[client \"172.17.0.1\"] Coraza: Warning. SQL Injection Attack Detected via libinjection [file \"@owasp_crs/REQUEST-942-APPLICATION-ATTACK-SQLI.conf\"] [line \"5885\"] [id \"942100\"] [rev \"\"] [msg \"SQL Injection Attack Detected via libinjection\"] [data \"Matched Data: s&sos found within ARGS:id: 1' OR '1'='1\"] [severity \"critical\"] [ver \"OWASP_CRS/4.25.0\"] [hostname \"\"] [uri \"/?id=1%27\"] [unique_id \"avoj\"]"}
{"level":"error","ts":1794391735.70,"logger":"http.handlers.waf","msg":"[client \"172.17.0.1\"] Coraza: Warning. Inbound Anomaly Score Exceeded [id \"949110\"] [msg \"Inbound Anomaly Score Exceeded (Total Score: 5)\"] [severity \"emergency\"] [hostname \"\"] [uri \"/?id=1%27\"] [unique_id \"avoj\"]"}
{"level":"error","ts":1794391735.70,"logger":"http.handlers.waf","msg":"WAF rule violation detected","hostname":"shop.example.com","uri":"/?id=1%27","client_ip":"172.17.0.1","unique_id":"avoj"}
{"level":"warn","ts":1794391735.71,"logger":"http.handlers.waf","msg":"[client \"172.17.0.1\"] Coraza: Warning. berth-site-7 [id \"1999\"] [msg \"berth-site-7\"] [hostname \"\"] [uri \"/?id=1%27\"] [unique_id \"avoj\"]"}
{"level":"error","ts":1794391736.00,"logger":"http.handlers.waf","msg":"[client \"203.0.113.5\"] Coraza: Warning. XSS Attack Detected via libinjection [id \"941100\"] [msg \"XSS Attack Detected via libinjection\"] [severity \"critical\"] [hostname \"\"] [uri \"/?x=%3Cscript\"] [unique_id \"uWy\"]"}
{"level":"error","ts":1794391736.00,"logger":"http.handlers.waf","msg":"[client \"203.0.113.5\"] Coraza: Warning. berth-site-9 [id \"1999\"] [msg \"berth-site-9\"] [unique_id \"uWy\"]"}
`

func TestParseWAF(t *testing.T) {
	ev := ParseWAF(strings.NewReader(wafSample))
	if len(ev) != 2 {
		t.Fatalf("%+v", ev)
	}
	a, b := ev[0], ev[1]
	if !a.Blocked || a.Host != "shop.example.com" || a.Site != 7 || a.IP != "172.17.0.1" || len(a.Rules) != 1 || a.Rules[0].ID != 942100 || a.Rules[0].Severity != "critical" || a.TS != 1794391735 {
		t.Errorf("%+v", a)
	}
	if b.Blocked || b.Site != 9 || b.IP != "203.0.113.5" || b.Rules[0].ID != 941100 { // log-only: flagged, not blocked
		t.Errorf("%+v", b)
	}
}

func TestReadWAFPosition(t *testing.T) {
	WAFLog = filepath.Join(t.TempDir(), "waf.log")
	ev, pos, err := ReadWAF("")
	if err != nil || len(ev) != 0 || pos != "0:0" {
		t.Fatal(ev, pos, err)
	}
	os.WriteFile(WAFLog, []byte(wafSample), 0o644)
	if ev, _, _ = ReadWAF(pos); len(ev) != 2 { // the file appeared after we looked: read it whole
		t.Fatalf("new file: %d", len(ev))
	}
	ev, pos, _ = ReadWAF("") // first read of an existing file starts at the end: no replay
	if len(ev) != 0 || pos == "" {
		t.Fatal(ev, pos)
	}
	f, _ := os.OpenFile(WAFLog, os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString(wafSample + `{"level":"error","msg":"half a li`)
	f.Close()
	ev, pos2, _ := ReadWAF(pos)
	if len(ev) != 2 {
		t.Fatalf("%d events", len(ev))
	}
	ev, _, _ = ReadWAF(pos2)
	if len(ev) != 0 {
		t.Fatal("re-read")
	}
	// rotation: a new file is read whole
	os.Remove(WAFLog)
	os.WriteFile(WAFLog, []byte(wafSample), 0o644)
	if ev, _, _ = ReadWAF(pos2); len(ev) != 2 {
		t.Fatalf("after rotation: %d", len(ev))
	}
}
