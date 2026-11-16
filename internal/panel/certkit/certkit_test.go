package certkit

import (
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

	"software.sslmate.com/src/go-pkcs12"
)

func TestSelfSignedFields(t *testing.T) {
	b, err := SelfSigned(Fields{CN: "box", O: "Bufan", OU: "Ops", C: "nz", DNS: []string{"Box.lan", "*.box.lan"}, IPs: []string{"10.0.2.5"}, Days: 30, KeyType: "rsa2048", Usage: []string{"server", "client"}})
	if err != nil {
		t.Fatal(err)
	}
	chain, err := Read(b.CertPEM)
	if err != nil {
		t.Fatal(err)
	}
	c := chain[0]
	if c.Subject.CommonName != "box" || c.Subject.Organization[0] != "Bufan" || c.Subject.OrganizationalUnit[0] != "Ops" || c.Subject.Country[0] != "NZ" {
		t.Fatalf("subject %v", c.Subject)
	}
	if strings.Join(c.DNSNames, ",") != "box.lan,*.box.lan" || c.IPAddresses[0].String() != "10.0.2.5" {
		t.Fatalf("SAN %v %v", c.DNSNames, c.IPAddresses)
	}
	if d := c.NotAfter.Sub(c.NotBefore); d < 30*24*time.Hour || d > 31*24*time.Hour {
		t.Fatalf("validity %v", d)
	}
	in := Describe(chain)
	if in.KeyType != "rsa2048" || !in.SelfSigned || len(c.ExtKeyUsage) != 2 {
		t.Fatalf("%+v", in)
	}
	for _, f := range []Fields{{}, {CN: "x", Days: 99999}, {CN: "x", KeyType: "dsa"}, {CN: "x", IPs: []string{"nope"}}, {CN: "x", C: "NZL"}} {
		if _, err := SelfSigned(f); err == nil {
			t.Errorf("accepted %+v", f)
		}
	}
}

func testCA(t *testing.T, cn string, parent *x509.Certificate, parentKey *ecdsa.PrivateKey) (*x509.Certificate, *ecdsa.PrivateKey) {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tpl := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: cn},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(48 * time.Hour), IsCA: true, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageCertSign}
	if parent == nil {
		parent, parentKey = tpl, key
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, parent, &key.PublicKey, parentKey)
	if err != nil {
		t.Fatal(err)
	}
	c, _ := x509.ParseCertificate(der)
	return c, key
}

func TestIssueAndLocalOnly(t *testing.T) {
	root, rootKey := testCA(t, "root", nil, nil)
	s := Signer{Cert: root, Key: rootKey, Allow: func(dns []string, ips []net.IP) error {
		for _, d := range dns {
			if !strings.HasSuffix(d, ".lan") {
				return errors.New("local CA only signs local names")
			}
		}
		return nil
	}}
	b, err := s.Issue(Fields{DNS: []string{"nas.lan"}, Days: 3000})
	if err != nil {
		t.Fatal(err)
	}
	chain, _ := Read(b.CertPEM)
	if len(chain) != 2 || chain[0].CheckSignatureFrom(root) != nil || chain[0].NotAfter.After(root.NotAfter) {
		t.Fatal("bad chain or validity past the CA")
	}
	if _, err := s.Issue(Fields{DNS: []string{"example.com"}}); err == nil {
		t.Fatal("public name signed")
	}
}

func TestParsePEMAndPFXChains(t *testing.T) {
	root, rootKey := testCA(t, "root", nil, nil)
	mid, midKey := testCA(t, "mid", root, rootKey)
	s := Signer{Cert: mid, Key: midKey}
	b, err := s.Issue(Fields{DNS: []string{"shop.example.com"}})
	if err != nil {
		t.Fatal(err)
	}
	// PEM in any order, key in the same text, plus the root
	chain, _ := Read(b.CertPEM)
	shuffled := pemOf(root) + pemOf(chain[1]) + string(b.KeyPEM) + pemOf(chain[0])
	got, info, err := Parse([]byte(shuffled), nil, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	parsed, _ := Read(got.CertPEM)
	if parsed[0].Subject.CommonName != "shop.example.com" || parsed[1].Subject.CommonName != "mid" || parsed[2].Subject.CommonName != "root" || info.ChainLen != 3 {
		t.Fatalf("order %v", info)
	}
	// PFX with the chain
	leafKey, _ := parseKey(mustBlock(b.KeyPEM))
	pfx, err := pkcs12.Modern.Encode(leafKey, chain[0], []*x509.Certificate{mid}, "s3cret")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := Parse(nil, nil, pfx, "wrong"); err == nil || err.(*Problem).Code != "cert.pfx_password" {
		t.Fatalf("wrong password: %v", err)
	}
	_, info, err = Parse(nil, nil, pfx, "s3cret")
	if err != nil || info.ChainLen != 2 || info.Domains[0] != "shop.example.com" {
		t.Fatalf("pfx: %v %+v", err, info)
	}
	// the key must match
	other, _ := SelfSigned(Fields{CN: "other"})
	if _, _, err := Parse(b.CertPEM, other.KeyPEM, nil, ""); err == nil || err.(*Problem).Code != "cert.key_mismatch" {
		t.Fatalf("mismatch: %v", err)
	}
	// export round trip
	out, err := PFX(got, "x")
	if err != nil {
		t.Fatal(err)
	}
	if _, info, err := Parse(nil, nil, out, "x"); err != nil || info.ChainLen != 3 {
		t.Fatalf("export: %v %+v", err, info)
	}
}

func pemOf(c *x509.Certificate) string {
	return string(pemEncode(c.Raw))
}
