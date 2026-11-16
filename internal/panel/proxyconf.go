package panel

// Proxy extension (ported from Wharf's, MIT): what nodes and users look like, and how they
// become an Xray configuration, share links and subscriptions. Pure functions; storage,
// changes and the agent round-trip are in proxy.go.
//
// Differences from Wharf: Berth's edge owns 443, so every node listens on its own port
// (no SNI front); a TLS node uses a self-signed certificate (pinned in the links) or the
// certificate Berth's edge already has for a site with that domain.

import (
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/wangsirco/berth/internal/errs"
)

type ProxyNode struct {
	ID        int64       `json:"id"`
	Name      string      `json:"name"`
	Enabled   bool        `json:"enabled"`
	Protocol  string      `json:"protocol"`  // vless | vmess | trojan | shadowsocks | hysteria
	Transport string      `json:"transport"` // raw | xhttp | ws | grpc | httpupgrade | hysteria
	Security  string      `json:"security"`  // reality | tls | none
	Port      int         `json:"port"`      // where it listens (and clients connect)
	Domain    string      `json:"domain"`    // TLS: certificate / server name; also the address in links
	Params    ProxyParams `json:"params"`
	Note      string      `json:"note"`
	Created   int64       `json:"created"`
	Updated   int64       `json:"updated"`
}

type ProxyParams struct {
	Flow        string   `json:"flow,omitempty"` // xtls-rprx-vision
	Target      string   `json:"target,omitempty"`
	ServerNames []string `json:"server_names,omitempty"`
	PrivateKey  string   `json:"private_key,omitempty"`
	PublicKey   string   `json:"public_key,omitempty"`
	ShortIDs    []string `json:"short_ids,omitempty"`
	Fingerprint string   `json:"fingerprint,omitempty"` // client uTLS fingerprint in links
	Cert        string   `json:"cert,omitempty"`        // self | site
	CertPEM     string   `json:"cert_pem,omitempty"`    // self: the generated certificate
	KeyPEM      string   `json:"key_pem,omitempty"`     // self: its key (never sent to the browser)
	Path        string   `json:"path,omitempty"`        // ws / xhttp / httpupgrade path, grpc service name
	XHTTPMode   string   `json:"xhttp_mode,omitempty"`
	SSMethod    string   `json:"ss_method,omitempty"`
	SSKey       string   `json:"ss_key,omitempty"` // server key (base64)
	UpMbps      int      `json:"up_mbps,omitempty"`
	DownMbps    int      `json:"down_mbps,omitempty"`
}

type ProxyUser struct {
	ID         int64   `json:"id"`
	Name       string  `json:"name"`
	Secret     string  `json:"secret"` // UUID: VLESS / VMess id, Trojan / Hysteria password, seed of SS keys
	SubToken   string  `json:"sub_token"`
	Nodes      []int64 `json:"nodes"`
	LimitBytes int64   `json:"limit_bytes"` // 0 = unlimited
	ResetDay   int     `json:"reset_day"`   // 1..28: usage resets monthly on that day; 0 = never
	ExpireAt   int64   `json:"expire_at"`   // 0 = never
	AllowLAN   bool    `json:"allow_lan"`   // may reach this server's LAN through the proxy
	Enabled    bool    `json:"enabled"`
	Stopped    string  `json:"stopped"` // "" | quota | expired: paused automatically
	UsedUp     int64   `json:"used_up"`
	UsedDown   int64   `json:"used_down"`
	PeriodAt   int64   `json:"period_at"` // start of the current usage period
	Note       string  `json:"note"`
	Created    int64   `json:"created"`
}

func (u *ProxyUser) active(now int64) bool {
	return u.Enabled && u.Stopped == "" && (u.ExpireAt == 0 || u.ExpireAt > now)
}

const proxyAPIPort = 20085

func (n *ProxyNode) tag() string { return "n" + strconv.FormatInt(n.ID, 10) }
func proxyEmail(uid, nid int64) string {
	return "u" + strconv.FormatInt(uid, 10) + "@n" + strconv.FormatInt(nid, 10)
}

// parseProxyEmail splits "u12@n3".
func parseProxyEmail(e string) (uid, nid int64, ok bool) {
	u, n, found := strings.Cut(e, "@")
	if !found || !strings.HasPrefix(u, "u") || !strings.HasPrefix(n, "n") {
		return 0, 0, false
	}
	a, err1 := strconv.ParseInt(u[1:], 10, 64)
	b, err2 := strconv.ParseInt(n[1:], 10, 64)
	return a, b, err1 == nil && err2 == nil
}

// fwProto: the protocols the node's port needs open.
func (n *ProxyNode) fwProto() []string {
	switch n.Protocol {
	case "hysteria":
		return []string{"udp"}
	case "shadowsocks":
		return []string{"tcp", "udp"}
	}
	return []string{"tcp"}
}

var (
	proxyProtocols  = []string{"vless", "vmess", "trojan", "shadowsocks", "hysteria"}
	proxyTransports = []string{"raw", "xhttp", "ws", "grpc", "httpupgrade", "hysteria"}
	ssMethods       = map[string]int{"2022-blake3-aes-128-gcm": 16, "2022-blake3-aes-256-gcm": 32, "2022-blake3-chacha20-poly1305": 32}
	fingerprints    = []string{"chrome", "firefox", "safari", "ios", "edge", "random"}
)

func pxErr(key string, kv ...any) error { return errs.New(400, "proxy."+key, kv...) }

func proxyClean(s string, n int) string {
	s = strings.Join(strings.Fields(strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, s)), " ")
	if r := []rune(s); len(r) > n {
		s = string(r[:n])
	}
	return s
}

// normalize checks a node's combination of choices and fills generated parameters.
func (n *ProxyNode) normalize() error {
	n.Name = proxyClean(n.Name, 40)
	if n.Name == "" {
		return pxErr("need_name")
	}
	if !slices.Contains(proxyProtocols, n.Protocol) {
		return pxErr("bad_protocol", "value", n.Protocol)
	}
	if n.Transport == "tcp" || n.Transport == "" {
		n.Transport = "raw"
	}
	if !slices.Contains(proxyTransports, n.Transport) {
		return pxErr("bad_transport", "value", n.Transport)
	}
	if n.Port < 1 || n.Port > 65535 {
		return pxErr("bad_port")
	}
	if n.Port == proxyAPIPort {
		return pxErr("port_reserved", "port", n.Port)
	}
	p := &n.Params
	switch n.Protocol {
	case "shadowsocks":
		n.Transport, n.Security = "raw", "none"
		if _, ok := ssMethods[p.SSMethod]; !ok {
			p.SSMethod = "2022-blake3-aes-128-gcm"
		}
		if k, err := base64.StdEncoding.DecodeString(p.SSKey); err != nil || len(k) != ssMethods[p.SSMethod] {
			p.SSKey = randKey(ssMethods[p.SSMethod])
		}
	case "hysteria":
		n.Transport, n.Security = "hysteria", "tls"
	default:
		if n.Transport == "hysteria" {
			return pxErr("hysteria_only")
		}
	}
	switch n.Security {
	case "reality":
		if n.Protocol != "vless" && n.Protocol != "trojan" {
			return pxErr("reality_protocol")
		}
		if n.Transport != "raw" && n.Transport != "xhttp" && n.Transport != "grpc" {
			return pxErr("reality_transport")
		}
		p.Target = strings.TrimSpace(p.Target)
		if p.Target == "" {
			p.Target = "www.cloudflare.com:443"
		}
		if !strings.Contains(p.Target, ":") {
			p.Target += ":443"
		}
		host, port, _ := strings.Cut(p.Target, ":")
		if !hostnameOK(host) || pxPort(port) <= 0 {
			return pxErr("bad_target")
		}
		var names []string
		for _, s := range p.ServerNames {
			if s = strings.ToLower(strings.TrimSpace(s)); s != "" && !slices.Contains(names, s) {
				if !hostnameOK(s) {
					return pxErr("bad_sni", "value", s)
				}
				names = append(names, s)
			}
		}
		if len(names) == 0 {
			names = []string{host}
		}
		p.ServerNames = names
		if p.PrivateKey == "" || p.PublicKey == "" {
			priv, pub, err := x25519Pair()
			if err != nil {
				return err
			}
			p.PrivateKey, p.PublicKey = priv, pub
		}
		if len(p.ShortIDs) == 0 {
			p.ShortIDs = []string{randHex(8)}
		}
		for _, s := range p.ShortIDs {
			if _, err := hex.DecodeString(s); err != nil || len(s) > 16 || len(s)%2 != 0 {
				return pxErr("bad_shortid", "value", s)
			}
		}
		n.Domain = ""
	case "tls":
		n.Domain = strings.ToLower(strings.TrimSpace(n.Domain))
		if !hostnameOK(n.Domain) {
			return pxErr("need_domain")
		}
		if p.Cert != "site" {
			p.Cert = "self"
		}
		if p.Cert == "self" && !certFor(p.CertPEM, n.Domain) {
			c, k, err := selfSigned(n.Domain)
			if err != nil {
				return err
			}
			p.CertPEM, p.KeyPEM = c, k
		}
	case "none":
		if n.Protocol == "trojan" || n.Protocol == "hysteria" {
			return pxErr("needs_tls", "protocol", n.Protocol)
		}
		n.Domain = strings.ToLower(strings.TrimSpace(n.Domain))
	default:
		return pxErr("bad_security", "value", n.Security)
	}
	if n.Protocol == "vless" && n.Transport == "raw" && n.Security != "none" {
		p.Flow = "xtls-rprx-vision"
	} else {
		p.Flow = ""
	}
	if !slices.Contains(fingerprints, p.Fingerprint) {
		p.Fingerprint = "chrome"
	}
	switch n.Transport {
	case "ws", "xhttp", "httpupgrade":
		if p.Path == "" {
			p.Path = "/" + randHex(6)
		}
		if !strings.HasPrefix(p.Path, "/") {
			p.Path = "/" + p.Path
		}
	case "grpc":
		if p.Path == "" {
			p.Path = randHex(6)
		}
		p.Path = strings.Trim(p.Path, "/")
	default:
		p.Path = ""
	}
	if n.Transport == "xhttp" && !slices.Contains([]string{"auto", "packet-up", "stream-up", "stream-one"}, p.XHTTPMode) {
		p.XHTTPMode = "auto"
	}
	if n.Transport != "xhttp" {
		p.XHTTPMode = ""
	}
	if n.Security != "reality" {
		p.PrivateKey, p.PublicKey, p.ShortIDs, p.Target, p.ServerNames = "", "", nil, "", nil
	}
	if n.Security != "tls" {
		p.Cert = ""
	}
	if p.Cert != "self" {
		p.CertPEM, p.KeyPEM = "", ""
	}
	if n.Protocol != "shadowsocks" {
		p.SSMethod, p.SSKey = "", ""
	}
	if n.Protocol != "hysteria" {
		p.UpMbps, p.DownMbps = 0, 0
	}
	n.Note = proxyClean(n.Note, 200)
	return nil
}

func pxPort(s string) int {
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 || n > 65535 {
		return 0
	}
	return n
}

func hostnameOK(h string) bool {
	if h == "" || len(h) > 253 || !strings.Contains(h, ".") {
		return false
	}
	for _, l := range strings.Split(h, ".") {
		if l == "" || len(l) > 63 || l[0] == '-' || l[len(l)-1] == '-' {
			return false
		}
		for _, c := range l {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-') {
				return false
			}
		}
	}
	return true
}

func randHex(n int) string {
	b := make([]byte, n/2)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func randKey(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return base64.StdEncoding.EncodeToString(b)
}

func newUUID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	h := hex.EncodeToString(b)
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

func newSubToken() string {
	b := make([]byte, 18)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// x25519Pair returns a REALITY key pair as Xray prints it (raw URL base64).
func x25519Pair() (priv, pub string, err error) {
	k, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return "", "", err
	}
	return base64.RawURLEncoding.EncodeToString(k.Bytes()), base64.RawURLEncoding.EncodeToString(k.PublicKey().Bytes()), nil
}

// selfSigned makes a ten-year ECDSA certificate for domain.
func selfSigned(domain string) (certPEM, keyPEM string, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", "", err
	}
	serial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 62))
	tpl := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: domain}, DNSNames: []string{domain},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().AddDate(10, 0, 0),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		return "", "", err
	}
	kd, _ := x509.MarshalECPrivateKey(key)
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})), string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kd})), nil
}

// certFor: the PEM is a certificate for domain.
func certFor(certPEM, domain string) bool {
	b, _ := pem.Decode([]byte(certPEM))
	if b == nil {
		return false
	}
	c, err := x509.ParseCertificate(b.Bytes)
	return err == nil && c.VerifyHostname(domain) == nil
}

// certPin: hex SHA-256 of the certificate, which clients pin for self-signed nodes.
func certPin(certPEM string) string {
	b, _ := pem.Decode([]byte(certPEM))
	if b == nil {
		return ""
	}
	s := sha256.Sum256(b.Bytes)
	return hex.EncodeToString(s[:])
}

// ssUserKey derives a user's Shadowsocks 2022 key for a node from the user's secret.
func ssUserKey(secret string, node *ProxyNode) string {
	h := sha256.Sum256([]byte("wharf-ss2022|" + secret + "|" + strconv.FormatInt(node.ID, 10)))
	return base64.StdEncoding.EncodeToString(h[:ssMethods[node.Params.SSMethod]])
}

// ---- Xray configuration ----

type proxyPolicy struct {
	BlockBT    bool     `json:"block_bt"`
	BlockPorts []int    `json:"block_ports"`
	BlockHosts []string `json:"block_hosts"` // domains (and their subdomains) nobody may reach through the proxy
}

var privateCIDRs = []string{"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16", "172.16.0.0/12",
	"192.168.0.0/16", "224.0.0.0/4", "255.255.255.255/32", "::1/128", "fc00::/7", "fe80::/10"}

type jm = map[string]any

func buildXray(nodes []*ProxyNode, users []*ProxyUser, pol proxyPolicy, now int64) (string, error) {
	inbounds := []any{jm{"tag": "api", "listen": "127.0.0.1", "port": proxyAPIPort, "protocol": "tunnel", "settings": jm{"address": "127.0.0.1"}}}
	var lanEmails []string
	for _, n := range nodes {
		if !n.Enabled {
			continue
		}
		inbounds = append(inbounds, buildInbound(n, users, now))
		for _, u := range users {
			if u.AllowLAN && slices.Contains(u.Nodes, n.ID) && u.active(now) {
				lanEmails = append(lanEmails, proxyEmail(u.ID, n.ID))
			}
		}
	}
	rules := []any{jm{"inboundTag": []string{"api"}, "outboundTag": "api"}}
	if pol.BlockBT {
		rules = append(rules, jm{"protocol": []string{"bittorrent"}, "outboundTag": "blocked"})
	}
	if len(pol.BlockPorts) > 0 {
		var ps []string
		for _, p := range pol.BlockPorts {
			ps = append(ps, strconv.Itoa(p))
		}
		rules = append(rules, jm{"port": strings.Join(ps, ","), "outboundTag": "blocked"})
	}
	if len(pol.BlockHosts) > 0 {
		var ds []string
		for _, h := range pol.BlockHosts {
			ds = append(ds, "domain:"+h)
		}
		rules = append(rules, jm{"domain": ds, "outboundTag": "blocked"})
	}
	if len(lanEmails) > 0 {
		rules = append(rules, jm{"user": lanEmails, "ip": privateCIDRs, "outboundTag": "direct"})
	}
	// nobody reaches this server's own network unless allowed
	rules = append(rules, jm{"ip": privateCIDRs, "outboundTag": "blocked"})
	cfg := jm{
		"log":   jm{"loglevel": "warning", "dnsLog": false},
		"api":   jm{"tag": "api", "services": []string{"StatsService", "HandlerService"}},
		"stats": jm{},
		"policy": jm{
			"levels": jm{"0": jm{"statsUserUplink": true, "statsUserDownlink": true, "handshake": 4, "connIdle": 300}},
			"system": jm{"statsInboundUplink": true, "statsInboundDownlink": true},
		},
		"inbounds": inbounds,
		"outbounds": []any{
			jm{"tag": "direct", "protocol": "freedom", "streamSettings": jm{"sockopt": jm{"domainStrategy": "UseIP"}}},
			jm{"tag": "blocked", "protocol": "blackhole"},
		},
		"routing": jm{"domainStrategy": "IPIfNonMatch", "rules": rules},
	}
	b, err := json.MarshalIndent(cfg, "", "  ")
	return string(b), err
}

func buildInbound(n *ProxyNode, users []*ProxyUser, now int64) jm {
	p := n.Params
	clients := []any{}
	for _, u := range users {
		if !u.active(now) || !slices.Contains(u.Nodes, n.ID) {
			continue
		}
		e := proxyEmail(u.ID, n.ID)
		switch n.Protocol {
		case "vless":
			c := jm{"id": u.Secret, "email": e}
			if p.Flow != "" {
				c["flow"] = p.Flow
			}
			clients = append(clients, c)
		case "vmess":
			clients = append(clients, jm{"id": u.Secret, "email": e})
		case "trojan":
			clients = append(clients, jm{"password": u.Secret, "email": e})
		case "shadowsocks":
			clients = append(clients, jm{"password": ssUserKey(u.Secret, n), "email": e})
		case "hysteria":
			clients = append(clients, jm{"auth": u.Secret, "email": e})
		}
	}
	settings := jm{"clients": clients}
	switch n.Protocol {
	case "vless":
		settings["decryption"] = "none"
	case "shadowsocks":
		settings["method"], settings["password"], settings["network"] = p.SSMethod, p.SSKey, "tcp,udp"
	case "hysteria":
		settings["version"] = 2
	}
	stream := jm{"network": n.Transport, "security": n.Security}
	switch n.Security {
	case "reality":
		stream["realitySettings"] = jm{"target": p.Target, "serverNames": p.ServerNames, "privateKey": p.PrivateKey, "shortIds": p.ShortIDs, "show": false}
	case "tls":
		t := jm{"serverName": n.Domain, "minVersion": "1.2", "rejectUnknownSni": false,
			"certificates": []any{jm{"certificateFile": "/etc/xray/certs/" + n.Domain + ".crt", "keyFile": "/etc/xray/certs/" + n.Domain + ".key"}}}
		switch {
		case n.Protocol == "hysteria":
			t["alpn"] = []string{"h3"}
		case n.Transport == "grpc":
			t["alpn"] = []string{"h2"}
		case n.Transport == "xhttp":
			t["alpn"] = []string{"h2", "http/1.1"}
		default:
			t["alpn"] = []string{"http/1.1"}
		}
		stream["tlsSettings"] = t
	}
	switch n.Transport {
	case "ws":
		stream["wsSettings"] = jm{"path": p.Path}
	case "httpupgrade":
		stream["httpupgradeSettings"] = jm{"path": p.Path}
	case "xhttp":
		stream["xhttpSettings"] = jm{"path": p.Path, "mode": p.XHTTPMode}
	case "grpc":
		stream["grpcSettings"] = jm{"serviceName": p.Path}
	case "hysteria":
		stream["hysteriaSettings"] = jm{"version": 2}
	}
	in := jm{"tag": n.tag(), "listen": "0.0.0.0", "port": n.Port, "protocol": n.Protocol, "settings": settings, "streamSettings": stream}
	if n.Protocol != "hysteria" {
		in["sniffing"] = jm{"enabled": true, "destOverride": []string{"http", "tls", "quic"}, "routeOnly": true}
	}
	return in
}

// ---- links ----

func shareLink(n *ProxyNode, u *ProxyUser, host string) string {
	p := n.Params
	name := url.PathEscape(n.Name)
	addr := hostPort(host, n.Port)
	pin := ""
	if p.Cert == "self" {
		pin = certPin(p.CertPEM)
	}
	q := url.Values{}
	setTransport := func() {
		q.Set("type", map[string]string{"raw": "tcp"}[n.Transport])
		if q.Get("type") == "" {
			q.Set("type", n.Transport)
		}
		switch n.Transport {
		case "ws", "httpupgrade":
			q.Set("path", p.Path)
			if n.Domain != "" {
				q.Set("host", n.Domain)
			}
		case "xhttp":
			q.Set("path", p.Path)
			q.Set("mode", p.XHTTPMode)
			if n.Domain != "" {
				q.Set("host", n.Domain)
			}
		case "grpc":
			q.Set("serviceName", p.Path)
			q.Set("mode", "gun")
		}
	}
	setSecurity := func() {
		q.Set("security", n.Security)
		switch n.Security {
		case "reality":
			q.Set("sni", p.ServerNames[0])
			q.Set("fp", p.Fingerprint)
			q.Set("pbk", p.PublicKey)
			q.Set("sid", p.ShortIDs[0])
		case "tls":
			q.Set("sni", n.Domain)
			q.Set("fp", p.Fingerprint)
			if p.Cert == "self" {
				q.Set("allowInsecure", "1")
				if pin != "" {
					q.Set("pcs", pin)
				}
			}
		}
	}
	switch n.Protocol {
	case "vless":
		q.Set("encryption", "none")
		setTransport()
		setSecurity()
		if p.Flow != "" {
			q.Set("flow", p.Flow)
		}
		return "vless://" + u.Secret + "@" + addr + "?" + q.Encode() + "#" + name
	case "trojan":
		setTransport()
		setSecurity()
		return "trojan://" + url.PathEscape(u.Secret) + "@" + addr + "?" + q.Encode() + "#" + name
	case "vmess":
		v := jm{"v": "2", "ps": n.Name, "add": host, "port": strconv.Itoa(n.Port), "id": u.Secret, "aid": "0", "scy": "auto",
			"net": map[string]string{"raw": "tcp"}[n.Transport], "type": "none", "host": n.Domain, "path": p.Path, "tls": "", "sni": "", "fp": ""}
		if v["net"] == "" {
			v["net"] = n.Transport
		}
		if n.Security == "tls" {
			v["tls"], v["sni"], v["fp"] = "tls", n.Domain, p.Fingerprint
			if p.Cert == "self" {
				v["allowInsecure"] = true
			}
		}
		b, _ := json.Marshal(v)
		return "vmess://" + base64.StdEncoding.EncodeToString(b)
	case "shadowsocks":
		userinfo := base64.RawURLEncoding.EncodeToString([]byte(p.SSMethod + ":" + p.SSKey + ":" + ssUserKey(u.Secret, n)))
		return "ss://" + userinfo + "@" + addr + "#" + name
	case "hysteria":
		q.Set("sni", n.Domain)
		if p.Cert == "self" {
			q.Set("insecure", "1")
			if pin != "" {
				q.Set("pinSHA256", pin)
			}
		}
		return "hysteria2://" + url.PathEscape(u.Secret) + "@" + addr + "?" + q.Encode() + "#" + name
	}
	return ""
}

func hostPort(host string, port int) string {
	if strings.Contains(host, ":") && !strings.HasPrefix(host, "[") {
		host = "[" + host + "]"
	}
	return host + ":" + strconv.Itoa(port)
}

// nodeHost is the address clients use for a node: a node with the site's (public)
// certificate is reached by its domain; others by the extension's address.
func nodeHost(n *ProxyNode, def string) string {
	if n.Security == "tls" && n.Params.Cert == "site" && n.Domain != "" {
		return n.Domain
	}
	return def
}

func subscription(nodes []*ProxyNode, u *ProxyUser, host string, now int64) string {
	var links []string
	for _, n := range nodes {
		if n.Enabled && slices.Contains(u.Nodes, n.ID) && u.active(now) {
			links = append(links, shareLink(n, u, nodeHost(n, host)))
		}
	}
	return base64.StdEncoding.EncodeToString([]byte(strings.Join(links, "\n")))
}

func subInfo(u *ProxyUser) string {
	return fmt.Sprintf("upload=%d; download=%d; total=%d; expire=%d", u.UsedUp, u.UsedDown, u.LimitBytes, u.ExpireAt)
}
