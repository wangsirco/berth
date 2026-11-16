package panel

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wangsirco/berth/internal/change"
	"github.com/wangsirco/berth/internal/panel/notify"
)

func TestApprovalPolicyAndTelegram(t *testing.T) {
	c := newTC(t)
	a := c.app
	boss := c.mkUser("boss", "pw-boss-123456", RoleSuper)
	ops := c.mkUser("ops", "pw-ops-123456", RoleAdmin)
	ops.Perms = []string{"monitor.manage"}
	a.saveUser(ops)
	a.setSetting("approval.who", "admins")
	a.setSetting("approval.kinds", "uptime.*")

	// a fake Telegram: records sent messages, hands out one button press
	var mu sync.Mutex
	var sent []string
	var press string
	tg := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		defer mu.Unlock()
		switch {
		case strings.HasSuffix(r.URL.Path, "/getUpdates"):
			res := []any{}
			if press != "" {
				res = append(res, map[string]any{"update_id": 7, "callback_query": map[string]any{"id": "cb1", "data": press,
					"from": map[string]any{"username": "boss"}, "message": map[string]any{"message_id": 3, "text": "req", "chat": map[string]any{"id": 42}}}})
				press = ""
			}
			json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": res})
			return
		default:
			sent = append(sent, r.URL.Path+" "+string(b))
		}
		w.Write([]byte(`{"ok":true}`))
	}))
	defer tg.Close()
	notify.SetTelegramAPI(tg.URL)
	cfg, _ := json.Marshal(notify.Config{Kind: "telegram", BotToken: "T", ChatID: "42"})
	enc, _ := a.seal(cfg)
	a.db.Exec(`INSERT INTO notify_channels(user_id, kind, name, cfg_enc, categories, created_at) VALUES(?,?,?,?,?,?)`, boss.ID, "telegram", "tg", enc, `["approvals"]`, time.Now().Unix())

	params := json.RawMessage(`{"name":"x","kind":"tcp","target":"127.0.0.1:1"}`)
	asOps := change.Actor{Type: "user", ID: itoa(ops.ID), Name: "ops", Via: "ui"}
	ch, err := a.pipe.Run(testRequest().Context(), asOps, "local", "uptime.set", params)
	if err != nil || ch.Status != "pending_approval" {
		t.Fatalf("admin's change should wait: %v %+v", err, ch)
	}
	// a super administrator's own change does not wait; other kinds do not either
	if c2, _ := a.pipe.Run(testRequest().Context(), change.Actor{Type: "user", ID: itoa(boss.ID), Name: "boss"}, "local", "uptime.set", params); c2.Status != "done" {
		t.Fatalf("super's change waited: %s", c2.Status)
	}
	// the request reached Telegram with buttons
	var body string
	for i := 0; i < 50 && body == ""; i++ {
		mu.Lock()
		for _, s := range sent {
			if strings.Contains(s, "inline_keyboard") {
				body = s
			}
		}
		mu.Unlock()
		time.Sleep(20 * time.Millisecond)
	}
	if !strings.Contains(body, "ap:"+ch.ID+":y:") {
		t.Fatalf("no approve button: %q", body)
	}
	// a forged press is ignored; the real one approves
	chans, _ := a.channelsFor("approvals", "local")
	a.telegramDecision(testRequest().Context(), chans[0], boss, notify.Callback{ID: "x", Data: "ap:" + ch.ID + ":y:0000000000000000", ChatID: "42"})
	if got, _ := a.pipe.Get(ch.ID); got.Status != "pending_approval" {
		t.Fatalf("forged button acted: %s", got.Status)
	}
	mu.Lock()
	press = "ap:" + ch.ID + ":y:" + a.approvalToken(ch.ID, "y")
	mu.Unlock()
	cbs, _, err := notify.TelegramUpdates(testRequest().Context(), "T", 0)
	if err != nil || len(cbs) != 1 {
		t.Fatalf("updates: %v %v", err, cbs)
	}
	a.telegramDecision(testRequest().Context(), chans[0], boss, cbs[0])
	got, _ := a.pipe.Get(ch.ID)
	if got.Status != "done" || !strings.Contains(got.Log, "confirmed by boss (telegram)") {
		t.Fatalf("not approved: %s %q", got.Status, got.Log)
	}
	// expiry rejects old requests
	ch3, _ := a.pipe.Run(testRequest().Context(), asOps, "local", "uptime.set", params)
	a.db.Exec(`UPDATE changes SET ts=? WHERE id=?`, time.Now().Add(-25*time.Hour).Unix(), ch3.ID)
	a.expireApprovals()
	if got, _ := a.pipe.Get(ch3.ID); got.Status != "rejected" {
		t.Fatalf("not expired: %s", got.Status)
	}
}
