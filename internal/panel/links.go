package panel

// Panel links (面板互联): two servers that both run a Berth panel. One panel (main) manages
// the servers the other (managed) hands over; the managed panel keeps working by itself and
// can take them back any time.
//
// Pairing: the main panel makes a code (its addresses, certificate fingerprint, a one-time
// secret and an X25519 public key). The managed panel checks the code against the main
// panel, shows who wants what and a 6-digit check code, and on confirmation sends its own
// public key; both sides derive the link key (it never crosses the network). The main
// panel's admin confirms too, then the link is active.
//
// Afterwards every request between them is signed with the link key (timestamp, nonce, body
// hash; replays refused), so a TLS-terminating proxy on a public path (Cloudflare…) learns
// nothing that lets it act as either panel. Requests for a handed-over server go through
// the managed panel's own API (its previews, database, edge config and audit), as a
// restricted admin limited to those servers.

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/wangsirco/berth/internal/errs"
	"github.com/wangsirco/berth/internal/sign"
	"github.com/wangsirco/berth/internal/version"
)

const (
	linkRoleMain    = "main"    // we manage the peer's servers
	linkRoleManaged = "managed" // the peer manages ours
	linkCodeTTL     = 15 * time.Minute
	linkPrefix      = "X-Berth-Link" // signature headers: -Id, -Ts, -Nonce, -Sig
	linkCodeTag     = "berth-link:"
)

type panelLink struct {
	ID          int64    `json:"id"`
	Role        string   `json:"role"`
	Status      string   `json:"status"` // main: code → confirm → active; managed: wait → active
	Name        string   `json:"name"`
	Slug        string   `json:"slug"`
	Addrs       []string `json:"addrs"`
	FP          string   `json:"fp"`
	UID         string   `json:"-"`
	SAS         string   `json:"sas,omitempty"`
	Hosts       []string `json:"hosts"`
	By          string   `json:"by"`
	ByID        int64    `json:"-"`
	CreatedAt   int64    `json:"created_at"`
	ExpiresAt   int64    `json:"expires_at,omitempty"`
	ConfirmedAt int64    `json:"confirmed_at,omitempty"`
	LastAddr    string   `json:"last_addr,omitempty"`
	Mode        string   `json:"mode"` // lan | public ("" before modes: whatever answers)
	key         []byte
	secretHash  string
	priv        []byte
}

const linkCols = `id, role, status, name, slug, addrs, fp, uid, key_enc, secret_hash, priv_enc, sas, hosts, by_user, by_id, created_at, expires_at, confirmed_at, last_addr, mode`

func (a *App) scanLink(row interface{ Scan(...any) error }) (*panelLink, error) {
	var l panelLink
	var addrs, hosts string
	var keyEnc, privEnc []byte
	if err := row.Scan(&l.ID, &l.Role, &l.Status, &l.Name, &l.Slug, &addrs, &l.FP, &l.UID, &keyEnc, &l.secretHash, &privEnc, &l.SAS, &hosts, &l.By, &l.ByID, &l.CreatedAt, &l.ExpiresAt, &l.ConfirmedAt, &l.LastAddr, &l.Mode); err != nil {
		return nil, err
	}
	json.Unmarshal([]byte(addrs), &l.Addrs)
	json.Unmarshal([]byte(hosts), &l.Hosts)
	if l.Addrs == nil {
		l.Addrs = []string{}
	}
	if l.Hosts == nil {
		l.Hosts = []string{}
	}
	if len(keyEnc) > 0 {
		l.key, _ = a.open(keyEnc)
	}
	if len(privEnc) > 0 {
		l.priv, _ = a.open(privEnc)
	}
	return &l, nil
}

func (a *App) links(where string, args ...any) []*panelLink {
	rows, err := a.db.Query(`SELECT `+linkCols+` FROM panel_links `+where, args...)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []*panelLink
	for rows.Next() {
		if l, err := a.scanLink(rows); err == nil {
			out = append(out, l)
		}
	}
	return out
}

func (a *App) link(where string, args ...any) (*panelLink, error) {
	l := a.links(where+" LIMIT 1", args...)
	if len(l) == 0 {
		return nil, errs.New(404, "link.not_found")
	}
	return l[0], nil
}

func jsonText(v any) string { b, _ := json.Marshal(v); return string(b) }

// ---- the code the main panel hands out ----

type linkCode struct {
	V     int      `json:"v"`
	Addrs []string `json:"a"`
	FP    string   `json:"fp"`
	S     string   `json:"s"` // one-time secret
	Name  string   `json:"n"`
	Pub   string   `json:"k"` // X25519 public key (base64)
	Mode  string   `json:"m,omitempty"` // lan | public: which names both sides use
}

func encodeLinkCode(c linkCode) string {
	b, _ := json.Marshal(c)
	return linkCodeTag + base64.RawURLEncoding.EncodeToString(b)
}

func decodeLinkCode(s string) (linkCode, error) {
	var c linkCode
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, linkCodeTag)
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil || json.Unmarshal(b, &c) != nil || c.V != 1 || c.S == "" || c.Pub == "" || len(c.Addrs) == 0 {
		return c, errs.New(400, "link.bad_code")
	}
	for _, u := range c.Addrs {
		if p, err := url.Parse(u); err != nil || p.Scheme != "https" || p.Host == "" {
			return c, errs.New(400, "link.bad_code")
		}
	}
	return c, nil
}

func secretHash(s string) string {
	h := sha256.Sum256([]byte("berth-link-secret:" + s))
	return hex.EncodeToString(h[:])
}

// linkKey derives the link key from the X25519 exchange, bound to both public keys.
func linkKey(priv *ecdh.PrivateKey, peerPub []byte, mainPub, managedPub []byte) ([]byte, error) {
	pp, err := ecdh.X25519().NewPublicKey(peerPub)
	if err != nil {
		return nil, err
	}
	shared, err := priv.ECDH(pp)
	if err != nil {
		return nil, err
	}
	h := sha256.New()
	h.Write([]byte("berth-panel-link-v1"))
	h.Write(shared)
	h.Write(mainPub)
	h.Write(managedPub)
	return h.Sum(nil), nil
}

// linkSAS: a 6-digit check code both admins see; equal codes mean nobody swapped a key in between.
func linkSAS(mainPub, managedPub []byte) string {
	h := sha256.Sum256(append(append([]byte("berth-link-sas"), mainPub...), managedPub...))
	return fmt.Sprintf("%03d %03d", binary.BigEndian.Uint32(h[:4])%1000, binary.BigEndian.Uint32(h[4:8])%1000)
}

var linkSlugRe = regexp.MustCompile(`[^a-z0-9-]+`)

// linkSlug turns the peer's name into the part after @ in its servers' names here.
func (a *App) linkSlug(name string, id int64) string {
	s := strings.Trim(linkSlugRe.ReplaceAllString(strings.ToLower(name), "-"), "-")
	if len(s) > 24 {
		s = s[:24]
	}
	if s == "" || s == "local" {
		s = fmt.Sprintf("panel%d", id)
	}
	for _, l := range a.links(`WHERE role=? AND slug=? AND id<>?`, linkRoleMain, s, id) {
		_ = l
		s = fmt.Sprintf("%s-%d", s, id)
	}
	return s
}

// ourAddrs: where the peer can reach this panel, the LAN first.
func (a *App) ourAddrs(r *http.Request, extra ...string) []string {
	var out []string
	add := func(u string) {
		u = strings.TrimRight(strings.TrimSpace(u), "/")
		if u == "" || !strings.HasPrefix(u, "https://") {
			return
		}
		for _, x := range out {
			if x == u {
				return
			}
		}
		out = append(out, u)
	}
	for _, e := range extra {
		add(e)
	}
	add(a.panelURL("panel.lan_host"))
	add(a.panelURL("panel.public_host"))
	add(a.publicURL())
	if r != nil {
		add(panelBase(r))
	}
	return out
}

func (a *App) panelDisplayName() string {
	if n := a.setting("panel.name"); n != "" {
		return n
	}
	if h, err := os.Hostname(); err == nil && h != "" {
		return h
	}
	return "Berth"
}

// ---- signed requests between linked panels ----

var linkNonces = struct {
	sync.Mutex
	m map[string]time.Time
}{m: map[string]time.Time{}}

// linkSign adds the link id and signature headers.
func linkSign(req *http.Request, l *panelLink, body []byte) {
	req.Header.Set(linkPrefix+"-Id", l.UID)
	sign.SignPrefixed(req, linkPrefix, l.key, body)
}

// linkVerify finds the link a signed request comes from and checks it. The body is read
// (and put back) so handlers can use it.
func (a *App) linkVerify(r *http.Request, roles ...string) (*panelLink, []byte, error) {
	uid := r.Header.Get(linkPrefix + "-Id")
	if len(uid) != 32 {
		return nil, nil, errs.New(401, "link.unsigned")
	}
	l, err := a.link(`WHERE uid=?`, uid)
	if err != nil || len(l.key) == 0 {
		return nil, nil, errs.New(401, "link.unknown")
	}
	ok := len(roles) == 0
	for _, ro := range roles {
		ok = ok || l.Role == ro
	}
	if !ok {
		return nil, nil, errs.New(403, "link.wrong_side")
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 64<<20))
	if err != nil {
		return nil, nil, err
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	nonce, err := sign.CheckPrefixed(r, linkPrefix, l.key, body)
	if err != nil {
		a.limiter.record("linkip:" + a.clientIP(r))
		return nil, nil, errs.New(401, "link.bad_signature")
	}
	linkNonces.Lock()
	now := time.Now()
	for n, exp := range linkNonces.m {
		if now.After(exp) {
			delete(linkNonces.m, n)
		}
	}
	_, dup := linkNonces.m[uid+nonce]
	linkNonces.m[uid+nonce] = now.Add(2 * sign.MaxSkew)
	linkNonces.Unlock()
	if dup {
		return nil, nil, errs.New(401, "link.replay")
	}
	return l, body, nil
}

// linkCall sends a signed request to the peer, trying its addresses (the last good one first).
func (a *App) linkCall(ctx context.Context, l *panelLink, method, path string, in, out any) error {
	var body []byte
	if in != nil {
		body, _ = json.Marshal(in)
	}
	var last error
	for _, base := range a.linkOrder(l) {
		req, err := http.NewRequestWithContext(ctx, method, base+path, bytes.NewReader(body))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		linkSign(req, l, body)
		resp, err := linkClient(l.FP).Do(req)
		if err != nil {
			last = err
			continue
		}
		err = readLinkResp(resp, out)
		if err == nil && base != l.LastAddr {
			a.db.Exec(`UPDATE panel_links SET last_addr=? WHERE id=?`, base, l.ID)
			l.LastAddr = base
		}
		return err
	}
	if last == nil {
		last = errors.New("no address")
	}
	return errs.New(502, "link.unreachable", "detail", last.Error())
}

func readLinkResp(resp *http.Response, out any) error {
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode >= 300 {
		var e struct{ Error errs.E }
		if json.Unmarshal(b, &e) == nil && e.Error.Code != "" {
			e.Error.Status = resp.StatusCode
			return &e.Error
		}
		return errs.New(502, "link.peer_error", "status", resp.StatusCode)
	}
	if out != nil && len(b) > 0 {
		return json.Unmarshal(b, out)
	}
	return nil
}

func (a *App) linkOrder(l *panelLink) []string {
	out := []string{}
	if l.LastAddr != "" {
		out = append(out, l.LastAddr)
	}
	for _, x := range l.Addrs {
		if x != l.LastAddr {
			out = append(out, x)
		}
	}
	return out
}

// plain (unsigned) call during pairing: the one-time secret proves the code
func linkPlain(ctx context.Context, base, fp, path string, in, out any) error {
	body, _ := json.Marshal(in)
	req, err := http.NewRequestWithContext(ctx, "POST", base+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := linkClient(fp).Do(req)
	if err != nil {
		return err
	}
	return readLinkResp(resp, out)
}

func randUID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func eqSecret(a, b string) bool { return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1 }

var _ = sql.ErrNoRows
var _ = version.Version
