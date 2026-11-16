package panel

// Alerts (方案 §5.13.2): built-in rules evaluated every 15 s, events that fire and
// resolve once, quiet hours, grouping of messages, delivery to each user's channels.

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/wangsirco/berth/internal/change"
	"github.com/wangsirco/berth/internal/errs"
	"github.com/wangsirco/berth/internal/panel/notify"
)

// ---- rules ----

type alertRule struct {
	Key      string  `json:"key"`
	Category string  `json:"category"` // resources | containers | hosts | certs | backups | sites | security | approvals
	Severity string  `json:"severity"` // info | warn | critical
	Enabled  bool    `json:"enabled"`
	Value    float64 `json:"value,omitempty"`   // threshold, where it applies
	Minutes  int     `json:"minutes,omitempty"` // must hold this long
	Quiet    string  `json:"quiet,omitempty"`   // "22:00-07:00": hold non-critical messages
}

var defaultRules = []alertRule{
	{Key: "mem_high", Category: "resources", Severity: "warn", Enabled: true, Value: 90, Minutes: 5},
	{Key: "disk_high", Category: "resources", Severity: "warn", Enabled: true, Value: 90},
	{Key: "container_down", Category: "containers", Severity: "critical", Enabled: true},
	{Key: "container_restarting", Category: "containers", Severity: "warn", Enabled: true, Value: 3, Minutes: 10},
	{Key: "host_offline", Category: "hosts", Severity: "critical", Enabled: true, Minutes: 2},
	{Key: "cert_expiring", Category: "certs", Severity: "warn", Enabled: true, Value: 14},
	{Key: "domain_expiring", Category: "certs", Severity: "warn", Enabled: true, Value: 30},
	{Key: "backup_failed", Category: "backups", Severity: "critical", Enabled: true},
	{Key: "site_down", Category: "sites", Severity: "critical", Enabled: true},
	{Key: "new_login", Category: "security", Severity: "info", Enabled: true},
	{Key: "ban_added", Category: "security", Severity: "info", Enabled: true},
	{Key: "traffic_quota", Category: "resources", Severity: "warn", Enabled: true},
	{Key: "user_traffic_quota", Category: "resources", Severity: "warn", Enabled: true},
	{Key: "wharf_alert", Category: "router", Severity: "warn", Enabled: true},
	{Key: "pending_approval", Category: "approvals", Severity: "info", Enabled: true},
	{Key: "job_failed", Category: "system", Severity: "warn", Enabled: true},
	{Key: "external_change", Category: "system", Severity: "info", Enabled: true},
	{Key: "virus_found", Category: "security", Severity: "critical", Enabled: true},
}

var alertCategories = []string{"resources", "containers", "hosts", "certs", "backups", "sites", "security", "approvals", "router", "dev", "system"}

func (a *App) alertRules() []alertRule {
	over := map[string]alertRule{}
	rows, err := a.db.Query(`SELECT key, spec FROM alert_rules`)
	if err == nil {
		for rows.Next() {
			var k, spec string
			rows.Scan(&k, &spec)
			var r alertRule
			if json.Unmarshal([]byte(spec), &r) == nil {
				over[k] = r
			}
		}
		rows.Close()
	}
	out := make([]alertRule, 0, len(defaultRules))
	for _, d := range defaultRules {
		r := d
		if o, ok := over[d.Key]; ok {
			r.Enabled, r.Quiet = o.Enabled, o.Quiet
			if o.Value > 0 {
				r.Value = o.Value
			}
			if o.Minutes > 0 {
				r.Minutes = o.Minutes
			}
		}
		out = append(out, r)
	}
	return out
}

func (a *App) alertRule(key string) (alertRule, bool) {
	for _, r := range a.alertRules() {
		if r.Key == key {
			return r, true
		}
	}
	return alertRule{}, false
}

// inQuiet: is t inside "HH:MM-HH:MM" (may wrap midnight)?
func inQuiet(spec string, t time.Time) bool {
	from, to, ok := strings.Cut(spec, "-")
	if !ok {
		return false
	}
	parse := func(s string) int {
		h, m, _ := strings.Cut(strings.TrimSpace(s), ":")
		hh, _ := strconv.Atoi(h)
		mm, _ := strconv.Atoi(m)
		return hh*60 + mm
	}
	f, e, now := parse(from), parse(to), t.Hour()*60+t.Minute()
	if f == e {
		return false
	}
	if f < e {
		return now >= f && now < e
	}
	return now >= f || now < e
}

// ---- events ----

type alertEvent struct {
	ID         int64          `json:"id"`
	Key        string         `json:"key"` // rule:subject
	Rule       string         `json:"rule"`
	Category   string         `json:"category"`
	Severity   string         `json:"severity"`
	Host       string         `json:"host"`
	Params     map[string]any `json:"params"`
	State      string         `json:"state"` // firing | resolved
	Started    int64          `json:"started"`
	ResolvedAt int64          `json:"resolved_at,omitempty"`
	Title      string         `json:"title,omitempty"`
	Message    string         `json:"message,omitempty"`
}

// fire opens an event unless one with the same key is already firing.
func (a *App) fire(rule alertRule, subject, host string, params map[string]any) {
	if !rule.Enabled {
		return
	}
	key := rule.Key + ":" + subject
	var id int64
	if a.db.QueryRow(`SELECT id FROM alert_events WHERE key=? AND state='firing'`, key).Scan(&id) == nil {
		return
	}
	pj, _ := json.Marshal(params)
	res, err := a.db.Exec(`INSERT INTO alert_events(key, rule, category, severity, host, params, state, started) VALUES(?,?,?,?,?,?, 'firing', ?)`,
		key, rule.Key, rule.Category, rule.Severity, host, string(pj), time.Now().Unix())
	if err != nil {
		return
	}
	id, _ = res.LastInsertId()
	ev := alertEvent{ID: id, Key: key, Rule: rule.Key, Category: rule.Category, Severity: rule.Severity, Host: host, Params: params, State: "firing", Started: time.Now().Unix()}
	a.hub.publish(event{Type: "alert", Data: ev})
	a.notifyEvent(rule, ev)
}

// resolve closes a firing event (and says so).
func (a *App) resolve(ruleKey, subject string) {
	key := ruleKey + ":" + subject
	var ev alertEvent
	var pj string
	err := a.db.QueryRow(`SELECT id, rule, category, severity, host, params, started FROM alert_events WHERE key=? AND state='firing'`, key).
		Scan(&ev.ID, &ev.Rule, &ev.Category, &ev.Severity, &ev.Host, &pj, &ev.Started)
	if err != nil {
		return
	}
	json.Unmarshal([]byte(pj), &ev.Params)
	a.db.Exec(`UPDATE alert_events SET state='resolved', resolved_at=? WHERE id=?`, time.Now().Unix(), ev.ID)
	ev.State, ev.Key, ev.ResolvedAt = "resolved", key, time.Now().Unix()
	a.hub.publish(event{Type: "alert", Data: ev})
	if rule, ok := a.alertRule(ruleKey); ok && ruleKey != "new_login" && ruleKey != "backup_failed" && ruleKey != "ban_added" && ruleKey != "traffic_quota" && ruleKey != "user_traffic_quota" && ruleKey != "wharf_alert" {
		a.notifyEvent(rule, ev)
	}
}

// once records an event that has no "resolved" moment (a login, a failed backup).
func (a *App) once(rule alertRule, subject, host string, params map[string]any) {
	a.fire(rule, subject+":"+strconv.FormatInt(time.Now().UnixNano(), 36), host, params)
	a.db.Exec(`UPDATE alert_events SET state='resolved', resolved_at=? WHERE rule=? AND state='firing' AND key LIKE ?`, time.Now().Unix(), rule.Key, rule.Key+":"+subject+":%")
}

// ---- evaluation ----

type alertState struct {
	mu        sync.Mutex
	breach    map[string]int       // consecutive evaluations a condition held
	restarts  map[string][]restart // container restart counts seen
	expected  map[string]time.Time // containers people stopped on purpose (until)
	paused    map[string]bool      // containers a backup paused
	offlineAt map[string]time.Time
}

type restart struct{ at time.Time }

// justStarted: the container is restarting, or came up since the last evaluation.
func justStarted(state, status string) bool {
	if state == "restarting" {
		return true
	}
	if !strings.HasPrefix(status, "Up ") {
		return false
	}
	rest := strings.TrimPrefix(status, "Up ")
	if strings.HasPrefix(rest, "Less than a second") {
		return true
	}
	n, unit, _ := strings.Cut(rest, " ")
	sec, err := strconv.Atoi(n)
	return err == nil && strings.HasPrefix(unit, "second") && sec < int(alertEvery/time.Second)
}

var alertStates sync.Map // *App -> *alertState

func (a *App) alertState() *alertState {
	v, _ := alertStates.LoadOrStore(a, &alertState{breach: map[string]int{}, restarts: map[string][]restart{}, expected: map[string]time.Time{}, paused: map[string]bool{}, offlineAt: map[string]time.Time{}})
	return v.(*alertState)
}

// expectStopped: a person stopped these; do not alert for a while.
func (a *App) expectStopped(host string, names ...string) {
	st := a.alertState()
	st.mu.Lock()
	for _, n := range names {
		st.expected[host+"/"+n] = time.Now().Add(365 * 24 * time.Hour) // until someone starts it again
	}
	st.mu.Unlock()
}

func (a *App) expectRunning(host string, names ...string) {
	st := a.alertState()
	st.mu.Lock()
	for _, n := range names {
		delete(st.expected, host+"/"+n)
	}
	st.mu.Unlock()
}

const alertEvery = 15 * time.Second

func (a *App) alertLoop(ctx context.Context) {
	t := time.NewTicker(alertEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			c, cancel := context.WithTimeout(ctx, 12*time.Second)
			a.evaluate(c)
			cancel()
		}
	}
}

// held counts consecutive breaches and says whether it held for the rule's minutes.
func (st *alertState) held(key string, cond bool, minutes int) bool {
	st.mu.Lock()
	defer st.mu.Unlock()
	if !cond {
		delete(st.breach, key)
		return false
	}
	st.breach[key]++
	need := 2 // two evaluations at least: no alerts for blips
	if minutes > 0 {
		need = max(need, int(time.Duration(minutes)*time.Minute/alertEvery))
	}
	return st.breach[key] >= need
}

func (a *App) evaluate(ctx context.Context) {
	rules := map[string]alertRule{}
	for _, r := range a.alertRules() {
		rules[r.Key] = r
	}
	st := a.alertState()
	hosts := a.connectedHosts()
	connected := map[string]bool{}
	for _, h := range hosts {
		connected[h] = true
	}
	// hosts that dropped off
	for _, rh := range a.remoteHosts() {
		r := rules["host_offline"]
		if connected[rh.Name] {
			st.mu.Lock()
			delete(st.offlineAt, rh.Name)
			st.mu.Unlock()
			a.resolve(r.Key, rh.Name)
			continue
		}
		st.mu.Lock()
		since, ok := st.offlineAt[rh.Name]
		if !ok {
			since = time.Now()
			st.offlineAt[rh.Name] = since
		}
		st.mu.Unlock()
		if time.Since(since) >= time.Duration(r.Minutes)*time.Minute {
			a.fire(r, rh.Name, rh.Name, map[string]any{"host": rh.Name})
		}
	}
	for _, h := range hosts {
		m, at := a.metrics.latestOf(h)
		if !at.IsZero() && time.Since(at) < time.Minute && m.MemTotal > 0 {
			r := rules["mem_high"]
			pct := float64(m.MemTotal-m.MemAvail) * 100 / float64(m.MemTotal)
			if st.held("mem:"+h, pct > r.Value, r.Minutes) {
				a.fire(r, h, h, map[string]any{"host": h, "pct": int(pct)})
			} else if pct < r.Value-5 {
				a.resolve(r.Key, h)
			}
			r = rules["disk_high"]
			for _, d := range m.Disks {
				if d.Total == 0 {
					continue
				}
				pct := float64(d.Used) * 100 / float64(d.Total)
				subj := h + ":" + d.Mount
				if st.held("disk:"+subj, pct > r.Value, r.Minutes) {
					a.fire(r, subj, h, map[string]any{"host": h, "mount": d.Mount, "pct": int(pct)})
				} else if pct < r.Value-2 {
					a.resolve(r.Key, subj)
				}
			}
		}
		a.evaluateContainers(ctx, h, rules, st)
	}
	// certificates
	r := rules["cert_expiring"]
	if certs, err := a.certs(); err == nil {
		for _, c := range certs {
			days := int(time.Until(c.Info.NotAfter).Hours() / 24)
			subj := strconv.FormatInt(c.ID, 10)
			if days < int(r.Value) && (!c.AutoRenew || days < 3) {
				a.fire(r, subj, "local", map[string]any{"name": c.Name, "days": days})
			} else {
				a.resolve(r.Key, subj)
			}
		}
	}
	// domain registrations (RDAP dates cached by 我的域名)
	r = rules["domain_expiring"]
	if rows, err := a.db.Query(`SELECT root, expires FROM domain_rdap WHERE expires > 0`); err == nil {
		type dexp struct {
			root string
			exp  int64
		}
		var ds []dexp
		for rows.Next() {
			var d dexp
			rows.Scan(&d.root, &d.exp)
			ds = append(ds, d)
		}
		rows.Close()
		for _, d := range ds {
			days := int(time.Until(time.Unix(d.exp, 0)).Hours() / 24)
			if days < int(r.Value) {
				a.fire(r, d.root, "local", map[string]any{"name": d.root, "days": days})
			} else {
				a.resolve(r.Key, d.root)
			}
		}
	}
	// approvals waiting
	r = rules["pending_approval"]
	var n int
	a.db.QueryRow(`SELECT COUNT(*) FROM changes WHERE status='pending_approval'`).Scan(&n)
	if n > 0 {
		a.fire(r, "queue", "local", map[string]any{"count": n})
	} else {
		a.resolve(r.Key, "queue")
	}
}

func (a *App) evaluateContainers(ctx context.Context, host string, rules map[string]alertRule, st *alertState) {
	cs, err := a.containers(ctx, host)
	if err != nil {
		return
	}
	down, restarting := rules["container_down"], rules["container_restarting"]
	st.mu.Lock()
	now := time.Now()
	for k, until := range st.expected {
		if now.After(until) {
			delete(st.expected, k)
		}
	}
	st.mu.Unlock()
	for _, c := range cs {
		// only containers Berth manages: projects that were adopted or installed here
		if c.Project == "" || !a.adopted(host, c.Project) {
			continue
		}
		key := host + "/" + c.Name
		st.mu.Lock()
		_, expected := st.expected[key]
		paused := st.paused[key]
		st.mu.Unlock()
		running := c.State == "running"
		if !running && !expected && !paused && a.stoppedOnPurpose(host, c.Name) {
			a.expectStopped(host, c.Name)
			expected = true
		}
		if running || expected || paused {
			st.held("down:"+key, false, 0)
			if running {
				a.resolve(down.Key, key)
			}
		} else if st.held("down:"+key, true, 0) {
			a.fire(down, key, host, map[string]any{"container": c.Name, "host": host, "state": c.State})
		}
		// restart loops: seen restarting (or freshly started) again and again within the window
		st.mu.Lock()
		hist := st.restarts[key]
		if justStarted(c.State, c.Status) {
			hist = append(hist, restart{at: now})
		}
		win := time.Duration(restarting.Minutes) * time.Minute
		for len(hist) > 0 && now.Sub(hist[0].at) > win {
			hist = hist[1:]
		}
		st.restarts[key] = hist
		grew := len(hist)
		st.mu.Unlock()
		if grew >= int(restarting.Value) {
			a.fire(restarting, key, host, map[string]any{"container": c.Name, "host": host, "count": grew, "minutes": restarting.Minutes})
		} else if grew == 0 {
			a.resolve(restarting.Key, key)
		}
	}
}

// ---- delivery ----

type outMsg struct {
	channel int64
	cfg     notify.Config
	msg     notify.Message
	cat     string
}

type dispatcher struct {
	mu      sync.Mutex
	pending map[int64][]outMsg // by channel
	started bool
}

var dispatchers sync.Map

func (a *App) dispatcher() *dispatcher {
	v, _ := dispatchers.LoadOrStore(a, &dispatcher{pending: map[int64][]outMsg{}})
	return v.(*dispatcher)
}

// groupWindow: messages of one channel within this window go out together.
var groupWindow = 8 * time.Second

func (a *App) eventText(ev alertEvent, locale string) (string, string) {
	title := T(locale, "alert."+ev.Rule, ev.Params)
	body := T(locale, "alert."+ev.Rule+".body", ev.Params)
	if ev.State == "resolved" {
		title = T(locale, "alert.resolved", map[string]any{"title": title})
		mins := max(1, int((ev.ResolvedAt-ev.Started+59)/60))
		body = T(locale, "alert.resolved.body", map[string]any{"minutes": mins})
	}
	if ev.Host != "" && ev.Host != "local" {
		body = "[" + ev.Host + "] " + body
	}
	return title, body
}

func (a *App) notifyEvent(rule alertRule, ev alertEvent) {
	if ev.State == "firing" && rule.Severity != "critical" && rule.Quiet != "" && inQuiet(rule.Quiet, time.Now()) {
		a.db.Exec(`UPDATE alert_events SET notified=-1 WHERE id=?`, ev.ID) // held by quiet hours; visible in the panel
		return
	}
	if a.sendToWharf(ev) { // Wharf is the main notifier: its channels send it
		a.db.Exec(`UPDATE alert_events SET notified=2 WHERE id=?`, ev.ID)
		return
	}
	chans, err := a.channelsFor(ev.Category, ev.Host)
	if err != nil || len(chans) == 0 {
		return
	}
	d := a.dispatcher()
	d.mu.Lock()
	for _, c := range chans {
		title, body := a.eventText(ev, c.locale)
		m := notify.Message{Title: title, Body: body, Severity: ev.Severity, Tag: ev.Key, URL: a.publicURL() + "/" + firstNonEmpty(ev.Host, "local") + "/monitor"}
		d.pending[c.id] = append(d.pending[c.id], outMsg{channel: c.id, cfg: c.cfg, msg: m, cat: ev.Category})
	}
	if !d.started {
		d.started = true
		go a.flush(d)
	}
	d.mu.Unlock()
	a.db.Exec(`UPDATE alert_events SET notified=1 WHERE id=?`, ev.ID)
}

func (a *App) flush(d *dispatcher) {
	time.Sleep(groupWindow)
	d.mu.Lock()
	batch := d.pending
	d.pending, d.started = map[int64][]outMsg{}, false
	d.mu.Unlock()
	v := a.vapid()
	for id, msgs := range batch {
		m := msgs[0].msg
		if len(msgs) > 1 { // merged
			var lines []string
			for _, x := range msgs {
				lines = append(lines, "• "+x.msg.Title+" — "+x.msg.Body)
			}
			m = notify.Message{Title: fmt.Sprintf("Berth: %d", len(msgs)) + " · " + msgs[0].msg.Title, Body: strings.Join(lines, "\n"), URL: m.URL, Severity: worst(msgs), Tag: "berth-batch"}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		err := notify.Send(ctx, msgs[0].cfg, v, m)
		cancel()
		if errors.Is(err, notify.ErrGone) {
			a.db.Exec(`DELETE FROM notify_channels WHERE id=?`, id) // the browser dropped it
		} else if err != nil {
			log.Printf("panel: notify channel %d: %v", id, err)
			a.db.Exec(`UPDATE notify_channels SET last_error=? WHERE id=?`, truncate(err.Error(), 300), id)
		} else {
			a.db.Exec(`UPDATE notify_channels SET last_error='', last_sent=? WHERE id=?`, time.Now().Unix(), id)
		}
	}
}

func worst(msgs []outMsg) string {
	rank := map[string]int{"info": 0, "warn": 1, "critical": 2}
	w := "info"
	for _, m := range msgs {
		if rank[m.msg.Severity] > rank[w] {
			w = m.msg.Severity
		}
	}
	return w
}

// ---- channels ----

type notifyChannel struct {
	ID         int64    `json:"id"`
	UserID     int64    `json:"user_id"`
	Kind       string   `json:"kind"`
	Name       string   `json:"name"`
	Categories []string `json:"categories"`
	LastSent   int64    `json:"last_sent,omitempty"`
	LastError  string   `json:"last_error,omitempty"`
	Hint       string   `json:"hint,omitempty"`
	cfg        notify.Config
	locale     string
	id         int64
}

func (a *App) channelsFor(category, host string) ([]notifyChannel, error) {
	rows, err := a.db.Query(`SELECT c.id, c.user_id, c.kind, c.cfg_enc, c.categories, u.locale FROM notify_channels c JOIN users u ON u.id = c.user_id WHERE u.disabled = 0`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []notifyChannel
	for rows.Next() {
		var c notifyChannel
		var enc []byte
		var cats string
		rows.Scan(&c.id, &c.UserID, &c.Kind, &enc, &cats, &c.locale)
		json.Unmarshal([]byte(cats), &c.Categories)
		if !contains(c.Categories, category) {
			continue
		}
		if u, err := a.userByID(c.UserID); err != nil || !(u.Role == RoleSuper || len(u.Scope.Hosts) == 0 || contains(u.Scope.Hosts, host)) {
			continue
		}
		plain, err := a.open(enc)
		if err != nil || json.Unmarshal(plain, &c.cfg) != nil {
			continue
		}
		if c.locale == "" {
			c.locale = "zh-CN"
		}
		out = append(out, c)
	}
	return out, nil
}

func (a *App) userByID(id int64) (*User, error) {
	u, _, err := scanUser(a.db.QueryRow(`SELECT `+userCols+` FROM users WHERE id=?`, id))
	return u, err
}

// vapid returns (creating once) the panel's Web Push keys.
func (a *App) vapid() notify.VAPID {
	pub := a.setting("notify.vapid_public")
	if pub == "" {
		p, priv, err := notify.GenerateVAPID()
		if err != nil {
			return notify.VAPID{}
		}
		enc, err := a.seal([]byte(priv))
		if err != nil {
			return notify.VAPID{}
		}
		a.setSetting("notify.vapid_private", base64.StdEncoding.EncodeToString(enc))
		a.setSetting("notify.vapid_public", p)
		pub = p
	}
	enc, _ := base64.StdEncoding.DecodeString(a.setting("notify.vapid_private"))
	priv, _ := a.open(enc)
	return notify.VAPID{Public: pub, Private: string(priv), Subject: "mailto:alerts@berth.invalid"}
}

// rememberPanelURL keeps the address people reach the panel at, for links in notifications.
func (a *App) rememberPanelURL(r *http.Request) {
	if a.setting("panel.url") == "" && r.Host != "" && !strings.HasPrefix(r.Host, "localhost") && !strings.HasPrefix(r.Host, "127.") {
		a.setSetting("panel.url", panelBase(r))
	}
}

func (a *App) publicURL() string {
	if u := a.setting("panel.url"); u != "" {
		return strings.TrimRight(u, "/")
	}
	return ""
}

func channelHint(c notify.Config) string {
	switch c.Kind {
	case "telegram":
		return "chat " + c.ChatID
	case "email":
		return c.To
	case "webpush":
		var s struct{ Endpoint string }
		json.Unmarshal(c.Subscription, &s)
		if i := strings.Index(s.Endpoint, "://"); i >= 0 {
			host, _, _ := strings.Cut(s.Endpoint[i+3:], "/")
			return host
		}
	case "serverchan", "pushplus":
		if k := strings.TrimSpace(c.Key); len(k) > 8 {
			return k[:4] + "…" + k[len(k)-4:]
		}
	case "wecom", "dingtalk", "feishu":
		host := c.URL
		if i := strings.Index(host, "://"); i >= 0 {
			host = host[i+3:]
		}
		h, _, _ := strings.Cut(host, "/")
		return h
	case "bark", "ntfy", "webhook":
		host := c.URL
		if i := strings.Index(host, "://"); i >= 0 {
			host = host[i+3:]
		}
		h, _, _ := strings.Cut(host, "/")
		return h
	}
	return ""
}

func (a *App) myChannels(uid int64) []notifyChannel {
	rows, err := a.db.Query(`SELECT id, user_id, kind, name, cfg_enc, categories, last_sent, last_error FROM notify_channels WHERE user_id=? ORDER BY id`, uid)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := []notifyChannel{}
	for rows.Next() {
		var c notifyChannel
		var enc []byte
		var cats string
		rows.Scan(&c.ID, &c.UserID, &c.Kind, &c.Name, &enc, &cats, &c.LastSent, &c.LastError)
		json.Unmarshal([]byte(cats), &c.Categories)
		if plain, err := a.open(enc); err == nil {
			var cfg notify.Config
			json.Unmarshal(plain, &cfg)
			c.Hint = channelHint(cfg)
		}
		out = append(out, c)
	}
	return out
}

func validChannel(c notify.Config) error {
	switch c.Kind {
	case "webpush":
		var s struct {
			Endpoint string
			Keys     struct{ P256dh, Auth string }
		}
		if json.Unmarshal(c.Subscription, &s) != nil || !strings.HasPrefix(s.Endpoint, "https://") || s.Keys.P256dh == "" {
			return errs.New(400, "notify.bad_channel")
		}
	case "telegram":
		if c.BotToken == "" || c.ChatID == "" {
			return errs.New(400, "notify.bad_channel")
		}
	case "email":
		if c.SMTPHost == "" || c.From == "" || c.To == "" {
			return errs.New(400, "notify.bad_channel")
		}
	case "serverchan", "pushplus":
		if len(strings.TrimSpace(c.Key)) < 8 {
			return errs.New(400, "notify.bad_channel")
		}
	case "wecom":
		if !strings.HasPrefix(c.URL, "https://qyapi.weixin.qq.com/") {
			return errs.New(400, "notify.bad_channel")
		}
	case "dingtalk":
		if !strings.HasPrefix(c.URL, "https://oapi.dingtalk.com/robot/send?") {
			return errs.New(400, "notify.bad_channel")
		}
	case "feishu":
		if !strings.HasPrefix(c.URL, "https://open.feishu.cn/") && !strings.HasPrefix(c.URL, "https://open.larksuite.com/") {
			return errs.New(400, "notify.bad_channel")
		}
	case "bark", "ntfy", "webhook":
		if !strings.HasPrefix(c.URL, "https://") && !strings.HasPrefix(c.URL, "http://") {
			return errs.New(400, "notify.bad_channel")
		}
	default:
		return errs.New(400, "notify.bad_channel")
	}
	return nil
}

// ---- API ----

func init() {
	routeHooks = append(routeHooks, func(a *App, mux *http.ServeMux) {
		mux.HandleFunc("GET /api/v1/alerts", a.authed("", a.handleAlerts))
		mux.HandleFunc("GET /api/v1/me/channels", a.authed("", func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, 200, map[string]any{"items": a.myChannels(currentUser(r).ID), "categories": alertCategories, "vapid": a.vapid().Public})
		}))
		mux.HandleFunc("POST /api/v1/me/channels", a.authed("", a.handleChannelSave))
		mux.HandleFunc("DELETE /api/v1/me/channels/{id}", a.authed("", func(w http.ResponseWriter, r *http.Request) {
			id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
			a.db.Exec(`DELETE FROM notify_channels WHERE id=? AND user_id=?`, id, currentUser(r).ID)
			writeJSON(w, 200, map[string]bool{"ok": true})
		}))
		mux.HandleFunc("POST /api/v1/me/channels/{id}/test", a.authed("", a.handleChannelTest))
	})

	change.Register(change.Kind{
		Name: "alert.rule.set", Perm: "monitor.manage", Scope: "system:write", Title: "kind.alert.rule.set",
		Params: change.ParamSchema{Props: map[string]change.Prop{"key": {Type: "string"}, "enabled": {Type: "boolean"}, "value": {Type: "number"},
			"minutes": {Type: "integer"}, "quiet": {Type: "string", Desc: "HH:MM-HH:MM"}}, Required: []string{"key"}},
		CLI: "berth alert rule {{.key}}",
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			var p alertRule
			json.Unmarshal(raw, &p)
			if _, ok := svc(env).alertRule(p.Key); !ok {
				return change.Plan{}, errs.New(404, "alert.no_rule", "key", p.Key)
			}
			if p.Quiet != "" && !strings.Contains(p.Quiet, "-") {
				return change.Plan{}, errs.New(400, "alert.bad_quiet")
			}
			return change.Plan{Summary: "plan.alert.rule.set", SummaryP: map[string]any{"rule": p.Key}, Risk: change.RiskLow, Undoable: true}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			a := svc(env)
			var p alertRule
			json.Unmarshal(raw, &p)
			old, _ := a.alertRule(p.Key)
			spec, _ := json.Marshal(p)
			if _, err := a.db.Exec(`INSERT INTO alert_rules(key, spec) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET spec=excluded.spec`, p.Key, string(spec)); err != nil {
				return nil, nil, err
			}
			u, _ := json.Marshal(old)
			return nil, &change.UndoRecipe{Kind: "alert.rule.set", Params: u}, nil
		},
	})
}

func (a *App) handleAlerts(w http.ResponseWriter, r *http.Request) {
	q := `SELECT id, key, rule, category, severity, host, params, state, started, resolved_at FROM alert_events ORDER BY (state='firing') DESC, id DESC LIMIT 200`
	rows, err := a.db.Query(q)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	defer rows.Close()
	u := currentUser(r)
	out := []alertEvent{}
	for rows.Next() {
		var ev alertEvent
		var pj string
		var res sql.NullInt64
		rows.Scan(&ev.ID, &ev.Key, &ev.Rule, &ev.Category, &ev.Severity, &ev.Host, &pj, &ev.State, &ev.Started, &res)
		ev.ResolvedAt = res.Int64
		if !(u.Role == RoleSuper || len(u.Scope.Hosts) == 0 || contains(u.Scope.Hosts, ev.Host)) {
			continue
		}
		json.Unmarshal([]byte(pj), &ev.Params)
		ev.Title, ev.Message = a.eventText(alertEvent{Rule: ev.Rule, Params: ev.Params, Host: ev.Host, State: "firing"}, a.locale(r))
		out = append(out, ev)
	}
	writeJSON(w, 200, map[string]any{"items": out, "rules": a.alertRules(), "categories": alertCategories})
}

func (a *App) handleChannelSave(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ID         int64         `json:"id"`
		Name       string        `json:"name"`
		Categories []string      `json:"categories"`
		Config     notify.Config `json:"config"`
	}
	if err := readJSON(r, &in); err != nil {
		a.fail(w, r, err)
		return
	}
	u := currentUser(r)
	a.rememberPanelURL(r)
	for _, c := range in.Categories {
		if !contains(alertCategories, c) {
			a.fail(w, r, errs.New(400, "notify.bad_channel"))
			return
		}
	}
	if in.ID != 0 && in.Config.Kind == "" { // only names/categories changed
		cats, _ := json.Marshal(in.Categories)
		a.db.Exec(`UPDATE notify_channels SET name=?, categories=? WHERE id=? AND user_id=?`, in.Name, string(cats), in.ID, u.ID)
		writeJSON(w, 200, map[string]int64{"id": in.ID})
		return
	}
	if err := validChannel(in.Config); err != nil {
		a.fail(w, r, err)
		return
	}
	if in.Config.Kind == "webpush" { // one subscription per browser: replace an earlier one
		rows, _ := a.db.Query(`SELECT id, cfg_enc FROM notify_channels WHERE user_id=? AND kind='webpush'`, u.ID)
		var dup []int64
		for rows != nil && rows.Next() {
			var id int64
			var enc []byte
			rows.Scan(&id, &enc)
			if p, err := a.open(enc); err == nil {
				var c notify.Config
				json.Unmarshal(p, &c)
				if channelHint(c) != "" && string(c.Subscription) == string(in.Config.Subscription) {
					dup = append(dup, id)
				}
			}
		}
		if rows != nil {
			rows.Close()
		}
		for _, id := range dup {
			a.db.Exec(`DELETE FROM notify_channels WHERE id=?`, id)
		}
	}
	b, _ := json.Marshal(in.Config)
	enc, err := a.seal(b)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if len(in.Categories) == 0 {
		in.Categories = alertCategories
	}
	cats, _ := json.Marshal(in.Categories)
	name := firstNonEmpty(in.Name, in.Config.Kind)
	if in.ID != 0 {
		a.db.Exec(`UPDATE notify_channels SET kind=?, name=?, cfg_enc=?, categories=? WHERE id=? AND user_id=?`, in.Config.Kind, name, enc, string(cats), in.ID, u.ID)
		writeJSON(w, 200, map[string]int64{"id": in.ID})
		return
	}
	res, err := a.db.Exec(`INSERT INTO notify_channels(user_id, kind, name, cfg_enc, categories, created_at) VALUES(?,?,?,?,?,?)`, u.ID, in.Config.Kind, name, enc, string(cats), time.Now().Unix())
	if err != nil {
		a.fail(w, r, err)
		return
	}
	id, _ := res.LastInsertId()
	_, _ = a.pipe.Record(currentActor(r), "local", "notify.channel_add", map[string]any{"kind": in.Config.Kind, "name": name}, change.Plan{
		Summary: "plan.notify.channel_add", SummaryP: map[string]any{"kind": in.Config.Kind, "name": name}, Risk: change.RiskLow})
	writeJSON(w, 200, map[string]int64{"id": id})
}

func (a *App) handleChannelTest(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	var enc []byte
	if err := a.db.QueryRow(`SELECT cfg_enc FROM notify_channels WHERE id=? AND user_id=?`, id, currentUser(r).ID).Scan(&enc); err != nil {
		a.fail(w, r, errs.New(404, "notify.not_found"))
		return
	}
	plain, err := a.open(enc)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	var c notify.Config
	json.Unmarshal(plain, &c)
	a.rememberPanelURL(r)
	loc := a.locale(r)
	err = notify.Send(r.Context(), c, a.vapid(), notify.Message{Title: T(loc, "notify.test_title", nil), Body: T(loc, "notify.test_body", nil), Severity: "info", Tag: "berth-test"})
	if err != nil {
		a.fail(w, r, errs.New(502, "notify.send_failed", "detail", err.Error()))
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// ---- hooks ----

func init() {
	backupFailedHook = func(a *App, job int64, kind string, err error) {
		r, _ := a.alertRule("backup_failed")
		name := ""
		host := "local"
		a.db.QueryRow(`SELECT name, host FROM backup_jobs WHERE id=?`, job).Scan(&name, &host)
		a.once(r, strconv.FormatInt(job, 10), host, map[string]any{"job": name, "kind": kind, "error": truncate(storeErr(err), 200)})
	}
}

func (a *App) markPaused(host string, names []string, on bool) {
	st := a.alertState()
	st.mu.Lock()
	for _, n := range names {
		if on {
			st.paused[host+"/"+n] = true
		} else {
			delete(st.paused, host+"/"+n)
		}
	}
	st.mu.Unlock()
}

// loginAlert: a successful sign-in from an address this user has not signed in from before.
func (a *App) loginAlert(username string, uid int64, ip, method string) {
	if uid == 0 || method == "setup" {
		return
	}
	var n int
	a.db.QueryRow(`SELECT COUNT(*) FROM logins WHERE user_id=? AND ok=1 AND ip=?`, uid, ip).Scan(&n)
	if n > 1 { // this one is already recorded
		return
	}
	r, _ := a.alertRule("new_login")
	a.once(r, strconv.FormatInt(uid, 10), "local", map[string]any{"user": username, "ip": ip, "method": method})
}

// alertAfterChange remembers containers people stopped or started on purpose.
func (a *App) alertAfterChange(c *change.Change) {
	if c.Status != "done" {
		return
	}
	var p struct {
		Name string `json:"name"`
	}
	json.Unmarshal(c.Params, &p)
	if p.Name == "" {
		return
	}
	switch c.Kind {
	case "container.stop":
		a.expectStopped(c.Host, p.Name)
	case "container.start", "container.restart":
		a.expectRunning(c.Host, p.Name)
	}
}

// stoppedOnPurpose survives panel restarts: the last start/stop change for the container.
func (a *App) stoppedOnPurpose(host, name string) bool {
	var kind string
	err := a.db.QueryRow(`SELECT kind FROM changes WHERE host=? AND status='done' AND kind IN ('container.stop','container.start','container.restart')
		AND json_extract(params,'$.name')=? ORDER BY ts DESC LIMIT 1`, host, name).Scan(&kind)
	return err == nil && kind == "container.stop"
}
