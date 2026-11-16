// Package cli is the `berth` command line: it talks to berth-panel's API with a token
// from `berth login`. Commands for changes are generated from the panel's kind list.
package cli

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/wangsirco/berth/internal/version"
)

type server struct {
	URL   string `json:"url"`
	Token string `json:"token"`
	CAFP  string `json:"ca_fp,omitempty"` // pinned certificate (any in the chain), for the panel's local CA
}

type config struct {
	Servers map[string]server `json:"servers"`
	Current string            `json:"current"`
}

func configPath() string {
	if p := os.Getenv("BERTH_CONFIG"); p != "" {
		return p
	}
	dir, _ := os.UserConfigDir()
	if h, err := os.UserHomeDir(); err == nil && (dir == "" || strings.Contains(dir, "Library/Application Support")) {
		dir = filepath.Join(h, ".config") // same place on macOS and Linux
	}
	return filepath.Join(dir, "berth", "config.json")
}

func loadConfig() (*config, error) {
	c := &config{Servers: map[string]server{}}
	b, err := os.ReadFile(configPath())
	if errors.Is(err, os.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, c); err != nil {
		return nil, fmt.Errorf("%s: %w", configPath(), err)
	}
	if c.Servers == nil {
		c.Servers = map[string]server{}
	}
	return c, nil
}

func (c *config) save() error {
	p := configPath()
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(c, "", "  ")
	return os.WriteFile(p, b, 0o600)
}

func (c *config) current() (server, error) {
	if s, ok := c.Servers[c.Current]; ok {
		return s, nil
	}
	return server{}, errors.New("not signed in; run: berth login https://<panel>:7090")
}

// httpClient trusts the system roots, or, when pinned, any certificate in the chain whose
// SHA-256 matches (the panel's local CA stays the same when its leaf renews).
func httpClient(pin string) *http.Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	if pin != "" {
		tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true, VerifyPeerCertificate: func(raw [][]byte, _ [][]*x509.Certificate) error {
			for _, c := range raw {
				h := sha256.Sum256(c)
				if hex.EncodeToString(h[:]) == pin {
					return nil
				}
			}
			return errors.New("the panel's certificate changed (fingerprint mismatch); run berth login again if this is expected")
		}}
	}
	return &http.Client{Transport: tr, Timeout: 0}
}

type client struct {
	srv  server
	hc   *http.Client
	lang string
}

type apiError struct {
	Status  int
	Code    string
	Message string
}

func (e *apiError) Error() string { return e.Message }

func newClient() (*client, error) {
	cfg, err := loadConfig()
	if err != nil {
		return nil, err
	}
	s, err := cfg.current()
	if err != nil {
		return nil, err
	}
	return &client{srv: s, hc: httpClient(s.CAFP), lang: lang()}, nil
}

func lang() string {
	for _, k := range []string{"BERTH_LANG", "LC_ALL", "LC_MESSAGES", "LANG"} {
		if v := os.Getenv(k); v != "" {
			v = strings.ToLower(v)
			switch {
			case strings.HasPrefix(v, "zh_tw"), strings.HasPrefix(v, "zh_hk"), strings.HasPrefix(v, "zh-tw"), strings.HasPrefix(v, "zh-hant"):
				return "zh-TW"
			case strings.HasPrefix(v, "zh"):
				return "zh-CN"
			case strings.HasPrefix(v, "en"), v == "c", strings.HasPrefix(v, "c."):
				return "en"
			}
		}
	}
	return "en"
}

func (c *client) do(method, path string, in, out any) error {
	var body io.Reader
	ctype := "application/json"
	if raw, ok := in.([]byte); ok { // an archive (berth push)
		body, ctype = bytes.NewReader(raw), "application/gzip"
	} else if in != nil {
		b, _ := json.Marshal(in)
		body = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, strings.TrimRight(c.srv.URL, "/")+path, body)
	req.Header.Set("Authorization", "Bearer "+c.srv.Token)
	req.Header.Set("X-Berth-Via", "cli")
	req.Header.Set("Accept-Language", c.lang)
	req.Header.Set("Content-Type", ctype)
	req.Header.Set("User-Agent", "berth-cli/"+version.Version)
	resp, err := c.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		var e struct {
			Error struct{ Code, Message string } `json:"error"`
		}
		b, _ := io.ReadAll(resp.Body)
		if json.Unmarshal(b, &e) == nil && e.Error.Code != "" {
			return &apiError{Status: resp.StatusCode, Code: e.Error.Code, Message: e.Error.Message}
		}
		return &apiError{Status: resp.StatusCode, Code: "http", Message: fmt.Sprintf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))}
	}
	if out == nil {
		return nil
	}
	if w, ok := out.(io.Writer); ok {
		_, err := io.Copy(w, resp.Body)
		return err
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// stream calls fn for each SSE data line of path until it ends.
func (c *client) stream(path string, fn func(event, data string) bool) error {
	req, _ := http.NewRequest("GET", strings.TrimRight(c.srv.URL, "/")+path, nil)
	req.Header.Set("Authorization", "Bearer "+c.srv.Token)
	req.Header.Set("Accept", "text/event-stream")
	resp, err := c.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	ev := ""
	for sc.Scan() {
		l := sc.Text()
		switch {
		case strings.HasPrefix(l, "event: "):
			ev = l[7:]
		case strings.HasPrefix(l, "data: "):
			if !fn(ev, l[6:]) {
				return nil
			}
		}
	}
	return sc.Err()
}

// Main runs a CLI command and returns the exit code.
func Main(args []string) int {
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		usage(os.Stdout)
		return 0
	}
	var err error
	switch args[0] {
	case "login":
		err = cmdLogin(args[1:])
	case "logout":
		err = cmdLogout()
	case "status":
		err = cmdStatus(args[1:])
	case "ps":
		err = cmdPs(args[1:])
	case "logs":
		err = cmdLogs(args[1:])
	case "changes":
		err = cmdChanges(args[1:])
	case "revert":
		err = cmdRevert(args[1:])
	case "tasks":
		err = cmdTasks(args[1:])
	case "commands":
		err = cmdCommands(args[1:])
	case "uninstall":
		err = cmdUninstall(args[1:])
	case "push":
		err = cmdPush(args[1:])
	case "restore-panel":
		err = cmdRestorePanel(args[1:])
	case "plugin":
		err = cmdPlugin(args[1:])
	case "admin":
		err = cmdAdmin(args[1:])
	default:
		err = runGenerated(args)
	}
	if err != nil {
		var ae *apiError
		if errors.As(err, &ae) {
			fmt.Fprintln(os.Stderr, "berth:", ae.Message)
		} else {
			fmt.Fprintln(os.Stderr, "berth:", err)
		}
		return 1
	}
	return 0
}

func usage(w io.Writer) {
	fmt.Fprint(w, `berth — command line for the Berth server panel

  berth login <https://panel:7090>    sign in (approve in the browser)
  berth logout
  berth status [--json]               host, memory, queue
  berth ps [--json]                   containers
  berth logs <container> [-f] [--tail N]
  berth push [--pipeline NAME] [DIR]  upload the working tree (with uncommitted changes) and run its pipeline
  berth changes [--limit N] [--json]  audit log
  berth revert <change-id> [--yes]    undo a change
  berth tasks [--json]
  berth commands                      every change command you may run
  berth <command> ... [--plan] [--yes] [--json] [--host H]
                                      e.g. berth container restart n8n
  berth uninstall [--purge]           (as root, on the server)
  berth restore-panel [--check] <file> (as root: put a panel snapshot onto this machine; --check only verifies it)
  berth plugin init|check|pack|keygen  make plugins (berth plugin for help)
  berth admin                         (as root, on the server) security entry, username, password

Each change shows a preview first; --plan only previews. Values starting with @ are read
from a file (--compose @compose.yaml).
`)
}
