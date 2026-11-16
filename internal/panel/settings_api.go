package panel

import (
	"encoding/json"
	"net"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/wangsirco/berth/internal/agent"
	"github.com/wangsirco/berth/internal/change"
	"github.com/wangsirco/berth/internal/errs"
)

// settingDef lists the panel settings people may change, with validation.
type settingDef struct {
	validate func(v string) error
	secret   bool
}

// names the local CA may sign for (方案 §5.2.2: private suffixes only)
var localNameRe = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]*[a-z0-9])?\.)+(lan|local|home\.arpa|internal)$`)

// CertNames are extra host names for the panel's certificate (settings access.names).
func (a *App) CertNames() []string { return a.certNamesOf() }

var entryRe = regexp.MustCompile(`^/[a-zA-Z0-9][a-zA-Z0-9_-]{5,31}$`)

var settingDefs = map[string]settingDef{
	"panel.name": {validate: func(v string) error {
		if len([]rune(v)) > 40 {
			return errs.New(400, "settings.too_long", "max", 40)
		}
		return nil
	}},
	"panel.locale": {validate: func(v string) error {
		if v != "" && !contains(Locales, v) {
			return errs.New(400, "params.enum", "name", "locale", "allowed", strings.Join(Locales, ", "))
		}
		return nil
	}},
	"access.entry": {validate: func(v string) error {
		if v != "" && (!entryRe.MatchString(v) || strings.HasPrefix(v, "/api") || strings.HasPrefix(v, "/assets")) {
			return errs.New(400, "settings.bad_entry")
		}
		return nil
	}},
	"access.allow_cidrs": {validate: func(v string) error {
		for _, c := range splitList(v) {
			if _, _, err := net.ParseCIDR(c); err != nil {
				return errs.New(400, "token.bad_ip", "value", c)
			}
		}
		return nil
	}},
	"access.public":     {validate: boolSetting},
	"panel.lan_host":    {validate: validLanHost},
	"panel.public_host": {validate: validPublicHost},
	"access.names": {validate: func(v string) error {
		for _, n := range splitList(v) {
			if !localNameRe.MatchString(strings.ToLower(n)) {
				return errs.New(400, "settings.bad_name", "name", n)
			}
		}
		return nil
	}},
	"terminal.default_user": {validate: func(v string) error {
		if v == "root" {
			return errs.New(400, "settings.no_root_default")
		}
		return nil
	}},
	"terminal.record_all":       {validate: boolSetting},
	"tasks.heavy_parallel":      {validate: intSetting(1, 4)},
	"metrics.keep_min_days":     {validate: intSetting(1, 90)},
	"metrics.keep_hour_days":    {validate: intSetting(7, 1095)},
	"files.trash_days":          {validate: intSetting(1, 365)},
	"security.bf_enabled":       {validate: boolSetting},
	"security.waf_ban":          {validate: boolSetting},
	"cloudflare.sync_auto_bans": {validate: boolSetting},
	"wharf.sync_auto_bans":      {validate: boolSetting},
	"cloudflare.ban_integration": {validate: func(v string) error {
		if v != "" && !regexp.MustCompile(`^[a-z0-9_-]{1,40}$`).MatchString(v) {
			return errs.New(400, "dns.account_not_found", "id", v)
		}
		return nil
	}},
	"security.bf_attempts":   {validate: intSetting(3, 100)},
	"security.bf_window_min": {validate: intSetting(1, 1440)},
	"security.bf_whitelist": {validate: func(v string) error {
		for _, c := range splitList(v) {
			if net.ParseIP(c) == nil {
				if _, _, err := net.ParseCIDR(c); err != nil {
					return errs.New(400, "ban.bad_addr", "addr", c)
				}
			}
		}
		return nil
	}},
}

func boolSetting(v string) error {
	if v != "" && v != "0" && v != "1" {
		return errs.New(400, "params.enum", "name", "value", "allowed", "0, 1")
	}
	return nil
}

func intSetting(lo, hi int) func(string) error {
	return func(v string) error {
		n, err := strconv.Atoi(v)
		if err != nil || n < lo || n > hi {
			return errs.New(400, "settings.range", "min", lo, "max", hi)
		}
		return nil
	}
}

func init() {
	change.Register(change.Kind{
		Name: "settings.update", Perm: "settings.manage", Scope: "settings:write", Title: "kind.settings.update",
		Params: change.ParamSchema{Props: map[string]change.Prop{"values": {Type: "object"}}, Required: []string{"values"}},
		CLI:    "berth settings set {{.values}}",
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			var p struct{ Values map[string]string }
			json.Unmarshal(raw, &p)
			a := svc(env)
			keys := make([]string, 0, len(p.Values))
			var before, after strings.Builder
			for k := range p.Values {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			risk := change.RiskLow
			for _, k := range keys {
				def, ok := settingDefs[k]
				if !ok {
					return change.Plan{}, errs.New(400, "settings.unknown", "key", k)
				}
				if err := def.validate(p.Values[k]); err != nil {
					return change.Plan{}, err
				}
				if strings.HasPrefix(k, "access.") {
					risk = change.RiskHigh // can lock people out
				}
				before.WriteString(k + " = " + a.setting(k) + "\n")
				after.WriteString(k + " = " + p.Values[k] + "\n")
			}
			return change.Plan{Summary: "plan.settings.update", SummaryP: map[string]any{"keys": strings.Join(keys, ", ")}, Risk: risk, Undoable: true,
				Files:    []change.FileDiff{{Path: "/settings", Before: before.String(), After: after.String()}},
				Affected: []change.Object{{Type: "setting", ID: strings.Join(keys, ","), Name: strings.Join(keys, ", ")}}}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			var p struct{ Values map[string]string }
			json.Unmarshal(raw, &p)
			a := svc(env)
			old := map[string]string{}
			for k, v := range p.Values {
				old[k] = a.setting(k)
				if err := a.setSetting(k, v); err != nil {
					return nil, nil, err
				}
			}
			if _, ok := p.Values["panel.lan_host"]; ok && a.certRefresh != nil {
				go a.certRefresh()
			} else if _, ok := p.Values["access.names"]; ok && a.certRefresh != nil {
				go a.certRefresh()
			}
			u, _ := json.Marshal(map[string]any{"values": old})
			return nil, &change.UndoRecipe{Kind: "settings.update", Params: u}, nil
		},
	})
	routeHooks = append(routeHooks, func(a *App, mux *http.ServeMux) {
		mux.HandleFunc("GET /api/v1/settings", a.superOnly(a.handleSettings))
		mux.HandleFunc("PUT /api/v1/settings", a.superOnly(a.handleSettingsPut))
		mux.HandleFunc("POST /api/v1/settings", a.superOnly(a.handleSettingsPut)) // previewable (?plan=1)
		mux.HandleFunc("GET /api/v1/hosts/{host}/system/users", a.superOnly(a.handleSystemUsers))
		mux.HandleFunc("POST /api/v1/welcome/done", a.authed("", a.handleWelcomeDone))
		mux.HandleFunc("PATCH /api/v1/me", a.authed("", a.handleMePatch))
		mux.HandleFunc("POST /api/v1/me/password", a.authed("", a.handleMePassword))
		mux.HandleFunc("GET /api/v1/me/sessions", a.authed("", a.handleMySessions))
		mux.HandleFunc("DELETE /api/v1/me/sessions/{id}", a.authed("", a.handleMySessionDelete))
		mux.HandleFunc("GET /api/v1/ca.crt", a.handleCACert)
	})
}

func (a *App) handleSettings(w http.ResponseWriter, r *http.Request) {
	out := map[string]string{}
	for k, d := range settingDefs {
		if !d.secret {
			out[k] = a.setting(k)
		}
	}
	if out["access.allow_cidrs"] == "" {
		out["access.allow_cidrs"] = strings.Join(a.cfg.AllowCIDRs, "\n")
	}
	writeJSON(w, 200, map[string]any{"values": out, "public": a.isPublic(), "ca_fingerprint": a.caFingerprint, "client_ip": a.clientIP(r)})
}

// PUT /api/v1/settings {values} — one change for all values (with ?plan=1 support).
func (a *App) handleSettingsPut(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Values map[string]string `json:"values"`
	}
	if err := readJSON(r, &in); err != nil {
		a.fail(w, r, err)
		return
	}
	if cidrs, ok := in.Values["access.allow_cidrs"]; ok {
		// refuse a list that would lock out the person making the change
		ip := net.ParseIP(a.clientIP(r))
		allowed := false
		for _, c := range splitList(cidrs) {
			if _, n, err := net.ParseCIDR(c); err == nil && ip != nil && n.Contains(ip) {
				allowed = true
			}
		}
		if !allowed && !a.isPublic() && in.Values["access.public"] != "1" {
			a.fail(w, r, errs.New(400, "settings.would_lock_out", "ip", a.clientIP(r)))
			return
		}
		in.Values["access.allow_cidrs"] = strings.Join(splitList(cidrs), ",")
	}
	if pub, ok := in.Values["access.public"]; ok && pub == "" && a.isPublic() && !a.cfg.Public {
		// closing public access from outside the allowlist would cut this browser off
		ip := net.ParseIP(a.clientIP(r))
		inside := false
		nets := a.allowNets()
		if cidrs, ok := in.Values["access.allow_cidrs"]; ok {
			nets = nil
			for _, c := range splitList(cidrs) {
				if _, n, err := net.ParseCIDR(c); err == nil {
					nets = append(nets, n)
				}
			}
		}
		for _, n := range nets {
			if ip != nil && n.Contains(ip) {
				inside = true
			}
		}
		if !inside {
			a.fail(w, r, errs.New(400, "settings.would_lock_out", "ip", a.clientIP(r)))
			return
		}
	}
	if pub, ok := in.Values["access.public"]; ok && pub == "1" {
		// open to the whole internet only behind a security entry
		entry, given := in.Values["access.entry"]
		if !given {
			entry = a.setting("access.entry")
		}
		if entry == "" {
			a.fail(w, r, errs.New(400, "settings.public_needs_entry"))
			return
		}
	}
	if entry, ok := in.Values["access.entry"]; ok && entry != "" && r.URL.Query().Get("plan") != "1" {
		// the person setting the entry path keeps access from this browser
		setCookie(w, entryCookie, a.entryToken(entry), 30*24*3600)
	}
	raw, _ := json.Marshal(in)
	a.runChange(w, r, "local", "settings.update", raw)
}

func (a *App) handleSystemUsers(w http.ResponseWriter, r *http.Request) {
	var out []agent.LoginUser
	if err := a.call(r.Context(), r.PathValue("host"), "GET", "/v1/system/users", nil, &out); err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"items": out})
}

func (a *App) handleWelcomeDone(w http.ResponseWriter, r *http.Request) {
	_ = a.setSetting(onboardKey(currentUser(r).ID), strconv.FormatInt(time.Now().Unix(), 10))
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func onboardKey(id int64) string { return "welcome.done." + strconv.FormatInt(id, 10) }

// PATCH /api/v1/me {display_name, locale} — your own profile (no special permission).
func (a *App) handleMePatch(w http.ResponseWriter, r *http.Request) {
	var in struct {
		DisplayName *string `json:"display_name"`
		Locale      *string `json:"locale"`
	}
	if err := readJSON(r, &in); err != nil {
		a.fail(w, r, err)
		return
	}
	u := currentUser(r)
	if in.DisplayName != nil {
		if n := strings.TrimSpace(*in.DisplayName); n != "" && len([]rune(n)) <= 40 {
			u.DisplayName = n
		}
	}
	if in.Locale != nil && (*in.Locale == "" || contains(Locales, *in.Locale)) {
		u.Locale = *in.Locale
	}
	if err := a.saveUser(u); err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, 200, u)
}

// POST /api/v1/me/password {current, new}
func (a *App) handleMePassword(w http.ResponseWriter, r *http.Request) {
	if err := requireSession(r); err != nil {
		a.fail(w, r, err)
		return
	}
	var in struct{ Current, New string }
	if err := readJSON(r, &in); err != nil {
		a.fail(w, r, err)
		return
	}
	u := currentUser(r)
	keys := []string{"ip:" + a.clientIP(r), "user:" + u.Username}
	if a.limiter.blocked(keys...) {
		a.fail(w, r, errs.New(429, "auth.rate_limited"))
		return
	}
	if _, err := a.checkPassword(u.Username, in.Current); err != nil {
		a.limiter.record(keys...)
		a.fail(w, r, errs.New(401, "auth.bad_current_password"))
		return
	}
	if err := validPassword(in.New); err != nil {
		a.fail(w, r, err)
		return
	}
	h, _ := hashPassword(in.New)
	if err := a.setPasswordHash(u.ID, h); err != nil {
		a.fail(w, r, err)
		return
	}
	// other sessions end; this one stays
	_, _ = a.db.Exec(`DELETE FROM sessions WHERE user_id=? AND token_hash<>?`, u.ID, currentSession(r).hash)
	a.recordSecurityEvent(r, "account.password", u, nil)
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (a *App) handleMySessions(w http.ResponseWriter, r *http.Request) {
	u, s := currentUser(r), currentSession(r)
	type sv struct {
		ID      string `json:"id"`
		IP      string `json:"ip"`
		Agent   string `json:"user_agent"`
		Method  string `json:"method"`
		Created int64  `json:"created_at"`
		Seen    int64  `json:"seen_at"`
		Current bool   `json:"current"`
	}
	out := []sv{}
	rows, err := a.db.Query(`SELECT token_hash, ip, user_agent, method, created_at, seen_at FROM sessions WHERE user_id=? AND stage='full' ORDER BY seen_at DESC`, u.ID)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var x sv
			var h string
			if rows.Scan(&h, &x.IP, &x.Agent, &x.Method, &x.Created, &x.Seen) == nil {
				x.ID, x.Current = h[:16], s != nil && h == s.hash
				out = append(out, x)
			}
		}
	}
	writeJSON(w, 200, map[string]any{"items": out})
}

func (a *App) handleMySessionDelete(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	id := r.PathValue("id")
	if len(id) != 16 {
		a.fail(w, r, errs.New(404, "session.not_found"))
		return
	}
	res, _ := a.db.Exec(`DELETE FROM sessions WHERE user_id=? AND substr(token_hash,1,16)=?`, u.ID, id)
	if n, _ := res.RowsAffected(); n == 0 {
		a.fail(w, r, errs.New(404, "session.not_found"))
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// GET /api/v1/ca.crt — the panel's local CA, to install on devices (no sign-in needed).
func (a *App) handleCACert(w http.ResponseWriter, r *http.Request) {
	if len(a.caPEM) == 0 {
		blank404(w)
		return
	}
	w.Header().Set("Content-Type", "application/x-x509-ca-cert")
	w.Header().Set("Content-Disposition", `attachment; filename="berth-ca.crt"`)
	_, _ = w.Write(a.caPEM)
}
