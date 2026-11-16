package flows

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/wangsirco/berth/internal/agent/traffic"
)

const line = "ipv4     2 tcp      6 431999 ESTABLISHED src=%s dst=%s sport=%d dport=%d packets=1 bytes=%d src=%s dst=%s sport=%d dport=%d packets=1 bytes=%d [ASSURED] mark=0 use=1\n"

func write(t *testing.T, dir string, lines ...string) {
	os.MkdirAll(filepath.Join(dir, "net"), 0o755)
	b := ""
	for _, l := range lines {
		b += l
	}
	if err := os.WriteFile(filepath.Join(dir, "net", "nf_conntrack"), []byte(b), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestDeltasPerConsumer(t *testing.T) {
	dir := t.TempDir()
	old := traffic.ProcRoot
	traffic.ProcRoot = dir
	defer func() { traffic.ProcRoot = old }()
	r := New(nil, nil)
	r.Owners = func(context.Context) (map[string]string, map[string]bool) {
		return map[string]string{"172.18.0.5": "container:app", "10.42.0.9": "vm:dev"}, map[string]bool{"192.0.2.10": true}
	}
	// app → 1.1.1.1:443 (out), 203.0.113.7 → published 8080 → app:80 (in), dev → app:5432 (out for dev, in for app)
	write(t, dir,
		fmt.Sprintf(line, "172.18.0.5", "1.1.1.1", 40000, 443, 100, "1.1.1.1", "192.0.2.10", 443, 40000, 900),
		fmt.Sprintf(line, "203.0.113.7", "192.0.2.10", 51000, 8080, 50, "172.18.0.5", "203.0.113.7", 80, 51000, 450),
		fmt.Sprintf(line, "10.42.0.9", "172.18.0.5", 52000, 5432, 10, "172.18.0.5", "10.42.0.9", 5432, 52000, 20))
	if rows, _ := r.Read(context.Background(), "a"); len(rows) != 0 {
		t.Fatalf("first read is a baseline: %+v", rows)
	}
	// later: the first connection moved 1000 more bytes, a new one appeared
	write(t, dir,
		fmt.Sprintf(line, "172.18.0.5", "1.1.1.1", 40000, 443, 600, "1.1.1.1", "192.0.2.10", 443, 40000, 1400),
		fmt.Sprintf(line, "203.0.113.7", "192.0.2.10", 51000, 8080, 50, "172.18.0.5", "203.0.113.7", 80, 51000, 450),
		fmt.Sprintf(line, "10.42.0.9", "172.18.0.5", 52000, 5432, 10, "172.18.0.5", "10.42.0.9", 5432, 52000, 20),
		fmt.Sprintf(line, "172.18.0.5", "8.8.8.8", 41000, 53, 60, "8.8.8.8", "192.0.2.10", 53, 41000, 120))
	rows, err := r.Read(context.Background(), "a")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]Row{}
	for _, x := range rows {
		got[x.Obj+" "+x.Dir+" "+x.Peer] = x
	}
	if x := got["container:app out 1.1.1.1"]; x.Bytes != 1000 || x.Conns != 0 {
		t.Fatalf("delta on a known connection: %+v", rows)
	}
	if x := got["container:app out 8.8.8.8"]; x.Bytes != 180 || x.Conns != 1 || x.Port != 53 {
		t.Fatalf("new connection: %+v", rows)
	}
	if len(rows) != 2 {
		t.Fatalf("unchanged connections must not show: %+v", rows)
	}
	// another panel starts with its own baseline
	if rows, _ := r.Read(context.Background(), "b"); len(rows) != 0 {
		t.Fatal("second consumer shares state")
	}
	// object-to-object and published-port mapping, seen fresh by a third consumer after a change
	write(t, dir,
		fmt.Sprintf(line, "203.0.113.7", "192.0.2.10", 51000, 8080, 70, "172.18.0.5", "203.0.113.7", 80, 51000, 470),
		fmt.Sprintf(line, "10.42.0.9", "172.18.0.5", 52000, 5432, 30, "172.18.0.5", "10.42.0.9", 5432, 52000, 40))
	rows, _ = r.Read(context.Background(), "b")
	got = map[string]Row{}
	for _, x := range rows {
		got[x.Obj+" "+x.Dir+" "+x.Peer] = x
	}
	if got["container:app in 203.0.113.7"].Bytes != 40 || got["vm:dev out container:app"].Bytes != 40 || got["container:app in vm:dev"].Port != 5432 {
		t.Fatalf("mapping: %+v", rows)
	}
}
