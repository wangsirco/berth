package accesslog

import (
	"encoding/gob"
	"hash/fnv"
	"net"
	"os"
	"regexp"
	"strings"
	"time"
)

// Who is behind a request: a person in a browser, or which kind of program.
var botKinds = []struct {
	kind string
	re   *regexp.Regexp
}{
	{"ai", regexp.MustCompile(`(?i)gptbot|chatgpt-user|oai-searchbot|claudebot|claude-web|anthropic|ccbot|perplexity|bytespider|amazonbot|google-extended|meta-externalagent|cohere-ai|diffbot|youbot|timpibot|imagesiftbot`)},
	{"search", regexp.MustCompile(`(?i)googlebot|google-inspectiontool|bingbot|baiduspider|yandex|duckduckbot|sogou|360spider|applebot|petalbot|seznambot|naver|daum|qwantify|mojeek`)},
	{"social", regexp.MustCompile(`(?i)facebookexternalhit|facebot|twitterbot|slackbot|discordbot|telegrambot|whatsapp|linkedinbot|pinterest|redditbot|skypeuripreview|wechat|line/`)},
	{"monitor", regexp.MustCompile(`(?i)uptime|pingdom|statuscake|monitor|healthcheck|kuma|site24x7|newrelic|datadog|better ?stack`)},
	{"scanner", regexp.MustCompile(`(?i)zgrab|masscan|nmap|scanner|nuclei|sqlmap|nikto|censys|shodan|expanse|internet-measurement|wpscan`)},
	{"tool", regexp.MustCompile(`(?i)curl/|wget/|python-requests|python-urllib|aiohttp|go-http-client|httpclient|axios|node-fetch|undici|okhttp|java/|libwww|postman|httpie|scrapy|headlesschrome|phantomjs`)},
	{"other", regexp.MustCompile(`(?i)bot|crawl|spider|slurp|fetch|preview`)},
}

// BotKind: "" for a person, else ai, search, social, monitor, scanner, tool or other.
func BotKind(ua string) string {
	if strings.TrimSpace(ua) == "" {
		return "other"
	}
	for _, b := range botKinds {
		if b.re.MatchString(ua) {
			return b.kind
		}
	}
	return ""
}

// visitorIP: the visitor's address. Caddy resolves client_ip through trusted proxies; for
// older lines (or before the edge trusted cloudflared) a private client_ip with a
// CF-Connecting-IP header means a Cloudflare tunnel: the header is the visitor.
func visitorIP(e *entry) string {
	ip := e.Request.ClientIP
	if ip == "" {
		ip = e.Request.RemoteIP
	}
	if p := net.ParseIP(ip); p == nil || p.IsPrivate() || p.IsLoopback() {
		if cf := net.ParseIP(strings.TrimSpace(first(e.Request.Headers["Cf-Connecting-Ip"]))); cf != nil {
			return cf.String()
		}
	}
	return ip
}

// seenSet remembers visitors (a hash of address and browser) per site, to tell new ones
// from returning ones. Kept for seenDays, at most seenMax per site.
type seenSet struct {
	Sites map[string]map[uint64]int32 // site → visitor hash → day last seen
	dirty bool
}

const (
	seenDays = 90
	seenMax  = 200000
)

func visitorHash(ip, ua string) uint64 {
	h := fnv.New64a()
	h.Write([]byte(ip + "|" + ua))
	return h.Sum64()
}

func today(t time.Time) int32 { return int32(t.Unix() / 86400) }

// isNew records the visitor and reports whether this site had not seen it before.
func (s *seenSet) isNew(site string, h uint64, day int32) bool {
	if s.Sites == nil {
		s.Sites = map[string]map[uint64]int32{}
	}
	m := s.Sites[site]
	if m == nil {
		m = map[uint64]int32{}
		s.Sites[site] = m
	}
	last, ok := m[h]
	if !ok || last < day-seenDays {
		if len(m) >= seenMax {
			s.prune(m, day)
		}
		m[h] = day
		s.dirty = true
		return true
	}
	if last != day {
		m[h] = day
		s.dirty = true
	}
	return false
}

func (s *seenSet) prune(m map[uint64]int32, day int32) {
	cut := day - seenDays
	for k, d := range m {
		if d < cut {
			delete(m, k)
		}
	}
	for k := range m { // still full: drop arbitrary old-ish entries
		if len(m) < seenMax*9/10 {
			break
		}
		delete(m, k)
	}
}

func loadSeen(path string) *seenSet {
	s := &seenSet{}
	if f, err := os.Open(path); err == nil {
		gob.NewDecoder(f).Decode(s)
		f.Close()
	}
	return s
}

func (s *seenSet) save(path string) {
	if !s.dirty {
		return
	}
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	if gob.NewEncoder(f).Encode(s) == nil && f.Close() == nil {
		os.Rename(tmp, path)
		s.dirty = false
	} else {
		f.Close()
	}
}
