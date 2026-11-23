package panel

// Wharf protocol v2 (Wharf 0.24.0, its docs/BERTH.md §5 and §8): the rest of the API Wharf
// calls on Berth (ping, events, unpair, rotate, ddns, hosts), signing without the token once
// both sides speak v2, and pairing from LAN discovery: Berth asks, the router's admin allows,
// both show the same six digits.

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/wangsirco/berth/internal/change"
	"github.com/wangsirco/berth/internal/errs"
	"github.com/wangsirco/berth/internal/panel/wharfapi"
	"github.com/wangsirco/berth/internal/sign"
	"github.com/wangsirco/berth/internal/version"
)

const wharfProtocol = 3 // 3: names, LAN / internet links, stats

var wharfPinged = struct {
	sync.Mutex
	at time.Time
}{}

// wharfRefresh asks Wharf for its protocol (at most every 30 seconds unless forced): from v2
// on, Berth names its token by key id and Wharf does the same in return.
func (a *App) wharfRefresh(ctx context.Context, force bool) {
	wharfPinged.Lock()
	if !force && time.Since(wharfPinged.at) < 30*time.Second {
		wharfPinged.Unlock()
		return
	}
	wharfPinged.at = time.Now()
	wharfPinged.Unlock()
	l, err := a.wharf()
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	p, err := l.api().Ping(ctx)
	if err != nil {
		var we *wharfapi.Error
		// only when Wharf no longer knows key ids ("缺少对接令牌"): an expired link, an old
		// signature or a replay are 401 too, and must never send the token itself
		if l.KeyAuth && errorsAs(err, &we) && we.Status == 401 && strings.Contains(we.Message, "缺少对接令牌") {
			// a Wharf that took key ids yesterday and not today (downgraded): back to v1
			a.db.Exec(`UPDATE wharf_link SET protocol=0 WHERE id=1`)
		}
		return
	}
	if p.Protocol != l.Protocol {
		a.db.Exec(`UPDATE wharf_link SET protocol=? WHERE id=1`, p.Protocol)
		l.Protocol = p.Protocol
	}
	a.wharfNames(ctx, l) // its LAN and public names (protocol 3)
	if p.Protocol >= 2 && !l.KeyAuth && l.inbound != "" {
		// the first signed call with a key id switches Wharf over: do it now, not at the next ban
		if l, err := a.wharf(); err == nil {
			l.api().Ping(ctx)
		}
	}
}

func errorsAs(err error, we **wharfapi.Error) bool {
	e, ok := err.(*wharfapi.Error)
	if ok {
		*we = e
	}
	return ok
}

func init() {
	routeHooks = append(routeHooks, func(a *App, mux *http.ServeMux) {
		// ---- Wharf calling Berth (signed) ----
		mux.HandleFunc("GET /api/ext/wharf/v1/ping", func(w http.ResponseWriter, r *http.Request) {
			if _, ok := a.wharfAuth(w, r); !ok {
				return
			}
			writeJSON(w, 200, map[string]any{"ok": true, "name": a.panelDisplayName(), "version": version.Version, "time": time.Now().Unix(),
				"protocol": wharfProtocol, "auth": []string{"key", "bearer"}})
		})
		mux.HandleFunc("POST /api/ext/wharf/v1/events", a.wharfInEvent)
		mux.HandleFunc("POST /api/ext/wharf/v1/unpair", func(w http.ResponseWriter, r *http.Request) {
			body, ok := a.wharfAuth(w, r)
			if !ok {
				return
			}
			var in struct {
				Keep bool `json:"keep"`
			}
			json.Unmarshal(body, &in)
			a.dropWharfLink()
			a.pipe.Record(wharfActor, "local", "wharf.unpair", map[string]any{"by": "wharf", "keep": in.Keep}, change.Plan{Summary: "plan.wharf.unpaired_by_wharf", Risk: change.RiskMedium})
			a.hub.publish(event{Type: "wharf", Data: map[string]any{"paired": false}})
			writeJSON(w, 200, map[string]bool{"ok": true})
		})
		mux.HandleFunc("POST /api/ext/wharf/v1/rotate", a.wharfInRotate)
		mux.HandleFunc("GET /api/ext/wharf/v1/ddns", func(w http.ResponseWriter, r *http.Request) {
			if _, ok := a.wharfAuth(w, r); !ok {
				return
			}
			items := []map[string]string{}
			rows, err := a.db.Query(`SELECT DISTINCT name FROM ddns WHERE enabled=1 ORDER BY name`)
			if err == nil {
				for rows.Next() {
					var d string
					rows.Scan(&d)
					items = append(items, map[string]string{"domain": d})
				}
				rows.Close()
			}
			writeJSON(w, 200, map[string]any{"items": items})
		})
		mux.HandleFunc("GET /api/ext/wharf/v1/hosts/{ip}", func(w http.ResponseWriter, r *http.Request) {
			if _, ok := a.wharfAuth(w, r); !ok {
				return
			}
			host := a.hostByIP(r.PathValue("ip"))
			if host == "" {
				writeJSON(w, 404, map[string]string{"error": "Berth 没有管理这个地址的服务器"})
				return
			}
			writeJSON(w, 200, a.wharfHostView(r.Context(), host))
		})

		// ---- people: pair with a Wharf found on the LAN ----
		mux.HandleFunc("POST /api/v1/wharf/request", a.authed("settings.manage", a.handleWharfRequest))
		mux.HandleFunc("GET /api/v1/wharf/request", a.authed("settings.manage", func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, 200, a.wharfAskView(r))
		}))
		mux.HandleFunc("DELETE /api/v1/wharf/request", a.authed("settings.manage", func(w http.ResponseWriter, r *http.Request) {
			a.mu.Lock()
			if q := a.wharfAsk_; q != nil && q.cancel != nil {
				q.cancel()
			}
			a.wharfAsk_ = nil
			a.mu.Unlock()
			writeJSON(w, 200, map[string]bool{"ok": true})
		}))
	})
}

// POST /events from Wharf
func (a *App) wharfInEvent(w http.ResponseWriter, r *http.Request) {
	body, ok := a.wharfAuth(w, r)
	if !ok {
		return
	}
	var ev map[string]any
	if json.Unmarshal(body, &ev) != nil {
		writeJSON(w, 400, map[string]string{"error": "请求格式错误"})
		return
	}
	str := func(k string) string { s, _ := ev[k].(string); return s }
	typ := str("type")
	switch typ {
	case "ban.revoked": // Wharf dropped a ban Berth had synced: it is no longer on the router
		id := strings.TrimSpace(firstNonEmpty(str("id"), strconv.FormatFloat(numOf(ev["id"]), 'f', -1, 64)))
		a.db.Exec(`UPDATE bans SET wharf_ban='', layers=(SELECT json_group_array(value) FROM json_each(layers) WHERE value != 'wharf')
			WHERE wharf_ban != '' AND (CAST(id AS TEXT)=? OR wharf_ban=?)`, id, "out:"+id)
		a.hub.publish(event{Type: "ban", Data: map[string]any{"host": "local"}})
	case "names.changed":
		if l, err := a.wharf(); err == nil {
			l.Protocol = max(l.Protocol, 3)
			a.wharfNames(r.Context(), l)
		}
	case "link.updated": // Wharf narrowed the scopes or changed the hosts (or its names)
		if n, ok := ev["names"].(map[string]any); ok {
			lan, _ := n["lan"].(string)
			pub, _ := n["public"].(string)
			if l, err := a.wharf(); err == nil {
				a.applyWharfNames(r.Context(), l, strings.TrimRight(lan, "/"), strings.TrimRight(pub, "/"))
			}
		}
		if sc, ok := ev["scopes"].([]any); ok {
			b, _ := json.Marshal(sc)
			a.db.Exec(`UPDATE wharf_link SET scopes=? WHERE id=1`, string(b))
		}
		if tg, ok := ev["targets"].([]any); ok {
			ips := []string{}
			for _, t := range tg {
				switch v := t.(type) {
				case string:
					ips = append(ips, v)
				case map[string]any:
					if ip, _ := v["ip"].(string); ip != "" {
						ips = append(ips, ip)
					}
				}
			}
			b, _ := json.Marshal(ips)
			a.db.Exec(`UPDATE wharf_link SET target_ips=? WHERE id=1`, string(b))
		}
	case "link.mode": // switched on Wharf's side: use Wharf's names of that kind
		mode := linkMode(str("mode"))
		if l, err := a.wharf(); err == nil {
			target := l.PeerLAN
			if mode == "public" {
				target = l.PeerPublic
			}
			if urls, ok := ev["wharf_urls"].([]any); ok && len(urls) > 0 {
				if u, _ := urls[0].(string); strings.HasPrefix(u, "https://") {
					target = strings.TrimRight(u, "/")
				}
			}
			if target != "" {
				a.db.Exec(`UPDATE wharf_link SET mode=?, url=? WHERE id=1`, mode, target)
			} else {
				a.db.Exec(`UPDATE wharf_link SET mode=? WHERE id=1`, mode)
			}
		}
	case "forward.changed", "ddns.owner":
	default:
		writeJSON(w, 200, map[string]any{"ok": true, "ignored": true})
		return
	}
	detail := strings.TrimSpace(str("berth_ref") + " " + str("change") + " " + str("domain") + " " + str("owner"))
	a.pipe.Record(wharfActor, "local", "wharf.event", map[string]any{"type": typ, "by": str("by"), "detail": detail},
		change.Plan{Summary: "plan.wharf.event", SummaryP: map[string]any{"type": typ, "detail": detail}, Risk: change.RiskLow})
	a.hub.publish(event{Type: "wharf", Data: map[string]any{"event": typ}})
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func numOf(v any) float64 {
	f, _ := v.(float64)
	return f
}

// POST /rotate {token: wharf_bth_new}: new tokens both ways, signed with the old ones
func (a *App) wharfInRotate(w http.ResponseWriter, r *http.Request) {
	body, ok := a.wharfAuth(w, r)
	if !ok {
		return
	}
	var in struct {
		Token string `json:"token"`
	}
	json.Unmarshal(body, &in)
	if !strings.HasPrefix(in.Token, "wharf_bth_") || len(in.Token) < 30 {
		writeJSON(w, 400, map[string]string{"error": "新令牌格式不对"})
		return
	}
	mine := "berth_wrf_" + sign.RandomHex(24)
	outEnc, err1 := a.seal([]byte(in.Token))
	inEnc, err2 := a.seal([]byte(mine))
	if err1 != nil || err2 != nil {
		writeJSON(w, 500, map[string]string{"error": "保存新令牌失败"})
		return
	}
	a.db.Exec(`UPDATE wharf_link SET token_enc=?, inbound_hash=?, inbound_enc=?, state='active' WHERE id=1`, outEnc, hashToken(mine), inEnc)
	a.pipe.Record(wharfActor, "local", "wharf.rotate", map[string]any{}, change.Plan{Summary: "plan.wharf.rotated", Risk: change.RiskLow})
	writeJSON(w, 200, map[string]string{"token": mine})
}

// hostByIP: the host Berth manages at this address ("local" for this machine)
func (a *App) hostByIP(ip string) string {
	if net.ParseIP(ip) == nil {
		return ""
	}
	if ip == a.lanIP() {
		return "local"
	}
	if _, self := lanNets(); self[ip] {
		return "local"
	}
	for _, h := range a.remoteHosts() {
		hh := h.Addr
		if x, _, err := net.SplitHostPort(hh); err == nil {
			hh = x
		}
		if hh == ip {
			return h.Name
		}
	}
	return ""
}

// wharfHostView: what Wharf's device page shows for a Berth host
func (a *App) wharfHostView(ctx context.Context, host string) map[string]any {
	type ctr struct {
		Name  string `json:"name"`
		Image string `json:"image"`
		State string `json:"state"`
	}
	cs := []ctr{}
	if list, err := a.containers(ctx, host); err == nil {
		for _, c := range list {
			cs = append(cs, ctr{c.Name, c.Image, c.State})
		}
	}
	sites := []map[string]any{}
	if ss, err := a.sites(host); err == nil {
		for _, s := range ss {
			state := "disabled"
			if s.Enabled {
				state = "enabled"
			}
			for _, d := range s.Domains {
				sites = append(sites, map[string]any{"domain": d, "state": state, "ref": "site:" + d})
			}
		}
	}
	name := host
	if host == "local" {
		name = a.panelDisplayName()
	}
	return map[string]any{"name": name, "containers": cs, "sites": sites, "vms": []any{}}
}

// ---- LAN discovery: ask the router's Wharf ----

type wharfAsk struct {
	URL     string `json:"url"`
	State   string `json:"state"` // waiting | refused | paired | expired | error
	SAS     string `json:"sas"`
	Expires int64  `json:"expires_at"`
	Error   string `json:"error,omitempty"`
	Name    string `json:"name,omitempty"` // Wharf's name, once paired
	errCode string
	errP    map[string]any
	fp      string
	code    string
	base    string
	cancel  context.CancelFunc
}

func (a *App) wharfAskView(r *http.Request) map[string]any {
	a.mu.Lock()
	defer a.mu.Unlock()
	q := a.wharfAsk_
	if q == nil {
		return map[string]any{"state": ""}
	}
	if q.State == "waiting" && time.Now().Unix() > q.Expires {
		q.State = "expired"
	}
	msg := q.Error
	if q.errCode != "" {
		msg = T(a.locale(r), q.errCode, q.errP) // in the reader's language, with Wharf's own words
	}
	return map[string]any{"state": q.State, "url": q.URL, "sas": q.SAS, "expires_at": q.Expires, "error": msg, "name": q.Name}
}

// wharfCAFP: Wharf's own CA in the chain it presents (what its pairing page shows)
func wharfCAFP(chain []wharfapi.CertInfo) string {
	for _, c := range chain {
		if c.CA {
			return c.FP
		}
	}
	if len(chain) > 0 {
		return chain[len(chain)-1].FP
	}
	return ""
}

func (a *App) handleWharfRequest(w http.ResponseWriter, r *http.Request) {
	if currentUser(r).Role != RoleSuper {
		a.fail(w, r, errs.New(403, "auth.super_only"))
		return
	}
	if err := a.requireReauth(r); err != nil {
		a.fail(w, r, err)
		return
	}
	if _, err := a.wharf(); err == nil {
		a.fail(w, r, errs.New(409, "wharf.already_paired"))
		return
	}
	var in struct {
		Addr  string
		Adopt *bool `json:"adopt"`
	}
	readJSON(r, &in)
	if in.Adopt != nil {
		a.setSetting("access.adopt_on_pair", map[bool]string{true: "1", false: "0"}[*in.Adopt])
	}
	u, err := url.Parse(strings.TrimRight(in.Addr, "/"))
	if err != nil || u.Scheme != "https" || u.Host == "" {
		a.fail(w, r, errs.New(400, "wharf.bad_url"))
		return
	}
	if ip := net.ParseIP(u.Hostname()); ip != nil && !(privateIP(ip) || ip.IsLoopback()) {
		a.fail(w, r, errs.New(400, "wharf.bad_url"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	chain, err := wharfapi.Chain(ctx, u.String())
	if err != nil {
		a.fail(w, r, errs.New(400, "wharf.unreachable", "detail", err.Error()))
		return
	}
	fp := wharfCAFP(chain)
	code := sign.RandomHex(16)
	urls := a.berthAddrsFor("lan", u.Hostname(), panelBase(r))
	if len(urls) == 0 {
		urls = []string{panelBase(r)}
	}
	base := urls[0]
	ask, err := wharfapi.RequestPair(ctx, u.String(), fp, wharfapi.PairRequest{Code: code, BerthName: a.panelDisplayName(),
		BerthURLs: urls, BerthFP: a.caFP(), WharfFP: fp, Mode: "lan"})
	if err != nil {
		var we *wharfapi.Error
		if errorsAs(err, &we) && we.Status == 404 {
			a.fail(w, r, errs.New(409, "wharf.not_discoverable"))
			return
		}
		a.fail(w, r, a.wharfErr(err))
		return
	}
	// the digits Wharf shows must be the ones computed here: same code, same two certificates
	sas := wharfapi.SAS(code, a.caFP(), fp)
	if subtle.ConstantTimeCompare([]byte(sas), []byte(ask.SAS)) != 1 {
		a.fail(w, r, errs.New(502, "wharf.sas_mismatch"))
		return
	}
	pctx, pcancel := context.WithDeadline(context.Background(), time.Unix(ask.ExpiresAt, 0).Add(5*time.Second))
	q := &wharfAsk{URL: u.String(), State: "waiting", SAS: sas, Expires: ask.ExpiresAt, fp: fp, code: code, base: base, cancel: pcancel}
	a.mu.Lock()
	if old := a.wharfAsk_; old != nil && old.cancel != nil {
		old.cancel()
	}
	a.wharfAsk_ = q
	a.mu.Unlock()
	actor := currentActor(r)
	a.pipe.Record(actor, "local", "wharf.request", map[string]any{"url": u.String()}, change.Plan{Summary: "plan.wharf.request", SummaryP: map[string]any{"url": u.String()}, Risk: change.RiskMedium})
	go a.watchWharfAsk(pctx, q, actor)
	writeJSON(w, 200, a.wharfAskView(r))
}

// watchWharfAsk polls Wharf until its admin allows or refuses (or the request runs out); once
// allowed, the pairing finishes on its own, like the browser flow.
func (a *App) watchWharfAsk(ctx context.Context, q *wharfAsk, actor change.Actor) {
	set := func(state, msg, name string) {
		a.mu.Lock()
		q.State, q.Error, q.Name = state, msg, name
		a.mu.Unlock()
		a.hub.publish(event{Type: "wharf.request", Data: map[string]any{"state": state}})
	}
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			a.mu.Lock()
			waiting := q.State == "waiting"
			a.mu.Unlock()
			if waiting {
				set("expired", "", "")
			}
			return
		case <-t.C:
		}
		c, cancel := context.WithTimeout(ctx, 8*time.Second)
		st, err := wharfapi.RequestStatus(c, q.URL, q.fp, q.code)
		cancel()
		if err != nil {
			if wharfapi.IsStatus(err, 404) {
				set("expired", "", "")
				return
			}
			continue // the router is busy or restarting: try again
		}
		switch st.State {
		case "refused":
			set("refused", "", "")
			return
		case "allowed":
			wurl := q.URL
			if u, err := url.Parse(st.WharfURL); err == nil && u.Scheme == "https" && u.Host != "" {
				wurl = strings.TrimRight(u.String(), "/")
			}
			c, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			res, err := a.completeWharfPair(c, wurl, q.fp, q.base, st.Code, actor, "lan") // found on the LAN: a LAN link
			cancel()
			if err != nil {
				e := errs.From(err)
				a.mu.Lock()
				q.errCode, q.errP = e.Code, e.Params
				a.mu.Unlock()
				set("error", "", "")
				return
			}
			set("paired", "", res.WharfName)
			a.hub.publish(event{Type: "wharf", Data: map[string]any{"paired": true}})
			return
		}
	}
}

// findWharf: the router's Wharf, quickly — the default gateway (port 7080, then 443 by the
// name wharf.lan), known by its certificate. For the Wharf page; the full LAN scan is in
// Panel links.
func (a *App) findWharf(ctx context.Context) []found {
	var out []found
	seen := map[string]bool{}
	for gw := range defaultGateways() {
		if f, ok := probePanel(ctx, gw, "7080"); ok && !seen[f.Addr] {
			f.Gateway = true
			seen[f.Addr] = true
			out = append(out, f)
		}
	}
	for _, name := range []string{"wharf.lan"} { // without a port (Wharf 0.25 "no port" on)
		if chain, err := wharfapi.Chain(ctx, "https://"+name); err == nil {
			for _, c := range chain {
				if strings.Contains(c.Issuer, "Wharf") || strings.Contains(c.Subject, "Wharf") {
					return []found{{Kind: "wharf", Addr: "https://" + name, Name: "Wharf", Gateway: true}} // its name, not the IP
				}
			}
		}
	}
	return out
}

func init() {
	routeHooks = append(routeHooks, func(a *App, mux *http.ServeMux) {
		mux.HandleFunc("POST /api/v1/wharf/find", a.authed("settings.manage", func(w http.ResponseWriter, r *http.Request) {
			ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
			defer cancel()
			items := a.findWharf(ctx)
			if items == nil {
				items = []found{}
			}
			writeJSON(w, 200, map[string]any{"items": items})
		}))
	})
}
