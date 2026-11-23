// Package wharfapi talks to a paired Wharf router panel (docs/wharf-berth-extension.md):
// every request carries the pairing token and an HMAC signature, and TLS is pinned to the
// fingerprint recorded at pairing (both sides may use local CAs).
package wharfapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/wangsirco/berth/internal/tlsutil"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/wangsirco/berth/internal/sign"
)

// Prefix is the API root on Wharf.
const Prefix = "/api/ext/berth/v1"

// Error is Wharf's answer to a failed request ({"error": "…"}, already in Chinese).
type Error struct {
	Status  int
	Message string
}

func (e *Error) Error() string { return fmt.Sprintf("wharf %d: %s", e.Status, e.Message) }

// IsStatus reports whether err is a Wharf error with this HTTP status.
func IsStatus(err error, status int) bool {
	var e *Error
	return errors.As(err, &e) && e.Status == status
}

// Fingerprint is the SHA-256 of a certificate (hex).
func Fingerprint(c *x509.Certificate) string {
	h := sha256.Sum256(c.Raw)
	return hex.EncodeToString(h[:])
}

// pinned accepts Wharf's own certificate (any one in the chain matching fp: its local CA stays
// when the leaf renews) or a valid public certificate for the name, when Wharf is reached
// through its public domain behind a reverse proxy or Cloudflare.
func pinned(fp string) *tls.Config { return tlsutil.PinOrPublic(fp) }

func httpClient(fp string) *http.Client {
	return &http.Client{Timeout: 20 * time.Second, Transport: &http.Transport{TLSClientConfig: pinned(fp),
		DialContext: (&net.Dialer{Timeout: 8 * time.Second}).DialContext}}
}

// Chain connects once (without trusting anything) and returns the certificates Wharf
// presents, so a person can confirm the fingerprint before pairing.
func Chain(ctx context.Context, base string) ([]CertInfo, error) {
	u, err := url.Parse(base)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return nil, errors.New("Wharf address must be https://host[:port]")
	}
	host := u.Host
	if u.Port() == "" {
		host += ":443"
	}
	d := tls.Dialer{NetDialer: &net.Dialer{Timeout: 8 * time.Second}, Config: &tls.Config{InsecureSkipVerify: true, ServerName: u.Hostname()}}
	conn, err := d.DialContext(ctx, "tcp", host)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	var out []CertInfo
	cs := conn.(*tls.Conn).ConnectionState()
	public := tlsutil.PublicOK(cs) == nil // a public certificate for this name: checked by name, renewals keep working
	for _, c := range cs.PeerCertificates {
		out = append(out, CertInfo{Subject: c.Subject.CommonName, Issuer: c.Issuer.CommonName, CA: c.IsCA, FP: Fingerprint(c), NotAfter: c.NotAfter.Unix(), Public: public})
	}
	return out, nil
}

type CertInfo struct {
	Subject  string `json:"subject"`
	Issuer   string `json:"issuer"`
	CA       bool   `json:"ca"`
	FP       string `json:"fp"`
	NotAfter int64  `json:"not_after"`
	Public   bool   `json:"public,omitempty"` // the chain is valid for the name under public roots
}

// ---- pairing ----

// ExchangeRequest is what Berth sends with the one-time code (no token yet).
type ExchangeRequest struct {
	Code       string `json:"code"`
	BerthToken string `json:"berth_token"` // the berth_wrf_ token Wharf will use to call Berth
	BerthName  string `json:"berth_name"`
	BerthURL   string `json:"berth_url"`
	BerthFP    string `json:"berth_fp"`
}

type ExchangeResult struct {
	Token     string   `json:"token"` // wharf_bth_…
	Scopes    []string `json:"scopes"`
	WharfName string   `json:"wharf_name"`
	ExpiresAt int64    `json:"expires_at,omitempty"` // 0 = never
	TargetIPs []string `json:"target_ips"`           // devices Berth may point forwards at
}

// Exchange trades the one-time code for the pairing token (server to server, pinned).
func Exchange(ctx context.Context, base, fp string, in ExchangeRequest) (ExchangeResult, error) {
	var out ExchangeResult
	b, _ := json.Marshal(in)
	req, err := http.NewRequestWithContext(ctx, "POST", strings.TrimRight(base, "/")+Prefix+"/pair/exchange", bytes.NewReader(b))
	if err != nil {
		return out, err
	}
	req.Header.Set("Content-Type", "application/json")
	return out, finish(httpClient(fp), req, &out)
}

// ---- client ----

type Client struct {
	Base  string
	Token string
	// KeyAuth (protocol v2): name the token by its key id (X-Berth-Key) instead of sending it;
	// set once Wharf says it speaks v2 and Berth can take X-Wharf-Key back
	KeyAuth bool
	hc      *http.Client
}

// KeyID names a pairing token without revealing it (X-Berth-Key / X-Wharf-Key).
func KeyID(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])[:16]
}

func New(base, fp, token string) *Client {
	return &Client{Base: strings.TrimRight(base, "/"), Token: token, hc: httpClient(fp)}
}

// Do sends one signed request; actor (e.g. "王 · 新建站点 vault.example.net") goes into
// Wharf's audit summary.
func (c *Client) Do(ctx context.Context, method, path, actor string, in, out any) error {
	var body []byte
	if in != nil {
		body, _ = json.Marshal(in)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.Base+Prefix+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.KeyAuth {
		req.Header.Set("X-Berth-Key", KeyID(c.Token))
	} else {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	if actor != "" {
		req.Header.Set("X-Berth-Actor", url.QueryEscape(actor))
	}
	sign.SignPrefixed(req, "X-Berth", sign.DeriveKey(c.Token), body)
	return finish(c.hc, req, out)
}

func finish(hc *http.Client, req *http.Request, out any) error {
	resp, err := hc.Do(req)
	if err != nil {
		return &Error{Status: 0, Message: err.Error()}
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode >= 300 {
		var e struct {
			Error string `json:"error"`
		}
		json.Unmarshal(raw, &e)
		if e.Error == "" {
			e.Error = strings.TrimSpace(string(raw))
		}
		return &Error{Status: resp.StatusCode, Message: e.Error}
	}
	if out != nil && len(raw) > 0 {
		return json.Unmarshal(raw, out)
	}
	return nil
}

// ---- typed calls ----

// Forward is a Wharf port mapping (§4.1).
type Forward struct {
	ID             string   `json:"id,omitempty"`
	Name           string   `json:"name"`
	Proto          string   `json:"proto"`
	WANPort        string   `json:"wan_port"`
	TargetIP       string   `json:"target_ip"`
	TargetPort     string   `json:"target_port"`
	Mode           string   `json:"mode,omitempty"` // "" | restrict
	AllowCountries []string `json:"allow_countries,omitempty"`
	BerthRef       string   `json:"berth_ref,omitempty"`
	BerthHost      string   `json:"berth_host,omitempty"`
	Note           string   `json:"note,omitempty"`
	Kind           string   `json:"kind,omitempty"` // berth for Berth's own
}

func (c *Client) Forwards(ctx context.Context) ([]Forward, error) {
	var out []Forward
	return out, c.Do(ctx, "GET", "/forwards", "", nil, &out)
}

// PlanForward asks what creating f would change (and whether it conflicts).
func (c *Client) PlanForward(ctx context.Context, f Forward) (json.RawMessage, error) {
	var out json.RawMessage
	return out, c.Do(ctx, "POST", "/forwards?plan=1", "", f, &out)
}

func (c *Client) CreateForward(ctx context.Context, actor string, f Forward) (Forward, error) {
	var out Forward
	return out, c.Do(ctx, "POST", "/forwards", actor, f, &out)
}

func (c *Client) DeleteForward(ctx context.Context, actor, id string) error {
	err := c.Do(ctx, "DELETE", "/forwards/"+url.PathEscape(id), actor, nil, nil)
	if IsStatus(err, 404) {
		return nil // already gone
	}
	return err
}

// SNI is Berth's registration with Wharf's proxy-front (§4.2).
type SNI struct {
	Target        string   `json:"target"`
	Domains       []string `json:"domains"`
	ProxyProtocol bool     `json:"proxy_protocol"`
}

func (c *Client) SetSNI(ctx context.Context, actor string, s SNI) error {
	return c.Do(ctx, "PUT", "/sni", actor, s, nil)
}

func (c *Client) GetSNI(ctx context.Context) (SNI, error) {
	var s SNI
	return s, c.Do(ctx, "GET", "/sni", "", nil, &s)
}

type DDNS struct {
	ID      string `json:"id"`
	Domain  string `json:"domain"`
	Owner   string `json:"owner"`
	IP      string `json:"ip,omitempty"`
	Status  string `json:"status,omitempty"`
	Updated int64  `json:"updated_at,omitempty"`
}

func (c *Client) DDNS(ctx context.Context) ([]DDNS, error) {
	var out []DDNS
	return out, c.Do(ctx, "GET", "/ddns", "", nil, &out)
}

func (c *Client) ClaimDDNS(ctx context.Context, actor, domain, owner string) error {
	return c.Do(ctx, "POST", "/ddns/claim", actor, map[string]string{"domain": domain, "owner": owner}, nil)
}

type Device struct {
	Name   string `json:"name"`
	IP     string `json:"ip"`
	MAC    string `json:"mac,omitempty"`
	Type   string `json:"type,omitempty"`
	Online bool   `json:"online"`
}

func (c *Client) Devices(ctx context.Context) ([]Device, error) {
	var out []Device
	return out, c.Do(ctx, "GET", "/devices", "", nil, &out)
}

// HostView: the router's view of a Berth host (traffic and inbound).
func (c *Client) HostView(ctx context.Context, ip, what, rng string) (json.RawMessage, error) {
	var out json.RawMessage
	return out, c.Do(ctx, "GET", "/hosts/"+url.PathEscape(ip)+"/"+what+"?range="+url.QueryEscape(rng), "", nil, &out)
}

type Ban struct {
	ID        string `json:"id,omitempty"`
	CIDR      string `json:"cidr"`
	Reason    string `json:"reason,omitempty"`
	Source    string `json:"source,omitempty"`
	ExpiresAt string `json:"expires_at,omitempty"` // RFC 3339; empty = permanent
}

func (c *Client) PushBan(ctx context.Context, actor string, b Ban) (string, error) {
	var out struct {
		ID string `json:"id"`
	}
	err := c.Do(ctx, "POST", "/bans", actor, b, &out)
	return out.ID, err
}

func (c *Client) WithdrawBan(ctx context.Context, actor, id string) error {
	err := c.Do(ctx, "DELETE", "/bans/"+url.PathEscape(id), actor, nil, nil)
	if IsStatus(err, 404) {
		return nil
	}
	return err
}

type Alert struct {
	Level     string `json:"level"` // info | warn | critical
	Title     string `json:"title"`
	Body      string `json:"body"`
	Link      string `json:"link,omitempty"`
	DedupeKey string `json:"dedupe_key,omitempty"`
}

func (c *Client) Alert(ctx context.Context, a Alert) error {
	return c.Do(ctx, "POST", "/alerts", "", a, nil)
}

// Ticket asks Wharf for a one-time sign-in link for a Berth user (§4.7).
func (c *Client) Ticket(ctx context.Context, berthUser, returnTo string) (string, error) {
	var out struct {
		URL string `json:"url"`
	}
	err := c.Do(ctx, "POST", "/sso/ticket", "", map[string]string{"berth_user": berthUser, "return_to": returnTo}, &out)
	return out.URL, err
}

// Unpair tells Wharf the link is gone; keep says whether Berth-made forwards stay (as
// ordinary forwards) or are deleted.
func (c *Client) Unpair(ctx context.Context, keep bool) error {
	err := c.Do(ctx, "POST", "/unpair", "", map[string]bool{"keep": keep}, nil)
	if IsStatus(err, 404) || IsStatus(err, 405) { // early Wharf builds
		err = c.Do(ctx, "DELETE", "/pair?keep="+fmt.Sprint(keep), "", nil, nil)
	}
	return err
}

// ---- protocol v2 ----

// PingResult is Wharf's answer to GET /ping; Protocol 2 and up takes X-Berth-Key.
type PingResult struct {
	OK       bool     `json:"ok"`
	Name     string   `json:"name"`
	Version  string   `json:"version"`
	Scopes   []string `json:"scopes"`
	Protocol int      `json:"protocol"`
	KeyAuth  bool     `json:"key_auth"`
}

func (c *Client) Ping(ctx context.Context) (PingResult, error) {
	var out PingResult
	return out, c.Do(ctx, "GET", "/ping", "", nil, &out)
}

// PairRequest asks a Wharf found on the LAN to be paired (its admin allows it there); not signed.
type PairRequest struct {
	Code      string   `json:"code"`
	BerthName string   `json:"berth_name"`
	BerthURLs []string `json:"berth_urls"`
	BerthFP   string   `json:"berth_fp"`
	WharfFP   string   `json:"wharf_fp"`
}

type PairAsk struct {
	ID        string `json:"id"`
	SAS       string `json:"sas"`
	ExpiresAt int64  `json:"expires_at"`
}

// PairAskState: waiting | refused | allowed (then Code is Wharf's one-time code for Exchange).
type PairAskState struct {
	State    string `json:"state"`
	Code     string `json:"code,omitempty"`
	WharfURL string `json:"wharf_url,omitempty"`
}

func plain(ctx context.Context, base, fp, path string, in, out any) error {
	b, _ := json.Marshal(in)
	req, err := http.NewRequestWithContext(ctx, "POST", strings.TrimRight(base, "/")+Prefix+path, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	return finish(httpClient(fp), req, out)
}

func RequestPair(ctx context.Context, base, fp string, in PairRequest) (PairAsk, error) {
	var out PairAsk
	return out, plain(ctx, base, fp, "/pair/request", in, &out)
}

func RequestStatus(ctx context.Context, base, fp, code string) (PairAskState, error) {
	var out PairAskState
	return out, plain(ctx, base, fp, "/pair/request/status", map[string]string{"code": code}, &out)
}

// SAS: the six digits both panels show for a LAN request (Wharf computes the same).
func SAS(code, berthFP, wharfFP string) string {
	h := sha256.Sum256([]byte("berth-wharf-sas" + code + berthFP + wharfFP))
	return fmt.Sprintf("%03d %03d", binary.BigEndian.Uint32(h[0:4])%1000, binary.BigEndian.Uint32(h[4:8])%1000)
}

// DNSNames: names Berth registered on the router's DNS (scope dns).
type DNSNames struct {
	Items    []struct{ Name, IP string } `json:"items"`
	Suffixes []string                    `json:"suffixes"`
	Max      int                         `json:"max"`
}

func (c *Client) DNSNames(ctx context.Context) (DNSNames, error) {
	var out DNSNames
	return out, c.Do(ctx, "GET", "/dns/names", "", nil, &out)
}

func (c *Client) SetDNSName(ctx context.Context, actor, name, ip string) error {
	return c.Do(ctx, "PUT", "/dns/names/"+url.PathEscape(name), actor, map[string]string{"ip": ip}, nil)
}

func (c *Client) DeleteDNSName(ctx context.Context, actor, name string) error {
	err := c.Do(ctx, "DELETE", "/dns/names/"+url.PathEscape(name), actor, nil, nil)
	if IsStatus(err, 404) {
		return nil
	}
	return err
}
