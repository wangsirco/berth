package panel

// LAN discovery, like Bluetooth: the panel to be bound opens "discoverable" for ten minutes;
// the other scans its LAN, finds it (and the router's Wharf, known by its certificate) and
// sends a request; the first one shows it and its admin allows. The request carries a normal
// link code, so the handshake, check code and signing are those of a pasted code.

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/wangsirco/berth/internal/change"
	"github.com/wangsirco/berth/internal/errs"
	"github.com/wangsirco/berth/internal/version"
)

const discoverWindow = 10 * time.Minute

type linkRequest struct {
	ID   string   `json:"id"`
	From string   `json:"from"`
	IP   string   `json:"ip"`
	At   int64    `json:"at"`
	Code string   `json:"code"`
	code linkCode // decoded
}

// discovery: per panel (tests run two in one process)
type discovery struct {
	sync.Mutex
	until    time.Time
	requests map[string]*linkRequest
}

func (a *App) disc() *discovery {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.discovery_ == nil {
		a.discovery_ = &discovery{requests: map[string]*linkRequest{}}
	}
	return a.discovery_
}

func (d *discovery) open() bool { return time.Now().Before(d.until) }

func (d *discovery) list() []*linkRequest {
	out := []*linkRequest{}
	for id, rq := range d.requests {
		if time.Since(time.Unix(rq.At, 0)) > discoverWindow {
			delete(d.requests, id)
			continue
		}
		out = append(out, rq)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].At > out[j].At })
	return out
}

func init() {
	routeHooks = append(routeHooks, func(a *App, mux *http.ServeMux) {
		super := func(h http.HandlerFunc) http.HandlerFunc {
			return a.authed("settings.manage", func(w http.ResponseWriter, r *http.Request) {
				if currentUser(r).Role != RoleSuper {
					a.fail(w, r, errs.New(403, "auth.super_only"))
					return
				}
				h(w, r)
			})
		}
		mux.HandleFunc("POST /api/v1/links/discoverable", super(a.handleDiscoverable))
		mux.HandleFunc("POST /api/v1/links/scan", super(a.handleLinkScan))
		mux.HandleFunc("POST /api/v1/links/request", super(a.handleLinkRequestOut))
		mux.HandleFunc("DELETE /api/v1/links/requests/{id}", super(func(w http.ResponseWriter, r *http.Request) {
			d := a.disc()
			d.Lock()
			delete(d.requests, r.PathValue("id"))
			d.Unlock()
			writeJSON(w, 200, map[string]bool{"ok": true})
		}))
		mux.HandleFunc("GET /link-api/discover", a.handleDiscover)
		mux.HandleFunc("POST /link-api/request", a.handleLinkRequestIn)
	})
}

// fromLAN: the request comes from a private address (never answered to the internet)
func (a *App) fromLAN(r *http.Request) bool {
	ip := net.ParseIP(a.clientIP(r))
	return ip != nil && (privateIP(ip) || ip.IsLoopback())
}

// ---- the panel to be found ----

func (a *App) handleDiscoverable(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Open bool `json:"open"`
	}
	readJSON(r, &in)
	d := a.disc()
	d.Lock()
	if in.Open {
		d.until = time.Now().Add(discoverWindow)
	} else {
		d.until = time.Time{}
		d.requests = map[string]*linkRequest{}
	}
	until := d.until
	d.Unlock()
	_, _ = a.pipe.Record(currentActor(r), "local", "link.discoverable", map[string]any{"open": in.Open},
		change.Plan{Summary: map[bool]string{true: "plan.link.discoverable_on", false: "plan.link.discoverable_off"}[in.Open], Risk: change.RiskLow})
	writeJSON(w, 200, map[string]int64{"until": until.Unix()})
}

type discoverInfo struct {
	Product string `json:"product"`
	Name    string `json:"name"`
	Version string `json:"version"`
	FP      string `json:"fp"`
	Managed string `json:"managed,omitempty"` // the main panel it is already handed to
}

func (a *App) handleDiscover(w http.ResponseWriter, r *http.Request) {
	if !a.disc().open() || !a.fromLAN(r) {
		blank404(w)
		return
	}
	info := discoverInfo{Product: "berth", Name: a.panelDisplayName(), Version: version.Version, FP: a.caFP()}
	if l := a.links(`WHERE role=? AND status IN ('wait','active')`, linkRoleManaged); len(l) > 0 {
		info.Managed = l[0].Name
	}
	writeJSON(w, 200, info)
}

// a request to be managed: a link code, shown to this panel's admin to allow or refuse
func (a *App) handleLinkRequestIn(w http.ResponseWriter, r *http.Request) {
	if !a.disc().open() || !a.fromLAN(r) || a.limiter.blocked("linkreq:"+a.clientIP(r)) {
		blank404(w)
		return
	}
	var in struct{ Code, From string }
	readJSON(r, &in)
	c, err := decodeLinkCode(in.Code)
	if err != nil || c.FP == a.caFP() {
		a.limiter.record("linkreq:" + a.clientIP(r))
		a.fail(w, r, errs.New(400, "link.bad_code"))
		return
	}
	id := secretHash(c.S)[:16]
	rq := &linkRequest{ID: id, From: truncate(firstNonEmpty(strings.TrimSpace(in.From), c.Name), 60), IP: a.clientIP(r), At: time.Now().Unix(), Code: in.Code, code: c}
	d := a.disc()
	d.Lock()
	if len(d.list()) >= 10 {
		d.Unlock()
		a.fail(w, r, errs.New(429, "auth.rate_limited"))
		return
	}
	d.requests[id] = rq
	d.Unlock()
	a.limiter.record("linkreq:" + a.clientIP(r)) // a few per window, not a flood
	a.hub.publish(event{Type: "link.request", Data: map[string]any{"id": id, "from": rq.From}})
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// ---- the panel that looks ----

type found struct {
	Kind    string   `json:"kind"` // berth | wharf
	Addr    string   `json:"addr"` // https://ip:port
	IP      string   `json:"ip"`
	Name    string   `json:"name,omitempty"`
	Version string   `json:"version,omitempty"`
	FP      string   `json:"fp,omitempty"`
	Managed string   `json:"managed,omitempty"`
	Gateway bool     `json:"gateway,omitempty"`
	Chain   []string `json:"chain,omitempty"` // Wharf: certificate fingerprints, its CA last
}

// innerIface: networks this machine carries for containers and VMs, VPNs: not the LAN
var innerIface = regexp.MustCompile(`^(docker\d*|br-[0-9a-f]+|veth.*|incusbr\d*|lxcbr\d*|lxdbr\d*|virbr\d*|cni.*|podman\d*|flannel.*|cali.*|tailscale.*|wg.*|tun.*|tap.*|zt.*)$`)

// lanNets: the /24 (at most) around each private IPv4 address of this machine's own cards
func lanNets() ([]*net.IPNet, map[string]bool) {
	self := map[string]bool{}
	var out []*net.IPNet
	ifs, _ := net.Interfaces()
	for _, ifc := range ifs {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 || innerIface.MatchString(ifc.Name) {
			continue
		}
		addrs, _ := ifc.Addrs()
		for _, ad := range addrs {
			ipn, ok := ad.(*net.IPNet)
			if !ok || ipn.IP.To4() == nil || !privateIP(ipn.IP) {
				continue
			}
			self[ipn.IP.String()] = true
			ones, _ := ipn.Mask.Size()
			if ones < 24 {
				ones = 24
			}
			m := net.CIDRMask(ones, 32)
			n := &net.IPNet{IP: ipn.IP.Mask(m), Mask: m}
			dup := false
			for _, x := range out {
				dup = dup || x.String() == n.String()
			}
			if !dup {
				out = append(out, n)
			}
		}
	}
	return out, self
}

func defaultGateways() map[string]bool {
	gw := map[string]bool{}
	b, err := os.ReadFile("/proc/net/route")
	if err != nil {
		return gw
	}
	for _, line := range strings.Split(string(b), "\n")[1:] {
		f := strings.Fields(line)
		if len(f) > 2 && f[1] == "00000000" {
			if v, err := hex.DecodeString(f[2]); err == nil && len(v) == 4 {
				gw[net.IPv4(v[3], v[2], v[1], v[0]).String()] = true
			}
		}
	}
	return gw
}

func (a *App) handleLinkScan(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	nets, self := lanNets()
	gws := defaultGateways()
	type target struct {
		ip   string
		port string
	}
	var targets []target
	seen := map[string]bool{}
	add := func(ip, port string) {
		if !self[ip] && !seen[ip+":"+port] {
			seen[ip+":"+port] = true
			targets = append(targets, target{ip, port})
		}
	}
	for gw := range gws {
		add(gw, "7080")
	}
	for _, n := range nets {
		base := binary.BigEndian.Uint32(n.IP.To4())
		ones, _ := n.Mask.Size()
		size := uint32(1) << (32 - ones)
		for i := uint32(1); i+1 < size && i < 255; i++ {
			v := make(net.IP, 4)
			binary.BigEndian.PutUint32(v, base+i)
			add(v.String(), "7090")
			add(v.String(), "7080")
		}
	}
	var mu sync.Mutex
	out := []found{}
	sem := make(chan struct{}, 96)
	var wg sync.WaitGroup
	for _, t := range targets {
		wg.Add(1)
		sem <- struct{}{}
		go func(t target) {
			defer wg.Done()
			defer func() { <-sem }()
			if f, ok := probePanel(ctx, t.ip, t.port); ok {
				f.Gateway = gws[t.ip]
				mu.Lock()
				out = append(out, f)
				mu.Unlock()
			}
		}(t)
	}
	wg.Wait()
	sort.Slice(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind == "wharf"
		}
		return out[i].IP < out[j].IP
	})
	nl := []string{}
	for _, n := range nets {
		nl = append(nl, n.String())
	}
	writeJSON(w, 200, map[string]any{"items": out, "nets": nl})
}

// probePanel: a Berth panel open for discovery, or a Wharf (its certificate says so)
func probePanel(ctx context.Context, ip, port string) (found, bool) {
	addr := net.JoinHostPort(ip, port)
	d := net.Dialer{Timeout: 500 * time.Millisecond}
	c, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return found{}, false
	}
	c.Close()
	tc, err := tls.DialWithDialer(&net.Dialer{Timeout: 2 * time.Second}, "tcp", addr, &tls.Config{InsecureSkipVerify: true}) // who it is gets checked below and at pairing
	if err != nil {
		return found{}, false
	}
	certs := tc.ConnectionState().PeerCertificates
	tc.Close()
	base := "https://" + addr
	if port == "7080" {
		for _, ct := range certs {
			if contains(ct.Subject.Organization, "Wharf") || contains(ct.Issuer.Organization, "Wharf") {
				f := found{Kind: "wharf", Addr: base, IP: ip, Name: "Wharf"}
				for _, x := range certs {
					h := sha256.Sum256(x.Raw)
					f.Chain = append(f.Chain, hex.EncodeToString(h[:]))
				}
				return f, true
			}
		}
		return found{}, false
	}
	hc := &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}
	req, _ := http.NewRequestWithContext(ctx, "GET", base+"/link-api/discover", nil)
	resp, err := hc.Do(req)
	if err != nil {
		return found{}, false
	}
	defer resp.Body.Close()
	var info discoverInfo
	if resp.StatusCode != 200 || json.NewDecoder(resp.Body).Decode(&info) != nil || info.Product != "berth" {
		return found{}, false
	}
	// the fingerprint it states must be in the chain it presented
	ok := false
	for _, x := range certs {
		h := sha256.Sum256(x.Raw)
		ok = ok || hex.EncodeToString(h[:]) == strings.ToLower(info.FP)
	}
	if !ok {
		return found{}, false
	}
	return found{Kind: "berth", Addr: base, IP: ip, Name: truncate(info.Name, 60), Version: truncate(info.Version, 30), FP: info.FP, Managed: truncate(info.Managed, 60)}, true
}

// ask a discovered panel to be managed by this one: a link code it shows its admin
func (a *App) handleLinkRequestOut(w http.ResponseWriter, r *http.Request) {
	if err := a.requireReauth(r); err != nil {
		a.fail(w, r, err)
		return
	}
	var in struct{ Addr, FP string }
	readJSON(r, &in)
	host := ""
	if u, err := url.Parse(in.Addr); err == nil && u.Scheme == "https" {
		host = u.Hostname()
	}
	if ip := net.ParseIP(host); ip == nil || !(privateIP(ip) || ip.IsLoopback()) || len(in.FP) != 64 {
		a.fail(w, r, errs.New(400, "link.bad_code"))
		return
	}
	priv, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	secret := randUID() + randUID()
	privEnc, _ := a.seal(priv.Bytes())
	u := currentUser(r)
	exp := time.Now().Add(linkCodeTTL).Unix()
	res, err := a.db.Exec(`INSERT INTO panel_links(role, status, secret_hash, priv_enc, by_user, by_id, created_at, expires_at, auto_confirm) VALUES(?,?,?,?,?,?,?,?,1)`,
		linkRoleMain, "code", secretHash(secret), privEnc, u.Username, u.ID, time.Now().Unix(), exp)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	id, _ := res.LastInsertId()
	// the address the other panel reaches this one at: the LAN way towards it first
	addrs := a.berthAddrsFor("lan", host, panelBase(r)) // found on the LAN: a LAN link
	a.db.Exec(`UPDATE panel_links SET mode='lan' WHERE id=?`, id)
	code := encodeLinkCode(linkCode{V: 1, Addrs: addrs, FP: a.caFP(), S: secret, Name: a.panelDisplayName(), Pub: base64.StdEncoding.EncodeToString(priv.PublicKey().Bytes()), Mode: "lan"})
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	if err := linkPlain(ctx, strings.TrimRight(in.Addr, "/"), in.FP, "/link-api/request", map[string]string{"code": code, "from": a.panelDisplayName()}, nil); err != nil {
		a.db.Exec(`DELETE FROM panel_links WHERE id=?`, id)
		a.fail(w, r, errs.New(502, "link.unreachable", "detail", errText(err)))
		return
	}
	_, _ = a.pipe.Record(currentActor(r), "local", "link.request", map[string]any{"id": id, "to": in.Addr},
		change.Plan{Summary: "plan.link.request", SummaryP: map[string]any{"to": in.Addr}, Risk: change.RiskMedium})
	writeJSON(w, 200, map[string]any{"id": id, "expires_at": exp})
}
