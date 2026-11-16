package panel

import (
	"net/http"
	"net/http/cookiejar"
	"testing"
)

func TestSnippets(t *testing.T) {
	c := newTC(t)
	c.mkUser("boss", "correct horse 9", "super")
	c.mkUser("ops", "correct horse 9", "viewer")
	c.login("ops", "correct horse 9")
	if resp, _ := c.do("POST", "/api/v1/snippets", map[string]any{"name": "x", "command": "ls", "shared": true}); resp.StatusCode != 403 {
		t.Fatal("non-admin shared a command")
	}
	resp, mine := c.do("POST", "/api/v1/snippets", map[string]any{"name": "跟踪日志", "command": "journalctl -fu {{服务:nginx}}"})
	if resp.StatusCode != 200 {
		t.Fatal(mine)
	}
	// boss shares one; ops sees it but cannot change it, and does not see boss's own
	jar, _ := cookiejar.New(nil)
	c2 := &tc{t: t, app: c.app, srv: c.srv, hc: &http.Client{Transport: c.srv.Client().Transport, Jar: jar}}
	c2.login("boss", "correct horse 9")
	c2.do("POST", "/api/v1/snippets", map[string]any{"name": "重启 Docker", "command": "systemctl restart docker", "shared": true, "danger": true})
	c2.do("POST", "/api/v1/snippets", map[string]any{"name": "private", "command": "id"})
	_, list := c.do("GET", "/api/v1/snippets", nil)
	items := list["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("ops sees %v", items)
	}
	var shared map[string]any
	for _, x := range items {
		if m := x.(map[string]any); m["shared"] == true {
			shared = m
		}
	}
	if shared == nil || shared["mine"] == true || shared["danger"] != true {
		t.Fatal(items)
	}
	id := itoa(int64(shared["id"].(float64)))
	if resp, _ := c.do("DELETE", "/api/v1/snippets/"+id, nil); resp.StatusCode != 404 {
		t.Fatal("ops deleted a shared command")
	}
	// using it moves it up
	c.do("POST", "/api/v1/snippets/"+id+"/used", nil)
	_, list = c.do("GET", "/api/v1/snippets", nil)
	if first := list["items"].([]any)[0].(map[string]any); first["id"] != shared["id"] || first["uses"].(float64) != 1 {
		t.Fatalf("order: %v", list)
	}
}
