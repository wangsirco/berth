package pve

// Fake is a small Proxmox VE API for tests (there is no PVE on the dev machine): two nodes,
// a few guests, token auth, tasks (UPIDs) that take a moment, snapshots, guest addresses and
// a console websocket that speaks the first line of RFB after checking the VNC ticket.

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

type fakeGuest struct {
	Guest
	snaps []Snapshot
	ip    string
	agent bool
}

type Fake struct {
	mu      sync.Mutex
	token   string // "user@realm!id=secret"
	guests  map[int]*fakeGuest
	tasks   map[string]time.Time // upid → done at
	failing map[string]bool
	tickets map[string]bool
	n       int
	Calls   []string // "POST /nodes/pve1/qemu/100/status/start"
}

func NewFake(tokenID, secret string) *Fake {
	f := &Fake{token: tokenID + "=" + secret, guests: map[int]*fakeGuest{}, tasks: map[string]time.Time{}, failing: map[string]bool{}, tickets: map[string]bool{}}
	add := func(g Guest, ip string, agent bool) {
		f.guests[g.VMID] = &fakeGuest{Guest: g, ip: ip, agent: agent}
	}
	add(Guest{VMID: 100, Name: "web", Node: "pve1", Type: "qemu", Status: "running", MaxCPU: 2, Mem: 1 << 30, MaxMem: 4 << 30, MaxDisk: 32 << 30, Uptime: 86400, CPU: 0.07, Tags: "prod"}, "10.0.9.100", true)
	add(Guest{VMID: 101, Name: "db", Node: "pve1", Type: "lxc", Status: "stopped", MaxCPU: 1, MaxMem: 1 << 30, MaxDisk: 8 << 30}, "10.0.9.101", false)
	add(Guest{VMID: 200, Name: "win11", Node: "pve2", Type: "qemu", Status: "stopped", MaxCPU: 4, MaxMem: 8 << 30, MaxDisk: 64 << 30}, "", false)
	f.guests[100].snaps = []Snapshot{{Name: "before-upgrade", Description: "pre 2.0", SnapTime: time.Now().Add(-48 * time.Hour).Unix()}}
	return f
}

func (f *Fake) task(node string) string {
	f.n++
	upid := fmt.Sprintf("UPID:%s:%08X:00000000:%08X:fake:%d:berth@pve!panel:", node, f.n, time.Now().Unix(), f.n)
	f.tasks[upid] = time.Now().Add(400 * time.Millisecond)
	return upid
}

func (f *Fake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api2/json")
	if r.Header.Get("Authorization") != "PVEAPIToken="+f.token {
		w.WriteHeader(401)
		w.Write([]byte(`{"data":null}`))
		return
	}
	f.mu.Lock()
	f.Calls = append(f.Calls, r.Method+" "+path)
	f.mu.Unlock()
	r.ParseForm()
	seg := strings.Split(strings.Trim(path, "/"), "/")
	ok := func(v any) { w.Header().Set("Content-Type", "application/json"); w.Write(jsonBody(v)) }
	fail := func(code int, msg string) { w.WriteHeader(code); fmt.Fprintf(w, `{"data":null,"message":%q}`, msg) }

	switch {
	case path == "/version":
		ok(Version{Version: "9.1.2", Release: "9.1"})
		return
	case path == "/nodes":
		ok([]Node{{Node: "pve1", Status: "online", CPU: 0.12, MaxCPU: 16, Mem: 20 << 30, MaxMem: 64 << 30, Disk: 200 << 30, MaxDisk: 900 << 30, Uptime: 3600 * 24 * 30},
			{Node: "pve2", Status: "online", CPU: 0.03, MaxCPU: 8, Mem: 6 << 30, MaxMem: 32 << 30, Disk: 80 << 30, MaxDisk: 450 << 30, Uptime: 3600 * 5}})
		return
	case path == "/cluster/resources":
		f.mu.Lock()
		var out []Guest
		for _, g := range f.guests {
			out = append(out, g.Guest)
		}
		f.mu.Unlock()
		ok(out)
		return
	case len(seg) == 5 && seg[0] == "nodes" && seg[2] == "tasks" && seg[4] == "status":
		f.mu.Lock()
		at, known := f.tasks[seg[3]]
		failed := f.failing[seg[3]]
		f.mu.Unlock()
		if !known {
			fail(404, "no such task")
			return
		}
		if time.Now().Before(at) {
			ok(map[string]any{"status": "running"})
		} else if failed {
			ok(map[string]any{"status": "stopped", "exitstatus": "command failed"})
		} else {
			ok(map[string]any{"status": "stopped", "exitstatus": "OK"})
		}
		return
	}
	// /nodes/{node}/{type}/{vmid}/...
	if len(seg) < 4 || seg[0] != "nodes" {
		fail(501, "not implemented in fake: "+path)
		return
	}
	id, _ := strconv.Atoi(seg[3])
	f.mu.Lock()
	g := f.guests[id]
	f.mu.Unlock()
	if g == nil || g.Node != seg[1] || g.Type != seg[2] {
		fail(500, fmt.Sprintf("Configuration file 'nodes/%s/%s/%d.conf' does not exist", seg[1], seg[2], id))
		return
	}
	rest := strings.Join(seg[4:], "/")
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case r.Method == "GET" && rest == "config":
		c := map[string]any{"cores": int(g.MaxCPU), "memory": g.MaxMem >> 20, "name": g.Name, "ostype": map[string]string{"qemu": "l26", "lxc": "debian"}[g.Type]}
		if g.Type == "qemu" {
			c["net0"] = "virtio=BC:24:11:00:00:01,bridge=vmbr0"
			c["scsi0"] = fmt.Sprintf("local-lvm:vm-%d-disk-0,size=%dG", id, g.MaxDisk>>30)
			c["agent"] = map[bool]string{true: "1", false: "0"}[g.agent]
		} else {
			c["net0"] = "name=eth0,bridge=vmbr0,ip=dhcp"
			c["rootfs"] = fmt.Sprintf("local-lvm:vm-%d-disk-0,size=%dG", id, g.MaxDisk>>30)
		}
		ok(c)
	case r.Method == "POST" && strings.HasPrefix(rest, "status/"):
		a := strings.TrimPrefix(rest, "status/")
		switch a {
		case "start", "resume":
			g.Status = "running"
			g.Uptime = 1
		case "stop", "shutdown":
			g.Status, g.Uptime, g.CPU, g.Mem = "stopped", 0, 0, 0
		case "reboot":
			if g.Status != "running" {
				fail(500, "VM "+seg[3]+" not running")
				return
			}
		case "suspend":
			g.Status = "paused"
		default:
			fail(501, "unknown action")
			return
		}
		ok(f.task(g.Node))
	case r.Method == "GET" && rest == "snapshot":
		out := append([]Snapshot{}, g.snaps...)
		out = append(out, Snapshot{Name: "current", Description: "You are here!"})
		ok(out)
	case r.Method == "POST" && rest == "snapshot":
		name := r.PostForm.Get("snapname")
		for _, s := range g.snaps {
			if s.Name == name {
				fail(500, "snapshot name '"+name+"' already used")
				return
			}
		}
		g.snaps = append(g.snaps, Snapshot{Name: name, Description: r.PostForm.Get("description"), SnapTime: time.Now().Unix(), VMState: map[bool]int{true: 1}[r.PostForm.Get("vmstate") == "1"]})
		ok(f.task(g.Node))
	case r.Method == "DELETE" && strings.HasPrefix(rest, "snapshot/"):
		name := strings.TrimPrefix(rest, "snapshot/")
		for i, s := range g.snaps {
			if s.Name == name {
				g.snaps = append(g.snaps[:i], g.snaps[i+1:]...)
				ok(f.task(g.Node))
				return
			}
		}
		fail(500, "snapshot '"+name+"' does not exist")
	case r.Method == "POST" && strings.HasPrefix(rest, "snapshot/") && strings.HasSuffix(rest, "/rollback"):
		name := strings.TrimSuffix(strings.TrimPrefix(rest, "snapshot/"), "/rollback")
		for _, s := range g.snaps {
			if s.Name == name {
				g.Status = "stopped" // a rollback without RAM state leaves it off
				ok(f.task(g.Node))
				return
			}
		}
		fail(500, "snapshot '"+name+"' does not exist")
	case r.Method == "GET" && rest == "agent/network-get-interfaces":
		if !g.agent || g.Status != "running" {
			fail(500, "QEMU guest agent is not running")
			return
		}
		ok(map[string]any{"result": []any{
			map[string]any{"name": "lo", "ip-addresses": []any{map[string]string{"ip-address": "127.0.0.1"}}},
			map[string]any{"name": "eth0", "ip-addresses": []any{map[string]string{"ip-address": g.ip}, map[string]string{"ip-address": "fe80::1"}}},
		}})
	case r.Method == "GET" && rest == "interfaces":
		if g.Status != "running" {
			fail(500, "CT is not running")
			return
		}
		ok([]map[string]string{{"name": "lo", "inet": "127.0.0.1/8"}, {"name": "eth0", "inet": g.ip + "/24"}})
	case r.Method == "POST" && rest == "vncproxy":
		if g.Status != "running" {
			fail(500, "VM "+seg[3]+" not running")
			return
		}
		f.n++
		t := fmt.Sprintf("PVEVNC:%08X::fake%d", time.Now().Unix(), f.n)
		f.tickets[t] = true
		ok(map[string]any{"port": strconv.Itoa(5900 + f.n), "ticket": t, "user": "berth@pve!panel"})
	case r.Method == "GET" && rest == "vncwebsocket":
		t := r.URL.Query().Get("vncticket")
		if !f.tickets[t] {
			fail(401, "invalid ticket")
			return
		}
		delete(f.tickets, t)
		f.mu.Unlock()
		defer f.mu.Lock()
		c, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{"binary"}})
		if err != nil {
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
		defer cancel()
		serveRFB(websocket.NetConn(ctx, c, websocket.MessageBinary), g.Name)
	default:
		fail(501, "not implemented in fake: "+r.Method+" "+path)
	}
}

// GuestStatus reports a guest's power state (tests).
func (f *Fake) GuestStatus(vmid int) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if g := f.guests[vmid]; g != nil {
		return g.Status
	}
	return ""
}

// serveRFB is just enough of a VNC server (RFB 3.8, VNC auth accepted as is) for noVNC to
// connect and draw one frame: a colour band with the guest's name in the desktop title.
func serveRFB(c net.Conn, name string) {
	defer c.Close()
	const w, h = 640, 400
	c.Write([]byte("RFB 003.008\n"))
	ver := make([]byte, 12)
	if _, err := io.ReadFull(c, ver); err != nil {
		return
	}
	c.Write([]byte{1, 2}) // one security type: VNC authentication
	sec := make([]byte, 1)
	if _, err := io.ReadFull(c, sec); err != nil {
		return
	}
	c.Write(make([]byte, 16)) // the challenge
	resp := make([]byte, 16)
	if _, err := io.ReadFull(c, resp); err != nil {
		return
	}
	c.Write([]byte{0, 0, 0, 0}) // security result: OK
	shared := make([]byte, 1)
	if _, err := io.ReadFull(c, shared); err != nil {
		return
	}
	init := make([]byte, 24)
	binary.BigEndian.PutUint16(init[0:], w)
	binary.BigEndian.PutUint16(init[2:], h)
	copy(init[4:], []byte{32, 24, 0, 1, 0, 255, 0, 255, 0, 255, 16, 8, 0, 0, 0, 0}) // 32 bpp true colour
	title := "fake PVE · " + name
	binary.BigEndian.PutUint32(init[20:], uint32(len(title)))
	c.Write(append(init, title...))
	frame := func() {
		hdr := make([]byte, 16)
		hdr[0] = 0 // FramebufferUpdate
		binary.BigEndian.PutUint16(hdr[2:], 1)
		binary.BigEndian.PutUint16(hdr[8:], w)
		binary.BigEndian.PutUint16(hdr[10:], h)
		px := make([]byte, w*h*4) // raw, in the client's format: R G B x (noVNC asks for that)
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				i := (y*w + x) * 4
				px[i], px[i+1], px[i+2] = byte(20+x*200/w), byte(60+y*120/h), 160
			}
		}
		c.Write(append(hdr, px...))
	}
	sent := false
	b := make([]byte, 1)
	for {
		if _, err := io.ReadFull(c, b); err != nil {
			return
		}
		skip := map[byte]int{0: 19, 4: 7, 5: 5}[b[0]]
		switch b[0] {
		case 2: // SetEncodings
			hd := make([]byte, 3)
			if _, err := io.ReadFull(c, hd); err != nil {
				return
			}
			skip = 4 * int(binary.BigEndian.Uint16(hd[1:]))
		case 3: // FramebufferUpdateRequest
			skip = 9
		case 6: // ClientCutText
			hd := make([]byte, 7)
			if _, err := io.ReadFull(c, hd); err != nil {
				return
			}
			skip = int(binary.BigEndian.Uint32(hd[3:]))
		}
		if _, err := io.CopyN(io.Discard, c, int64(skip)); err != nil {
			return
		}
		if b[0] == 3 && !sent {
			sent = true
			frame()
		}
	}
}
