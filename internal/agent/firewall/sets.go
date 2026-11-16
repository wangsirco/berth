package firewall

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Cloudflare's published edge ranges (fallback when the list cannot be fetched).
var cloudflareFallback = []string{
	"173.245.48.0/20", "103.21.244.0/22", "103.22.200.0/22", "103.31.4.0/22", "141.101.64.0/18", "108.162.192.0/18",
	"190.93.240.0/20", "188.114.96.0/20", "197.234.240.0/22", "198.41.128.0/17", "162.158.0.0/15", "104.16.0.0/13",
	"104.24.0.0/14", "172.64.0.0/13", "131.0.72.0/22",
	"2400:cb00::/32", "2606:4700::/32", "2803:f800::/32", "2405:b500::/32", "2405:8100::/32", "2a06:98c0::/29", "2c0f:f248::/32",
}

var cloudflareURLs = []string{"https://www.cloudflare.com/ips-v4", "https://www.cloudflare.com/ips-v6"}

// CloudflareNets returns the ranges, refreshed at most daily (cached under Dir).
func CloudflareNets(ctx context.Context) []string {
	cache := filepath.Join(Dir, "cloudflare.txt")
	if st, err := os.Stat(cache); err == nil && time.Since(st.ModTime()) < 24*time.Hour {
		if b, err := os.ReadFile(cache); err == nil {
			if nets := parseNets(string(b)); len(nets) > 10 {
				return nets
			}
		}
	}
	var all []string
	cl := &http.Client{Timeout: 10 * time.Second}
	for _, u := range cloudflareURLs {
		req, _ := http.NewRequestWithContext(ctx, "GET", u, nil)
		resp, err := cl.Do(req)
		if err != nil {
			break
		}
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		resp.Body.Close()
		if resp.StatusCode != 200 {
			break
		}
		all = append(all, parseNets(string(b))...)
	}
	if len(all) > 10 {
		os.MkdirAll(Dir, 0o700)
		os.WriteFile(cache, []byte(strings.Join(all, "\n")+"\n"), 0o600)
		return all
	}
	if b, err := os.ReadFile(cache); err == nil { // stale beats the built-in list
		if nets := parseNets(string(b)); len(nets) > 10 {
			return nets
		}
	}
	return cloudflareFallback
}

func parseNets(s string) []string {
	var out []string
	for _, l := range strings.Fields(s) {
		if _, _, err := net.ParseCIDR(l); err == nil {
			out = append(out, l)
		}
	}
	return out
}

// CountryNets is set by the agent (the access-log country database).
var CountryNets func(ctx context.Context, code string) ([]string, error)

// fillSets adds the address lists the rules need and the panel did not send.
func (d *Desired) fillSets(ctx context.Context) error {
	if d.Sets == nil {
		d.Sets = Sets{}
	}
	for _, r := range d.Rules {
		if !r.Enabled {
			continue
		}
		switch n := setName(r); {
		case r.Source == "cloudflare" && len(d.Sets[n]) == 0:
			d.Sets[n] = CloudflareNets(ctx)
		case r.Source == "country" && len(d.Sets[n]) == 0:
			if CountryNets == nil {
				return fmt.Errorf("country data unavailable")
			}
			nets, err := CountryNets(ctx, r.Value)
			if err != nil {
				return fmt.Errorf("country data: %w", err)
			}
			if len(nets) == 0 {
				return fmt.Errorf("no networks for country %s", r.Value)
			}
			d.Sets[n] = nets
		}
	}
	return nil
}
