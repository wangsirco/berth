package panel

import (
	"testing"
	"time"
)

func TestPassword2FARequiresSecondStep(t *testing.T) {
	c := newTC(t)
	u := c.mkUser("iwsheng", "correct horse 9", "super")
	secret := []byte("12345678901234567890")
	c.app.setTOTPForTest(u.ID, secret)
	c.app.setLoginMode(u.ID, "password_2fa")
	_, out := c.do("POST", "/api/v1/auth/login", map[string]string{"username": "iwsheng", "password": "correct horse 9"})
	if out["stage"] != "second_step" {
		t.Fatalf("%v", out)
	}
	if r, _ := c.do("GET", "/api/v1/me", nil); r.StatusCode != 401 {
		t.Fatal("half-logged-in session must not access /me")
	}
	r, out := c.do("POST", "/api/v1/auth/totp", map[string]any{"code": totpCode(secret, time.Now()), "remember": true})
	if r.StatusCode != 200 || out["stage"] != "full" {
		t.Fatalf("%d %v", r.StatusCode, out)
	}
	if r, _ := c.do("GET", "/api/v1/me", nil); r.StatusCode != 200 {
		t.Fatal("full session")
	}
}

func TestPasskeyOnlyRejectsPassword(t *testing.T) {
	c := newTC(t)
	u := c.mkUser("iwsheng", "correct horse 9", "super")
	c.app.setLoginMode(u.ID, "passkey_only")
	r, out := c.do("POST", "/api/v1/auth/login", map[string]string{"username": "iwsheng", "password": "correct horse 9"})
	if r.StatusCode != 403 || out["error"].(map[string]any)["code"] != "auth.passkey_only" {
		t.Fatalf("%d %v", r.StatusCode, out)
	}
}

func TestEnableTOTPOverAPIAndRecovery(t *testing.T) {
	c := newTC(t)
	c.mkUser("iwsheng", "correct horse 9", "super")
	c.login("iwsheng", "correct horse 9")
	_, begin := c.do("POST", "/api/v1/me/totp/begin", nil)
	secret, err := b32.DecodeString(begin["secret"].(string))
	if err != nil || len(secret) != 20 {
		t.Fatalf("secret: %v %v", begin, err)
	}
	if r, _ := c.do("POST", "/api/v1/me/totp/confirm", map[string]string{"code": "000000"}); r.StatusCode != 400 {
		t.Fatal("wrong code accepted")
	}
	r, conf := c.do("POST", "/api/v1/me/totp/confirm", map[string]string{"code": totpCode(secret, time.Now())})
	if r.StatusCode != 200 || len(conf["recovery"].([]any)) != 10 {
		t.Fatalf("%d %v", r.StatusCode, conf)
	}
	rec := conf["recovery"].([]any)[0].(string)
	if r, _ := c.do("PUT", "/api/v1/me/login-mode", map[string]string{"mode": "password_2fa"}); r.StatusCode != 200 {
		t.Fatal("switch mode")
	}
	c.do("POST", "/api/v1/auth/logout", nil)
	c.do("POST", "/api/v1/auth/login", map[string]string{"username": "iwsheng", "password": "correct horse 9"})
	if r, _ := c.do("POST", "/api/v1/auth/totp", map[string]any{"recovery": rec}); r.StatusCode != 200 {
		t.Fatal("recovery code rejected")
	}
	c.do("POST", "/api/v1/auth/logout", nil)
	c.do("POST", "/api/v1/auth/login", map[string]string{"username": "iwsheng", "password": "correct horse 9"})
	if r, _ := c.do("POST", "/api/v1/auth/totp", map[string]any{"recovery": rec}); r.StatusCode != 401 {
		t.Fatal("recovery code reused")
	}
	// security changes are audited
	var n int
	c.app.db.QueryRow(`SELECT COUNT(*) FROM changes WHERE kind IN ('totp.enable','account.login_mode')`).Scan(&n)
	if n != 2 {
		t.Fatalf("audited %d", n)
	}
}

func TestTrustedDeviceSkipsSecondStep(t *testing.T) {
	c := newTC(t)
	u := c.mkUser("iwsheng", "correct horse 9", "super")
	secret := []byte("12345678901234567890")
	c.app.setTOTPForTest(u.ID, secret)
	c.app.setLoginMode(u.ID, "password_2fa")
	c.do("POST", "/api/v1/auth/login", map[string]string{"username": "iwsheng", "password": "correct horse 9"})
	c.do("POST", "/api/v1/auth/totp", map[string]any{"code": totpCode(secret, time.Now()), "remember": true})
	c.do("POST", "/api/v1/auth/logout", nil)
	_, out := c.do("POST", "/api/v1/auth/login", map[string]string{"username": "iwsheng", "password": "correct horse 9"})
	if out["stage"] != "full" {
		t.Fatalf("trusted device should skip: %v", out)
	}
}

func TestReauthExpires(t *testing.T) {
	c := newTC(t)
	c.mkUser("iwsheng", "correct horse 9", "super")
	c.login("iwsheng", "correct horse 9")
	c.app.db.Exec(`UPDATE sessions SET created_at=created_at-3600`)
	r, out := c.do("POST", "/api/v1/me/totp/begin", nil)
	if r.StatusCode != 403 || out["error"].(map[string]any)["code"] != "auth.reauth_required" {
		t.Fatalf("%d %v", r.StatusCode, out)
	}
	c.do("POST", "/api/v1/me/reauth", map[string]string{"password": "correct horse 9"})
	if r, _ := c.do("POST", "/api/v1/me/totp/begin", nil); r.StatusCode != 200 {
		t.Fatalf("after reauth %d", r.StatusCode)
	}
}

func TestPasskeyNeedsHostName(t *testing.T) {
	c := newTC(t) // httptest serves on 127.0.0.1
	r, out := c.do("POST", "/api/v1/auth/passkey/begin", nil)
	if r.StatusCode != 400 || out["error"].(map[string]any)["code"] != "passkey.needs_name" {
		t.Fatalf("%d %v", r.StatusCode, out)
	}
}
