// Package dns manages DNS records at providers. Cloudflare has full record management;
// other providers are only used by Caddy (libdns plugins) for DNS-01 certificates.
package dns

import "context"

type Zone struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Status  string `json:"status"`
	Account string `json:"account,omitempty"`
	// AccountID is the Cloudflare account (tunnels and Access live there)
	AccountID string `json:"account_id,omitempty"`
}

type Record struct {
	ID      string `json:"id,omitempty"`
	Type    string `json:"type"` // A | AAAA | CNAME | TXT | MX ...
	Name    string `json:"name"` // full name, e.g. shop.example.com
	Content string `json:"content"`
	TTL     int    `json:"ttl"`               // 1 = automatic
	Proxied bool   `json:"proxied,omitempty"` // Cloudflare orange cloud
	Comment string `json:"comment,omitempty"`
}

type Provider interface {
	Verify(ctx context.Context) (Check, error)
	Zones(ctx context.Context) ([]Zone, error)
	Records(ctx context.Context, zoneID string) ([]Record, error)
	Create(ctx context.Context, zoneID string, r Record) (Record, error)
	Update(ctx context.Context, zoneID string, r Record) (Record, error)
	Delete(ctx context.Context, zoneID, recordID string) error
}

// Check is what a token can do, tested item by item.
type Check struct {
	Active    bool   `json:"active"`
	ZonesRead bool   `json:"zones_read"`
	DNSRead   bool   `json:"dns_read"`
	ZoneCount int    `json:"zone_count"`
	ExpiresAt string `json:"expires_at,omitempty"`
	Message   string `json:"message,omitempty"`
}

// Error is a provider error with a stable code for translation.
type Error struct {
	Code   string // dns.auth | dns.forbidden | dns.not_found | dns.conflict | dns.api
	Detail string
}

func (e *Error) Error() string { return e.Code + ": " + e.Detail }

// ZoneFor picks the longest zone that name belongs to.
func ZoneFor(zones []Zone, name string) (Zone, bool) {
	var best Zone
	for _, z := range zones {
		if name == z.Name || hasSuffix(name, "."+z.Name) {
			if len(z.Name) > len(best.Name) {
				best = z
			}
		}
	}
	return best, best.ID != ""
}

func hasSuffix(s, suf string) bool { return len(s) >= len(suf) && s[len(s)-len(suf):] == suf }
