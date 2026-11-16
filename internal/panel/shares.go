package panel

import (
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/wangsirco/berth/internal/change"
	"github.com/wangsirco/berth/internal/errs"
	"github.com/wangsirco/berth/internal/sign"
)

// Share links: let someone without an account see (or use) one thing for a while.
// Shareable today: a remote desktop connection, which is also how a virtual machine's
// desktop is reached. Watch-only by default; control, a password and the expiry are
// chosen when sharing. Files have their own share links (file.share).

type linkShare struct {
	ID        int64  `json:"id"`
	By        string `json:"by"`
	ByID      int64  `json:"-"`
	Kind      string `json:"kind"` // rd
	TargetID  int64  `json:"target_id"`
	Name      string `json:"name"`
	Control   bool   `json:"control"`
	Password  bool   `json:"password"`
	pwHash    string
	Uses      int   `json:"uses"`
	LastUsed  int64 `json:"last_used"`
	ExpiresAt int64 `json:"expires_at"`
	CreatedAt int64 `json:"created_at"`
	Revoked   bool  `json:"revoked"`
}

const shareCols = `s.id, COALESCE(u.display_name, ''), s.created_by, s.kind, s.target_id, s.name, s.control, s.password_hash, s.uses, s.last_used, s.expires_at, s.created_at, s.revoked`

func scanShare(row interface{ Scan(...any) error }) (*linkShare, error) {
	var s linkShare
	err := row.Scan(&s.ID, &s.By, &s.ByID, &s.Kind, &s.TargetID, &s.Name, &s.Control, &s.pwHash, &s.Uses, &s.LastUsed, &s.ExpiresAt, &s.CreatedAt, &s.Revoked)
	s.Password = s.pwHash != ""
	return &s, err
}

func (a *App) shareByCode(code string) (*linkShare, error) {
	s, err := scanShare(a.db.QueryRow(`SELECT `+shareCols+` FROM link_shares s LEFT JOIN users u ON u.id=s.created_by WHERE s.code_hash=?`, tokenHash(code)))
	if err != nil || s.Revoked || time.Now().Unix() >= s.ExpiresAt {
		return nil, errs.New(404, "share.invalid")
	}
	return s, nil
}

func (a *App) handleShareCreate(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Kind     string `json:"kind"`
		TargetID int64  `json:"target_id"`
		Control  bool   `json:"control"`
		Hours    int    `json:"hours"`
		Password string `json:"password"`
	}
	if err := readJSON(r, &in); err != nil {
		a.fail(w, r, err)
		return
	}
	if in.Kind != "rd" {
		a.fail(w, r, errs.New(400, "share.bad_kind"))
		return
	}
	c, _, err := a.rdConn(in.TargetID)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	u := currentUser(r)
	if !c.mayUse(u) {
		a.fail(w, r, errs.New(403, "auth.forbidden", "perm", "remote.use"))
		return
	}
	in.Hours = clamp(in.Hours, 1, 24*30)
	pw := ""
	if in.Password != "" {
		if len([]rune(in.Password)) < 4 {
			a.fail(w, r, errs.New(400, "share.password_short"))
			return
		}
		if pw, err = hashPassword(in.Password); err != nil {
			a.fail(w, r, err)
			return
		}
	}
	code := sign.RandomHex(16)
	now := time.Now()
	res, err := a.db.Exec(`INSERT INTO link_shares(code_hash, created_by, kind, target_id, name, control, password_hash, expires_at, created_at) VALUES(?,?,?,?,?,?,?,?,?)`,
		tokenHash(code), u.ID, in.Kind, c.ID, c.Name, b2i(in.Control), pw, now.Add(time.Duration(in.Hours)*time.Hour).Unix(), now.Unix())
	if err != nil {
		a.fail(w, r, err)
		return
	}
	id, _ := res.LastInsertId()
	a.pipe.Record(currentActor(r), "local", "share.create", map[string]any{"id": id, "kind": in.Kind, "target": c.Name, "control": in.Control, "hours": in.Hours, "password": pw != ""},
		change.Plan{Summary: "plan.share.create", SummaryP: map[string]any{"name": c.Name, "hours": in.Hours}, Risk: change.RiskHigh,
			Affected: []change.Object{{Type: "rd", ID: strconv.FormatInt(c.ID, 10), Name: c.Name}}})
	writeJSON(w, 200, map[string]any{"id": id, "url": a.panelLinkBase(r) + "/v/" + code})
}

func (a *App) handleShareList(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	q := `SELECT ` + shareCols + ` FROM link_shares s LEFT JOIN users u ON u.id=s.created_by WHERE s.revoked=0 AND s.expires_at>?`
	args := []any{time.Now().Unix()}
	if !u.Can("users.manage") {
		q += ` AND s.created_by=?`
		args = append(args, u.ID)
	}
	rows, err := a.db.Query(q+` ORDER BY s.id DESC`, args...)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	defer rows.Close()
	out := []*linkShare{}
	for rows.Next() {
		if s, err := scanShare(rows); err == nil {
			out = append(out, s)
		}
	}
	writeJSON(w, 200, map[string]any{"items": out})
}

func (a *App) handleShareRevoke(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	u := currentUser(r)
	q, args := `UPDATE link_shares SET revoked=1 WHERE id=? AND revoked=0`, []any{id}
	if !u.Can("users.manage") {
		q += ` AND created_by=?`
		args = append(args, u.ID)
	}
	res, _ := a.db.Exec(q, args...)
	if n, _ := res.RowsAffected(); n == 0 {
		a.fail(w, r, errs.New(404, "share.invalid"))
		return
	}
	a.pipe.Record(currentActor(r), "local", "share.revoke", map[string]any{"id": id},
		change.Plan{Summary: "plan.share.revoke", SummaryP: map[string]any{"id": id}, Risk: change.RiskLow})
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// GET /v/{code}: past the security entry to the guest viewer, code in the fragment.
func (a *App) handleShareLink(w http.ResponseWriter, r *http.Request) {
	if a.limiter.blocked("share:" + a.clientIP(r)) {
		blank404(w)
		return
	}
	if _, err := a.shareByCode(r.PathValue("code")); err != nil {
		a.limiter.record("share:" + a.clientIP(r))
		http.Redirect(w, r, "/shared#invalid", http.StatusFound)
		return
	}
	if entry := a.setting("access.entry"); entry != "" {
		setCookie(w, entryCookie, a.entryToken(entry), 24*3600)
	}
	http.Redirect(w, r, "/shared#"+r.PathValue("code"), http.StatusFound)
}

// shareTickets: a short-lived ticket per opened share (after the password, if any), so
// the websocket URL never carries the password.
var shareTickets = struct {
	sync.Mutex
	m map[string]shareTicket
}{m: map[string]shareTicket{}}

type shareTicket struct {
	linkShare int64
	until     time.Time
}

func (a *App) handleShareOpen(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Password string `json:"password"`
	}
	readJSON(r, &in)
	ip := a.clientIP(r)
	if a.limiter.blocked("share:" + ip) {
		a.fail(w, r, errs.New(429, "auth.rate_limited"))
		return
	}
	s, err := a.shareByCode(r.PathValue("code"))
	if err != nil {
		a.limiter.record("share:" + ip)
		a.fail(w, r, err)
		return
	}
	info := map[string]any{"name": s.Name, "kind": s.Kind, "control": s.Control, "by": s.By, "expires_at": s.ExpiresAt, "password": s.Password}
	if s.Password && (in.Password == "" || !checkPasswordHash(s.pwHash, in.Password)) {
		if in.Password != "" {
			a.limiter.record("share:" + ip)
			a.fail(w, r, errs.New(403, "share.wrong_password"))
			return
		}
		writeJSON(w, 200, info) // ask for the password
		return
	}
	t := sign.RandomHex(16)
	shareTickets.Lock()
	for k, v := range shareTickets.m {
		if time.Now().After(v.until) {
			delete(shareTickets.m, k)
		}
	}
	shareTickets.m[t] = shareTicket{linkShare: s.ID, until: time.Now().Add(time.Minute)}
	shareTickets.Unlock()
	info["ticket"] = t
	writeJSON(w, 200, info)
}

func (a *App) handleShareTunnel(w http.ResponseWriter, r *http.Request) {
	shareTickets.Lock()
	tk, ok := shareTickets.m[r.URL.Query().Get("ticket")]
	delete(shareTickets.m, r.URL.Query().Get("ticket"))
	shareTickets.Unlock()
	if !ok || time.Now().After(tk.until) {
		a.fail(w, r, errs.New(404, "share.invalid"))
		return
	}
	s, err := scanShare(a.db.QueryRow(`SELECT `+shareCols+` FROM link_shares s LEFT JOIN users u ON u.id=s.created_by WHERE s.id=?`, tk.linkShare))
	if err != nil || s.Revoked || time.Now().Unix() >= s.ExpiresAt {
		a.fail(w, r, errs.New(404, "share.invalid"))
		return
	}
	c, secret, err := a.rdConn(s.TargetID)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	// the sharer must still be allowed to use it
	if owner, err := a.getUser(s.ByID); err != nil || !c.mayUse(owner) {
		a.fail(w, r, errs.New(404, "share.invalid"))
		return
	}
	a.db.Exec(`UPDATE link_shares SET uses=uses+1, last_used=? WHERE id=?`, time.Now().Unix(), s.ID)
	ac := change.Actor{Type: "share", ID: strconv.FormatInt(s.ID, 10), Name: "share #" + strconv.FormatInt(s.ID, 10) + " (" + s.By + ")", Via: "share", IP: a.clientIP(r)}
	a.rdRelay(w, r, c, secret, !s.Control, ac)
}

func init() {
	routeHooks = append(routeHooks, func(a *App, mux *http.ServeMux) {
		mux.HandleFunc("GET /api/v1/shares", a.authed("remote.use", a.handleShareList))
		mux.HandleFunc("POST /api/v1/shares", a.authed("remote.use", a.handleShareCreate))
		mux.HandleFunc("DELETE /api/v1/shares/{id}", a.authed("remote.use", a.handleShareRevoke))
		mux.HandleFunc("GET /v/{code}", a.handleShareLink)
		mux.HandleFunc("POST /api/v1/share/{code}/open", a.handleShareOpen)
		mux.HandleFunc("GET /api/v1/share/tunnel", a.handleShareTunnel)
	})
}
