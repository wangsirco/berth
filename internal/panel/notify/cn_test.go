package notify

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
)

func TestChineseChannels(t *testing.T) {
	var mu sync.Mutex
	got := map[string]string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		got[r.URL.Path] = r.URL.RawQuery + "|" + string(b)
		mu.Unlock()
		switch r.URL.Path {
		case "/bad":
			w.Write([]byte(`{"errcode":310000,"errmsg":"keywords not in content"}`))
		case "/push":
			w.Write([]byte(`{"code":200,"msg":"ok"}`))
		default:
			w.Write([]byte(`{"code":0,"errcode":0}`))
		}
	}))
	defer srv.Close()
	serverChanAPI, pushPlusAPI = srv.URL, srv.URL+"/push"
	m := Message{Title: "磁盘快满了", Body: "/ 用了 95%", URL: "https://panel/x"}
	ctx := context.Background()
	for _, c := range []Config{
		{Kind: "serverchan", Key: "SCT12345abcdef"},
		{Kind: "pushplus", Key: "pptoken123456"},
		{Kind: "wecom", URL: srv.URL + "/wecom?key=k"},
		{Kind: "dingtalk", URL: srv.URL + "/ding?access_token=a", Secret: "SECxyz"},
		{Kind: "feishu", URL: srv.URL + "/feishu", Secret: "fs"},
	} {
		if err := Send(ctx, c, VAPID{}, m); err != nil {
			t.Fatalf("%s: %v", c.Kind, err)
		}
	}
	sc := got["/SCT12345abcdef.send"]
	if q, _ := url.ParseQuery(strings.SplitN(sc, "|", 2)[1]); q.Get("title") != "磁盘快满了" || !strings.Contains(q.Get("desp"), "95%") {
		t.Fatalf("serverchan: %q", sc)
	}
	var pp map[string]string
	json.Unmarshal([]byte(strings.SplitN(got["/push"], "|", 2)[1]), &pp)
	if pp["token"] != "pptoken123456" || pp["template"] != "markdown" {
		t.Fatalf("pushplus: %v", pp)
	}
	if d := got["/ding"]; !strings.Contains(d, "timestamp=") || !strings.Contains(d, "sign=") || !strings.Contains(d, `"msgtype":"markdown"`) {
		t.Fatalf("dingtalk: %q", d)
	}
	if f := got["/feishu"]; !strings.Contains(f, `"sign"`) || !strings.Contains(f, `"msg_type":"text"`) {
		t.Fatalf("feishu: %q", f)
	}
	if !strings.Contains(got["/wecom"], "磁盘快满了") {
		t.Fatal("wecom")
	}
	// a robot that answers 200 with an error code is a failure
	if err := Send(ctx, Config{Kind: "wecom", URL: srv.URL + "/bad"}, VAPID{}, m); err == nil || !strings.Contains(err.Error(), "310000") {
		t.Fatalf("error code ignored: %v", err)
	}
	// Server酱³ keys go to the user's own host
	if g := sc3Re.FindStringSubmatch("sctp4321tABCDEF"); g == nil || g[1] != "4321" {
		t.Fatal(g)
	}
}
