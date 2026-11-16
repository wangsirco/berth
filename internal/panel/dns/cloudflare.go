package dns

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

// Cloudflare talks to the v4 API with an API token.
type Cloudflare struct {
	Token string
	Base  string // tests point this at a fake
	HC    *http.Client
}

func NewCloudflare(token string) *Cloudflare {
	return &Cloudflare{Token: token, Base: "https://api.cloudflare.com/client/v4", HC: &http.Client{Timeout: 20 * time.Second}}
}

type cfResp struct {
	Success bool            `json:"success"`
	Errors  []cfMsg         `json:"errors"`
	Result  json.RawMessage `json:"result"`
	Info    *struct {
		Page       int `json:"page"`
		TotalPages int `json:"total_pages"`
	} `json:"result_info"`
}

type cfMsg struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (c *Cloudflare) do(ctx context.Context, method, path string, q url.Values, in, out any) (*cfResp, error) {
	var body io.Reader
	if in != nil {
		b, _ := json.Marshal(in)
		body = bytes.NewReader(b)
	}
	u := c.Base + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, u, body)
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
	var r cfResp
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, &Error{Code: "dns.api", Detail: fmt.Sprintf("HTTP %d", resp.StatusCode)}
	}
	if !r.Success || resp.StatusCode >= 400 {
		var msgs []string
		for _, e := range r.Errors {
			msgs = append(msgs, fmt.Sprintf("%d %s", e.Code, e.Message))
		}
		detail := strings.Join(msgs, "; ")
		switch {
		case resp.StatusCode == 401 || hasCode(r.Errors, 6003, 6111, 1000):
			return nil, &Error{Code: "dns.auth", Detail: detail}
		case resp.StatusCode == 403 || hasCode(r.Errors, 9109, 10000):
			return nil, &Error{Code: "dns.forbidden", Detail: detail}
		case resp.StatusCode == 404:
			return nil, &Error{Code: "dns.not_found", Detail: detail}
		case hasCode(r.Errors, 81053, 81057, 81058):
			return nil, &Error{Code: "dns.conflict", Detail: detail}
		}
		return nil, &Error{Code: "dns.api", Detail: detail}
	}
	if out != nil {
		if err := json.Unmarshal(r.Result, out); err != nil {
			return nil, &Error{Code: "dns.api", Detail: "unexpected response"}
		}
	}
	return &r, nil
}

func hasCode(es []cfMsg, codes ...int) bool {
	for _, e := range es {
		for _, c := range codes {
			if e.Code == c {
				return true
			}
		}
	}
	return false
}

func (c *Cloudflare) Verify(ctx context.Context) (Check, error) {
	var v struct {
		Status    string `json:"status"`
		ExpiresOn string `json:"expires_on"`
	}
	if _, err := c.do(ctx, "GET", "/user/tokens/verify", nil, nil, &v); err != nil {
		return Check{}, err
	}
	ch := Check{Active: v.Status == "active", ExpiresAt: v.ExpiresOn}
	if !ch.Active {
		ch.Message = v.Status
		return ch, nil
	}
	zones, err := c.Zones(ctx)
	if err != nil {
		ch.Message = err.Error()
		return ch, nil
	}
	ch.ZonesRead, ch.ZoneCount = true, len(zones)
	if len(zones) > 0 {
		if _, err := c.do(ctx, "GET", "/zones/"+zones[0].ID+"/dns_records", url.Values{"per_page": {"5"}}, nil, nil); err == nil {
			ch.DNSRead = true
		}
	}
	return ch, nil
}

func (c *Cloudflare) Zones(ctx context.Context) ([]Zone, error) {
	var out []Zone
	for page := 1; ; page++ {
		var res []struct {
			ID      string `json:"id"`
			Name    string `json:"name"`
			Status  string `json:"status"`
			Account struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"account"`
		}
		r, err := c.do(ctx, "GET", "/zones", url.Values{"per_page": {"50"}, "page": {fmt.Sprint(page)}}, nil, &res)
		if err != nil {
			return nil, err
		}
		for _, z := range res {
			out = append(out, Zone{ID: z.ID, Name: z.Name, Status: z.Status, Account: z.Account.Name, AccountID: z.Account.ID})
		}
		if r.Info == nil || page >= r.Info.TotalPages || page >= 100 {
			return out, nil
		}
	}
}

func (c *Cloudflare) Records(ctx context.Context, zoneID string) ([]Record, error) {
	out := []Record{}
	for page := 1; ; page++ {
		var res []Record
		r, err := c.do(ctx, "GET", "/zones/"+url.PathEscape(zoneID)+"/dns_records", url.Values{"per_page": {"100"}, "page": {fmt.Sprint(page)}}, nil, &res)
		if err != nil {
			return nil, err
		}
		out = append(out, res...)
		if r.Info == nil || page >= r.Info.TotalPages || page >= 200 {
			return out, nil
		}
	}
}

func cfBody(r Record) map[string]any {
	ttl := r.TTL
	if ttl == 0 {
		ttl = 1
	}
	b := map[string]any{"type": r.Type, "name": r.Name, "content": r.Content, "ttl": ttl}
	switch r.Type {
	case "A", "AAAA", "CNAME":
		b["proxied"] = r.Proxied
	}
	if r.Comment != "" {
		b["comment"] = r.Comment
	}
	return b
}

func (c *Cloudflare) Create(ctx context.Context, zoneID string, r Record) (Record, error) {
	var out Record
	_, err := c.do(ctx, "POST", "/zones/"+url.PathEscape(zoneID)+"/dns_records", nil, cfBody(r), &out)
	return out, err
}

func (c *Cloudflare) Update(ctx context.Context, zoneID string, r Record) (Record, error) {
	var out Record
	_, err := c.do(ctx, "PATCH", "/zones/"+url.PathEscape(zoneID)+"/dns_records/"+url.PathEscape(r.ID), nil, cfBody(r), &out)
	return out, err
}

func (c *Cloudflare) Delete(ctx context.Context, zoneID, recordID string) error {
	_, err := c.do(ctx, "DELETE", "/zones/"+url.PathEscape(zoneID)+"/dns_records/"+url.PathEscape(recordID), nil, nil, nil)
	return err
}
