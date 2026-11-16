package pve

import (
	"context"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/coder/websocket"
	"github.com/wangsirco/berth/internal/errs"
)

func fixture(t *testing.T) (*Fake, *Client, string) {
	t.Helper()
	f := NewFake("berth@pve!panel", "11111111-2222-3333-4444-555555555555")
	srv := httptest.NewTLSServer(f)
	t.Cleanup(srv.Close)
	fp, _, trusted, err := Probe(context.Background(), srv.URL)
	if err != nil || len(fp) != 64 || trusted {
		t.Fatalf("probe: %q %v %v", fp, trusted, err)
	}
	return f, New(Config{URL: srv.URL, TokenID: "berth@pve!panel", Secret: "11111111-2222-3333-4444-555555555555", Fingerprint: fp}), srv.URL
}

func TestPVE(t *testing.T) {
	ctx := context.Background()
	f, c, url := fixture(t)
	if v, err := c.Version(ctx); err != nil || v.Version != "9.1.2" {
		t.Fatal(v, err)
	}
	nodes, _ := c.Nodes(ctx)
	guests, err := c.Guests(ctx)
	if err != nil || len(nodes) != 2 || len(guests) != 3 || guests[0].VMID != 100 || guests[1].Type != "lxc" {
		t.Fatalf("%v %v %v", nodes, guests, err)
	}
	// addresses: from the guest agent (qemu), none while the container is off
	if ips := c.Addresses(ctx, "pve1", "qemu", 100); len(ips) != 1 || ips[0] != "10.0.9.100" {
		t.Fatal(ips)
	}
	if ips := c.Addresses(ctx, "pve1", "lxc", 101); len(ips) != 0 {
		t.Fatal(ips)
	}
	// power waits for the task
	if err := c.Power(ctx, "pve1", "lxc", 101, "start"); err != nil || f.GuestStatus(101) != "running" {
		t.Fatal(err, f.GuestStatus(101))
	}
	if ips := c.Addresses(ctx, "pve1", "lxc", 101); len(ips) != 1 || ips[0] != "10.0.9.101" {
		t.Fatal(ips)
	}
	if err := c.Power(ctx, "pve2", "qemu", 200, "reboot"); err == nil || !strings.Contains(fmt.Sprint(errs.From(err).Params["detail"]), "not running") {
		t.Fatalf("reboot of a stopped VM: %v", err)
	}
	// snapshots
	if err := c.Snapshot(ctx, "pve1", "qemu", 100, "berth-test", "e2e", false); err != nil {
		t.Fatal(err)
	}
	s, _ := c.Snapshots(ctx, "pve1", "qemu", 100)
	if len(s) != 2 || s[0].Name != "berth-test" {
		t.Fatal(s)
	}
	if err := c.Rollback(ctx, "pve1", "qemu", 100, "berth-test"); err != nil {
		t.Fatal(err)
	}
	if err := c.DeleteSnapshot(ctx, "pve1", "qemu", 100, "berth-test"); err != nil {
		t.Fatal(err)
	}
	if err := c.Snapshot(ctx, "pve1", "qemu", 100, "bad name!", "", false); err == nil {
		t.Fatal("bad snapshot name accepted")
	}
	// console: ticket, then the websocket with the token
	c.Power(ctx, "pve1", "qemu", 100, "start")
	tk, err := c.VNCProxy(ctx, "pve1", "qemu", 100)
	if err != nil || tk.Ticket == "" {
		t.Fatal(tk, err)
	}
	u, h, hc, _ := c.VNCSocket("pve1", "qemu", 100, tk)
	ws, _, err := websocket.Dial(ctx, u, &websocket.DialOptions{HTTPHeader: h, HTTPClient: hc, Subprotocols: []string{"binary"}})
	if err != nil {
		t.Fatal(err)
	}
	_, b, err := ws.Read(ctx)
	if err != nil || string(b) != "RFB 003.008\n" {
		t.Fatal(string(b), err)
	}
	ws.Close(websocket.StatusNormalClosure, "")

	// a wrong secret, a changed certificate
	bad := New(Config{URL: url, TokenID: "berth@pve!panel", Secret: "nope", Fingerprint: c.cfg.Fingerprint})
	if _, err := bad.Nodes(ctx); err == nil || !strings.Contains(err.Error(), "pve.auth_failed") {
		t.Fatalf("wrong secret: %v", err)
	}
	other := New(Config{URL: url, TokenID: "berth@pve!panel", Secret: c.cfg.Secret, Fingerprint: strings.Repeat("ab", 32)})
	if _, err := other.Nodes(ctx); err == nil || !strings.Contains(err.Error(), "pve.cert_changed") {
		t.Fatalf("pin: %v", err)
	}
}

func TestNormalize(t *testing.T) {
	for in, want := range map[string]string{"pve.lan": "https://pve.lan:8006", "https://10.0.0.5/": "https://10.0.0.5:8006", "https://pve:443": "https://pve:443"} {
		if got, _ := NormalizeURL(in); got != want {
			t.Errorf("%s: %s", in, got)
		}
	}
	if !ValidTokenID("berth@pve!panel") || ValidTokenID("berth@pve") {
		t.Fatal("token id")
	}
	if FormatFP("abcd") != "AB:CD" {
		t.Fatal(FormatFP("abcd"))
	}
}
