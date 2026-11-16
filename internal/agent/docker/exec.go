package docker

import (
	"bytes"
	"context"
	"encoding/json"
	"net/url"
	"strings"

	"github.com/wangsirco/berth/internal/errs"
)

// Exec runs cmd in a running container and returns its combined output and exit code.
func (c *Client) Exec(ctx context.Context, id string, cmd []string) (string, int, error) {
	body, _ := json.Marshal(map[string]any{"Cmd": cmd, "AttachStdout": true, "AttachStderr": true})
	resp, err := c.do(ctx, c.hc, "POST", "/containers/"+url.PathEscape(id)+"/exec", nil, bytes.NewReader(body))
	if err != nil {
		return "", -1, err
	}
	var created struct{ Id string }
	json.NewDecoder(resp.Body).Decode(&created)
	resp.Body.Close()
	if created.Id == "" {
		return "", -1, errs.New(502, "docker.error", "detail", "exec create returned no id")
	}
	resp, err = c.do(ctx, c.stream, "POST", "/exec/"+created.Id+"/start", nil, strings.NewReader(`{"Detach":false,"Tty":false}`))
	if err != nil {
		return "", -1, err
	}
	var out strings.Builder
	demux(resp.Body, func(l LogLine) {
		out.WriteString(l.Line)
		out.WriteByte('\n')
	})
	resp.Body.Close()
	var info struct{ ExitCode int }
	if err := c.getJSON(ctx, "/exec/"+created.Id+"/json", nil, &info); err != nil {
		return out.String(), -1, err
	}
	return out.String(), info.ExitCode, nil
}

// EnsureNetwork creates a bridge network if it does not exist.
func (c *Client) EnsureNetwork(ctx context.Context, name string) (created bool, err error) {
	var n map[string]any
	if c.getJSON(ctx, "/networks/"+url.PathEscape(name), nil, &n) == nil {
		return false, nil
	}
	body, _ := json.Marshal(map[string]any{"Name": name, "Driver": "bridge", "Labels": map[string]string{"io.berth.managed": "true"}})
	resp, err := c.do(ctx, c.hc, "POST", "/networks/create", nil, bytes.NewReader(body))
	if err != nil {
		return false, err
	}
	resp.Body.Close()
	return true, nil
}

// Connect attaches a container to a network (no-op when already attached).
func (c *Client) Connect(ctx context.Context, network, container string) error {
	body, _ := json.Marshal(map[string]string{"Container": container})
	resp, err := c.do(ctx, c.hc, "POST", "/networks/"+url.PathEscape(network)+"/connect", nil, bytes.NewReader(body))
	if err != nil {
		if d, _ := errs.From(err).Params["detail"].(string); strings.Contains(d, "already exists") {
			return nil
		}
		return err
	}
	resp.Body.Close()
	return nil
}

// Disconnect detaches a container from a network.
func (c *Client) Disconnect(ctx context.Context, network, container string) error {
	body, _ := json.Marshal(map[string]any{"Container": container, "Force": false})
	resp, err := c.do(ctx, c.hc, "POST", "/networks/"+url.PathEscape(network)+"/disconnect", nil, bytes.NewReader(body))
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}
