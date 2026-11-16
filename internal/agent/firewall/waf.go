package firewall

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
	"syscall"
)

// WAFLog is the edge's WAF log (Caddy writes it; see caddycfg.Render).
var WAFLog = "/srv/berth/caddy/logs/waf/waf.log"

// WAFEvent is one request the WAF flagged (log-only) or blocked.
type WAFEvent struct {
	TS      int64     `json:"ts"`
	ID      string    `json:"id"` // Coraza transaction id
	IP      string    `json:"ip"`
	Host    string    `json:"host,omitempty"`
	Site    int64     `json:"site,omitempty"`
	URI     string    `json:"uri"`
	Blocked bool      `json:"blocked"`
	Rules   []WAFRule `json:"rules"`
}

type WAFRule struct {
	ID       int    `json:"id"`
	Msg      string `json:"msg"`
	Severity string `json:"severity,omitempty"`
}

var (
	corazaField = regexp.MustCompile(`\[(id|msg|severity|uri|unique_id) "((?:[^"\\]|\\.)*)"\]`)
	corazaIP    = regexp.MustCompile(`^\[client "([^"]+)"\]`)
)

type wafLine struct {
	Level    string  `json:"level"`
	TS       float64 `json:"ts"`
	Msg      string  `json:"msg"`
	Hostname string  `json:"hostname"`
	URI      string  `json:"uri"`
	ClientIP string  `json:"client_ip"`
	UID      string  `json:"unique_id"`
}

// ParseWAF groups log lines into events by transaction. Rule hits ("Coraza: Warning…")
// name the rule; Berth's marker rule (msg berth-site-N) names the site; "WAF rule violation
// detected" means the request was blocked and carries the host.
func ParseWAF(r io.Reader) []WAFEvent {
	by := map[string]*WAFEvent{}
	var order []string
	get := func(uid string, ts float64) *WAFEvent {
		e := by[uid]
		if e == nil {
			e = &WAFEvent{ID: uid, TS: int64(ts), Rules: []WAFRule{}}
			by[uid] = e
			order = append(order, uid)
		}
		return e
	}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		var l wafLine
		if json.Unmarshal(sc.Bytes(), &l) != nil {
			continue
		}
		// rule hits are "Coraza: Warning…" at any level (no severity → warn); other
		// warn lines are Coraza's own chatter
		if l.Level != "error" && !strings.Contains(l.Msg, "Coraza:") {
			continue
		}
		if l.Msg == "WAF rule violation detected" && l.UID != "" {
			e := get(l.UID, l.TS)
			e.Blocked, e.Host, e.IP = true, l.Hostname, l.ClientIP
			if e.URI == "" {
				e.URI = l.URI
			}
			continue
		}
		if !strings.Contains(l.Msg, "Coraza:") {
			continue
		}
		f := map[string]string{}
		for _, m := range corazaField.FindAllStringSubmatch(l.Msg, -1) {
			if _, seen := f[m[1]]; !seen {
				f[m[1]] = strings.ReplaceAll(m[2], `\"`, `"`)
			}
		}
		if f["unique_id"] == "" {
			continue
		}
		e := get(f["unique_id"], l.TS)
		if m := corazaIP.FindStringSubmatch(l.Msg); m != nil && e.IP == "" {
			e.IP = m[1]
		}
		if e.URI == "" {
			e.URI = f["uri"]
		}
		if site, ok := strings.CutPrefix(f["msg"], "berth-site-"); ok {
			e.Site, _ = strconv.ParseInt(site, 10, 64)
			continue
		}
		id, _ := strconv.Atoi(f["id"])
		// CRS bookkeeping rules (anomaly score evaluation 949/959/980) are not findings
		if id >= 949000 && id < 950000 || id >= 959000 && id < 960000 || id >= 980000 && id < 981000 {
			continue
		}
		e.Rules = append(e.Rules, WAFRule{ID: id, Msg: f["msg"], Severity: f["severity"]})
	}
	var out []WAFEvent
	for _, uid := range order {
		if e := by[uid]; e.IP != "" && (len(e.Rules) > 0 || e.Blocked) {
			out = append(out, *e)
		}
	}
	return out
}

// ReadWAF returns the events after pos ("inode:offset"; empty = from the end, so old
// history is not replayed) and the position to continue from.
func ReadWAF(pos string) ([]WAFEvent, string, error) {
	f, err := os.Open(WAFLog)
	if err != nil {
		if os.IsNotExist(err) { // when it appears, read it from the start
			return nil, "0:0", nil
		}
		return nil, pos, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, pos, err
	}
	var ino uint64
	if sys, ok := st.Sys().(*syscall.Stat_t); ok {
		ino = uint64(sys.Ino)
	}
	start := st.Size()
	if a, b, ok := strings.Cut(pos, ":"); ok {
		pi, _ := strconv.ParseUint(a, 10, 64)
		off, _ := strconv.ParseInt(b, 10, 64)
		switch {
		case pi != ino || off > st.Size(): // rotated or truncated: read the new file whole
			start = 0
		default:
			start = off
		}
	}
	const max = 8 << 20 // a burst: the rest next time
	if st.Size()-start > max {
		start = st.Size() - max
	}
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return nil, pos, err
	}
	b, err := io.ReadAll(io.LimitReader(f, max))
	if err != nil {
		return nil, pos, err
	}
	// stop at the last complete line; a half-written one waits for next time
	if i := strings.LastIndexByte(string(b), '\n'); i >= 0 {
		b = b[:i+1]
	} else {
		b = nil
	}
	return ParseWAF(strings.NewReader(string(b))), fmt.Sprintf("%d:%d", ino, start+int64(len(b))), nil
}
