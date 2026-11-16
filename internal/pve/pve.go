// Package pve talks to an existing Proxmox VE cluster (方案 §5.4.2) with an API token:
// nodes and guests (QEMU VMs and LXC containers), power, snapshots, guest addresses and the
// noVNC console ticket. PVE usually serves a self-signed certificate: the client pins its
// SHA-256 fingerprint (shown once when the cluster is added) instead of trusting any.
package pve

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/wangsirco/berth/internal/errs"
)

// Config is one cluster as the panel keeps it.
type Config struct {
	URL         string `json:"url"`         // https://pve.lan:8006
	TokenID     string `json:"token_id"`    // berth@pve!panel
	Secret      string `json:"-"`           // the token's UUID (sealed by the panel)
	Fingerprint string `json:"fingerprint"` // SHA-256 of the server certificate, hex; "" = system CAs
}

type Client struct {
	cfg  Config
	base string
	http *http.Client
}

var tokenIDRe = regexp.MustCompile(`^[A-Za-z0-9._-]+@[A-Za-z0-9._-]+![A-Za-z0-9._-]+$`)

func ValidTokenID(s string) bool { return tokenIDRe.MatchString(s) }

// NormalizeURL: "pve.lan" → "https://pve.lan:8006".
func NormalizeURL(s string) (string, error) {
	s = strings.TrimRight(strings.TrimSpace(s), "/")
	if !strings.Contains(s, "://") {
		s = "https://" + s
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return "", errs.New(400, "pve.bad_url", "url", s)
	}
	if u.Port() == "" {
		u.Host = net.JoinHostPort(u.Hostname(), "8006")
	}
	return u.Scheme + "://" + u.Host, nil
}

func tlsConfig(fp string) *tls.Config {
	if fp == "" {
		return &tls.Config{MinVersion: tls.VersionTLS12}
	}
	want := strings.ToLower(strings.ReplaceAll(fp, ":", ""))
	return &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: true, // replaced by the pin below
		VerifyPeerCertificate: func(raw [][]byte, _ [][]*x509.Certificate) error {
			if len(raw) == 0 {
				return errs.New(502, "pve.cert_changed")
			}
			sum := sha256.Sum256(raw[0])
			if hex.EncodeToString(sum[:]) != want {
				return errs.New(502, "pve.cert_changed")
			}
			return nil
		}}
}

func New(cfg Config) *Client {
	tr := &http.Transport{TLSClientConfig: tlsConfig(cfg.Fingerprint), Proxy: nil,
		DialContext: (&net.Dialer{Timeout: 8 * time.Second}).DialContext, TLSHandshakeTimeout: 8 * time.Second}
	return &Client{cfg: cfg, base: cfg.URL + "/api2/json", http: &http.Client{Transport: tr, Timeout: 30 * time.Second}}
}

// Probe reads the server certificate (for the person to confirm) without trusting it.
func Probe(ctx context.Context, rawURL string) (fingerprint, subject string, trusted bool, err error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", "", false, errs.New(400, "pve.bad_url", "url", rawURL)
	}
	if u.Scheme == "http" {
		return "", "", false, nil
	}
	d := &tls.Dialer{NetDialer: &net.Dialer{Timeout: 8 * time.Second}, Config: &tls.Config{InsecureSkipVerify: true}} // only to read it
	c, err := d.DialContext(ctx, "tcp", u.Host)
	if err != nil {
		return "", "", false, errs.New(502, "pve.unreachable", "url", rawURL, "detail", err.Error())
	}
	defer c.Close()
	certs := c.(*tls.Conn).ConnectionState().PeerCertificates
	if len(certs) == 0 {
		return "", "", false, errs.New(502, "pve.unreachable", "url", rawURL, "detail", "no certificate")
	}
	sum := sha256.Sum256(certs[0].Raw)
	pool, _ := x509.SystemCertPool()
	inter := x509.NewCertPool()
	for _, x := range certs[1:] {
		inter.AddCert(x)
	}
	_, verr := certs[0].Verify(x509.VerifyOptions{Roots: pool, Intermediates: inter, DNSName: u.Hostname()})
	return hex.EncodeToString(sum[:]), certs[0].Subject.CommonName, verr == nil, nil
}

// FormatFP: aa:bb:… as PVE shows it (Datacenter → node → Certificates).
func FormatFP(fp string) string {
	var b strings.Builder
	for i := 0; i+1 < len(fp); i += 2 {
		if i > 0 {
			b.WriteByte(':')
		}
		b.WriteString(strings.ToUpper(fp[i : i+2]))
	}
	return b.String()
}

func (c *Client) auth(r *http.Request) {
	r.Header.Set("Authorization", "PVEAPIToken="+c.cfg.TokenID+"="+c.cfg.Secret)
}

// do calls the API; out receives the "data" member.
func (c *Client) do(ctx context.Context, method, path string, form url.Values, out any) error {
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, body)
	if err != nil {
		return err
	}
	c.auth(req)
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		if e, ok := asE(err); ok {
			return e
		}
		return errs.New(502, "pve.unreachable", "url", c.cfg.URL, "detail", err.Error())
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	switch {
	case resp.StatusCode == 401:
		return errs.New(401, "pve.auth_failed")
	case resp.StatusCode == 403:
		return errs.New(403, "pve.forbidden", "detail", pveMessage(b, resp.Status))
	case resp.StatusCode >= 300:
		return errs.New(502, "pve.error", "detail", pveMessage(b, resp.Status))
	}
	if out == nil {
		return nil
	}
	var env struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(b, &env); err != nil {
		return errs.New(502, "pve.error", "detail", "unexpected answer")
	}
	return json.Unmarshal(env.Data, out)
}

func asE(err error) (*errs.E, bool) {
	for e := err; e != nil; {
		if x, ok := e.(*errs.E); ok {
			return x, true
		}
		u, ok := e.(interface{ Unwrap() error })
		if !ok {
			break
		}
		e = u.Unwrap()
	}
	return nil, false
}

func pveMessage(b []byte, status string) string {
	var m struct {
		Message string            `json:"message"`
		Errors  map[string]string `json:"errors"`
	}
	json.Unmarshal(b, &m)
	msg := strings.TrimSpace(m.Message)
	for k, v := range m.Errors {
		msg += " " + k + ": " + v
	}
	if msg == "" {
		msg = status
	}
	return msg
}

// --- reading ----------------------------------------------------------------------

type Version struct {
	Version string `json:"version"`
	Release string `json:"release"`
}

func (c *Client) Version(ctx context.Context) (Version, error) {
	var v Version
	return v, c.do(ctx, "GET", "/version", nil, &v)
}

type Node struct {
	Node    string  `json:"node"`
	Status  string  `json:"status"` // online | offline
	CPU     float64 `json:"cpu"`
	MaxCPU  int     `json:"maxcpu"`
	Mem     int64   `json:"mem"`
	MaxMem  int64   `json:"maxmem"`
	Disk    int64   `json:"disk"`
	MaxDisk int64   `json:"maxdisk"`
	Uptime  int64   `json:"uptime"`
}

func (c *Client) Nodes(ctx context.Context) ([]Node, error) {
	var n []Node
	err := c.do(ctx, "GET", "/nodes", nil, &n)
	sort.Slice(n, func(i, j int) bool { return n[i].Node < n[j].Node })
	return n, err
}

// Guest is a VM (qemu) or a container (lxc), from /cluster/resources.
type Guest struct {
	VMID     int     `json:"vmid"`
	Name     string  `json:"name"`
	Node     string  `json:"node"`
	Type     string  `json:"type"`   // qemu | lxc
	Status   string  `json:"status"` // running | stopped | paused
	CPU      float64 `json:"cpu"`
	MaxCPU   float64 `json:"maxcpu"`
	Mem      int64   `json:"mem"`
	MaxMem   int64   `json:"maxmem"`
	MaxDisk  int64   `json:"maxdisk"`
	Uptime   int64   `json:"uptime"`
	NetIn    int64   `json:"netin"`
	NetOut   int64   `json:"netout"`
	Tags     string  `json:"tags,omitempty"`
	Template int     `json:"template,omitempty"`
	Lock     string  `json:"lock,omitempty"`
}

func (c *Client) Guests(ctx context.Context) ([]Guest, error) {
	var g []Guest
	err := c.do(ctx, "GET", "/cluster/resources?type=vm", nil, &g)
	sort.Slice(g, func(i, j int) bool { return g[i].VMID < g[j].VMID })
	return g, err
}

var nameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,62}$`)

func guestPath(node, typ string, vmid int) (string, error) {
	if !nameRe.MatchString(node) || (typ != "qemu" && typ != "lxc") || vmid < 100 || vmid > 999999999 {
		return "", errs.New(400, "pve.bad_guest")
	}
	return fmt.Sprintf("/nodes/%s/%s/%d", node, typ, vmid), nil
}

type Snapshot struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	SnapTime    int64  `json:"snaptime,omitempty"`
	VMState     int    `json:"vmstate,omitempty"`
	Parent      string `json:"parent,omitempty"`
}

func (c *Client) Snapshots(ctx context.Context, node, typ string, vmid int) ([]Snapshot, error) {
	p, err := guestPath(node, typ, vmid)
	if err != nil {
		return nil, err
	}
	var s []Snapshot
	if err := c.do(ctx, "GET", p+"/snapshot", nil, &s); err != nil {
		return nil, err
	}
	out := s[:0]
	for _, x := range s {
		if x.Name != "current" {
			out = append(out, x)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SnapTime > out[j].SnapTime })
	return out, nil
}

// Config: the guest's settings (cores, memory, disks, net…) as PVE keeps them.
func (c *Client) Config(ctx context.Context, node, typ string, vmid int) (map[string]any, error) {
	p, err := guestPath(node, typ, vmid)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	return m, c.do(ctx, "GET", p+"/config", nil, &m)
}

// Addresses: the guest's IPs (QEMU through its guest agent, LXC from its interfaces).
// Loopback and link-local are left out; none is an answer, not an error.
func (c *Client) Addresses(ctx context.Context, node, typ string, vmid int) []string {
	p, err := guestPath(node, typ, vmid)
	if err != nil {
		return nil
	}
	var ips []string
	add := func(ip string) {
		a := net.ParseIP(strings.Split(ip, "/")[0])
		if a == nil || a.IsLoopback() || a.IsLinkLocalUnicast() {
			return
		}
		ips = append(ips, a.String())
	}
	if typ == "lxc" {
		var ifs []struct {
			Name  string `json:"name"`
			Inet  string `json:"inet"`
			Inet6 string `json:"inet6"`
		}
		if c.do(ctx, "GET", p+"/interfaces", nil, &ifs) == nil {
			for _, i := range ifs {
				if i.Name != "lo" {
					add(i.Inet)
					add(i.Inet6)
				}
			}
		}
	} else {
		var r struct {
			Result []struct {
				Name string `json:"name"`
				IPs  []struct {
					Addr string `json:"ip-address"`
				} `json:"ip-addresses"`
			} `json:"result"`
		}
		if c.do(ctx, "GET", p+"/agent/network-get-interfaces", nil, &r) == nil {
			for _, i := range r.Result {
				if i.Name != "lo" {
					for _, a := range i.IPs {
						add(a.Addr)
					}
				}
			}
		}
	}
	if ips == nil {
		ips = []string{} // "no address" is an empty list, not null
	}
	// IPv4 first: what a site would point at
	sort.SliceStable(ips, func(i, j int) bool { return strings.Contains(ips[j], ":") && !strings.Contains(ips[i], ":") })
	return ips
}

// --- acting -----------------------------------------------------------------------

var powerActions = map[string]bool{"start": true, "shutdown": true, "stop": true, "reboot": true, "suspend": true, "resume": true}

func ValidPower(a string) bool { return powerActions[a] }

// Power starts, shuts down, stops, reboots… and waits for PVE's task to finish.
func (c *Client) Power(ctx context.Context, node, typ string, vmid int, action string) error {
	p, err := guestPath(node, typ, vmid)
	if err != nil {
		return err
	}
	if !powerActions[action] {
		return errs.New(400, "pve.bad_action", "action", action)
	}
	var upid string
	if err := c.do(ctx, "POST", p+"/status/"+action, url.Values{}, &upid); err != nil {
		return err
	}
	return c.Wait(ctx, node, upid)
}

var snapRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]{1,39}$`)

func ValidSnapshot(s string) bool { return snapRe.MatchString(s) }

func (c *Client) Snapshot(ctx context.Context, node, typ string, vmid int, name, desc string, vmstate bool) error {
	p, err := guestPath(node, typ, vmid)
	if err != nil {
		return err
	}
	if !snapRe.MatchString(name) {
		return errs.New(400, "pve.bad_snapshot", "name", name)
	}
	f := url.Values{"snapname": {name}}
	if desc != "" {
		f.Set("description", desc)
	}
	if vmstate && typ == "qemu" {
		f.Set("vmstate", "1")
	}
	var upid string
	if err := c.do(ctx, "POST", p+"/snapshot", f, &upid); err != nil {
		return err
	}
	return c.Wait(ctx, node, upid)
}

func (c *Client) DeleteSnapshot(ctx context.Context, node, typ string, vmid int, name string) error {
	p, err := guestPath(node, typ, vmid)
	if err != nil {
		return err
	}
	if !snapRe.MatchString(name) {
		return errs.New(400, "pve.bad_snapshot", "name", name)
	}
	var upid string
	if err := c.do(ctx, "DELETE", p+"/snapshot/"+name, nil, &upid); err != nil {
		return err
	}
	return c.Wait(ctx, node, upid)
}

func (c *Client) Rollback(ctx context.Context, node, typ string, vmid int, name string) error {
	p, err := guestPath(node, typ, vmid)
	if err != nil {
		return err
	}
	if !snapRe.MatchString(name) {
		return errs.New(400, "pve.bad_snapshot", "name", name)
	}
	var upid string
	if err := c.do(ctx, "POST", p+"/snapshot/"+name+"/rollback", url.Values{}, &upid); err != nil {
		return err
	}
	return c.Wait(ctx, node, upid)
}

// Wait follows a PVE task (UPID) until it stops; a failed task is an error.
func (c *Client) Wait(ctx context.Context, node, upid string) error {
	if upid == "" {
		return nil
	}
	deadline := time.Now().Add(10 * time.Minute)
	for time.Now().Before(deadline) {
		var st struct {
			Status     string `json:"status"`
			ExitStatus string `json:"exitstatus"`
		}
		if err := c.do(ctx, "GET", "/nodes/"+node+"/tasks/"+url.PathEscape(upid)+"/status", nil, &st); err != nil {
			return err
		}
		if st.Status == "stopped" {
			if st.ExitStatus != "OK" && !strings.HasPrefix(st.ExitStatus, "WARNINGS") {
				return errs.New(502, "pve.task_failed", "detail", st.ExitStatus)
			}
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(700 * time.Millisecond):
		}
	}
	return errs.New(504, "pve.task_timeout")
}

// --- console ----------------------------------------------------------------------

type VNCTicket struct {
	Port   string `json:"port"`
	Ticket string `json:"ticket"`
	User   string `json:"user"`
	Cert   string `json:"cert,omitempty"`
}

func (c *Client) VNCProxy(ctx context.Context, node, typ string, vmid int) (VNCTicket, error) {
	var t VNCTicket
	p, err := guestPath(node, typ, vmid)
	if err != nil {
		return t, err
	}
	var raw struct {
		Port   json.RawMessage `json:"port"`
		Ticket string          `json:"ticket"`
		User   string          `json:"user"`
		Cert   string          `json:"cert"`
	}
	if err := c.do(ctx, "POST", p+"/vncproxy", url.Values{"websocket": {"1"}}, &raw); err != nil {
		return t, err
	}
	t.Port = strings.Trim(string(raw.Port), `"`)
	t.Ticket, t.User, t.Cert = raw.Ticket, raw.User, raw.Cert
	if _, err := strconv.Atoi(t.Port); err != nil {
		return t, errs.New(502, "pve.error", "detail", "bad vnc port")
	}
	return t, nil
}

// VNCSocket: where the console websocket is, and how to dial it (headers + TLS).
func (c *Client) VNCSocket(node, typ string, vmid int, t VNCTicket) (string, http.Header, *http.Client, error) {
	p, err := guestPath(node, typ, vmid)
	if err != nil {
		return "", nil, nil, err
	}
	ws := strings.Replace(c.base, "http", "ws", 1) + p + "/vncwebsocket?port=" + url.QueryEscape(t.Port) + "&vncticket=" + url.QueryEscape(t.Ticket)
	h := http.Header{}
	h.Set("Authorization", "PVEAPIToken="+c.cfg.TokenID+"="+c.cfg.Secret)
	return ws, h, c.http, nil
}

// jsonBody is used by the fake server.
func jsonBody(v any) []byte {
	var b bytes.Buffer
	json.NewEncoder(&b).Encode(map[string]any{"data": v})
	return b.Bytes()
}
