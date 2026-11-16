package dns

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Updater is a DDNS-only service: it can point one name at an address and nothing else
// (no zones, no other records, no certificates).
type Updater interface {
	SetAddress(ctx context.Context, name, ip string) error
}

var ddnsHTTP = &http.Client{Timeout: 20 * time.Second}

func get(ctx context.Context, u, user, pass string) (int, string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return 0, "", err
	}
	req.Header.Set("User-Agent", "Berth-DDNS/1.0")
	if user != "" {
		req.SetBasicAuth(user, pass)
	}
	resp, err := ddnsHTTP.Do(req)
	if err != nil {
		return 0, "", &Error{Code: "dns.api", Detail: err.Error()}
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	return resp.StatusCode, strings.TrimSpace(string(b)), nil
}

// DuckDNS: name.duckdns.org with an account token.
type DuckDNS struct {
	Token string
	Base  string // https://www.duckdns.org
}

func (d DuckDNS) SetAddress(ctx context.Context, name, ip string) error {
	sub := strings.TrimSuffix(name, ".duckdns.org")
	q := url.Values{"domains": {sub}, "token": {d.Token}}
	if net.ParseIP(ip).To4() != nil {
		q.Set("ip", ip)
	} else {
		q.Set("ipv6", ip)
	}
	base := d.Base
	if base == "" {
		base = "https://www.duckdns.org"
	}
	code, body, err := get(ctx, base+"/update?"+q.Encode(), "", "")
	if err != nil {
		return err
	}
	if code != 200 || !strings.HasPrefix(body, "OK") {
		return &Error{Code: "dns.auth", Detail: "DuckDNS: " + body + " (check the token and that the name is yours)"}
	}
	return nil
}

// DynDNS2 is the protocol most DDNS services speak (No-IP, Dynu, Hurricane Electric,
// Oray-compatible routers, …): GET /nic/update?hostname=&myip= with basic auth.
type DynDNS2 struct {
	Server, User, Pass string // server: host or https://host
}

func (d DynDNS2) SetAddress(ctx context.Context, name, ip string) error {
	base := d.Server
	if !strings.Contains(base, "://") {
		base = "https://" + base
	}
	u := strings.TrimRight(base, "/") + "/nic/update?" + url.Values{"hostname": {name}, "myip": {ip}}.Encode()
	code, body, err := get(ctx, u, d.User, d.Pass)
	if err != nil {
		return err
	}
	word := strings.Fields(body + " ")[0]
	switch word {
	case "good", "nochg":
		return nil
	case "badauth", "!donator", "notfqdn", "nohost", "numhost", "abuse", "badagent", "dnserr", "911":
		code := map[string]string{"badauth": "dns.auth", "nohost": "dns.not_found", "notfqdn": "dns.not_found", "abuse": "dns.forbidden", "!donator": "dns.forbidden"}[word]
		if code == "" {
			code = "dns.api"
		}
		return &Error{Code: code, Detail: body}
	}
	if code >= 300 {
		return &Error{Code: "dns.api", Detail: fmt.Sprintf("HTTP %d: %s", code, body)}
	}
	return nil
}

// CustomURL calls any URL with {ip} and {domain} filled in; success is a 2xx answer.
type CustomURL struct {
	URL string
}

func (c CustomURL) SetAddress(ctx context.Context, name, ip string) error {
	u := strings.NewReplacer("{ip}", url.QueryEscape(ip), "{domain}", url.QueryEscape(name), "{name}", url.QueryEscape(name)).Replace(c.URL)
	if !strings.HasPrefix(u, "https://") && !strings.HasPrefix(u, "http://") {
		return &Error{Code: "dns.api", Detail: "the URL must start with https://"}
	}
	code, body, err := get(ctx, u, "", "")
	if err != nil {
		return err
	}
	if code < 200 || code >= 300 {
		return &Error{Code: "dns.api", Detail: fmt.Sprintf("HTTP %d: %s", code, body)}
	}
	return nil
}
