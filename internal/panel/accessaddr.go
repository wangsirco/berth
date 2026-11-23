package panel

// One name per panel. The main LAN name (the chosen one, else the router-registered
// host.lan, else host.local) and the public name; any other way in (an IP, the port, another
// name) moves the browser to the main name, because Passkey belongs to a name: one added on
// mini.lan does not work on mini.local. The browser checks first that the main name opens on
// this device; then a one-time hop link carries the entry cookie (and the sign-in) across.

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/wangsirco/berth/internal/change"
	"github.com/wangsirco/berth/internal/sign"
)

// mainLAN: the LAN name the panel is opened by ("" when there is none yet)
func (a *App) mainLAN() string {
	if h := strings.ToLower(a.setting("panel.lan_host")); h != "" && net.ParseIP(h) == nil {
		return h
	}
	if l, err := a.wharf(); err == nil && l.DNSName != "" {
		return l.DNSName
	}
	if b := hostBase(); b != "" {
		return b + ".local"
	}
	return ""
}

// nameURL: https://name, without the port once the edge serves it
func (a *App) nameURL(h string) string {
	if h == "" {
		return ""
	}
	if a.entryServes(h) {
		return "https://" + h
	}
	return "https://" + net.JoinHostPort(h, a.panelPort())
}

func (a *App) mainPublic() string {
	if h := strings.ToLower(a.setting("panel.public_host")); h != "" {
		return h
	}
	return ""
}

// hops: one-time links that carry the entry cookie and the sign-in to the main name
var hops = struct {
	sync.Mutex
	m map[string]hop
}{m: map[string]hop{}}

type hop struct {
	host string
	uid  int64
	path string
	exp  time.Time
}

func init() {
	routeHooks = append(routeHooks, func(a *App, mux *http.ServeMux) {
		// where this panel lives (behind the entry gate, so no secret leaks)
		mux.HandleFunc("GET /api/v1/access/where", func(w http.ResponseWriter, r *http.Request) {
			lan, pub := a.mainLAN(), a.mainPublic()
			cur := strings.ToLower(hostOnly(r.Host))
			writeJSON(w, 200, map[string]any{
				"lan": a.nameURL(lan), "lan_name": lan, "public": a.nameURL(pub), "public_name": pub,
				"current": cur, "is_main": cur == lan || (pub != "" && cur == pub),
				"fallback": "https://" + net.JoinHostPort(firstNonEmpty(a.lanIP(), cur), a.panelPort()),
				"setup":    a.userCount() == 0,
				"renamed":  a.setting("access.renamed"), // "old>new" after the main name changed (Passkey needs adding again)
			})
		})
		// a one-time link to the main name (lan | public) that keeps the entry and the sign-in
		mux.HandleFunc("GET /api/v1/access/hop", func(w http.ResponseWriter, r *http.Request) {
			to := r.URL.Query().Get("to")
			host := a.mainLAN()
			if to == "public" {
				host = a.mainPublic()
			}
			if host == "" {
				writeJSON(w, 404, map[string]string{"error": "no name"})
				return
			}
			path := r.URL.Query().Get("path")
			if !strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") || strings.ContainsAny(path, "\\\r\n") {
				path = "/"
			}
			h := hop{host: host, path: path, exp: time.Now().Add(60 * time.Second)}
			if s := a.loadSession(r); s != nil {
				h.uid = s.user.ID
			}
			t := sign.RandomHex(24)
			hops.Lock()
			for k, v := range hops.m {
				if time.Now().After(v.exp) {
					delete(hops.m, k)
				}
			}
			hops.m[tokenHash(t)] = h
			hops.Unlock()
			writeJSON(w, 200, map[string]string{"url": a.nameURL(host) + "/hop/" + t})
		})
		// the hop lands on the main name: entry cookie, sign-in, then the page
		mux.HandleFunc("GET /hop/{t}", func(w http.ResponseWriter, r *http.Request) {
			k := tokenHash(r.PathValue("t"))
			hops.Lock()
			h, ok := hops.m[k]
			delete(hops.m, k)
			hops.Unlock()
			if !ok || time.Now().After(h.exp) || subtle.ConstantTimeCompare([]byte(strings.ToLower(hostOnly(r.Host))), []byte(h.host)) != 1 {
				blank404(w)
				return
			}
			if e := a.setting("access.entry"); e != "" {
				setCookie(w, entryCookie, a.entryToken(e), 30*24*3600)
			}
			dest := h.path
			if h.uid != 0 {
				if u, err := a.userByID(h.uid); err == nil && !u.Disabled {
					if _, err := a.createSession(w, r, u, "hop", "full"); err == nil {
						a.recordLogin(r, u.Username, u.ID, true, "hop", "")
					}
				}
			} else if dest == "/" {
				dest = "/login"
			}
			http.Redirect(w, r, dest, http.StatusFound)
		})
		mux.HandleFunc("POST /api/v1/access/renamed/seen", a.authed("", func(w http.ResponseWriter, r *http.Request) {
			a.setSetting("access.renamed", "")
			writeJSON(w, 200, map[string]bool{"ok": true})
		}))
	})
}

// adoptLANName: paired with a Wharf that registers names, the panel's LAN name becomes
// host.lan (every device on the LAN resolves it, not only those that speak mDNS)
func (a *App) adoptLANName(scopes []string) {
	if !contains(scopes, "dns") {
		return
	}
	cur := strings.ToLower(a.setting("panel.lan_host"))
	if strings.HasSuffix(cur, ".lan") || hostBase() == "" {
		return
	}
	if cur != "" && !strings.HasSuffix(cur, ".local") { // a name the person chose (home.arpa, …) stays
		return
	}
	old := firstNonEmpty(cur, hostBase()+".local")
	name := hostBase() + ".lan"
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	p, _ := json.Marshal(map[string]string{"name": name})
	c, err := a.pipe.Run(ctx, change.Actor{Type: "system", ID: "wharf-pair", Name: "Berth", Via: "system", IP: "127.0.0.1"}, "local", "panel.local_name", p)
	if err != nil || c == nil {
		return
	}
	for i := 0; i < 120 && c.Status != "done" && c.Status != "failed"; i++ {
		time.Sleep(time.Second)
		if x, err := a.pipe.Get(c.ID); err == nil {
			c = x
		}
	}
	if c.Status == "done" {
		a.setSetting("access.renamed", old+">"+name)
	}
}


// connectSrc: where the page may probe besides itself — the panel's own names and LAN names
// in general (does this device open the main name? is the router's Wharf reachable from
// here?), and the public names of the panel and its peers
func (a *App) connectSrc() string {
	out := []string{"https://*.local:*", "https://*.lan:*", "https://*.home.arpa:*", "https://*.internal:*"}
	for _, h := range a.peerPublicNames() {
		if h != "" && dnsNameRe.MatchString(h) {
			out = append(out, "https://"+h+":*")
		}
	}
	return strings.Join(out, " ")
}

// peerPublicNames: this panel's public name and those of the Wharf and linked panels
func (a *App) peerPublicNames() []string {
	names := []string{strings.ToLower(a.setting("panel.public_host"))}
	names = append(names, splitList(a.setting("access.peer_public"))...)
	return names
}
