package panel

// Access records (方案 §5.12.4): every sign-in to the panel with country, device and
// warning signs, and everyone's open sessions (which an administrator can end).

import (
	"context"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/wangsirco/berth/internal/change"
	"github.com/wangsirco/berth/internal/errs"
)

type loginRecord struct {
	ID       int64    `json:"id"`
	TS       int64    `json:"ts"`
	Username string   `json:"username"`
	OK       bool     `json:"ok"`
	Method   string   `json:"method"`
	IP       string   `json:"ip"`
	Country  string   `json:"country,omitempty"`
	Device   string   `json:"device"`
	Reason   string   `json:"reason,omitempty"`
	Flags    []string `json:"flags,omitempty"` // new_ip | new_country | after_failures
}

var (
	uaBrowsers = []struct {
		re   *regexp.Regexp
		name string
	}{
		{regexp.MustCompile(`Edg/`), "Edge"}, {regexp.MustCompile(`OPR/|Opera`), "Opera"}, {regexp.MustCompile(`Firefox/`), "Firefox"},
		{regexp.MustCompile(`Chrome/|CriOS/`), "Chrome"}, {regexp.MustCompile(`Safari/`), "Safari"}, {regexp.MustCompile(`curl/`), "curl"},
		{regexp.MustCompile(`^berth`), "berth CLI"},
	}
	uaOS = []struct {
		re   *regexp.Regexp
		name string
	}{
		{regexp.MustCompile(`iPhone|iPad|iPod`), "iOS"}, {regexp.MustCompile(`Android`), "Android"}, {regexp.MustCompile(`Mac OS X|Macintosh`), "macOS"},
		{regexp.MustCompile(`Windows`), "Windows"}, {regexp.MustCompile(`CrOS`), "ChromeOS"}, {regexp.MustCompile(`Linux`), "Linux"},
	}
)

// deviceOf: "Chrome · macOS" from a user agent.
func deviceOf(ua string) string {
	var parts []string
	for _, b := range uaBrowsers {
		if b.re.MatchString(ua) {
			parts = append(parts, b.name)
			break
		}
	}
	for _, o := range uaOS {
		if o.re.MatchString(ua) {
			parts = append(parts, o.name)
			break
		}
	}
	if len(parts) == 0 {
		return truncate(ua, 40)
	}
	return strings.Join(parts, " · ")
}

// countries looks addresses up on the panel's own host (the agent has the database).
var geoCache sync.Map // ip -> country

func (a *App) countries(ctx context.Context, ips []string) map[string]string {
	out := map[string]string{}
	var ask []string
	for _, ip := range ips {
		if v, ok := geoCache.Load(ip); ok {
			out[ip] = v.(string)
		} else {
			ask = append(ask, ip)
		}
	}
	if len(ask) > 0 {
		var got map[string]string
		c, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		if a.call(c, "local", "POST", "/v1/geo", ask, &got) == nil {
			for ip, cc := range got {
				out[ip] = cc
				geoCache.Store(ip, cc)
			}
		}
	}
	return out
}

func (a *App) loginRecords(ctx context.Context, user string, failedOnly bool, limit int) []loginRecord {
	q := `SELECT id, ts, username, ok, method, ip, user_agent, reason, user_id FROM logins WHERE 1=1`
	var args []any
	if user != "" {
		q += ` AND username=?`
		args = append(args, user)
	}
	if failedOnly {
		q += ` AND ok=0`
	}
	rows, err := a.db.Query(q+` ORDER BY id DESC LIMIT ?`, append(args, limit)...)
	if err != nil {
		return nil
	}
	type raw struct {
		loginRecord
		uid int64
	}
	var list []raw
	ipSet := map[string]bool{}
	for rows.Next() {
		var x raw
		var ua string
		var uid *int64
		rows.Scan(&x.ID, &x.TS, &x.Username, &x.OK, &x.Method, &x.IP, &ua, &x.Reason, &uid)
		x.Device = deviceOf(ua)
		if uid != nil {
			x.uid = *uid
		}
		list = append(list, x)
		ipSet[x.IP] = true
	}
	rows.Close()
	ips := make([]string, 0, len(ipSet))
	for ip := range ipSet {
		ips = append(ips, ip)
	}
	cc := a.countries(ctx, ips)
	out := make([]loginRecord, 0, len(list))
	for _, x := range list {
		x.Country = cc[x.IP]
		if x.OK && x.uid > 0 {
			var before int
			a.db.QueryRow(`SELECT COUNT(*) FROM logins WHERE user_id=? AND ok=1 AND ip=? AND id < ?`, x.uid, x.IP, x.ID).Scan(&before)
			if before == 0 {
				x.Flags = append(x.Flags, "new_ip")
				// a country this person never signed in from
				if x.Country != "" && x.Country != "LAN" {
					prev := map[string]bool{}
					r2, err := a.db.Query(`SELECT DISTINCT ip FROM logins WHERE user_id=? AND ok=1 AND id < ? LIMIT 200`, x.uid, x.ID)
					if err == nil {
						var pips []string
						for r2.Next() {
							var ip string
							r2.Scan(&ip)
							pips = append(pips, ip)
						}
						r2.Close()
						for _, c := range a.countries(ctx, pips) {
							prev[c] = true
						}
						if len(pips) > 0 && !prev[x.Country] {
							x.Flags = append(x.Flags, "new_country")
						}
					}
				}
			}
			var fails int
			a.db.QueryRow(`SELECT COUNT(*) FROM logins WHERE username=? AND ok=0 AND ts BETWEEN ? AND ?`, x.Username, x.TS-3600, x.TS).Scan(&fails)
			if fails >= 3 {
				x.Flags = append(x.Flags, "after_failures")
			}
		}
		out = append(out, x.loginRecord)
	}
	return out
}

type sessionView struct {
	ID       string `json:"id"`
	UserID   int64  `json:"user_id"`
	Username string `json:"username"`
	IP       string `json:"ip"`
	Country  string `json:"country,omitempty"`
	Device   string `json:"device"`
	Method   string `json:"method"`
	Created  int64  `json:"created_at"`
	Seen     int64  `json:"seen_at"`
	Current  bool   `json:"current"`
}

func init() {
	routeHooks = append(routeHooks, func(a *App, mux *http.ServeMux) {
		mux.HandleFunc("GET /api/v1/access/logins", a.authed("audit.view", func(w http.ResponseWriter, r *http.Request) {
			q := r.URL.Query()
			limit, _ := strconv.Atoi(q.Get("limit"))
			if limit <= 0 || limit > 500 {
				limit = 200
			}
			writeJSON(w, 200, map[string]any{"items": a.loginRecords(r.Context(), q.Get("user"), q.Get("failed") == "1", limit)})
		}))
		mux.HandleFunc("GET /api/v1/access/sessions", a.authed("users.manage", func(w http.ResponseWriter, r *http.Request) {
			cur := currentSession(r)
			rows, err := a.db.Query(`SELECT s.token_hash, s.user_id, u.username, s.ip, s.user_agent, s.method, s.created_at, s.seen_at
				FROM sessions s JOIN users u ON u.id = s.user_id WHERE s.stage='full' ORDER BY s.seen_at DESC`)
			if err != nil {
				a.fail(w, r, err)
				return
			}
			out := []sessionView{}
			var ips []string
			for rows.Next() {
				var x sessionView
				var h, ua string
				rows.Scan(&h, &x.UserID, &x.Username, &x.IP, &ua, &x.Method, &x.Created, &x.Seen)
				x.ID, x.Device, x.Current = h[:16], deviceOf(ua), cur != nil && h == cur.hash
				out = append(out, x)
				ips = append(ips, x.IP)
			}
			rows.Close()
			cc := a.countries(r.Context(), ips)
			for i := range out {
				out[i].Country = cc[out[i].IP]
			}
			writeJSON(w, 200, map[string]any{"items": out})
		}))
		mux.HandleFunc("DELETE /api/v1/access/sessions/{id}", a.authed("users.manage", func(w http.ResponseWriter, r *http.Request) {
			id := r.PathValue("id")
			var uname, ip string
			if len(id) != 16 || a.db.QueryRow(`SELECT u.username, s.ip FROM sessions s JOIN users u ON u.id=s.user_id WHERE substr(s.token_hash,1,16)=?`, id).Scan(&uname, &ip) != nil {
				a.fail(w, r, errs.New(404, "session.not_found"))
				return
			}
			if cur := currentSession(r); cur != nil && strings.HasPrefix(cur.hash, id) {
				a.fail(w, r, errs.New(400, "session.not_self"))
				return
			}
			a.db.Exec(`DELETE FROM sessions WHERE substr(token_hash,1,16)=?`, id)
			a.pipe.Record(currentActor(r), "local", "session.kick", map[string]any{"user": uname, "ip": ip}, change.Plan{
				Summary: "plan.session.kick", SummaryP: map[string]any{"user": uname, "ip": ip}, Risk: change.RiskLow})
			writeJSON(w, 200, map[string]bool{"ok": true})
		}))
	})
}
