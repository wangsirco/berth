package panel

// Uptime checks (方案 §5.13.4): HTTP(S) and TCP probes run by the panel, 90 days of daily
// history, "site down" alerts, and public status pages at /status/{slug}.

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/wangsirco/berth/internal/change"
	"github.com/wangsirco/berth/internal/errs"
)

type uptimeCheck struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Kind     string `json:"kind"`   // http | tcp
	Target   string `json:"target"` // URL or host:port
	Interval int    `json:"interval"`
	Expect   int    `json:"expect,omitempty"`  // HTTP status; 0 = any 2xx/3xx
	Keyword  string `json:"keyword,omitempty"` // must appear in the body
	Insecure bool   `json:"insecure,omitempty"`
	From     string `json:"from,omitempty"` // a managed host to check from ("" = the panel itself)

	Up      *bool   `json:"up,omitempty"`
	LastMS  int     `json:"last_ms,omitempty"`
	LastErr string  `json:"last_error,omitempty"`
	LastAt  int64   `json:"last_at,omitempty"`
	Uptime  float64 `json:"uptime_30d"`
	Days    []dayUp `json:"days,omitempty"`
}

type dayUp struct {
	Day   string  `json:"day"`
	Ratio float64 `json:"ratio"` // -1: no data
	AvgMS int     `json:"avg_ms"`
}

var slugRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,40}$`)

func validCheck(c uptimeCheck) error {
	if strings.TrimSpace(c.Name) == "" || len(c.Name) > 80 {
		return errs.New(400, "uptime.bad_check")
	}
	switch c.Kind {
	case "http":
		if !strings.HasPrefix(c.Target, "http://") && !strings.HasPrefix(c.Target, "https://") {
			return errs.New(400, "uptime.bad_check")
		}
	case "tcp":
		if _, p, err := net.SplitHostPort(c.Target); err != nil || p == "" {
			return errs.New(400, "uptime.bad_check")
		}
	default:
		return errs.New(400, "uptime.bad_check")
	}
	if c.Interval != 0 && (c.Interval < 30 || c.Interval > 3600) {
		return errs.New(400, "uptime.bad_check")
	}
	return nil
}

var probeClient = func(insecure bool) *http.Client {
	return &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: insecure}, DisableKeepAlives: true}}
}

// probe runs one check: ok, latency, error text.
func probe(ctx context.Context, c uptimeCheck) (bool, int, string) {
	start := time.Now()
	ms := func() int { return int(time.Since(start).Milliseconds()) }
	if c.Kind == "tcp" {
		conn, err := (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, "tcp", c.Target)
		if err != nil {
			return false, ms(), shortErr(err)
		}
		conn.Close()
		return true, ms(), ""
	}
	req, err := http.NewRequestWithContext(ctx, "GET", c.Target, nil)
	if err != nil {
		return false, 0, shortErr(err)
	}
	req.Header.Set("User-Agent", "Berth-Uptime")
	resp, err := probeClient(c.Insecure).Do(req)
	if err != nil {
		return false, ms(), shortErr(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	d := ms()
	if c.Expect != 0 && resp.StatusCode != c.Expect || c.Expect == 0 && resp.StatusCode >= 400 {
		return false, d, "HTTP " + strconv.Itoa(resp.StatusCode)
	}
	if c.Keyword != "" && !strings.Contains(string(body), c.Keyword) {
		return false, d, "keyword missing"
	}
	return true, d, ""
}

// probeFrom runs the check on another managed host (an outside view of the site). reached
// is false when that host could not be asked.
func (a *App) probeFrom(ctx context.Context, c uptimeCheck) (ok bool, ms int, msg string, reached bool) {
	var out struct {
		MS     int    `json:"ms"`
		Status int    `json:"status"`
		Error  string `json:"error"`
	}
	path, body := "/v1/probe/http", map[string]any{"url": c.Target}
	if c.Kind == "tcp" {
		path, body = "/v1/probe/tcp", map[string]any{"addr": c.Target}
	}
	if err := a.call(ctx, c.From, "POST", path, body, &out); err != nil {
		return false, 0, "", false
	}
	if out.Error != "" {
		return false, out.MS, truncate(out.Error, 160), true
	}
	if c.Kind == "http" && (c.Expect != 0 && out.Status != c.Expect || c.Expect == 0 && out.Status >= 400) {
		return false, out.MS, "HTTP " + strconv.Itoa(out.Status), true
	}
	return true, out.MS, "", true
}

func shortErr(err error) string {
	s := err.Error()
	if i := strings.LastIndex(s, ": "); i >= 0 && len(s) > 80 {
		s = s[i+2:]
	}
	return truncate(s, 160)
}

// ---- loop ----

type uptimeRunner struct {
	mu   sync.Mutex
	next map[int64]time.Time
	fail map[int64]int
}

var uptimeRunners sync.Map

func (a *App) uptimeRunner() *uptimeRunner {
	v, _ := uptimeRunners.LoadOrStore(a, &uptimeRunner{next: map[int64]time.Time{}, fail: map[int64]int{}})
	return v.(*uptimeRunner)
}

func (a *App) uptimeLoop(ctx context.Context) {
	t := time.NewTicker(10 * time.Second)
	defer t.Stop()
	lastPrune := time.Time{}
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			a.runDueChecks(ctx, now)
			if now.Sub(lastPrune) > time.Hour {
				lastPrune = now
				a.db.Exec(`DELETE FROM uptime_results WHERE ts < ?`, now.Add(-48*time.Hour).Unix())
				a.db.Exec(`DELETE FROM uptime_daily WHERE day < ?`, now.AddDate(0, 0, -90).Format("2006-01-02"))
			}
		}
	}
}

func (a *App) runDueChecks(ctx context.Context, now time.Time) {
	checks, _ := a.uptimeChecks(false)
	ur := a.uptimeRunner()
	var wg sync.WaitGroup
	for _, c := range checks {
		ur.mu.Lock()
		due := !now.Before(ur.next[c.ID])
		if due {
			ur.next[c.ID] = now.Add(time.Duration(max(c.Interval, 30)) * time.Second)
		}
		ur.mu.Unlock()
		if !due {
			continue
		}
		wg.Add(1)
		go func(c uptimeCheck) {
			defer wg.Done()
			cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
			defer cancel()
			if c.From != "" && c.From != "local" {
				ok, ms, msg, reached := a.probeFrom(cctx, c)
				if !reached { // the vantage point itself is offline: that is the host alert, not this site's
					return
				}
				a.recordProbe(c, now)(ok, ms, msg)
				return
			}
			a.recordProbe(c, now)(probe(cctx, c))
		}(c)
	}
	wg.Wait()
}

func (a *App) recordProbe(c uptimeCheck, now time.Time) func(bool, int, string) {
	return func(ok bool, ms int, msg string) {
		a.db.Exec(`INSERT INTO uptime_results(check_id, ts, ok, ms, error) VALUES(?,?,?,?,?)`, c.ID, now.Unix(), ok, ms, msg)
		day := now.Format("2006-01-02")
		up := 0
		if ok {
			up = 1
		}
		a.db.Exec(`INSERT INTO uptime_daily(check_id, day, up, total, ms_sum) VALUES(?,?,?,1,?)
			ON CONFLICT(check_id, day) DO UPDATE SET up=up+excluded.up, total=total+1, ms_sum=ms_sum+excluded.ms_sum`, c.ID, day, up, ms)
		ur := a.uptimeRunner()
		ur.mu.Lock()
		if ok {
			ur.fail[c.ID] = 0
		} else {
			ur.fail[c.ID]++
		}
		fails := ur.fail[c.ID]
		ur.mu.Unlock()
		subj := strconv.FormatInt(c.ID, 10)
		r, _ := a.alertRule("site_down")
		if ok {
			a.resolve(r.Key, subj)
		} else if fails >= 2 { // two misses in a row
			a.fire(r, subj, "local", map[string]any{"name": c.Name, "url": c.Target, "error": msg})
		}
		a.hub.publish(event{Type: "uptime", Data: map[string]any{"id": c.ID, "ok": ok, "ms": ms}})
	}
}

// ---- reads ----

func (a *App) uptimeChecks(withStats bool) ([]uptimeCheck, error) {
	rows, err := a.db.Query(`SELECT id, spec FROM uptime_checks ORDER BY id`)
	if err != nil {
		return nil, err
	}
	var out []uptimeCheck
	for rows.Next() {
		var id int64
		var spec string
		rows.Scan(&id, &spec)
		var c uptimeCheck
		json.Unmarshal([]byte(spec), &c)
		c.ID = id
		out = append(out, c)
	}
	rows.Close()
	if withStats {
		for i := range out {
			a.fillStats(&out[i], 30)
		}
	}
	return out, nil
}

func (a *App) fillStats(c *uptimeCheck, days int) {
	var ok bool
	var at int64
	if a.db.QueryRow(`SELECT ok, ms, error, ts FROM uptime_results WHERE check_id=? ORDER BY ts DESC LIMIT 1`, c.ID).Scan(&ok, &c.LastMS, &c.LastErr, &at) == nil {
		c.Up, c.LastAt = &ok, at
	}
	byDay := map[string]dayUp{}
	var up, total int
	rows, err := a.db.Query(`SELECT day, up, total, ms_sum FROM uptime_daily WHERE check_id=? AND day >= ?`, c.ID, time.Now().AddDate(0, 0, -days+1).Format("2006-01-02"))
	if err == nil {
		for rows.Next() {
			var d string
			var u, t, ms int
			rows.Scan(&d, &u, &t, &ms)
			if t > 0 {
				byDay[d] = dayUp{Day: d, Ratio: float64(u) / float64(t), AvgMS: ms / t}
				up, total = up+u, total+t
			}
		}
		rows.Close()
	}
	c.Uptime = -1
	if total > 0 {
		c.Uptime = float64(up) * 100 / float64(total)
	}
	c.Days = make([]dayUp, 0, days)
	for i := days - 1; i >= 0; i-- {
		d := time.Now().AddDate(0, 0, -i).Format("2006-01-02")
		if v, ok := byDay[d]; ok {
			c.Days = append(c.Days, v)
		} else {
			c.Days = append(c.Days, dayUp{Day: d, Ratio: -1})
		}
	}
}

type statusPage struct {
	Slug   string  `json:"slug"`
	Title  string  `json:"title"`
	Checks []int64 `json:"checks"`
	Note   string  `json:"note,omitempty"`
}

func (a *App) statusPages() []statusPage {
	out := []statusPage{}
	rows, err := a.db.Query(`SELECT slug, spec FROM status_pages ORDER BY slug`)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var slug, spec string
		rows.Scan(&slug, &spec)
		var p statusPage
		json.Unmarshal([]byte(spec), &p)
		p.Slug = slug
		out = append(out, p)
	}
	return out
}

// ---- API ----

func init() {
	routeHooks = append(routeHooks, func(a *App, mux *http.ServeMux) {
		mux.HandleFunc("GET /api/v1/uptime", a.authed("", func(w http.ResponseWriter, r *http.Request) {
			cs, err := a.uptimeChecks(true)
			if err != nil {
				a.fail(w, r, err)
				return
			}
			if cs == nil {
				cs = []uptimeCheck{}
			}
			writeJSON(w, 200, map[string]any{"items": cs, "pages": a.statusPages()})
		}))
		mux.HandleFunc("GET /api/v1/uptime/{id}/recent", a.authed("", func(w http.ResponseWriter, r *http.Request) {
			id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
			rows, err := a.db.Query(`SELECT ts, ok, ms, error FROM uptime_results WHERE check_id=? AND ts > ? ORDER BY ts`, id, time.Now().Add(-24*time.Hour).Unix())
			if err != nil {
				a.fail(w, r, err)
				return
			}
			defer rows.Close()
			out := []map[string]any{}
			for rows.Next() {
				var ts int64
				var ok bool
				var ms int
				var e string
				rows.Scan(&ts, &ok, &ms, &e)
				out = append(out, map[string]any{"ts": ts, "ok": ok, "ms": ms, "error": e})
			}
			writeJSON(w, 200, map[string]any{"items": out})
		}))
		mux.HandleFunc("GET /status/{slug}", a.handleStatusPage)
		mux.HandleFunc("GET /status/{slug}/json", a.handleStatusPage)
	})

	checkProps := map[string]change.Prop{"id": {Type: "integer"}, "name": {Type: "string"}, "kind": {Type: "string", Enum: []string{"http", "tcp"}},
		"target": {Type: "string"}, "interval": {Type: "integer"}, "expect": {Type: "integer"}, "keyword": {Type: "string"}, "insecure": {Type: "boolean"},
		"from": {Type: "string", Desc: "managed host to check from (default: the panel)"}}
	change.Register(change.Kind{
		Name: "uptime.set", Perm: "monitor.manage", Scope: "system:write", Title: "kind.uptime.set",
		Params: change.ParamSchema{Props: checkProps, Required: []string{"name", "kind", "target"}}, CLI: "berth uptime set {{.name}}",
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			var c uptimeCheck
			json.Unmarshal(raw, &c)
			if err := validCheck(c); err != nil {
				return change.Plan{}, err
			}
			return change.Plan{Summary: "plan.uptime.set", SummaryP: map[string]any{"name": c.Name, "target": c.Target}, Risk: change.RiskLow, Undoable: true}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			a := svc(env)
			var c uptimeCheck
			json.Unmarshal(raw, &c)
			if c.Interval == 0 {
				c.Interval = 60
			}
			spec, _ := json.Marshal(uptimeCheck{Name: c.Name, Kind: c.Kind, Target: c.Target, Interval: c.Interval, Expect: c.Expect, Keyword: c.Keyword, Insecure: c.Insecure, From: c.From})
			if c.ID != 0 {
				var old string
				if a.db.QueryRow(`SELECT spec FROM uptime_checks WHERE id=?`, c.ID).Scan(&old) != nil {
					// restoring a deleted check keeps its id
					if _, err := a.db.Exec(`INSERT INTO uptime_checks(id, spec, created_at) VALUES(?,?,?)`, c.ID, string(spec), time.Now().Unix()); err != nil {
						return nil, nil, err
					}
					u, _ := json.Marshal(map[string]int64{"id": c.ID})
					return map[string]int64{"id": c.ID}, &change.UndoRecipe{Kind: "uptime.delete", Params: u}, nil
				}
				a.db.Exec(`UPDATE uptime_checks SET spec=? WHERE id=?`, string(spec), c.ID)
				var prev uptimeCheck
				json.Unmarshal([]byte(old), &prev)
				prev.ID = c.ID
				u, _ := json.Marshal(prev)
				return map[string]int64{"id": c.ID}, &change.UndoRecipe{Kind: "uptime.set", Params: u}, nil
			}
			res, err := a.db.Exec(`INSERT INTO uptime_checks(spec, created_at) VALUES(?,?)`, string(spec), time.Now().Unix())
			if err != nil {
				return nil, nil, err
			}
			id, _ := res.LastInsertId()
			u, _ := json.Marshal(map[string]int64{"id": id})
			return map[string]int64{"id": id}, &change.UndoRecipe{Kind: "uptime.delete", Params: u}, nil
		},
	})
	change.Register(change.Kind{
		Name: "uptime.delete", Perm: "monitor.manage", Scope: "system:write", Title: "kind.uptime.delete",
		Params: change.ParamSchema{Props: map[string]change.Prop{"id": {Type: "integer"}}, Required: []string{"id"}}, CLI: "berth uptime delete {{.id}}",
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			var p struct{ ID int64 }
			json.Unmarshal(raw, &p)
			var spec string
			if svc(env).db.QueryRow(`SELECT spec FROM uptime_checks WHERE id=?`, p.ID).Scan(&spec) != nil {
				return change.Plan{}, errs.New(404, "uptime.not_found")
			}
			var c uptimeCheck
			json.Unmarshal([]byte(spec), &c)
			return change.Plan{Summary: "plan.uptime.delete", SummaryP: map[string]any{"name": c.Name}, Risk: change.RiskLow, Undoable: true}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			a := svc(env)
			var p struct{ ID int64 }
			json.Unmarshal(raw, &p)
			var spec string
			a.db.QueryRow(`SELECT spec FROM uptime_checks WHERE id=?`, p.ID).Scan(&spec)
			a.db.Exec(`DELETE FROM uptime_checks WHERE id=?`, p.ID) // history stays until it ages out
			a.resolve("site_down", strconv.FormatInt(p.ID, 10))
			var c uptimeCheck
			json.Unmarshal([]byte(spec), &c)
			c.ID = p.ID
			u, _ := json.Marshal(c)
			return nil, &change.UndoRecipe{Kind: "uptime.set", Params: u}, nil
		},
	})
	change.Register(change.Kind{
		Name: "statuspage.set", Perm: "monitor.manage", Scope: "system:write", Title: "kind.statuspage.set",
		Params: change.ParamSchema{Props: map[string]change.Prop{"slug": {Type: "string"}, "title": {Type: "string"}, "checks": {Type: "array", Desc: "uptime check ids"},
			"note": {Type: "string"}}, Required: []string{"slug", "title"}},
		CLI: "berth statuspage set {{.slug}}",
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			var p statusPage
			json.Unmarshal(raw, &p)
			if !slugRe.MatchString(p.Slug) || strings.TrimSpace(p.Title) == "" {
				return change.Plan{}, errs.New(400, "uptime.bad_page")
			}
			return change.Plan{Summary: "plan.statuspage.set", SummaryP: map[string]any{"slug": p.Slug, "count": len(p.Checks)}, Risk: change.RiskMedium, Undoable: true,
				Commands: []string{"# public: /status/" + p.Slug + " (names and up/down only)"}}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			a := svc(env)
			var p statusPage
			json.Unmarshal(raw, &p)
			var old string
			had := a.db.QueryRow(`SELECT spec FROM status_pages WHERE slug=?`, p.Slug).Scan(&old) == nil
			spec, _ := json.Marshal(statusPage{Title: p.Title, Checks: p.Checks, Note: p.Note})
			if _, err := a.db.Exec(`INSERT INTO status_pages(slug, spec) VALUES(?,?) ON CONFLICT(slug) DO UPDATE SET spec=excluded.spec`, p.Slug, string(spec)); err != nil {
				return nil, nil, err
			}
			if !had {
				u, _ := json.Marshal(map[string]string{"slug": p.Slug})
				return map[string]string{"url": "/status/" + p.Slug}, &change.UndoRecipe{Kind: "statuspage.delete", Params: u}, nil
			}
			var prev statusPage
			json.Unmarshal([]byte(old), &prev)
			prev.Slug = p.Slug
			u, _ := json.Marshal(prev)
			return map[string]string{"url": "/status/" + p.Slug}, &change.UndoRecipe{Kind: "statuspage.set", Params: u}, nil
		},
	})
	change.Register(change.Kind{
		Name: "statuspage.delete", Perm: "monitor.manage", Scope: "system:write", Title: "kind.statuspage.delete",
		Params: change.ParamSchema{Props: map[string]change.Prop{"slug": {Type: "string"}}, Required: []string{"slug"}}, CLI: "berth statuspage delete {{.slug}}",
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			var p statusPage
			json.Unmarshal(raw, &p)
			return change.Plan{Summary: "plan.statuspage.delete", SummaryP: map[string]any{"slug": p.Slug}, Risk: change.RiskLow, Undoable: true}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			a := svc(env)
			var p statusPage
			json.Unmarshal(raw, &p)
			var old string
			if a.db.QueryRow(`SELECT spec FROM status_pages WHERE slug=?`, p.Slug).Scan(&old) != nil {
				return nil, nil, errs.New(404, "uptime.not_found")
			}
			a.db.Exec(`DELETE FROM status_pages WHERE slug=?`, p.Slug)
			var prev statusPage
			json.Unmarshal([]byte(old), &prev)
			prev.Slug = p.Slug
			u, _ := json.Marshal(prev)
			return nil, &change.UndoRecipe{Kind: "statuspage.set", Params: u}, nil
		},
	})
}

// ---- public status page ----

type publicCheck struct {
	Name   string  `json:"name"`
	Up     *bool   `json:"up"`
	Uptime float64 `json:"uptime_90d"`
	Days   []dayUp `json:"days"`
}

func (a *App) handleStatusPage(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	var spec string
	if !slugRe.MatchString(slug) || a.db.QueryRow(`SELECT spec FROM status_pages WHERE slug=?`, slug).Scan(&spec) != nil {
		blank404(w)
		return
	}
	var p statusPage
	json.Unmarshal([]byte(spec), &p)
	all, _ := a.uptimeChecks(false)
	byID := map[int64]uptimeCheck{}
	for _, c := range all {
		byID[c.ID] = c
	}
	var checks []publicCheck // only names and up/down: never targets or errors
	allUp := true
	for _, id := range p.Checks {
		c, ok := byID[id]
		if !ok {
			continue
		}
		a.fillStats(&c, 90)
		if c.Up != nil && !*c.Up {
			allUp = false
		}
		checks = append(checks, publicCheck{Name: c.Name, Up: c.Up, Uptime: c.Uptime, Days: c.Days})
	}
	w.Header().Set("Cache-Control", "public, max-age=30")
	if strings.HasSuffix(r.URL.Path, "/json") {
		writeJSON(w, 200, map[string]any{"title": p.Title, "note": p.Note, "all_up": allUp, "checks": checks, "updated": time.Now().Unix()})
		return
	}
	loc := "en"
	if al := r.Header.Get("Accept-Language"); strings.HasPrefix(al, "zh") {
		loc = "zh-CN"
		if strings.Contains(strings.ToLower(al[:min(len(al), 8)]), "tw") || strings.Contains(strings.ToLower(al[:min(len(al), 8)]), "hk") {
			loc = "zh-TW"
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'")
	statusTpl.Execute(w, map[string]any{
		"Title": p.Title, "Note": p.Note, "AllUp": allUp, "Checks": checks, "Lang": loc,
		"T": func(k string) string { return T(loc, k, nil) },
		"Pct": func(f float64) string {
			if f < 0 {
				return "—"
			}
			return fmt.Sprintf("%.2f%%", f)
		},
		"Color": func(d dayUp) string {
			switch {
			case d.Ratio < 0:
				return "#d4d4d8"
			case d.Ratio >= 0.999:
				return "#22c55e"
			case d.Ratio >= 0.95:
				return "#eab308"
			}
			return "#ef4444"
		},
		"Updated": time.Now().Format("2006-01-02 15:04"),
	})
}

var statusTpl = template.Must(template.New("status").Funcs(template.FuncMap{"deref": func(b *bool) bool { return b != nil && *b }}).Parse(`<!doctype html>
<html lang="{{.Lang}}"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<meta http-equiv="refresh" content="60"><title>{{.Title}}</title>
<style>
body{font:15px/1.5 system-ui,-apple-system,"PingFang SC","Microsoft YaHei",sans-serif;margin:0;background:#fafafa;color:#18181b}
main{max-width:760px;margin:0 auto;padding:40px 20px}
h1{font-size:24px;margin:0 0 16px}
.banner{padding:14px 18px;border-radius:10px;color:#fff;font-weight:600;margin-bottom:24px}
.ok{background:#16a34a}.bad{background:#dc2626}
.card{background:#fff;border:1px solid #e4e4e7;border-radius:10px;padding:16px 18px;margin-bottom:12px}
.row{display:flex;justify-content:space-between;align-items:center;gap:12px}
.dot{display:inline-block;width:10px;height:10px;border-radius:50%;margin-right:8px}
.bars{display:flex;gap:2px;margin-top:10px;height:28px}
.bars i{flex:1;border-radius:2px}
.muted{color:#71717a;font-size:13px}
@media (prefers-color-scheme:dark){body{background:#09090b;color:#fafafa}.card{background:#18181b;border-color:#27272a}}
</style></head><body><main>
<h1>{{.Title}}</h1>
{{if .AllUp}}<div class="banner ok">{{call .T "status.all_up"}}</div>{{else}}<div class="banner bad">{{call .T "status.some_down"}}</div>{{end}}
{{if .Note}}<p>{{.Note}}</p>{{end}}
{{range .Checks}}<div class="card"><div class="row"><div>
{{if .Up}}{{if deref .Up}}<span class="dot" style="background:#22c55e"></span>{{else}}<span class="dot" style="background:#ef4444"></span>{{end}}{{else}}<span class="dot" style="background:#d4d4d8"></span>{{end}}<b>{{.Name}}</b></div>
<span class="muted">{{call $.Pct .Uptime}}</span></div>
<div class="bars">{{range .Days}}<i style="background:{{call $.Color .}}" title="{{.Day}}"></i>{{end}}</div></div>
{{end}}
<p class="muted">{{call .T "status.updated"}} {{.Updated}} · Berth</p>
</main></body></html>`))
