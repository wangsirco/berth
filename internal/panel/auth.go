package panel

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/wangsirco/berth/internal/change"
	"github.com/wangsirco/berth/internal/errs"
	"github.com/wangsirco/berth/internal/sign"
)

const (
	sessionCookie = "berth_s"
	sessionIdle   = 30 * 24 * time.Hour
	secondStepTTL = 10 * time.Minute
	loginWindow   = 15 * time.Minute
)

type ctxKey int

const (
	userKey ctxKey = iota
	actorKey
	sessionKey
	appKey
)

type session struct {
	hash  string
	user  *User
	csrf  string
	stage string // second_step | full
}

func tokenHash(t string) string {
	h := sha256.Sum256([]byte(t))
	return hex.EncodeToString(h[:])
}

func setCookie(w http.ResponseWriter, name, value string, maxAge int) {
	http.SetCookie(w, &http.Cookie{Name: name, Value: value, Path: "/", HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode, MaxAge: maxAge})
}

// createSession starts a session at the given stage and returns its CSRF token.
func (a *App) createSession(w http.ResponseWriter, r *http.Request, u *User, method, stage string) (string, error) {
	token, csrf := sign.RandomHex(32), sign.RandomHex(16)
	now := time.Now().Unix()
	if _, err := a.db.Exec(`INSERT INTO sessions(token_hash, user_id, csrf, ip, user_agent, method, stage, created_at, seen_at) VALUES(?,?,?,?,?,?,?,?,?)`,
		tokenHash(token), u.ID, csrf, a.clientIP(r), truncate(r.UserAgent(), 300), method, stage, now, now); err != nil {
		return "", err
	}
	if stage == "full" {
		_, _ = a.db.Exec(`UPDATE users SET last_login_at=? WHERE id=?`, now, u.ID)
	}
	setCookie(w, sessionCookie, token, int(sessionIdle.Seconds()))
	return csrf, nil
}

// promoteSession turns a second-step session into a full one.
func (a *App) promoteSession(s *session, method string) error {
	_, err := a.db.Exec(`UPDATE sessions SET stage='full', method=? WHERE token_hash=?`, method, s.hash)
	if err == nil {
		_, _ = a.db.Exec(`UPDATE users SET last_login_at=? WHERE id=?`, time.Now().Unix(), s.user.ID)
	}
	return err
}

func (a *App) destroySession(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		_, _ = a.db.Exec(`DELETE FROM sessions WHERE token_hash=?`, tokenHash(c.Value))
	}
	setCookie(w, sessionCookie, "", -1)
}

func (a *App) loadSession(r *http.Request) *session {
	c, err := r.Cookie(sessionCookie)
	if err != nil || c.Value == "" {
		return nil
	}
	h := tokenHash(c.Value)
	var uid, created, seen int64
	var csrf, stage string
	if a.db.QueryRow(`SELECT user_id, csrf, stage, created_at, seen_at FROM sessions WHERE token_hash=?`, h).Scan(&uid, &csrf, &stage, &created, &seen) != nil {
		return nil
	}
	now := time.Now()
	expired := now.Sub(time.Unix(seen, 0)) > sessionIdle || (stage == "second_step" && now.Sub(time.Unix(created, 0)) > secondStepTTL)
	if expired {
		_, _ = a.db.Exec(`DELETE FROM sessions WHERE token_hash=?`, h)
		return nil
	}
	u, err := a.getUser(uid)
	if err != nil || u.Disabled {
		return nil
	}
	if now.Unix()-seen > 60 {
		_, _ = a.db.Exec(`UPDATE sessions SET seen_at=? WHERE token_hash=?`, now.Unix(), h)
	}
	return &session{hash: h, user: u, csrf: csrf, stage: stage}
}

func currentUser(r *http.Request) *User {
	u, _ := r.Context().Value(userKey).(*User)
	return u
}

func currentActor(r *http.Request) change.Actor {
	a, _ := r.Context().Value(actorKey).(change.Actor)
	return a
}

func currentSession(r *http.Request) *session {
	s, _ := r.Context().Value(sessionKey).(*session)
	return s
}

func withIdentity(r *http.Request, u *User, ac change.Actor, s *session) *http.Request {
	ctx := context.WithValue(r.Context(), userKey, u)
	ctx = context.WithValue(ctx, actorKey, ac)
	if s != nil {
		ctx = context.WithValue(ctx, sessionKey, s)
	}
	return r.WithContext(ctx)
}

func (a *App) userActor(r *http.Request, u *User) change.Actor {
	return change.Actor{Type: "user", ID: strconv.FormatInt(u.ID, 10), Name: u.Username, Via: "ui", IP: a.clientIP(r)}
}

// authed requires a signed-in user (full session) or a valid token, a CSRF header on
// session writes, and perm (if non-empty).
func (a *App) authed(perm string, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if hasBearer(r) {
			r2, err := a.bearerAuth(r, perm)
			if err != nil {
				a.fail(w, r, err)
				return
			}
			h(w, r2)
			return
		}
		s := a.loadSession(r)
		if s == nil || s.stage != "full" {
			a.fail(w, r, errs.New(401, "auth.unauthorized"))
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			if subtle.ConstantTimeCompare([]byte(r.Header.Get("X-CSRF-Token")), []byte(s.csrf)) != 1 {
				a.fail(w, r, errs.New(403, "auth.csrf"))
				return
			}
		}
		if perm != "" && !s.user.Can(perm) {
			a.fail(w, r, errs.New(403, "auth.forbidden", "perm", perm))
			return
		}
		h(w, withIdentity(r, s.user, a.userActor(r, s.user), s))
	}
}

func (a *App) superOnly(h http.HandlerFunc) http.HandlerFunc {
	return a.authed("", func(w http.ResponseWriter, r *http.Request) {
		if currentUser(r).Role != RoleSuper {
			a.fail(w, r, errs.New(403, "auth.super_only"))
			return
		}
		h(w, r)
	})
}

func hasBearer(r *http.Request) bool {
	v := r.Header.Get("Authorization")
	return len(v) > 7 && v[:7] == "Bearer "
}

// loginLimiter counts failures per key in a sliding window (copied in spirit from Wharf).
type loginLimiter struct {
	mu     sync.Mutex
	max    int
	window time.Duration
	fail   map[string][]time.Time
}

func newLimiter(max int, window time.Duration) *loginLimiter {
	return &loginLimiter{max: max, window: window, fail: map[string][]time.Time{}}
}

func (l *loginLimiter) recent(k string) []time.Time {
	keep := l.fail[k][:0]
	for _, t := range l.fail[k] {
		if time.Since(t) < l.window {
			keep = append(keep, t)
		}
	}
	if len(keep) == 0 {
		delete(l.fail, k)
		return nil
	}
	l.fail[k] = keep
	return keep
}

func (l *loginLimiter) blocked(keys ...string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, k := range keys {
		if len(l.recent(k)) >= l.max {
			return true
		}
	}
	return false
}

func (l *loginLimiter) record(keys ...string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, k := range keys {
		l.fail[k] = append(l.recent(k), time.Now())
	}
}

func (l *loginLimiter) reset(keys ...string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, k := range keys {
		delete(l.fail, k)
	}
}

func (a *App) recordLogin(r *http.Request, username string, uid int64, ok bool, method, reason string) {
	var id any
	if uid > 0 {
		id = uid
	}
	_, _ = a.db.Exec(`INSERT INTO logins(ts, username, user_id, ok, method, ip, user_agent, reason) VALUES(?,?,?,?,?,?,?,?)`,
		time.Now().Unix(), truncate(username, 64), id, ok, method, a.clientIP(r), truncate(r.UserAgent(), 300), reason)
	if ok {
		a.loginAlert(username, uid, a.clientIP(r), method)
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

func contextWith(r *http.Request, k ctxKey, v any) context.Context {
	return context.WithValue(r.Context(), k, v)
}
