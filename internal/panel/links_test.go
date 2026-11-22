package panel

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wangsirco/berth/internal/store"
)

// newPanelTC: a test panel with a certificate of its own (httptest's is shared by every server)
func newPanelTC(t *testing.T) *tc {
	db, err := store.Open(filepath.Join(t.TempDir(), "b.db"))
	if err != nil {
		t.Fatal(err)
	}
	app := New(Config{DataDir: t.TempDir(), AllowCIDRs: []string{"127.0.0.0/8"}}, db, nil, nil)
	app.masterKey = bytes.Repeat([]byte{7}, 32)
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	serial, _ := rand.Int(rand.Reader, big.NewInt(1<<62))
	tpl := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: "berth-test"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, IsCA: true, BasicConstraintsValid: true}
	der, _ := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	srv := httptest.NewUnstartedServer(app.Handler())
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}}
	srv.StartTLS()
	t.Cleanup(srv.Close)
	app.caPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}) // what peers pin
	jar, _ := cookiejar.New(nil)
	hc := srv.Client()
	hc.Jar = jar
	return &tc{t: t, app: app, srv: srv, hc: hc}
}

// two panels in one test: A manages, B hands its "local" over
func linkPair(t *testing.T) (a, b *tc, slug string, aID, bID float64) {
	a, b = newPanelTC(t), newPanelTC(t)
	for _, c := range []*tc{a, b} {
		c.mkUser("root", "correct horse 9", RoleSuper)
		c.login("root", "correct horse 9")
	}
	a.app.setSetting("panel.name", "Main Office")
	b.app.setSetting("panel.name", "Home mini")

	// 1. A makes a code
	r, out := a.do("POST", "/api/v1/links/code", map[string]string{})
	if r.StatusCode != 200 || !strings.HasPrefix(out["code"].(string), linkCodeTag) {
		t.Fatalf("code: %d %v", r.StatusCode, out)
	}
	code := out["code"].(string)
	// 2. B looks at it (and reaches A)
	r, ins := b.do("POST", "/api/v1/links/inspect", map[string]string{"code": code})
	if r.StatusCode != 200 || ins["name"] != "Main Office" || ins["sas"] == "" {
		t.Fatalf("inspect: %d %v", r.StatusCode, ins)
	}
	// 3. B accepts (a fresh login counts as having confirmed who it is)
	b.do("POST", "/api/v1/me/reauth", map[string]string{"password": "correct horse 9"})
	r, acc := b.do("POST", "/api/v1/links/accept", map[string]any{"code": code, "hosts": []string{"local", "not-a-host"}})
	if r.StatusCode != 200 || acc["sas"] != ins["sas"] {
		t.Fatalf("accept: %d %v", r.StatusCode, acc)
	}
	bID = acc["id"].(float64)
	// the code was single use
	if r, _ := b.do("POST", "/api/v1/links/inspect", map[string]string{"code": code}); r.StatusCode == 200 {
		t.Fatal("a used code still answers")
	}
	// 4. A sees it waiting, with the same check code and only real hosts
	_, list := a.do("GET", "/api/v1/links", nil)
	l := list["items"].([]any)[0].(map[string]any)
	if l["status"] != "confirm" || l["sas"] != ins["sas"] || l["name"] != "Home mini" || len(l["hosts"].([]any)) != 1 {
		t.Fatalf("A's view: %v", l)
	}
	aID = l["id"].(float64)
	slug = l["slug"].(string)
	// nothing goes through before A confirms
	if r, _ := a.do("GET", "/api/v1/hosts/local@"+slug+"/changes/x", nil); r.StatusCode != 404 {
		t.Fatalf("before confirm: %d", r.StatusCode)
	}
	a.do("POST", "/api/v1/me/reauth", map[string]string{"password": "correct horse 9"})
	if r, out := a.do("POST", "/api/v1/links/"+itoa(int64(aID))+"/confirm", nil); r.StatusCode != 200 {
		t.Fatalf("confirm: %d %v", r.StatusCode, out)
	}
	return
}

func TestPanelLink(t *testing.T) {
	a, b, slug, _, bID := linkPair(t)
	_, bl := b.do("GET", "/api/v1/links", nil)
	if s := bl["items"].([]any)[0].(map[string]any)["status"]; s != "active" {
		t.Fatalf("B not active: %v", s)
	}
	// a request for B's server reaches B's own API (B answers about its own change ids)
	r, out := a.do("GET", "/api/v1/hosts/local@"+slug+"/changes/nope", nil)
	if r.StatusCode != 404 || out["error"].(map[string]any)["code"] != "change.not_found" {
		t.Fatalf("forward: %d %v", r.StatusCode, out)
	}
	// B's servers show up in A's list as name@peer
	_, hosts := a.do("GET", "/api/v1/hosts", nil)
	found := false
	for _, h := range hosts["items"].([]any) {
		if h.(map[string]any)["name"] == "local@"+slug {
			found = true
		}
	}
	if !found {
		t.Fatalf("peer host missing: %v", hosts)
	}
	// no settings, users or tokens of B: the link acts as an admin, never super
	r, out = a.do("POST", "/api/v1/hosts/local@"+slug+"/changes/settings.update?plan=1", map[string]any{"values": map[string]string{"panel.name": "pwned"}})
	if r.StatusCode != 403 {
		t.Fatalf("settings through the link: %d %v", r.StatusCode, out)
	}
	if b.app.setting("panel.name") != "Home mini" {
		t.Fatal("B's settings changed")
	}
	// only handed-over servers
	if r, _ := a.do("GET", "/api/v1/hosts/other@"+slug+"/changes/x", nil); r.StatusCode != 404 {
		t.Fatalf("a server not handed over: %d", r.StatusCode)
	}
	// B's forward endpoint refuses anything unsigned, and a replay
	req, _ := http.NewRequest("GET", b.srv.URL+"/link-api/fwd/hosts/local/changes/x", nil)
	if resp, _ := b.srv.Client().Do(req); resp.StatusCode != 404 {
		t.Fatalf("unsigned: %d", resp.StatusCode)
	}
	al, _ := a.app.link(`WHERE role=?`, linkRoleMain)
	signed, _ := http.NewRequest("POST", b.srv.URL+"/link-api/ping", bytes.NewReader([]byte("{}")))
	linkSign(signed, al, []byte("{}"))
	again := signed.Clone(signed.Context())
	again.Body = ioNop([]byte("{}"))
	if resp, _ := b.srv.Client().Do(signed); resp.StatusCode != 200 {
		t.Fatalf("signed ping: %d", resp.StatusCode)
	}
	if resp, _ := b.srv.Client().Do(again); resp.StatusCode == 200 {
		t.Fatal("a replayed request was accepted")
	}
	// B's audit names who did it
	var n int
	b.app.db.QueryRow(`SELECT COUNT(*) FROM changes WHERE kind='link.active' AND actor_type='link'`).Scan(&n)
	if n != 1 {
		t.Fatal("B's audit has no link entry")
	}
	// B takes its servers back: A loses them at once
	if r, out := b.do("DELETE", "/api/v1/links/"+itoa(int64(bID)), nil); r.StatusCode != 200 || out["told"] != true {
		t.Fatalf("unlink: %d %v", r.StatusCode, out)
	}
	if r, _ := a.do("GET", "/api/v1/hosts/local@"+slug+"/changes/nope", nil); r.StatusCode != 404 {
		t.Fatalf("after unlink: %d", r.StatusCode)
	}
	if l := a.app.links(`WHERE role=?`, linkRoleMain); len(l) != 0 {
		t.Fatal("A still has the link")
	}
}

// a panel can be handed to one main panel at a time, and never to itself
func TestPanelLinkOnlyOneMain(t *testing.T) {
	_, b, _, _, _ := linkPair(t)
	c := newPanelTC(t)
	c.mkUser("root", "correct horse 9", RoleSuper)
	c.login("root", "correct horse 9")
	_, out := c.do("POST", "/api/v1/links/code", map[string]string{})
	if r, out := b.do("POST", "/api/v1/links/inspect", map[string]string{"code": out["code"].(string)}); r.StatusCode != 409 {
		t.Fatalf("second main: %d %v", r.StatusCode, out)
	}
	_, own := b.do("POST", "/api/v1/links/code", map[string]string{})
	if r, _ := b.do("POST", "/api/v1/links/inspect", map[string]string{"code": own["code"].(string)}); r.StatusCode != 400 {
		t.Fatalf("own code: %d", r.StatusCode)
	}
}
