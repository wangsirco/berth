package agent

// Agent self-update on a remote host (远程主机一键升级): the panel asks, the agent fetches the
// panel's own berth binary over the pinned connection, checks its hash and that it runs,
// swaps it in and hands over to a watchdog outside the service. The watchdog restarts the
// agent and waits for it to reconnect with the new version; if it does not, the old binary
// goes back.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/wangsirco/berth/internal/errs"
	"github.com/wangsirco/berth/internal/sign"
	"github.com/wangsirco/berth/internal/version"
)

func connectedMarker() string { return filepath.Join(DataDir, "agent-connected") }

// markConnected notes which version last reached the panel (the watchdog reads it).
func markConnected() {
	_ = os.MkdirAll(DataDir, 0o755)
	_ = os.WriteFile(connectedMarker(), []byte(version.Version), 0o644)
}

// ConnectedSince reports whether version want has connected to the panel after t.
func ConnectedSince(want string, t time.Time) bool {
	fi, err := os.Stat(connectedMarker())
	if err != nil || fi.ModTime().Before(t) {
		return false
	}
	b, _ := os.ReadFile(connectedMarker())
	return strings.TrimSpace(string(b)) == want
}

func registerAgentUpdate(s *Server, rc RemoteConfig) {
	s.Handle("POST /v1/self/agent-update", func(w http.ResponseWriter, r *http.Request, body []byte) {
		var in struct {
			SHA256  string `json:"sha256"`
			Version string `json:"version"`
		}
		json.Unmarshal(body, &in)
		if len(in.SHA256) != 64 || in.Version == "" {
			WriteErr(w, errs.New(400, "params.invalid"))
			return
		}
		res, err := fetchAgentBinary(r.Context(), rc, in.SHA256, in.Version)
		if err != nil {
			WriteErr(w, errs.New(500, "host.agent_update_failed", "detail", err.Error()))
			return
		}
		WriteJSON(w, 200, res)
	})
}

func fetchAgentBinary(ctx context.Context, rc RemoteConfig, sha, want string) (map[string]string, error) {
	bin := filepath.Join(BinDir, "berth")
	newBin := bin + ".agent-new"
	defer os.Remove(newBin)
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", strings.TrimRight(rc.PanelURL, "/")+"/api/v1/agents/binary?arch="+runtime.GOARCH, nil)
	sign.Sign(req, rc.KeyID, rc.Secret, nil)
	hc := &http.Client{Transport: &http.Transport{TLSClientConfig: PinnedTLS(rc.PanelFP)}}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		return nil, fmt.Errorf("panel answered %s: %s", resp.Status, strings.TrimSpace(string(b)))
	}
	f, err := os.OpenFile(newBin, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return nil, err
	}
	h := sha256.New()
	_, err = io.Copy(io.MultiWriter(f, h), io.LimitReader(resp.Body, 300<<20))
	f.Close()
	if err != nil {
		return nil, err
	}
	if hex.EncodeToString(h.Sum(nil)) != strings.ToLower(sha) {
		return nil, fmt.Errorf("checksum mismatch")
	}
	out, err := exec.CommandContext(ctx, newBin, "version").CombinedOutput()
	got, ok := strings.CutPrefix(strings.TrimSpace(string(out)), "berth ")
	if err != nil || !ok || got == "" {
		return nil, fmt.Errorf("new binary does not run here: %s", strings.TrimSpace(string(out)))
	}
	want = got // what it really is; the watchdog waits for this version to reconnect
	rb := filepath.Join(DataDir, "rollback", "agent-"+time.Now().Format("20060102-150405"))
	if err := os.MkdirAll(rb, 0o700); err != nil {
		return nil, err
	}
	if err := copyFile(bin, filepath.Join(rb, "berth"), 0o755); err != nil {
		return nil, fmt.Errorf("backup: %w", err)
	}
	if err := os.Rename(newBin, bin); err != nil { // atomic: the running agent keeps its old inode
		return nil, err
	}
	unit := "berth-agent-update-" + time.Now().Format("150405")
	cmd := exec.Command("systemd-run", "--unit", unit, "--on-active=2", bin, "update-finish", "--agent",
		"--rollback", rb, "--from", version.Version, "--to", want)
	if o, err := cmd.CombinedOutput(); err != nil {
		_ = copyFile(filepath.Join(rb, "berth"), bin, 0o755)
		return nil, fmt.Errorf("watchdog: %s", strings.TrimSpace(string(o)))
	}
	return map[string]string{"from": version.Version, "to": want, "watchdog": unit}, nil
}
