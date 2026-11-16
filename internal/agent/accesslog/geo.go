package accesslog

import (
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/oschwald/maxminddb-golang/v2"
)

// Geo maps IPs to countries with the DB-IP "IP to Country Lite" database
// (https://db-ip.com, CC BY 4.0), downloaded monthly. The file is memory-mapped.
type Geo struct {
	Path string
	URL  func(t time.Time) string // tests override
	// Needed reports whether there is anything to look up (no site, no download).
	Needed func() bool

	mu       sync.RWMutex
	r        *maxminddb.Reader
	netCache map[string][]string
	netStamp time.Time
}

func NewGeo(dir string) *Geo {
	return &Geo{Path: filepath.Join(dir, "dbip-country-lite.mmdb"), URL: func(t time.Time) string {
		return fmt.Sprintf("https://download.db-ip.com/free/dbip-country-lite-%s.mmdb.gz", t.Format("2006-01"))
	}}
}

var privateNets = func() []*net.IPNet {
	var out []*net.IPNet
	for _, c := range []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "100.64.0.0/10", "127.0.0.0/8", "fc00::/7", "fe80::/10", "::1/128"} {
		_, n, _ := net.ParseCIDR(c)
		out = append(out, n)
	}
	return out
}()

// Country is a Country func.
func (g *Geo) Country(ip net.IP) string {
	for _, n := range privateNets {
		if n.Contains(ip) {
			return "LAN"
		}
	}
	g.mu.RLock()
	r := g.r
	g.mu.RUnlock()
	if r == nil {
		return ""
	}
	addr, ok := netip.AddrFromSlice(ip)
	if !ok {
		return ""
	}
	var rec struct {
		Country struct {
			ISOCode string `maxminddb:"iso_code"`
		} `maxminddb:"country"`
	}
	if r.Lookup(addr.Unmap()).Decode(&rec) != nil {
		return ""
	}
	return rec.Country.ISOCode
}

func (g *Geo) load() error {
	r, err := maxminddb.Open(g.Path)
	if err != nil {
		return err
	}
	g.mu.Lock()
	old := g.r
	g.r = r
	g.mu.Unlock()
	if old != nil {
		old.Close()
	}
	return nil
}

// Refresh loads the database, downloading this month's (or last month's) when the file
// is missing or older than 32 days.
func (g *Geo) Refresh(ctx context.Context) error {
	if fi, err := os.Stat(g.Path); err == nil && time.Since(fi.ModTime()) < 32*24*time.Hour {
		g.mu.RLock()
		loaded := g.r != nil
		g.mu.RUnlock()
		if loaded {
			return nil
		}
		return g.load()
	}
	var last error
	now := time.Now().UTC()
	for _, t := range []time.Time{now, now.AddDate(0, -1, 0)} {
		if last = g.download(ctx, g.URL(t)); last == nil {
			return g.load()
		}
	}
	if _, err := os.Stat(g.Path); err == nil { // keep using the old file
		return g.load()
	}
	return last
}

func (g *Geo) download(ctx context.Context, url string) error {
	req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
	req.Header.Set("User-Agent", "berth")
	resp, err := (&http.Client{Timeout: 5 * time.Minute}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("%s: HTTP %d", url, resp.StatusCode)
	}
	zr, err := gzip.NewReader(io.LimitReader(resp.Body, 100<<20))
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(g.Path), 0o755); err != nil {
		return err
	}
	tmp := g.Path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, io.LimitReader(zr, 200<<20)); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	f.Close()
	if _, err := maxminddb.Open(tmp); err != nil { // must be a valid database
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, g.Path)
}

// Loop refreshes daily.
func (g *Geo) Loop(ctx context.Context, logf func(string, ...any)) {
	for {
		if g.Needed != nil && !g.Needed() {
			select {
			case <-ctx.Done():
				return
			case <-time.After(10 * time.Minute):
			}
			continue
		}
		if err := g.Refresh(ctx); err != nil {
			logf("berth agent: country database: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(24 * time.Hour):
		}
	}
}

// Shared is the agent's one country database (set by Register); the firewall uses it for
// country rules.
var Shared *Geo

// CountryNets lists every network the database assigns to a country (ISO code), loading
// or downloading the database first. Results are cached until the file changes.
func (g *Geo) CountryNets(ctx context.Context, code string) ([]string, error) {
	if err := g.Refresh(ctx); err != nil {
		return nil, err
	}
	fi, _ := os.Stat(g.Path)
	g.mu.Lock()
	if g.netCache == nil || !g.netStamp.Equal(fi.ModTime()) {
		g.netCache, g.netStamp = map[string][]string{}, fi.ModTime()
	}
	if v, ok := g.netCache[code]; ok {
		g.mu.Unlock()
		return v, nil
	}
	r := g.r
	g.mu.Unlock()
	if r == nil {
		return nil, fmt.Errorf("country database not loaded")
	}
	var out []string
	for res := range r.Networks() {
		var rec struct {
			Country struct {
				ISOCode string `maxminddb:"iso_code"`
			} `maxminddb:"country"`
		}
		if res.Decode(&rec) == nil && rec.Country.ISOCode == code {
			p := res.Prefix()
			if p.Addr().Is4In6() {
				p = netip.PrefixFrom(p.Addr().Unmap(), p.Bits()-96)
			}
			out = append(out, p.String())
		}
	}
	g.mu.Lock()
	g.netCache[code] = out
	g.mu.Unlock()
	return out, nil
}
