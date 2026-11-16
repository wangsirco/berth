package docker

import (
	"strings"
	"testing"
)

const procNetTCP = `  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 00000000:162E 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 1 1 0000000000000000 100 0 0 10 0
   1: 0100007F:0CEA 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 2 1 0000000000000000 100 0 0 10 0
   3: 0B00007F:887D 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 4 1 0000000000000000 100 0 0 10 0
   2: 0200A8C0:D2F0 0300A8C0:01BB 01 00000000:00000000 00:00000000 00000000  1000        0 3 1 0000000000000000 100 0 0 10 0
`

func TestParseListen(t *testing.T) {
	got := parseProcNet(strings.NewReader(procNetTCP), "tcp")
	if len(got) != 3 || got[0].Port != 5678 || got[0].Loopback || got[1].Port != 3306 || !got[1].Loopback || !got[2].Loopback {
		t.Fatalf("%+v", got)
	}
}
