package panel

// Firewall (方案 §5.12.1): an ordered rule list per host, applied as a whole to the agent's
// `inet berth` table. Every change lives for 60 s unless someone confirms it.

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/wangsirco/berth/internal/agent/firewall"
	"github.com/wangsirco/berth/internal/change"
	"github.com/wangsirco/berth/internal/errs"
)

type fwRule = firewall.Rule

// fwMu orders applying, confirming, rolling back and reconciling, so a status read that
// lands between "agent confirmed" and "panel recorded it" cannot look like a rollback.
var fwMu sync.Mutex

func (a *App) fwRules(host string) []fwRule {
	rows, err := a.db.Query(`SELECT id, spec FROM fw_rules WHERE host=? ORDER BY position, id`, host)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := []fwRule{}
	for rows.Next() {
		var id int64
		var spec string
		rows.Scan(&id, &spec)
		var r fwRule
		json.Unmarshal([]byte(spec), &r)
		r.ID = id
		out = append(out, r)
	}
	return out
}

// fwManaged: firewalld entries Berth opened earlier on this host (so it may close them).
func (a *App) fwManaged(host string, rules []fwRule) []string {
	var out []string
	if v := a.setting("firewall.managed." + host); v != "" {
		json.Unmarshal([]byte(v), &out)
	}
	return out
}

func (a *App) fwDesired(host string, rules []fwRule) firewall.Desired {
	return firewall.Desired{Rules: rules, Sets: firewall.Sets{}, Managed: a.fwManaged(host, rules)}
}

// fwApply sends the whole list; the agent arms the 60 s rollback.
func (a *App) fwApply(ctx context.Context, host string, rules []fwRule) error {
	fwMu.Lock()
	defer fwMu.Unlock()
	var res struct {
		Plan firewall.Plan `json:"plan"`
	}
	if err := a.call(ctx, host, "POST", "/v1/firewall/apply", a.fwDesired(host, rules), &res); err != nil {
		return err
	}
	a.setSetting("firewall.dirty."+host, "1") // until confirmed; "firewall.confirmed.<host>" holds the list in force
	// remember what Berth opened in firewalld
	ports, rich := firewall.FirewalldPorts(rules)
	keep := append(ports, rich...)
	b, _ := json.Marshal(keep)
	a.setSetting("firewall.managed."+host, string(b))
	a.fwPushBans(ctx, host) // the table was rebuilt: put the bans back
	a.hub.publish(event{Type: "firewall.pending", Data: map[string]any{"host": host, "rollback_at": time.Now().Add(60 * time.Second).Unix()}})
	return nil
}

// fwPushBans is filled in by the bans module.
var fwPushBansHook func(a *App, ctx context.Context, host string)

func (a *App) fwPushBans(ctx context.Context, host string) {
	if fwPushBansHook != nil {
		fwPushBansHook(a, ctx, host)
	}
}

// fwPlan previews a new list against the current one.
func (a *App) fwPlan(env change.Env, rules []fwRule, summary string, sp map[string]any) (change.Plan, error) {
	a2 := svc(env)
	var cur struct {
		State firewall.State `json:"state"`
	}
	a2.call(env.Ctx, env.Host, "GET", "/v1/firewall", nil, &cur)
	var p firewall.Plan
	if err := a2.call(env.Ctx, env.Host, "POST", "/v1/firewall/preview", a2.fwDesired(env.Host, rules), &p); err != nil {
		return change.Plan{}, err
	}
	plan := change.Plan{Summary: summary, SummaryP: sp, Risk: change.RiskHigh, Undoable: true,
		Files: []change.FileDiff{{Path: "nft: table inet berth", Before: cur.State.Table + "\n", After: p.Script}}}
	for _, x := range p.AddPorts {
		plan.Commands = append(plan.Commands, "firewall-cmd --add-port="+x)
	}
	for _, x := range p.DelPorts {
		plan.Commands = append(plan.Commands, "firewall-cmd --remove-port="+x)
	}
	for _, x := range p.AddRich {
		plan.Commands = append(plan.Commands, "firewall-cmd --add-rich-rule='"+x+"'")
	}
	for _, x := range p.DelRich {
		plan.Commands = append(plan.Commands, "firewall-cmd --remove-rich-rule='"+x+"'")
	}
	plan.Commands = append(plan.Commands, "# rollback in 60 s unless confirmed")
	if r := a2.lockout(env, rules); r != "" {
		plan.SummaryP["lockout"] = r
		plan.Summary = summary + "_lockout"
	}
	return plan, nil
}

// lockout names the port a deny rule would cut the requester off from ("" when none).
func (a *App) lockout(env change.Env, rules []fwRule) string {
	ip := net.ParseIP(env.ActorIP)
	if ip == nil || ip.IsLoopback() {
		return ""
	}
	_, port, _ := net.SplitHostPort(a.cfg.Listen)
	critical := map[int]string{22: "SSH 22"}
	if p, err := strconv.Atoi(port); err == nil {
		critical[p] = "panel " + port
	}
	matches := func(r fwRule) bool {
		hit := false
		switch r.Source {
		case "any":
			hit = true
		case "ip":
			hit = net.ParseIP(r.Value).Equal(ip)
		case "cidr":
			_, n, err := net.ParseCIDR(r.Value)
			hit = err == nil && n.Contains(ip)
		case "cloudflare":
			hit = false // the requester is not a Cloudflare edge address
		}
		if r.Except {
			hit = !hit
		}
		return hit
	}
	// for each port that matters, the first rule that matches decides (as in nft)
	for p, name := range critical {
		for _, r := range rules {
			if !r.Enabled || r.Proto == "udp" || !matches(r) || !(r.Ports == "" || portIn(r.Ports, p)) {
				continue
			}
			if r.Action == "deny" {
				return name
			}
			break
		}
	}
	return ""
}

func portIn(spec string, p int) bool {
	for _, part := range strings.Split(spec, ",") {
		a, b, _ := strings.Cut(part, "-")
		x, _ := strconv.Atoi(a)
		y := x
		if b != "" {
			y, _ = strconv.Atoi(b)
		}
		if p >= x && p <= y {
			return true
		}
	}
	return false
}

// ---- API and kinds ----

type fwRuleParams struct {
	fwRule
	Position int `json:"position,omitempty"`
}

func init() {
	routeHooks = append(routeHooks, func(a *App, mux *http.ServeMux) {
		mux.HandleFunc("GET /api/v1/hosts/{host}/firewall", a.authed("network.view", func(w http.ResponseWriter, r *http.Request) {
			host := r.PathValue("host")
			a.fwReconcile(r.Context(), host)
			var cur map[string]any
			if err := a.call(r.Context(), host, "GET", "/v1/firewall", nil, &cur); err != nil {
				a.fail(w, r, err)
				return
			}
			cur["rules"] = a.fwRules(host)
			cur["your_ip"] = a.clientIP(r)
			writeJSON(w, 200, cur)
		}))
	})

	ruleProps := map[string]change.Prop{"id": {Type: "integer"}, "action": {Type: "string", Enum: []string{"allow", "deny"}},
		"proto": {Type: "string", Enum: []string{"tcp", "udp", "any"}}, "ports": {Type: "string", Desc: "22 | 80,443 | 8000-8100; empty = all"},
		"source": {Type: "string", Enum: []string{"any", "ip", "cidr", "cloudflare", "country"}}, "value": {Type: "string"},
		"except": {Type: "boolean"}, "comment": {Type: "string"}, "enabled": {Type: "boolean"}, "position": {Type: "integer"}}

	change.Register(change.Kind{
		Name: "firewall.rule.set", Perm: "network.manage", Scope: "system:write", Title: "kind.firewall.rule.set",
		Params: change.ParamSchema{Props: ruleProps, Required: []string{"action", "proto", "source"}}, CLI: "berth firewall rule set",
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			a := svc(env)
			var p fwRuleParams
			json.Unmarshal(raw, &p)
			if err := p.fwRule.Validate(); err != nil {
				return change.Plan{}, errs.New(400, "firewall.bad_rule", "detail", err.Error())
			}
			rules := withRule(a.fwRules(env.Host), p)
			return a.fwPlan(env, rules, "plan.firewall.rule.set", map[string]any{"rule": describeRule(p.fwRule)})
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			a := svc(env)
			var p fwRuleParams
			json.Unmarshal(raw, &p)
			before := a.fwRules(env.Host)
			var undo *change.UndoRecipe
			var old *fwRule
			for i := range before {
				if before[i].ID == p.ID && p.ID != 0 {
					old = &before[i]
				}
			}
			spec, _ := json.Marshal(p.fwRule)
			if old != nil {
				a.db.Exec(`UPDATE fw_rules SET spec=? WHERE id=? AND host=?`, string(spec), p.ID, env.Host)
				if p.Position > 0 {
					a.db.Exec(`UPDATE fw_rules SET position=? WHERE id=?`, p.Position, p.ID)
				}
				u, _ := json.Marshal(fwRuleParams{fwRule: *old})
				undo = &change.UndoRecipe{Kind: "firewall.rule.set", Params: u}
			} else {
				pos := p.Position
				if pos == 0 {
					a.db.QueryRow(`SELECT COALESCE(MAX(position),0)+10 FROM fw_rules WHERE host=?`, env.Host).Scan(&pos)
				}
				var res interface{ LastInsertId() (int64, error) }
				var err error
				if p.ID != 0 { // re-creating a deleted rule keeps its id
					res, err = a.db.Exec(`INSERT INTO fw_rules(id, host, position, spec, created_at) VALUES(?,?,?,?,?)`, p.ID, env.Host, pos, string(spec), time.Now().Unix())
				} else {
					res, err = a.db.Exec(`INSERT INTO fw_rules(host, position, spec, created_at) VALUES(?,?,?,?)`, env.Host, pos, string(spec), time.Now().Unix())
				}
				if err != nil {
					return nil, nil, err
				}
				p.ID, _ = res.LastInsertId()
				u, _ := json.Marshal(map[string]int64{"id": p.ID})
				undo = &change.UndoRecipe{Kind: "firewall.rule.delete", Params: u}
			}
			if err := a.fwApply(env.Ctx, env.Host, a.fwRules(env.Host)); err != nil {
				a.fwRestoreRows(env.Host, before)
				return nil, nil, err
			}
			return map[string]any{"id": p.ID, "pending": true}, undo, nil
		},
	})

	change.Register(change.Kind{
		Name: "firewall.rule.delete", Perm: "network.manage", Scope: "system:write", Title: "kind.firewall.rule.delete",
		Params: change.ParamSchema{Props: map[string]change.Prop{"id": {Type: "integer"}}, Required: []string{"id"}}, CLI: "berth firewall rule delete {{.id}}",
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			a := svc(env)
			var p struct{ ID int64 }
			json.Unmarshal(raw, &p)
			var rules []fwRule
			var gone *fwRule
			for _, r := range a.fwRules(env.Host) {
				if r.ID == p.ID {
					x := r
					gone = &x
					continue
				}
				rules = append(rules, r)
			}
			if gone == nil {
				return change.Plan{}, errs.New(404, "firewall.no_rule")
			}
			return a.fwPlan(env, rules, "plan.firewall.rule.delete", map[string]any{"rule": describeRule(*gone)})
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			a := svc(env)
			var p struct{ ID int64 }
			json.Unmarshal(raw, &p)
			before := a.fwRules(env.Host)
			var pos int
			var spec string
			if a.db.QueryRow(`SELECT position, spec FROM fw_rules WHERE id=? AND host=?`, p.ID, env.Host).Scan(&pos, &spec) != nil {
				return nil, nil, errs.New(404, "firewall.no_rule")
			}
			a.db.Exec(`DELETE FROM fw_rules WHERE id=?`, p.ID)
			if err := a.fwApply(env.Ctx, env.Host, a.fwRules(env.Host)); err != nil {
				a.fwRestoreRows(env.Host, before)
				return nil, nil, err
			}
			var old fwRule
			json.Unmarshal([]byte(spec), &old)
			old.ID = p.ID
			u, _ := json.Marshal(fwRuleParams{fwRule: old, Position: pos})
			return map[string]any{"pending": true}, &change.UndoRecipe{Kind: "firewall.rule.set", Params: u}, nil
		},
	})

	change.Register(change.Kind{
		Name: "firewall.confirm", Perm: "network.manage", Scope: "system:write", Title: "kind.firewall.confirm",
		Params: change.ParamSchema{Props: map[string]change.Prop{}}, CLI: "berth firewall confirm",
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			return change.Plan{Summary: "plan.firewall.confirm", Risk: change.RiskLow, Commands: []string{"systemctl stop berth-fw-rollback.timer", "firewall-cmd --permanent …"}}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			a := svc(env)
			fwMu.Lock()
			defer fwMu.Unlock()
			if err := a.call(env.Ctx, env.Host, "POST", "/v1/firewall/confirm", nil, nil); err != nil {
				return nil, nil, err
			}
			a.fwMarkConfirmed(env.Host)
			a.hub.publish(event{Type: "firewall.pending", Data: map[string]any{"host": env.Host, "rollback_at": 0}})
			return nil, nil, nil
		},
	})

	change.Register(change.Kind{
		Name: "firewall.rollback", Perm: "network.manage", Scope: "system:write", Title: "kind.firewall.rollback",
		Params: change.ParamSchema{Props: map[string]change.Prop{}}, CLI: "berth firewall rollback",
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			return change.Plan{Summary: "plan.firewall.rollback", Risk: change.RiskLow, Commands: []string{"sh /var/lib/berth/firewall/rollback.sh"}}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			a := svc(env)
			fwMu.Lock()
			defer fwMu.Unlock()
			if err := a.call(env.Ctx, env.Host, "POST", "/v1/firewall/rollback", nil, nil); err != nil {
				return nil, nil, err
			}
			a.fwReconcileLocked(env.Ctx, env.Host)
			a.hub.publish(event{Type: "firewall.pending", Data: map[string]any{"host": env.Host, "rollback_at": 0}})
			return nil, nil, nil
		},
	})
}

// withRule returns the list with p added or replaced (kept in position order).
func withRule(rules []fwRule, p fwRuleParams) []fwRule {
	out := make([]fwRule, 0, len(rules)+1)
	found := false
	for _, r := range rules {
		if r.ID == p.ID && p.ID != 0 {
			r = p.fwRule
			found = true
		}
		out = append(out, r)
	}
	if !found {
		out = append(out, p.fwRule)
	}
	return out
}

func (a *App) fwRestoreRows(host string, rules []fwRule) {
	a.db.Exec(`DELETE FROM fw_rules WHERE host=?`, host)
	for i, r := range rules {
		spec, _ := json.Marshal(r)
		a.db.Exec(`INSERT INTO fw_rules(id, host, position, spec, created_at) VALUES(?,?,?,?,?)`, r.ID, host, (i+1)*10, string(spec), time.Now().Unix())
	}
}

// fwMarkConfirmed records the current list as the one in force.
func (a *App) fwMarkConfirmed(host string) {
	b, _ := json.Marshal(a.fwRules(host))
	a.setSetting("firewall.confirmed."+host, string(b))
	a.setSetting("firewall.dirty."+host, "")
}

// fwReconcile: when unconfirmed edits exist but the agent no longer waits for a
// confirmation, the rollback ran (timer or button): put the confirmed list back.
func (a *App) fwReconcile(ctx context.Context, host string) {
	fwMu.Lock()
	defer fwMu.Unlock()
	a.fwReconcileLocked(ctx, host)
}

func (a *App) fwReconcileLocked(ctx context.Context, host string) {
	if a.setting("firewall.dirty."+host) == "" {
		return
	}
	var cur struct {
		State firewall.State `json:"state"`
	}
	if a.call(ctx, host, "GET", "/v1/firewall", nil, &cur) != nil || cur.State.Pending {
		return
	}
	var rules []fwRule
	json.Unmarshal([]byte(a.setting("firewall.confirmed."+host)), &rules)
	a.fwRestoreRows(host, rules)
	a.setSetting("firewall.dirty."+host, "")
	a.hub.publish(event{Type: "firewall.pending", Data: map[string]any{"host": host, "rollback_at": 0, "rolled_back": true}})
}

func describeRule(r fwRule) string {
	src := r.Source
	switch r.Source {
	case "ip", "cidr":
		src = r.Value
	case "country":
		src = "country " + r.Value
	}
	if r.Except {
		src = "not " + src
	}
	ports := r.Ports
	if ports == "" {
		ports = "*"
	}
	return r.Action + " " + r.Proto + "/" + ports + " from " + src
}
