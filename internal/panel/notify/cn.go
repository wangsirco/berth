package notify

// Push services common in China: Server酱 (ServerChan, both SendKey kinds), PushPlus, and the
// group-chat robots of WeCom (企业微信), DingTalk (钉钉) and Feishu (飞书). Each answers HTTP 200
// with its own error code, so the code is checked too.

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var (
	serverChanAPI = "https://sctapi.ftqq.com"
	pushPlusAPI   = "https://www.pushplus.plus/send"
	sc3Re         = regexp.MustCompile(`^sctp(\d+)t`)
)

// markdown: the message as Markdown (title as a heading, then the body and the link).
func markdown(m Message) string {
	s := m.Body
	if m.URL != "" {
		s += "\n\n[" + m.URL + "](" + m.URL + ")"
	}
	return s
}

func plain(m Message) string {
	s := m.Title + "\n" + m.Body
	if m.URL != "" {
		s += "\n" + m.URL
	}
	return s
}

// postCode posts and checks the service's own result field (0 or 200 means fine).
func postCode(ctx context.Context, u, ctype string, body []byte) error {
	req, err := http.NewRequestWithContext(ctx, "POST", u, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", ctype)
	req.Header.Set("User-Agent", "berth")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode >= 300 {
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	var r struct {
		Code    *int   `json:"code"`
		ErrCode *int   `json:"errcode"`
		Msg     string `json:"msg"`
		Message string `json:"message"`
		ErrMsg  string `json:"errmsg"`
	}
	if json.Unmarshal(b, &r) != nil {
		return nil
	}
	code := 0
	if r.Code != nil {
		code = *r.Code
	} else if r.ErrCode != nil {
		code = *r.ErrCode
	}
	if code != 0 && code != 200 {
		msg := r.Msg + r.Message + r.ErrMsg
		return fmt.Errorf("code %d: %s", code, msg)
	}
	return nil
}

func sign(secret, data string) string {
	h := hmac.New(sha256.New, []byte(secret))
	h.Write([]byte(data))
	return base64.StdEncoding.EncodeToString(h.Sum(nil))
}

func sendCN(ctx context.Context, c Config, m Message) error {
	switch c.Kind {
	case "serverchan":
		key := strings.TrimSpace(c.Key)
		u := serverChanAPI + "/" + url.PathEscape(key) + ".send"
		if g := sc3Re.FindStringSubmatch(key); g != nil { // Server酱³ keys carry the user id
			u = "https://" + g[1] + ".push.ft07.com/send/" + url.PathEscape(key) + ".send"
		}
		form := url.Values{"title": {m.Title}, "desp": {markdown(m)}}
		return postCode(ctx, u, "application/x-www-form-urlencoded", []byte(form.Encode()))
	case "pushplus":
		body, _ := json.Marshal(map[string]string{"token": strings.TrimSpace(c.Key), "title": m.Title, "content": markdown(m), "template": "markdown"})
		return postCode(ctx, pushPlusAPI, "application/json", body)
	case "wecom":
		body, _ := json.Marshal(map[string]any{"msgtype": "markdown", "markdown": map[string]string{"content": "**" + m.Title + "**\n" + markdown(m)}})
		return postCode(ctx, c.URL, "application/json", body)
	case "dingtalk":
		u := c.URL
		if c.Secret != "" {
			ts := strconv.FormatInt(time.Now().UnixMilli(), 10)
			u += "&timestamp=" + ts + "&sign=" + url.QueryEscape(sign(c.Secret, ts+"\n"+c.Secret))
		}
		body, _ := json.Marshal(map[string]any{"msgtype": "markdown", "markdown": map[string]string{"title": m.Title, "text": "### " + m.Title + "\n\n" + markdown(m)}})
		return postCode(ctx, u, "application/json", body)
	case "feishu":
		msg := map[string]any{"msg_type": "text", "content": map[string]string{"text": plain(m)}}
		if c.Secret != "" {
			ts := strconv.FormatInt(time.Now().Unix(), 10)
			h := hmac.New(sha256.New, []byte(ts+"\n"+c.Secret)) // Feishu signs an empty message with this key
			msg["timestamp"], msg["sign"] = ts, base64.StdEncoding.EncodeToString(h.Sum(nil))
		}
		body, _ := json.Marshal(msg)
		return postCode(ctx, c.URL, "application/json", body)
	}
	return fmt.Errorf("unknown channel %q", c.Kind)
}
