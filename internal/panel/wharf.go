package panel

// Wharf link (方案 §12, docs/wharf-berth-extension.md): pairing, the two tokens, and the
// API Wharf calls on Berth (/api/ext/wharf/v1/…). What Berth does at Wharf lives in
// wharf_features.go.

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/wangsirco/berth/internal/change"
	"github.com/wangsirco/berth/internal/errs"
	"github.com/wangsirco/berth/internal/panel/wharfapi"
	"github.com/wangsirco/berth/internal/sign"
)

type wharfLink struct {
	URL           string   `json:"url"`
	Name          string   `json:"name"`
	FP            string   `json:"fp"`
	Scopes        []string `json:"scopes"`
	TargetIPs     []string `json:"target_ips"`
	AlertsPrimary string   `json:"alerts_primary"` // "" | wharf | berth
	PairedAt      int64    `json:"paired_at"`
	LastSeen      int64    `json:"last_seen,omitempty"`
	ExpiresAt     int64    `json:"expires_at,omitempty"`
	State         string   `json:"state"` // active | broken
	token         string
	inboundHash   string
}

func (a *App) wharf() (*wharfLink, error) {
	var l wharfLink
	var enc []byte
	var scopes, targets string
	err := a.db.QueryRow(`SELECT url, name, fp, token_enc, inbound_hash, scopes, target_ips, alerts_primary, paired_at, last_seen, expires_at, state FROM wharf_link WHERE id=1`).
		Scan(&l.URL, &l.Name, &l.FP, &enc, &l.inboundHash, &scopes, &targets, &l.AlertsPrimary, &l.PairedAt, &l.LastSeen, &l.ExpiresAt, &l.State)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errs.New(409, "wharf.not_paired")
	}
	if err != nil {
		return nil, err
	}
	json.Unmarshal([]byte(scopes), &l.Scopes)
	json.Unmarshal([]byte(targets), &l.TargetIPs)
	tok, err := a.open(enc)
	if err != nil {
		return nil, err
	}
	l.token = string(tok)
	return &l, nil
}

func (l *wharfLink) has(scope string) bool { return contains(l.Scopes, scope) }

// client returns a Wharf client when the link has scope.
func (a *App) wharfClient(scope string) (*wharfapi.Client, *wharfLink, error) {
	l, err := a.wharf()
	if err != nil {
		return nil, nil, err
	}
	if scope != "" && !l.has(scope) {
		return nil, nil, errs.New(403, "wharf.no_scope", "scope", scope)
	}
	return wharfapi.New(l.URL, l.FP, l.token), l, nil
}

// wharfErr turns a Wharf answer into something translatable (Wharf already speaks Chinese).
func (a *App) wharfErr(err error) error {
	if err == nil {
		return nil
	}
	var we *wharfapi.Error
	if errors.As(err, &we) {
		if we.Status == 401 {
			a.db.Exec(`UPDATE wharf_link SET state='broken' WHERE id=1`)
			return errs.New(502, "wharf.link_broken", "detail", we.Message)
		}
		return errs.New(502, "wharf.error", "detail", we.Message)
	}
	return errs.New(502, "wharf.error", "detail", err.Error())
}

// caFP is this panel's CA fingerprint (what Wharf pins for Berth).
func (a *App) caFP() string {
	b, _ := pem.Decode(a.caPEM)
	if b == nil {
		return ""
	}
	h := sha256.Sum256(b.Bytes)
	return hex.EncodeToString(h[:])
}

func hashToken(t string) string {
	h := sha256.Sum256([]byte(t))
	return hex.EncodeToString(h[:])
}

// ---- inbound: Wharf calling Berth ----

// wharfAuth checks Wharf's token, signature, time window and nonce; it returns the body.
func (a *App) wharfAuth(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	deny := func() ([]byte, bool) {
		writeJSON(w, 401, map[string]string{"error": "对接已失效或签名无效"})
		return nil, false
	}
	l, err := a.wharf()
	if err != nil {
		return deny()
	}
	tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !strings.HasPrefix(tok, "berth_wrf_") || subtle.ConstantTimeCompare([]byte(hashToken(tok)), []byte(l.inboundHash)) != 1 {
		return deny()
	}
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	nonce, err := sign.CheckPrefixed(r, "X-Wharf", sign.DeriveKey(tok), body)
	if err != nil {
		return deny()
	}
	res, err := a.db.Exec(`INSERT OR IGNORE INTO wharf_nonces(nonce, seen_at) VALUES(?,?)`, nonce, time.Now().Unix())
	if n, _ := res.RowsAffected(); err != nil || n == 0 {
		return deny() // replayed
	}
	a.db.Exec(`UPDATE wharf_link SET last_seen=?, state='active' WHERE id=1`, time.Now().Unix())
	return body, true
}

var wharfActor = change.Actor{Type: "integration", ID: "wharf", Name: "Wharf", Via: "wharf", IP: "wharf"}

func init() {
	routeHooks = append(routeHooks, func(a *App, mux *http.ServeMux) {
		// ---- people (settings) ----
		mux.HandleFunc("GET /api/v1/wharf", a.authed("", func(w http.ResponseWriter, r *http.Request) {
			out := map[string]any{"paired": false, "berth_fp": a.caFP()}
			if l, err := a.wharf(); err == nil {
				out["paired"], out["link"] = true, l
				out["live"] = a.wharfLive(r.Context(), l.URL, l.FP) // LAN or internet, latency, where it is
				maps := []map[string]any{}
				rows, err := a.db.Query(`SELECT m.wharf_user, u.username FROM wharf_user_map m JOIN users u ON u.id = m.berth_user_id ORDER BY m.wharf_user`)
				if err == nil {
					for rows.Next() {
						var wu, bu string
						rows.Scan(&wu, &bu)
						maps = append(maps, map[string]any{"wharf_user": wu, "berth_user": bu})
					}
					rows.Close()
				}
				out["user_map"] = maps
			}
			writeJSON(w, 200, out)
		}))
		// 1. look at Wharf's certificate before trusting it
		mux.HandleFunc("POST /api/v1/wharf/probe", a.authed("settings.manage", func(w http.ResponseWriter, r *http.Request) {
			var in struct{ URL string }
			readJSON(r, &in)
			chain, err := wharfapi.Chain(r.Context(), in.URL)
			if err != nil {
				a.fail(w, r, errs.New(400, "wharf.unreachable", "detail", err.Error()))
				return
			}
			writeJSON(w, 200, map[string]any{"chain": chain})
		}))
		// 2. remember the pending pairing and send the person to Wharf's approval page
		mux.HandleFunc("POST /api/v1/wharf/pair/start", a.authed("settings.manage", func(w http.ResponseWriter, r *http.Request) {
			if currentUser(r).Role != RoleSuper {
				a.fail(w, r, errs.New(403, "auth.forbidden"))
				return
			}
			var in struct{ URL, FP string }
			readJSON(r, &in)
			u, err := url.Parse(strings.TrimRight(in.URL, "/"))
			if err != nil || u.Scheme != "https" || u.Host == "" || len(in.FP) != 64 {
				a.fail(w, r, errs.New(400, "wharf.bad_url"))
				return
			}
			if _, err := a.wharf(); err == nil {
				a.fail(w, r, errs.New(409, "wharf.already_paired"))
				return
			}
			code := sign.RandomHex(16)
			p, _ := json.Marshal(map[string]any{"url": u.String(), "fp": strings.ToLower(in.FP), "code": code, "at": time.Now().Unix()})
			a.setSetting("wharf.pending", string(p))
			base := firstNonEmpty(a.publicURL(), panelBase(r))
			q := url.Values{"berth_url": {base}, "berth_fp": {a.caFP()}, "code": {code}, "cb": {base + "/wharf/paired"}}
			writeJSON(w, 200, map[string]string{"redirect": u.String() + "/pair/berth?" + q.Encode()})
		}))
		// 3. back from Wharf: trade the code for the token (and hand Wharf ours)
		mux.HandleFunc("POST /api/v1/wharf/pair/finish", a.authed("settings.manage", func(w http.ResponseWriter, r *http.Request) {
			if currentUser(r).Role != RoleSuper {
				a.fail(w, r, errs.New(403, "auth.forbidden"))
				return
			}
			var in struct{ Code string }
			readJSON(r, &in)
			var pend struct {
				URL, FP, Code string
				At            int64
			}
			json.Unmarshal([]byte(a.setting("wharf.pending")), &pend)
			if pend.Code == "" || subtle.ConstantTimeCompare([]byte(pend.Code), []byte(in.Code)) != 1 || time.Since(time.Unix(pend.At, 0)) > 15*time.Minute {
				a.fail(w, r, errs.New(400, "wharf.bad_code"))
				return
			}
			a.setSetting("wharf.pending", "")
			inbound := "berth_wrf_" + sign.RandomHex(24)
			name := firstNonEmpty(a.setting("panel.name"), "Berth")
			res, err := wharfapi.Exchange(r.Context(), pend.URL, pend.FP, wharfapi.ExchangeRequest{Code: in.Code, BerthToken: inbound, BerthName: name,
				BerthURL: firstNonEmpty(a.publicURL(), panelBase(r)), BerthFP: a.caFP()})
			if err != nil {
				a.fail(w, r, a.wharfErr(err))
				return
			}
			enc, err := a.seal([]byte(res.Token))
			if err != nil {
				a.fail(w, r, err)
				return
			}
			scopes, _ := json.Marshal(res.Scopes)
			targets, _ := json.Marshal(res.TargetIPs)
			now := time.Now().Unix()
			a.db.Exec(`INSERT OR REPLACE INTO wharf_link(id, url, name, fp, token_enc, inbound_hash, scopes, target_ips, alerts_primary, paired_at, last_seen, expires_at, state)
				VALUES(1,?,?,?,?,?,?,?,'',?,?,?,'active')`, pend.URL, res.WharfName, pend.FP, enc, hashToken(inbound), string(scopes), string(targets), now, now, res.ExpiresAt)
			a.pipe.Record(currentActor(r), "local", "wharf.pair", map[string]any{"url": pend.URL, "scopes": res.Scopes}, change.Plan{
				Summary: "plan.wharf.pair", SummaryP: map[string]any{"name": res.WharfName, "url": pend.URL}, Risk: change.RiskMedium})
			writeJSON(w, 200, map[string]any{"name": res.WharfName, "scopes": res.Scopes})
		}))
		mux.HandleFunc("PUT /api/v1/wharf/users", a.authed("users.manage", func(w http.ResponseWriter, r *http.Request) {
			var in struct {
				WharfUser string `json:"wharf_user"`
				BerthUser string `json:"berth_user"`
			}
			readJSON(r, &in)
			in.WharfUser = strings.TrimSpace(in.WharfUser)
			if in.WharfUser == "" || len(in.WharfUser) > 64 {
				a.fail(w, r, errs.New(400, "wharf.bad_user"))
				return
			}
			if in.BerthUser == "" {
				a.db.Exec(`DELETE FROM wharf_user_map WHERE wharf_user=?`, in.WharfUser)
				writeJSON(w, 200, map[string]bool{"ok": true})
				return
			}
			var uid int64
			if a.db.QueryRow(`SELECT id FROM users WHERE username=? AND disabled=0`, in.BerthUser).Scan(&uid) != nil {
				a.fail(w, r, errs.New(404, "wharf.bad_user"))
				return
			}
			a.db.Exec(`INSERT OR REPLACE INTO wharf_user_map(wharf_user, berth_user_id) VALUES(?,?)`, in.WharfUser, uid)
			writeJSON(w, 200, map[string]bool{"ok": true})
		}))
		// open Wharf without signing in again
		mux.HandleFunc("POST /api/v1/wharf/sso", a.authed("", func(w http.ResponseWriter, r *http.Request) {
			var in struct {
				ReturnTo string `json:"return_to"`
			}
			readJSON(r, &in)
			if !strings.HasPrefix(in.ReturnTo, "/") || strings.HasPrefix(in.ReturnTo, "//") {
				in.ReturnTo = "/"
			}
			c, _, err := a.wharfClient("sso")
			if err != nil {
				a.fail(w, r, err)
				return
			}
			u, err := c.Ticket(r.Context(), currentUser(r).Username, in.ReturnTo)
			if err != nil {
				a.fail(w, r, a.wharfErr(err))
				return
			}
			writeJSON(w, 200, map[string]string{"url": u})
		}))
		// Wharf → Berth one-time sign-in link lands here
		mux.HandleFunc("GET /sso/wharf", a.handleWharfSSO)

		// ---- Wharf (token + signature) ----
		mux.HandleFunc("POST /api/ext/wharf/v1/bans", a.wharfInBan)
		mux.HandleFunc("DELETE /api/ext/wharf/v1/bans/{id}", a.wharfInUnban)
		mux.HandleFunc("POST /api/ext/wharf/v1/alerts", a.wharfInAlert)
		mux.HandleFunc("POST /api/ext/wharf/v1/sso/ticket", a.wharfInTicket)
		mux.HandleFunc("GET /api/ext/wharf/v1/host", a.wharfInHost)
		mux.HandleFunc("DELETE /api/ext/wharf/v1/pair", func(w http.ResponseWriter, r *http.Request) {
			if _, ok := a.wharfAuth(w, r); !ok {
				return
			}
			a.dropWharfLink()
			a.pipe.Record(wharfActor, "local", "wharf.unpair", map[string]any{"by": "wharf"}, change.Plan{Summary: "plan.wharf.unpaired_by_wharf", Risk: change.RiskMedium})
			writeJSON(w, 200, map[string]bool{"ok": true})
		})
	})
}

func (a *App) dropWharfLink() {
	a.db.Exec(`DELETE FROM wharf_link`)
	a.db.Exec(`DELETE FROM wharf_publish`)
	a.db.Exec(`UPDATE bans SET wharf_ban='' WHERE wharf_ban != ''`)
}

// POST /bans from Wharf: ban on every host Berth manages (the router saw it attack).
func (a *App) wharfInBan(w http.ResponseWriter, r *http.Request) {
	body, ok := a.wharfAuth(w, r)
	if !ok {
		return
	}
	var in wharfapi.Ban
	json.Unmarshal(body, &in)
	addr, err := normAddr(in.CIDR)
	if err != nil {
		writeJSON(w, 400, map[string]string{"error": "地址不正确"})
		return
	}
	ip := net.ParseIP(strings.Split(addr, "/")[0])
	if ip == nil || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || a.whitelisted(ip) {
		writeJSON(w, 400, map[string]string{"error": "不能封禁内网地址或白名单里的地址"})
		return
	}
	if ones, bits := mustMask(addr); (bits == 32 && ones < 16) || (bits == 128 && ones < 32) {
		writeJSON(w, 400, map[string]string{"error": "网段太大（IPv4 至少 /16，IPv6 至少 /32）"})
		return
	}
	var exp int64
	if in.ExpiresAt != "" {
		t, err := time.Parse(time.RFC3339, in.ExpiresAt)
		if err != nil || t.Before(time.Now()) {
			writeJSON(w, 400, map[string]string{"error": "到期时间不正确"})
			return
		}
		exp = t.Unix()
	}
	ref := "in:" + sign.RandomHex(8)
	for _, h := range append([]string{"local"}, a.remoteHostNames()...) {
		a.db.Exec(`INSERT INTO bans(host, addr, reason, source, layers, created_by, created_at, expires_at, wharf_ban) VALUES(?,?,?,?,?,?,?,?,?)`,
			h, addr, truncate(firstNonEmpty(in.Reason, in.Source), 200), "wharf", `["local"]`, "Wharf", time.Now().Unix(), exp, ref)
		go a.pushBans(context.Background(), h)
	}
	a.pipe.Record(wharfActor, "local", "ban.add", map[string]any{"addr": addr, "reason": in.Reason}, change.Plan{
		Summary: "plan.wharf.ban_in", SummaryP: map[string]any{"addr": addr, "reason": in.Reason}, Risk: change.RiskLow})
	a.hub.publish(event{Type: "ban", Data: map[string]any{"host": "local"}})
	writeJSON(w, 200, map[string]string{"id": strings.TrimPrefix(ref, "in:")})
}

func mustMask(addr string) (int, int) {
	if _, n, err := net.ParseCIDR(addr); err == nil {
		return n.Mask.Size()
	}
	if strings.Contains(addr, ":") {
		return 128, 128
	}
	return 32, 32
}

func (a *App) remoteHostNames() []string {
	var out []string
	for _, h := range a.remoteHosts() {
		out = append(out, h.Name)
	}
	return out
}

func (a *App) wharfInUnban(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.wharfAuth(w, r); !ok {
		return
	}
	ref := "in:" + r.PathValue("id")
	res, _ := a.db.Exec(`UPDATE bans SET revoked_at=? WHERE wharf_ban=? AND revoked_at=0`, time.Now().Unix(), ref)
	if n, _ := res.RowsAffected(); n == 0 {
		writeJSON(w, 404, map[string]string{"error": "封禁不存在"})
		return
	}
	for _, h := range append([]string{"local"}, a.remoteHostNames()...) {
		go a.pushBans(context.Background(), h)
	}
	a.hub.publish(event{Type: "ban", Data: map[string]any{"host": "local"}})
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// POST /alerts from Wharf: when Berth is the main notifier, Wharf's alerts go out
// through Berth's channels (one per dedupe key per 10 minutes).
func (a *App) wharfInAlert(w http.ResponseWriter, r *http.Request) {
	body, ok := a.wharfAuth(w, r)
	if !ok {
		return
	}
	var in wharfapi.Alert
	json.Unmarshal(body, &in)
	if strings.TrimSpace(in.Title) == "" {
		writeJSON(w, 400, map[string]string{"error": "告警缺少标题"})
		return
	}
	l, _ := a.wharf()
	if l == nil || l.AlertsPrimary != "berth" {
		writeJSON(w, 409, map[string]string{"error": "Berth 不是主通知方"})
		return
	}
	key := firstNonEmpty(in.DedupeKey, in.Title)
	var n int
	a.db.QueryRow(`SELECT COUNT(*) FROM alert_events WHERE rule='wharf_alert' AND key LIKE ? AND started > ?`, "wharf_alert:"+key+":%", time.Now().Add(-10*time.Minute).Unix()).Scan(&n)
	if n > 0 {
		writeJSON(w, 200, map[string]any{"ok": true, "deduped": true})
		return
	}
	rule, _ := a.alertRule("wharf_alert")
	switch in.Level {
	case "critical", "warn", "info":
		rule.Severity = in.Level
	}
	a.once(rule, key, "wharf", map[string]any{"title": truncate(in.Title, 200), "body": truncate(in.Body, 1000), "link": in.Link})
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// POST /sso/ticket from Wharf: a 60-second, single-use sign-in link for the mapped user.
func (a *App) wharfInTicket(w http.ResponseWriter, r *http.Request) {
	body, ok := a.wharfAuth(w, r)
	if !ok {
		return
	}
	var in struct {
		WharfUser string `json:"wharf_user"`
		ReturnTo  string `json:"return_to"`
	}
	json.Unmarshal(body, &in)
	var uid int64
	if a.db.QueryRow(`SELECT berth_user_id FROM wharf_user_map WHERE wharf_user=?`, in.WharfUser).Scan(&uid) != nil {
		writeJSON(w, 403, map[string]string{"error": "这个 Wharf 用户没有对应的 Berth 用户"})
		return
	}
	if !strings.HasPrefix(in.ReturnTo, "/") || strings.HasPrefix(in.ReturnTo, "//") || strings.ContainsAny(in.ReturnTo, "\\\r\n") {
		in.ReturnTo = "/"
	}
	t := sign.RandomHex(24)
	a.db.Exec(`INSERT INTO wharf_sso(ticket_hash, user_id, return_to, expires_at) VALUES(?,?,?,?)`, hashToken(t), uid, in.ReturnTo, time.Now().Add(60*time.Second).Unix())
	base := firstNonEmpty(a.publicURL(), panelBase(r))
	writeJSON(w, 200, map[string]string{"url": base + "/sso/wharf?t=" + t})
}

func (a *App) handleWharfSSO(w http.ResponseWriter, r *http.Request) {
	t := r.URL.Query().Get("t")
	var uid, exp int64
	var ret string
	h := hashToken(t)
	err := a.db.QueryRow(`SELECT user_id, return_to, expires_at FROM wharf_sso WHERE ticket_hash=?`, h).Scan(&uid, &ret, &exp)
	a.db.Exec(`DELETE FROM wharf_sso WHERE ticket_hash=? OR expires_at < ?`, h, time.Now().Unix()) // single use
	if err != nil || time.Now().Unix() > exp {
		http.Redirect(w, r, "/login?sso=expired", http.StatusFound)
		return
	}
	u, err := a.userByID(uid)
	if err != nil || u.Disabled {
		http.Redirect(w, r, "/login?sso=expired", http.StatusFound)
		return
	}
	if _, err := a.createSession(w, r, u, "wharf", "full"); err != nil {
		a.fail(w, r, err)
		return
	}
	a.recordLogin(r, u.Username, u.ID, true, "wharf", "")
	http.Redirect(w, r, ret, http.StatusFound)
}

// GET /host: what Wharf's device page shows for this Berth (containers and sites).
func (a *App) wharfInHost(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.wharfAuth(w, r); !ok {
		return
	}
	type ctr struct {
		Name  string `json:"name"`
		Image string `json:"image"`
		State string `json:"state"`
	}
	cs := []ctr{}
	if list, err := a.containers(r.Context(), "local"); err == nil {
		for _, c := range list {
			cs = append(cs, ctr{c.Name, c.Image, c.State})
		}
	}
	sites := []map[string]any{}
	if ss, err := a.sites("local"); err == nil {
		for _, s := range ss {
			sites = append(sites, map[string]any{"domains": s.Domains, "enabled": s.Enabled})
		}
	}
	writeJSON(w, 200, map[string]any{"name": firstNonEmpty(a.setting("panel.name"), "Berth"), "containers": cs, "sites": sites, "vms": []any{}})
}
