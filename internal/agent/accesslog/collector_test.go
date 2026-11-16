package accesslog

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func logLine(ts float64, ip, uri string, status int, dur float64, ref string) string {
	return fmt.Sprintf(`{"level":"info","ts":%f,"logger":"http.log.access.site1","msg":"handled request","request":{"remote_ip":"%s","client_ip":"%s","method":"GET","host":"a.example.com","uri":"%s","headers":{"User-Agent":["Mozilla/5.0"],"Referer":["%s"]}},"duration":%f,"size":100,"status":%d}`+"\n",
		ts, ip, ip, uri, ref, dur, status)
}

func appendFile(t *testing.T, p, s string) {
	f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString(s)
	f.Close()
}

func total(b Batch) (req int64) {
	for _, m := range b.Minutes {
		req += m.Req
	}
	return
}

func TestCollectorRotationNoLossNoDup(t *testing.T) {
	dir := t.TempDir()
	state := filepath.Join(t.TempDir(), "state.json")
	logp := filepath.Join(dir, "a.example.com.log")
	base := float64(1794350000 - 1794350000%60)
	now := time.Unix(int64(base)+3600, 0)
	c := NewCollector(dir, state, func(ip net.IP) string { return "NZ" })
	c.Now = func() time.Time { return now }

	appendFile(t, logp, logLine(base+1, "203.0.113.1", "/", 200, 0.01, "https://news.example.org/x"))
	appendFile(t, logp, logLine(base+2, "203.0.113.2", "/a?b=1", 404, 0.03, ""))
	appendFile(t, logp, `{"level":"info","ts":`) // partial line: read next time
	c.Poll()
	// the partial line completes; then the file rotates and more lines go to the new one
	appendFile(t, logp, fmt.Sprintf(`%f,"request":{"client_ip":"203.0.113.3","uri":"/wp-login.php","headers":{}},"duration":3,"size":5,"status":404}`+"\n", base+61))
	appendFile(t, logp, logLine(base+62, "203.0.113.1", "/", 502, 0.2, ""))
	os.Rename(logp, filepath.Join(dir, "a.example.com-2026-11-11T00-00-00.000.log"))
	appendFile(t, logp, logLine(base+125, "203.0.113.9", "/", 200, 0.01, ""))
	c.Poll()

	b := c.Take("", 0)
	if got := total(b); got != 5 {
		t.Fatalf("requests %d want 5 (%d minutes)", got, len(b.Minutes))
	}
	m0 := b.Minutes[0]
	if m0.Status["2xx"] != 1 || m0.Status["4xx"] != 1 || m0.Refs["news.example.org"] != 1 || m0.Countries["NZ"] != 2 || m0.Uniq != 2 || m0.Paths["/a"] != 1 {
		t.Fatalf("minute 0: %+v", m0)
	}
	m1 := b.Minutes[1]
	if m1.Scans != 1 || m1.ScanPaths["/wp-login.php"] != 1 || len(m1.Slow) != 1 || m1.Status["5xx"] != 1 || m1.Lat[len(m1.Lat)-1] != 0 {
		t.Fatalf("minute 1: %+v", m1)
	}

	// a restarted agent resumes from the saved offsets: nothing is counted twice
	c2 := NewCollector(dir, state, nil)
	c2.Now = c.Now
	c2.Poll()
	if got := total(c2.Take("", 0)); got != 0 {
		t.Fatalf("re-read %d lines after restart", got)
	}

	// acknowledgement drops what the panel stored, only for the same boot
	if again := c.Take("other-boot", b.Seq); len(again.Minutes) != 3 {
		t.Fatal("ack from another boot applied")
	}
	if again := c.Take(b.Boot, b.Seq); len(again.Minutes) != 0 {
		t.Fatal("acknowledged minutes returned again")
	}
}

func TestScanDetection(t *testing.T) {
	for p, want := range map[string]bool{"/wp-login.php": true, "/.env": true, "/.git/config": true, "/index.php": true, "/api/users": false, "/": false, "/static/app.js": false} {
		if IsScan(p, 404) != want {
			t.Errorf("%s: %v", p, !want)
		}
	}
	if IsScan("/wp-login.php", 200) {
		t.Error("a real WordPress login counted as a scan")
	}
}
