package agentproto

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/coder/websocket"
	"github.com/wangsirco/berth/internal/errs"
	"github.com/wangsirco/berth/internal/sign"
)

type Client struct {
	keyID  string
	secret []byte
	hc     *http.Client
	socket string
}

// NewClient talks to the local agent over its unix socket.
func NewClient(socket, keyID string, secret []byte) *Client {
	c := NewDialClient(func(ctx context.Context) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "unix", socket)
	}, keyID, secret)
	c.socket = socket
	return c
}

// NewDialClient talks to an agent over connections from dial: a unix socket, or a stream
// of a remote agent's multiplexed WebSocket session. Requests are signed either way.
func NewDialClient(dial func(ctx context.Context) (net.Conn, error), keyID string, secret []byte) *Client {
	tr := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) { return dial(ctx) },
		MaxIdleConns: 4, IdleConnTimeout: 30 * time.Second}
	return &Client{keyID: keyID, secret: secret, hc: &http.Client{Transport: tr}}
}

func (c *Client) req(ctx context.Context, method, path string, in any) (*http.Request, error) {
	var body []byte
	if in != nil {
		var err error
		if body, err = json.Marshal(in); err != nil {
			return nil, err
		}
	}
	r, err := http.NewRequestWithContext(ctx, method, "http://agent"+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	r.Header.Set("Content-Type", "application/json")
	sign.Sign(r, c.keyID, c.secret, body)
	return r, nil
}

func decodeErr(resp *http.Response) error {
	var eb ErrBody
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if json.Unmarshal(b, &eb) != nil || eb.Error.Code == "" {
		return errs.New(resp.StatusCode, "agent.http", "status", resp.StatusCode, "body", string(b))
	}
	return &errs.E{Status: resp.StatusCode, Code: eb.Error.Code, Params: eb.Error.Params}
}

func (c *Client) Do(ctx context.Context, method, path string, in, out any) error {
	rc, err := c.Stream(ctx, method, path, in)
	if err != nil {
		return err
	}
	defer rc.Close()
	if out == nil {
		_, _ = io.Copy(io.Discard, rc)
		return nil
	}
	return json.NewDecoder(rc).Decode(out)
}

func (c *Client) Stream(ctx context.Context, method, path string, in any) (io.ReadCloser, error) {
	r, err := c.req(ctx, method, path, in)
	if err != nil {
		return nil, err
	}
	resp, err := c.hc.Do(r)
	if err != nil {
		return nil, errs.New(502, "agent.unreachable", "detail", err.Error())
	}
	if resp.StatusCode/100 != 2 {
		defer resp.Body.Close()
		return nil, decodeErr(resp)
	}
	return resp.Body, nil
}

// Dial opens a signed WebSocket (used for PTY sessions).
func (c *Client) Dial(ctx context.Context, path string) (*websocket.Conn, error) {
	r, err := c.req(ctx, "GET", path, nil)
	if err != nil {
		return nil, err
	}
	conn, _, err := websocket.Dial(ctx, "ws://agent"+path, &websocket.DialOptions{HTTPClient: c.hc, HTTPHeader: r.Header})
	if err != nil {
		return nil, errs.New(502, "agent.unreachable", "detail", err.Error())
	}
	conn.SetReadLimit(1 << 20)
	return conn, nil
}

// DoRaw sends a raw (non-JSON) body, e.g. an uploaded archive.
func (c *Client) DoRaw(ctx context.Context, method, path string, body []byte) error {
	r, err := http.NewRequestWithContext(ctx, method, "http://agent"+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	r.Header.Set("Content-Type", "application/octet-stream")
	sign.Sign(r, c.keyID, c.secret, body)
	resp, err := c.hc.Do(r)
	if err != nil {
		return errs.New(502, "agent.unreachable", "detail", err.Error())
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return decodeErr(resp)
	}
	io.Copy(io.Discard, resp.Body)
	return nil
}
