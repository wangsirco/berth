package panel

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/wangsirco/berth/internal/errs"
	"github.com/wangsirco/berth/internal/sign"
)

const setupTTL = 10 * time.Minute

// issueSetupCode creates a one-time code for creating the first super admin.
func (a *App) issueSetupCode() string {
	code := sign.RandomHex(16)
	_, _ = a.db.Exec(`DELETE FROM setup`)
	_, _ = a.db.Exec(`INSERT INTO setup(code_hash, expires_at) VALUES(?,?)`, tokenHash(code), time.Now().Add(setupTTL).Unix())
	return code
}

// AnnounceSetup prints and stores the setup link when nobody can sign in yet.
func (a *App) AnnounceSetup(baseURLs []string) {
	if a.userCount() > 0 {
		return
	}
	code := a.issueSetupCode()
	var text string
	for _, u := range baseURLs {
		text += fmt.Sprintf("%s/setup/%s\n", u, code)
	}
	log.Printf("berth: no users yet. Open within 10 minutes to create the super admin:\n%s", text)
	if a.cfg.DataDir != "" {
		_ = os.WriteFile(filepath.Join(a.cfg.DataDir, "setup-url"), []byte(text), 0o600)
	}
}

func (a *App) handleSetupStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]bool{"needs_setup": a.userCount() == 0})
}

func (a *App) handleSetup(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Code        string `json:"code"`
		Username    string `json:"username"`
		DisplayName string `json:"display_name"`
		Password    string `json:"password"`
	}
	if err := readJSON(r, &in); err != nil {
		a.fail(w, r, err)
		return
	}
	if a.limiter.blocked("setup:" + a.clientIP(r)) {
		a.fail(w, r, errs.New(429, "auth.rate_limited"))
		return
	}
	// Consume the code first so it can never be used twice, even on a later validation error.
	res, err := a.db.Exec(`DELETE FROM setup WHERE code_hash=? AND expires_at>?`, tokenHash(in.Code), time.Now().Unix())
	if n, _ := res.RowsAffected(); err != nil || n != 1 || a.userCount() > 0 {
		a.limiter.record("setup:" + a.clientIP(r))
		a.fail(w, r, errs.New(410, "setup.code_invalid"))
		return
	}
	u, err := a.createUser(in.Username, in.DisplayName, in.Password, RoleSuper)
	if err != nil {
		a.issueSetupCodeAgain(in.Code)
		a.fail(w, r, err)
		return
	}
	if a.cfg.DataDir != "" {
		_ = os.Remove(filepath.Join(a.cfg.DataDir, "setup-url"))
	}
	csrf, err := a.createSession(w, r, u, "setup", "full")
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.recordLogin(r, u.Username, u.ID, true, "setup", "")
	writeJSON(w, 200, map[string]any{"stage": "full", "csrf": csrf, "user": u})
}

// issueSetupCodeAgain restores a consumed code after a fixable input error (weak password),
// so the person can correct it without a new link. Same expiry window from now.
func (a *App) issueSetupCodeAgain(code string) {
	_, _ = a.db.Exec(`INSERT OR REPLACE INTO setup(code_hash, expires_at) VALUES(?,?)`, tokenHash(code), time.Now().Add(setupTTL).Unix())
}
