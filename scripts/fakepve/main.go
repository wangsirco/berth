// fakepve serves the test Proxmox VE API (internal/pve.Fake) over HTTPS with a fresh
// self-signed certificate, the way a new PVE install does. For e2e on the dev server.
//
//	fakepve -listen 127.0.0.1:18006 -token 'berth@pve!panel' -secret <uuid>
package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"flag"
	"log"
	"math/big"
	"net"
	"net/http"
	"time"

	"github.com/wangsirco/berth/internal/pve"
)

func main() {
	listen := flag.String("listen", "127.0.0.1:18006", "address")
	token := flag.String("token", "berth@pve!panel", "token id")
	secret := flag.String("secret", "", "token secret")
	flag.Parse()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tpl := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: "pve1.fake"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour), DNSNames: []string{"pve1.fake"},
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		log.Fatal(err)
	}
	srv := &http.Server{Addr: *listen, Handler: pve.NewFake(*token, *secret),
		TLSConfig: &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}}}
	log.Printf("fake PVE on https://%s", *listen)
	log.Fatal(srv.ListenAndServeTLS("", ""))
}
