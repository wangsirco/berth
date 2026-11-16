package guac

import (
	"context"
	"io"
	"net"
	"strings"
	"testing"
)

func TestEncodeParse(t *testing.T) {
	s := Encode("select", "rdp") + Encode("clipboard", "你好, world;") + Encode("", "ping", "123")
	if !strings.HasPrefix(s, "6.select,3.rdp;9.clipboard,10.你好, world;;") {
		t.Fatal(s)
	}
	ins, err := Parse(s)
	if err != nil || len(ins) != 3 || ins[1][1] != "你好, world;" || ins[2][0] != "" || ins[2][1] != "ping" {
		t.Fatalf("%q %v", ins, err)
	}
	if _, err := Parse("4.size,x.1;"); err == nil {
		t.Fatal("bad length accepted")
	}
}

// fake guacd: answers select with args, checks connect values, says ready
func TestHandshake(t *testing.T) {
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	defer l.Close()
	got := make(chan []string, 1)
	go func() {
		c, _ := l.Accept()
		defer c.Close()
		rd := NewReader(c)
		_, sel, _ := rd.Read()
		if sel[1] != "rdp" {
			return
		}
		io.WriteString(c, Encode("args", "VERSION_1_5_0", "hostname", "port", "username", "password", "ignore-cert"))
		for {
			_, ins, err := rd.Read()
			if err != nil {
				return
			}
			if ins[0] == "connect" {
				got <- ins
				io.WriteString(c, Encode("ready", "$abc"))
				io.WriteString(c, Encode("sync", "1"))
				return
			}
		}
	}()
	conn, rd, id, err := Handshake(context.Background(), l.Addr().String(), Params{Protocol: "rdp",
		Args: map[string]string{"hostname": "10.42.231.11", "port": "3389", "username": "admin", "password": "p,w;", "ignore-cert": "true"}})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	c := <-got
	if id != "$abc" || strings.Join(c[1:], "|") != "VERSION_1_5_0|10.42.231.11|3389|admin|p,w;|true" {
		t.Fatalf("%q %q", id, c)
	}
	raw, ins, err := rd.Read()
	if err != nil || ins[0] != "sync" || raw != "4.sync,1.1;" {
		t.Fatal(raw, err)
	}
}
