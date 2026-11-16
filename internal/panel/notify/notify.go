// Package notify delivers alerts (方案 §5.13.3): browser/PWA push (Web Push with the
// panel's own VAPID keys), Telegram, email (SMTP), Bark, ntfy and a generic webhook with
// an editable body template (WeCom, DingTalk, Slack…).
package notify

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/smtp"
	"net/url"
	"strings"
	"text/template"
	"time"

	webpush "github.com/SherClockHolmes/webpush-go"
)

// Message is one notification.
type Message struct {
	Title    string   `json:"title"`
	Body     string   `json:"body"`
	URL      string   `json:"url,omitempty"`      // where clicking leads (panel page)
	Severity string   `json:"severity,omitempty"` // info | warn | critical
	Tag      string   `json:"tag,omitempty"`      // same tag replaces the previous notification
	Actions  []Action `json:"actions,omitempty"`  // buttons (Telegram inline keyboard; push notification actions)
}

// Action is a button on a notification. Data comes back in a Telegram callback.
type Action struct {
	Title string `json:"title"`
	Data  string `json:"action"`
}

// Config holds one channel's settings (sealed by the panel).
type Config struct {
	Kind string `json:"kind"` // webpush | telegram | email | bark | ntfy | webhook | serverchan | pushplus | wecom | dingtalk | feishu

	// webpush
	Subscription json.RawMessage `json:"subscription,omitempty"`
	// telegram
	BotToken string `json:"bot_token,omitempty"`
	ChatID   string `json:"chat_id,omitempty"`
	// email
	SMTPHost string `json:"smtp_host,omitempty"` // host:port
	SMTPUser string `json:"smtp_user,omitempty"`
	SMTPPass string `json:"smtp_pass,omitempty"`
	From     string `json:"from,omitempty"`
	To       string `json:"to,omitempty"`
	// bark, ntfy, webhook
	URL      string `json:"url,omitempty"`
	Token    string `json:"token,omitempty"`    // ntfy access token
	Template string `json:"template,omitempty"` // webhook body (text/template over Message)
	// serverchan (SendKey), pushplus (token)
	Key string `json:"key,omitempty"`
	// dingtalk, feishu: the robot's signing secret (optional); wecom/dingtalk/feishu use URL
	Secret string `json:"secret,omitempty"`
}

// VAPID are the panel's Web Push keys.
type VAPID struct{ Public, Private, Subject string }

var client = &http.Client{Timeout: 15 * time.Second}

// Send delivers m through one channel.
func Send(ctx context.Context, c Config, v VAPID, m Message) error {
	switch c.Kind {
	case "webpush":
		var sub webpush.Subscription
		if err := json.Unmarshal(c.Subscription, &sub); err != nil {
			return fmt.Errorf("bad subscription")
		}
		payload, _ := json.Marshal(m)
		resp, err := webpush.SendNotificationWithContext(ctx, payload, &sub, &webpush.Options{
			Subscriber: v.Subject, VAPIDPublicKey: v.Public, VAPIDPrivateKey: v.Private, TTL: 86400, Urgency: urgency(m.Severity), Topic: topic(m.Tag),
		})
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode == 404 || resp.StatusCode == 410 {
			return ErrGone
		}
		if resp.StatusCode >= 300 {
			b, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
			return fmt.Errorf("push service: HTTP %d %s", resp.StatusCode, b)
		}
		return nil
	case "telegram":
		text := "<b>" + escapeHTML(m.Title) + "</b>\n" + escapeHTML(m.Body)
		if m.URL != "" {
			text += "\n" + escapeHTML(m.URL)
		}
		msg := map[string]any{"chat_id": c.ChatID, "text": text, "parse_mode": "HTML", "disable_web_page_preview": true}
		if len(m.Actions) > 0 {
			row := []map[string]string{}
			for _, a := range m.Actions {
				row = append(row, map[string]string{"text": a.Title, "callback_data": a.Data})
			}
			msg["reply_markup"] = map[string]any{"inline_keyboard": [][]map[string]string{row}}
		}
		body, _ := json.Marshal(msg)
		return post(ctx, telegramAPI+"/bot"+c.BotToken+"/sendMessage", "application/json", body, nil)
	case "email":
		return sendMail(c, m)
	case "bark":
		u := strings.TrimRight(c.URL, "/") + "/" + url.PathEscape(m.Title) + "/" + url.PathEscape(m.Body)
		if m.URL != "" {
			u += "?url=" + url.QueryEscape(m.URL)
		}
		req, _ := http.NewRequestWithContext(ctx, "GET", u, nil)
		return do(req)
	case "ntfy":
		h := map[string]string{"Title": m.Title, "Priority": map[string]string{"critical": "5", "warn": "4"}[m.Severity], "Tags": "berth"}
		if m.URL != "" {
			h["Click"] = m.URL
		}
		if c.Token != "" {
			h["Authorization"] = "Bearer " + c.Token
		}
		return post(ctx, c.URL, "text/plain", []byte(m.Body), h)
	case "webhook":
		body, err := renderTemplate(c.Template, m)
		if err != nil {
			return err
		}
		return post(ctx, c.URL, "application/json", body, nil)
	case "serverchan", "pushplus", "wecom", "dingtalk", "feishu":
		return sendCN(ctx, c, m)
	}
	return fmt.Errorf("unknown channel %q", c.Kind)
}

// ErrGone: the browser dropped the subscription; the channel should be removed.
var ErrGone = fmt.Errorf("subscription expired")

var telegramAPI = "https://api.telegram.org"

func urgency(sev string) webpush.Urgency {
	switch sev {
	case "critical":
		return webpush.UrgencyHigh
	case "warn":
		return webpush.UrgencyNormal
	}
	return webpush.UrgencyLow
}

func topic(tag string) string {
	t := strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			return r
		}
		return -1
	}, tag)
	if len(t) > 32 {
		t = t[:32]
	}
	return t
}

func escapeHTML(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}

// DefaultTemplate is a JSON body most webhooks accept.
const DefaultTemplate = `{"title": {{json .Title}}, "text": {{json .Body}}, "url": {{json .URL}}, "severity": {{json .Severity}}}`

func renderTemplate(tpl string, m Message) ([]byte, error) {
	if strings.TrimSpace(tpl) == "" {
		tpl = DefaultTemplate
	}
	t, err := template.New("").Funcs(template.FuncMap{"json": func(v any) string { b, _ := json.Marshal(v); return string(b) }}).Parse(tpl)
	if err != nil {
		return nil, fmt.Errorf("template: %w", err)
	}
	var b bytes.Buffer
	if err := t.Execute(&b, m); err != nil {
		return nil, fmt.Errorf("template: %w", err)
	}
	return b.Bytes(), nil
}

func post(ctx context.Context, u, ctype string, body []byte, headers map[string]string) error {
	req, err := http.NewRequestWithContext(ctx, "POST", u, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", ctype)
	for k, v := range headers {
		if v != "" {
			req.Header.Set(k, v)
		}
	}
	return do(req)
}

func do(req *http.Request) error {
	req.Header.Set("User-Agent", "berth")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return nil
}

func sendMail(c Config, m Message) error {
	host, _, err := net.SplitHostPort(c.SMTPHost)
	if err != nil {
		return fmt.Errorf("SMTP server must be host:port")
	}
	msg := "From: " + c.From + "\r\nTo: " + c.To + "\r\nSubject: =?UTF-8?B?" + b64(m.Title) + "?=\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\n" + m.Body
	if m.URL != "" {
		msg += "\r\n\r\n" + m.URL
	}
	var auth smtp.Auth
	if c.SMTPUser != "" {
		auth = smtp.PlainAuth("", c.SMTPUser, c.SMTPPass, host)
	}
	to := strings.Split(c.To, ",")
	if strings.HasSuffix(c.SMTPHost, ":465") { // implicit TLS
		conn, err := tls.DialWithDialer(&net.Dialer{Timeout: 15 * time.Second}, "tcp", c.SMTPHost, &tls.Config{ServerName: host})
		if err != nil {
			return err
		}
		cl, err := smtp.NewClient(conn, host)
		if err != nil {
			return err
		}
		defer cl.Close()
		if auth != nil {
			if err := cl.Auth(auth); err != nil {
				return err
			}
		}
		if err := cl.Mail(c.From); err != nil {
			return err
		}
		for _, t := range to {
			if err := cl.Rcpt(strings.TrimSpace(t)); err != nil {
				return err
			}
		}
		w, err := cl.Data()
		if err != nil {
			return err
		}
		w.Write([]byte(msg))
		w.Close()
		return cl.Quit()
	}
	return smtp.SendMail(c.SMTPHost, auth, c.From, to, []byte(msg)) // STARTTLS when offered
}

func b64(s string) string {
	const abc = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	b := []byte(s)
	var out strings.Builder
	for i := 0; i < len(b); i += 3 {
		var n uint32
		k := 0
		for j := 0; j < 3; j++ {
			n <<= 8
			if i+j < len(b) {
				n |= uint32(b[i+j])
				k++
			}
		}
		for j := 0; j < 4; j++ {
			if j <= k {
				out.WriteByte(abc[(n>>(18-6*j))&63])
			} else {
				out.WriteByte('=')
			}
		}
	}
	return out.String()
}

// GenerateVAPID makes a new key pair.
func GenerateVAPID() (pub, priv string, err error) {
	priv, pub, err = webpush.GenerateVAPIDKeys()
	return pub, priv, err
}
