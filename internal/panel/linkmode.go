package panel

// LAN or internet: a link (to Wharf, or between panels) uses one pair of names, chosen when
// pairing — the LAN names (mini.lan ⇄ wharf.lan) or the public ones. It does not wander to
// the other pair when one fails; the card says which name does not answer, and the person
// can switch (no pairing again, the tokens stay).

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/wangsirco/berth/internal/change"
	"github.com/wangsirco/berth/internal/errs"
	"github.com/wangsirco/berth/internal/panel/wharfapi"
)

func linkMode(m string) string {
	if m == "public" {
		return "public"
	}
	return "lan"
}

// routerResolves: names the router's DNS answers (mDNS .local names it usually does not)
func routerResolves(name string) bool {
	for _, s := range []string{".lan", ".home.arpa", ".internal"} {
		if strings.HasSuffix(name, s) {
			return true
		}
	}
	return false
}

// berthAddrsFor: this panel's addresses for a link of this mode, best first. LAN: the name
// the router answers, then the address toward the peer (a router cannot ask mDNS).
// Internet: the public name.
func (a *App) berthAddrsFor(mode, peerHost string, here ...string) []string {
	var out []string
	add := func(u string) {
		if u != "" && !contains(out, u) {
			out = append(out, u)
		}
	}
	if mode == "public" {
		add(a.nameURL(a.mainPublic()))
		return out
	}
	if n := a.mainLAN(); routerResolves(n) {
		add(a.nameURL(n))
	}
	add(a.urlToward(peerHost))
	for _, h := range here { // the address this browser uses, when it is a LAN one
		if u, err := url.Parse(h); err == nil {
			if ip := net.ParseIP(u.Hostname()); ip != nil && (privateIP(ip) || ip.IsLoopback()) {
				add(strings.TrimRight(h, "/"))
			}
		}
	}
	if n := a.mainLAN(); !routerResolves(n) {
		add(a.nameURL(n)) // another panel on the LAN may still find host.local
	}
	return out
}

// wharfByName: a Wharf paired by the router's IP is kept by its name (wharf.lan) when that
// name is the same router and shows the same certificate
func (a *App) wharfByName(ctx context.Context, wharfURL, fp string) string {
	u, err := url.Parse(wharfURL)
	if err != nil || net.ParseIP(u.Hostname()) == nil {
		return wharfURL
	}
	for _, name := range []string{"wharf.lan", "wharf.home.arpa"} {
		ips, err := net.DefaultResolver.LookupHost(ctx, name)
		if err != nil || !contains(ips, u.Hostname()) {
			continue
		}
		cand := "https://" + name
		if p := u.Port(); p != "" {
			cand = "https://" + net.JoinHostPort(name, p)
		}
		if _, err := wharfapi.Chain(ctx, cand); err == nil && a.chainHas(ctx, cand, fp) {
			return cand
		}
	}
	return wharfURL
}

func (a *App) chainHas(ctx context.Context, base, fp string) bool {
	chain, err := wharfapi.Chain(ctx, base)
	if err != nil {
		return false
	}
	for _, c := range chain {
		if c.FP == fp || c.Public {
			return true
		}
	}
	return false
}

// wharfNames: Wharf's own names (protocol 3), remembered for the card and for switching
func (a *App) wharfNames(ctx context.Context, l *wharfLink) {
	if l.Protocol < 3 {
		return
	}
	n, err := l.api().Names(ctx)
	if err != nil {
		return
	}
	a.applyWharfNames(ctx, l, strings.TrimRight(n.LAN, "/"), strings.TrimRight(n.Public, "/"))
}

// applyWharfNames: remember Wharf's names; the link follows a rename within its own mode
func (a *App) applyWharfNames(ctx context.Context, l *wharfLink, lan, pub string) {
	if lan != l.PeerLAN || pub != l.PeerPublic {
		a.db.Exec(`UPDATE wharf_link SET peer_lan=?, peer_public=? WHERE id=1`, lan, pub)
	}
	// the link follows a renamed Wharf within its own mode (never to the other)
	want := lan
	if l.Mode == "public" {
		want = pub
	}
	if want != "" && want != l.URL {
		cctx, cancel := context.WithTimeout(ctx, 8*time.Second)
		defer cancel()
		if a.chainHas(cctx, want, l.FP) {
			a.db.Exec(`UPDATE wharf_link SET url=? WHERE id=1`, want)
		}
	}
	a.notePeerPublic(pub)
}

// notePeerPublic: the page may probe a peer's public name (open it from outside or at home)
func (a *App) notePeerPublic(u string) {
	h := ""
	if p, err := url.Parse(u); err == nil {
		h = strings.ToLower(p.Hostname())
	}
	if h == "" || net.ParseIP(h) != nil {
		return
	}
	cur := splitList(a.setting("access.peer_public"))
	if !contains(cur, h) {
		a.setSetting("access.peer_public", strings.Join(append(cur, h), ","))
	}
}

func init() {
	routeHooks = append(routeHooks, func(a *App, mux *http.ServeMux) {
		// Wharf asks for this panel's names (protocol 3)
		mux.HandleFunc("GET /api/ext/wharf/v1/names", func(w http.ResponseWriter, r *http.Request) {
			if _, ok := a.wharfAuth(w, r); !ok {
				return
			}
			lan := ""
			if n := a.mainLAN(); routerResolves(n) {
				lan = a.nameURL(n)
			}
			writeJSON(w, 200, map[string]string{"lan": lan, "public": a.nameURL(a.mainPublic()), "lan_fp": a.caFP()})
		})
		// switch the Wharf link between LAN and internet (no pairing again)
		mux.HandleFunc("POST /api/v1/wharf/mode", a.authed("settings.manage", func(w http.ResponseWriter, r *http.Request) {
			if currentUser(r).Role != RoleSuper {
				a.fail(w, r, errs.New(403, "auth.super_only"))
				return
			}
			var in struct{ Mode, URL string }
			readJSON(r, &in)
			l, err := a.wharf()
			if err != nil {
				a.fail(w, r, err)
				return
			}
			mode := linkMode(in.Mode)
			target := strings.TrimRight(in.URL, "/")
			if target == "" {
				target = l.PeerLAN
				if mode == "public" {
					target = l.PeerPublic
				}
			}
			u, err := url.Parse(target)
			if target == "" || err != nil || u.Scheme != "https" || u.Host == "" {
				a.fail(w, r, errs.New(400, "wharf.mode_needs_url"))
				return
			}
			ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
			defer cancel()
			if !a.chainHas(ctx, target, l.FP) { // the same Wharf: its own certificate, or a public one for the name
				a.fail(w, r, errs.New(400, "wharf.mode_wrong_cert", "url", target))
				return
			}
			mine := a.berthAddrsFor(mode, u.Hostname())
			if len(mine) == 0 {
				a.fail(w, r, errs.New(409, "wharf.no_public_name"))
				return
			}
			col := "peer_lan"
			if mode == "public" {
				col = "peer_public"
			}
			a.db.Exec(`UPDATE wharf_link SET mode=?, url=?, `+col+`=? WHERE id=1`, mode, target, target)
			if mode == "public" {
				a.notePeerPublic(target)
			}
			// tell Wharf which of Berth's names to use now (protocol 3; older ones keep theirs)
			if l2, err := a.wharf(); err == nil && l2.Protocol >= 3 {
				_ = l2.api().Event(ctx, map[string]any{"type": "link.mode", "mode": mode, "berth_urls": mine})
			}
			b, _ := json.Marshal(map[string]string{"mode": mode, "url": target})
			a.pipe.Record(currentActor(r), "local", "wharf.mode", json.RawMessage(b), change.Plan{Summary: "plan.wharf.mode_" + mode, SummaryP: map[string]any{"url": target}, Risk: change.RiskLow})
			wharfLiveCache.Lock()
			wharfLiveCache.at = time.Time{}
			wharfLiveCache.Unlock()
			writeJSON(w, 200, map[string]string{"mode": mode, "url": target})
		}))
	})
}


func nonNilS(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// notifyWharfNames: this panel's names changed (renamed, a public name, now without a port):
// Wharf reads /names again (protocol 3). Sent only when they really differ from last time.
func (a *App) notifyWharfNames() {
	l, err := a.wharf()
	if err != nil || l.Protocol < 3 {
		return
	}
	lan := ""
	if n := a.mainLAN(); routerResolves(n) {
		lan = a.nameURL(n)
	}
	pub := a.nameURL(a.mainPublic())
	key := lan + "|" + pub
	if key == a.setting("wharf.names_sent") {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if l.api().Event(ctx, map[string]any{"type": "names.changed", "names": map[string]string{"lan": lan, "public": pub}}) == nil {
		a.setSetting("wharf.names_sent", key)
	}
}
