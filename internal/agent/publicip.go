package agent

import (
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/wangsirco/berth/internal/errs"
)

// Public IP detection for DDNS and diagnosis: ask several services at once and take
// the answer most of them agree on, so one wrong or hijacked source cannot move a record.
var PublicIPSources = map[string][]string{
	"4": {"https://api.ipify.org", "https://ipv4.icanhazip.com", "https://v4.ident.me", "https://ipinfo.io/ip"},
	"6": {"https://api6.ipify.org", "https://ipv6.icanhazip.com", "https://v6.ident.me"},
}

func fetchIP(ctx context.Context, hc *http.Client, url, family string) string {
	req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
	req.Header.Set("User-Agent", "berth")
	resp, err := hc.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 128))
	ip := net.ParseIP(strings.TrimSpace(string(b)))
	if ip == nil || (family == "4") != (ip.To4() != nil) {
		return ""
	}
	return ip.String()
}

// PublicIP returns the address most sources report, with how many agreed.
func PublicIP(ctx context.Context, family string, sources []string) (string, int, int, error) {
	return PublicIPVia(ctx, family, sources, "")
}

// PublicIPVia asks through one network card only (iface "" = the normal route): the
// address the internet sees for traffic leaving through it.
func PublicIPVia(ctx context.Context, family string, sources []string, iface string) (string, int, int, error) {
	network := "tcp4"
	if family == "6" {
		network = "tcp6"
	}
	hc := &http.Client{Timeout: 8 * time.Second, Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, addr string) (net.Conn, error) {
			d := net.Dialer{Control: bindControl(iface)}
			return d.DialContext(ctx, network, addr)
		}}}
	var mu sync.Mutex
	votes := map[string]int{}
	var wg sync.WaitGroup
	for _, u := range sources {
		wg.Add(1)
		go func(u string) {
			defer wg.Done()
			if ip := fetchIP(ctx, hc, u, family); ip != "" {
				mu.Lock()
				votes[ip]++
				mu.Unlock()
			}
		}(u)
	}
	wg.Wait()
	best, n := "", 0
	for ip, c := range votes {
		if c > n || (c == n && ip < best) {
			best, n = ip, c
		}
	}
	if n == 0 {
		return "", 0, len(sources), errs.New(502, "net.no_public_ip", "family", "IPv"+family)
	}
	if n*2 <= len(sources) && len(votes) > 1 { // no majority among the answers
		return "", n, len(sources), errs.New(502, "net.public_ip_disagree", "family", "IPv"+family)
	}
	return best, n, len(sources), nil
}

func registerPublicIP(s *Server) {
	s.Handle("GET /v1/net/public-ip", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		fam := r.URL.Query().Get("family")
		if fam != "6" {
			fam = "4"
		}
		ip, agree, total, err := PublicIPVia(r.Context(), fam, PublicIPSources[fam], r.URL.Query().Get("iface"))
		if err != nil {
			WriteErr(w, err)
			return
		}
		WriteJSON(w, 200, map[string]any{"ip": ip, "agree": agree, "sources": total})
	})
	s.Handle("GET /v1/net/lan-ip", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		WriteJSON(w, 200, map[string]string{"ip": LANIP()})
	})
}

// LANIP: the IPv4 address of the interface the default route uses (what other machines on
// the network reach this one at; NAT proxies cannot listen on 0.0.0.0).
func LANIP() string {
	b, err := os.ReadFile("/proc/net/route")
	if err != nil {
		return "0.0.0.0"
	}
	for _, line := range strings.Split(string(b), "\n")[1:] {
		f := strings.Fields(line)
		if len(f) > 2 && f[1] == "00000000" { // default route
			if ifc, err := net.InterfaceByName(f[0]); err == nil {
				addrs, _ := ifc.Addrs()
				for _, a := range addrs {
					if ip, _, err := net.ParseCIDR(a.String()); err == nil && ip.To4() != nil {
						return ip.String()
					}
				}
			}
		}
	}
	return "0.0.0.0"
}
