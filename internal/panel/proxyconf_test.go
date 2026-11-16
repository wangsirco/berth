package panel

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testNodes(t *testing.T) []*ProxyNode {
	t.Helper()
	specs := []ProxyNode{
		{Name: "Reality", Protocol: "vless", Transport: "raw", Security: "reality", Port: 24443},
		{Name: "Reality XHTTP", Protocol: "vless", Transport: "xhttp", Security: "reality", Port: 24444,
			Params: ProxyParams{Target: "www.apple.com", ServerNames: []string{"www.apple.com", "images.apple.com"}}},
		{Name: "VLESS TLS", Protocol: "vless", Transport: "raw", Security: "tls", Port: 24445, Domain: "xtls.example.com"},
		{Name: "VLESS WS", Protocol: "vless", Transport: "ws", Security: "tls", Port: 24446, Domain: "ws.example.com"},
		{Name: "VLESS gRPC", Protocol: "vless", Transport: "grpc", Security: "tls", Port: 24447, Domain: "grpc.example.com"},
		{Name: "VMess WS", Protocol: "vmess", Transport: "ws", Security: "tls", Port: 24448, Domain: "vm.example.com"},
		{Name: "VMess plain", Protocol: "vmess", Transport: "raw", Security: "none", Port: 24449},
		{Name: "Trojan", Protocol: "trojan", Transport: "raw", Security: "tls", Port: 24450, Domain: "tj.example.com"},
		{Name: "Trojan Reality", Protocol: "trojan", Transport: "raw", Security: "reality", Port: 24451},
		{Name: "HTTPUpgrade", Protocol: "vless", Transport: "httpupgrade", Security: "tls", Port: 24452, Domain: "hu.example.com"},
		{Name: "SS 2022", Protocol: "shadowsocks", Port: 24453},
		{Name: "SS 2022 256", Protocol: "shadowsocks", Port: 24454, Params: ProxyParams{SSMethod: "2022-blake3-aes-256-gcm"}},
		{Name: "Hysteria2", Protocol: "hysteria", Port: 24455, Domain: "hy.example.com"},
	}
	var out []*ProxyNode
	for i, s := range specs {
		n := s
		n.ID, n.Enabled = int64(i+1), true
		if err := n.normalize(); err != nil {
			t.Fatalf("%s: %v", n.Name, err)
		}
		out = append(out, &n)
	}
	return out
}

func TestProxyNormalizeRejects(t *testing.T) {
	bad := []ProxyNode{
		{Name: "x", Protocol: "vmess", Transport: "raw", Security: "reality", Port: 1000},
		{Name: "x", Protocol: "vless", Transport: "ws", Security: "reality", Port: 1000},
		{Name: "x", Protocol: "trojan", Transport: "raw", Security: "none", Port: 1000},
		{Name: "x", Protocol: "vless", Transport: "raw", Security: "tls", Port: 1000, Domain: ""},
		{Name: "x", Protocol: "vless", Transport: "raw", Security: "none", Port: 0},
		{Name: "x", Protocol: "vless", Transport: "raw", Security: "none", Port: proxyAPIPort},
		{Name: "", Protocol: "vless", Transport: "raw", Security: "reality", Port: 1000},
	}
	for i, n := range bad {
		if err := n.normalize(); err == nil {
			t.Errorf("case %d accepted", i)
		}
	}
}

func TestProxyLinksAndSecrets(t *testing.T) {
	nodes := testNodes(t)
	u := &ProxyUser{ID: 7, Name: "friend", Secret: newUUID(), Nodes: []int64{}, Enabled: true}
	for _, n := range nodes {
		u.Nodes = append(u.Nodes, n.ID)
	}
	for _, n := range nodes {
		if shareLink(n, u, "203.0.113.5") == "" {
			t.Fatalf("%s: no link", n.Name)
		}
	}
	vl := shareLink(nodes[0], u, "203.0.113.5")
	for _, want := range []string{"vless://" + u.Secret + "@203.0.113.5:24443?", "security=reality", "flow=xtls-rprx-vision", "pbk=" + nodes[0].Params.PublicKey, "sni=www.cloudflare.com", "type=tcp"} {
		if !strings.Contains(vl, want) {
			t.Errorf("reality link %q lacks %q", vl, want)
		}
	}
	// self-signed TLS: the client pins the certificate
	tl := shareLink(nodes[2], u, "203.0.113.5")
	if pin := certPin(nodes[2].Params.CertPEM); pin == "" || !strings.Contains(tl, "pcs="+pin) || !strings.Contains(tl, "allowInsecure=1") {
		t.Errorf("self-signed link without pin: %s", tl)
	}
	if v := nodeView(nodes[2]); v.Params.KeyPEM != "" || nodeView(nodes[0]).Params.PrivateKey != "" {
		t.Error("the browser view carries private keys")
	}
	sub, _ := base64.StdEncoding.DecodeString(subscription(nodes, u, "203.0.113.5", 1000))
	if strings.Count(string(sub), "\n") != len(nodes)-1 {
		t.Errorf("subscription: %s", sub)
	}
	u.Stopped = "quota"
	if subscription(nodes, u, "x", 1000) != "" {
		t.Error("a paused user still gets links")
	}
}

// TestXrayConfig builds a configuration with every kind of node; with BERTH_XRAY_OUT set
// it writes it (and the certificates) there, to check with the real xray:
// docker run --rm -v DIR:/etc/xray ghcr.io/xtls/xray-core run -test -c /etc/xray/config.json
func TestXrayConfig(t *testing.T) {
	nodes := testNodes(t)
	users := []*ProxyUser{
		{ID: 1, Name: "a", Secret: newUUID(), Enabled: true, AllowLAN: true},
		{ID: 2, Name: "b", Secret: newUUID(), Enabled: true},
		{ID: 3, Name: "expired", Secret: newUUID(), Enabled: true, ExpireAt: 1},
	}
	for _, u := range users {
		for _, n := range nodes {
			u.Nodes = append(u.Nodes, n.ID)
		}
	}
	cfg, err := buildXray(nodes, users, proxyPolicy{BlockBT: true, BlockPorts: []int{25}, BlockHosts: []string{"example.org"}}, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(cfg, proxyEmail(3, 1)) || !strings.Contains(cfg, proxyEmail(2, 13)) {
		t.Error("users wrong in the config")
	}
	dir := os.Getenv("BERTH_XRAY_OUT")
	if dir == "" {
		return
	}
	os.MkdirAll(filepath.Join(dir, "certs"), 0o755)
	for _, n := range nodes {
		if n.Domain != "" {
			os.WriteFile(filepath.Join(dir, "certs", n.Domain+".crt"), []byte(n.Params.CertPEM), 0o644)
			os.WriteFile(filepath.Join(dir, "certs", n.Domain+".key"), []byte(n.Params.KeyPEM), 0o644)
		}
	}
	os.WriteFile(filepath.Join(dir, "config.json"), []byte(cfg), 0o644)
}

func TestPeriodStart(t *testing.T) {
	now := mustTime(t, "2026-11-17T10:00:00Z")
	if got := periodStart(now, 20).Format("2006-01-02"); got != "2026-10-20" {
		t.Errorf("before the day: %s", got)
	}
	if got := periodStart(now, 5).Format("2006-01-02"); got != "2026-11-05" {
		t.Errorf("after the day: %s", got)
	}
}

func mustTime(t *testing.T, s string) time.Time {
	t.Helper()
	v, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatal(err)
	}
	return v
}
