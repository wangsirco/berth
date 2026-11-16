package caddy

// Tamper protection (防篡改) for a site's files: everything under the site directory is
// made immutable (chattr +i) — not even root or the PHP runtime can change it — except the
// directories you list as writable (uploads, cache). Turning it off clears the flag.

import (
	"context"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"encoding/json"

	"github.com/wangsirco/berth/internal/agent"
	"github.com/wangsirco/berth/internal/errs"
)

func siteDir(domain string) (string, error) {
	if domain == "" || strings.ContainsAny(domain, "/\\") || strings.HasPrefix(domain, ".") {
		return "", errors.New("bad domain")
	}
	return filepath.Join(Root, "sites", domain), nil
}

func chattr(ctx context.Context, args ...string) error {
	out, err := exec.CommandContext(ctx, "chattr", args...).CombinedOutput()
	if err != nil {
		return errors.New(strings.TrimSpace(string(out)))
	}
	return nil
}

func tamperOn(dir string) bool {
	out, err := exec.Command("lsattr", "-d", dir).Output()
	if err != nil {
		return false
	}
	f := strings.Fields(string(out))
	return len(f) > 0 && strings.Contains(f[0], "i")
}

func SetTamper(ctx context.Context, domain string, on bool, writable []string) error {
	dir, err := siteDir(domain)
	if err != nil {
		return err
	}
	if _, err := os.Stat(dir); err != nil {
		return errors.New("the site has no directory yet")
	}
	// always start from a clean state
	if err := chattr(ctx, "-R", "-i", dir); err != nil {
		return err
	}
	if !on {
		return nil
	}
	if err := chattr(ctx, "-R", "+i", dir); err != nil {
		chattr(ctx, "-R", "-i", dir)
		return err
	}
	for _, w := range writable {
		w = filepath.Clean("/" + w)
		p := filepath.Join(dir, w)
		if !strings.HasPrefix(p, dir+"/") {
			continue
		}
		if _, err := os.Stat(p); err == nil {
			chattr(ctx, "-R", "-i", p)
		}
	}
	return nil
}

func registerTamper(s *agent.Server) {
	s.Handle("GET /v1/edge/tamper", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		dir, err := siteDir(r.URL.Query().Get("domain"))
		if err != nil {
			agent.WriteErr(w, errs.New(400, "params.invalid_json"))
			return
		}
		agent.WriteJSON(w, 200, map[string]bool{"on": tamperOn(dir)})
	})
	s.Handle("POST /v1/edge/tamper", func(w http.ResponseWriter, r *http.Request, body []byte) {
		var in struct {
			Domain   string   `json:"domain"`
			On       bool     `json:"on"`
			Writable []string `json:"writable"`
		}
		json.Unmarshal(body, &in)
		if err := SetTamper(r.Context(), in.Domain, in.On, in.Writable); err != nil {
			agent.WriteErr(w, errs.New(400, "site.tamper_failed", "detail", err.Error()))
			return
		}
		agent.WriteJSON(w, 200, map[string]bool{"ok": true})
	})
}
