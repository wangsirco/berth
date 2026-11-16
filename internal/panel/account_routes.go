package panel

import "net/http"

func init() {
	routeHooks = append(routeHooks, func(a *App, mux *http.ServeMux) {
		mux.HandleFunc("POST /api/v1/auth/totp", a.handleSecondStepTOTP)
		mux.HandleFunc("POST /api/v1/auth/passkey/begin", a.handlePasskeyLoginBegin)
		mux.HandleFunc("POST /api/v1/auth/passkey/finish", a.handlePasskeyLoginFinish)
		mux.HandleFunc("POST /api/v1/me/reauth", a.authed("", a.handleReauth))
		mux.HandleFunc("POST /api/v1/me/totp/begin", a.authed("", a.handleTOTPBegin))
		mux.HandleFunc("POST /api/v1/me/totp/confirm", a.authed("", a.handleTOTPConfirm))
		mux.HandleFunc("DELETE /api/v1/me/totp", a.authed("", a.handleTOTPDelete))
		mux.HandleFunc("GET /api/v1/me/passkeys", a.authed("", a.handlePasskeys))
		mux.HandleFunc("POST /api/v1/me/passkeys/begin", a.authed("", a.handlePasskeyBegin))
		mux.HandleFunc("POST /api/v1/me/passkeys/finish", a.authed("", a.handlePasskeyFinish))
		mux.HandleFunc("PATCH /api/v1/me/passkeys/{id}", a.authed("", a.handlePasskeyRename))
		mux.HandleFunc("DELETE /api/v1/me/passkeys/{id}", a.authed("", a.handlePasskeyDelete))
		mux.HandleFunc("PUT /api/v1/me/login-mode", a.authed("", a.handleLoginMode))
		mux.HandleFunc("GET /api/v1/me/devices", a.authed("", a.handleDevices))
		mux.HandleFunc("DELETE /api/v1/me/devices/{id}", a.authed("", a.handleDeviceDelete))
	})
}
