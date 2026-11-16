// Package flows says who each container and virtual machine talks to (方案 §5.12): the
// kernel's connection table, with every address mapped back to the object that owns it.
// Each caller (a panel) gets what changed since its own previous read, so two panels
// sharing this agent each see whole numbers.
package flows

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/wangsirco/berth/internal/agent/docker"
	"github.com/wangsirco/berth/internal/agent/incus"
	"github.com/wangsirco/berth/internal/agent/traffic"
)

// Row is one object's traffic with one peer on one port since the previous read.
type Row struct {
	Obj     string `json:"obj"`  // container:NAME | vm:NAME
	Dir     string `json:"dir"`  // out (the object opened it) | in (someone reached the object)
	Peer    string `json:"peer"` // an address, or another object (container:NAME / vm:NAME), or "host"
	Proto   string `json:"proto"`
	Port    int    `json:"port"` // the port that was connected to
	Conns   int    `json:"conns"`
	Bytes   uint64 `json:"bytes"`
	Country string `json:"country,omitempty"`
	PTR     string `json:"ptr,omitempty"` // reverse DNS of the peer
}

type state struct {
	last map[string]uint64 // connection key -> bytes seen
	seen time.Time
}

type Reader struct {
	dc  *docker.Client
	ic  *incus.Client
	Geo func(net.IP) string
	// Owners replaces the Docker/Incus lookup (tests)
	Owners func(ctx context.Context) (map[string]string, map[string]bool)

	mu        sync.Mutex
	consumers map[string]*state
	ptr       map[string]ptrEntry
}

type ptrEntry struct {
	name string
	at   time.Time
}

func New(dc *docker.Client, ic *incus.Client) *Reader {
	return &Reader{dc: dc, ic: ic, consumers: map[string]*state{}, ptr: map[string]ptrEntry{}}
}

// owners maps addresses to objects: containers on every Docker network, Incus instances.
func (r *Reader) owners(ctx context.Context) (map[string]string, map[string]bool) {
	own := map[string]string{}
	gateways := map[string]bool{}
	if r.dc != nil {
		if cs, err := r.dc.Containers(ctx); err == nil {
			for _, c := range cs {
				for _, ip := range c.IPs {
					if ip != "" {
						own[ip] = "container:" + c.Name
					}
				}
			}
		}
	}
	if r.ic != nil {
		if ins, err := r.ic.Instances(ctx); err == nil {
			for _, in := range ins {
				for _, ip := range in.IPv4 {
					own[ip] = "vm:" + in.Name
				}
			}
		}
	}
	addrs, _ := net.InterfaceAddrs()
	for _, a := range addrs {
		if ip, _, err := net.ParseCIDR(a.String()); err == nil {
			gateways[ip.String()] = true // this server's own addresses (bridge gateways included)
		}
	}
	return own, gateways
}

// Read returns the rows since this consumer's last read (nothing on its first: a baseline).
func (r *Reader) Read(ctx context.Context, consumer string) ([]Row, error) {
	f, err := os.Open(filepath.Join(traffic.ProcRoot, "net", "nf_conntrack"))
	if err != nil {
		return nil, err
	}
	conns := traffic.ParseConntrack(f)
	f.Close()
	owners := r.owners
	if r.Owners != nil {
		owners = r.Owners
	}
	own, local := owners(ctx)

	r.mu.Lock()
	st := r.consumers[consumer]
	first := st == nil
	if first {
		st = &state{}
		r.consumers[consumer] = st
	}
	prev := st.last
	for k, s := range r.consumers { // forget callers that stopped asking
		if time.Since(s.seen) > 15*time.Minute && k != consumer {
			delete(r.consumers, k)
		}
	}
	r.mu.Unlock()

	next := make(map[string]uint64, len(conns))
	agg := map[string]*Row{}
	add := func(obj, dir, peer, proto string, port int, isNew bool, bytes uint64) {
		k := obj + "|" + dir + "|" + peer + "|" + proto + "|" + strconv.Itoa(port)
		row := agg[k]
		if row == nil {
			row = &Row{Obj: obj, Dir: dir, Peer: peer, Proto: proto, Port: port}
			agg[k] = row
		}
		if isNew {
			row.Conns++
		}
		row.Bytes += bytes
	}
	peerOf := func(ip string) string {
		if o, ok := own[ip]; ok {
			return o
		}
		if local[ip] {
			return "host"
		}
		return ip
	}
	for _, c := range conns {
		srcObj := own[c.Src]
		// a published port: the original destination is this server, the reply comes from the object
		dstObj := own[c.Dst]
		if dstObj == "" && c.ReplySrc != "" && c.ReplySrc != c.Dst {
			dstObj = own[c.ReplySrc]
		}
		if srcObj == "" && dstObj == "" {
			continue
		}
		key := c.Proto + "|" + c.Src + "|" + strconv.Itoa(c.Sport) + "|" + c.Dst + "|" + strconv.Itoa(c.Dport)
		total := c.BytesOrig + c.BytesReply
		next[key] = total
		last, seen := prev[key]
		if first {
			continue
		}
		delta := total
		if seen && total >= last {
			delta = total - last
		}
		if seen && delta == 0 {
			continue
		}
		if srcObj != "" && srcObj != dstObj {
			add(srcObj, "out", peerOf(c.Dst), c.Proto, c.Dport, !seen, delta)
		}
		if dstObj != "" && dstObj != srcObj {
			add(dstObj, "in", peerOf(c.Src), c.Proto, c.Dport, !seen, delta)
		}
	}
	r.mu.Lock()
	st.last, st.seen = next, time.Now()
	r.mu.Unlock()

	out := make([]Row, 0, len(agg))
	for _, row := range agg {
		out = append(out, *row)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Bytes > out[j].Bytes })
	r.enrich(ctx, out)
	return out, nil
}

// enrich adds country and reverse DNS for outside addresses (a few new lookups per read).
func (r *Reader) enrich(ctx context.Context, rows []Row) {
	lookups := 0
	for i := range rows {
		ip := net.ParseIP(rows[i].Peer)
		if ip == nil || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
			continue
		}
		if r.Geo != nil {
			rows[i].Country = r.Geo(ip)
		}
		r.mu.Lock()
		e, ok := r.ptr[rows[i].Peer]
		r.mu.Unlock()
		if !ok || time.Since(e.at) > 24*time.Hour {
			if lookups >= 12 {
				continue
			}
			lookups++
			c, cancel := context.WithTimeout(ctx, 400*time.Millisecond)
			names, _ := net.DefaultResolver.LookupAddr(c, rows[i].Peer)
			cancel()
			e = ptrEntry{at: time.Now()}
			if len(names) > 0 {
				e.name = trimDot(names[0])
			}
			r.mu.Lock()
			if len(r.ptr) > 20000 {
				r.ptr = map[string]ptrEntry{}
			}
			r.ptr[rows[i].Peer] = e
			r.mu.Unlock()
		}
		rows[i].PTR = e.name
	}
}

func trimDot(s string) string {
	if len(s) > 0 && s[len(s)-1] == '.' {
		return s[:len(s)-1]
	}
	return s
}
