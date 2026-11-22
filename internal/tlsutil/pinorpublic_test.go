package tlsutil

import (
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"testing"
)

// one TLS server (httptest's own CA): reached by its pin, by a public chain for the name,
// or refused when neither holds
func TestPinOrPublic(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok")) }))
	defer srv.Close()
	leaf := srv.Certificate()
	h := sha256.Sum256(leaf.Raw)
	pin := hex.EncodeToString(h[:])
	get := func(cfg *tls.Config, name string) error {
		cfg.ServerName = name
		c := &http.Client{Transport: &http.Transport{TLSClientConfig: cfg}}
		r, err := c.Get(srv.URL)
		if err == nil {
			r.Body.Close()
		}
		return err
	}
	publicRoots = x509.NewCertPool() // nothing trusted publicly yet
	if err := get(PinOrPublic(pin), "example.com"); err != nil {
		t.Fatalf("pinned: %v", err)
	}
	if err := get(PinOrPublic("00"+pin[2:]), "example.com"); err == nil {
		t.Fatal("a wrong pin and no public chain must fail")
	}
	// the same certificate as if a public CA had signed it for the name (httptest's is for example.com)
	publicRoots.AddCert(leaf)
	defer func() { publicRoots = nil }()
	if err := get(PinOrPublic("00"+pin[2:]), "example.com"); err != nil {
		t.Fatalf("public chain for the name: %v", err)
	}
	if err := get(PinOrPublic(""), "other.test"); err == nil {
		t.Fatal("a public chain for another name must fail")
	}
}
