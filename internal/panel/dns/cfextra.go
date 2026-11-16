package dns

// Cloudflare features beyond DNS (方案 §6): Tunnel, Zero Trust Access, IP access rules
// (bans), the zone security level ("under attack"), cache purge and analytics.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Features is what a token may do, checked one by one with harmless reads.
type Features struct {
	DNS       bool `json:"dns"`
	Tunnel    bool `json:"tunnel"`
	Access    bool `json:"access"`
	Firewall  bool `json:"firewall"`  // IP access rules (ban sync)
	Settings  bool `json:"settings"`  // zone settings (under attack mode)
	Analytics bool `json:"analytics"` // GraphQL analytics
}

// CheckFeatures probes each permission against one zone and its account.
func (c *Cloudflare) CheckFeatures(ctx context.Context, z Zone) Features {
	ok := func(method, path string, q url.Values, in any) bool {
		_, err := c.do(ctx, method, path, q, in, nil)
		return err == nil
	}
	one := url.Values{"per_page": {"1"}}
	f := Features{
		DNS:      ok("GET", "/zones/"+url.PathEscape(z.ID)+"/dns_records", one, nil),
		Settings: ok("GET", "/zones/"+url.PathEscape(z.ID)+"/settings/security_level", nil, nil),
	}
	if z.AccountID != "" {
		acc := "/accounts/" + url.PathEscape(z.AccountID)
		f.Tunnel = ok("GET", acc+"/cfd_tunnel", one, nil)
		f.Access = ok("GET", acc+"/access/apps", one, nil)
		f.Firewall = ok("GET", acc+"/firewall/access_rules/rules", one, nil)
	}
	f.Analytics = c.graphql(ctx, `query { viewer { zones(filter: {zoneTag: $zone}) { zoneTag } } }`, map[string]any{"zone": z.ID}, nil) == nil
	return f
}

// ---- Tunnel ----

type Tunnel struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func (c *Cloudflare) CreateTunnel(ctx context.Context, account, name string) (Tunnel, error) {
	var t Tunnel
	_, err := c.do(ctx, "POST", "/accounts/"+url.PathEscape(account)+"/cfd_tunnel", nil,
		map[string]any{"name": name, "config_src": "cloudflare"}, &t)
	return t, err
}

// TunnelToken is what cloudflared runs with.
func (c *Cloudflare) TunnelToken(ctx context.Context, account, id string) (string, error) {
	var tok string
	_, err := c.do(ctx, "GET", "/accounts/"+url.PathEscape(account)+"/cfd_tunnel/"+url.PathEscape(id)+"/token", nil, nil, &tok)
	return tok, err
}

func (c *Cloudflare) DeleteTunnel(ctx context.Context, account, id string) error {
	_, err := c.do(ctx, "DELETE", "/accounts/"+url.PathEscape(account)+"/cfd_tunnel/"+url.PathEscape(id), url.Values{"cascade": {"true"}}, nil, nil)
	return err
}

// Ingress is one public hostname of a tunnel.
type Ingress struct {
	Hostname string `json:"hostname"`
	Service  string `json:"service"` // e.g. https://berth-caddy:443
}

// SetIngress replaces the tunnel's routes (Cloudflare-managed configuration). Requests
// reach the site gateway over HTTPS with the right SNI, so Caddy serves the same site and
// certificate as for direct visitors.
func (c *Cloudflare) SetIngress(ctx context.Context, account, id string, routes []Ingress) error {
	var ing []map[string]any
	for _, r := range routes {
		ing = append(ing, map[string]any{"hostname": r.Hostname, "service": r.Service,
			"originRequest": map[string]any{"originServerName": r.Hostname, "noTLSVerify": true}})
	}
	ing = append(ing, map[string]any{"service": "http_status:404"}) // the catch-all Cloudflare requires
	_, err := c.do(ctx, "PUT", "/accounts/"+url.PathEscape(account)+"/cfd_tunnel/"+url.PathEscape(id)+"/configurations", nil,
		map[string]any{"config": map[string]any{"ingress": ing}}, nil)
	return err
}

// TunnelTarget is the DNS name a tunnel hostname points at.
func TunnelTarget(id string) string { return id + ".cfargotunnel.com" }

// ---- Zero Trust Access ----

type AccessApp struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Domain string `json:"domain"`
}

// AccessPolicy: who may in (emails, email domains). Cloudflare asks for a one-time code.
type AccessPolicy struct {
	Emails       []string `json:"emails,omitempty"`
	EmailDomains []string `json:"email_domains,omitempty"`
}

func (c *Cloudflare) CreateAccessApp(ctx context.Context, account, domain string, pol AccessPolicy) (AccessApp, error) {
	var app AccessApp
	base := "/accounts/" + url.PathEscape(account) + "/access/apps"
	if _, err := c.do(ctx, "POST", base, nil, map[string]any{"name": "Berth: " + domain, "domain": domain, "type": "self_hosted",
		"session_duration": "24h", "app_launcher_visible": false}, &app); err != nil {
		return app, err
	}
	var include []map[string]any
	for _, e := range pol.Emails {
		include = append(include, map[string]any{"email": map[string]string{"email": e}})
	}
	for _, d := range pol.EmailDomains {
		include = append(include, map[string]any{"email_domain": map[string]string{"domain": d}})
	}
	if _, err := c.do(ctx, "POST", base+"/"+url.PathEscape(app.ID)+"/policies", nil,
		map[string]any{"name": "Berth allow", "decision": "allow", "include": include, "precedence": 1}, nil); err != nil {
		c.DeleteAccessApp(ctx, account, app.ID) // no half-made app without a policy
		return app, err
	}
	return app, nil
}

func (c *Cloudflare) DeleteAccessApp(ctx context.Context, account, id string) error {
	_, err := c.do(ctx, "DELETE", "/accounts/"+url.PathEscape(account)+"/access/apps/"+url.PathEscape(id), nil, nil, nil)
	return err
}

// ---- edge protection ----

// BlockIP adds an account-wide IP access rule (all zones) and returns its id.
func (c *Cloudflare) BlockIP(ctx context.Context, account, addr, note string) (string, error) {
	target := "ip"
	if strings.Contains(addr, "/") {
		target = "ip_range"
	}
	var r struct {
		ID string `json:"id"`
	}
	_, err := c.do(ctx, "POST", "/accounts/"+url.PathEscape(account)+"/firewall/access_rules/rules", nil,
		map[string]any{"mode": "block", "configuration": map[string]string{"target": target, "value": addr}, "notes": note}, &r)
	return r.ID, err
}

func (c *Cloudflare) UnblockIP(ctx context.Context, account, ruleID string) error {
	_, err := c.do(ctx, "DELETE", "/accounts/"+url.PathEscape(account)+"/firewall/access_rules/rules/"+url.PathEscape(ruleID), nil, nil, nil)
	if e, ok := err.(*Error); ok && e.Code == "dns.not_found" {
		return nil // already gone
	}
	return err
}

// SecurityLevel reads or sets a zone's security level (under_attack, high, medium…).
func (c *Cloudflare) SecurityLevel(ctx context.Context, zone string) (string, error) {
	var v struct {
		Value string `json:"value"`
	}
	_, err := c.do(ctx, "GET", "/zones/"+url.PathEscape(zone)+"/settings/security_level", nil, nil, &v)
	return v.Value, err
}

func (c *Cloudflare) SetSecurityLevel(ctx context.Context, zone, level string) error {
	_, err := c.do(ctx, "PATCH", "/zones/"+url.PathEscape(zone)+"/settings/security_level", nil, map[string]string{"value": level}, nil)
	return err
}

// PurgeCache clears the zone's cache (everything, or the given URLs).
func (c *Cloudflare) PurgeCache(ctx context.Context, zone string, urls []string) error {
	body := map[string]any{"purge_everything": true}
	if len(urls) > 0 {
		body = map[string]any{"files": urls}
	}
	_, err := c.do(ctx, "POST", "/zones/"+url.PathEscape(zone)+"/purge_cache", nil, body, nil)
	return err
}

// ---- analytics ----

func (c *Cloudflare) graphql(ctx context.Context, query string, vars map[string]any, out any) error {
	var res struct {
		Data   json.RawMessage `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	// GraphQL answers in its own envelope, not the v4 one
	b, _ := json.Marshal(map[string]any{"query": query, "variables": vars})
	raw, err := c.raw(ctx, "POST", "/graphql", b)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return &Error{Code: "dns.api", Detail: "unexpected response"}
	}
	if len(res.Errors) > 0 {
		return &Error{Code: "dns.forbidden", Detail: res.Errors[0].Message}
	}
	if out != nil {
		return json.Unmarshal(res.Data, out)
	}
	return nil
}

// DayStats is one day of a zone at the edge.
type DayStats struct {
	Date      string `json:"date"`
	Requests  int64  `json:"requests"`
	Cached    int64  `json:"cached"`
	Bytes     int64  `json:"bytes"`
	Threats   int64  `json:"threats"`
	PageViews int64  `json:"page_views"`
	UniqueIPs int64  `json:"uniques"`
}

// ZoneDays pulls the last n days (httpRequests1dGroups).
func (c *Cloudflare) ZoneDays(ctx context.Context, zone string, n int) ([]DayStats, error) {
	since := time.Now().UTC().AddDate(0, 0, -n).Format("2006-01-02")
	var data struct {
		Viewer struct {
			Zones []struct {
				Groups []struct {
					Dims struct {
						Date string `json:"date"`
					} `json:"dimensions"`
					Sum struct {
						Requests       int64 `json:"requests"`
						CachedRequests int64 `json:"cachedRequests"`
						Bytes          int64 `json:"bytes"`
						Threats        int64 `json:"threats"`
						PageViews      int64 `json:"pageViews"`
					} `json:"sum"`
					Uniq struct {
						Uniques int64 `json:"uniques"`
					} `json:"uniq"`
				} `json:"httpRequests1dGroups"`
			} `json:"zones"`
		} `json:"viewer"`
	}
	q := `query($zone: String!, $since: Date!) { viewer { zones(filter: {zoneTag: $zone}) {
  httpRequests1dGroups(limit: 100, filter: {date_geq: $since}, orderBy: [date_ASC]) {
    dimensions { date } sum { requests cachedRequests bytes threats pageViews } uniq { uniques } } } } }`
	if err := c.graphql(ctx, q, map[string]any{"zone": zone, "since": since}, &data); err != nil {
		return nil, err
	}
	out := []DayStats{}
	for _, z := range data.Viewer.Zones {
		for _, g := range z.Groups {
			out = append(out, DayStats{Date: g.Dims.Date, Requests: g.Sum.Requests, Cached: g.Sum.CachedRequests, Bytes: g.Sum.Bytes,
				Threats: g.Sum.Threats, PageViews: g.Sum.PageViews, UniqueIPs: g.Uniq.Uniques})
		}
	}
	return out, nil
}

// raw sends a request and returns the body as is (for GraphQL).
func (c *Cloudflare) raw(ctx context.Context, method, path string, body []byte) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.Base+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HC.Do(req)
	if err != nil {
		return nil, &Error{Code: "dns.unreachable", Detail: err.Error()}
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	switch {
	case resp.StatusCode == 401:
		return nil, &Error{Code: "dns.auth", Detail: "HTTP 401"}
	case resp.StatusCode == 403:
		return nil, &Error{Code: "dns.forbidden", Detail: "HTTP 403"}
	case resp.StatusCode >= 400:
		return nil, &Error{Code: "dns.api", Detail: fmt.Sprintf("HTTP %d", resp.StatusCode)}
	}
	return b, nil
}
