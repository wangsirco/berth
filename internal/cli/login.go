package cli

import (
	"bufio"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// probeChain connects once without verification and returns the chain's fingerprints
// (leaf first) and whether the system already trusts it.
func probeChain(u *url.URL) (fps []string, trusted bool, err error) {
	host := u.Host
	if u.Port() == "" {
		host = net.JoinHostPort(u.Hostname(), "443")
	}
	conn, err := tls.DialWithDialer(&net.Dialer{Timeout: 10 * time.Second}, "tcp", host, &tls.Config{InsecureSkipVerify: true, ServerName: u.Hostname()})
	if err != nil {
		return nil, false, err
	}
	defer conn.Close()
	for _, c := range conn.ConnectionState().PeerCertificates {
		h := sha256.Sum256(c.Raw)
		fps = append(fps, hex.EncodeToString(h[:]))
	}
	if t, err := tls.Dial("tcp", host, &tls.Config{ServerName: u.Hostname()}); err == nil {
		t.Close()
		trusted = true
	}
	return fps, trusted, nil
}

func ask(prompt string) string {
	fmt.Fprint(os.Stderr, prompt)
	l, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	return strings.TrimSpace(l)
}

func openBrowser(u string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", u)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", u)
	default:
		cmd = exec.Command("xdg-open", u)
	}
	_ = cmd.Start()
}

func cmdLogin(args []string) error {
	if len(args) < 1 {
		return errors.New("usage: berth login https://<panel>:7090")
	}
	raw := args[0]
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return errors.New("the panel address must be https://host[:port]")
	}
	base := u.Scheme + "://" + u.Host
	srv := server{URL: base}
	fps, trusted, err := probeChain(u)
	if err != nil {
		return fmt.Errorf("cannot reach %s: %w", base, err)
	}
	if !trusted {
		pin := fps[len(fps)-1] // the last certificate in the chain is the panel's local CA
		fmt.Fprintf(os.Stderr, "The panel uses its own certificate. Fingerprint (SHA-256):\n  %s\n", pin)
		fmt.Fprintln(os.Stderr, "Compare it with the CA fingerprint shown in the panel (Settings → Access) or in `journalctl -u berth-panel`.")
		if a := strings.ToLower(ask("Trust it? [y/N] ")); a != "y" && a != "yes" {
			return errors.New("not trusted; nothing saved")
		}
		srv.CAFP = pin
	}
	hc := httpClient(srv.CAFP)
	host, _ := os.Hostname()
	var auth struct {
		DeviceCode string `json:"device_code"`
		UserCode   string `json:"user_code"`
		VerifyPath string `json:"verify_path"`
		ExpiresIn  int    `json:"expires_in"`
		Interval   int    `json:"interval"`
	}
	if err := postJSON(hc, base+"/api/v1/cli/authorize", map[string]string{"device": host}, &auth); err != nil {
		return err
	}
	verify := base + auth.VerifyPath
	fmt.Fprintf(os.Stderr, "Open %s and approve code %s\n", verify, auth.UserCode)
	openBrowser(verify)
	deadline := time.Now().Add(time.Duration(auth.ExpiresIn) * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(time.Duration(max(auth.Interval, 1)) * time.Second)
		var tok struct {
			Token string `json:"token"`
		}
		status, err := postStatus(hc, base+"/api/v1/cli/token", map[string]string{"device_code": auth.DeviceCode}, &tok)
		if err != nil {
			return err
		}
		if status == 202 {
			continue
		}
		srv.Token = tok.Token
		cfg, err := loadConfig()
		if err != nil {
			return err
		}
		name := u.Hostname()
		cfg.Servers[name], cfg.Current = srv, name
		if err := cfg.save(); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "Signed in to %s (saved in %s)\n", base, configPath())
		return nil
	}
	return errors.New("timed out waiting for approval")
}

func postJSON(hc *http.Client, u string, in, out any) error {
	status, err := postStatus(hc, u, in, out)
	if err == nil && status >= 300 {
		err = fmt.Errorf("HTTP %d", status)
	}
	return err
}

func postStatus(hc *http.Client, u string, in, out any) (int, error) {
	b, _ := json.Marshal(in)
	resp, err := hc.Post(u, "application/json", strings.NewReader(string(b)))
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		var e struct {
			Error struct{ Message string } `json:"error"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&e)
		return resp.StatusCode, &apiError{Status: resp.StatusCode, Message: e.Error.Message}
	}
	return resp.StatusCode, json.NewDecoder(resp.Body).Decode(out)
}

func cmdLogout() error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	delete(cfg.Servers, cfg.Current)
	cfg.Current = ""
	return cfg.save()
}
