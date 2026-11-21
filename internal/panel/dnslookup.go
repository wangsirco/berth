package panel

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/wangsirco/berth/internal/panel/dns"
)

// Per-name DNS state for the site dialogs: which DNS account holds the name, what its records
// say, and whether they reach this server (directly or through its Cloudflare Tunnel). The
// dialogs offer to create or change the record from here; each of those is its own change
// with a preview, so nothing in DNS moves without a second confirmation.

type nameLookup struct {
	Name     string       `json:"name"`
	Root     string       `json:"root"`
	Account  string       `json:"account,omitempty"` // DNS account id holding the zone
	Provider string       `json:"provider,omitempty"`
	ZoneID   string       `json:"zone_id,omitempty"`
	Records  []dns.Record `json:"records"`
	// here: points at this server; tunnel: at this server's Cloudflare Tunnel; elsewhere;
	// none: no record; unknown: no account and the public lookup failed
	Points  string      `json:"points"`
	Tunnel  bool        `json:"tunnel"`            // the name is routed through this host's tunnel
	Suggest *dns.Record `json:"suggest,omitempty"` // the record that would make it point here
}

func (a *App) lookupNames(ctx context.Context, host string, names []string) []nameLookup {
	zones, _, ins := a.accountZones(ctx)
	kind := map[string]string{}
	for _, in := range ins {
		kind[in.ID] = in.Kind
	}
	here := map[string]bool{}
	var v4 string
	for _, typ := range []string{"A", "AAAA"} {
		if ip, err := a.publicIP(ctx, host, typ); err == nil && ip != "" {
			here[ip] = true
			if typ == "A" {
				v4 = ip
			}
		}
	}
	tunnelID := ""
	if t, _, ok := a.tunnelOf(host); ok {
		tunnelID = t.TunnelID
	}
	routed := map[string]bool{}
	for _, r := range a.tunnelRoutes(host) {
		for _, d := range r.Domains {
			routed[d] = true
		}
	}
	out := make([]nameLookup, len(names))
	var wg sync.WaitGroup
	for i, n := range names {
		n = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(n), "."))
		out[i] = nameLookup{Name: n, Root: registrable(n), Records: []dns.Record{}, Points: "unknown", Tunnel: routed[n]}
		wg.Add(1)
		go func(x *nameLookup) {
			defer wg.Done()
			// the account whose zone is the longest suffix of the name
			best := ""
			for acc, zs := range zones {
				for _, z := range zs {
					if (x.Name == z.Name || strings.HasSuffix(x.Name, "."+z.Name)) && len(z.Name) > len(best) {
						best, x.Account, x.ZoneID, x.Provider = z.Name, acc, z.ID, kind[acc]
					}
				}
			}
			if x.Account != "" {
				if p, err := a.provider(x.Account); err == nil {
					c, cancel := context.WithTimeout(ctx, 15*time.Second)
					recs, err := p.Records(c, x.ZoneID)
					cancel()
					if err == nil {
						for _, r := range recs {
							if r.Name == x.Name && (r.Type == "A" || r.Type == "AAAA" || r.Type == "CNAME") {
								x.Records = append(x.Records, r)
							}
						}
						x.Points = pointsOf(x.Records, here, tunnelID)
					}
				}
			} else if !strings.HasPrefix(x.Name, "*.") {
				var res []dnsRes
				c, cancel := context.WithTimeout(ctx, 10*time.Second)
				if a.call(c, host, "POST", "/v1/probe/dns", map[string]any{"name": x.Name, "resolvers": []string{"1.1.1.1"}}, &res) == nil {
					for _, r := range res {
						for _, ip := range r.IPs {
							x.Records = append(x.Records, dns.Record{Type: "A", Name: x.Name, Content: ip})
						}
					}
					x.Points = pointsOf(x.Records, here, tunnelID)
				}
				cancel()
			}
			// what to create or change it to
			switch {
			case x.Tunnel && tunnelID != "":
				x.Suggest = &dns.Record{Type: "CNAME", Name: x.Name, Content: tunnelID + ".cfargotunnel.com", TTL: 1, Proxied: true}
			case v4 != "":
				x.Suggest = &dns.Record{Type: "A", Name: x.Name, Content: v4, TTL: 1, Proxied: x.Provider == "cloudflare"}
			}
		}(&out[i])
	}
	wg.Wait()
	return out
}

func pointsOf(recs []dns.Record, here map[string]bool, tunnelID string) string {
	if len(recs) == 0 {
		return "none"
	}
	for _, r := range recs {
		if here[r.Content] {
			return "here"
		}
		if tunnelID != "" && strings.EqualFold(r.Content, tunnelID+".cfargotunnel.com") {
			return "tunnel"
		}
	}
	return "elsewhere"
}

func init() {
	routeHooks = append(routeHooks, func(a *App, mux *http.ServeMux) {
		mux.HandleFunc("GET /api/v1/hosts/{host}/domains/lookup", a.authed("sites.view", func(w http.ResponseWriter, r *http.Request) {
			var names []string
			for _, n := range strings.Split(r.URL.Query().Get("names"), ",") {
				if n = strings.TrimSpace(n); n != "" && !slices.Contains(names, n) && len(names) < 20 {
					names = append(names, n)
				}
			}
			ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
			defer cancel()
			writeJSON(w, 200, map[string]any{"items": a.lookupNames(ctx, r.PathValue("host"), names)})
		}))
	})
}
