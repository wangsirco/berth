// Copied from github.com/wangsirco/wharf (internal/tlsutil/tlsutil.go), Copyright (c) Wharf authors, MIT License.

// Package tlsutil creates self-signed certificates and pins them by fingerprint.
package tlsutil

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// LoadOrCreate returns a certificate from certFile/keyFile, generating a
// self-signed one (10 years) if they don't exist.
func LoadOrCreate(certFile, keyFile, commonName string) (tls.Certificate, error) {
	if _, err := os.Stat(certFile); err != nil {
		if err := generate(certFile, keyFile, commonName); err != nil {
			return tls.Certificate{}, err
		}
	}
	return tls.LoadX509KeyPair(certFile, keyFile)
}

func generate(certFile, keyFile, cn string) error {
	certPEM, keyPEM, err := SelfSigned(cn, "Berth", "localhost")
	if err != nil {
		return err
	}
	_ = os.MkdirAll(filepath.Dir(certFile), 0o700)
	if err := os.WriteFile(keyFile, keyPEM, 0o600); err != nil {
		return err
	}
	return os.WriteFile(certFile, certPEM, 0o644)
}

// SelfSigned returns a 10-year self-signed ECDSA certificate and key (PEM) for cn and extra names.
func SelfSigned(cn, org string, names ...string) (certPEM, keyPEM []byte, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	serial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	tpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: cn, Organization: []string{org}},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().AddDate(10, 0, 0),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     append([]string{cn}, names...),
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		return nil, nil, err
	}
	kb, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kb}), nil
}

// Fingerprint is the hex SHA-256 of the leaf certificate (DER).
func Fingerprint(c tls.Certificate) string {
	if len(c.Certificate) == 0 {
		return ""
	}
	h := sha256.Sum256(c.Certificate[0])
	return hex.EncodeToString(h[:])
}

// PinnedConfig accepts exactly the certificate with the given fingerprint
// (colons/case ignored), regardless of CA or hostname.
func PinnedConfig(fingerprint string) *tls.Config {
	want := strings.ToLower(strings.ReplaceAll(fingerprint, ":", ""))
	return &tls.Config{
		InsecureSkipVerify: true, // replaced by the pin check below
		VerifyPeerCertificate: func(raw [][]byte, _ [][]*x509.Certificate) error {
			if len(raw) == 0 {
				return errors.New("no certificate")
			}
			h := sha256.Sum256(raw[0])
			if hex.EncodeToString(h[:]) != want {
				return errors.New("agent certificate fingerprint mismatch")
			}
			return nil
		},
	}
}

// PinOrPublic trusts a server two ways, so the same peer works on the LAN and through a
// public name: a certificate in its chain matches the pinned fingerprint (its own CA, reached
// directly), or the chain is valid for the name dialled under the system roots (a public
// certificate: a reverse proxy, Cloudflare, Let's Encrypt in front of it). Who the peer is
// gets proven above TLS (the pairing secret), so a valid public certificate is enough here.
func PinOrPublic(fingerprint string) *tls.Config {
	want := strings.ToLower(strings.ReplaceAll(fingerprint, ":", ""))
	return &tls.Config{
		MinVersion:         tls.VersionTLS12,
		InsecureSkipVerify: true, // replaced by VerifyConnection
		VerifyConnection: func(cs tls.ConnectionState) error {
			return CheckPinOrPublic(cs, want)
		},
	}
}

// CheckPinOrPublic is PinOrPublic's test, for a connection already made.
func CheckPinOrPublic(cs tls.ConnectionState, fingerprint string) error {
	if len(cs.PeerCertificates) == 0 {
		return errors.New("no certificate")
	}
	want := strings.ToLower(strings.ReplaceAll(fingerprint, ":", ""))
	for _, c := range cs.PeerCertificates {
		h := sha256.Sum256(c.Raw)
		if want != "" && hex.EncodeToString(h[:]) == want {
			return nil
		}
	}
	if PublicOK(cs) == nil {
		return nil
	}
	return fmt.Errorf("certificate is neither the paired one nor valid for %q (resumed=%v, %d certs, want %.8s)", cs.ServerName, cs.DidResume, len(cs.PeerCertificates), want)
}

// publicRoots: nil means the system's; tests put their own root here.
var publicRoots *x509.CertPool

// PublicOK verifies the chain for the dialled name under the system roots.
func PublicOK(cs tls.ConnectionState) error {
	if len(cs.PeerCertificates) == 0 || cs.ServerName == "" {
		return errors.New("no certificate or no name")
	}
	inter := x509.NewCertPool()
	for _, c := range cs.PeerCertificates[1:] {
		inter.AddCert(c)
	}
	_, err := cs.PeerCertificates[0].Verify(x509.VerifyOptions{DNSName: cs.ServerName, Intermediates: inter, Roots: publicRoots})
	return err
}
