package notify

import (
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestChannels(t *testing.T) {
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got = append(got, r.Method+" "+r.URL.RequestURI()+" "+r.Header.Get("Title")+" "+string(b))
	}))
	defer srv.Close()
	telegramAPI = srv.URL
	m := Message{Title: "容器停了", Body: "n8n <exited>", URL: "https://p/x", Severity: "critical"}
	ctx := context.Background()
	for _, c := range []Config{
		{Kind: "telegram", BotToken: "T", ChatID: "42"},
		{Kind: "bark", URL: srv.URL + "/key"},
		{Kind: "ntfy", URL: srv.URL + "/berth"},
		{Kind: "webhook", URL: srv.URL + "/hook", Template: `{"msgtype":"text","text":{"content":{{json .Title}}}}`},
		{Kind: "webhook", URL: srv.URL + "/hook2"},
	} {
		if err := Send(ctx, c, VAPID{}, m); err != nil {
			t.Fatalf("%s: %v", c.Kind, err)
		}
	}
	all := strings.Join(got, "\n")
	for _, want := range []string{
		`POST /botT/sendMessage`, `"chat_id":"42"`, `lt;exited`,
		`GET /key/%E5%AE%B9%E5%99%A8%E5%81%9C%E4%BA%86/n8n%20%3Cexited%3E?url=https%3A%2F%2Fp%2Fx`,
		`POST /berth 容器停了 n8n <exited>`,
		`POST /hook  {"msgtype":"text","text":{"content":"容器停了"}}`,
		`"severity": "critical"`,
	} {
		if !strings.Contains(all, want) {
			t.Errorf("missing %s in\n%s", want, all)
		}
	}
	if _, err := renderTemplate("{{.Nope", m); err == nil {
		t.Fatal("bad template accepted")
	}
}

func TestWebPushEncrypts(t *testing.T) {
	pub, priv, err := GenerateVAPID()
	if err != nil {
		t.Fatal(err)
	}
	var body []byte
	var hdr http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
		hdr = r.Header
		w.WriteHeader(201)
	}))
	defer srv.Close()
	// a browser-like subscription (P-256 key and auth secret)
	sub := `{"endpoint":"` + srv.URL + `/push/abc","keys":{"p256dh":"BNcRdreALRFXTkOOUHK1EtK2wtaz5Ry4YfYCA_0QTpQtUbVlUls0VJXg7A8u-Ts1XbjhazAkj7I99e8QcYP7DkM","auth":"tBHItJI5svbpez7KI4CCXg"}}`
	if err := Send(context.Background(), Config{Kind: "webpush", Subscription: []byte(sub)}, VAPID{Public: pub, Private: priv, Subject: "mailto:admin@berth.local"},
		Message{Title: "t", Body: "secret body", Tag: "x"}); err != nil {
		t.Fatal(err)
	}
	if hdr.Get("Content-Encoding") != "aes128gcm" || !strings.HasPrefix(hdr.Get("Authorization"), "vapid t=") || strings.Contains(string(body), "secret body") {
		t.Fatalf("not encrypted / not signed: %v", hdr)
	}
	if _, err := base64.RawURLEncoding.DecodeString(pub); err != nil {
		t.Fatal("public key encoding")
	}
}
