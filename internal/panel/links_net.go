package panel

// How linked panels reach each other, and what the link looks like from here: the address
// that answers, LAN or public, direct or through a proxy / CDN (whose certificate is not the
// peer's own), the peer's IP and country, and the round-trip time.

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"github.com/wangsirco/berth/internal/panel/wharfapi"
	"net"
	"net/http"
	"net/http/httptrace"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/wangsirco/berth/internal/tlsutil"
)

var linkClients = struct {
	sync.Mutex
	m map[string]*http.Client
}{m: map[string]*http.Client{}}

// linkClient trusts the peer's own certificate (pinned) or a valid public one for the name,
// and speaks HTTP/1.1 so WebSocket upgrades (terminals, screens) pass through.
func linkClient(fp string) *http.Client {
	linkClients.Lock()
	defer linkClients.Unlock()
	if c, ok := linkClients.m[fp]; ok {
		return c
	}
	tr := &http.Transport{
		TLSClientConfig:     tlsutil.PinOrPublic(fp),
		DialContext:         (&net.Dialer{Timeout: 6 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout: 8 * time.Second,
		IdleConnTimeout:     90 * time.Second,
		MaxIdleConnsPerHost: 8,
		ForceAttemptHTTP2:   false,
		TLSNextProto:        map[string]func(string, *tls.Conn) http.RoundTripper{}, // HTTP/1.1 only
	}
	c := &http.Client{Transport: tr, CheckRedirect: wharfapi.NoAccessRedirect} // a peer behind Cloudflare Access: say so
	linkClients.m[fp] = c
	return c
}

// linkLive is what the last probe found.
type linkLive struct {
	Online  bool       `json:"online"`
	Addr    string     `json:"addr,omitempty"`  // the base URL that answered
	IP      string     `json:"ip,omitempty"`    // the peer's address as connected
	Route   string     `json:"route,omitempty"` // lan | tailscale | public
	Via     string     `json:"via,omitempty"`   // direct | cdn | proxy (a certificate that is not the peer's own)
	CDN     string     `json:"cdn,omitempty"`   // e.g. Cloudflare
	Issuer  string     `json:"issuer,omitempty"`
	RTT     float64    `json:"rtt_ms,omitempty"`
	Jitter  float64    `json:"jitter_ms,omitempty"`
	Country string     `json:"country,omitempty"`
	Error   string     `json:"error,omitempty"`
	Checked int64      `json:"checked"`
	Hosts   []peerHost `json:"hosts,omitempty"` // main side: the peer's servers handed over
	PeerVer string     `json:"peer_version,omitempty"`
	samples []float64
}

type peerHost struct {
	Name    string `json:"name"`
	Online  bool   `json:"online"`
	Version string `json:"version,omitempty"`
}

// linkStates: what the probes found, per panel (two panels share a process in tests)
type linkStates struct {
	sync.Mutex
	m map[string]*linkLive
}

func liveKey(l *panelLink) string { return l.Role + ":" + l.UID }

func (a *App) linkState() *linkStates {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.links_ == nil {
		a.links_ = &linkStates{m: map[string]*linkLive{}}
	}
	return a.links_
}

func (a *App) liveOf(l *panelLink) linkLive {
	st := a.linkState()
	st.Lock()
	defer st.Unlock()
	if x, ok := st.m[liveKey(l)]; ok {
		return *x
	}
	return linkLive{}
}

// routeOf classifies an address: the LAN, Tailscale (100.64.0.0/10, encrypted end to end), or the internet.
func routeOf(ip net.IP) string {
	if ip == nil {
		return ""
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() {
		return "lan"
	}
	if _, cg, _ := net.ParseCIDR("100.64.0.0/10"); cg.Contains(ip) {
		return "tailscale"
	}
	if strings.HasPrefix(ip.String(), "fd7a:115c:a1e0:") {
		return "tailscale"
	}
	return "public"
}

// probeLink pings the peer on each address (the last good first), measures the round
// trip on a warm connection, and notes how it got there.
func (a *App) probeLink(ctx context.Context, l *panelLink) linkLive {
	out := linkLive{Checked: time.Now().Unix()}
	for _, base := range a.linkOrder(l) {
		var ip net.IP
		var state *tls.ConnectionState
		var hdr http.Header
		trace := &httptrace.ClientTrace{GotConn: func(i httptrace.GotConnInfo) {
			if ta, ok := i.Conn.RemoteAddr().(*net.TCPAddr); ok {
				ip = ta.IP
			}
		}}
		ping := func() (time.Duration, error) {
			c, cancel := context.WithTimeout(httptrace.WithClientTrace(ctx, trace), 6*time.Second)
			defer cancel()
			req, _ := http.NewRequestWithContext(c, "POST", base+"/link-api/ping", strings.NewReader("{}"))
			req.Header.Set("Content-Type", "application/json")
			linkSign(req, l, []byte("{}"))
			t0 := time.Now()
			resp, err := linkClient(l.FP).Do(req)
			if err != nil {
				return 0, err
			}
			d := time.Since(t0)
			state, hdr = resp.TLS, resp.Header
			var pong struct {
				Version string `json:"version"`
			}
			if err := readLinkResp(resp, &pong); err != nil {
				return 0, err
			}
			out.PeerVer = pong.Version
			return d, nil
		}
		if _, err := ping(); err != nil { // the first opens the connection
			out.Error = err.Error()
			continue
		}
		var best time.Duration
		for i := 0; i < 3; i++ {
			if d, err := ping(); err == nil && (best == 0 || d < best) {
				best = d
			}
		}
		out.Online, out.Addr, out.Error = true, base, ""
		out.RTT = float64(best.Microseconds()) / 1000
		if ip != nil {
			out.IP = ip.String()
			out.Route = routeOf(ip)
		}
		out.Via = "direct"
		if state != nil && !pinMatches(state, l.FP) {
			out.Via = "proxy"
			if len(state.PeerCertificates) > 0 {
				out.Issuer = state.PeerCertificates[0].Issuer.CommonName
				if len(state.PeerCertificates[0].Issuer.Organization) > 0 {
					out.Issuer = state.PeerCertificates[0].Issuer.Organization[0]
				}
			}
			switch {
			case hdr.Get("Cf-Ray") != "" || strings.EqualFold(hdr.Get("Server"), "cloudflare"):
				out.Via, out.CDN = "cdn", "Cloudflare"
			case hdr.Get("X-Amz-Cf-Id") != "":
				out.Via, out.CDN = "cdn", "CloudFront"
			case strings.Contains(strings.ToLower(hdr.Get("Server")), "fastly") || hdr.Get("X-Served-By") != "":
				out.Via, out.CDN = "cdn", "Fastly"
			}
		}
		if base != l.LastAddr {
			a.db.Exec(`UPDATE panel_links SET last_addr=? WHERE id=?`, base, l.ID)
			l.LastAddr = base
		}
		break
	}
	if out.Online && out.Route == "public" && out.IP != "" {
		out.Country = a.countryOf(ctx, out.IP)
	}
	return out
}

func pinMatches(cs *tls.ConnectionState, fp string) bool {
	want := strings.ToLower(strings.ReplaceAll(fp, ":", ""))
	for _, c := range cs.PeerCertificates {
		h := sha256.Sum256(c.Raw)
		if hex.EncodeToString(h[:]) == want {
			return true
		}
	}
	return false
}

// countryOf asks the local agent's country database (no third party learns the address).
func (a *App) countryOf(ctx context.Context, ip string) string {
	var got map[string]string
	c, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if a.call(c, "local", "POST", "/v1/geo", []string{ip}, &got) == nil {
		return got[ip]
	}
	return ""
}

// linkProber checks every active link every 30 seconds (both sides), and on the main side
// also which of the handed-over servers are online.
func (a *App) linkProber(ctx context.Context) {
	tick := time.NewTicker(30 * time.Second)
	defer tick.Stop()
	for {
		a.probeLinks(ctx)
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

func (a *App) probeLinks(ctx context.Context) {
	linkState := a.linkState()
	seen := map[string]bool{}
	for _, l := range a.links(`WHERE status='active'`) {
		seen[liveKey(l)] = true
		live := a.probeLink(ctx, l)
		if live.Online && l.Role == linkRoleMain {
			var hv struct {
				Items []hostView `json:"items"`
			}
			c, cancel := context.WithTimeout(ctx, 15*time.Second)
			if err := a.linkCall(c, l, "GET", "/link-api/fwd/hosts", nil, &hv); err == nil {
				for _, h := range hv.Items {
					if contains(l.Hosts, h.Name) {
						live.Hosts = append(live.Hosts, peerHost{Name: h.Name, Online: h.Online, Version: h.Version})
					}
				}
				sort.Slice(live.Hosts, func(i, j int) bool { return live.Hosts[i].Name < live.Hosts[j].Name })
			}
			cancel()
		}
		linkState.Lock()
		prev := linkState.m[liveKey(l)]
		if prev != nil && live.Online {
			live.samples = append(prev.samples, live.RTT)
			if len(live.samples) > 20 {
				live.samples = live.samples[len(live.samples)-20:]
			}
			live.Jitter = jitter(live.samples)
		} else if live.Online {
			live.samples = []float64{live.RTT}
		}
		linkState.m[liveKey(l)] = &live
		linkState.Unlock()
	}
	linkState.Lock()
	for id := range linkState.m {
		if !seen[id] {
			delete(linkState.m, id)
		}
	}
	linkState.Unlock()
}

// jitter: mean absolute difference between consecutive samples
func jitter(s []float64) float64 {
	if len(s) < 2 {
		return 0
	}
	var sum float64
	for i := 1; i < len(s); i++ {
		d := s[i] - s[i-1]
		if d < 0 {
			d = -d
		}
		sum += d
	}
	return float64(int(sum/float64(len(s)-1)*10)) / 10
}

// ---- the same view of the Wharf link ----

var wharfLiveCache = struct {
	sync.Mutex
	at   time.Time
	live linkLive
}{}

// wharfLive: LAN or internet, direct or through a proxy, latency, address and country of the
// paired Wharf, measured with plain requests to its address (any answer counts).
func (a *App) wharfLive(ctx context.Context, base, fp string) linkLive {
	wharfLiveCache.Lock()
	if time.Since(wharfLiveCache.at) < 30*time.Second {
		l := wharfLiveCache.live
		wharfLiveCache.Unlock()
		return l
	}
	wharfLiveCache.Unlock()
	out := linkLive{Checked: time.Now().Unix()}
	var ip net.IP
	var state *tls.ConnectionState
	var hdr http.Header
	trace := &httptrace.ClientTrace{GotConn: func(i httptrace.GotConnInfo) {
		if ta, ok := i.Conn.RemoteAddr().(*net.TCPAddr); ok {
			ip = ta.IP
		}
	}}
	ping := func() (time.Duration, error) {
		c, cancel := context.WithTimeout(httptrace.WithClientTrace(ctx, trace), 6*time.Second)
		defer cancel()
		req, _ := http.NewRequestWithContext(c, "HEAD", strings.TrimRight(base, "/")+"/", nil)
		t0 := time.Now()
		resp, err := linkClient(fp).Do(req)
		if err != nil {
			return 0, err
		}
		resp.Body.Close()
		state, hdr = resp.TLS, resp.Header
		return time.Since(t0), nil
	}
	if _, err := ping(); err != nil {
		out.Error = err.Error()
	} else {
		var best time.Duration
		for i := 0; i < 3; i++ {
			if d, err := ping(); err == nil && (best == 0 || d < best) {
				best = d
			}
		}
		out.Online, out.Addr, out.RTT = true, base, float64(best.Microseconds())/1000
		if ip != nil {
			out.IP, out.Route = ip.String(), routeOf(ip)
		}
		out.Via = "direct"
		if state != nil && !pinMatches(state, fp) {
			out.Via = "proxy"
			if len(state.PeerCertificates) > 0 {
				out.Issuer = state.PeerCertificates[0].Issuer.CommonName
				if o := state.PeerCertificates[0].Issuer.Organization; len(o) > 0 {
					out.Issuer = o[0]
				}
			}
			if hdr.Get("Cf-Ray") != "" || strings.EqualFold(hdr.Get("Server"), "cloudflare") {
				out.Via, out.CDN = "cdn", "Cloudflare"
			}
		}
		if out.Route == "public" {
			out.Country = a.countryOf(ctx, out.IP)
		}
	}
	wharfLiveCache.Lock()
	wharfLiveCache.at, wharfLiveCache.live = time.Now(), out
	wharfLiveCache.Unlock()
	return out
}
