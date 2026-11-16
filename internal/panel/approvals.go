package panel

// Approvals (方案 §5.15 审批流程). A super administrator chooses which operations need
// approval (by risk and by kind) and for whom (administrators, API/CLI tokens). Such a
// change waits as an approval request — its preview is the request — until a super
// administrator approves it in the panel, from a push notification's link, or with the
// buttons on the Telegram message. Requests expire after a set time (rejected). The
// requester hears the outcome.

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/wangsirco/berth/internal/change"
	"github.com/wangsirco/berth/internal/errs"
	"github.com/wangsirco/berth/internal/panel/notify"
)

type approvalPolicy struct {
	Risks  []string
	Kinds  []string // names, or prefixes ending in "*" (firewall.*)
	Admins bool
	Tokens bool
	Hours  int
}

func (a *App) approvalPolicy() approvalPolicy {
	who := splitList(a.setting("approval.who"))
	return approvalPolicy{Risks: splitList(a.setting("approval.risks")), Kinds: splitList(a.setting("approval.kinds")),
		Admins: contains(who, "admins"), Tokens: contains(who, "tokens"), Hours: a.settingInt("approval.hours", 24)}
}

func (p approvalPolicy) matches(kind string, risk change.Risk) bool {
	if contains(p.Risks, string(risk)) {
		return true
	}
	for _, k := range p.Kinds {
		if k == kind || strings.HasSuffix(k, "*") && strings.HasPrefix(kind, strings.TrimSuffix(k, "*")) {
			return true
		}
	}
	return false
}

// needsApproval: the policy applies to this actor and this change.
func (a *App) needsApproval(ac change.Actor, k change.Kind, p change.Plan) bool {
	pol := a.approvalPolicy()
	if !pol.matches(k.Name, p.Risk) {
		return false
	}
	switch ac.Type {
	case "user":
		if !pol.Admins {
			return false
		}
		id, _ := strconv.ParseInt(ac.ID, 10, 64)
		u, err := a.userByID(id)
		return err == nil && u.Role != RoleSuper
	case "pat", "cli", "ext":
		return pol.Tokens
	}
	return false
}

func init() {
	for k, v := range map[string]func(string) error{
		"approval.risks": func(v string) error {
			for _, r := range splitList(v) {
				if r != "medium" && r != "high" && r != "destructive" {
					return errs.New(400, "params.enum", "name", "risk", "allowed", "medium, high, destructive")
				}
			}
			return nil
		},
		"approval.kinds": func(v string) error {
			for _, k := range splitList(v) {
				if _, ok := change.Get(k); !ok && !strings.HasSuffix(k, "*") {
					return errs.New(400, "change.unknown_kind", "kind", k)
				}
			}
			return nil
		},
		"approval.who": func(v string) error {
			for _, w := range splitList(v) {
				if w != "admins" && w != "tokens" {
					return errs.New(400, "params.enum", "name", "who", "allowed", "admins, tokens")
				}
			}
			return nil
		},
		"approval.hours": intSetting(1, 168),
	} {
		settingDefs[k] = settingDef{validate: v}
	}
	startHooks = append(startHooks, func(a *App, ctx context.Context) { go a.approvalLoop(ctx) })
}

// wireApprovals is called from New.
func (a *App) wireApprovals() {
	a.pipe.OnPending = func(c *change.Change) { a.publishChange("change.pending", c); go a.notifyApproval(c) }
	a.pipe.OnRejected = func(c *change.Change) { a.publishChange("change.done", c); go a.notifyRequester(c) }
}

// ---- notifications ----

func (a *App) approvalSecret() []byte {
	s := a.setting("approval.secret")
	if s == "" {
		b := make([]byte, 32)
		rand.Read(b)
		s = hex.EncodeToString(b)
		a.setSetting("approval.secret", s)
	}
	b, _ := hex.DecodeString(s)
	return b
}

// approvalToken signs "id:verdict" so a Telegram button cannot be forged.
func (a *App) approvalToken(id, verdict string) string {
	m := hmac.New(sha256.New, a.approvalSecret())
	m.Write([]byte(id + ":" + verdict))
	return hex.EncodeToString(m.Sum(nil))[:16]
}

func (a *App) supers() map[int64]*User {
	out := map[int64]*User{}
	rows, err := a.db.Query(`SELECT id FROM users WHERE role=? AND disabled=0`, RoleSuper)
	if err != nil {
		return out
	}
	var ids []int64
	for rows.Next() {
		var id int64
		rows.Scan(&id)
		ids = append(ids, id)
	}
	rows.Close()
	for _, id := range ids {
		if u, err := a.userByID(id); err == nil {
			out[id] = u
		}
	}
	return out
}

// notifyApproval sends the request to the super administrators' "approvals" channels.
func (a *App) notifyApproval(c *change.Change) {
	chans, err := a.channelsFor("approvals", c.Host)
	if err != nil {
		return
	}
	sup := a.supers()
	for _, ch := range chans {
		if sup[ch.UserID] == nil {
			continue
		}
		title := T(ch.locale, "notify.approval", map[string]any{"who": c.Actor.Name, "kind": T(ch.locale, "kind."+c.Kind, nil)})
		body := T(ch.locale, c.Plan.Summary, c.Plan.SummaryP) + "\n" + T(ch.locale, "notify.approval_risk", map[string]any{"risk": T(ch.locale, "risk."+string(c.Plan.Risk), nil), "host": c.Host})
		m := notify.Message{Title: title, Body: body, Severity: "warn", Tag: "berth-approval-" + c.ID, URL: a.publicURL() + "/" + c.Host + "/audit?pending=" + c.ID}
		if ch.cfg.Kind == "telegram" {
			m.Actions = []notify.Action{
				{Title: T(ch.locale, "notify.approve", nil), Data: "ap:" + c.ID + ":y:" + a.approvalToken(c.ID, "y")},
				{Title: T(ch.locale, "notify.reject", nil), Data: "ap:" + c.ID + ":n:" + a.approvalToken(c.ID, "n")},
			}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		if err := notify.Send(ctx, ch.cfg, a.vapid(), m); err != nil {
			log.Printf("panel: approval notice via channel %d: %v", ch.id, err)
		}
		cancel()
	}
	a.wakeTelegram()
}

// notifyRequester tells the person who asked how it went (their "approvals" channels).
func (a *App) notifyRequester(c *change.Change) {
	if c.Actor.Type != "user" {
		return
	}
	uid, _ := strconv.ParseInt(c.Actor.ID, 10, 64)
	chans, err := a.channelsFor("approvals", c.Host)
	if err != nil {
		return
	}
	for _, ch := range chans {
		if ch.UserID != uid {
			continue
		}
		key := "notify.approval_" + map[bool]string{true: "rejected", false: "approved"}[c.Status == "rejected"]
		m := notify.Message{Title: T(ch.locale, key, map[string]any{"kind": T(ch.locale, "kind."+c.Kind, nil)}), Body: T(ch.locale, c.Plan.Summary, c.Plan.SummaryP),
			Severity: "info", Tag: "berth-approval-" + c.ID, URL: a.publicURL() + "/" + c.Host + "/audit"}
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		notify.Send(ctx, ch.cfg, a.vapid(), m)
		cancel()
	}
}

// ---- expiry and Telegram buttons ----

var tgWake = make(chan struct{}, 1)

func (a *App) wakeTelegram() {
	select {
	case tgWake <- struct{}{}:
	default:
	}
}

// expireApprovals rejects requests older than the set time; it returns how many still wait.
func (a *App) expireApprovals() int {
	hours := a.settingInt("approval.hours", 24)
	list, err := a.pipe.List(change.ListFilter{Status: "pending_approval", Limit: 500})
	if err != nil {
		return 0
	}
	n := 0
	for _, c := range list {
		if time.Since(time.Unix(c.TS, 0)) > time.Duration(hours)*time.Hour {
			a.pipe.Reject(c.ID, change.Actor{Type: "system", ID: "timeout", Name: "timeout", Via: "system"})
		} else {
			n++
		}
	}
	return n
}

func (a *App) approvalLoop(ctx context.Context) {
	tick := time.NewTicker(time.Minute)
	defer tick.Stop()
	offsets := map[string]int64{}
	var mu sync.Mutex
	polling := map[string]bool{}
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		case <-tgWake:
		}
		if a.expireApprovals() == 0 {
			continue
		}
		// listen for button presses on the super administrators' Telegram bots
		chans, _ := a.channelsFor("approvals", "local")
		sup := a.supers()
		for _, ch := range chans {
			if ch.cfg.Kind != "telegram" || sup[ch.UserID] == nil {
				continue
			}
			tok := ch.cfg.BotToken
			mu.Lock()
			busy := polling[tok]
			polling[tok] = true
			mu.Unlock()
			if busy {
				continue
			}
			go func(ch notifyChannel, owner *User) {
				defer func() { mu.Lock(); delete(polling, ch.cfg.BotToken); mu.Unlock() }()
				for i := 0; i < 20; i++ { // ~8 minutes, then the next tick decides again
					mu.Lock()
					off := offsets[ch.cfg.BotToken]
					mu.Unlock()
					cbs, next, err := notify.TelegramUpdates(ctx, ch.cfg.BotToken, off)
					if err != nil {
						return
					}
					mu.Lock()
					offsets[ch.cfg.BotToken] = next
					mu.Unlock()
					for _, cb := range cbs {
						a.telegramDecision(ctx, ch, owner, cb)
					}
					var n int
					a.db.QueryRow(`SELECT COUNT(*) FROM changes WHERE status='pending_approval'`).Scan(&n)
					if n == 0 {
						return
					}
				}
			}(ch, sup[ch.UserID])
		}
	}
}

func (a *App) telegramDecision(ctx context.Context, ch notifyChannel, owner *User, cb notify.Callback) {
	f := strings.Split(cb.Data, ":")
	tok := ch.cfg.BotToken
	if len(f) != 4 || f[0] != "ap" || cb.ChatID != ch.cfg.ChatID || !hmac.Equal([]byte(f[3]), []byte(a.approvalToken(f[1], f[2]))) {
		notify.TelegramAnswer(ctx, tok, cb.ID, "✗")
		return
	}
	by := change.Actor{Type: "user", ID: strconv.FormatInt(owner.ID, 10), Name: owner.Username, Via: "telegram"}
	var c *change.Change
	var err error
	if f[2] == "y" {
		c, err = a.pipe.Confirm(ctx, f[1], by)
	} else {
		c, err = a.pipe.Reject(f[1], by)
	}
	loc := firstNonEmpty(ch.locale, "zh-CN")
	if err != nil && c == nil {
		notify.TelegramAnswer(ctx, tok, cb.ID, T(loc, errs.From(err).Code, nil))
		return
	}
	verdict := T(loc, map[bool]string{true: "notify.approved_by", false: "notify.rejected_by"}[f[2] == "y"], map[string]any{"who": owner.Username})
	notify.TelegramAnswer(ctx, tok, cb.ID, verdict)
	notify.TelegramDone(ctx, tok, cb.ChatID, cb.MessageID, cb.Text+"\n\n"+verdict)
	if f[2] == "y" {
		a.notifyRequester(c)
	}
}
