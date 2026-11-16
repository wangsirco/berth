package agent

// The internal image registry (berth-registry stack) and Docker's trust in it.

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/wangsirco/berth/internal/errs"
	"github.com/wangsirco/berth/internal/tlsutil"
)

var (
	RegistryRoot   = "/srv/berth/registry"
	DockerCertsDir = "/etc/docker/certs.d"
	DockerConfig   = "/root/.docker/config.json"
	registryAddrRe = regexp.MustCompile(`^[a-zA-Z0-9.-]+(:[0-9]{1,5})?$`)
)

func writeFile(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, mode); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// trustRegistry makes this host's Docker trust the registry's CA and log in to it.
func trustRegistry(addr, caPEM, user, pass string) error {
	if !registryAddrRe.MatchString(addr) {
		return errs.New(400, "params.invalid_json")
	}
	if err := writeFile(filepath.Join(DockerCertsDir, addr, "ca.crt"), []byte(caPEM), 0o644); err != nil {
		return err
	}
	cfg := map[string]any{}
	if b, err := os.ReadFile(DockerConfig); err == nil {
		json.Unmarshal(b, &cfg)
	}
	auths, _ := cfg["auths"].(map[string]any)
	if auths == nil {
		auths = map[string]any{}
	}
	auths[addr] = map[string]string{"auth": base64.StdEncoding.EncodeToString([]byte(user + ":" + pass))}
	cfg["auths"] = auths
	b, _ := json.MarshalIndent(cfg, "", "\t")
	return writeFile(DockerConfig, b, 0o600)
}

func registerRegistry(s *Server) {
	s.Handle("GET /v1/net/addrs", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		WriteJSON(w, 200, tlsutil.LocalAddrs())
	})
	s.Handle("PUT /v1/registry/files", func(w http.ResponseWriter, r *http.Request, body []byte) {
		var in struct{ Cert, Key, Htpasswd string }
		if json.Unmarshal(body, &in) != nil || in.Cert == "" || in.Key == "" || in.Htpasswd == "" {
			WriteErr(w, errs.New(400, "params.invalid_json"))
			return
		}
		for p, d := range map[string]string{"certs/cert.pem": in.Cert, "certs/key.pem": in.Key, "auth/htpasswd": in.Htpasswd} {
			if err := writeFile(filepath.Join(RegistryRoot, p), []byte(d), 0o600); err != nil {
				WriteErr(w, err)
				return
			}
		}
		os.MkdirAll(filepath.Join(RegistryRoot, "data"), 0o755)
		WriteJSON(w, 200, map[string]bool{"ok": true})
	})
	s.Handle("POST /v1/docker/trust-registry", func(w http.ResponseWriter, r *http.Request, body []byte) {
		var in struct{ Addr, CA, User, Pass string }
		json.Unmarshal(body, &in)
		if err := trustRegistry(in.Addr, in.CA, in.User, in.Pass); err != nil {
			WriteErr(w, err)
			return
		}
		WriteJSON(w, 200, map[string]bool{"ok": true})
	})
	// a registry API call from this host (the registry may only be reachable here)
	s.Handle("POST /v1/registry/request", func(w http.ResponseWriter, r *http.Request, body []byte) {
		var in struct {
			Method, Path, Addr, CA, User, Pass string
			Accept                             []string
		}
		json.Unmarshal(body, &in)
		if !strings.HasPrefix(in.Path, "/v2/") || !registryAddrRe.MatchString(in.Addr) || (in.Method != "GET" && in.Method != "HEAD" && in.Method != "DELETE") {
			WriteErr(w, errs.New(400, "params.invalid_json"))
			return
		}
		pool := x509.NewCertPool()
		pool.AppendCertsFromPEM([]byte(in.CA))
		host, _, _ := net.SplitHostPort(in.Addr)
		hc := &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, ServerName: host}}}
		req, _ := http.NewRequestWithContext(r.Context(), in.Method, "https://"+in.Addr+in.Path, nil)
		req.SetBasicAuth(in.User, in.Pass)
		for _, a := range in.Accept {
			req.Header.Add("Accept", a)
		}
		resp, err := hc.Do(req)
		if err != nil {
			WriteErr(w, errs.New(502, "registry.unreachable", "detail", err.Error()))
			return
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		WriteJSON(w, 200, map[string]any{"status": resp.StatusCode, "digest": resp.Header.Get("Docker-Content-Digest"),
			"body": json.RawMessage(validJSON(b))})
	})
}

func validJSON(b []byte) []byte {
	if json.Valid(b) && len(bytes.TrimSpace(b)) > 0 {
		return b
	}
	q, _ := json.Marshal(string(b))
	return q
}
