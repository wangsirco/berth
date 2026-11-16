package firewall

import (
	"bufio"
	"encoding/hex"
	"io"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Listener is one listening socket in the host's network namespace.
type Listener struct {
	Proto   string `json:"proto"` // tcp | udp
	Addr    string `json:"addr"`  // bind address
	Port    int    `json:"port"`
	Public  bool   `json:"public"` // not bound to loopback only
	Process string `json:"process,omitempty"`
	PID     int    `json:"pid,omitempty"`
}

// ProcRoot is /proc (a variable for tests).
var ProcRoot = "/proc"

// Listening lists TCP listeners and bound UDP sockets, with the owning process.
func Listening() []Listener {
	var out []Listener
	inodes := map[string]int{} // socket inode -> index in out
	for _, f := range []struct{ file, proto string }{{"net/tcp", "tcp"}, {"net/tcp6", "tcp"}, {"net/udp", "udp"}, {"net/udp6", "udp"}} {
		r, err := os.Open(filepath.Join(ProcRoot, f.file))
		if err != nil {
			continue
		}
		for _, l := range parse(r, f.proto) {
			inodes[l.inode] = len(out)
			out = append(out, l.Listener)
		}
		r.Close()
	}
	owners(inodes, out)
	// one row per proto/addr/port (IPv4 and IPv6 sockets of the same service)
	seen := map[string]bool{}
	dedup := out[:0]
	for _, l := range out {
		k := l.Proto + "/" + strconv.Itoa(l.Port) + "/" + strconv.FormatBool(l.Public) + "/" + l.Process
		if !seen[k] {
			seen[k] = true
			dedup = append(dedup, l)
		}
	}
	sort.Slice(dedup, func(i, j int) bool {
		if dedup[i].Port != dedup[j].Port {
			return dedup[i].Port < dedup[j].Port
		}
		return dedup[i].Proto < dedup[j].Proto
	})
	return dedup
}

type rawListener struct {
	Listener
	inode string
}

func parse(r io.Reader, proto string) []rawListener {
	var out []rawListener
	sc := bufio.NewScanner(r)
	sc.Scan()
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 10 {
			continue
		}
		// TCP: only LISTEN (0A); UDP: unconnected sockets (07) with a local port
		if (proto == "tcp" && f[3] != "0A") || (proto == "udp" && f[3] != "07") {
			continue
		}
		addrHex, portHex, ok := strings.Cut(f[1], ":")
		if !ok {
			continue
		}
		port, err := strconv.ParseUint(portHex, 16, 16)
		if err != nil || port == 0 {
			continue
		}
		ip := hexIP(addrHex)
		out = append(out, rawListener{Listener: Listener{Proto: proto, Addr: ip.String(), Port: int(port), Public: !ip.IsLoopback()}, inode: f[9]})
	}
	return out
}

// hexIP decodes /proc/net addresses (little-endian 32-bit words).
func hexIP(s string) net.IP {
	b, err := hex.DecodeString(s)
	if err != nil {
		return net.IPv4zero
	}
	for i := 0; i+4 <= len(b); i += 4 {
		b[i], b[i+1], b[i+2], b[i+3] = b[i+3], b[i+2], b[i+1], b[i]
	}
	return net.IP(b)
}

// owners finds which process holds each socket inode.
func owners(inodes map[string]int, out []Listener) {
	if len(inodes) == 0 {
		return
	}
	procs, _ := os.ReadDir(ProcRoot)
	for _, p := range procs {
		pid, err := strconv.Atoi(p.Name())
		if err != nil {
			continue
		}
		fdDir := filepath.Join(ProcRoot, p.Name(), "fd")
		fds, err := os.ReadDir(fdDir)
		if err != nil {
			continue
		}
		for _, fd := range fds {
			link, err := os.Readlink(filepath.Join(fdDir, fd.Name()))
			if err != nil || !strings.HasPrefix(link, "socket:[") {
				continue
			}
			ino := strings.TrimSuffix(strings.TrimPrefix(link, "socket:["), "]")
			if i, ok := inodes[ino]; ok && out[i].PID == 0 {
				out[i].PID = pid
				if b, err := os.ReadFile(filepath.Join(ProcRoot, p.Name(), "comm")); err == nil {
					out[i].Process = strings.TrimSpace(string(b))
				}
			}
		}
	}
}
