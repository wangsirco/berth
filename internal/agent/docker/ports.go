package docker

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
)

// ProcRoot is /proc; tests point it elsewhere.
var ProcRoot = "/proc"

type Listen struct {
	Port     int    `json:"port"`
	Proto    string `json:"proto"`
	Loopback bool   `json:"loopback"` // only on 127.0.0.1/::1: other containers cannot reach it
}

// parseProcNet reads LISTEN sockets (state 0A) from /proc/<pid>/net/tcp{,6}.
func parseProcNet(r io.Reader, proto string) []Listen {
	var out []Listen
	sc := bufio.NewScanner(r)
	sc.Scan() // header
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 4 || f[3] != "0A" {
			continue
		}
		addr, portHex, ok := strings.Cut(f[1], ":")
		if !ok {
			continue
		}
		port, err := strconv.ParseUint(portHex, 16, 16)
		if err != nil {
			continue
		}
		loop := isLoopbackHex(addr)
		out = append(out, Listen{Port: int(port), Proto: proto, Loopback: loop})
	}
	return out
}

// Listening lists ports a running container actually listens on. /proc/<pid>/net/*
// shows the network namespace of that process, so no setns is needed.
func (c *Client) Listening(ctx context.Context, id string) ([]Listen, error) {
	d, err := c.Inspect(ctx, id)
	if err != nil {
		return nil, err
	}
	if d.Pid == 0 {
		return []Listen{}, nil
	}
	byPort := map[string]Listen{}
	for _, f := range []string{"tcp", "tcp6"} {
		fh, err := os.Open(fmt.Sprintf("%s/%d/net/%s", ProcRoot, d.Pid, f))
		if err != nil {
			continue
		}
		for _, l := range parseProcNet(fh, "tcp") {
			key := strconv.Itoa(l.Port)
			if prev, ok := byPort[key]; ok && !prev.Loopback {
				continue // a wildcard listener wins over a loopback one
			}
			byPort[key] = l
		}
		fh.Close()
	}
	out := make([]Listen, 0, len(byPort))
	for _, l := range byPort {
		out = append(out, l)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Port < out[j].Port })
	return out, nil
}

// isLoopbackHex: /proc/net/tcp stores IPv4 little-endian, so 127.x.x.x ends in "7F"
// (this covers Docker's embedded DNS on 127.0.0.11); IPv6 ::1 is a fixed pattern.
func isLoopbackHex(addr string) bool {
	if len(addr) == 8 {
		return strings.HasSuffix(addr, "7F")
	}
	return addr == "00000000000000000000000001000000"
}
