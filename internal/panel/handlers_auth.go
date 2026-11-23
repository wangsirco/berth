package panel

import (
	"net/http"
	"time"

	"github.com/wangsirco/berth/internal/errs"
	"github.com/wangsirco/berth/internal/version"
)

type meView struct {
	*User
	CSRF              string   `json:"csrf"`
	Stage             string   `json:"stage"`
	Onboarded         bool     `json:"onboarded"`
	HasTOTP           bool     `json:"has_totp"`
	Passkeys          int      `json:"passkeys"`
	Needs2FA          bool     `json:"needs_2fa,omitempty"`          // public access is on and this account has no second factor (usable on this name)
	PasskeysElsewhere []string `json:"passkeys_elsewhere,omitempty"` // ...though it has Passkeys on these names
	Here              string   `json:"here,omitempty"`
}

func (a *App) handleLogin(w http.ResponseWriter, r *http.Request) {
	var in struct{ Username, Password string }
	if err := readJSON(r, &in); err != nil {
		a.fail(w, r, err)
		return
	}
	ip := a.clientIP(r)
	keys := []string{"ip:" + ip, "user:" + in.Username}
	if a.limiter.blocked(keys...) {
		a.recordLogin(r, in.Username, 0, false, "password", "rate_limited")
		a.fail(w, r, errs.New(429, "auth.rate_limited"))
		return
	}
	u, err := a.checkPassword(in.Username, in.Password)
	if err != nil {
		a.limiter.record(keys...)
		a.recordLogin(r, in.Username, 0, false, "password", "bad_credentials")
		a.fail(w, r, err)
		return
	}
	stage, err := a.passwordLoginStage(r, u)
	if err != nil {
		a.recordLogin(r, u.Username, u.ID, false, "password", errs.From(err).Code)
		a.fail(w, r, err)
		return
	}
	a.limiter.reset("user:" + in.Username)
	csrf, err := a.createSession(w, r, u, "password", stage)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.recordLogin(r, u.Username, u.ID, stage == "full", "password", stage)
	out := map[string]any{"stage": stage, "csrf": csrf}
	if stage == "full" {
		out["user"] = u
	} else {
		out["methods"] = a.secondStepMethods(r, u)
	}
	writeJSON(w, 200, out)
}

func (a *App) handleLogout(w http.ResponseWriter, r *http.Request) {
	a.destroySession(w, r)
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (a *App) handleMe(w http.ResponseWriter, r *http.Request) {
	s := currentSession(r)
	u := currentUser(r)
	me := meView{User: u, Stage: "full", Onboarded: a.setting(onboardKey(u.ID)) != "", HasTOTP: a.hasTOTP(u.ID), Passkeys: a.usablePasskeyCount(u.ID)}
	me.Needs2FA = a.exposed() && len(a.secondStepMethods(r, u)) == 0
	if me.Needs2FA { // its Passkeys are on other names: add one on this one
		for _, n := range a.passkeyNames(u.ID) {
			if n != requestName(r) {
				me.PasskeysElsewhere = append(me.PasskeysElsewhere, n)
			}
		}
		me.Here = requestName(r)
	}
	if s != nil {
		me.CSRF = s.csrf
	}
	writeJSON(w, 200, me)
}

func (a *App) handleMeta(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{
		"version": version.Version, "name": a.setting("panel.name"), "needs_setup": a.userCount() == 0,
		"time": time.Now().Unix(), "locales": Locales,
	})
}
