package caddycfg

import (
	"strings"
	"testing"
)

func TestErrorPages(t *testing.T) {
	a := site(1, "a.example.com", Target{Type: "static"})
	a.Options.DenyIPs = []string{"203.0.113.0/24"}
	m := render(t, Env{}, a)
	j := js(m)
	if !strings.Contains(j, `"handler":"error"`) || !strings.Contains(j, `"error":"blocked"`) || strings.Contains(j, `"handler":"static_response","status_code":403`) {
		t.Fatal("IP blocks should go through the error page")
	}
	errs := js(at(m, "apps", "http", "servers", "https", "errors"))
	for _, want := range []string{"访问被拦截", "Access blocked", "页面不存在", "请求太频繁", "网站暂时打不开", "{http.request.uuid}", "{http.error.status_code} == 403"} {
		if !strings.Contains(errs, want) {
			t.Errorf("error pages lack %q", want)
		}
	}
	// nothing the visitor controls is written into the page
	for _, bad := range []string{"{http.request.host}", "{http.request.uri}", "{http.request.header", "{http.error.message}<"} {
		if strings.Contains(errs, bad) {
			t.Errorf("error page echoes %s", bad)
		}
	}
	// the stopped-site page wins over the generic 503
	routes := at(m, "apps", "http", "servers", "https", "errors", "routes").([]any)
	if !strings.Contains(js(routes[0]), StoppedMessage) {
		t.Fatal("stopped page must come first")
	}
}
