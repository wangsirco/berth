// agentcall signs and sends one request to a remote berth agent through an SSH-forwarded
// unix socket. Development tool only; not shipped.
//
//	go run ./scripts/agentcall -host root@192.0.2.10 GET /v1/docker/containers
//	go run ./scripts/agentcall -host root@192.0.2.10 POST /v1/compose/projects/x/up '{}'
//	(on the server) agentcall -host local GET /v1/ping
package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/wangsirco/berth/internal/agentproto"
)

func main() {
	host := flag.String("host", "root@192.0.2.10", "ssh destination (\"local\": run on the server itself)")
	flag.Parse()
	if *host == "local" {
		runLocal()
		return
	}
	if flag.NArg() < 2 {
		fmt.Fprintln(os.Stderr, "usage: agentcall [-host user@h] METHOD PATH [JSON]")
		os.Exit(2)
	}
	keyJSON, err := exec.Command("ssh", *host, "cat /etc/berth/panel-agent.key").Output()
	check(err)
	var pk struct {
		KeyID  string `json:"key_id"`
		Secret string `json:"secret"`
		Socket string `json:"socket"`
	}
	check(json.Unmarshal(keyJSON, &pk))
	secret, err := base64.StdEncoding.DecodeString(pk.Secret)
	check(err)
	dir, _ := os.MkdirTemp("", "ac")
	defer os.RemoveAll(dir)
	local := filepath.Join(dir, "a.sock")
	fwd := exec.Command("ssh", "-N", "-o", "ExitOnForwardFailure=yes", "-L", local+":"+pk.Socket, *host)
	check(fwd.Start())
	defer fwd.Process.Kill()
	for i := 0; i < 50; i++ {
		if _, err := os.Stat(local); err == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	var in any
	if flag.NArg() > 2 {
		check(json.Unmarshal([]byte(flag.Arg(2)), &in))
	}
	c := agentproto.NewClient(local, pk.KeyID, secret)
	rc, err := c.Stream(context.Background(), flag.Arg(0), flag.Arg(1), in)
	check(err)
	defer rc.Close()
	io.Copy(os.Stdout, rc)
}

func check(err error) {
	if err != nil {
		fmt.Fprintf(os.Stderr, "agentcall: %v %#v\n", err, err)
		os.Exit(1)
	}
}

// runLocal is used when agentcall runs on the server (as root): no SSH involved.
func runLocal() {
	b, err := os.ReadFile("/etc/berth/panel-agent.key")
	check(err)
	var pk struct {
		KeyID  string `json:"key_id"`
		Secret string `json:"secret"`
		Socket string `json:"socket"`
	}
	check(json.Unmarshal(b, &pk))
	secret, err := base64.StdEncoding.DecodeString(pk.Secret)
	check(err)
	var in any
	if flag.NArg() > 2 {
		check(json.Unmarshal([]byte(flag.Arg(2)), &in))
	}
	rc, err := agentproto.NewClient(pk.Socket, pk.KeyID, secret).Stream(context.Background(), flag.Arg(0), flag.Arg(1), in)
	check(err)
	defer rc.Close()
	io.Copy(os.Stdout, rc)
}
