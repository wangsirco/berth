package panel

import (
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"math/big"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/wangsirco/berth/internal/change"
	"github.com/wangsirco/berth/internal/errs"
)

// Token kinds: pat = API token, agt = Agent token (for AI, with a danger policy),
// cli = issued to `berth login`.
var tokenPrefixes = map[string]string{"pat": "berth_pat_", "agt": "berth_agt_", "cli": "berth_cli_", "ext": "berth_ext_"}

// TokenResources are the resources a scope can name: <resource>:read|write.
var TokenResources = []string{"overview", "sites", "containers", "pipeline", "files", "terminal", "tasks", "audit", "users", "tokens", "settings", "system"}

type tokenSpec struct {
	Kind         string    `json:"kind"`
	Name         string    `json:"name"`
	Scopes       []string  `json:"scopes"`
	Limits       UserScope `json:"limits"`
	IPAllow      []string  `json:"ip_allow"`
	DangerPolicy string    `json:"danger_policy"` // run | confirm | readonly (agt only)
	RatePerMin   int       `json:"rate_per_min"`
	Note         string    `json:"note"`
	NotBefore    int64     `json:"not_before"`
	ExpiresAt    int64     `json:"expires_at"` // 0 = never
}

type tokenRow struct {
	ID           int64     `json:"id"`
	UserID       int64     `json:"user_id"`
	Kind         string    `json:"kind"`
	Name         string    `json:"name"`
	Prefix       string    `json:"prefix"`
	Scopes       []string  `json:"scopes"`
	Limits       UserScope `json:"limits"`
	IPAllow      []string  `json:"ip_allow"`
	DangerPolicy string    `json:"danger_policy"`
	RatePerMin   int       `json:"rate_per_min"`
	Note         string    `json:"note"`
	NotBefore    int64     `json:"not_before"`
	ExpiresAt    int64     `json:"expires_at"`
	LastUsedAt   int64     `json:"last_used_at"`
	LastIP       string    `json:"last_ip"`
	Uses         int64     `json:"uses"`
	Revoked      bool      `json:"revoked"`
	CreatedAt    int64     `json:"created_at"`
}

const tokenCols = `id, user_id, kind, name, prefix, scopes, limits, ip_allow, danger_policy, rate_per_min, note, not_before, expires_at, last_used_at, last_ip, uses, revoked, created_at`

func scanToken(row interface{ Scan(...any) error }) (*tokenRow, error) {
	var t tokenRow
	var scopes, limits, ips string
	if err := row.Scan(&t.ID, &t.UserID, &t.Kind, &t.Name, &t.Prefix, &scopes, &limits, &ips, &t.DangerPolicy, &t.RatePerMin, &t.Note,
		&t.NotBefore, &t.ExpiresAt, &t.LastUsedAt, &t.LastIP, &t.Uses, &t.Revoked, &t.CreatedAt); err != nil {
		return nil, err
	}
	_ = json.Unmarshal([]byte(scopes), &t.Scopes)
	_ = json.Unmarshal([]byte(limits), &t.Limits)
	_ = json.Unmarshal([]byte(ips), &t.IPAllow)
	return &t, nil
}

const base62 = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

func randomBase62(n int) string {
	b := make([]byte, n)
	max := big.NewInt(int64(len(base62)))
	for i := range b {
		v, err := rand.Int(rand.Reader, max)
		if err != nil {
			panic(err)
		}
		b[i] = base62[v.Int64()]
	}
	return string(b)
}

func validScope(s string) bool {
	res, act, ok := strings.Cut(s, ":")
	return ok && (act == "read" || act == "write") && contains(TokenResources, res)
}

func (sp *tokenSpec) normalize() error {
	if _, ok := tokenPrefixes[sp.Kind]; !ok {
		return errs.New(400, "params.enum", "name", "kind", "allowed", "pat, agt")
	}
	sp.Name = strings.TrimSpace(sp.Name)
	if sp.Name == "" || len(sp.Name) > 64 {
		return errs.New(400, "token.bad_name")
	}
	for _, s := range sp.Scopes {
		if !validScope(s) {
			return errs.New(400, "token.bad_scope", "scope", s)
		}
	}
	for _, c := range sp.IPAllow {
		if _, _, err := net.ParseCIDR(c); err != nil && net.ParseIP(c) == nil {
			return errs.New(400, "token.bad_ip", "value", c)
		}
	}
	if sp.Kind != "agt" {
		sp.DangerPolicy = "run"
	} else if sp.DangerPolicy == "" {
		sp.DangerPolicy = "confirm"
	}
	if sp.DangerPolicy != "run" && sp.DangerPolicy != "confirm" && sp.DangerPolicy != "readonly" {
		return errs.New(400, "params.enum", "name", "danger_policy", "allowed", "run, confirm, readonly")
	}
	if sp.RatePerMin <= 0 {
		sp.RatePerMin = 120
	}
	return nil
}

// issueToken creates a token for u and returns its plaintext (shown once) and id.
func (a *App) issueToken(u *User, sp tokenSpec) (string, int64) {
	tok, id, err := a.createToken(u, sp)
	if err != nil {
		panic(err)
	}
	return tok, id
}

func (a *App) createToken(u *User, sp tokenSpec) (string, int64, error) {
	if err := sp.normalize(); err != nil {
		return "", 0, err
	}
	tok := tokenPrefixes[sp.Kind] + randomBase62(32)
	sj, _ := json.Marshal(sp.Scopes)
	lj, _ := json.Marshal(sp.Limits)
	ij, _ := json.Marshal(orEmpty(sp.IPAllow))
	res, err := a.db.Exec(`INSERT INTO tokens(user_id, kind, name, token_hash, prefix, scopes, limits, ip_allow, danger_policy, rate_per_min, note, not_before, expires_at, created_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, u.ID, sp.Kind, sp.Name, tokenHash(tok), tok[:14], string(sj), string(lj), string(ij),
		sp.DangerPolicy, sp.RatePerMin, sp.Note, sp.NotBefore, sp.ExpiresAt, time.Now().Unix())
	if err != nil {
		return "", 0, err
	}
	id, _ := res.LastInsertId()
	return tok, id, nil
}

func (a *App) revokeToken(id int64) {
	_, _ = a.db.Exec(`UPDATE tokens SET revoked=1 WHERE id=?`, id)
}

func orEmpty(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// ---- request authentication ----

const tokenKey ctxKey = 100

func currentToken(r *http.Request) *tokenRow {
	t, _ := r.Context().Value(tokenKey).(*tokenRow)
	return t
}

// permScope maps a permission key to the token scope that allows it.
func permScope(perm string) string {
	res, act, _ := strings.Cut(perm, ".")
	switch res {
	case "certs":
		res = "sites"
	case "sites", "pipeline":
	case "vms", "store", "db", "remote", "dev", "network", "backup", "monitor", "disk", "extensions":
		res = "system" // not yet scoped individually
	}
	if act == "view" || act == "read" {
		return res + ":read"
	}
	return res + ":write"
}

func (t *tokenRow) has(scope string) bool {
	if contains(t.Scopes, scope) {
		return true
	}
	// write implies read
	if res, act, _ := strings.Cut(scope, ":"); act == "read" {
		return contains(t.Scopes, res+":write")
	}
	return false
}

var tokenBuckets struct {
	sync.Mutex
	m map[int64]*bucket
}

type bucket struct {
	tokens float64
	last   time.Time
}

func allowRate(id int64, perMin int) bool {
	tokenBuckets.Lock()
	defer tokenBuckets.Unlock()
	if tokenBuckets.m == nil {
		tokenBuckets.m = map[int64]*bucket{}
	}
	b := tokenBuckets.m[id]
	now := time.Now()
	if b == nil {
		b = &bucket{tokens: float64(perMin), last: now}
		tokenBuckets.m[id] = b
	}
	b.tokens = min(float64(perMin), b.tokens+now.Sub(b.last).Minutes()*float64(perMin))
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

func (a *App) bearerAuth(r *http.Request, perm string) (*http.Request, error) {
	tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	t, err := scanToken(a.db.QueryRow(`SELECT `+tokenCols+` FROM tokens WHERE token_hash=?`, tokenHash(tok)))
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		a.limiter.record("tokenip:" + a.clientIP(r))
		return nil, errs.New(401, "token.invalid")
	}
	if a.limiter.blocked("tokenip:" + a.clientIP(r)) {
		return nil, errs.New(429, "auth.rate_limited")
	}
	now := time.Now().Unix()
	if t.Revoked || (t.ExpiresAt > 0 && now >= t.ExpiresAt) || now < t.NotBefore {
		return nil, errs.New(401, "token.invalid")
	}
	u, err := a.getUser(t.UserID)
	if err != nil || u.Disabled {
		return nil, errs.New(401, "token.invalid")
	}
	ip := a.clientIP(r)
	if len(t.IPAllow) > 0 && !ipAllowed(ip, t.IPAllow) {
		return nil, errs.New(403, "token.ip_denied", "ip", ip)
	}
	if !allowRate(t.ID, t.RatePerMin) {
		return nil, errs.New(429, "token.rate_limited")
	}
	if perm != "" {
		if !u.Can(perm) {
			return nil, errs.New(403, "auth.forbidden", "perm", perm)
		}
		if !t.has(permScope(perm)) {
			return nil, errs.New(403, "token.out_of_scope", "scope", permScope(perm))
		}
	}
	if now-t.LastUsedAt > 10 || t.LastIP != ip {
		_, _ = a.db.Exec(`UPDATE tokens SET last_used_at=?, last_ip=?, uses=uses+1 WHERE id=?`, now, ip, t.ID)
	} else {
		_, _ = a.db.Exec(`UPDATE tokens SET uses=uses+1 WHERE id=?`, t.ID)
	}
	ac := a.actorForToken(u, t)
	ac.IP = ip
	switch v := r.Header.Get("X-Berth-Via"); v {
	case "cli", "mcp":
		ac.Via = v
	}
	ac.AISession = truncate(r.Header.Get("X-Berth-Session"), 64)
	ac.Intent = truncate(r.Header.Get("X-Berth-Intent"), 300)
	r2 := withIdentity(r, u, ac, nil)
	return r2.WithContext(contextWith(r2, tokenKey, t)), nil
}

func ipAllowed(ip string, allow []string) bool {
	p := net.ParseIP(ip)
	for _, c := range allow {
		if _, n, err := net.ParseCIDR(c); err == nil && p != nil && n.Contains(p) {
			return true
		}
		if c == ip {
			return true
		}
	}
	return false
}

var tokenPolicies sync.Map // actor ID -> danger policy, refreshed on every authenticated request

func (a *App) actorForToken(u *User, t *tokenRow) change.Actor {
	id := "token:" + strconv.FormatInt(t.ID, 10)
	if t.ID == 0 {
		id = "token:test-" + t.Kind + "-" + t.DangerPolicy
	}
	tokenPolicies.Store(id, t.DangerPolicy)
	via := "api"
	switch t.Kind {
	case "cli":
		via = "cli"
	case "ext":
		via = "extension"
	}
	name := t.Name
	if name == "" {
		name = u.Username
	}
	return change.Actor{Type: t.Kind, ID: id, Name: name, Via: via}
}

// tokenMayRun checks the token's scope and host limits for a kind.
func (a *App) tokenMayRun(r *http.Request, k change.Kind, host string) error {
	t := currentToken(r)
	if t == nil {
		return nil
	}
	if k.Scope != "" && !t.has(k.Scope) {
		return errs.New(403, "token.out_of_scope", "scope", k.Scope)
	}
	if len(t.Limits.Hosts) > 0 && !contains(t.Limits.Hosts, host) {
		return errs.New(403, "auth.host_forbidden", "host", host)
	}
	return nil
}

// requireSession refuses token callers (token management, approvals).
func requireSession(r *http.Request) error {
	if currentSession(r) == nil {
		return errs.New(403, "auth.session_required")
	}
	return nil
}

// ---- handlers ----

func (a *App) listTokens(u *User, all bool) ([]*tokenRow, error) {
	q, args := `SELECT `+tokenCols+` FROM tokens WHERE user_id=? ORDER BY revoked, id DESC`, []any{u.ID}
	if all {
		q, args = `SELECT `+tokenCols+` FROM tokens ORDER BY revoked, id DESC`, nil
	}
	rows, err := a.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*tokenRow{}
	for rows.Next() {
		t, err := scanToken(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (a *App) handleTokens(w http.ResponseWriter, r *http.Request) {
	if err := requireSession(r); err != nil {
		a.fail(w, r, err)
		return
	}
	u := currentUser(r)
	list, err := a.listTokens(u, u.Role == RoleSuper && r.URL.Query().Get("all") == "1")
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"items": list, "resources": TokenResources})
}

// POST /api/v1/tokens — the plaintext is returned once and never stored, so creation is
// recorded in the audit log (with the prefix) rather than replayable through the pipeline.
func (a *App) handleTokenCreate(w http.ResponseWriter, r *http.Request) {
	if err := requireSession(r); err != nil {
		a.fail(w, r, err)
		return
	}
	var sp tokenSpec
	if err := readJSON(r, &sp); err != nil {
		a.fail(w, r, err)
		return
	}
	if sp.Kind == "cli" {
		a.fail(w, r, errs.New(400, "params.enum", "name", "kind", "allowed", "pat, agt"))
		return
	}
	u := currentUser(r)
	tok, id, err := a.createToken(u, sp)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.recordTokenEvent(r, "token.create", id, sp.Name, tok[:14])
	t, _ := scanToken(a.db.QueryRow(`SELECT `+tokenCols+` FROM tokens WHERE id=?`, id))
	writeJSON(w, 200, map[string]any{"token": tok, "item": t})
}

// POST /api/v1/tokens/{id}/rotate — new token with the same settings, old one revoked.
func (a *App) handleTokenRotate(w http.ResponseWriter, r *http.Request) {
	if err := requireSession(r); err != nil {
		a.fail(w, r, err)
		return
	}
	u := currentUser(r)
	old, err := a.ownToken(u, r.PathValue("id"))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	owner, err := a.getUser(old.UserID)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	tok, id, err := a.createToken(owner, tokenSpec{Kind: old.Kind, Name: old.Name, Scopes: old.Scopes, Limits: old.Limits, IPAllow: old.IPAllow,
		DangerPolicy: old.DangerPolicy, RatePerMin: old.RatePerMin, Note: old.Note, ExpiresAt: old.ExpiresAt})
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.revokeToken(old.ID)
	a.recordTokenEvent(r, "token.rotate", id, old.Name, tok[:14])
	t, _ := scanToken(a.db.QueryRow(`SELECT `+tokenCols+` FROM tokens WHERE id=?`, id))
	writeJSON(w, 200, map[string]any{"token": tok, "item": t})
}

func (a *App) handleTokenRevoke(w http.ResponseWriter, r *http.Request) {
	if err := requireSession(r); err != nil {
		a.fail(w, r, err)
		return
	}
	t, err := a.ownToken(currentUser(r), r.PathValue("id"))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	raw, _ := json.Marshal(map[string]any{"id": t.ID})
	// ownToken already checked that the caller owns it (or is super)
	a.runChangeAuthorized(w, r, "local", "token.revoke", raw, func() error { return nil })
}

func (a *App) ownToken(u *User, idStr string) (*tokenRow, error) {
	id, _ := strconv.ParseInt(idStr, 10, 64)
	t, err := scanToken(a.db.QueryRow(`SELECT `+tokenCols+` FROM tokens WHERE id=?`, id))
	if err != nil || (t.UserID != u.ID && u.Role != RoleSuper) {
		return nil, errs.New(404, "token.not_found")
	}
	return t, nil
}

func (a *App) recordTokenEvent(r *http.Request, kind string, id int64, name, prefix string) {
	_, _ = a.pipe.Record(currentActor(r), "local", kind, map[string]any{"id": id, "name": name, "prefix": prefix}, change.Plan{
		Summary: "plan." + kind, SummaryP: map[string]any{"name": name, "prefix": prefix}, Risk: change.RiskMedium,
		Affected: []change.Object{{Type: "token", ID: strconv.FormatInt(id, 10), Name: name}},
	})
}

func init() {
	change.Register(change.Kind{
		Name: "token.revoke", Perm: "tokens.manage", Scope: "tokens:write", Title: "kind.token.revoke",
		Params: change.ParamSchema{Props: map[string]change.Prop{"id": {Type: "integer"}}, Required: []string{"id"}},
		CLI:    "berth token revoke {{.id}}",
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			var p struct{ ID int64 }
			json.Unmarshal(raw, &p)
			t, err := scanToken(svc(env).db.QueryRow(`SELECT `+tokenCols+` FROM tokens WHERE id=?`, p.ID))
			if err != nil {
				return change.Plan{}, errs.New(404, "token.not_found")
			}
			return change.Plan{Summary: "plan.token.revoke", SummaryP: map[string]any{"name": t.Name, "prefix": t.Prefix}, Risk: change.RiskMedium,
				Affected: []change.Object{{Type: "token", ID: strconv.FormatInt(t.ID, 10), Name: t.Name}}}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			var p struct{ ID int64 }
			json.Unmarshal(raw, &p)
			svc(env).revokeToken(p.ID)
			return nil, nil, nil
		},
	})
	routeHooks = append(routeHooks, func(a *App, mux *http.ServeMux) {
		mux.HandleFunc("GET /api/v1/tokens", a.authed("", a.handleTokens))
		mux.HandleFunc("POST /api/v1/tokens", a.authed("", a.handleTokenCreate))
		mux.HandleFunc("DELETE /api/v1/tokens/{id}", a.authed("", a.handleTokenRevoke))
		mux.HandleFunc("POST /api/v1/tokens/{id}/revoke", a.authed("", a.handleTokenRevoke)) // previewable (?plan=1)
		mux.HandleFunc("POST /api/v1/tokens/{id}/rotate", a.authed("", a.handleTokenRotate))
	})
}
