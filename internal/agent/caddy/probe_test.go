package caddy

import (
	"context"
	"crypto/tls"
	"encoding/pem"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/wangsirco/berth/internal/panel/certkit"
	"github.com/wangsirco/berth/internal/tlsutil"
)

func TestProbeHTTPSTrustsGivenCA(t *testing.T) {
	ca, err := tlsutil.OpenLocalCA(t.TempDir(), "Berth")
	if err != nil {
		t.Fatal(err)
	}
	cert, key := ca.SigningCA()
	b, err := certkit.Signer{Cert: cert, Key: key, Allow: ca.Permits}.Issue(certkit.Fields{DNS: []string{"smoke.lan"}})
	if err != nil {
		t.Fatal(err)
	}
	pair, err := tls.X509KeyPair(b.CertPEM, b.KeyPEM)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{pair}}
	srv.StartTLS()
	defer srv.Close()
	_, p, _ := net.SplitHostPort(srv.Listener.Addr().String())
	port, _ := strconv.Atoi(p)
	caPEM := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ca.CADER()}))
	r := probeHTTPS(context.Background(), "smoke.lan", port, caPEM)
	if r.Cert == nil || !r.Cert.Verified {
		t.Fatalf("%+v %+v", r, r.Cert)
	}
	r = probeHTTPS(context.Background(), "smoke.lan", port, "")
	if r.Cert == nil || r.Cert.Verified {
		t.Fatal("verified without the CA")
	}
}
