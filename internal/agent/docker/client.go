// Package docker talks to the Docker Engine API over its unix socket with plain
// net/http (no SDK: keeps berth agent small), and runs `docker compose` for projects.
package docker

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/wangsirco/berth/internal/errs"
)

// apiVersion is the newest Docker API Berth speaks; older daemons (e.g. Debian 12's
// docker.io 20.10, API 1.41) get their own version, learned once from /version.
const apiVersion = "v1.45"

type Client struct {
	hc     *http.Client
	stream *http.Client
	verMu  sync.Mutex
	ver    string
}

// version negotiates the API version with the daemon (unversioned /version works on all).
func (c *Client) version(ctx context.Context) string {
	c.verMu.Lock()
	defer c.verMu.Unlock()
	if c.ver != "" {
		return c.ver
	}
	req, _ := http.NewRequestWithContext(ctx, "GET", "http://docker/version", nil)
	resp, err := c.hc.Do(req)
	if err != nil {
		return apiVersion // the daemon may not be up yet: ask again next time
	}
	defer resp.Body.Close()
	var v struct {
		APIVersion string `json:"ApiVersion"`
	}
	c.ver = apiVersion
	if json.NewDecoder(resp.Body).Decode(&v) == nil && v.APIVersion != "" && olderAPI(v.APIVersion, strings.TrimPrefix(apiVersion, "v")) {
		c.ver = "v" + v.APIVersion
	}
	return c.ver
}

// olderAPI reports whether Docker API version a ("1.41") is older than b.
func olderAPI(a, b string) bool {
	pa, pb := strings.SplitN(a, ".", 2), strings.SplitN(b, ".", 2)
	if len(pa) != 2 || len(pb) != 2 {
		return false
	}
	ma, _ := strconv.Atoi(pa[0])
	mb, _ := strconv.Atoi(pb[0])
	na, _ := strconv.Atoi(pa[1])
	nb, _ := strconv.Atoi(pb[1])
	return ma < mb || ma == mb && na < nb
}

func New(sock string) *Client {
	tr := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "unix", sock)
	}, MaxIdleConns: 8, IdleConnTimeout: 30 * time.Second}
	return &Client{hc: &http.Client{Transport: tr, Timeout: 30 * time.Second}, stream: &http.Client{Transport: tr}}
}

type apiError struct {
	Message string `json:"message"`
}

func (c *Client) do(ctx context.Context, hc *http.Client, method, path string, q url.Values, body io.Reader) (*http.Response, error) {
	u := "http://docker/" + c.version(ctx) + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, errs.New(502, "docker.unreachable", "detail", err.Error())
	}
	if resp.StatusCode >= 400 {
		defer resp.Body.Close()
		var e apiError
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		_ = json.Unmarshal(b, &e)
		if e.Message == "" {
			e.Message = strings.TrimSpace(string(b))
		}
		switch resp.StatusCode {
		case 404:
			return nil, errs.New(404, "container.not_found", "detail", e.Message)
		case 409:
			return nil, errs.New(409, "docker.conflict", "detail", e.Message)
		}
		return nil, errs.New(resp.StatusCode, "docker.error", "detail", e.Message)
	}
	return resp, nil
}

func (c *Client) getJSON(ctx context.Context, path string, q url.Values, out any) error {
	resp, err := c.do(ctx, c.hc, "GET", path, q, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return json.NewDecoder(resp.Body).Decode(out)
}

// ---- containers ----

type Port struct {
	Private int    `json:"private"`
	Public  int    `json:"public,omitempty"`
	Proto   string `json:"proto"`
	IP      string `json:"ip,omitempty"`
}

type Mount struct {
	Type        string `json:"type"`
	Name        string `json:"name,omitempty"`
	Source      string `json:"source,omitempty"`
	Destination string `json:"destination"`
	RW          bool   `json:"rw"`
}

type Container struct {
	ID           string            `json:"id"`
	Name         string            `json:"name"`
	Image        string            `json:"image"`
	ImageID      string            `json:"image_id"`
	State        string            `json:"state"`
	Status       string            `json:"status"`
	Created      int64             `json:"created"`
	Ports        []Port            `json:"ports"`
	Project      string            `json:"project,omitempty"`
	Service      string            `json:"service,omitempty"`
	Networks     []string          `json:"networks"`
	IPs          map[string]string `json:"ips,omitempty"` // network -> address
	Mounts       []Mount           `json:"mounts"`
	Labels       map[string]string `json:"labels"`
	RestartCount int               `json:"restart_count"`
	MemLimit     int64             `json:"mem_limit,omitempty"`
}

type rawContainer struct {
	ID      string   `json:"Id"`
	Names   []string `json:"Names"`
	Image   string   `json:"Image"`
	ImageID string   `json:"ImageID"`
	State   string   `json:"State"`
	Status  string   `json:"Status"`
	Created int64    `json:"Created"`
	Ports   []struct {
		IP          string `json:"IP"`
		PrivatePort int    `json:"PrivatePort"`
		PublicPort  int    `json:"PublicPort"`
		Type        string `json:"Type"`
	} `json:"Ports"`
	Labels          map[string]string `json:"Labels"`
	NetworkSettings struct {
		Networks map[string]json.RawMessage `json:"Networks"`
	} `json:"NetworkSettings"`
	Mounts []struct {
		Type        string `json:"Type"`
		Name        string `json:"Name"`
		Source      string `json:"Source"`
		Destination string `json:"Destination"`
		RW          bool   `json:"RW"`
	} `json:"Mounts"`
}

func (r rawContainer) convert() Container {
	c := Container{ID: r.ID, Image: r.Image, ImageID: r.ImageID, State: r.State, Status: r.Status, Created: r.Created,
		Labels: r.Labels, Ports: []Port{}, Networks: []string{}, Mounts: []Mount{}}
	if len(r.Names) > 0 {
		c.Name = strings.TrimPrefix(r.Names[0], "/")
	}
	c.Project = r.Labels["com.docker.compose.project"]
	c.Service = r.Labels["com.docker.compose.service"]
	seen := map[string]bool{}
	for _, p := range r.Ports {
		key := fmt.Sprintf("%d/%s/%d", p.PrivatePort, p.Type, p.PublicPort)
		if seen[key] { // IPv4 and IPv6 bindings of the same port
			continue
		}
		seen[key] = true
		c.Ports = append(c.Ports, Port{Private: p.PrivatePort, Public: p.PublicPort, Proto: p.Type, IP: p.IP})
	}
	for n, raw := range r.NetworkSettings.Networks {
		c.Networks = append(c.Networks, n)
		var nw struct{ IPAddress string }
		if json.Unmarshal(raw, &nw) == nil && nw.IPAddress != "" {
			if c.IPs == nil {
				c.IPs = map[string]string{}
			}
			c.IPs[n] = nw.IPAddress
		}
	}
	sort.Strings(c.Networks)
	for _, m := range r.Mounts {
		c.Mounts = append(c.Mounts, Mount{Type: m.Type, Name: m.Name, Source: m.Source, Destination: m.Destination, RW: m.RW})
	}
	return c
}

func (c *Client) Containers(ctx context.Context) ([]Container, error) {
	var raw []rawContainer
	if err := c.getJSON(ctx, "/containers/json", url.Values{"all": {"1"}}, &raw); err != nil {
		return nil, err
	}
	out := make([]Container, 0, len(raw))
	for _, r := range raw {
		out = append(out, r.convert())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

type ContainerDetail struct {
	Container
	Env         []string `json:"env"`
	Cmd         []string `json:"cmd"`
	Health      string   `json:"health,omitempty"`
	OOMKilled   bool     `json:"oom_killed"`
	Pid         int      `json:"pid"`
	Tty         bool     `json:"tty"`
	ComposeFile string   `json:"compose_file,omitempty"`
	StartedAt   string   `json:"started_at,omitempty"`
	Runtime     string   `json:"runtime,omitempty"` // runc, krun (4K-page compatibility VM)
}

type rawInspect struct {
	ID           string `json:"Id"`
	Name         string `json:"Name"`
	Image        string `json:"Image"`
	Created      string `json:"Created"`
	RestartCount int    `json:"RestartCount"`
	Config       struct {
		Image  string            `json:"Image"`
		Env    []string          `json:"Env"`
		Cmd    []string          `json:"Cmd"`
		Tty    bool              `json:"Tty"`
		Labels map[string]string `json:"Labels"`
	} `json:"Config"`
	State struct {
		Status    string `json:"Status"`
		Pid       int    `json:"Pid"`
		OOMKilled bool   `json:"OOMKilled"`
		StartedAt string `json:"StartedAt"`
		Health    *struct {
			Status string `json:"Status"`
		} `json:"Health"`
	} `json:"State"`
	HostConfig struct {
		Memory  int64  `json:"Memory"`
		Runtime string `json:"Runtime"`
	} `json:"HostConfig"`
}

func (c *Client) Inspect(ctx context.Context, id string) (*ContainerDetail, error) {
	var ri rawInspect
	if err := c.getJSON(ctx, "/containers/"+url.PathEscape(id)+"/json", nil, &ri); err != nil {
		return nil, err
	}
	// The list endpoint has ports/mounts/networks in the shape we use; reuse it.
	var raw []rawContainer
	if err := c.getJSON(ctx, "/containers/json", url.Values{"all": {"1"}, "filters": {`{"id":["` + ri.ID + `"]}`}}, &raw); err != nil {
		return nil, err
	}
	d := &ContainerDetail{Env: maskEnvList(ri.Config.Env), Cmd: ri.Config.Cmd, OOMKilled: ri.State.OOMKilled, Pid: ri.State.Pid,
		Tty: ri.Config.Tty, StartedAt: ri.State.StartedAt, ComposeFile: ri.Config.Labels["com.docker.compose.project.config_files"]}
	if len(raw) == 1 {
		d.Container = raw[0].convert()
	} else {
		d.Container = Container{ID: ri.ID, Name: strings.TrimPrefix(ri.Name, "/"), Image: ri.Config.Image, State: ri.State.Status, Labels: ri.Config.Labels}
	}
	d.RestartCount, d.MemLimit, d.Runtime = ri.RestartCount, ri.HostConfig.Memory, ri.HostConfig.Runtime
	if ri.State.Health != nil {
		d.Health = ri.State.Health.Status
	}
	if d.Env == nil {
		d.Env = []string{}
	}
	return d, nil
}

// Action runs start|stop|restart|kill on a container, or remove (q: v=1 removes anonymous volumes).
func (c *Client) Action(ctx context.Context, id, action string, q url.Values) error {
	method, path := "POST", "/containers/"+url.PathEscape(id)+"/"+action
	if action == "remove" {
		method, path = "DELETE", "/containers/"+url.PathEscape(id)
	}
	resp, err := c.do(ctx, c.stream, method, path, q, nil) // stop can take a while
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

// ---- logs ----

type LogLine struct {
	TS     string `json:"t"`
	Stream string `json:"s"`
	Line   string `json:"l"`
}

// Logs streams log lines to fn. Non-TTY containers use Docker's 8-byte multiplexing.
func (c *Client) Logs(ctx context.Context, id string, tail int, follow bool, since string, fn func(LogLine)) error {
	d, err := c.Inspect(ctx, id)
	if err != nil {
		return err
	}
	q := url.Values{"stdout": {"1"}, "stderr": {"1"}, "timestamps": {"1"}, "tail": {fmt.Sprint(tail)}}
	if follow {
		q.Set("follow", "1")
	}
	if since != "" {
		q.Set("since", since)
	}
	resp, err := c.do(ctx, c.stream, "GET", "/containers/"+url.PathEscape(id)+"/logs", q, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if d.Tty {
		return splitLines(resp.Body, "out", fn)
	}
	return demux(resp.Body, fn)
}

func demux(r io.Reader, fn func(LogLine)) error {
	br := bufio.NewReaderSize(r, 64<<10)
	var hdr [8]byte
	partial := map[string]string{}
	for {
		if _, err := io.ReadFull(br, hdr[:]); err != nil {
			if err == io.EOF || err == io.ErrUnexpectedEOF {
				return nil
			}
			return err
		}
		stream := "out"
		if hdr[0] == 2 {
			stream = "err"
		}
		n := binary.BigEndian.Uint32(hdr[4:])
		buf := make([]byte, n)
		if _, err := io.ReadFull(br, buf); err != nil {
			return nil
		}
		text := partial[stream] + string(buf)
		lines := strings.Split(text, "\n")
		partial[stream] = lines[len(lines)-1]
		for _, l := range lines[:len(lines)-1] {
			fn(parseLogLine(stream, l))
		}
	}
}

func splitLines(r io.Reader, stream string, fn func(LogLine)) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		fn(parseLogLine(stream, strings.TrimRight(sc.Text(), "\r")))
	}
	return nil
}

func parseLogLine(stream, l string) LogLine {
	ts, rest, ok := strings.Cut(l, " ")
	if !ok || len(ts) < 20 || ts[4] != '-' {
		return LogLine{Stream: stream, Line: l}
	}
	return LogLine{TS: ts, Stream: stream, Line: rest}
}

// ---- stats ----

type Stat struct {
	ID       string  `json:"id"`
	CPU      float64 `json:"cpu"` // percent of one CPU × online CPUs (docker stats semantics)
	MemBytes int64   `json:"mem_bytes"`
	MemLimit int64   `json:"mem_limit"`
	NetRx    int64   `json:"net_rx"`
	NetTx    int64   `json:"net_tx"`
	BlkR     int64   `json:"blk_r"`
	BlkW     int64   `json:"blk_w"`
}

type rawStats struct {
	CPU struct {
		Usage struct {
			Total uint64 `json:"total_usage"`
		} `json:"cpu_usage"`
		System uint64 `json:"system_cpu_usage"`
		Online int    `json:"online_cpus"`
	} `json:"cpu_stats"`
	PreCPU struct {
		Usage struct {
			Total uint64 `json:"total_usage"`
		} `json:"cpu_usage"`
		System uint64 `json:"system_cpu_usage"`
	} `json:"precpu_stats"`
	Mem struct {
		Usage int64            `json:"usage"`
		Limit int64            `json:"limit"`
		Stats map[string]int64 `json:"stats"`
	} `json:"memory_stats"`
	Networks map[string]struct {
		Rx int64 `json:"rx_bytes"`
		Tx int64 `json:"tx_bytes"`
	} `json:"networks"`
	Blkio struct {
		IO []struct {
			Op    string `json:"op"`
			Value int64  `json:"value"`
		} `json:"io_service_bytes_recursive"`
	} `json:"blkio_stats"`
}

func (s rawStats) convert(id string) Stat {
	st := Stat{ID: id, MemLimit: s.Mem.Limit}
	cpuDelta := float64(s.CPU.Usage.Total) - float64(s.PreCPU.Usage.Total)
	sysDelta := float64(s.CPU.System) - float64(s.PreCPU.System)
	if sysDelta > 0 && cpuDelta > 0 && s.PreCPU.System > 0 {
		st.CPU = cpuDelta / sysDelta * float64(max(s.CPU.Online, 1)) * 100
	}
	st.MemBytes = max(0, s.Mem.Usage-s.Mem.Stats["inactive_file"])
	for _, n := range s.Networks {
		st.NetRx += n.Rx
		st.NetTx += n.Tx
	}
	for _, b := range s.Blkio.IO {
		switch strings.ToLower(b.Op) {
		case "read":
			st.BlkR += b.Value
		case "write":
			st.BlkW += b.Value
		}
	}
	return st
}

// Stats samples all running containers in parallel (each call takes ~1 s for the CPU delta).
func (c *Client) Stats(ctx context.Context) ([]Stat, error) {
	list, err := c.Containers(ctx)
	if err != nil {
		return nil, err
	}
	var mu sync.Mutex
	var wg sync.WaitGroup
	out := []Stat{}
	sem := make(chan struct{}, 8)
	for _, ct := range list {
		if ct.State != "running" {
			continue
		}
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			var s rawStats
			if err := c.getJSON(ctx, "/containers/"+id+"/stats", url.Values{"stream": {"0"}}, &s); err != nil {
				return
			}
			mu.Lock()
			out = append(out, s.convert(id))
			mu.Unlock()
		}(ct.ID)
	}
	wg.Wait()
	return out, nil
}

// ---- images, networks, volumes ----

type Image struct {
	ID       string   `json:"id"`
	Tags     []string `json:"tags"`
	Size     int64    `json:"size"`
	Created  int64    `json:"created"`
	Dangling bool     `json:"dangling"`
	UsedBy   []string `json:"used_by"`
}

func (c *Client) Images(ctx context.Context) ([]Image, error) {
	var raw []struct {
		ID       string   `json:"Id"`
		RepoTags []string `json:"RepoTags"`
		Size     int64    `json:"Size"`
		Created  int64    `json:"Created"`
	}
	if err := c.getJSON(ctx, "/images/json", nil, &raw); err != nil {
		return nil, err
	}
	cts, _ := c.Containers(ctx)
	out := make([]Image, 0, len(raw))
	for _, r := range raw {
		im := Image{ID: r.ID, Tags: []string{}, Size: r.Size, Created: r.Created, UsedBy: []string{}}
		for _, t := range r.RepoTags {
			if t != "<none>:<none>" {
				im.Tags = append(im.Tags, t)
			}
		}
		im.Dangling = len(im.Tags) == 0
		for _, ct := range cts {
			if ct.ImageID == r.ID {
				im.UsedBy = append(im.UsedBy, ct.Name)
			}
		}
		out = append(out, im)
	}
	return out, nil
}

func (c *Client) RemoveImage(ctx context.Context, ref string) error {
	resp, err := c.do(ctx, c.stream, "DELETE", "/images/"+url.PathEscape(ref), nil, nil)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

// PruneImages removes dangling images and reports the bytes reclaimed.
func (c *Client) PruneImages(ctx context.Context) (int64, error) {
	resp, err := c.do(ctx, c.stream, "POST", "/images/prune", url.Values{"filters": {`{"dangling":["true"]}`}}, nil)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	var r struct {
		SpaceReclaimed int64 `json:"SpaceReclaimed"`
	}
	err = json.NewDecoder(resp.Body).Decode(&r)
	return r.SpaceReclaimed, err
}

// Pull pulls ref and reports progress lines.
func (c *Client) Pull(ctx context.Context, ref string, progress func(string)) error {
	img, tag := ref, "latest"
	if i := strings.LastIndex(ref, ":"); i > strings.LastIndex(ref, "/") {
		img, tag = ref[:i], ref[i+1:]
	}
	if i := strings.Index(ref, "@"); i > 0 {
		img, tag = ref[:i], ref[i+1:]
	}
	resp, err := c.do(ctx, c.stream, "POST", "/images/create", url.Values{"fromImage": {img}, "tag": {tag}}, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	dec := json.NewDecoder(resp.Body)
	last := map[string]string{}
	for {
		var m struct {
			Status string `json:"status"`
			ID     string `json:"id"`
			Error  string `json:"error"`
		}
		if err := dec.Decode(&m); err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
		if m.Error != "" {
			return errs.New(502, "image.pull_failed", "ref", ref, "detail", m.Error)
		}
		// report each layer's status changes, not every byte of progress
		if last[m.ID] != m.Status {
			last[m.ID] = m.Status
			if m.ID != "" {
				progress(m.ID + ": " + m.Status)
			} else {
				progress(m.Status)
			}
		}
	}
}

type Network struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	Driver     string   `json:"driver"`
	Subnet     string   `json:"subnet,omitempty"`
	Containers []string `json:"containers"`
	Builtin    bool     `json:"builtin"`
}

func (c *Client) Networks(ctx context.Context) ([]Network, error) {
	var raw []struct {
		ID     string `json:"Id"`
		Name   string `json:"Name"`
		Driver string `json:"Driver"`
		IPAM   struct {
			Config []struct {
				Subnet string `json:"Subnet"`
			} `json:"Config"`
		} `json:"IPAM"`
	}
	if err := c.getJSON(ctx, "/networks", nil, &raw); err != nil {
		return nil, err
	}
	cts, _ := c.Containers(ctx)
	out := []Network{}
	for _, r := range raw {
		n := Network{ID: r.ID, Name: r.Name, Driver: r.Driver, Containers: []string{}, Builtin: r.Name == "bridge" || r.Name == "host" || r.Name == "none"}
		if len(r.IPAM.Config) > 0 {
			n.Subnet = r.IPAM.Config[0].Subnet
		}
		for _, ct := range cts {
			for _, cn := range ct.Networks {
				if cn == r.Name {
					n.Containers = append(n.Containers, ct.Name)
				}
			}
		}
		out = append(out, n)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

type Volume struct {
	Name       string   `json:"name"`
	Driver     string   `json:"driver"`
	Mountpoint string   `json:"mountpoint"`
	Created    string   `json:"created"`
	Project    string   `json:"project,omitempty"`
	Containers []string `json:"containers"`
}

func (c *Client) Volumes(ctx context.Context) ([]Volume, error) {
	var raw struct {
		Volumes []struct {
			Name       string            `json:"Name"`
			Driver     string            `json:"Driver"`
			Mountpoint string            `json:"Mountpoint"`
			CreatedAt  string            `json:"CreatedAt"`
			Labels     map[string]string `json:"Labels"`
		} `json:"Volumes"`
	}
	if err := c.getJSON(ctx, "/volumes", nil, &raw); err != nil {
		return nil, err
	}
	cts, _ := c.Containers(ctx)
	out := []Volume{}
	for _, r := range raw.Volumes {
		v := Volume{Name: r.Name, Driver: r.Driver, Mountpoint: r.Mountpoint, Created: r.CreatedAt, Project: r.Labels["com.docker.compose.project"], Containers: []string{}}
		for _, ct := range cts {
			for _, m := range ct.Mounts {
				if m.Type == "volume" && m.Name == r.Name {
					v.Containers = append(v.Containers, ct.Name)
				}
			}
		}
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (c *Client) RemoveVolume(ctx context.Context, name string) error {
	resp, err := c.do(ctx, c.hc, "DELETE", "/volumes/"+url.PathEscape(name), nil, nil)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

func (c *Client) RemoveNetwork(ctx context.Context, name string) error {
	resp, err := c.do(ctx, c.hc, "DELETE", "/networks/"+url.PathEscape(name), nil, nil)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

// Reclaimable estimates what pruning dangling images and the build cache would free.
func (c *Client) Reclaimable(ctx context.Context) (images, build int64) {
	var df struct {
		Images []struct {
			Size       int64    `json:"Size"`
			Containers int64    `json:"Containers"`
			RepoTags   []string `json:"RepoTags"`
		} `json:"Images"`
		BuildCache []struct {
			Size   int64 `json:"Size"`
			InUse  bool  `json:"InUse"`
			Shared bool  `json:"Shared"`
		} `json:"BuildCache"`
	}
	if c.getJSON(ctx, "/system/df", nil, &df) != nil {
		return 0, 0
	}
	for _, i := range df.Images {
		dangling := len(i.RepoTags) == 0 || len(i.RepoTags) == 1 && i.RepoTags[0] == "<none>:<none>"
		if dangling && i.Containers == 0 {
			images += i.Size
		}
	}
	for _, b := range df.BuildCache {
		if !b.InUse && !b.Shared {
			build += b.Size
		}
	}
	return images, build
}

// PruneBuildCache drops build cache nobody uses and reports the bytes reclaimed.
func (c *Client) PruneBuildCache(ctx context.Context) (int64, error) {
	resp, err := c.do(ctx, c.stream, "POST", "/build/prune", nil, nil)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	var r struct {
		SpaceReclaimed int64 `json:"SpaceReclaimed"`
	}
	err = json.NewDecoder(resp.Body).Decode(&r)
	return r.SpaceReclaimed, err
}
