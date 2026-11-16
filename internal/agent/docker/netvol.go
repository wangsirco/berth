package docker

// Creating networks and volumes by hand (1Panel 的“新建网络/存储卷”): a network to put
// containers from different compose projects on, or a named volume to mount.

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"regexp"

	"github.com/wangsirco/berth/internal/agent"
	"github.com/wangsirco/berth/internal/errs"
)

var objNameRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,62}$`)

type NetworkSpec struct {
	Name     string `json:"name"`
	Subnet   string `json:"subnet,omitempty"`   // e.g. 172.30.0.0/24; empty lets Docker pick
	Internal bool   `json:"internal,omitempty"` // no way out to the internet
}

func (c *Client) CreateNetwork(ctx context.Context, s NetworkSpec) error {
	if !objNameRe.MatchString(s.Name) {
		return errs.New(400, "docker.bad_name", "name", s.Name)
	}
	body := map[string]any{"Name": s.Name, "Driver": "bridge", "Internal": s.Internal, "CheckDuplicate": true,
		"Labels": map[string]string{"berth.created": "panel"}}
	if s.Subnet != "" {
		ip, n, err := net.ParseCIDR(s.Subnet)
		if err != nil || ip.To4() == nil {
			return errs.New(400, "docker.bad_subnet", "subnet", s.Subnet)
		}
		body["IPAM"] = map[string]any{"Config": []map[string]string{{"Subnet": n.String()}}}
	}
	b, _ := json.Marshal(body)
	resp, err := c.do(ctx, c.hc, "POST", "/networks/create", nil, bytes.NewReader(b))
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

func (c *Client) CreateVolume(ctx context.Context, name string) error {
	if !objNameRe.MatchString(name) {
		return errs.New(400, "docker.bad_name", "name", name)
	}
	vols, _ := c.Volumes(ctx)
	for _, v := range vols {
		if v.Name == name {
			return errs.New(409, "docker.conflict", "detail", "volume "+name+" already exists")
		}
	}
	b, _ := json.Marshal(map[string]any{"Name": name, "Driver": "local", "Labels": map[string]string{"berth.created": "panel"}})
	resp, err := c.do(ctx, c.hc, "POST", "/volumes/create", nil, bytes.NewReader(b))
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

func registerNetVol(s *agent.Server, c *Client) {
	s.Handle("POST /v1/docker/networks/create", func(w http.ResponseWriter, r *http.Request, body []byte) {
		var in NetworkSpec
		json.Unmarshal(body, &in)
		reply(w, map[string]bool{"ok": true}, c.CreateNetwork(r.Context(), in))
	})
	s.Handle("POST /v1/docker/volumes/create", func(w http.ResponseWriter, r *http.Request, body []byte) {
		var in struct{ Name string }
		json.Unmarshal(body, &in)
		reply(w, map[string]bool{"ok": true}, c.CreateVolume(r.Context(), in.Name))
	})
}
