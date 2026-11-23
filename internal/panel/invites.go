package panel

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/wangsirco/berth/internal/change"
	"github.com/wangsirco/berth/internal/errs"
	"github.com/wangsirco/berth/internal/sign"
)

// Invitations: a super admin makes a link (or a card with it) carrying a role, permissions
// and quotas; whoever opens it within its time picks a username and password and is in,
// with exactly those rights. Presets cover the usual cases.

type invitePreset struct {
	Key    string   `json:"key"`
	Role   string   `json:"role"`
	Perms  []string `json:"perms"`
	Quota  Quota    `json:"quota"`
	Simple bool     `json:"simple,omitempty"`
}

var invitePresets = []invitePreset{
	// family and friends: only the app wall (the apps you put there), nothing else
	{Key: "family", Role: RoleViewer, Simple: true},
	{Key: "viewer", Role: RoleViewer},
	{Key: "sites", Role: RoleAdmin, Perms: []string{"overview.view", "sites.view", "sites.manage", "certs.manage", "store.install", "containers.view", "files.read", "files.write", "db.view"},
		Quota: Quota{Sites: 5, Containers: 5}},
	{Key: "vms", Role: RoleAdmin, Perms: []string{"overview.view", "vms.view", "vms.manage"}, Quota: Quota{VMs: 2, CPUs: 2, MemMB: 4096, DiskGB: 40}},
	{Key: "dev", Role: RoleAdmin, Perms: []string{"overview.view", "sites.view", "sites.manage", "containers.view", "containers.manage", "store.install", "files.read", "files.write",
		"db.view", "db.manage", "pipeline.run", "dev.use"}, Quota: Quota{Sites: 5, Containers: 10, CPUs: 4, MemMB: 8192}},
	{Key: "ops", Role: RoleAdmin, Perms: []string{"overview.view", "sites.view", "containers.view", "vms.view", "db.view", "network.view", "backup.view", "monitor.manage", "system.view"}},
}

func presetOf(key string) (invitePreset, bool) {
	for _, p := range invitePresets {
		if p.Key == key {
			return p, true
		}
	}
	return invitePreset{}, false
}

type invite struct {
	ID        int64    `json:"id"`
	CreatedBy string   `json:"created_by"`
	Preset    string   `json:"preset"`
	Role      string   `json:"role"`
	Perms     []string `json:"perms"`
	Quota     Quota    `json:"quota"`
	Simple    bool     `json:"simple"`
	Note      string   `json:"note"`
	MaxUses   int      `json:"max_uses"`
	Uses      int      `json:"uses"`
	Accepted  []string `json:"accepted"`
	ExpiresAt int64    `json:"expires_at"`
	CreatedAt int64    `json:"created_at"`
	Revoked   bool     `json:"revoked"`
}

func (iv *invite) usable() bool {
	return !iv.Revoked && iv.Uses < iv.MaxUses && time.Now().Unix() < iv.ExpiresAt
}

const inviteCols = `i.id, COALESCE(u.display_name, ''), i.preset, i.role, i.perms, i.quota, i.simple, i.note, i.max_uses, i.uses, i.accepted, i.expires_at, i.created_at, i.revoked`

func scanInvite(row interface{ Scan(...any) error }) (*invite, error) {
	var iv invite
	var perms, quota, acc string
	if err := row.Scan(&iv.ID, &iv.CreatedBy, &iv.Preset, &iv.Role, &perms, &quota, &iv.Simple, &iv.Note, &iv.MaxUses, &iv.Uses, &acc, &iv.ExpiresAt, &iv.CreatedAt, &iv.Revoked); err != nil {
		return nil, err
	}
	json.Unmarshal([]byte(perms), &iv.Perms)
	json.Unmarshal([]byte(quota), &iv.Quota)
	json.Unmarshal([]byte(acc), &iv.Accepted)
	if iv.Perms == nil {
		iv.Perms = []string{}
	}
	if iv.Accepted == nil {
		iv.Accepted = []string{}
	}
	return &iv, nil
}

func (a *App) inviteByCode(code string) (*invite, error) {
	iv, err := scanInvite(a.db.QueryRow(`SELECT `+inviteCols+` FROM invites i LEFT JOIN users u ON u.id=i.created_by WHERE i.code_hash=?`, tokenHash(code)))
	if err != nil || !iv.usable() {
		return nil, errs.New(404, "invite.invalid")
	}
	return iv, nil
}

// panelLinkBase: where people outside should open the panel — the external address,
// then the internal one, then whatever this browser used.
func (a *App) panelLinkBase(r *http.Request) string {
	if a.exposed() {
		if u := a.panelURL("panel.public_host"); u != "" {
			return u
		}
	}
	if u := a.panelURL("panel.lan_host"); u != "" {
		return u
	}
	return panelBase(r)
}

func (a *App) handleInviteCreate(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Preset  string   `json:"preset"`
		Role    string   `json:"role"`
		Perms   []string `json:"perms"`
		Quota   *Quota   `json:"quota"`
		Simple  bool     `json:"simple"`
		Note    string   `json:"note"`
		Days    int      `json:"days"`
		MaxUses int      `json:"max_uses"`
	}
	if err := readJSON(r, &in); err != nil {
		a.fail(w, r, err)
		return
	}
	if p, ok := presetOf(in.Preset); ok {
		in.Role, in.Perms, in.Simple = p.Role, p.Perms, p.Simple
		if in.Quota == nil {
			q := p.Quota
			in.Quota = &q
		}
	} else if in.Preset != "custom" {
		a.fail(w, r, errs.New(400, "invite.bad_preset"))
		return
	}
	if in.Role != RoleAdmin && in.Role != RoleViewer { // nobody is invited as a super admin
		a.fail(w, r, errs.New(400, "invite.bad_role"))
		return
	}
	if err := validPerms(in.Perms); err != nil {
		a.fail(w, r, err)
		return
	}
	if in.Role == RoleViewer {
		in.Perms = []string{}
	}
	if in.Quota == nil {
		in.Quota = &Quota{}
	}
	in.Days = clamp(in.Days, 1, 30)
	in.MaxUses = clamp(in.MaxUses, 1, 50)
	if len([]rune(in.Note)) > 200 {
		in.Note = string([]rune(in.Note)[:200])
	}
	code := sign.RandomHex(16)
	pj, _ := json.Marshal(in.Perms)
	qj, _ := json.Marshal(in.Quota)
	now := time.Now()
	res, err := a.db.Exec(`INSERT INTO invites(code_hash, created_by, preset, role, perms, quota, simple, note, max_uses, expires_at, created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
		tokenHash(code), currentUser(r).ID, in.Preset, in.Role, string(pj), string(qj), b2i(in.Simple), in.Note, in.MaxUses, now.Add(time.Duration(in.Days)*24*time.Hour).Unix(), now.Unix())
	if err != nil {
		a.fail(w, r, err)
		return
	}
	id, _ := res.LastInsertId()
	a.pipe.Record(currentActor(r), "local", "invite.create", map[string]any{"id": id, "preset": in.Preset, "role": in.Role, "perms": in.Perms, "days": in.Days, "max_uses": in.MaxUses},
		change.Plan{Summary: "plan.invite.create", SummaryP: map[string]any{"preset": in.Preset, "n": in.MaxUses, "days": in.Days}, Risk: change.RiskHigh,
			Affected: []change.Object{{Type: "invite", ID: strconv.FormatInt(id, 10)}}})
	iv, _ := scanInvite(a.db.QueryRow(`SELECT `+inviteCols+` FROM invites i LEFT JOIN users u ON u.id=i.created_by WHERE i.id=?`, id))
	writeJSON(w, 200, map[string]any{"invite": iv, "url": a.panelLinkBase(r) + "/invite/" + code})
}

func (a *App) handleInviteList(w http.ResponseWriter, r *http.Request) {
	rows, err := a.db.Query(`SELECT ` + inviteCols + ` FROM invites i LEFT JOIN users u ON u.id=i.created_by ORDER BY i.id DESC LIMIT 100`)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	defer rows.Close()
	out := []*invite{}
	for rows.Next() {
		if iv, err := scanInvite(rows); err == nil {
			out = append(out, iv)
		}
	}
	writeJSON(w, 200, map[string]any{"items": out, "presets": invitePresets, "public": a.isPublic()})
}

func (a *App) handleInviteRevoke(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	res, _ := a.db.Exec(`UPDATE invites SET revoked=1 WHERE id=? AND revoked=0`, id)
	if n, _ := res.RowsAffected(); n == 0 {
		a.fail(w, r, errs.New(404, "invite.invalid"))
		return
	}
	a.pipe.Record(currentActor(r), "local", "invite.revoke", map[string]any{"id": id},
		change.Plan{Summary: "plan.invite.revoke", SummaryP: map[string]any{"id": id}, Risk: change.RiskLow, Affected: []change.Object{{Type: "invite", ID: strconv.FormatInt(id, 10)}}})
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// GET /invite/{code}: a valid link lets its holder past the security entry, then the
// page that takes the code from the URL fragment (kept out of logs and Referer).
func (a *App) handleInviteLink(w http.ResponseWriter, r *http.Request) {
	code := r.PathValue("code")
	if a.limiter.blocked("invite:" + a.clientIP(r)) {
		blank404(w)
		return
	}
	if _, err := a.inviteByCode(code); err != nil {
		a.limiter.record("invite:" + a.clientIP(r))
		http.Redirect(w, r, "/accept-invite#invalid", http.StatusFound)
		return
	}
	if entry := a.setting("access.entry"); entry != "" {
		setCookie(w, entryCookie, a.entryToken(entry), 30*24*3600)
	}
	http.Redirect(w, r, "/accept-invite#"+code, http.StatusFound)
}

func (a *App) handleInviteInfo(w http.ResponseWriter, r *http.Request) {
	if a.limiter.blocked("invite:" + a.clientIP(r)) {
		a.fail(w, r, errs.New(429, "auth.rate_limited"))
		return
	}
	iv, err := a.inviteByCode(r.PathValue("code"))
	if err != nil {
		a.limiter.record("invite:" + a.clientIP(r))
		a.fail(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"panel": a.setting("panel.name"), "by": iv.CreatedBy, "preset": iv.Preset, "role": iv.Role, "perms": iv.Perms,
		"quota": iv.Quota, "simple": iv.Simple, "note": iv.Note, "expires_at": iv.ExpiresAt, "public": a.isPublic()})
}

func (a *App) handleInviteAccept(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Username    string `json:"username"`
		DisplayName string `json:"display_name"`
		Password    string `json:"password"`
	}
	if err := readJSON(r, &in); err != nil {
		a.fail(w, r, err)
		return
	}
	if a.limiter.blocked("invite:" + a.clientIP(r)) {
		a.fail(w, r, errs.New(429, "auth.rate_limited"))
		return
	}
	code := r.PathValue("code")
	iv, err := a.inviteByCode(code)
	if err != nil {
		a.limiter.record("invite:" + a.clientIP(r))
		a.fail(w, r, err)
		return
	}
	if err := validPassword(in.Password); err != nil {
		a.fail(w, r, err)
		return
	}
	hash, err := hashPassword(in.Password)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	// take a use first, so two people racing on a single-use link cannot both get in
	res, _ := a.db.Exec(`UPDATE invites SET uses=uses+1 WHERE id=? AND revoked=0 AND uses<max_uses AND expires_at>?`, iv.ID, time.Now().Unix())
	if n, _ := res.RowsAffected(); n != 1 {
		a.fail(w, r, errs.New(404, "invite.invalid"))
		return
	}
	u, err := a.insertUser(strings.TrimSpace(in.Username), strings.TrimSpace(in.DisplayName), hash, iv.Role, iv.Perms, UserScope{})
	if err != nil {
		a.db.Exec(`UPDATE invites SET uses=uses-1 WHERE id=?`, iv.ID)
		a.fail(w, r, err)
		return
	}
	qj, _ := json.Marshal(iv.Quota)
	a.db.Exec(`UPDATE users SET quota=? WHERE id=?`, string(qj), u.ID)
	if iv.Simple {
		u.Simple = true
		a.saveUser(u)
	}
	acc, _ := json.Marshal(append(iv.Accepted, u.Username))
	a.db.Exec(`UPDATE invites SET accepted=? WHERE id=?`, string(acc), iv.ID)
	a.pipe.Record(change.Actor{Type: "user", ID: strconv.FormatInt(u.ID, 10), Name: u.Username, Via: "invite", IP: a.clientIP(r)}, "local", "invite.accept",
		map[string]any{"invite": iv.ID, "username": u.Username, "role": iv.Role, "perms": iv.Perms},
		change.Plan{Summary: "plan.invite.accept", SummaryP: map[string]any{"username": u.Username, "by": iv.CreatedBy}, Risk: change.RiskHigh,
			Affected: []change.Object{{Type: "user", ID: u.Username, Name: u.Username}}})
	csrf, err := a.createSession(w, r, u, "password", "full")
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.recordLogin(r, u.Username, u.ID, true, "invite", "")
	writeJSON(w, 200, map[string]any{"stage": "full", "csrf": csrf, "user": u})
}

func init() {
	routeHooks = append(routeHooks, func(a *App, mux *http.ServeMux) {
		mux.HandleFunc("GET /api/v1/invites", a.superOnly(a.handleInviteList))
		mux.HandleFunc("POST /api/v1/invites", a.superOnly(a.handleInviteCreate))
		mux.HandleFunc("DELETE /api/v1/invites/{id}", a.superOnly(a.handleInviteRevoke))
		mux.HandleFunc("GET /invite/{code}", a.handleInviteLink)
		mux.HandleFunc("GET /api/v1/invite/{code}", a.handleInviteInfo)
		mux.HandleFunc("POST /api/v1/invite/{code}/accept", a.handleInviteAccept)
	})
}
