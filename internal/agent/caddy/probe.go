package caddy

// Probes for site diagnosis, run on the site's host: DNS through chosen resolvers, TCP
// and HTTPS to the edge (with the certificate it serves), and HTTP to an upstream from
// inside the Caddy container's network.

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/wangsirco/berth/internal/agent"
	"github.com/wangsirco/berth/internal/agent/docker"
	"github.com/wangsirco/berth/internal/errs"
)

type DNSResult struct {
	Resolver string   `json:"resolver"`
	IPs      []string `json:"ips"`
	Error    string   `json:"error,omitempty"`
}

func resolve(ctx context.Context, name, server string) DNSResult {
	r := &net.Resolver{}
	if server != "system" {
		r = &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, network, net.JoinHostPort(server, "53"))
		}}
	}
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	ips, err := r.LookupIPAddr(ctx, name)
	out := DNSResult{Resolver: server, IPs: []string{}}
	if err != nil {
		out.Error = err.Error()
		if de, ok := err.(*net.DNSError); ok && de.IsNotFound {
			out.Error = "NXDOMAIN"
		}
	}
	for _, ip := range ips {
		out.IPs = append(out.IPs, ip.IP.String())
	}
	return out
}

type CertInfo struct {
	Names       []string  `json:"names"`
	Issuer      string    `json:"issuer"`
	NotAfter    time.Time `json:"not_after"`
	ChainLen    int       `json:"chain_len"`
	Verified    bool      `json:"verified"`
	VerifyError string    `json:"verify_error,omitempty"`
}

type HTTPResult struct {
	Status int       `json:"status"`
	MS     int64     `json:"ms"`
	Error  string    `json:"error,omitempty"`
	Cert   *CertInfo `json:"cert,omitempty"`
	Server string    `json:"server,omitempty"`
}

// probeHTTPS asks the local edge for https://host/ (SNI host) and checks its certificate
// against the system roots plus extraCA.
func probeHTTPS(ctx context.Context, host string, port int, extraCA string) HTTPResult {
	var res HTTPResult
	var peer []*x509.Certificate
	tr := &http.Transport{
		TLSClientConfig: &tls.Config{ServerName: host, InsecureSkipVerify: true, // verified below, to report why
			VerifyConnection: func(cs tls.ConnectionState) error { peer = cs.PeerCertificates; return nil }},
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
		},
	}
	hc := &http.Client{Transport: tr, Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, _ := http.NewRequestWithContext(ctx, "GET", "https://"+host+"/", nil)
	start := time.Now()
	resp, err := hc.Do(req)
	res.MS = time.Since(start).Milliseconds()
	if err != nil {
		res.Error = err.Error()
	} else {
		resp.Body.Close()
		res.Status, res.Server = resp.StatusCode, resp.Header.Get("Server")
	}
	if len(peer) > 0 {
		leaf := peer[0]
		ci := &CertInfo{Names: leaf.DNSNames, Issuer: leaf.Issuer.CommonName, NotAfter: leaf.NotAfter, ChainLen: len(peer)}
		roots, _ := x509.SystemCertPool()
		if roots == nil {
			roots = x509.NewCertPool()
		}
		if extraCA != "" {
			roots.AppendCertsFromPEM([]byte(extraCA))
		}
		inter := x509.NewCertPool()
		for _, c := range peer[1:] {
			inter.AddCert(c)
		}
		if _, err := leaf.Verify(x509.VerifyOptions{DNSName: host, Roots: roots, Intermediates: inter}); err != nil {
			ci.VerifyError = err.Error()
		} else {
			ci.Verified = true
		}
		res.Cert = ci
	}
	return res
}

var wgetStatus = regexp.MustCompile(`HTTP/[0-9.]+ ([0-9]{3})`)

// probeFromEdge fetches url from inside the Caddy container (same networks as Caddy).
func probeFromEdge(ctx context.Context, dc *docker.Client, url string) HTTPResult {
	start := time.Now()
	out, code, err := dc.Exec(ctx, Container, []string{"wget", "-q", "-S", "-T", "4", "-O", "/dev/null", url})
	res := HTTPResult{MS: time.Since(start).Milliseconds()}
	if err != nil {
		res.Error = err.Error()
		return res
	}
	if m := wgetStatus.FindAllStringSubmatch(out, -1); len(m) > 0 {
		res.Status, _ = strconv.Atoi(m[len(m)-1][1])
	}
	if res.Status == 0 && code != 0 {
		res.Error = lastLines(out, 2)
	}
	return res
}

// edgeErrors: recent error lines from Caddy's own log mentioning domain.
func edgeErrors(ctx context.Context, dc *docker.Client, domain string, tail int) []string {
	out := []string{}
	_ = dc.Logs(ctx, Container, tail, false, "", func(l docker.LogLine) {
		if !strings.Contains(l.Line, domain) {
			return
		}
		var m map[string]any
		if json.Unmarshal([]byte(l.Line), &m) == nil {
			if lv, _ := m["level"].(string); lv != "error" && lv != "warn" {
				return
			}
		}
		out = append(out, l.Line)
	})
	if len(out) > 20 {
		out = out[len(out)-20:]
	}
	return out
}

func registerProbes(s *agent.Server, a *Admin) {
	s.Handle("POST /v1/probe/dns", func(w http.ResponseWriter, r *http.Request, body []byte) {
		var in struct {
			Name      string   `json:"name"`
			Resolvers []string `json:"resolvers"`
		}
		json.Unmarshal(body, &in)
		if in.Name == "" {
			agent.WriteErr(w, errs.New(400, "params.invalid_json"))
			return
		}
		if len(in.Resolvers) == 0 {
			in.Resolvers = []string{"1.1.1.1", "8.8.8.8", "system"}
		}
		out := make([]DNSResult, len(in.Resolvers))
		done := make(chan struct{}, len(in.Resolvers))
		for i, rs := range in.Resolvers {
			if rs != "system" && net.ParseIP(rs) == nil {
				out[i] = DNSResult{Resolver: rs, IPs: []string{}, Error: "bad resolver"}
				done <- struct{}{}
				continue
			}
			go func(i int, rs string) { out[i] = resolve(r.Context(), in.Name, rs); done <- struct{}{} }(i, rs)
		}
		for range in.Resolvers {
			<-done
		}
		agent.WriteJSON(w, 200, out)
	})
	s.Handle("POST /v1/probe/tcp", func(w http.ResponseWriter, r *http.Request, body []byte) {
		var in struct {
			Addr string `json:"addr"`
		}
		json.Unmarshal(body, &in)
		start := time.Now()
		c, err := net.DialTimeout("tcp", in.Addr, 3*time.Second)
		res := map[string]any{"ok": err == nil, "ms": time.Since(start).Milliseconds()}
		if err != nil {
			res["error"] = err.Error()
		} else {
			c.Close()
		}
		agent.WriteJSON(w, 200, res)
	})
	s.Handle("POST /v1/probe/https", func(w http.ResponseWriter, r *http.Request, body []byte) {
		var in struct {
			Host  string `json:"host"`
			Port  int    `json:"port"`
			CAPEM string `json:"ca_pem"`
		}
		json.Unmarshal(body, &in)
		if in.Port == 0 {
			in.Port = 443
		}
		if blk, _ := pem.Decode([]byte(in.CAPEM)); in.CAPEM != "" && blk == nil {
			in.CAPEM = ""
		}
		agent.WriteJSON(w, 200, probeHTTPS(r.Context(), in.Host, in.Port, in.CAPEM))
	})
	s.Handle("POST /v1/probe/edge-http", func(w http.ResponseWriter, r *http.Request, body []byte) {
		var in struct {
			URL string `json:"url"`
		}
		json.Unmarshal(body, &in)
		if !strings.HasPrefix(in.URL, "http://") && !strings.HasPrefix(in.URL, "https://") {
			agent.WriteErr(w, errs.New(400, "params.invalid_json"))
			return
		}
		agent.WriteJSON(w, 200, probeFromEdge(r.Context(), a.dc, in.URL))
	})
	s.Handle("GET /v1/edge/errors", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		agent.WriteJSON(w, 200, edgeErrors(r.Context(), a.dc, r.URL.Query().Get("domain"), 500))
	})
	s.Handle("GET /v1/edge/sitedir", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		d := r.URL.Query().Get("domain")
		if strings.ContainsAny(d, "/\\") || d == "" || strings.HasPrefix(d, ".") {
			agent.WriteErr(w, errs.New(400, "params.invalid_json"))
			return
		}
		dir := filepath.Join(Root, "sites", d)
		_, err := os.Stat(dir)
		_, ierr := os.Stat(filepath.Join(dir, "index.html"))
		agent.WriteJSON(w, 200, map[string]any{"dir": dir, "exists": err == nil, "index": ierr == nil})
	})
}
