// Adapted from github.com/wangsirco/wharf (internal/panel/passkey.go), Copyright (c) Wharf authors, MIT License.

package panel

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"

	"github.com/wangsirco/berth/internal/errs"
	"github.com/wangsirco/berth/internal/sign"
)

// Passkeys (WebAuthn). Credentials are discoverable (no username needed) and bound to
// the host name they were created on. Browsers refuse passkeys on bare IP addresses,
// so the panel must be opened by a name (mini.lan, a Tailscale name, ...).

type pkUser struct {
	u     *User
	creds []webauthn.Credential
}

func (p *pkUser) WebAuthnID() []byte                         { return []byte("berth-user-" + strconv.FormatInt(p.u.ID, 10)) }
func (p *pkUser) WebAuthnName() string                       { return p.u.Username }
func (p *pkUser) WebAuthnDisplayName() string                { return firstNonEmpty(p.u.DisplayName, p.u.Username) }
func (p *pkUser) WebAuthnCredentials() []webauthn.Credential { return p.creds }

func userIDFromHandle(h []byte) (int64, bool) {
	s, ok := strings.CutPrefix(string(h), "berth-user-")
	if !ok {
		return 0, false
	}
	id, err := strconv.ParseInt(s, 10, 64)
	return id, err == nil
}

var pkFlows struct {
	sync.Mutex
	m map[string]pkFlow
}

type pkFlow struct {
	data webauthn.SessionData
	uid  int64
	kind string
	rp   string
	name string
	exp  time.Time
}

const (
	pkAdd    = "add"
	pkLogin  = "login"
	pkSecond = "second"
)

func pkPut(f pkFlow) string {
	pkFlows.Lock()
	defer pkFlows.Unlock()
	if pkFlows.m == nil {
		pkFlows.m = map[string]pkFlow{}
	}
	for k, v := range pkFlows.m {
		if time.Now().After(v.exp) {
			delete(pkFlows.m, k)
		}
	}
	id := sign.RandomHex(16)
	f.exp = time.Now().Add(5 * time.Minute)
	pkFlows.m[id] = f
	return id
}

func pkTake(id string) (pkFlow, bool) {
	pkFlows.Lock()
	defer pkFlows.Unlock()
	f, ok := pkFlows.m[id]
	delete(pkFlows.m, id)
	return f, ok && time.Now().Before(f.exp)
}

// rpFor builds the relying party for the host name this request came in on.
func (a *App) rpFor(r *http.Request) (*webauthn.WebAuthn, string, error) {
	host := strings.ToLower(r.Host)
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]")
	if host == "" || net.ParseIP(host) != nil {
		return nil, "", errs.New(400, "passkey.needs_name")
	}
	wa, err := webauthn.New(&webauthn.Config{RPID: host, RPDisplayName: firstNonEmpty(a.setting("panel.name"), "Berth"), RPOrigins: []string{"https://" + r.Host}})
	return wa, host, err
}

type passkeyRow struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	RPID     string `json:"rp_id"`
	Created  int64  `json:"created_at"`
	LastUsed int64  `json:"last_used_at"`
}

func (a *App) passkeyCreds(uid int64, rp string) []webauthn.Credential {
	var out []webauthn.Credential
	rows, err := a.db.Query(`SELECT cred FROM passkeys WHERE user_id=? AND rp_id=?`, uid, rp)
	if err != nil {
		return nil
	}
	defer rows.Close()
	for rows.Next() {
		var raw string
		var c webauthn.Credential
		if rows.Scan(&raw) == nil && json.Unmarshal([]byte(raw), &c) == nil {
			out = append(out, c)
		}
	}
	return out
}

// GET /api/v1/me/passkeys
func (a *App) handlePasskeys(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	list := []passkeyRow{}
	if rows, err := a.db.Query(`SELECT id, name, rp_id, created_at, last_used_at FROM passkeys WHERE user_id=? ORDER BY id`, u.ID); err == nil {
		for rows.Next() {
			var p passkeyRow
			if rows.Scan(&p.ID, &p.Name, &p.RPID, &p.Created, &p.LastUsed) == nil {
				list = append(list, p)
			}
		}
		rows.Close()
	}
	_, here, err := a.rpFor(r)
	writeJSON(w, 200, map[string]any{"items": list, "here": here, "usable": err == nil})
}

// POST /api/v1/me/passkeys/begin {name}
func (a *App) handlePasskeyBegin(w http.ResponseWriter, r *http.Request) {
	var in struct{ Name string }
	_ = readJSON(r, &in)
	wa, rp, err := a.rpFor(r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if err := a.requireReauth(r); err != nil {
		a.fail(w, r, err)
		return
	}
	u := currentUser(r)
	pu := &pkUser{u: u, creds: a.passkeyCreds(u.ID, rp)}
	creation, data, err := wa.BeginRegistration(pu,
		webauthn.WithResidentKeyRequirement(protocol.ResidentKeyRequirementRequired),
		webauthn.WithAuthenticatorSelection(protocol.AuthenticatorSelection{ResidentKey: protocol.ResidentKeyRequirementRequired, UserVerification: protocol.VerificationRequired}),
		webauthn.WithExclusions(webauthn.Credentials(pu.creds).CredentialDescriptors()))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	name := strings.TrimSpace(in.Name)
	if len([]rune(name)) > 40 {
		name = string([]rune(name)[:40])
	}
	flow := pkPut(pkFlow{data: *data, uid: u.ID, kind: pkAdd, rp: rp, name: firstNonEmpty(name, "Passkey")})
	writeJSON(w, 200, map[string]any{"flow": flow, "options": creation.Response})
}

// POST /api/v1/me/passkeys/finish?flow= (body: the browser's credential)
func (a *App) handlePasskeyFinish(w http.ResponseWriter, r *http.Request) {
	f, ok := pkTake(r.URL.Query().Get("flow"))
	u := currentUser(r)
	if !ok || f.uid != u.ID || f.kind != pkAdd {
		a.fail(w, r, errs.New(400, "passkey.timeout"))
		return
	}
	wa, rp, err := a.rpFor(r)
	if err != nil || rp != f.rp {
		a.fail(w, r, errs.New(400, "passkey.needs_name"))
		return
	}
	cred, err := wa.FinishRegistration(&pkUser{u: u, creds: a.passkeyCreds(u.ID, rp)}, f.data, r)
	if err != nil {
		a.fail(w, r, errs.New(400, "passkey.failed", "detail", pkErr(err)))
		return
	}
	raw, _ := json.Marshal(cred)
	if _, err := a.db.Exec(`INSERT INTO passkeys(user_id, rp_id, cred_id, cred, name, created_at) VALUES(?,?,?,?,?,?)`,
		u.ID, rp, base64.RawURLEncoding.EncodeToString(cred.ID), string(raw), f.name, time.Now().Unix()); err != nil {
		a.fail(w, r, err)
		return
	}
	a.recordSecurityEvent(r, "account.passkey_add", u, map[string]string{"name": f.name, "rp_id": rp})
	a.handlePasskeys(w, r)
}

// PATCH /api/v1/me/passkeys/{id} {name}
func (a *App) handlePasskeyRename(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	var in struct{ Name string }
	if err := readJSON(r, &in); err != nil || strings.TrimSpace(in.Name) == "" {
		a.fail(w, r, errs.New(400, "params.required", "name", "name"))
		return
	}
	res, _ := a.db.Exec(`UPDATE passkeys SET name=? WHERE id=? AND user_id=?`, strings.TrimSpace(in.Name), r.PathValue("id"), u.ID)
	if n, _ := res.RowsAffected(); n == 0 {
		a.fail(w, r, errs.New(404, "passkey.not_found"))
		return
	}
	a.handlePasskeys(w, r)
}

// DELETE /api/v1/me/passkeys/{id}
func (a *App) handlePasskeyDelete(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	var name string
	if a.db.QueryRow(`SELECT name FROM passkeys WHERE id=? AND user_id=?`, r.PathValue("id"), u.ID).Scan(&name) != nil {
		a.fail(w, r, errs.New(404, "passkey.not_found"))
		return
	}
	if err := a.requireReauth(r); err != nil {
		a.fail(w, r, err)
		return
	}
	last := a.usablePasskeyCount(u.ID) <= 1
	if last && (u.LoginMode == LoginPasskeyOnly || (u.LoginMode == LoginPassword2FA && !a.hasTOTP(u.ID))) {
		a.fail(w, r, errs.New(409, "passkey.last_one"))
		return
	}
	_, _ = a.db.Exec(`DELETE FROM passkeys WHERE id=? AND user_id=?`, r.PathValue("id"), u.ID)
	a.recordSecurityEvent(r, "account.passkey_remove", u, map[string]string{"name": name})
	a.handlePasskeys(w, r)
}

// POST /api/v1/auth/passkey/begin (public). With a second-step session it asks for that
// user's passkey; otherwise it is a discoverable sign-in.
func (a *App) handlePasskeyLoginBegin(w http.ResponseWriter, r *http.Request) {
	wa, rp, err := a.rpFor(r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if a.limiter.blocked("ip:" + a.clientIP(r)) {
		a.fail(w, r, errs.New(429, "auth.rate_limited"))
		return
	}
	if s, _ := a.secondStepSession(r); s != nil {
		assertion, data, err := wa.BeginLogin(&pkUser{u: s.user, creds: a.passkeyCreds(s.user.ID, rp)},
			webauthn.WithUserVerification(protocol.VerificationRequired))
		if err != nil {
			a.fail(w, r, errs.New(400, "passkey.none_here"))
			return
		}
		writeJSON(w, 200, map[string]any{"flow": pkPut(pkFlow{data: *data, uid: s.user.ID, kind: pkSecond, rp: rp}), "options": assertion.Response})
		return
	}
	assertion, data, err := wa.BeginDiscoverableLogin(webauthn.WithUserVerification(protocol.VerificationRequired))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"flow": pkPut(pkFlow{data: *data, kind: pkLogin, rp: rp}), "options": assertion.Response})
}

// POST /api/v1/auth/passkey/finish?flow=&remember=1 (public; body: the browser's assertion)
func (a *App) handlePasskeyLoginFinish(w http.ResponseWriter, r *http.Request) {
	ip := a.clientIP(r)
	if a.limiter.blocked("ip:" + ip) {
		a.fail(w, r, errs.New(429, "auth.rate_limited"))
		return
	}
	f, ok := pkTake(r.URL.Query().Get("flow"))
	if !ok {
		a.fail(w, r, errs.New(400, "passkey.timeout"))
		return
	}
	wa, rp, err := a.rpFor(r)
	if err != nil || rp != f.rp {
		a.fail(w, r, errs.New(400, "passkey.needs_name"))
		return
	}
	var who *User
	var cred *webauthn.Credential
	var second *session
	switch f.kind {
	case pkLogin:
		_, cred, err = wa.FinishPasskeyLogin(func(rawID, handle []byte) (webauthn.User, error) {
			uid, ok := userIDFromHandle(handle)
			if !ok {
				return nil, errors.New("unknown user")
			}
			u, err := a.getUser(uid)
			if err != nil || u.Disabled {
				return nil, errors.New("account missing or disabled")
			}
			who = u
			return &pkUser{u: u, creds: a.passkeyCreds(uid, rp)}, nil
		}, f.data, r)
	case pkSecond:
		second, err = a.secondStepSession(r)
		if err == nil && second.user.ID == f.uid {
			who = second.user
			cred, err = wa.FinishLogin(&pkUser{u: who, creds: a.passkeyCreds(who.ID, rp)}, f.data, r)
		} else if err == nil {
			err = errors.New("session changed")
		}
	default:
		err = errors.New("flow")
	}
	if err != nil || who == nil || cred == nil {
		a.limiter.record("ip:" + ip)
		name := ""
		if who != nil {
			name = who.Username
		}
		a.recordLogin(r, name, 0, false, "passkey", "failed")
		a.fail(w, r, errs.New(401, "passkey.failed", "detail", pkErr(err)))
		return
	}
	if cred.Authenticator.CloneWarning {
		a.limiter.record("ip:" + ip)
		a.fail(w, r, errs.New(401, "passkey.clone_warning"))
		return
	}
	raw, _ := json.Marshal(cred)
	_, _ = a.db.Exec(`UPDATE passkeys SET cred=?, last_used_at=? WHERE user_id=? AND cred_id=?`, string(raw), time.Now().Unix(), who.ID, base64.RawURLEncoding.EncodeToString(cred.ID))
	remember := r.URL.Query().Get("remember") == "1"
	if second != nil {
		a.finishSecondStep(w, r, second, "password+passkey", remember)
		return
	}
	// A user-verified passkey is two factors by itself, so it signs in fully in every mode.
	csrf, err := a.createSession(w, r, who, "passkey", "full")
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.limiter.reset("ip:" + ip)
	a.recordLogin(r, who.Username, who.ID, true, "passkey", "")
	writeJSON(w, 200, map[string]any{"stage": "full", "csrf": csrf, "user": who})
}

func pkErr(err error) string {
	if err == nil {
		return "unknown"
	}
	var pe *protocol.Error
	if errors.As(err, &pe) && pe.DevInfo != "" {
		return pe.Details + " (" + pe.DevInfo + ")"
	}
	return err.Error()
}
