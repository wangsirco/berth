package proxy

import (
	"os"
	"testing"
)

func TestTailAccess(t *testing.T) {
	LogDir = t.TempDir()
	logOff = 0
	os.WriteFile(LogDir+"/access.log", []byte(`2026/10/24 22:35:03.798918 from 1.2.3.4:54358 accepted tcp:www.cloudflare.com:443 [n1 >> direct] email: u1@n1
2026/10/24 22:35:04.1 from 1.2.3.4:54359 accepted tcp:www.cloudflare.com:443 [n1 >> direct] email: u1@n1
2026/10/24 22:35:05 from 1.2.3.4:5 accepted tcp:192.168.1.1:80 [n1 >> blocked] email: u1@n1
2026/10/24 22:35:06 from 127.0.0.1:1 accepted tcp:127.0.0.1:20085 [api -> api]
`), 0o644)
	got := tailAccess(func(string) string { return "NZ" })
	if len(got) != 2 || got[0].Count != 2 || got[0].Dest != "www.cloudflare.com" || got[0].Port != 443 || got[0].Country != "NZ" || got[1].Status != "blocked" {
		t.Fatalf("%+v", got)
	}
	if again := tailAccess(func(string) string { return "" }); len(again) != 0 {
		t.Fatalf("read twice: %+v", again)
	}
}
