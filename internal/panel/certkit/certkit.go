// Package certkit makes and reads certificates: self-signed with editable fields,
// signed by the panel's local CA, or imported from PEM / PFX bundles.
package certkit

import (
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"strings"
	"time"

	"software.sslmate.com/src/go-pkcs12"
)

// Fields are what a user can set on a self-signed or internal certificate.
type Fields struct {
	CN      string   `json:"cn"`
	O       string   `json:"o,omitempty"`
	OU      string   `json:"ou,omitempty"`
	C       string   `json:"c,omitempty"`
	DNS     []string `json:"dns"`
	IPs     []string `json:"ips,omitempty"`
	Days    int      `json:"days"`
	KeyType string   `json:"key_type"`        // ec256 | ec384 | rsa2048 | rsa4096
	Usage   []string `json:"usage,omitempty"` // server | client
	IsCA    bool     `json:"is_ca,omitempty"` // a self-signed CA (for client certificates)
}

// Info describes a certificate for the UI.
type Info struct {
	Subject     string    `json:"subject"`
	Issuer      string    `json:"issuer"`
	Domains     []string  `json:"domains"`
	IPs         []string  `json:"ips,omitempty"`
	NotBefore   time.Time `json:"not_before"`
	NotAfter    time.Time `json:"not_after"`
	Fingerprint string    `json:"fingerprint"` // sha256 of the leaf
	KeyType     string    `json:"key_type"`
	ChainLen    int       `json:"chain_len"`
	SelfSigned  bool      `json:"self_signed"`
}

// Bundle is a certificate chain (leaf first) and its key, PEM-encoded.
type Bundle struct {
	CertPEM []byte // leaf + intermediates
	KeyPEM  []byte
}

type Problem struct{ Code string }

func (p *Problem) Error() string { return p.Code }

func bad(code string) error { return &Problem{Code: code} }

func (f *Fields) normalize() error {
	f.CN = strings.TrimSpace(f.CN)
	var dns []string
	for _, d := range f.DNS {
		if d = strings.ToLower(strings.TrimSpace(d)); d != "" {
			dns = append(dns, d)
		}
	}
	f.DNS = dns
	for _, ip := range f.IPs {
		if net.ParseIP(strings.TrimSpace(ip)) == nil {
			return bad("cert.bad_ip")
		}
	}
	if f.CN == "" {
		switch {
		case len(f.DNS) > 0:
			f.CN = f.DNS[0]
		case len(f.IPs) > 0:
			f.CN = f.IPs[0]
		}
	}
	if f.CN == "" {
		return bad("cert.no_name")
	}
	if f.Days == 0 {
		f.Days = 365
	}
	if f.Days < 1 || f.Days > 3650 {
		return bad("cert.bad_days")
	}
	if f.KeyType == "" {
		f.KeyType = "ec256"
	}
	if len(f.C) > 2 {
		return bad("cert.bad_country")
	}
	if len(f.Usage) == 0 {
		f.Usage = []string{"server"}
	}
	return nil
}

func newKey(kt string) (crypto.Signer, error) {
	switch kt {
	case "ec256":
		return ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	case "ec384":
		return ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	case "rsa2048":
		return rsa.GenerateKey(rand.Reader, 2048)
	case "rsa4096":
		return rsa.GenerateKey(rand.Reader, 4096)
	}
	return nil, bad("cert.bad_key_type")
}

func template(f Fields) *x509.Certificate {
	serial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	name := pkix.Name{CommonName: f.CN}
	if f.O != "" {
		name.Organization = []string{f.O}
	}
	if f.OU != "" {
		name.OrganizationalUnit = []string{f.OU}
	}
	if f.C != "" {
		name.Country = []string{strings.ToUpper(f.C)}
	}
	t := &x509.Certificate{
		SerialNumber: serial, Subject: name,
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Duration(f.Days) * 24 * time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, DNSNames: f.DNS, BasicConstraintsValid: true,
	}
	for _, ip := range f.IPs {
		t.IPAddresses = append(t.IPAddresses, net.ParseIP(strings.TrimSpace(ip)))
	}
	for _, u := range f.Usage {
		switch u {
		case "server":
			t.ExtKeyUsage = append(t.ExtKeyUsage, x509.ExtKeyUsageServerAuth)
		case "client":
			t.ExtKeyUsage = append(t.ExtKeyUsage, x509.ExtKeyUsageClientAuth)
		}
	}
	if f.IsCA {
		t.IsCA = true
		t.KeyUsage |= x509.KeyUsageCertSign | x509.KeyUsageCRLSign
	}
	return t
}

func encode(der [][]byte, key crypto.Signer) (Bundle, error) {
	var cb bytes.Buffer
	for _, d := range der {
		pem.Encode(&cb, &pem.Block{Type: "CERTIFICATE", Bytes: d})
	}
	kd, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return Bundle{}, err
	}
	return Bundle{CertPEM: cb.Bytes(), KeyPEM: pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: kd})}, nil
}

// SelfSigned makes a certificate signed by its own key.
func SelfSigned(f Fields) (Bundle, error) {
	if err := f.normalize(); err != nil {
		return Bundle{}, err
	}
	key, err := newKey(f.KeyType)
	if err != nil {
		return Bundle{}, err
	}
	t := template(f)
	der, err := x509.CreateCertificate(rand.Reader, t, t, key.Public(), key)
	if err != nil {
		return Bundle{}, err
	}
	return encode([][]byte{der}, key)
}

// Signer is a CA that can sign leaves (the panel's local CA).
type Signer struct {
	Cert  *x509.Certificate
	Key   crypto.Signer
	Allow func(dns []string, ips []net.IP) error // name constraints, checked before signing
}

// Issue signs a leaf with the CA. The chain includes the CA certificate.
func (s Signer) Issue(f Fields) (Bundle, error) {
	if err := f.normalize(); err != nil {
		return Bundle{}, err
	}
	f.IsCA = false
	t := template(f)
	if s.Allow != nil {
		if err := s.Allow(t.DNSNames, t.IPAddresses); err != nil {
			return Bundle{}, err
		}
	}
	if t.NotAfter.After(s.Cert.NotAfter) {
		t.NotAfter = s.Cert.NotAfter
	}
	key, err := newKey(f.KeyType)
	if err != nil {
		return Bundle{}, err
	}
	der, err := x509.CreateCertificate(rand.Reader, t, s.Cert, key.Public(), s.Key)
	if err != nil {
		return Bundle{}, err
	}
	return encode([][]byte{der, s.Cert.Raw}, key)
}

// Parse reads an uploaded certificate: PEM (certificate chain and key, in one text or
// two) or PFX/PKCS#12 with a password. The chain is ordered leaf first and the key must
// match the leaf.
func Parse(certText, keyText, pfx []byte, password string) (Bundle, Info, error) {
	var certs []*x509.Certificate
	var key crypto.PrivateKey
	if len(pfx) > 0 {
		k, leaf, chain, err := pkcs12.DecodeChain(pfx, password)
		if err != nil {
			if errors.Is(err, pkcs12.ErrIncorrectPassword) {
				return Bundle{}, Info{}, bad("cert.pfx_password")
			}
			return Bundle{}, Info{}, bad("cert.bad_pfx")
		}
		key = k
		certs = append([]*x509.Certificate{leaf}, chain...)
	} else {
		all := append(append([]byte{}, certText...), '\n')
		all = append(all, keyText...)
		for len(all) > 0 {
			var b *pem.Block
			b, all = pem.Decode(all)
			if b == nil {
				break
			}
			switch {
			case b.Type == "CERTIFICATE":
				c, err := x509.ParseCertificate(b.Bytes)
				if err != nil {
					return Bundle{}, Info{}, bad("cert.bad_pem")
				}
				certs = append(certs, c)
			case strings.HasSuffix(b.Type, "PRIVATE KEY"):
				k, err := parseKey(b)
				if err != nil {
					return Bundle{}, Info{}, err
				}
				key = k
			}
		}
	}
	if len(certs) == 0 {
		return Bundle{}, Info{}, bad("cert.no_certificate")
	}
	if key == nil {
		return Bundle{}, Info{}, bad("cert.no_key")
	}
	signer, ok := key.(crypto.Signer)
	if !ok {
		return Bundle{}, Info{}, bad("cert.bad_key")
	}
	certs = orderChain(certs, signer)
	if certs == nil {
		return Bundle{}, Info{}, bad("cert.key_mismatch")
	}
	var der [][]byte
	for _, c := range certs {
		der = append(der, c.Raw)
	}
	b, err := encode(der, signer)
	if err != nil {
		return Bundle{}, Info{}, err
	}
	return b, Describe(certs), nil
}

func parseKey(b *pem.Block) (crypto.PrivateKey, error) {
	if strings.Contains(b.Type, "ENCRYPTED") {
		return nil, bad("cert.key_encrypted")
	}
	if k, err := x509.ParsePKCS8PrivateKey(b.Bytes); err == nil {
		return k, nil
	}
	if k, err := x509.ParsePKCS1PrivateKey(b.Bytes); err == nil {
		return k, nil
	}
	if k, err := x509.ParseECPrivateKey(b.Bytes); err == nil {
		return k, nil
	}
	return nil, bad("cert.bad_key")
}

// orderChain puts the certificate matching key first, then follows issuers.
func orderChain(certs []*x509.Certificate, key crypto.Signer) []*x509.Certificate {
	var leaf *x509.Certificate
	rest := []*x509.Certificate{}
	for _, c := range certs {
		if leaf == nil && samePublic(c.PublicKey, key.Public()) {
			leaf = c
			continue
		}
		rest = append(rest, c)
	}
	if leaf == nil {
		return nil
	}
	out := []*x509.Certificate{leaf}
	cur := leaf
	for len(rest) > 0 {
		found := -1
		for i, c := range rest {
			if bytes.Equal(cur.RawIssuer, c.RawSubject) && cur.CheckSignatureFrom(c) == nil {
				found = i
				break
			}
		}
		if found < 0 {
			break // unrelated extras are dropped
		}
		cur = rest[found]
		out = append(out, cur)
		rest = append(rest[:found], rest[found+1:]...)
	}
	return out
}

func samePublic(a, b crypto.PublicKey) bool {
	type eq interface{ Equal(crypto.PublicKey) bool }
	if e, ok := a.(eq); ok {
		return e.Equal(b)
	}
	return false
}

// Describe summarises a chain (leaf first).
func Describe(chain []*x509.Certificate) Info {
	l := chain[0]
	fp := sha256.Sum256(l.Raw)
	in := Info{Subject: l.Subject.String(), Issuer: l.Issuer.String(), Domains: l.DNSNames, NotBefore: l.NotBefore, NotAfter: l.NotAfter,
		Fingerprint: hex.EncodeToString(fp[:]), ChainLen: len(chain), SelfSigned: bytes.Equal(l.RawIssuer, l.RawSubject)}
	if len(in.Domains) == 0 && l.Subject.CommonName != "" {
		in.Domains = []string{l.Subject.CommonName}
	}
	for _, ip := range l.IPAddresses {
		in.IPs = append(in.IPs, ip.String())
	}
	switch k := l.PublicKey.(type) {
	case *ecdsa.PublicKey:
		in.KeyType = fmt.Sprintf("ec%d", k.Curve.Params().BitSize)
	case *rsa.PublicKey:
		in.KeyType = fmt.Sprintf("rsa%d", k.N.BitLen())
	default:
		in.KeyType = "other"
	}
	return in
}

// Read parses a PEM chain (no key).
func Read(certPEM []byte) ([]*x509.Certificate, error) {
	var out []*x509.Certificate
	for {
		var b *pem.Block
		b, certPEM = pem.Decode(certPEM)
		if b == nil {
			break
		}
		if b.Type == "CERTIFICATE" {
			c, err := x509.ParseCertificate(b.Bytes)
			if err != nil {
				return nil, err
			}
			out = append(out, c)
		}
	}
	if len(out) == 0 {
		return nil, bad("cert.no_certificate")
	}
	return out, nil
}

// PFX packs a bundle for export.
func PFX(b Bundle, password string) ([]byte, error) {
	chain, err := Read(b.CertPEM)
	if err != nil {
		return nil, err
	}
	blk, _ := pem.Decode(b.KeyPEM)
	if blk == nil {
		return nil, bad("cert.no_key")
	}
	k, err := parseKey(blk)
	if err != nil {
		return nil, err
	}
	return pkcs12.Modern.Encode(k, chain[0], chain[1:], password)
}

func pemEncode(der []byte) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func mustBlock(b []byte) *pem.Block {
	blk, _ := pem.Decode(b)
	return blk
}
