package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"runtime"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/hashicorp/yamux"

	"github.com/wangsirco/berth/internal/sign"
	"github.com/wangsirco/berth/internal/version"
)

// Remote mode (方案 §4.3): an agent on another machine dials the panel, which may be
// behind NAT relative to it, and keeps a WebSocket open. A yamux session runs over it;
// the panel opens one stream per request, and the agent serves its usual signed HTTP
// routes on those streams.

type RemoteConfig struct {
	PanelURL string // https://panel:7090
	PanelFP  string // sha256 of any certificate in the panel's chain (its local CA)
	KeyID    string
	Secret   []byte
	Name     string
}

// PinnedTLS trusts the system roots, or any certificate in the chain matching fp.
func PinnedTLS(fp string) *tls.Config {
	if fp == "" {
		return &tls.Config{MinVersion: tls.VersionTLS12}
	}
	want := strings.ToLower(strings.ReplaceAll(fp, ":", ""))
	return &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: true, // replaced by the pin below
		VerifyPeerCertificate: func(raw [][]byte, _ [][]*x509.Certificate) error {
			for _, c := range raw {
				h := sha256.Sum256(c)
				if hex.EncodeToString(h[:]) == want {
					return nil
				}
			}
			return errors.New("panel certificate does not match the pinned fingerprint")
		}}
}

func yamuxConfig() *yamux.Config {
	c := yamux.DefaultConfig()
	c.EnableKeepAlive = true
	c.KeepAliveInterval = 20 * time.Second
	c.ConnectionWriteTimeout = 30 * time.Second
	c.LogOutput = io.Discard
	return c
}

// YamuxConfig is shared with the panel side.
func YamuxConfig() *yamux.Config { return yamuxConfig() }

func connectOnce(ctx context.Context, rc RemoteConfig, s *Server) error {
	u := strings.TrimRight(rc.PanelURL, "/") + "/api/v1/agents/connect"
	u = "wss" + strings.TrimPrefix(u, "https")
	req, _ := http.NewRequest("GET", u, nil)
	sign.Sign(req, rc.KeyID, rc.Secret, nil)
	req.Header.Set("X-Berth-Agent-Name", rc.Name)
	req.Header.Set("X-Berth-Agent-Version", version.Version)
	req.Header.Set("X-Berth-Agent-Arch", runtime.GOARCH)
	hc := &http.Client{Transport: &http.Transport{TLSClientConfig: PinnedTLS(rc.PanelFP)}} // no timeout: the connection is long-lived
	dctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	conn, _, err := websocket.Dial(dctx, u, &websocket.DialOptions{HTTPClient: hc, HTTPHeader: req.Header})
	cancel()
	if err != nil {
		return err
	}
	conn.SetReadLimit(-1)
	nc := websocket.NetConn(ctx, conn, websocket.MessageBinary)
	sess, err := yamux.Server(nc, yamuxConfig())
	if err != nil {
		nc.Close()
		return err
	}
	defer sess.Close()
	markConnected()
	return s.ServeListener(sess)
}

// RunRemote keeps the connection to the panel up, reconnecting with backoff (1 s → 60 s).
func RunRemote(ctx context.Context, rc RemoteConfig, s *Server, logf func(string, ...any)) {
	registerAgentUpdate(s, rc)
	backoff := time.Second
	for ctx.Err() == nil {
		start := time.Now()
		err := connectOnce(ctx, rc, s)
		if time.Since(start) > time.Minute {
			backoff = time.Second // it was up for a while: retry quickly
		}
		logf("berth agent: connection to %s ended (%v); retrying in %s", rc.PanelURL, err, backoff)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, time.Minute)
	}
}

func pinnedClient(fp string) *http.Client {
	return &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{TLSClientConfig: PinnedTLS(fp)}}
}

// CheckPanel makes sure the panel answers with the pinned certificate.
func CheckPanel(ctx context.Context, rc RemoteConfig) error {
	req, _ := http.NewRequestWithContext(ctx, "GET", strings.TrimRight(rc.PanelURL, "/")+"/api/v1/ca.crt", nil)
	resp, err := pinnedClient(rc.PanelFP).Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

// Enroll trades a one-time join code for this host's key.
func Enroll(ctx context.Context, panelURL, fp, code, name string) (keyID string, secret []byte, err error) {
	body, _ := json.Marshal(map[string]string{"code": code, "name": name, "os": runtime.GOOS, "arch": runtime.GOARCH, "version": version.Version})
	req, _ := http.NewRequestWithContext(ctx, "POST", strings.TrimRight(panelURL, "/")+"/api/v1/agents/enroll", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := pinnedClient(fp).Do(req)
	if err != nil {
		return "", nil, err
	}
	defer resp.Body.Close()
	var out struct {
		KeyID  string `json:"key_id"`
		Secret string `json:"secret"`
		Error  *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out); err != nil {
		return "", nil, fmt.Errorf("enroll: HTTP %d", resp.StatusCode)
	}
	if resp.StatusCode != 200 || out.KeyID == "" {
		msg := fmt.Sprintf("HTTP %d", resp.StatusCode)
		if out.Error != nil {
			msg = out.Error.Message
		}
		return "", nil, fmt.Errorf("enroll: %s", msg)
	}
	secret, err = base64.StdEncoding.DecodeString(out.Secret)
	return out.KeyID, secret, err
}
