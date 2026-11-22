package panel

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/wangsirco/berth/internal/change"
	"github.com/wangsirco/berth/internal/errs"
	"github.com/wangsirco/berth/internal/version"
)

// pending inspections on the managed side: the code's own key pair waits here until the
// admin confirms (or 15 minutes pass)
var linkInspect = struct {
	sync.Mutex
	m map[string]inspected
}{m: map[string]inspected{}}

type inspected struct {
	code   linkCode
	base   string // the main panel's address that answered
	priv   *ecdh.PrivateKey
	sas    string
	user   int64
	expire time.Time
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
		mux.HandleFunc("GET /api/v1/links", super(a.handleLinks))
		mux.HandleFunc("POST /api/v1/links/code", super(a.handleLinkCode))
		mux.HandleFunc("POST /api/v1/links/inspect", super(a.handleLinkInspect))
		mux.HandleFunc("POST /api/v1/links/accept", super(a.handleLinkAccept))
		mux.HandleFunc("POST /api/v1/links/{id}/confirm", super(a.handleLinkConfirm))
		mux.HandleFunc("DELETE /api/v1/links/{id}", super(a.handleLinkDelete))
		// the peer panel (each proves itself: one-time secret, or a request signed with the link key)
		mux.HandleFunc("POST /link-api/hello", a.handleLinkHello)
		mux.HandleFunc("POST /link-api/offer", a.handleLinkOffer)
		mux.HandleFunc("POST /link-api/activate", a.handleLinkActivate)
		mux.HandleFunc("POST /link-api/ping", a.handleLinkPing)
		mux.HandleFunc("POST /link-api/unlink", a.handleLinkUnlink)
		mux.HandleFunc("/link-api/fwd/", a.handleLinkForward)
		// host-scoped views of a change (a linked panel follows its changes this way)
		mux.HandleFunc("GET /api/v1/hosts/{host}/changes/{id}", a.authed("", func(w http.ResponseWriter, r *http.Request) {
			if c, err := a.pipe.Get(r.PathValue("id")); err != nil || c.Host != r.PathValue("host") {
				a.fail(w, r, errs.New(404, "change.not_found"))
				return
			}
			a.handleChange(w, r)
		}))
		mux.HandleFunc("POST /api/v1/hosts/{host}/changes/{id}/revert", a.authed("", func(w http.ResponseWriter, r *http.Request) {
			if c, err := a.pipe.Get(r.PathValue("id")); err != nil || c.Host != r.PathValue("host") {
				a.fail(w, r, errs.New(404, "change.not_found"))
				return
			}
			a.handleRevert(w, r)
		}))
		mux.HandleFunc("GET /api/v1/hosts/{host}/vm-screen/{key}", a.authed("vms.manage", a.handleVMScreenWS))
	})
	startHooks = append(startHooks, func(a *App, ctx context.Context) { go a.linkProber(ctx) })
}

// ---- the list ----

type linkView struct {
	*panelLink
	Live linkLive `json:"live"`
}

func (a *App) handleLinks(w http.ResponseWriter, r *http.Request) {
	a.db.Exec(`DELETE FROM panel_links WHERE status='code' AND expires_at<?`, time.Now().Unix())
	out := []linkView{}
	for _, l := range a.links(`ORDER BY id`) {
		out = append(out, linkView{l, a.liveOf(l)})
	}
	writeJSON(w, 200, map[string]any{"items": out, "name": a.panelDisplayName(), "fp": a.caFP(), "addrs": a.ourAddrs(r)})
}

// ---- 1. main: a code ----

func (a *App) handleLinkCode(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Addrs []string `json:"addrs"` // where the peer should reach this panel, in order (default: what this panel knows)
	}
	readJSON(r, &in)
	addrs := []string{}
	for _, x := range in.Addrs {
		if u, err := url.Parse(strings.TrimRight(strings.TrimSpace(x), "/")); err == nil && u.Scheme == "https" && u.Host != "" && !contains(addrs, u.String()) && len(addrs) < 6 {
			addrs = append(addrs, u.String())
		}
	}
	if len(addrs) == 0 {
		addrs = a.ourAddrs(r)
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
	res, err := a.db.Exec(`INSERT INTO panel_links(role, status, secret_hash, priv_enc, by_user, by_id, created_at, expires_at) VALUES(?,?,?,?,?,?,?,?)`,
		linkRoleMain, "code", secretHash(secret), privEnc, u.Username, u.ID, time.Now().Unix(), exp)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	id, _ := res.LastInsertId()
	code := encodeLinkCode(linkCode{V: 1, Addrs: addrs, FP: a.caFP(), S: secret, Name: a.panelDisplayName(), Pub: base64.StdEncoding.EncodeToString(priv.PublicKey().Bytes())})
	_, _ = a.pipe.Record(currentActor(r), "local", "link.code", map[string]any{"id": id}, change.Plan{Summary: "plan.link.code", Risk: change.RiskMedium})
	writeJSON(w, 200, map[string]any{"id": id, "code": code, "expires_at": exp})
}

// ---- 2. managed: look at a code ----

func (a *App) handleLinkInspect(w http.ResponseWriter, r *http.Request) {
	var in struct{ Code string }
	readJSON(r, &in)
	c, err := decodeLinkCode(in.Code)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if c.FP == a.caFP() {
		a.fail(w, r, errs.New(400, "link.self"))
		return
	}
	if l := a.links(`WHERE role=? AND status IN ('wait','active')`, linkRoleManaged); len(l) > 0 {
		a.fail(w, r, errs.New(409, "link.already_managed", "name", l[0].Name))
		return
	}
	var hello struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	}
	base, err := "", error(nil)
	for _, b := range c.Addrs {
		ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
		err = linkPlain(ctx, b, c.FP, "/link-api/hello", map[string]string{"s": c.S}, &hello)
		cancel()
		if err == nil {
			base = b
			break
		}
	}
	if base == "" {
		a.fail(w, r, errs.New(502, "link.unreachable", "detail", errText(err)))
		return
	}
	priv, _ := ecdh.X25519().GenerateKey(rand.Reader)
	mainPub, _ := base64.StdEncoding.DecodeString(c.Pub)
	sas := linkSAS(mainPub, priv.PublicKey().Bytes())
	linkInspect.Lock()
	for k, v := range linkInspect.m {
		if time.Now().After(v.expire) {
			delete(linkInspect.m, k)
		}
	}
	linkInspect.m[secretHash(c.S)] = inspected{code: c, base: base, priv: priv, sas: sas, user: currentUser(r).ID, expire: time.Now().Add(linkCodeTTL)}
	linkInspect.Unlock()
	hosts := []string{}
	for _, h := range a.knownHosts() {
		hosts = append(hosts, h)
	}
	ip := ""
	if u, err := url.Parse(base); err == nil {
		ip = u.Hostname()
	}
	writeJSON(w, 200, map[string]any{"name": firstNonEmpty(hello.Name, c.Name), "version": hello.Version, "addr": base, "addrs": c.Addrs, "fp": c.FP,
		"sas": sas, "hosts": hosts, "self_addrs": a.ourAddrs(r), "host": ip})
}

// knownHosts: this panel's servers (connected or registered)
func (a *App) knownHosts() []string {
	all := a.connectedHosts()
	for _, rh := range a.remoteHosts() {
		if !contains(all, rh.Name) {
			all = append(all, rh.Name)
		}
	}
	if !contains(all, "local") {
		all = append([]string{"local"}, all...)
	}
	return all
}

// ---- 3. managed: accept (hand the chosen servers over) ----

func (a *App) handleLinkAccept(w http.ResponseWriter, r *http.Request) {
	if err := a.requireReauth(r); err != nil {
		a.fail(w, r, err)
		return
	}
	var in struct {
		Code  string   `json:"code"`
		Hosts []string `json:"hosts"`
		Addrs []string `json:"addrs"` // where the main panel should reach us (default: ours)
	}
	readJSON(r, &in)
	c, err := decodeLinkCode(in.Code)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	linkInspect.Lock()
	ins, ok := linkInspect.m[secretHash(c.S)]
	delete(linkInspect.m, secretHash(c.S))
	linkInspect.Unlock()
	if !ok || time.Now().After(ins.expire) || ins.user != currentUser(r).ID {
		a.fail(w, r, errs.New(409, "link.inspect_first"))
		return
	}
	known := a.knownHosts()
	hosts := []string{}
	for _, h := range in.Hosts {
		if contains(known, h) && !contains(hosts, h) {
			hosts = append(hosts, h)
		}
	}
	if len(hosts) == 0 {
		a.fail(w, r, errs.New(400, "link.no_hosts"))
		return
	}
	addrs := []string{}
	for _, x := range in.Addrs {
		if u, err := url.Parse(strings.TrimRight(strings.TrimSpace(x), "/")); err == nil && u.Scheme == "https" && u.Host != "" && !contains(addrs, u.String()) {
			addrs = append(addrs, u.String())
		}
	}
	if len(addrs) == 0 {
		addrs = a.ourAddrs(r)
	}
	mainPub, _ := base64.StdEncoding.DecodeString(c.Pub)
	myPub := ins.priv.PublicKey().Bytes()
	key, err := linkKey(ins.priv, mainPub, mainPub, myPub)
	if err != nil {
		a.fail(w, r, errs.New(400, "link.bad_code"))
		return
	}
	uid := randUID()
	keyEnc, _ := a.seal(key)
	u := currentUser(r)
	res, err := a.db.Exec(`INSERT INTO panel_links(role, status, name, addrs, fp, uid, key_enc, sas, hosts, by_user, by_id, created_at, last_addr) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		linkRoleManaged, "wait", c.Name, jsonText(c.Addrs), c.FP, uid, keyEnc, ins.sas, jsonText(hosts), u.Username, u.ID, time.Now().Unix(), ins.base)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	id, _ := res.LastInsertId()
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	err = linkPlain(ctx, ins.base, c.FP, "/link-api/offer", map[string]any{"s": c.S, "uid": uid, "pub": base64.StdEncoding.EncodeToString(myPub),
		"name": a.panelDisplayName(), "fp": a.caFP(), "addrs": addrs, "hosts": hosts}, nil)
	if err != nil {
		a.db.Exec(`DELETE FROM panel_links WHERE id=?`, id)
		a.fail(w, r, errs.New(502, "link.unreachable", "detail", errText(err)))
		return
	}
	_, _ = a.pipe.Record(currentActor(r), "local", "link.accept", map[string]any{"id": id, "main": c.Name, "hosts": hosts},
		change.Plan{Summary: "plan.link.accept", SummaryP: map[string]any{"name": c.Name, "hosts": strings.Join(hosts, ", ")}, Risk: change.RiskHigh})
	writeJSON(w, 200, map[string]any{"id": id, "sas": ins.sas})
}

// ---- main: the managed panel's answer (public; the one-time secret proves it) ----

func (a *App) handleLinkHello(w http.ResponseWriter, r *http.Request) {
	var in struct{ S string }
	readJSON(r, &in)
	if _, err := a.codeRow(in.S); err != nil {
		a.limiter.record("linkip:" + a.clientIP(r))
		blank404(w)
		return
	}
	writeJSON(w, 200, map[string]string{"name": a.panelDisplayName(), "version": version.Version})
}

func (a *App) codeRow(secret string) (*panelLink, error) {
	if len(secret) < 32 || a.limiter.blocked("linkip:") {
		return nil, errs.New(404, "link.not_found")
	}
	l, err := a.link(`WHERE role=? AND status='code' AND secret_hash=?`, linkRoleMain, secretHash(secret))
	if err != nil || time.Now().Unix() > l.ExpiresAt {
		return nil, errs.New(404, "link.not_found")
	}
	return l, nil
}

func (a *App) handleLinkOffer(w http.ResponseWriter, r *http.Request) {
	var in struct {
		S, UID, Pub, Name, FP string
		Addrs, Hosts          []string
	}
	readJSON(r, &in)
	l, err := a.codeRow(in.S)
	if err != nil || len(in.UID) != 32 || len(l.priv) == 0 {
		a.limiter.record("linkip:" + a.clientIP(r))
		blank404(w)
		return
	}
	priv, err := ecdh.X25519().NewPrivateKey(l.priv)
	peerPub, err2 := base64.StdEncoding.DecodeString(in.Pub)
	if err != nil || err2 != nil {
		a.fail(w, r, errs.New(400, "link.bad_code"))
		return
	}
	myPub := priv.PublicKey().Bytes()
	key, err := linkKey(priv, peerPub, myPub, peerPub)
	if err != nil {
		a.fail(w, r, errs.New(400, "link.bad_code"))
		return
	}
	addrs := []string{}
	for _, x := range in.Addrs {
		if u, err := url.Parse(x); err == nil && u.Scheme == "https" && u.Host != "" && len(addrs) < 6 {
			addrs = append(addrs, u.String())
		}
	}
	hosts := []string{}
	for _, h := range in.Hosts {
		if hostNameRe.MatchString(h) && len(hosts) < 200 {
			hosts = append(hosts, h)
		}
	}
	if len(addrs) == 0 || len(hosts) == 0 || len(in.FP) != 64 {
		a.fail(w, r, errs.New(400, "link.bad_code"))
		return
	}
	keyEnc, _ := a.seal(key)
	name := truncate(strings.TrimSpace(in.Name), 60)
	a.db.Exec(`UPDATE panel_links SET status='confirm', name=?, slug=?, addrs=?, fp=?, uid=?, key_enc=?, sas=?, hosts=?, secret_hash='', priv_enc=NULL WHERE id=?`,
		name, a.linkSlug(name, l.ID), jsonText(addrs), strings.ToLower(in.FP), in.UID, keyEnc, linkSAS(myPub, peerPub), jsonText(hosts), l.ID)
	a.hub.publish(event{Type: "link.pending", Data: map[string]any{"id": l.ID, "name": name}})
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// ---- 4. main: confirm ----

func (a *App) handleLinkConfirm(w http.ResponseWriter, r *http.Request) {
	if err := a.requireReauth(r); err != nil {
		a.fail(w, r, err)
		return
	}
	l, err := a.link(`WHERE id=? AND role=? AND status='confirm'`, r.PathValue("id"), linkRoleMain)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	if err := a.linkCall(ctx, l, "POST", "/link-api/activate", map[string]string{"name": a.panelDisplayName()}, nil); err != nil {
		a.fail(w, r, err)
		return
	}
	a.db.Exec(`UPDATE panel_links SET status='active', confirmed_at=? WHERE id=?`, time.Now().Unix(), l.ID)
	_, _ = a.pipe.Record(currentActor(r), "local", "link.confirm", map[string]any{"id": l.ID, "peer": l.Name, "hosts": l.Hosts},
		change.Plan{Summary: "plan.link.confirm", SummaryP: map[string]any{"name": l.Name, "hosts": strings.Join(l.Hosts, ", ")}, Risk: change.RiskHigh})
	go a.probeLinks(context.Background())
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (a *App) handleLinkActivate(w http.ResponseWriter, r *http.Request) {
	l, _, err := a.linkVerify(r, linkRoleManaged)
	if err != nil || l.Status != "wait" {
		blank404(w)
		return
	}
	a.db.Exec(`UPDATE panel_links SET status='active', confirmed_at=? WHERE id=?`, time.Now().Unix(), l.ID)
	_, _ = a.pipe.Record(linkActor(l, "", a.clientIP(r)), "local", "link.active", map[string]any{"id": l.ID, "main": l.Name},
		change.Plan{Summary: "plan.link.active", SummaryP: map[string]any{"name": l.Name}, Risk: change.RiskHigh})
	go a.probeLinks(context.Background())
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (a *App) handleLinkPing(w http.ResponseWriter, r *http.Request) {
	l, _, err := a.linkVerify(r)
	if err != nil || l.Status != "active" && l.Status != "wait" && l.Status != "confirm" {
		blank404(w)
		return
	}
	writeJSON(w, 200, map[string]string{"name": a.panelDisplayName(), "version": version.Version})
}

// ---- unlink (either side; the other is told, and forgets too) ----

func (a *App) handleLinkDelete(w http.ResponseWriter, r *http.Request) {
	l, err := a.link(`WHERE id=?`, r.PathValue("id"))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	told := false
	if len(l.key) > 0 && l.Status != "code" {
		ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
		told = a.linkCall(ctx, l, "POST", "/link-api/unlink", map[string]string{}, nil) == nil
		cancel()
	}
	a.db.Exec(`DELETE FROM panel_links WHERE id=?`, l.ID)
	_, _ = a.pipe.Record(currentActor(r), "local", "link.delete", map[string]any{"id": l.ID, "peer": l.Name, "role": l.Role, "told": told},
		change.Plan{Summary: "plan.link.delete", SummaryP: map[string]any{"name": firstNonEmpty(l.Name, "—")}, Risk: change.RiskMedium})
	go a.probeLinks(context.Background())
	writeJSON(w, 200, map[string]bool{"ok": true, "told": told})
}

func (a *App) handleLinkUnlink(w http.ResponseWriter, r *http.Request) {
	l, _, err := a.linkVerify(r)
	if err != nil {
		blank404(w)
		return
	}
	a.db.Exec(`DELETE FROM panel_links WHERE id=?`, l.ID)
	_, _ = a.pipe.Record(linkActor(l, "", a.clientIP(r)), "local", "link.delete", map[string]any{"id": l.ID, "peer": l.Name, "role": l.Role, "by_peer": true},
		change.Plan{Summary: "plan.link.delete_by_peer", SummaryP: map[string]any{"name": l.Name}, Risk: change.RiskMedium})
	go a.probeLinks(context.Background())
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// ---- managed: run a request for a handed-over server, as a restricted admin ----

type linkIdentKey struct{}

type linkIdent struct {
	user  *User
	actor change.Actor
}

func linkActor(l *panelLink, who, ip string) change.Actor {
	name := l.Name
	if who != "" {
		name += " · " + who
	}
	return change.Actor{Type: "link", ID: l.UID[:12], Name: name, Via: "link", IP: ip}
}

// linkUser: an admin (never super: no settings, users, tokens or plugins) on the handed-over
// servers only, with all of their files.
func linkUser(l *panelLink) *User {
	perms := []string{}
	for _, p := range Permissions {
		if !p.Super {
			perms = append(perms, p.Key)
		}
	}
	return &User{ID: -l.ID, Username: "link:" + firstNonEmpty(l.Slug, strconv.FormatInt(l.ID, 10)), DisplayName: l.Name, Role: RoleAdmin, Perms: perms,
		Scope: UserScope{Hosts: l.Hosts, Paths: []string{"/"}}} // the whole server, like this panel's own admin (the terminal reaches it anyway)
}

func linkFrom(r *http.Request) *linkIdent {
	li, _ := r.Context().Value(linkIdentKey{}).(*linkIdent)
	return li
}

func (a *App) handleLinkForward(w http.ResponseWriter, r *http.Request) {
	l, body, err := a.linkVerify(r, linkRoleManaged)
	if err != nil || l.Status != "active" {
		blank404(w)
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/link-api/fwd/")
	who := truncate(r.Header.Get("X-Berth-On-Behalf"), 40)
	ident := &linkIdent{user: linkUser(l), actor: linkActor(l, who, a.clientIP(r))}
	r2 := r.Clone(context.WithValue(r.Context(), linkIdentKey{}, ident))
	r2.Body = http.NoBody
	if len(body) > 0 {
		r2.Body = ioNop(body)
	}
	for _, h := range []string{"Authorization", "Cookie", "X-Csrf-Token", "Origin"} {
		r2.Header.Del(h)
	}
	if rest == "hosts" && r.Method == http.MethodGet { // the list, only the handed-over servers
		r2.URL.Path = "/api/v1/hosts"
		rec := httptest.NewRecorder()
		a.apiMux.ServeHTTP(rec, r2)
		var hv struct {
			Items []hostView `json:"items"`
			Name  string     `json:"name"`
		}
		json.Unmarshal(rec.Body.Bytes(), &hv)
		keep := []hostView{}
		for _, h := range hv.Items {
			if contains(l.Hosts, h.Name) {
				keep = append(keep, h)
			}
		}
		writeJSON(w, 200, map[string]any{"items": keep, "name": a.panelDisplayName()})
		return
	}
	// only /api/v1/hosts/<a handed-over server>/…
	parts := strings.SplitN(rest, "/", 3)
	if len(parts) < 3 || parts[0] != "hosts" || !contains(l.Hosts, parts[1]) {
		a.fail(w, r, errs.New(403, "link.out_of_scope"))
		return
	}
	r2.URL.Path = "/api/v1/" + rest
	r2.URL.RawPath = ""
	r2.RequestURI = r2.URL.RequestURI()
	a.apiMux.ServeHTTP(w, r2)
}

func ioNop(b []byte) *nopBody { return &nopBody{bytes.NewReader(b)} }

type nopBody struct{ *bytes.Reader }

func (nopBody) Close() error { return nil }

// ---- main: servers of a linked panel, operated from here ----

// peerRoute sends /api/v1/hosts/<server>@<peer>/… to the peer panel (signed), for a signed-in
// super admin here. Everything else goes on as usual.
func (a *App) peerRoute(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/v1/hosts/") {
			next.ServeHTTP(w, r)
			return
		}
		seg := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/api/v1/hosts/"), "/", 2)
		at := strings.LastIndex(seg[0], "@")
		if at <= 0 {
			next.ServeHTTP(w, r)
			return
		}
		host, slug := seg[0][:at], seg[0][at+1:]
		rest := ""
		if len(seg) == 2 {
			rest = seg[1]
		}
		a.authed("settings.manage", func(w http.ResponseWriter, r *http.Request) {
			if currentUser(r).Role != RoleSuper {
				a.fail(w, r, errs.New(403, "auth.super_only"))
				return
			}
			l, err := a.link(`WHERE role=? AND status='active' AND slug=?`, linkRoleMain, slug)
			if err != nil || !contains(l.Hosts, host) {
				a.fail(w, r, errs.New(404, "host.not_found", "host", seg[0]))
				return
			}
			a.forwardToPeer(w, r, l, "/link-api/fwd/hosts/"+url.PathEscape(host)+"/"+rest)
		})(w, r)
	})
}

func (a *App) forwardToPeer(w http.ResponseWriter, r *http.Request, l *panelLink, path string) {
	body, err := readAllLimit(r, 64<<20)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	base := firstNonEmpty(a.liveOf(l).Addr, l.LastAddr)
	if base == "" && len(l.Addrs) > 0 {
		base = l.Addrs[0]
	}
	target, err := url.Parse(base)
	if err != nil || base == "" {
		a.fail(w, r, errs.New(502, "link.unreachable", "detail", "no address"))
		return
	}
	u := currentUser(r)
	if r.Method != http.MethodGet && r.Method != http.MethodHead && !strings.HasSuffix(path, "/ws") {
		_, _ = a.pipe.Record(currentActor(r), "local", "link.forward", map[string]any{"peer": l.Name, "method": r.Method, "path": path},
			change.Plan{Summary: "plan.link.forward", SummaryP: map[string]any{"name": l.Name, "what": r.Method + " " + strings.TrimPrefix(path, "/link-api/fwd/")}, Risk: change.RiskLow})
	}
	rp := &httputil.ReverseProxy{
		Transport:     linkClient(l.FP).Transport,
		FlushInterval: -1, // streams (task logs, terminals) go through as they come
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.URL.Scheme, pr.Out.URL.Host = target.Scheme, target.Host
			pr.Out.URL.Path, pr.Out.URL.RawPath = path, ""
			pr.Out.URL.RawQuery = r.URL.RawQuery
			pr.Out.Host = target.Host
			for _, h := range []string{"Cookie", "Authorization", "X-Csrf-Token", "Origin", "Referer"} {
				pr.Out.Header.Del(h)
			}
			pr.Out.Header.Set("X-Berth-On-Behalf", u.Username)
			pr.Out.Body = ioNop(body)
			pr.Out.ContentLength = int64(len(body))
			if len(body) == 0 && (r.Method == http.MethodGet || r.Method == http.MethodHead) {
				pr.Out.Body = http.NoBody
			}
			linkSign(pr.Out, l, body)
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			a.fail(w, r, errs.New(502, "link.unreachable", "detail", err.Error()))
		},
	}
	rp.ServeHTTP(w, r)
}

func readAllLimit(r *http.Request, n int64) ([]byte, error) {
	if r.Body == nil {
		return nil, nil
	}
	var b bytes.Buffer
	_, err := b.ReadFrom(http.MaxBytesReader(nil, r.Body, n))
	return b.Bytes(), err
}

// peerHosts: the linked panels' servers as hosts here (name@peer)
func (a *App) peerHosts() []hostView {
	out := []hostView{}
	for _, l := range a.links(`WHERE role=? AND status='active' ORDER BY id`, linkRoleMain) {
		live := a.liveOf(l)
		on := map[string]peerHost{}
		for _, h := range live.Hosts {
			on[h.Name] = h
		}
		for _, h := range l.Hosts {
			ph := on[h]
			out = append(out, hostView{Name: h + "@" + l.Slug, Online: live.Online && ph.Online, Remote: true, Version: ph.Version,
				Peer: &peerInfo{ID: l.ID, Name: l.Name, Slug: l.Slug, Route: live.Route, Via: live.Via, RTT: live.RTT}})
		}
	}
	return out
}

type peerInfo struct {
	ID    int64   `json:"id"`
	Name  string  `json:"name"`
	Slug  string  `json:"slug"`
	Route string  `json:"route,omitempty"`
	Via   string  `json:"via,omitempty"`
	RTT   float64 `json:"rtt_ms,omitempty"`
}
