package panel

import (
	"crypto/rand"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/argon2"

	"github.com/wangsirco/berth/internal/errs"
)

const (
	RoleSuper  = "super"
	RoleAdmin  = "admin"
	RoleViewer = "viewer"
)

type UserScope struct {
	Hosts []string `json:"hosts,omitempty"`
	Paths []string `json:"paths,omitempty"`
	Sites []int64  `json:"sites,omitempty"` // site ids (plus the sites the user made)
}

type User struct {
	ID          int64     `json:"id"`
	Username    string    `json:"username"`
	DisplayName string    `json:"display_name"`
	Role        string    `json:"role"`
	Perms       []string  `json:"perms"`
	Scope       UserScope `json:"scope"`
	LoginMode   string    `json:"login_mode"`
	Locale      string    `json:"locale"`
	Disabled    bool      `json:"disabled"`
	CreatedAt   int64     `json:"created_at"`
	LastLoginAt int64     `json:"last_login_at"`
	Simple      bool      `json:"simple"` // opens the app wall (简要模式) instead of the panel
}

// PermDef is one permission key (方案 §5.15). Danger marks high-risk grants.
type PermDef struct {
	Key    string `json:"key"`
	Group  string `json:"group"`
	Danger bool   `json:"danger,omitempty"`
	Super  bool   `json:"super_only,omitempty"`
}

var Permissions = []PermDef{
	{Key: "overview.view", Group: "overview"},
	{Key: "sites.view", Group: "sites"}, {Key: "sites.manage", Group: "sites"},
	{Key: "certs.manage", Group: "sites"},
	{Key: "containers.view", Group: "containers"}, {Key: "containers.manage", Group: "containers"},
	{Key: "vms.view", Group: "vms"}, {Key: "vms.manage", Group: "vms"},
	{Key: "store.install", Group: "store"},
	{Key: "files.read", Group: "files"}, {Key: "files.write", Group: "files"},
	{Key: "db.view", Group: "db"}, {Key: "db.manage", Group: "db"}, {Key: "db.query", Group: "db"},
	{Key: "terminal.use", Group: "terminal", Danger: true},
	{Key: "remote.use", Group: "remote", Danger: true},
	{Key: "dev.use", Group: "dev", Danger: true},
	{Key: "pipeline.run", Group: "pipeline"}, {Key: "pipeline.manage", Group: "pipeline"},
	{Key: "network.view", Group: "network"}, {Key: "network.manage", Group: "network"},
	{Key: "backup.view", Group: "backup"}, {Key: "backup.manage", Group: "backup"}, {Key: "backup.restore", Group: "backup"},
	{Key: "monitor.manage", Group: "monitor"},
	{Key: "system.view", Group: "system"}, {Key: "system.manage", Group: "system"},
	{Key: "disk.manage", Group: "system", Danger: true},
	{Key: "users.manage", Group: "admin", Super: true},
	{Key: "tokens.manage", Group: "admin", Super: true},
	{Key: "audit.view", Group: "admin", Super: true},
	{Key: "extensions.manage", Group: "admin", Super: true},
	{Key: "extensions.use", Group: "admin"},
	{Key: "settings.manage", Group: "admin", Super: true},
}

func permDef(key string) (PermDef, bool) {
	for _, p := range Permissions {
		if p.Key == key {
			return p, true
		}
	}
	return PermDef{}, false
}

// Can reports whether u holds perm. Super holds everything; viewers hold every
// non-dangerous *.view key; admins hold what was granted (never super-only keys).
func (u *User) Can(perm string) bool {
	if u == nil || u.Disabled {
		return false
	}
	if u.Role == RoleSuper {
		return true
	}
	def, ok := permDef(perm)
	if !ok || def.Super {
		return false
	}
	if u.Role == RoleViewer {
		return strings.HasSuffix(perm, ".view") && !def.Danger
	}
	for _, p := range u.Perms {
		if p == perm {
			return true
		}
	}
	return false
}

var usernameRe = regexp.MustCompile(`^[a-zA-Z0-9_.-]{3,32}$`)

func validPassword(p string) error {
	if len([]rune(p)) < 10 {
		return errs.New(400, "user.password_short", "min", 10)
	}
	if len(p) > 256 {
		return errs.New(400, "user.password_long", "max", 256)
	}
	if strings.Count(p, p[:1]) == len(p) {
		return errs.New(400, "user.password_weak")
	}
	return nil
}

// argon2id parameters (OWASP minimum: m=19 MiB, t=2, p=1).
const (
	argonTime    = 2
	argonMemory  = 19 * 1024
	argonThreads = 1
	argonKeyLen  = 32
)

func hashPassword(p string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	h := argon2.IDKey([]byte(p), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	enc := base64.RawStdEncoding
	return fmt.Sprintf("$argon2id$v=19$m=%d,t=%d,p=%d$%s$%s", argonMemory, argonTime, argonThreads, enc.EncodeToString(salt), enc.EncodeToString(h)), nil
}

func checkPasswordHash(encoded, p string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false
	}
	var m, t uint32
	var th uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &m, &t, &th); err != nil {
		return false
	}
	enc := base64.RawStdEncoding
	salt, err1 := enc.DecodeString(parts[4])
	want, err2 := enc.DecodeString(parts[5])
	if err1 != nil || err2 != nil {
		return false
	}
	got := argon2.IDKey([]byte(p), salt, t, m, th, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1
}

var (
	dummyOnce sync.Once
	dummyHash string
)

// burnDummyHash spends the same time as a real check, so unknown usernames are not
// distinguishable by response time.
func burnDummyHash(p string) {
	dummyOnce.Do(func() { dummyHash, _ = hashPassword("berth-dummy-password") })
	checkPasswordHash(dummyHash, p)
}

const userCols = `id, username, display_name, password_hash, role, perms, scope, login_mode, locale, disabled, created_at, last_login_at, simple`

func scanUser(row interface{ Scan(...any) error }) (*User, string, error) {
	var u User
	var hash, perms, scope string
	if err := row.Scan(&u.ID, &u.Username, &u.DisplayName, &hash, &u.Role, &perms, &scope, &u.LoginMode, &u.Locale, &u.Disabled, &u.CreatedAt, &u.LastLoginAt, &u.Simple); err != nil {
		return nil, "", err
	}
	_ = json.Unmarshal([]byte(perms), &u.Perms)
	_ = json.Unmarshal([]byte(scope), &u.Scope)
	if u.Perms == nil {
		u.Perms = []string{}
	}
	return &u, hash, nil
}

func (a *App) getUser(id int64) (*User, error) {
	u, _, err := scanUser(a.db.QueryRow(`SELECT `+userCols+` FROM users WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errs.New(404, "user.not_found")
	}
	return u, err
}

func (a *App) listUsers() ([]*User, error) {
	rows, err := a.db.Query(`SELECT ` + userCols + ` FROM users ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*User{}
	for rows.Next() {
		u, _, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

func (a *App) userCount() int {
	var n int
	a.db.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&n)
	return n
}

func validRole(r string) bool { return r == RoleSuper || r == RoleAdmin || r == RoleViewer }

// createUser inserts a user directly (setup and tests). UI/API go through the user.create kind.
func (a *App) createUser(username, display, password, role string) (*User, error) {
	if err := validPassword(password); err != nil {
		return nil, err
	}
	h, err := hashPassword(password)
	if err != nil {
		return nil, err
	}
	return a.insertUser(username, display, h, role, nil, UserScope{})
}

func (a *App) insertUser(username, display, hash, role string, perms []string, scope UserScope) (*User, error) {
	if !usernameRe.MatchString(username) {
		return nil, errs.New(400, "user.bad_username")
	}
	if !validRole(role) {
		return nil, errs.New(400, "user.bad_role")
	}
	if display == "" {
		display = username
	}
	if perms == nil {
		perms = []string{}
	}
	pj, _ := json.Marshal(perms)
	sj, _ := json.Marshal(scope)
	now := time.Now().Unix()
	res, err := a.db.Exec(`INSERT INTO users(username, display_name, password_hash, role, perms, scope, created_at, updated_at) VALUES(?,?,?,?,?,?,?,?)`,
		username, display, hash, role, string(pj), string(sj), now, now)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return nil, errs.New(409, "user.exists", "username", username)
		}
		return nil, err
	}
	id, _ := res.LastInsertId()
	return a.getUser(id)
}

func (a *App) saveUser(u *User) error {
	pj, _ := json.Marshal(u.Perms)
	sj, _ := json.Marshal(u.Scope)
	_, err := a.db.Exec(`UPDATE users SET display_name=?, role=?, perms=?, scope=?, login_mode=?, locale=?, disabled=?, simple=?, updated_at=? WHERE id=?`,
		u.DisplayName, u.Role, string(pj), string(sj), u.LoginMode, u.Locale, u.Disabled, u.Simple, time.Now().Unix(), u.ID)
	if err == nil && u.Disabled {
		_, err = a.db.Exec(`DELETE FROM sessions WHERE user_id=?`, u.ID)
	}
	return err
}

func (a *App) setPasswordHash(id int64, hash string) error {
	_, err := a.db.Exec(`UPDATE users SET password_hash=?, updated_at=? WHERE id=?`, hash, time.Now().Unix(), id)
	return err
}

// checkPassword returns the user if the password matches and the account is active.
func (a *App) checkPassword(username, password string) (*User, error) {
	u, hash, err := scanUser(a.db.QueryRow(`SELECT `+userCols+` FROM users WHERE username=?`, username))
	if err != nil {
		burnDummyHash(password)
		return nil, errs.New(401, "auth.bad_credentials")
	}
	if hash == "" || !checkPasswordHash(hash, password) || u.Disabled {
		return nil, errs.New(401, "auth.bad_credentials")
	}
	return u, nil
}

// superCount counts active super admins (we never let the last one go away).
func (a *App) superCount() int {
	var n int
	a.db.QueryRow(`SELECT COUNT(*) FROM users WHERE role='super' AND disabled=0`).Scan(&n)
	return n
}
