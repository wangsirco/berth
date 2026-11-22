// Package incus manages system containers and virtual machines through Incus's REST API
// on its local unix socket (方案 §5.4.1), and sets Incus up on Fedora Asahi.
package incus

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"

	"github.com/wangsirco/berth/internal/errs"
	"time"

	"github.com/wangsirco/berth/internal/agent"
)

// Socket is Incus's API socket (Fedora's package sets INCUS_SOCKET to this).
var Socket = firstExisting("/run/incus/unix.socket", "/var/lib/incus/unix.socket")

func firstExisting(paths ...string) string {
	for _, p := range paths {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return paths[0]
}

type Client struct{ hc *http.Client }

func New() *Client {
	return &Client{hc: &http.Client{Timeout: 10 * time.Minute, Transport: incusTransport()}}
}

// incusTransport opens one connection per request and closes it after. With a pool, Go
// sometimes dials a connection that another request's freed one makes unneeded and parks it
// idle without sending a byte; incusd's unix listener waits in Accept for the first bytes of
// every new connection, so that one silent connection blocks the whole Incus API (every
// incus command hangs) until it is used or closed. Seen on 6.23 under the panel's polling.
func incusTransport() *http.Transport {
	return &http.Transport{DisableKeepAlives: true, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "unix", Socket)
	}}
}

// Error is Incus's own message.
type Error struct {
	Status  int
	Message string
}

func (e *Error) Error() string { return e.Message }

type envelope struct {
	Type      string          `json:"type"` // sync | async | error
	Status    string          `json:"status"`
	ErrorCode int             `json:"error_code"`
	Error     string          `json:"error"`
	Operation string          `json:"operation"`
	Metadata  json.RawMessage `json:"metadata"`
}

func (c *Client) do(ctx context.Context, method, path string, in, out any) (*envelope, error) {
	var body io.Reader
	if in != nil {
		b, _ := json.Marshal(in)
		body = bytes.NewReader(b)
	}
	// reads answer in well under a second; one that hangs means incusd itself is stuck
	// (seen after many start/stop cycles), which a restart of incus fixes
	quick := method == "GET" && !strings.Contains(path, "/wait")
	if quick {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://incus"+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.hc.Do(req)
	if err != nil {
		if quick && errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, errs.New(504, "vms.incus_stuck")
		}
		return nil, &Error{Status: 503, Message: "Incus is not running: " + err.Error()}
	}
	defer resp.Body.Close()
	var e envelope
	if err := json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(&e); err != nil {
		return nil, &Error{Status: resp.StatusCode, Message: fmt.Sprintf("Incus: HTTP %d", resp.StatusCode)}
	}
	if e.Type == "error" {
		return nil, &Error{Status: e.ErrorCode, Message: e.Error}
	}
	if out != nil && e.Type == "sync" {
		if err := json.Unmarshal(e.Metadata, out); err != nil {
			return nil, err
		}
	}
	return &e, nil
}

// wait follows an async operation to its end.
func (c *Client) wait(ctx context.Context, e *envelope) error {
	if e == nil || e.Type != "async" || e.Operation == "" {
		return nil
	}
	var op struct {
		Status string `json:"status"`
		Err    string `json:"err"`
	}
	if _, err := c.do(ctx, "GET", e.Operation+"/wait?timeout=900", nil, &op); err != nil {
		return err
	}
	if op.Status != "Success" {
		return &Error{Status: 500, Message: firstNonEmpty(op.Err, op.Status)}
	}
	return nil
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}

// ---- instances ----

type Instance struct {
	Name        string            `json:"name"`
	Type        string            `json:"type"` // container | virtual-machine
	Status      string            `json:"status"`
	Description string            `json:"description,omitempty"`
	Created     time.Time         `json:"created_at"`
	Config      map[string]string `json:"config"`
	Image       string            `json:"image,omitempty"`
	CPUs        string            `json:"cpus,omitempty"`
	Memory      string            `json:"memory,omitempty"`
	Disk        string            `json:"disk,omitempty"`
	IPv4        []string          `json:"ipv4"`
	IPv6        []string          `json:"ipv6,omitempty"`
	MemUsed     int64             `json:"mem_used,omitempty"`
	CPUNs       int64             `json:"cpu_ns,omitempty"`
	DiskUsed    int64             `json:"disk_used,omitempty"`
	Snapshots   []Snapshot        `json:"snapshots"`
	Forwards    []Forward         `json:"forwards"`
	Net         NetState          `json:"net"`
	Perms       Perms             `json:"perms"`
	// VMs: a VNC screen the panel can open (see VNCArg), and whether incus-agent answers
	// inside (without it Incus knows no addresses outside NAT, and the shell cannot open).
	VNC    bool  `json:"vnc,omitempty"`
	Agent  bool  `json:"agent"`
	Booted int64 `json:"booted_at,omitempty"` // unix seconds the machine last started
}

type Snapshot struct {
	Name    string    `json:"name"`
	Created time.Time `json:"created_at"`
}

// Forward is a host port passed to the instance (Incus proxy device named berth-fwd-*).
type Forward struct {
	Name       string `json:"name"`
	Proto      string `json:"proto"`
	HostPort   int    `json:"host_port"`
	TargetPort int    `json:"target_port"`
}

type rawInstance struct {
	Name            string                       `json:"name"`
	Type            string                       `json:"type"`
	Status          string                       `json:"status"`
	Description     string                       `json:"description"`
	CreatedAt       time.Time                    `json:"created_at"`
	Config          map[string]string            `json:"config"`
	ExpandedDevices map[string]map[string]string `json:"expanded_devices"`
	LastUsedAt      time.Time                    `json:"last_used_at"`
	State           *struct {
		Processes int `json:"processes"`
		Memory    struct {
			Usage int64 `json:"usage"`
		} `json:"memory"`
		CPU struct {
			Usage int64 `json:"usage"`
		} `json:"cpu"`
		Disk map[string]struct {
			Usage int64 `json:"usage"`
		} `json:"disk"`
		Network map[string]struct {
			Addresses []struct {
				Family  string `json:"family"`
				Address string `json:"address"`
				Scope   string `json:"scope"`
			} `json:"addresses"`
		} `json:"network"`
	} `json:"state"`
	Snapshots []struct {
		Name      string    `json:"name"`
		CreatedAt time.Time `json:"created_at"`
	} `json:"snapshots"`
}

func (r rawInstance) convert() Instance {
	in := Instance{Name: r.Name, Type: r.Type, Status: r.Status, Description: r.Description, Created: r.CreatedAt,
		Config: map[string]string{}, IPv4: []string{}, Snapshots: []Snapshot{}, Forwards: []Forward{}}
	for k, v := range r.Config {
		if strings.HasPrefix(k, "user.") || strings.HasPrefix(k, "limits.") || k == "image.description" || k == "image.os" || k == "image.release" {
			in.Config[k] = v
		}
	}
	in.Image = r.Config["image.description"]
	in.Net, in.Perms = readNet(r), readPerms(r)
	in.CPUs, in.Memory = r.Config["limits.cpu"], r.Config["limits.memory"]
	if root, ok := r.ExpandedDevices["root"]; ok {
		in.Disk = root["size"]
	}
	for name, d := range r.ExpandedDevices {
		if d["type"] == "proxy" && strings.HasPrefix(name, "berth-fwd-") {
			f := Forward{Name: name}
			f.Proto, f.HostPort = splitAddr(d["listen"])
			_, f.TargetPort = splitAddr(d["connect"])
			in.Forwards = append(in.Forwards, f)
		}
	}
	if r.Type == "virtual-machine" {
		in.VNC = r.Config["raw.qemu"] == VNCArg(r.Name)
	}
	if r.Status == "Running" && !r.LastUsedAt.IsZero() {
		in.Booted = r.LastUsedAt.Unix()
	}
	if s := r.State; s != nil {
		in.Agent = r.Type != "virtual-machine" || s.Processes >= 0
		in.MemUsed, in.CPUNs = s.Memory.Usage, s.CPU.Usage
		if d, ok := s.Disk["root"]; ok {
			in.DiskUsed = d.Usage
		}
		// the machine's own cards first (eth0, enp5s0…), in a stable order; bridges and
		// container networks inside it (docker0, br-…, veth…) are not its address
		nics := make([]string, 0, len(s.Network))
		for nic := range s.Network {
			if nic != "lo" && !innerNIC.MatchString(nic) {
				nics = append(nics, nic)
			}
		}
		sort.Slice(nics, func(i, j int) bool {
			pi, pj := physNIC.MatchString(nics[i]), physNIC.MatchString(nics[j])
			if pi != pj {
				return pi
			}
			return nics[i] < nics[j]
		})
		for _, nic := range nics {
			n := s.Network[nic]
			for _, a := range n.Addresses {
				if a.Scope != "global" {
					continue
				}
				if a.Family == "inet" {
					in.IPv4 = append(in.IPv4, a.Address)
				} else {
					in.IPv6 = append(in.IPv6, a.Address)
				}
			}
		}
	}
	for _, sn := range r.Snapshots {
		in.Snapshots = append(in.Snapshots, Snapshot{Name: sn.Name, Created: sn.CreatedAt})
	}
	return in
}

var (
	// networks a machine carries for its own containers or VMs: never its address
	innerNIC = regexp.MustCompile(`^(docker\d*|br-[0-9a-f]+|veth.*|cni\d*|flannel.*|cali.*|vxlan.*|virbr\d*|lxcbr\d*|lxdbr\d*|incusbr\d*|podman\d*|kube-.*|tun\d*|wg\d*)$`)
	physNIC  = regexp.MustCompile(`^(eth|en[ospx])`)
)

// "tcp:0.0.0.0:8080" → ("tcp", 8080)
func splitAddr(s string) (string, int) {
	parts := strings.Split(s, ":")
	if len(parts) < 2 {
		return "", 0
	}
	var port int
	fmt.Sscanf(parts[len(parts)-1], "%d", &port)
	return parts[0], port
}

func (c *Client) Instances(ctx context.Context) ([]Instance, error) {
	var raw []rawInstance
	if _, err := c.do(ctx, "GET", "/1.0/instances?recursion=2", nil, &raw); err != nil {
		return nil, err
	}
	out := make([]Instance, 0, len(raw))
	for _, r := range raw {
		out = append(out, r.convert())
	}
	return out, nil
}

func (c *Client) Instance(ctx context.Context, name string) (Instance, error) {
	var raw rawInstance
	if _, err := c.do(ctx, "GET", "/1.0/instances/"+url.PathEscape(name)+"?recursion=1", nil, &raw); err != nil {
		return Instance{}, err
	}
	var st json.RawMessage
	if _, err := c.do(ctx, "GET", "/1.0/instances/"+url.PathEscape(name)+"/state", nil, &st); err == nil {
		json.Unmarshal(st, &raw.State)
	}
	var snaps json.RawMessage
	if _, err := c.do(ctx, "GET", "/1.0/instances/"+url.PathEscape(name)+"/snapshots?recursion=1", nil, &snaps); err == nil {
		json.Unmarshal(snaps, &raw.Snapshots)
	}
	return raw.convert(), nil
}

// CreateSpec is what the panel asks for.
type CreateSpec struct {
	Name     string `json:"name"`
	Image    string `json:"image"` // alias on images:, e.g. debian/13/cloud
	VM       bool   `json:"vm"`
	CPUs     int    `json:"cpus"`
	MemoryMB int    `json:"memory_mb"`
	DiskGB   int    `json:"disk_gb"`
	UserData string `json:"user_data,omitempty"` // cloud-init (SSH keys, password)
	LAN      string `json:"lan,omitempty"`       // a host port: on the LAN from the start (else NAT)
	Note     string `json:"note,omitempty"`
	Start    bool   `json:"start"`
}

func (c *Client) Create(ctx context.Context, s CreateSpec) error {
	typ := "container"
	if s.VM {
		typ = "virtual-machine"
	}
	cfg := map[string]string{"limits.cpu": fmt.Sprint(s.CPUs), "limits.memory": fmt.Sprintf("%dMiB", s.MemoryMB), "user.berth": "1"}
	if s.VM {
		cfg["security.secureboot"] = "false" // Fedora's ARM firmware has no Microsoft-key variant
		cfg["raw.qemu"] = VNCArg(s.Name)
	}
	if s.UserData != "" {
		cfg["cloud-init.user-data"] = s.UserData
	}
	devs := map[string]any{"root": map[string]string{"type": "disk", "path": "/", "pool": "default", "size": fmt.Sprintf("%dGiB", s.DiskGB)}}
	if s.LAN != "" { // its own address from the router, set before the first boot: no second step
		nic, err := lanNIC(s.LAN)
		if err != nil {
			return err
		}
		devs["eth0"] = nic
	}
	body := map[string]any{
		"name": s.Name, "type": typ, "description": s.Note, "config": cfg, "start": false,
		"source":  map[string]any{"type": "image", "alias": s.Image, "server": "https://images.linuxcontainers.org", "protocol": "simplestreams"},
		"devices": devs,
	}
	e, err := c.do(ctx, "POST", "/1.0/instances", body, nil)
	if err != nil {
		return err
	}
	if err := c.wait(ctx, e); err != nil || !s.Start {
		return err
	}
	// The first start now and then fails in its pre-start hook (seen on Asahi when the host's
	// firewall is being touched at the same moment); a second try a moment later works.
	if err := c.SetState(ctx, s.Name, "start", false); err != nil {
		select {
		case <-ctx.Done():
			return err
		case <-time.After(3 * time.Second):
		}
		return c.SetState(ctx, s.Name, "start", false)
	}
	return nil
}

// SetState: start | stop | restart | freeze | unfreeze; force for stop/restart.
func (c *Client) SetState(ctx context.Context, name, action string, force bool) error {
	e, err := c.do(ctx, "PUT", "/1.0/instances/"+url.PathEscape(name)+"/state", map[string]any{"action": action, "force": force, "timeout": 60}, nil)
	if err != nil {
		return err
	}
	return c.wait(ctx, e)
}

func (c *Client) Delete(ctx context.Context, name string) error {
	e, err := c.do(ctx, "DELETE", "/1.0/instances/"+url.PathEscape(name), nil, nil)
	if err != nil {
		return err
	}
	return c.wait(ctx, e)
}

func (c *Client) Snapshot(ctx context.Context, name, snap string) error {
	e, err := c.do(ctx, "POST", "/1.0/instances/"+url.PathEscape(name)+"/snapshots", map[string]any{"name": snap}, nil)
	if err != nil {
		return err
	}
	return c.wait(ctx, e)
}

func (c *Client) DeleteSnapshot(ctx context.Context, name, snap string) error {
	e, err := c.do(ctx, "DELETE", "/1.0/instances/"+url.PathEscape(name)+"/snapshots/"+url.PathEscape(snap), nil, nil)
	if err != nil {
		return err
	}
	return c.wait(ctx, e)
}

// Restore puts the instance back to a snapshot (Incus stops a running one as needed).
func (c *Client) Restore(ctx context.Context, name, snap string) error {
	e, err := c.do(ctx, "PUT", "/1.0/instances/"+url.PathEscape(name), map[string]any{"restore": snap}, nil)
	if err != nil {
		return err
	}
	return c.wait(ctx, e)
}

func (c *Client) Clone(ctx context.Context, from, to string) error {
	e, err := c.do(ctx, "POST", "/1.0/instances", map[string]any{"name": to, "source": map[string]any{"type": "copy", "source": from}}, nil)
	if err != nil {
		return err
	}
	if err := c.wait(ctx, e); err != nil {
		return err
	}
	// the copy's screen gets its own socket, not the original's
	if raw, err := c.raw(ctx, to); err == nil {
		if cfg, _ := raw["config"].(map[string]any); cfg["raw.qemu"] == VNCArg(from) {
			return c.patch(ctx, to, map[string]any{"config": map[string]string{"raw.qemu": VNCArg(to)}})
		}
	}
	return nil
}

// VNCSocket is where a VM's screen is served: QEMU's own VNC server on a Unix socket in the
// VM's run directory (root only), relayed to the panel's noVNC. Incus' own graphical
// console is SPICE, which browsers cannot speak, and its text console does not work on
// ARM (the PL011 serial port cannot be re-attached: "Chardev user does not support
// chardev hotswap").
func VNCSocket(name string) string { return "/run/incus/" + name + "/vnc.sock" }

// VNCArg is the raw.qemu that adds the screen.
func VNCArg(name string) string { return "-vnc unix:" + VNCSocket(name) }

// EnableVNC adds the screen; it shows up at the next start.
func (c *Client) EnableVNC(ctx context.Context, name string) error {
	return c.patch(ctx, name, map[string]any{"config": map[string]string{"raw.qemu": VNCArg(name)}})
}

// AgentPolicy is the SELinux module (CIL) incus-agent needs on Fedora-like guests. The
// agent runs as init_t (its binary is copied to /run at boot), which may not listen on a
// vsock or make /dev/incus/sock. Without the agent Incus knows no address outside NAT, no
// shell opens, and cloud-init (which reads /dev/incus/sock) gets nothing new.
const AgentPolicy = "(allow init_t self (vsock_socket (listen accept)))\n" +
	"(allow init_t device_t (dir (create add_name remove_name write search)))\n" +
	"(allow init_t device_t (sock_file (create unlink setattr getattr write)))\n"

// AgentFixCommand is what to run inside a guest whose agent SELinux stopped.
func AgentFixCommand() string {
	return "printf '" + strings.ReplaceAll(AgentPolicy, "\n", "\\n") + "' > /root/berth-incus-agent.cil && semodule -i /root/berth-incus-agent.cil && systemctl reset-failed incus-agent; systemctl restart incus-agent"
}

// AgentGuard is cloud-config for new machines: the module and a boot unit that loads it
// (once) before incus-agent starts, whenever SELinux is on — it often comes later, with a
// package that pulls the policy in, when cloud-init can no longer help.
func AgentGuard() (writeFiles, runcmd string) {
	var b strings.Builder
	b.WriteString("write_files:\n  - path: /etc/berth/berth-incus-agent.cil\n    content: |\n")
	for _, l := range strings.Split(strings.TrimSpace(AgentPolicy), "\n") {
		b.WriteString("      " + l + "\n")
	}
	b.WriteString("  - path: /etc/systemd/system/berth-incus-agent-selinux.service\n    content: |\n" +
		"      [Unit]\n      Description=Let incus-agent run under SELinux (Berth)\n      Before=incus-agent.service\n      ConditionPathExists=/sys/fs/selinux/enforce\n" +
		"      [Service]\n      Type=oneshot\n      ExecStart=/bin/sh -c 'semodule -l | grep -qx berth-incus-agent || semodule -i /etc/berth/berth-incus-agent.cil'\n" +
		"      [Install]\n      WantedBy=multi-user.target incus-agent.service\n")
	return b.String(), "  - [sh, -c, \"systemctl daemon-reload; systemctl enable berth-incus-agent-selinux.service; true\"]\n"
}

// patch merges config and devices (a nil device value removes it).
func (c *Client) patch(ctx context.Context, name string, body map[string]any) error {
	e, err := c.do(ctx, "PATCH", "/1.0/instances/"+url.PathEscape(name), body, nil)
	if err != nil {
		return err
	}
	return c.wait(ctx, e)
}

// Resize changes CPU and memory (and grows the root disk; disks never shrink).
func (c *Client) Resize(ctx context.Context, name string, cpus, memMB, diskGB int) error {
	body := map[string]any{"config": map[string]string{"limits.cpu": fmt.Sprint(cpus), "limits.memory": fmt.Sprintf("%dMiB", memMB)}}
	if diskGB > 0 {
		body["devices"] = map[string]any{"root": map[string]string{"type": "disk", "path": "/", "pool": "default", "size": fmt.Sprintf("%dGiB", diskGB)}}
	}
	return c.patch(ctx, name, body)
}

// SetForward adds or (port 0) removes a host-port forward. VMs need NAT mode with the
// instance's address; containers use a plain proxy.
func (c *Client) SetForward(ctx context.Context, name, proto string, hostPort, targetPort int, vm bool, ip string) error {
	dev := fmt.Sprintf("berth-fwd-%s-%d", proto, hostPort)
	if targetPort == 0 {
		// PATCH merges, so removing a device means writing the instance back without it
		var cur map[string]any
		if _, err := c.do(ctx, "GET", "/1.0/instances/"+url.PathEscape(name), nil, &cur); err != nil {
			return err
		}
		if devs, ok := cur["devices"].(map[string]any); ok {
			delete(devs, dev)
		}
		put := map[string]any{}
		for _, k := range []string{"architecture", "config", "devices", "ephemeral", "profiles", "stateful", "description"} {
			if v, ok := cur[k]; ok {
				put[k] = v
			}
		}
		e, err := c.do(ctx, "PUT", "/1.0/instances/"+url.PathEscape(name), put, nil)
		if err != nil {
			return err
		}
		return c.wait(ctx, e)
	}
	d := map[string]string{"type": "proxy", "listen": fmt.Sprintf("%s:0.0.0.0:%d", proto, hostPort)}
	if vm {
		if ip == "" {
			return &Error{Status: 409, Message: "the virtual machine has no address yet"}
		}
		d["nat"], d["connect"] = "true", fmt.Sprintf("%s:%s:%d", proto, ip, targetPort)
		d["listen"] = fmt.Sprintf("%s:%s:%d", proto, hostIP(), hostPort)
	} else {
		d["connect"] = fmt.Sprintf("%s:127.0.0.1:%d", proto, targetPort)
	}
	return c.patch(ctx, name, map[string]any{"devices": map[string]any{dev: d}})
}

func hostIP() string { return agent.LANIP() }

// ImportBackup creates an instance from an `incus export` tarball read from r, streamed to
// incusd as it arrives (nothing is kept on disk first). (Server migration, R6.)
func ImportBackup(ctx context.Context, name string, r io.Reader) error {
	if !nameRe.MatchString(name) {
		return errs.New(400, "vms.bad_name")
	}
	hc := &http.Client{Transport: incusTransport()}
	req, err := http.NewRequestWithContext(ctx, "POST", "http://incus/1.0/instances", r)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("X-Incus-name", name)
	resp, err := hc.Do(req)
	if err != nil {
		return &Error{Status: 503, Message: "Incus: " + err.Error()}
	}
	defer resp.Body.Close()
	var e envelope
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&e); err != nil {
		return &Error{Status: resp.StatusCode, Message: fmt.Sprintf("Incus: HTTP %d", resp.StatusCode)}
	}
	if e.Type == "error" {
		return &Error{Status: e.ErrorCode, Message: e.Error}
	}
	return New().wait(ctx, &e)
}
