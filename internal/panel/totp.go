package panel

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/wangsirco/berth/internal/errs"
)

const totpStep = 30

// totpCode is RFC 6238 (SHA-1, 30 s, 6 digits).
func totpCode(secret []byte, t time.Time) string { return hotp(secret, uint64(t.Unix()/totpStep)) }

func hotp(secret []byte, counter uint64) string {
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], counter)
	m := hmac.New(sha1.New, secret)
	m.Write(msg[:])
	sum := m.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	v := binary.BigEndian.Uint32(sum[off:off+4]) & 0x7fffffff
	return fmt.Sprintf("%06d", v%1_000_000)
}

// totpVerify accepts the previous, current and next step, and only steps after lastStep
// (so a code cannot be replayed). Returns the matched step.
func totpVerify(secret []byte, code string, now time.Time, lastStep int64) (int64, bool) {
	code = strings.TrimSpace(code)
	if len(code) != 6 {
		return 0, false
	}
	cur := now.Unix() / totpStep
	for _, s := range []int64{cur - 1, cur, cur + 1} {
		if s > lastStep && subtle.ConstantTimeCompare([]byte(hotp(secret, uint64(s))), []byte(code)) == 1 {
			return s, true
		}
	}
	return 0, false
}

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

func (a *App) totpSecret(uid int64) (secret []byte, confirmed bool, lastStep int64, err error) {
	var enc []byte
	if err = a.db.QueryRow(`SELECT secret_enc, confirmed, last_step FROM totp WHERE user_id=?`, uid).Scan(&enc, &confirmed, &lastStep); err != nil {
		return nil, false, 0, err
	}
	secret, err = a.open(enc)
	return
}

func (a *App) storeTOTP(uid int64, secret []byte, confirmed bool) error {
	enc, err := a.seal(secret)
	if err != nil {
		return err
	}
	_, err = a.db.Exec(`INSERT INTO totp(user_id, secret_enc, confirmed) VALUES(?,?,?)
		ON CONFLICT(user_id) DO UPDATE SET secret_enc=excluded.secret_enc, confirmed=excluded.confirmed, last_step=0, recovery='[]'`, uid, enc, confirmed)
	return err
}

func (a *App) setTOTPForTest(uid int64, secret []byte) {
	if err := a.storeTOTP(uid, secret, true); err != nil {
		panic(err)
	}
}

func (a *App) hasTOTP(uid int64) bool {
	var ok bool
	a.db.QueryRow(`SELECT confirmed FROM totp WHERE user_id=?`, uid).Scan(&ok)
	return ok
}

// checkTOTP verifies a code for a confirmed TOTP and records the step.
func (a *App) checkTOTP(uid int64, code string) bool {
	secret, confirmed, last, err := a.totpSecret(uid)
	if err != nil || !confirmed {
		return false
	}
	step, ok := totpVerify(secret, code, time.Now(), last)
	if !ok {
		return false
	}
	res, err := a.db.Exec(`UPDATE totp SET last_step=? WHERE user_id=? AND last_step<?`, step, uid, step)
	n, _ := res.RowsAffected()
	return err == nil && n == 1
}

// useRecoveryCode consumes one recovery code.
func (a *App) useRecoveryCode(uid int64, code string) bool {
	var raw string
	if a.db.QueryRow(`SELECT recovery FROM totp WHERE user_id=? AND confirmed=1`, uid).Scan(&raw) != nil {
		return false
	}
	var hashes []string
	_ = json.Unmarshal([]byte(raw), &hashes)
	code = strings.ToLower(strings.TrimSpace(code))
	for i, h := range hashes {
		if checkPasswordHash(h, code) {
			hashes = append(hashes[:i], hashes[i+1:]...)
			b, _ := json.Marshal(hashes)
			_, err := a.db.Exec(`UPDATE totp SET recovery=? WHERE user_id=?`, string(b), uid)
			return err == nil
		}
	}
	return false
}

func newRecoveryCodes() (plain []string, hashes []string, err error) {
	const alphabet = "abcdefghjkmnpqrstuvwxyz23456789"
	for i := 0; i < 10; i++ {
		b := make([]byte, 8)
		if _, err := rand.Read(b); err != nil {
			return nil, nil, err
		}
		for j := range b {
			b[j] = alphabet[int(b[j])%len(alphabet)]
		}
		code := string(b[:4]) + "-" + string(b[4:])
		h, err := hashPassword(code)
		if err != nil {
			return nil, nil, err
		}
		plain, hashes = append(plain, code), append(hashes, h)
	}
	return plain, hashes, nil
}

// POST /api/v1/me/totp/begin
func (a *App) handleTOTPBegin(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	if err := a.requireReauth(r); err != nil {
		a.fail(w, r, err)
		return
	}
	if a.hasTOTP(u.ID) {
		a.fail(w, r, errs.New(409, "totp.already_on"))
		return
	}
	secret := make([]byte, 20)
	if _, err := rand.Read(secret); err != nil {
		a.fail(w, r, err)
		return
	}
	if err := a.storeTOTP(u.ID, secret, false); err != nil {
		a.fail(w, r, err)
		return
	}
	s := b32.EncodeToString(secret)
	issuer := firstNonEmpty(a.setting("panel.name"), "Berth")
	label := url.PathEscape(issuer + ":" + u.Username)
	uri := fmt.Sprintf("otpauth://totp/%s?secret=%s&issuer=%s&digits=6&period=30", label, s, url.QueryEscape(issuer))
	writeJSON(w, 200, map[string]string{"secret": s, "otpauth": uri})
}

// POST /api/v1/me/totp/confirm {code} -> recovery codes (shown once)
func (a *App) handleTOTPConfirm(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	var in struct{ Code string }
	if err := readJSON(r, &in); err != nil {
		a.fail(w, r, err)
		return
	}
	secret, confirmed, _, err := a.totpSecret(u.ID)
	if err != nil || confirmed {
		a.fail(w, r, errs.New(409, "totp.not_started"))
		return
	}
	step, ok := totpVerify(secret, in.Code, time.Now(), 0)
	if !ok {
		a.fail(w, r, errs.New(400, "auth.bad_code"))
		return
	}
	plain, hashes, err := newRecoveryCodes()
	if err != nil {
		a.fail(w, r, err)
		return
	}
	hj, _ := json.Marshal(hashes)
	if _, err := a.db.Exec(`UPDATE totp SET confirmed=1, last_step=?, recovery=? WHERE user_id=?`, step, string(hj), u.ID); err != nil {
		a.fail(w, r, err)
		return
	}
	a.recordSecurityEvent(r, "totp.enable", u, nil)
	writeJSON(w, 200, map[string]any{"recovery": plain})
}

// DELETE /api/v1/me/totp
func (a *App) handleTOTPDelete(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	if err := a.requireReauth(r); err != nil {
		a.fail(w, r, err)
		return
	}
	if u.LoginMode == "password_2fa" && a.usablePasskeyCount(u.ID) == 0 {
		a.fail(w, r, errs.New(409, "auth.no_second_factor"))
		return
	}
	_, _ = a.db.Exec(`DELETE FROM totp WHERE user_id=?`, u.ID)
	a.recordSecurityEvent(r, "totp.disable", u, nil)
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}
