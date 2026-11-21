// Package accesslog turns the edge's per-site JSON access logs into per-minute
// statistics: requests, bytes, status classes, latency buckets, unique visitors,
// countries, top paths and referrers, slow requests and scanner probes.
package accesslog

import (
	"encoding/json"
	"net"
	"net/url"
	"regexp"
	"sort"
	"strings"
)

// LatencyBounds are the upper bounds (ms) of the latency buckets; the last is open.
var LatencyBounds = []float64{25, 50, 100, 250, 500, 1000, 2500, 5000}

const (
	topKeep     = 20
	slowAfterMS = 2000
	slowKeep    = 10
)

type Slow struct {
	TS     int64  `json:"ts"`
	Method string `json:"method"`
	Path   string `json:"path"`
	Status int    `json:"status"`
	MS     int64  `json:"ms"`
}

// Minute is one site's traffic in one minute.
type Minute struct {
	TS        int64            `json:"ts"` // start of the minute (unix seconds)
	Site      string           `json:"site"`
	Req       int64            `json:"req"`
	Bytes     int64            `json:"bytes"`
	Status    map[string]int64 `json:"status"` // 2xx 3xx 4xx 5xx
	Lat       []int64          `json:"lat"`    // counts per LatencyBounds bucket (+1 open)
	Countries map[string]int64 `json:"countries,omitempty"`
	Paths     map[string]int64 `json:"paths,omitempty"`
	Refs      map[string]int64 `json:"refs,omitempty"`
	Scans     int64            `json:"scans"`
	ScanPaths map[string]int64 `json:"scan_paths,omitempty"`
	Bots      int64            `json:"bots"`
	BotKinds  map[string]int64 `json:"bot_kinds,omitempty"` // requests by ai, search, social, monitor, scanner, tool, other
	HumanReq  int64            `json:"human_req"`           // requests from people (browsers)
	Humans    int64            `json:"humans"`              // unique people
	HLLHuman  []byte           `json:"hll_human,omitempty"`
	New       int64            `json:"new"` // people this site had not seen in the last 90 days
	Slow      []Slow           `json:"slow,omitempty"`
	Uniq      int64            `json:"uniq"`
	HLL       []byte           `json:"hll,omitempty"` // packed registers, for merging into hours

	hll      *HLL
	hllHuman *HLL
}

func newMinute(site string, ts int64) *Minute {
	return &Minute{TS: ts, Site: site, Status: map[string]int64{}, Lat: make([]int64, len(LatencyBounds)+1),
		Countries: map[string]int64{}, Paths: map[string]int64{}, Refs: map[string]int64{}, ScanPaths: map[string]int64{}, BotKinds: map[string]int64{}, hll: NewHLL(), hllHuman: NewHLL()}
}

// close packs the visitor counter and trims the top lists.
func (m *Minute) close() {
	m.Uniq = m.hll.Count()
	m.HLL = m.hll.Pack()
	m.hll = nil
	if m.hllHuman != nil {
		m.Humans = m.hllHuman.Count()
		m.HLLHuman = m.hllHuman.Pack()
		m.hllHuman = nil
	}
	m.Paths = Top(m.Paths, topKeep)
	m.Refs = Top(m.Refs, topKeep)
	m.ScanPaths = Top(m.ScanPaths, topKeep)
}

// Top keeps the n largest entries (ties by key, for stable output).
func Top(m map[string]int64, n int) map[string]int64 {
	if len(m) <= n {
		return m
	}
	type kv struct {
		k string
		v int64
	}
	l := make([]kv, 0, len(m))
	for k, v := range m {
		l = append(l, kv{k, v})
	}
	sort.Slice(l, func(i, j int) bool { return l[i].v > l[j].v || (l[i].v == l[j].v && l[i].k < l[j].k) })
	out := make(map[string]int64, n)
	for _, e := range l[:n] {
		out[e.k] = e.v
	}
	return out
}

// scanRe matches paths that only scanners ask for (counted when the answer is an error).
var scanRe = regexp.MustCompile(`(?i)(^/(wp-login\.php|wp-admin|xmlrpc\.php|wp-content/plugins|wp-includes|phpmyadmin|pma|myadmin|admin\.php|administrator|cgi-bin|boaform|hnap1|actuator|solr|owa|autodiscover|server-status|vendor/phpunit|console|manager/html|shell|setup\.cgi|geoserver|telescope|_ignition|webui|remote/fgt_lang|global-protect|dana-na|\+cscoe\+)|/\.(env|git|svn|hg|aws|ssh|ds_store|htpasswd|vscode|idea)(/|$)|\.(php|asp|aspx|jsp|cgi)$|/(config|credentials|secrets?|backup|db|database|dump)\.(json|ya?ml|sql|zip|tar\.gz|bak)$)`)

func IsScan(path string, status int) bool { return status >= 400 && scanRe.MatchString(path) }

type entry struct {
	TS      float64 `json:"ts"`
	Request struct {
		RemoteIP string              `json:"remote_ip"`
		ClientIP string              `json:"client_ip"`
		Method   string              `json:"method"`
		Host     string              `json:"host"`
		URI      string              `json:"uri"`
		Headers  map[string][]string `json:"headers"`
	} `json:"request"`
	Duration float64 `json:"duration"` // seconds
	Size     int64   `json:"size"`
	Status   int     `json:"status"`
}

// Country maps an address to a two-letter code ("" unknown, "LAN" private).
type Country func(ip net.IP) string

// add counts one log line into m. seen (may be nil) tells first-time visitors apart.
func (m *Minute) add(e *entry, country Country, seen *seenSet) {
	m.Req++
	m.Bytes += e.Size
	switch {
	case e.Status >= 500:
		m.Status["5xx"]++
	case e.Status >= 400:
		m.Status["4xx"]++
	case e.Status >= 300:
		m.Status["3xx"]++
	default:
		m.Status["2xx"]++
	}
	ms := e.Duration * 1000
	i := sort.SearchFloat64s(LatencyBounds, ms)
	m.Lat[i]++
	ipStr := visitorIP(e)
	ua := first(e.Request.Headers["User-Agent"])
	m.hll.Add(ipStr + "|" + ua)
	if k := BotKind(ua); k != "" {
		m.Bots++
		if m.BotKinds == nil {
			m.BotKinds = map[string]int64{}
		}
		m.BotKinds[k]++
	} else {
		m.HumanReq++
		if m.hllHuman == nil {
			m.hllHuman = NewHLL()
		}
		m.hllHuman.Add(ipStr + "|" + ua)
		if seen != nil && seen.isNew(m.Site, visitorHash(ipStr, ua), int32(int64(e.TS)/86400)) {
			m.New++
		}
	}
	if country != nil {
		if ip := net.ParseIP(ipStr); ip != nil {
			if c := country(ip); c != "" {
				m.Countries[c]++
			}
		}
	}
	path := e.Request.URI
	if i := strings.IndexByte(path, '?'); i >= 0 {
		path = path[:i]
	}
	if len(path) > 200 {
		path = path[:200]
	}
	if IsScan(path, e.Status) {
		m.Scans++
		m.ScanPaths[path]++
	} else {
		m.Paths[path]++
	}
	if ref := first(e.Request.Headers["Referer"]); ref != "" {
		if u, err := url.Parse(ref); err == nil && u.Host != "" && !strings.EqualFold(u.Hostname(), e.Request.Host) {
			m.Refs[strings.ToLower(u.Hostname())]++
		}
	}
	if ms >= slowAfterMS {
		m.Slow = append(m.Slow, Slow{TS: int64(e.TS), Method: e.Request.Method, Path: path, Status: e.Status, MS: int64(ms)})
		if len(m.Slow) > slowKeep {
			sort.Slice(m.Slow, func(i, j int) bool { return m.Slow[i].MS > m.Slow[j].MS })
			m.Slow = m.Slow[:slowKeep]
		}
	}
	// guard the maps against floods of unique paths
	if len(m.Paths) > 2000 {
		m.Paths = Top(m.Paths, 500)
	}
	if len(m.ScanPaths) > 500 {
		m.ScanPaths = Top(m.ScanPaths, 100)
	}
}

func first(l []string) string {
	if len(l) > 0 {
		return l[0]
	}
	return ""
}

// parse reads one access log line; other lines (errors, startup) are skipped.
func parse(line []byte) (*entry, bool) {
	if len(line) == 0 || line[0] != '{' {
		return nil, false
	}
	var e entry
	if json.Unmarshal(line, &e) != nil || e.Status == 0 || e.TS == 0 {
		return nil, false
	}
	return &e, true
}

func minuteOf(ts float64) int64 {
	t := int64(ts)
	return t - t%60
}
