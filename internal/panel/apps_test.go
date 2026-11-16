package panel

import (
	"github.com/wangsirco/berth/internal/appstore"
	"strings"
	"testing"
	"time"
)

func TestAppInstallChecksAndUndo(t *testing.T) {
	appHealthWait = 100 * time.Millisecond
	c := newTC(t)
	fa := c.withFakeAgent()
	c.mkUser("root", "correct horse 9", "super")
	c.login("root", "correct horse 9")

	_, list := c.do("GET", "/api/v1/store/apps", nil)
	if !strings.Contains(toJSON(list), `"id":"it-tools"`) {
		t.Fatalf("catalog: %v", list)
	}

	// the port is taken: blocked at preview
	fa.mu.Lock()
	fa.listening = []int{8181}
	fa.mu.Unlock()
	r, out := c.do("POST", "/api/v1/hosts/local/changes/app.install?plan=1", map[string]any{"app": "it-tools"})
	if r.StatusCode != 409 || !strings.Contains(toJSON(out), "app.check_port") {
		t.Fatalf("port conflict: %d %v", r.StatusCode, out)
	}
	_, chk := c.do("POST", "/api/v1/hosts/local/store/check", map[string]any{"app": "it-tools"})
	if !strings.Contains(toJSON(chk), "8181") {
		t.Fatalf("check: %v", chk)
	}

	// another port works
	_, out = c.do("POST", "/api/v1/hosts/local/changes/app.install", map[string]any{"app": "it-tools", "name": "tools", "values": map[string]string{"HTTP_PORT": "8282"}})
	id, _ := out["id"].(string)
	if st := c.waitChange(id); st != "done" {
		ch, _ := c.app.pipe.Get(id)
		t.Fatalf("install: %s %v %s", st, out, ch.Log)
	}
	if !fa.called("compose up tools") {
		t.Fatal("not started")
	}
	ch, _ := c.app.pipe.Get(id)
	if !strings.Contains(string(ch.Result), `"container":"tools-it-tools-1"`) {
		t.Fatalf("site hint: %s", ch.Result)
	}
	_, inst := c.do("GET", "/api/v1/hosts/local/apps", nil)
	if !strings.Contains(toJSON(inst), `"name":"tools"`) || !strings.Contains(toJSON(inst), `"HTTP_PORT":"8282"`) {
		t.Fatalf("installed: %v", inst)
	}
	if r, _ := c.do("POST", "/api/v1/hosts/local/changes/app.install?plan=1", map[string]any{"app": "it-tools", "name": "tools"}); r.StatusCode != 409 {
		t.Fatal("same name installed twice")
	}

	// undo removes it; undoing that reinstalls with the same values
	c.do("POST", "/api/v1/changes/"+id+"/revert", nil)
	time.Sleep(300 * time.Millisecond)
	for i := 0; i < 50 && !fa.called("compose remove tools"); i++ {
		time.Sleep(20 * time.Millisecond)
	}
	if !fa.called("compose remove tools") {
		t.Fatal("undo did not remove")
	}
	if _, err := c.app.appInstance("local", "tools"); err == nil {
		t.Fatal("still recorded")
	}
}

func TestAppSecretsStayOutOfTheAuditLog(t *testing.T) {
	appHealthWait = 100 * time.Millisecond
	c := newTC(t)
	c.withFakeAgent()
	c.mkUser("root", "correct horse 9", "super")
	c.login("root", "correct horse 9")
	for _, a := range mustCatalog(t) {
		for _, f := range a.Fields {
			if f.Type == "password" {
				_, out := c.do("POST", "/api/v1/hosts/local/changes/app.install", map[string]any{"app": a.ID, "name": "sec", "values": map[string]string{f.Key: "VerySecretValue123"}})
				c.waitChange(out["id"].(string))
				_, audit := c.do("GET", "/api/v1/changes?limit=20", nil)
				if strings.Contains(toJSON(audit), "VerySecretValue123") {
					t.Fatal("password in the audit log")
				}
				_, sec := c.do("POST", "/api/v1/hosts/local/apps/sec/secrets", nil)
				if !strings.Contains(toJSON(sec), "VerySecretValue123") {
					t.Fatalf("secrets view: %v", sec)
				}
				return
			}
		}
	}
	t.Skip("no app with a password field yet")
}

func mustCatalog(t *testing.T) []appstore.App {
	l, err := appCatalog()
	if err != nil {
		t.Fatal(err)
	}
	return l
}

// The install wizard's "public" choice makes the site in the same step; removing the app
// takes its site along, and undoing the removal brings both back. "lan" opens the web port.
func TestAppInstallAccess(t *testing.T) {
	appHealthWait = 100 * time.Millisecond
	c := newTC(t)
	c.withFakeAgent()
	c.mkUser("root", "correct horse 9", "super")
	c.login("root", "correct horse 9")
	done := func(kind string, p any) string {
		t.Helper()
		r, out := c.do("POST", "/api/v1/hosts/local/changes/"+kind, p)
		id, _ := out["id"].(string)
		if id == "" {
			t.Fatalf("%s: %d %v", kind, r.StatusCode, out)
		}
		if st := c.waitChange(id); st != "done" {
			ch, _ := c.app.pipe.Get(id)
			t.Fatalf("%s: %s %v %s", kind, st, out, ch.Log)
		}
		return id
	}
	if r, _ := c.do("POST", "/api/v1/hosts/local/changes/app.install?plan=1", map[string]any{"app": "it-tools", "name": "pub", "access": "public"}); r.StatusCode != 400 {
		t.Fatal("public without a domain accepted")
	}
	id := done("app.install", map[string]any{"app": "it-tools", "name": "pub", "values": map[string]string{"HTTP_PORT": "8283"},
		"access": "public", "site": map[string]any{"domains": []string{"tools.example.net"}, "tls": map[string]any{"mode": "internal"}}})
	sites := c.app.sitesForProject("local", "pub")
	if len(sites) != 1 || sites[0].Primary() != "tools.example.net" || sites[0].Target.Container != "pub-it-tools-1" {
		ch, _ := c.app.pipe.Get(id)
		t.Fatalf("site: %+v %s", sites, ch.Log)
	}
	_, inst := c.do("GET", "/api/v1/hosts/local/apps", nil)
	if !strings.Contains(toJSON(inst), `"domain":"tools.example.net"`) || !strings.Contains(toJSON(inst), `"access":"public"`) {
		t.Fatalf("installed list: %v", inst)
	}
	rm := done("app.remove", map[string]any{"name": "pub"})
	if len(c.app.sitesForProject("local", "pub")) != 0 {
		t.Fatal("site left behind")
	}
	if r, out := c.do("POST", "/api/v1/changes/"+rm+"/revert", nil); r.StatusCode != 200 {
		t.Fatalf("revert remove: %d %v", r.StatusCode, out)
	}
	for i := 0; i < 100 && len(c.app.sitesForProject("local", "pub")) == 0; i++ {
		time.Sleep(50 * time.Millisecond)
	}
	if len(c.app.sitesForProject("local", "pub")) != 1 {
		_, l := c.do("GET", "/api/v1/changes?kind=app.install", nil)
		t.Fatalf("site not back after undoing the removal: %v", toJSON(l)[:min(3000, len(toJSON(l)))])
	}

	app, _ := findApp("it-tools")
	lan, _ := composeFor(app, "", "lan")
	if !strings.Contains(lan, "0.0.0.0:${HTTP_PORT}") || strings.Contains(lan, "127.0.0.1:${HTTP_PORT}") {
		t.Fatalf("lan compose: %s", lan)
	}
}
