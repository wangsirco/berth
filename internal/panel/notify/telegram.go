package notify

// Telegram callbacks: the panel long-polls getUpdates for button presses (approve /
// reject), answers them and updates the message. Polling needs no public address.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// Callback is one button press.
type Callback struct {
	ID        string
	Data      string
	ChatID    string
	MessageID int64
	FromName  string
	Text      string // the message's text, as shown
}

var pollClient = &http.Client{Timeout: 40 * time.Second}

// TelegramUpdates waits up to 25 s for button presses after offset; it returns them and
// the next offset.
func TelegramUpdates(ctx context.Context, token string, offset int64) ([]Callback, int64, error) {
	q := url.Values{"timeout": {"25"}, "offset": {strconv.FormatInt(offset, 10)}, "allowed_updates": {`["callback_query"]`}}
	req, _ := http.NewRequestWithContext(ctx, "GET", telegramAPI+"/bot"+token+"/getUpdates?"+q.Encode(), nil)
	resp, err := pollClient.Do(req)
	if err != nil {
		return nil, offset, err
	}
	defer resp.Body.Close()
	var r struct {
		OK          bool   `json:"ok"`
		Description string `json:"description"`
		Result      []struct {
			UpdateID int64 `json:"update_id"`
			CB       *struct {
				ID   string `json:"id"`
				Data string `json:"data"`
				From struct {
					Username  string `json:"username"`
					FirstName string `json:"first_name"`
				} `json:"from"`
				Message *struct {
					MessageID int64  `json:"message_id"`
					Text      string `json:"text"`
					Chat      struct {
						ID int64 `json:"id"`
					} `json:"chat"`
				} `json:"message"`
			} `json:"callback_query"`
		} `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return nil, offset, err
	}
	if !r.OK {
		return nil, offset, fmt.Errorf("telegram: %s", r.Description)
	}
	var out []Callback
	for _, u := range r.Result {
		if u.UpdateID >= offset {
			offset = u.UpdateID + 1
		}
		if u.CB == nil || u.CB.Message == nil {
			continue
		}
		name := u.CB.From.Username
		if name == "" {
			name = u.CB.From.FirstName
		}
		out = append(out, Callback{ID: u.CB.ID, Data: u.CB.Data, ChatID: strconv.FormatInt(u.CB.Message.Chat.ID, 10), MessageID: u.CB.Message.MessageID, FromName: name, Text: u.CB.Message.Text})
	}
	return out, offset, nil
}

func tgCall(ctx context.Context, token, method string, v any) error {
	b, _ := json.Marshal(v)
	req, _ := http.NewRequestWithContext(ctx, "POST", telegramAPI+"/bot"+token+"/"+method, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	return do(req)
}

// TelegramAnswer acknowledges a button press with a short toast.
func TelegramAnswer(ctx context.Context, token, callbackID, text string) error {
	return tgCall(ctx, token, "answerCallbackQuery", map[string]any{"callback_query_id": callbackID, "text": text})
}

// TelegramDone replaces the message's buttons with a line saying what happened.
func TelegramDone(ctx context.Context, token, chatID string, messageID int64, text string) error {
	return tgCall(ctx, token, "editMessageText", map[string]any{"chat_id": chatID, "message_id": messageID, "text": text, "disable_web_page_preview": true})
}

// SetTelegramAPI points the Telegram calls elsewhere (tests).
func SetTelegramAPI(u string) { telegramAPI = u }
