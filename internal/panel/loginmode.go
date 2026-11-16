package panel

import (
	"net/http"
	"strconv"
	"time"

	"github.com/wangsirco/berth/internal/change"
	"github.com/wangsirco/berth/internal/errs"
	"github.com/wangsirco/berth/internal/sign"
)

const (
	LoginPassword    = "password"     // password or passkey
	LoginPassword2FA = "password_2fa" // password, then TOTP or passkey
	LoginPasskeyOnly = "passkey_only"

	trustedCookie = "berth_td"
	trustedTTL    = 30 * 24 * time.Hour
	reauthTTL     = 10 * time.Minute
)

// effectiveLoginMode: with public access on, plain password sign-in is not allowed (方案 §7.4).
func (a *App) effectiveLoginMode(u *User) string {
	if u.LoginMode == LoginPassword && a.cfg.Public {
		return LoginPassword2FA
	}
	return u.LoginMode
}

func (a *App) passwordLoginStage(r *http.Request, u *User) (string, error) {
	switch a.effectiveLoginMode(u) {
	case LoginPasskeyOnly:
		return "", errs.New(403, "auth.passkey_only")
	case LoginPassword2FA:
		if len(a.secondStepMethods(u)) == 0 {
			// misconfigured account (factor removed): refuse rather than silently skipping 2FA
			return "", errs.New(403, "auth.no_second_factor")
		}
		if a.trustedDevice(r, u.ID) {
			return "full", nil
		}
		return "second_step", nil
	}
	return "full", nil
}

func (a *App) secondStepMethods(u *User) []string {
	var m []string
	if a.hasTOTP(u.ID) {
		m = append(m, "totp", "recovery")
	}
	if a.usablePasskeyCount(u.ID) > 0 {
		m = append(m, "passkey")
	}
	return m
}

func (a *App) usablePasskeyCount(uid int64) int {
	var n int
	a.db.QueryRow(`SELECT COUNT(*) FROM passkeys WHERE user_id=?`, uid).Scan(&n)
	return n
}

func (a *App) setLoginMode(uid int64, mode string) {
	_, _ = a.db.Exec(`UPDATE users SET login_mode=? WHERE id=?`, mode, uid)
}

// ---- trusted devices ----

func (a *App) trustedDevice(r *http.Request, uid int64) bool {
	c, err := r.Cookie(trustedCookie)
	if err != nil || c.Value == "" {
		return false
	}
	var exp int64
	if a.db.QueryRow(`SELECT expires_at FROM trusted_devices WHERE id=? AND user_id=?`, tokenHash(c.Value), uid).Scan(&exp) != nil {
		return false
	}
	return time.Now().Unix() < exp
}

func (a *App) rememberDevice(w http.ResponseWriter, r *http.Request, u *User) {
	tok := sign.RandomHex(32)
	now := time.Now()
	_, _ = a.db.Exec(`INSERT INTO trusted_devices(id, user_id, label, ip, created_at, expires_at) VALUES(?,?,?,?,?,?)`,
		tokenHash(tok), u.ID, truncate(r.UserAgent(), 200), a.clientIP(r), now.Unix(), now.Add(trustedTTL).Unix())
	setCookie(w, trustedCookie, tok, int(trustedTTL.Seconds()))
}

// ---- second step ----

// secondStepSession returns the caller's half-signed-in session.
func (a *App) secondStepSession(r *http.Request) (*session, error) {
	s := a.loadSession(r)
	if s == nil || s.stage != "second_step" {
		return nil, errs.New(401, "auth.second_step_required")
	}
	return s, nil
}

// POST /api/v1/auth/totp {code | recovery, remember}
func (a *App) handleSecondStepTOTP(w http.ResponseWriter, r *http.Request) {
	s, err := a.secondStepSession(r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	var in struct {
		Code     string `json:"code"`
		Recovery string `json:"recovery"`
		Remember bool   `json:"remember"`
	}
	if err := readJSON(r, &in); err != nil {
		a.fail(w, r, err)
		return
	}
	keys := []string{"ip:" + a.clientIP(r), "user:" + s.user.Username}
	if a.limiter.blocked(keys...) {
		a.fail(w, r, errs.New(429, "auth.rate_limited"))
		return
	}
	method := "password+totp"
	ok := in.Code != "" && a.checkTOTP(s.user.ID, in.Code)
	if !ok && in.Recovery != "" {
		ok, method = a.useRecoveryCode(s.user.ID, in.Recovery), "password+recovery"
	}
	if !ok {
		a.limiter.record(keys...)
		a.recordLogin(r, s.user.Username, s.user.ID, false, method, "bad_code")
		a.fail(w, r, errs.New(401, "auth.bad_code"))
		return
	}
	a.finishSecondStep(w, r, s, method, in.Remember)
}

func (a *App) finishSecondStep(w http.ResponseWriter, r *http.Request, s *session, method string, remember bool) {
	if err := a.promoteSession(s, method); err != nil {
		a.fail(w, r, err)
		return
	}
	if remember {
		a.rememberDevice(w, r, s.user)
	}
	a.limiter.reset("user:" + s.user.Username)
	a.recordLogin(r, s.user.Username, s.user.ID, true, method, "")
	writeJSON(w, 200, map[string]any{"stage": "full", "csrf": s.csrf, "user": s.user})
}

// ---- re-authentication for sensitive account changes ----

func (a *App) requireReauth(r *http.Request) error {
	s := currentSession(r)
	if s == nil { // tokens cannot change sign-in factors
		return errs.New(403, "auth.session_required")
	}
	var created, reauth int64
	a.db.QueryRow(`SELECT created_at, reauth_at FROM sessions WHERE token_hash=?`, s.hash).Scan(&created, &reauth)
	last := max(created, reauth)
	if time.Since(time.Unix(last, 0)) > reauthTTL {
		return errs.New(403, "auth.reauth_required")
	}
	return nil
}

// POST /api/v1/me/reauth {password | code}
func (a *App) handleReauth(w http.ResponseWriter, r *http.Request) {
	s, u := currentSession(r), currentUser(r)
	var in struct{ Password, Code string }
	if err := readJSON(r, &in); err != nil {
		a.fail(w, r, err)
		return
	}
	keys := []string{"ip:" + a.clientIP(r), "user:" + u.Username}
	if a.limiter.blocked(keys...) {
		a.fail(w, r, errs.New(429, "auth.rate_limited"))
		return
	}
	ok := false
	if in.Password != "" {
		_, err := a.checkPassword(u.Username, in.Password)
		ok = err == nil
	} else if in.Code != "" {
		ok = a.checkTOTP(u.ID, in.Code)
	}
	if !ok {
		a.limiter.record(keys...)
		a.fail(w, r, errs.New(401, "auth.bad_credentials"))
		return
	}
	a.markReauth(s)
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (a *App) markReauth(s *session) {
	_, _ = a.db.Exec(`UPDATE sessions SET reauth_at=? WHERE token_hash=?`, time.Now().Unix(), s.hash)
}

// PUT /api/v1/me/login-mode {mode}
func (a *App) handleLoginMode(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	var in struct{ Mode string }
	if err := readJSON(r, &in); err != nil {
		a.fail(w, r, err)
		return
	}
	if err := a.requireReauth(r); err != nil {
		a.fail(w, r, err)
		return
	}
	switch in.Mode {
	case LoginPassword:
	case LoginPassword2FA:
		if !a.hasTOTP(u.ID) && a.usablePasskeyCount(u.ID) == 0 {
			a.fail(w, r, errs.New(409, "auth.no_second_factor"))
			return
		}
	case LoginPasskeyOnly:
		if a.usablePasskeyCount(u.ID) == 0 {
			a.fail(w, r, errs.New(409, "auth.no_passkey"))
			return
		}
	default:
		a.fail(w, r, errs.New(400, "params.enum", "name", "mode", "allowed", "password, password_2fa, passkey_only"))
		return
	}
	before := u.LoginMode
	a.setLoginMode(u.ID, in.Mode)
	a.recordSecurityEvent(r, "account.login_mode", u, map[string]string{"from": before, "to": in.Mode})
	writeJSON(w, 200, map[string]string{"mode": in.Mode})
}

// GET /api/v1/me/devices, DELETE /api/v1/me/devices/{id}
func (a *App) handleDevices(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	type dev struct {
		ID        string `json:"id"`
		Label     string `json:"label"`
		IP        string `json:"ip"`
		CreatedAt int64  `json:"created_at"`
		ExpiresAt int64  `json:"expires_at"`
	}
	out := []dev{}
	rows, err := a.db.Query(`SELECT id, label, ip, created_at, expires_at FROM trusted_devices WHERE user_id=? AND expires_at>? ORDER BY created_at DESC`, u.ID, time.Now().Unix())
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var d dev
			if rows.Scan(&d.ID, &d.Label, &d.IP, &d.CreatedAt, &d.ExpiresAt) == nil {
				d.ID = d.ID[:16] // the stored id is a hash; a prefix is enough to address it
				out = append(out, d)
			}
		}
	}
	writeJSON(w, 200, map[string]any{"items": out})
}

func (a *App) handleDeviceDelete(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	id := r.PathValue("id")
	if len(id) != 16 {
		a.fail(w, r, errs.New(404, "device.not_found"))
		return
	}
	res, _ := a.db.Exec(`DELETE FROM trusted_devices WHERE user_id=? AND substr(id,1,16)=?`, u.ID, id)
	if n, _ := res.RowsAffected(); n == 0 {
		a.fail(w, r, errs.New(404, "device.not_found"))
		return
	}
	a.recordSecurityEvent(r, "account.device_revoke", u, map[string]string{"device": id})
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// recordSecurityEvent puts an account security change in the audit log.
func (a *App) recordSecurityEvent(r *http.Request, kind string, u *User, params any) {
	if params == nil {
		params = map[string]any{}
	}
	_, _ = a.pipe.Record(currentActorOr(r, a, u), "local", kind, params, change.Plan{
		Summary: "plan." + kind, SummaryP: map[string]any{"username": u.Username}, Risk: change.RiskMedium,
		Affected: []change.Object{{Type: "user", ID: strconv.FormatInt(u.ID, 10), Name: u.Username}},
	})
}

func currentActorOr(r *http.Request, a *App, u *User) change.Actor {
	if ac := currentActor(r); ac.Type != "" {
		return ac
	}
	return a.userActor(r, u)
}
