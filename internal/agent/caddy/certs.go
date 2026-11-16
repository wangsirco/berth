package caddy

import (
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/wangsirco/berth/internal/agent"
	"github.com/wangsirco/berth/internal/errs"
)

// Certificates the panel manages (uploaded, self-signed, local CA) live under
// <Root>/data/berth-certs/<id>/ and are mounted in the container at /data/berth-certs.
func certDir(id string) string { return filepath.Join(Root, "data", "berth-certs", id) }

// CertPaths are the file names inside the container. Each version of a certificate gets
// its own directory: Caddy caches loaded files by path, so a changed file at the same
// path would never be picked up.
func CertPaths(id, version string) (cert, key string) {
	d := "/data/berth-certs/" + id + "/" + version
	return d + "/cert.pem", d + "/key.pem"
}

var (
	certIDRe  = regexp.MustCompile(`^[0-9]{1,12}$`)
	certVerRe = regexp.MustCompile(`^[0-9a-f]{8,64}$`)
)

// ACMECert is one certificate Caddy obtained by itself.
type ACMECert struct {
	Domain      string    `json:"domain"`
	Issuer      string    `json:"issuer"` // directory name, e.g. acme-v02.api.letsencrypt.org-directory
	Names       []string  `json:"names"`
	NotBefore   time.Time `json:"not_before"`
	NotAfter    time.Time `json:"not_after"`
	Fingerprint string    `json:"fingerprint"`
}

func acmeCerts() []ACMECert {
	out := []ACMECert{}
	base := filepath.Join(Root, "data", "caddy", "certificates")
	files, _ := filepath.Glob(filepath.Join(base, "*", "*", "*.crt"))
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		blk, _ := pem.Decode(b)
		if blk == nil {
			continue
		}
		c, err := x509.ParseCertificate(blk.Bytes)
		if err != nil {
			continue
		}
		fp := sha256.Sum256(c.Raw)
		rel, _ := filepath.Rel(base, f)
		parts := strings.Split(rel, string(filepath.Separator))
		out = append(out, ACMECert{Domain: parts[1], Issuer: parts[0], Names: c.DNSNames, NotBefore: c.NotBefore, NotAfter: c.NotAfter,
			Fingerprint: hex.EncodeToString(fp[:])})
	}
	return out
}

func registerCerts(s *agent.Server) {
	s.Handle("GET /v1/edge/acme-certs", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		agent.WriteJSON(w, 200, acmeCerts())
	})
	s.Handle("PUT /v1/edge/certs/{id}", func(w http.ResponseWriter, r *http.Request, body []byte) {
		id := r.PathValue("id")
		var in struct {
			Version string `json:"version"`
			Cert    string `json:"cert"`
			Key     string `json:"key"`
		}
		if !certIDRe.MatchString(id) || json.Unmarshal(body, &in) != nil || !certVerRe.MatchString(in.Version) || in.Cert == "" || in.Key == "" {
			agent.WriteErr(w, errs.New(400, "params.invalid_json"))
			return
		}
		// keep only the version being written (older ones are already in Caddy's memory)
		if old, _ := os.ReadDir(certDir(id)); old != nil {
			for _, e := range old {
				if e.Name() != in.Version {
					os.RemoveAll(filepath.Join(certDir(id), e.Name()))
				}
			}
		}
		dir := filepath.Join(certDir(id), in.Version)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			agent.WriteErr(w, err)
			return
		}
		for name, data := range map[string]string{"cert.pem": in.Cert, "key.pem": in.Key} {
			tmp := filepath.Join(dir, "."+name)
			if err := os.WriteFile(tmp, []byte(data), 0o600); err != nil {
				agent.WriteErr(w, err)
				return
			}
			if err := os.Rename(tmp, filepath.Join(dir, name)); err != nil {
				agent.WriteErr(w, err)
				return
			}
		}
		agent.WriteJSON(w, 200, map[string]bool{"ok": true})
	})
	s.Handle("DELETE /v1/edge/certs/{id}", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		id := r.PathValue("id")
		if !certIDRe.MatchString(id) {
			agent.WriteErr(w, errs.New(400, "params.invalid_json"))
			return
		}
		agent.WriteJSON(w, 200, map[string]bool{"ok": os.RemoveAll(certDir(id)) == nil})
	})
}
