package panel

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"math/big"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/wangsirco/berth/internal/panel/certkit"
)

func (c *tc) withLocalCA() {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Berth test CA"}, IsCA: true, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageCertSign, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(3650 * 24 * time.Hour)}
	der, _ := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	ca, _ := x509.ParseCertificate(der)
	c.app.SetLocalSigner(certkit.Signer{Cert: ca, Key: key, Allow: func(dns []string, _ []net.IP) error {
		for _, d := range dns {
			if !strings.HasSuffix(d, ".lan") {
				return errors.New("the local CA only signs local names")
			}
		}
		return nil
	}})
}

func TestCertLifecycle(t *testing.T) {
	c := newTC(t)
	fa := c.withFakeAgent()
	c.withLocalCA()
	c.mkUser("root", "correct horse 9", "super")
	c.login("root", "correct horse 9")

	// the local CA refuses public names at preview
	r, out := c.do("POST", "/api/v1/hosts/local/changes/cert.create?plan=1", map[string]any{"source": "internal", "fields": map[string]any{"dns": []string{"shop.example.com"}}})
	if r.StatusCode != 400 || !strings.Contains(toJSON(out), "cert.not_local") {
		t.Fatalf("public name: %d %v", r.StatusCode, out)
	}
	_, out = c.do("POST", "/api/v1/hosts/local/changes/cert.create", map[string]any{"source": "internal", "name": "nas", "fields": map[string]any{"dns": []string{"nas.lan"}, "days": 90}})
	if out["status"] != "done" {
		t.Fatalf("create: %v", out)
	}
	cert, _ := c.app.cert(1)
	fp1 := cert.Info.Fingerprint
	if cert.Source != "internal" || !cert.AutoRenew || cert.Info.ChainLen != 2 {
		t.Fatalf("%+v", cert)
	}

	// a site using it gets the files pushed before Caddy validates
	_, out = c.do("POST", "/api/v1/hosts/local/changes/site.create", map[string]any{"domains": []string{"nas.lan"}, "target": map[string]any{"type": "static"}, "tls": map[string]any{"mode": "custom", "cert_id": 1}})
	if out["status"] != "done" || !fa.called("PUT cert 1") || !strings.Contains(fa.config(), "/data/berth-certs/1/") {
		t.Fatalf("site: %v %s", out, fa.config())
	}
	// deleting a certificate in use is refused
	if r, _ := c.do("POST", "/api/v1/hosts/local/changes/cert.delete", map[string]any{"id": 1}); r.StatusCode != 409 {
		t.Fatalf("delete in use: %d", r.StatusCode)
	}

	// reissue keeps the old one in history; undo switches back
	_, out = c.do("POST", "/api/v1/hosts/local/changes/cert.reissue", map[string]any{"id": 1, "fields": map[string]any{"dns": []string{"nas.lan", "files.lan"}, "days": 30}})
	if out["status"] != "done" {
		t.Fatalf("reissue: %v", out)
	}
	cert, _ = c.app.cert(1)
	if len(cert.History) != 1 || cert.History[0].Fingerprint != fp1 || len(cert.Info.Domains) != 2 {
		t.Fatalf("after reissue: %+v", cert.Info)
	}
	c.do("POST", "/api/v1/changes/"+out["id"].(string)+"/revert", nil)
	cert, _ = c.app.cert(1)
	if cert.Info.Fingerprint != fp1 {
		t.Fatal("undo did not switch back")
	}

	// export needs a session (not a token) and is recorded
	_, out = c.do("POST", "/api/v1/certs/1/export", map[string]string{"format": "pfx", "password": "x"})
	if out["pfx"] == nil {
		t.Fatalf("export: %v", out)
	}
	_, list := c.do("GET", "/api/v1/changes?limit=5", nil)
	if !strings.Contains(toJSON(list), `"kind":"cert.export"`) {
		t.Fatal("export not in the audit log")
	}

	// listing hides history keys and includes Caddy's ACME certificates
	_, all := c.do("GET", "/api/v1/certs", nil)
	js := toJSON(all)
	if strings.Contains(js, "key_sealed\":\"A") || !strings.Contains(js, "shop.example.com") || !strings.Contains(js, `"used_by":["local/nas.lan"]`) {
		t.Fatalf("list: %s", js)
	}
}

func TestCertDeleteRestoreAndRenew(t *testing.T) {
	c := newTC(t)
	c.withFakeAgent()
	c.mkUser("root", "correct horse 9", "super")
	c.login("root", "correct horse 9")
	_, out := c.do("POST", "/api/v1/hosts/local/changes/cert.create", map[string]any{"source": "self", "fields": map[string]any{"cn": "box", "dns": []string{"box.example.com"}, "days": 10}})
	if out["status"] != "done" {
		t.Fatalf("create: %v", out)
	}
	before, _ := c.app.cert(1)

	// due within 30 days: renewed, old version kept, recorded
	c.app.renewDue(context.Background())
	after, _ := c.app.cert(1)
	if after.Info.Fingerprint == before.Info.Fingerprint || len(after.History) != 1 {
		t.Fatal("not renewed")
	}
	_, list := c.do("GET", "/api/v1/changes?limit=5", nil)
	if !strings.Contains(toJSON(list), `"kind":"cert.renew"`) {
		t.Fatal("renewal not recorded")
	}

	_, out = c.do("POST", "/api/v1/hosts/local/changes/cert.delete", map[string]any{"id": 1})
	if out["status"] != "done" {
		t.Fatalf("delete: %v", out)
	}
	if _, err := c.app.cert(1); err == nil {
		t.Fatal("still there")
	}
	c.do("POST", "/api/v1/changes/"+out["id"].(string)+"/revert", nil)
	back, err := c.app.cert(1)
	if err != nil || back.Info.Fingerprint != after.Info.Fingerprint || back.Source != "self" || len(back.History) != 1 || !back.AutoRenew {
		t.Fatalf("restore: %v %+v", err, back)
	}

	// import: PEM with the key
	b, _ := certkit.SelfSigned(certkit.Fields{CN: "imp.example.com", DNS: []string{"imp.example.com"}})
	_, out = c.do("POST", "/api/v1/hosts/local/changes/cert.create", map[string]any{"source": "import", "cert_pem": string(b.CertPEM), "key_pem": string(b.KeyPEM)})
	if out["status"] != "done" {
		t.Fatalf("import: %v", out)
	}
	// the key never reaches the audit log
	_, list = c.do("GET", "/api/v1/changes?limit=10", nil)
	if strings.Contains(toJSON(list), "PRIVATE KEY") {
		t.Fatal("private key in the audit log")
	}
}

func TestInternalSiteGetsPanelCACert(t *testing.T) {
	c := newTC(t)
	fa := c.withFakeAgent()
	c.withLocalCA()
	c.mkUser("root", "correct horse 9", "super")
	c.login("root", "correct horse 9")
	_, out := c.do("POST", "/api/v1/hosts/local/changes/site.create", map[string]any{"domains": []string{"wiki.lan"}, "target": map[string]any{"type": "static"}, "tls": map[string]any{"mode": "internal"}})
	if out["status"] != "done" {
		t.Fatalf("%v", out)
	}
	s, _ := c.app.site("local", 1)
	if s.TLS.CertID == 0 || !strings.Contains(fa.config(), "/data/berth-certs/") || strings.Contains(fa.config(), `"module":"internal"`) {
		t.Fatalf("site %+v config %s", s.TLS, fa.config())
	}
	// editing keeps the same certificate
	c.do("POST", "/api/v1/hosts/local/changes/site.update", map[string]any{"id": 1, "domains": []string{"wiki.lan"}, "target": map[string]any{"type": "static", "spa": true}, "tls": map[string]any{"mode": "internal"}})
	s2, _ := c.app.site("local", 1)
	if s2.TLS.CertID != s.TLS.CertID {
		t.Fatal("a new certificate was issued on edit")
	}
	// a public name falls back to Caddy's internal issuer
	_, out = c.do("POST", "/api/v1/hosts/local/changes/site.create", map[string]any{"domains": []string{"staging.example.com"}, "target": map[string]any{"type": "static"}, "tls": map[string]any{"mode": "internal"}})
	if out["status"] != "done" || !strings.Contains(fa.config(), `"module":"internal"`) {
		t.Fatalf("fallback: %v", out)
	}
}
