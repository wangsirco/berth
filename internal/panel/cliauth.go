package panel

import (
	"crypto/rand"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/wangsirco/berth/internal/errs"
	"github.com/wangsirco/berth/internal/sign"
)

// `berth login` device flow: the CLI asks for a code, a signed-in person approves it in the
// panel, the CLI polls with its secret device code and receives a berth_cli_ token once.

const cliAuthTTL = 10 * time.Minute

type cliPending struct {
	userCode string
	device   string
	ip       string
	created  time.Time
	token    string // set when approved, handed out once
	denied   bool
}

var cliFlows struct {
	sync.Mutex
	m map[string]*cliPending // device code -> flow
}

func newUserCode() string {
	const alphabet = "BCDFGHJKLMNPQRSTVWXZ23456789"
	b := []byte(randomFrom(alphabet, 8))
	return string(b[:4]) + "-" + string(b[4:])
}

func randomFrom(alphabet string, n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	for i := range b {
		b[i] = alphabet[int(b[i])%len(alphabet)]
	}
	return string(b)
}

func cliGC() {
	for k, f := range cliFlows.m {
		if time.Since(f.created) > cliAuthTTL {
			delete(cliFlows.m, k)
		}
	}
}

// POST /api/v1/cli/authorize {device} (public)
func (a *App) handleCLIAuthorize(w http.ResponseWriter, r *http.Request) {
	var in struct{ Device string }
	_ = readJSON(r, &in)
	ip := a.clientIP(r)
	if a.limiter.blocked("cli:" + ip) {
		a.fail(w, r, errs.New(429, "auth.rate_limited"))
		return
	}
	a.limiter.record("cli:" + ip) // counts requests, not just failures: 10 per 15 min
	device := sign.RandomHex(24)
	f := &cliPending{userCode: newUserCode(), device: truncate(strings.TrimSpace(in.Device), 80), ip: ip, created: time.Now()}
	cliFlows.Lock()
	if cliFlows.m == nil {
		cliFlows.m = map[string]*cliPending{}
	}
	cliGC()
	cliFlows.m[device] = f
	cliFlows.Unlock()
	writeJSON(w, 200, map[string]any{"device_code": device, "user_code": f.userCode, "verify_path": "/cli-auth?code=" + f.userCode, "expires_in": int(cliAuthTTL.Seconds()), "interval": 2})
}

func findByUserCode(code string) *cliPending {
	for _, f := range cliFlows.m {
		if f.userCode == strings.ToUpper(strings.TrimSpace(code)) && time.Since(f.created) < cliAuthTTL {
			return f
		}
	}
	return nil
}

// GET /api/v1/cli/pending?code= (session) — what the approval page shows
func (a *App) handleCLIPending(w http.ResponseWriter, r *http.Request) {
	cliFlows.Lock()
	f := findByUserCode(r.URL.Query().Get("code"))
	cliFlows.Unlock()
	if f == nil {
		a.fail(w, r, errs.New(404, "cli.code_invalid"))
		return
	}
	writeJSON(w, 200, map[string]any{"device": f.device, "ip": f.ip, "created": f.created.Unix()})
}

// POST /api/v1/cli/approve {code, allow} (session only)
func (a *App) handleCLIApprove(w http.ResponseWriter, r *http.Request) {
	if err := requireSession(r); err != nil {
		a.fail(w, r, err)
		return
	}
	var in struct {
		Code  string `json:"code"`
		Allow bool   `json:"allow"`
	}
	if err := readJSON(r, &in); err != nil {
		a.fail(w, r, err)
		return
	}
	cliFlows.Lock()
	defer cliFlows.Unlock()
	f := findByUserCode(in.Code)
	if f == nil || f.token != "" || f.denied {
		a.fail(w, r, errs.New(404, "cli.code_invalid"))
		return
	}
	if !in.Allow {
		f.denied = true
		writeJSON(w, 200, map[string]bool{"ok": true})
		return
	}
	u := currentUser(r)
	// the CLI acts as the person: every scope, still capped by their own permissions
	scopes := []string{}
	for _, res := range TokenResources {
		scopes = append(scopes, res+":read", res+":write")
	}
	tok, id, err := a.createToken(u, tokenSpec{Kind: "cli", Name: "CLI " + firstNonEmpty(f.device, "berth"), Scopes: scopes,
		ExpiresAt: time.Now().Add(90 * 24 * time.Hour).Unix(), RatePerMin: 600})
	if err != nil {
		a.fail(w, r, err)
		return
	}
	f.token = tok
	a.recordTokenEvent(r, "token.create", id, "CLI "+f.device, tok[:14])
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// POST /api/v1/cli/token {device_code} (public) — 202 while waiting.
func (a *App) handleCLIToken(w http.ResponseWriter, r *http.Request) {
	var in struct {
		DeviceCode string `json:"device_code"`
	}
	_ = readJSON(r, &in)
	cliFlows.Lock()
	defer cliFlows.Unlock()
	f, ok := cliFlows.m[in.DeviceCode]
	switch {
	case !ok || time.Since(f.created) > cliAuthTTL:
		a.fail(w, r, errs.New(410, "cli.code_invalid"))
	case f.denied:
		delete(cliFlows.m, in.DeviceCode)
		a.fail(w, r, errs.New(403, "cli.denied"))
	case f.token == "":
		writeJSON(w, 202, map[string]string{"status": "pending"})
	default:
		delete(cliFlows.m, in.DeviceCode)
		writeJSON(w, 200, map[string]string{"token": f.token})
	}
}

func init() {
	routeHooks = append(routeHooks, func(a *App, mux *http.ServeMux) {
		mux.HandleFunc("POST /api/v1/cli/authorize", a.handleCLIAuthorize)
		mux.HandleFunc("POST /api/v1/cli/token", a.handleCLIToken)
		mux.HandleFunc("GET /api/v1/cli/pending", a.authed("", a.handleCLIPending))
		mux.HandleFunc("POST /api/v1/cli/approve", a.authed("", a.handleCLIApprove))
	})
}
