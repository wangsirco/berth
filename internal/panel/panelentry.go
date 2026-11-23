package panel

// The panel on 443, by name: the edge (Caddy, already on 443 for the sites) routes the
// panel's own names to the panel, so https://mini.lan opens it without a port. The LAN names
// carry a certificate from the panel's own CA (the one devices already trust); public names
// get theirs from ACME like any site. The panel keeps listening on its port as the fallback.

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/wangsirco/berth/internal/agent/caddy"
	"github.com/wangsirco/berth/internal/change"
	"github.com/wangsirco/berth/internal/panel/caddycfg"
	"github.com/wangsirco/berth/internal/panel/certkit"
	"github.com/wangsirco/berth/internal/sign"
)

// panelCertID: the edge's slot for the panel certificate (stored certificates have small ids)
const panelCertID = "999000000001"

type entryCert struct {
	Names    []string `json:"names"`
	Version  string   `json:"version"`
	Cert     string   `json:"cert"`
	KeyEnc   []byte   `json:"key_enc"`
	NotAfter int64    `json:"not_after"`
}

// entryNames: the names that should open the panel on 443
func (a *App) entryNames() (lan, pub []string) {
	if h := strings.ToLower(a.setting("panel.lan_host")); h != "" && net.ParseIP(h) == nil {
		lan = append(lan, h)
	}
	// the machine's own names work before any is chosen (the panel certificate has them too)
	if base := hostBase(); base != "" {
		for _, n := range []string{base + ".local", base + ".lan"} {
			if !contains(lan, n) {
				lan = append(lan, n)
			}
		}
	}
	if h := strings.ToLower(a.setting("panel.public_host")); h != "" && net.ParseIP(h) == nil && !a.domainUsedBySite(h) {
		pub = append(pub, h)
	}
	return lan, pub
}

// hostBase: this machine's short host name, lower case ("" for localhost)
func hostBase() string {
	h, _ := os.Hostname()
	b := strings.ToLower(strings.Split(h, ".")[0])
	if b == "localhost" || !dnsLabel.MatchString(b) {
		return ""
	}
	return b
}

var dnsLabel = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

func (a *App) domainUsedBySite(d string) bool {
	var n int
	a.db.QueryRow(`SELECT COUNT(*) FROM sites WHERE domains LIKE ?`, `%"`+d+`"%`).Scan(&n)
	return n > 0
}

// edgeSecret: the edge sends it with every request it passes to the panel; only then the
// panel takes X-Forwarded-For as the visitor's address
func (a *App) edgeSecret() string {
	if s := a.setting("access.edge_secret"); s != "" {
		return s
	}
	s := sign.RandomHex(24)
	a.setSetting("access.edge_secret", s)
	return s
}

func (a *App) fromEdge(r *http.Request) bool {
	h := r.Header.Get("X-Berth-Edge")
	s := a.setting("access.edge_secret")
	return h != "" && s != "" && subtle.ConstantTimeCompare([]byte(h), []byte(s)) == 1
}

// entryCertFor: the panel certificate for these LAN names, issued again when the names
// change or it has less than 30 days left
func (a *App) entryCertFor(names []string) (*entryCert, error) {
	sorted := append([]string{}, names...)
	sort.Strings(sorted)
	var c entryCert
	json.Unmarshal([]byte(a.setting("access.entry_cert")), &c)
	if strings.Join(c.Names, ",") == strings.Join(sorted, ",") && time.Until(time.Unix(c.NotAfter, 0)) > 30*24*time.Hour {
		return &c, nil
	}
	if a.localSigner == nil {
		return nil, nil
	}
	b, err := a.localSigner.Issue(certkit.Fields{CN: sorted[0], O: "Berth", DNS: sorted, Days: 300, KeyType: "ec256", Usage: []string{"server"}})
	if err != nil {
		return nil, err
	}
	enc, err := a.seal(b.KeyPEM)
	if err != nil {
		return nil, err
	}
	h := sha256.Sum256(b.CertPEM)
	c = entryCert{Names: sorted, Version: hex.EncodeToString(h[:])[:12], Cert: string(b.CertPEM), KeyEnc: enc, NotAfter: time.Now().Add(300 * 24 * time.Hour).Unix()}
	j, _ := json.Marshal(c)
	a.setSetting("access.entry_cert", string(j))
	return &c, nil
}

// panelEntry: what the edge on this machine needs to serve the panel (nil: nothing to serve)
func (a *App) panelEntry() *caddycfg.PanelEntry {
	if a.cfg.NoEdgeEntry {
		return nil
	}
	lan, pub := a.entryNames()
	if len(lan)+len(pub) == 0 || a.setting("access.entry") == "off" {
		return nil
	}
	p := &caddycfg.PanelEntry{LANHosts: lan, PublicHosts: pub, Upstream: "host.docker.internal:" + a.panelPort(), Secret: a.edgeSecret()}
	if len(lan) > 0 {
		c, err := a.entryCertFor(lan)
		if err != nil || c == nil {
			p.LANHosts = nil
		} else {
			cp, kp := caddy.CertPaths(panelCertID, c.Version)
			p.Cert = caddycfg.CertFiles{Cert: cp, Key: kp, Version: c.Version}
		}
	}
	if len(p.LANHosts)+len(p.PublicHosts) == 0 {
		return nil
	}
	return p
}

// pushEntryCert writes the panel certificate to this machine's edge
func (a *App) pushEntryCert(ctx context.Context) error {
	if a.cfg.NoEdgeEntry {
		return nil
	}
	lan, _ := a.entryNames()
	if len(lan) == 0 {
		return nil
	}
	c, err := a.entryCertFor(lan)
	if err != nil || c == nil {
		return err
	}
	key, err := a.open(c.KeyEnc)
	if err != nil {
		return err
	}
	return a.call(ctx, "local", "PUT", "/v1/edge/certs/"+panelCertID, map[string]string{"version": c.Version, "cert": c.Cert, "key": string(key)}, nil)
}

// entryKey: what the edge serves for the panel; a change means loading the edge again
func (a *App) entryKey() string {
	p := a.panelEntry()
	if p == nil {
		return "none"
	}
	return strings.Join(p.LANHosts, ",") + "|" + strings.Join(p.PublicHosts, ",") + "|" + p.Cert.Version
}

// entrySync loads the edge again when the panel's names changed; afterwards the names
// open the panel without a port (entryServes)
func (a *App) entrySync(ctx context.Context) {
	if a.cfg.NoEdgeEntry {
		return
	}
	key := a.entryKey()
	if key == a.setting("access.entry_served") {
		return
	}
	st, err := a.edgeStatus(ctx, "local")
	if err != nil || !st.Running {
		if a.setting("access.entry_served") != "" {
			a.setSetting("access.entry_served", "") // no edge: names need the port
		}
		return
	}
	if err := a.applyEdge(change.Env{Ctx: ctx, Host: "local"}); err != nil {
		return
	}
	a.setSetting("access.entry_served", key)
	go a.notifyWharfNames() // now without a port: tell the router
}

func (a *App) entrySyncSoon() {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	a.entrySync(ctx)
}

// entryServes: this name opens the panel on 443 (no port in links)
func (a *App) entryServes(host string) bool {
	served := a.setting("access.entry_served")
	if served == "" || served == "none" || host == "" {
		return false
	}
	parts := strings.SplitN(served, "|", 3)
	for _, p := range parts[:min(2, len(parts))] {
		for _, h := range strings.Split(p, ",") {
			if h != "" && strings.EqualFold(h, host) {
				return true
			}
		}
	}
	return false
}

func init() {
	startHooks = append(startHooks, func(a *App, ctx context.Context) {
		go func() {
			time.Sleep(5 * time.Second)
			c, cancel := context.WithTimeout(ctx, 60*time.Second)
			a.entrySync(c)
			cancel()
		}()
	})
}
