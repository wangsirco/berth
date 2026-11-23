// Package wharftest is a fake Wharf for tests: the Berth extension API with token,
// signature, time window and nonce checks, and a way to call Berth back like Wharf would.
package wharftest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"time"

	"github.com/wangsirco/berth/internal/panel/wharfapi"
	"github.com/wangsirco/berth/internal/sign"
)

type Fake struct {
	*httptest.Server
	FP string // fingerprint of the server certificate

	mu         sync.Mutex
	Code       string // the one-time code the pairing page hands out
	Token      string // wharf_bth_ issued at exchange
	BerthToken string // berth_wrf_ received at exchange
	BerthURL   string
	BerthFP    string
	Paired     bool
	ProxyFront bool // 443 taken by proxy-front (SNI registration possible)
	Forwards   map[string]wharfapi.Forward
	SNI        wharfapi.SNI
	DDNSList   []wharfapi.DDNS
	Bans       map[string]wharfapi.Ban
	Alerts     []wharfapi.Alert
	Actors     []string
	Unpaired   string   // keep value of the last unpair
	BanRefs    []string // the id Berth sent with each ban
	// protocol v2
	V2      bool              // answer ping with protocol 2 and take X-Berth-Key
	V3      bool              // protocol 3: names, statistics (scope stats)
	KeyAuth bool              // Berth signed with a key id: call it back with X-Wharf-Key
	Bearers int               // requests that still carried the token
	Open    bool              // LAN requests accepted
	Asks    map[string]string // code → waiting | allowed | refused
	AskURLs []string
	DNS     map[string]string // name → ip
	nonces  map[string]bool
	next    int
}

func New() *Fake {
	f := newFake()
	f.Server = httptest.NewTLSServer(http.HandlerFunc(f.serve))
	f.FP = wharfapi.Fingerprint(f.Server.Certificate())
	return f
}

// NewOn listens on addr (for running the fake outside Go tests, e.g. next to an e2e panel).
func NewOn(addr string) (*Fake, error) {
	f := newFake()
	f.Server = httptest.NewUnstartedServer(http.HandlerFunc(f.serve))
	l, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	f.Server.Listener.Close()
	f.Server.Listener = l
	f.Server.StartTLS()
	f.FP = wharfapi.Fingerprint(f.Server.Certificate())
	return f, nil
}

// State is a snapshot for tests outside the process.
func (f *Fake) State() map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return map[string]any{"paired": f.Paired, "forwards": f.Forwards, "sni": f.SNI, "bans": f.Bans, "alerts": f.Alerts, "actors": f.Actors}
}

func newFake() *Fake {
	f := &Fake{Code: "pair-code-1", Forwards: map[string]wharfapi.Forward{}, Bans: map[string]wharfapi.Ban{}, Asks: map[string]string{}, DNS: map[string]string{}, nonces: map[string]bool{},
		DDNSList: []wharfapi.DDNS{{ID: "d1", Domain: "home.example.net", Owner: "wharf", IP: "198.51.100.20", Status: "ok"}}}
	return f
}

func reply(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, code int, msg string) {
	reply(w, code, map[string]string{"error": msg})
}

func (f *Fake) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	path := strings.TrimPrefix(r.URL.Path, wharfapi.Prefix)
	f.mu.Lock()
	defer f.mu.Unlock()
	if path == "/pair/exchange" && r.Method == "POST" {
		var in struct { // what the real Wharf reads (its docs/BERTH.md §3)
			Code, Token, Name string
		}
		json.Unmarshal(body, &in)
		if in.Code == "" || (in.Code != f.Code && f.Code != "*") || f.Paired {
			fail(w, 403, "配对码无效或已用过")
			return
		}
		f.Code, f.Paired = "", true
		if !strings.HasPrefix(in.Token, "berth_wrf_") || len(in.Token) < 30 {
			fail(w, 400, "Berth 需要同时交给 Wharf 一个 berth_wrf_ 开头的令牌")
			return
		}
		var extra struct {
			BerthURL string `json:"berth_url"`
			BerthFP  string `json:"berth_fp"`
		}
		json.Unmarshal(body, &extra)
		f.Token, f.BerthToken, f.BerthURL, f.BerthFP = "wharf_bth_"+sign.RandomHex(16), in.Token, extra.BerthURL, extra.BerthFP
		reply(w, 200, map[string]any{"token": f.Token, "scopes": []string{"forwards", "sni", "ddns", "devices.read", "traffic.read", "bans", "alerts", "sso", "dns", "stats"},
			"wharf_name": "Wharf · 家里", "targets": []map[string]string{{"ip": "192.168.1.20", "mac": "aa:bb:cc:00:11:22", "name": "mini"}}, "expires_at": 0})
		return
	}
	if path == "/pair/request" && r.Method == "POST" {
		var in wharfapi.PairRequest
		json.Unmarshal(body, &in)
		if !f.V2 || !f.Open {
			fail(w, 404, "404 page not found")
			return
		}
		if in.WharfFP != f.FP {
			fail(w, 400, "Berth 看到的 Wharf 证书指纹不对：中间可能有人换了证书")
			return
		}
		f.Asks[in.Code], f.AskURLs = "waiting", in.BerthURLs
		reply(w, 200, wharfapi.PairAsk{ID: "q1", SAS: wharfapi.SAS(in.Code, in.BerthFP, f.FP), ExpiresAt: time.Now().Add(10 * time.Minute).Unix()})
		return
	}
	if path == "/pair/request/status" && r.Method == "POST" {
		var in struct{ Code string }
		json.Unmarshal(body, &in)
		st, ok := f.Asks[in.Code]
		if !ok {
			fail(w, 404, "404 page not found")
			return
		}
		if st == "allowed" {
			f.Code = "allowed-code"
			reply(w, 200, wharfapi.PairAskState{State: st, Code: f.Code, WharfURL: f.URL})
			return
		}
		reply(w, 200, wharfapi.PairAskState{State: st})
		return
	}
	// everything else: token (or its key id), signature, nonce
	kid := r.Header.Get("X-Berth-Key")
	if !f.Paired || (kid == "" && r.Header.Get("Authorization") != "Bearer "+f.Token) || (kid != "" && (!f.V2 || kid != wharfapi.KeyID(f.Token))) {
		fail(w, 401, "对接已失效，请重新配对")
		return
	}
	if kid == "" {
		f.Bearers++
	}
	nonce, err := sign.CheckPrefixed(r, "X-Berth", sign.DeriveKey(f.Token), body)
	if err != nil {
		fail(w, 401, "签名无效："+err.Error())
		return
	}
	if f.nonces[nonce] {
		fail(w, 401, "重放的请求")
		return
	}
	f.nonces[nonce] = true
	if kid != "" {
		f.KeyAuth = true
	}
	if a := r.Header.Get("X-Berth-Actor"); a != "" {
		f.Actors = append(f.Actors, a)
	}
	f.next++
	id := fmt.Sprintf("w%d", f.next)
	switch {
	case path == "/forwards" && r.Method == "GET":
		out := []wharfapi.Forward{}
		for _, x := range f.Forwards {
			out = append(out, x)
		}
		reply(w, 200, map[string]any{"items": out})
	case path == "/forwards" && r.Method == "POST":
		var in wharfapi.Forward
		json.Unmarshal(body, &in)
		if in.TargetIP != "192.168.1.20" {
			fail(w, 400, "只能映射到配对时选定的 Berth 主机")
			return
		}
		for _, x := range f.Forwards {
			if x.WANPort == in.WANPort && x.Proto == in.Proto {
				fail(w, 409, "外网端口 "+in.WANPort+" 已被映射「"+x.Name+"」占用")
				return
			}
		}
		if r.URL.Query().Get("plan") == "1" {
			reply(w, 200, map[string]any{"adds": []string{"firewall.owpr_" + id}, "conflicts": []string{}})
			return
		}
		in.ID, in.Kind = id, "berth"
		f.Forwards[id] = in
		reply(w, 200, in)
	case strings.HasPrefix(path, "/forwards/") && r.Method == "DELETE":
		fid := strings.TrimPrefix(path, "/forwards/")
		x, ok := f.Forwards[fid]
		if !ok {
			fail(w, 404, "映射不存在")
			return
		}
		if x.Kind != "berth" {
			fail(w, 403, "只能删除由 Berth 创建的映射")
			return
		}
		delete(f.Forwards, fid)
		reply(w, 200, map[string]bool{"ok": true})
	case path == "/sni" && r.Method == "GET":
		reply(w, 200, f.SNI)
	case path == "/sni" && r.Method == "PUT":
		if !f.ProxyFront {
			fail(w, 409, "443 未被占用，请改用端口映射")
			return
		}
		json.Unmarshal(body, &f.SNI)
		reply(w, 200, f.SNI)
	case path == "/ddns" && r.Method == "GET":
		out := []map[string]any{} // Wharf's own names
		for i, d := range f.DDNSList {
			out = append(out, map[string]any{"id": i + 1, "domain": d.Domain, "owner": d.Owner, "last_ip": d.IP, "last_ok": 1700000000, "enabled": true})
		}
		reply(w, 200, map[string]any{"items": out, "owners": map[string]string{}})
	case path == "/ddns/claim" && r.Method == "POST":
		var in struct{ Domain, Owner string }
		json.Unmarshal(body, &in)
		for i, d := range f.DDNSList {
			if d.Domain == in.Domain {
				f.DDNSList[i].Owner = in.Owner
				reply(w, 200, f.DDNSList[i])
				return
			}
		}
		d := wharfapi.DDNS{ID: wharfapi.ID(id), Domain: in.Domain, Owner: in.Owner, Status: "pending"}
		f.DDNSList = append(f.DDNSList, d)
		reply(w, 200, d)
	case path == "/devices":
		reply(w, 200, map[string]any{"items": []wharfapi.Device{{Name: "mini", IP: "192.168.1.20", MAC: "aa:bb:cc:00:11:22", Type: "server", Online: true}, {Name: "phone", IP: "192.168.1.50", Online: true}}})
	case strings.HasPrefix(path, "/hosts/"):
		reply(w, 200, map[string]any{"range": r.URL.Query().Get("range"), "wan_in_bytes": 123456789, "wan_out_bytes": 987654321,
			"countries": []map[string]any{{"cc": "US", "conns": 40}, {"cc": "CN", "conns": 12}}, "scans": 3})
	case path == "/bans" && r.Method == "POST":
		var in wharfapi.Ban
		json.Unmarshal(body, &in)
		if strings.Contains(in.CIDR, ":") {
			fail(w, 400, "暂只支持封禁 IPv4 地址")
			return
		}
		f.BanRefs = append(f.BanRefs, string(in.ID)) // Berth's own number (Wharf keeps it as remote_id)
		n := f.next
		in.ID = wharfapi.ID(fmt.Sprint(n))
		f.Bans[fmt.Sprint(n)] = in
		reply(w, 200, map[string]any{"id": n}) // Wharf numbers bans
	case strings.HasPrefix(path, "/bans/") && r.Method == "DELETE":
		bid := strings.TrimPrefix(path, "/bans/")
		if _, ok := f.Bans[bid]; !ok {
			fail(w, 404, "封禁不存在")
			return
		}
		delete(f.Bans, bid)
		reply(w, 200, map[string]bool{"ok": true})
	case path == "/alerts" && r.Method == "POST":
		var a wharfapi.Alert
		json.Unmarshal(body, &a)
		f.Alerts = append(f.Alerts, a)
		reply(w, 200, map[string]bool{"ok": true})
	case path == "/sso/ticket" && r.Method == "POST":
		var in struct {
			BerthUser string `json:"berth_user"`
			ReturnTo  string `json:"return_to"`
		}
		json.Unmarshal(body, &in)
		if in.BerthUser != "root" {
			fail(w, 403, "这个 Berth 用户没有对应的 Wharf 用户")
			return
		}
		reply(w, 200, map[string]string{"url": f.URL + "/sso/berth?t=ticket-" + id})
	case path == "/unpair" && r.Method == "POST":
		var in struct{ Keep bool }
		json.Unmarshal(body, &in)
		f.Paired, f.Unpaired, f.DNS = false, fmt.Sprint(in.Keep), map[string]string{}
		reply(w, 200, map[string]bool{"ok": true})
	case path == "/ping":
		p := map[string]any{"ok": true, "name": "Wharf · 家里"}
		if f.V2 {
			p["protocol"], p["key_auth"] = 2, f.KeyAuth
		}
		if f.V3 {
			p["protocol"] = 3
		}
		reply(w, 200, p)
	case path == "/names" && f.V3:
		reply(w, 200, map[string]string{"lan": f.URL, "public": "https://wharf.example.net", "lan_fp": f.FP})
	case strings.HasPrefix(path, "/stats/") && f.V3:
		f.stats(w, strings.TrimPrefix(path, "/stats/"))
	case path == "/dns/names" && r.Method == "GET":
		items := []map[string]string{}
		for n, ip := range f.DNS {
			items = append(items, map[string]string{"name": n, "ip": ip})
		}
		reply(w, 200, map[string]any{"items": items, "suffixes": []string{"lan"}, "max": 5})
	case strings.HasPrefix(path, "/dns/names/") && r.Method == "PUT":
		var in struct{ IP string }
		json.Unmarshal(body, &in)
		f.DNS[strings.TrimPrefix(path, "/dns/names/")] = in.IP
		reply(w, 200, map[string]any{"ok": true})
	case strings.HasPrefix(path, "/dns/names/") && r.Method == "DELETE":
		n := strings.TrimPrefix(path, "/dns/names/")
		if _, ok := f.DNS[n]; !ok {
			fail(w, 404, "不存在")
			return
		}
		delete(f.DNS, n)
		reply(w, 200, map[string]any{"ok": true})
	default:
		fail(w, 404, "没有这个接口")
	}
}

// Allow: the router's admin allows (or refuses) every waiting LAN request.
func (f *Fake) Allow(ok bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for c, st := range f.Asks {
		if st == "waiting" {
			f.Asks[c] = map[bool]string{true: "allowed", false: "refused"}[ok]
		}
	}
}

// Lock / Unlock: tests read the fields under the fake's lock.
func (f *Fake) Lock()   { f.mu.Lock() }
func (f *Fake) Unlock() { f.mu.Unlock() }

// CallBerth calls Berth's side of the link the way Wharf does (token + X-Wharf signature).
func (f *Fake) CallBerth(hc *http.Client, base, method, path string, in any) (int, map[string]any) {
	var body []byte
	if in != nil {
		body, _ = json.Marshal(in)
	}
	req, _ := http.NewRequest(method, strings.TrimRight(base, "/")+"/api/ext/wharf/v1"+path, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	f.mu.Lock()
	tok := f.BerthToken
	f.mu.Unlock()
	if f.KeyAuth {
		req.Header.Set("X-Wharf-Key", wharfapi.KeyID(tok))
	} else {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	sign.SignPrefixed(req, "X-Wharf", sign.DeriveKey(tok), body)
	resp, err := hc.Do(req)
	if err != nil {
		return 0, map[string]any{"error": err.Error()}
	}
	defer resp.Body.Close()
	out := map[string]any{}
	json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

// stats answers the read-only statistics (shapes as Wharf's own pages use them)
func (f *Fake) stats(w http.ResponseWriter, what string) {
	now := time.Now().Unix() / 3600 * 3600
	series := []map[string]any{}
	for i := 23; i >= 0; i-- {
		series = append(series, map[string]any{"t": now - int64(i)*3600, "down": 1_000_000_000 + i*50_000_000, "up": 90_000_000 + i*3_000_000})
	}
	devs := []map[string]any{{"mac": "aa:bb:cc:00:11:22", "name": "mini", "ip": "192.168.1.20", "type": "server", "down": 14_000_000_000, "up": 900_000_000, "last_seen": now},
		{"mac": "aa:bb:cc:00:11:33", "name": "iPhone", "ip": "192.168.1.50", "type": "phone", "down": 8_200_000_000, "up": 300_000_000, "last_seen": now}}
	switch {
	case what == "summary":
		reply(w, 200, map[string]any{"devices": map[string]int{"online": 23, "total": 41}, "today": map[string]int64{"down": 37_580_000_000, "up": 3_300_000_000},
			"visitors_24h": map[string]int{"count": 1284, "countries": 37}, "blocked_24h": map[string]int{"attacks": 41, "dns": 271},
			"series_24h": series, "top_devices": devs, "top_countries": []map[string]any{{"cc": "US", "count": 412}, {"cc": "NZ", "count": 260}},
			"pages": map[string]string{"traffic": "/traffic", "visitors": "/visitors", "world": "/world", "devices": "/devices", "device": "/devices/{mac}", "dns": "/dns"}})
	case what == "traffic":
		reply(w, 200, map[string]any{"up": 3_300_000_000, "down": 37_580_000_000, "step": 3600, "series": series, "devices": devs,
			"services": []map[string]any{{"name": "YouTube", "category": "video", "bytes": 9_000_000_000, "up": 1, "down": 9_000_000_000, "requests": 10, "conns": 4}}, "categories": nil})
	case what == "geo":
		reply(w, 200, map[string]any{"origin": map[string]any{"name": "Auckland"}, "countries": []map[string]any{{"code": "US", "name": "美国", "lat": 38, "lon": -97, "bytes": 5_000_000_000, "conns": 120}, {"code": "JP", "name": "日本", "lat": 36, "lon": 138, "bytes": 1_000_000_000, "conns": 40}}, "cities": nil})
	case what == "visitors":
		reply(w, 200, map[string]any{"items": []map[string]any{{"id": 1, "ts": now, "ip": "203.0.113.9", "country": "US", "city": "Dallas", "username": "", "path": "/login", "blocked": true, "reason": "国家不在允许列表", "tz_match": "unknown"}}, "geo_ready": true})
	case what == "devices":
		reply(w, 200, []map[string]any{{"mac": "aa:bb:cc:00:11:22", "ip": "192.168.1.20", "name": "mini", "type": "server", "online": true, "presence": "home"}, {"mac": "aa:bb:cc:00:11:33", "ip": "192.168.1.50", "name": "iPhone", "type": "phone", "online": false, "presence": "away"}})
	case strings.HasSuffix(what, "/traffic"):
		reply(w, 200, map[string]any{"from": now - 86400, "to": now, "step": 3600, "series": series, "up": 1, "down": 2, "heatmap": nil, "by_hour": nil, "peak_hour": 20, "services": nil, "categories": nil})
	case strings.HasSuffix(what, "/presence"):
		reply(w, 200, map[string]any{"state": "home", "online": true, "events": []any{}, "away_minutes": 0})
	case what == "dns":
		fail(w, 404, "没有配置 NextDNS")
	default:
		fail(w, 404, "没有这个接口")
	}
}
