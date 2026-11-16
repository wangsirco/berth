// guacprobe connects to guacd like the panel does and reports what the remote screen
// sends (dev tool: is a desktop actually drawing?).
package main

import (
	"context"
	"encoding/base64"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/wangsirco/berth/internal/panel/guac"
)

func main() {
	proto := flag.String("proto", "rdp", "rdp | vnc")
	host := flag.String("host", "127.0.0.1", "")
	port := flag.String("port", "3389", "")
	user := flag.String("user", "", "")
	pass := flag.String("pass", "", "")
	secs := flag.Int("t", 8, "seconds to watch")
	flag.Parse()
	ctx := context.Background()
	args := map[string]string{"hostname": *host, "port": *port, "username": *user, "password": *pass, "ignore-cert": "true", "security": "any", "resize-method": "display-update"}
	conn, rd, id, err := guac.Handshake(ctx, "127.0.0.1:4822", guac.Params{Protocol: *proto, Args: args, Width: 1280, Height: 800})
	if err != nil {
		fmt.Println("handshake:", err)
		os.Exit(1)
	}
	fmt.Println("ready", id)
	counts := map[string]int{}
	streams := map[string][]byte{}
	mimes := map[string]string{}
	bytes := 0
	var best []byte
	bestMime := ""
	conn.SetReadDeadline(time.Now().Add(time.Duration(*secs) * time.Second))
	for {
		raw, els, err := rd.Read()
		if err != nil {
			break
		}
		bytes += len(raw)
		counts[els[0]]++
		if els[0] == "error" {
			fmt.Println("error:", strings.Join(els[1:], " | "))
		}
		if els[0] == "img" && len(els) > 3 {
			mimes[els[1]] = els[3]
			streams[els[1]] = nil
		}
		if els[0] == "blob" && len(els) > 2 {
			b, _ := base64.StdEncoding.DecodeString(els[2])
			streams[els[1]] = append(streams[els[1]], b...)
		}
		if els[0] == "end" && len(els) > 1 {
			if b := streams[els[1]]; len(b) > len(best) {
				best, bestMime = b, mimes[els[1]]
			}
		}
		if els[0] == "sync" {
			conn.Write([]byte(guac.Encode("sync", els[1])))
		}
	}
	fmt.Println("bytes", bytes, counts)
	if len(best) > 0 {
		ext := strings.TrimPrefix(bestMime, "image/")
		os.WriteFile("/root/frame."+ext, best, 0o644)
		fmt.Println("largest image", len(best), bestMime)
	}
}
