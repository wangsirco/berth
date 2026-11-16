package traffic

import (
	"strings"
	"testing"
)

func TestParseNetDev(t *testing.T) {
	in := `Inter-|   Receive                                                |  Transmit
 face |bytes    packets errs drop fifo frame compressed multicast|bytes    packets errs drop fifo colls carrier compressed
    lo:    4420      64    0    0    0     0          0         0     4420      64    0    0    0     0       0          0
  eth0:  298327    1541    0    0    0     0          0         0   355995    1317    0    0    0     0       0          0
  eth1:    1000      10    0    0    0     0          0         0     2000      20    0    0    0     0       0          0
`
	rx, tx, err := parseNetDev(strings.NewReader(in))
	if err != nil || rx != 299327 || tx != 357995 {
		t.Fatal(rx, tx, err)
	}
}

const ct = `ipv4     2 tcp      6 427644 ESTABLISHED src=203.0.113.9 dst=192.0.2.10 sport=56219 dport=22 packets=10 bytes=1000 src=192.0.2.10 dst=203.0.113.9 sport=22 dport=56219 packets=8 bytes=4000 [ASSURED] mark=0 zone=0 use=2
ipv4     2 tcp      6 431314 ESTABLISHED src=203.0.113.9 dst=192.0.2.10 sport=52209 dport=8080 packets=1 bytes=100 src=172.17.0.5 dst=203.0.113.9 sport=80 dport=52209 packets=1 bytes=900 [ASSURED] mark=0 zone=0 use=2
ipv4     2 tcp      6 431314 ESTABLISHED src=198.51.100.4 dst=192.0.2.10 sport=1 dport=22 packets=1 bytes=10 src=192.0.2.10 dst=198.51.100.4 sport=22 dport=1 packets=1 bytes=10 [ASSURED] mark=0 zone=0 use=2
ipv4     2 tcp      6 100 ESTABLISHED src=192.0.2.10 dst=1.1.1.1 sport=40000 dport=443 packets=1 bytes=10 src=1.1.1.1 dst=192.0.2.10 sport=443 dport=40000 packets=1 bytes=10 [ASSURED] mark=0 zone=0 use=2
ipv4     2 udp      17 18 src=10.0.2.177 dst=224.0.0.251 sport=5353 dport=5353 [UNREPLIED] src=224.0.0.251 dst=10.0.2.177 sport=5353 dport=5353 mark=0 zone=0 use=2
ipv4     2 tcp      6 100 ESTABLISHED src=172.17.0.5 dst=172.17.0.6 sport=4000 dport=5432 packets=1 bytes=10 src=172.17.0.6 dst=172.17.0.5 sport=5432 dport=4000 packets=1 bytes=10 [ASSURED] mark=0 zone=0 use=2
`

func TestSummarize(t *testing.T) {
	cs := ParseConntrack(strings.NewReader(ct))
	if len(cs) != 6 || cs[1].ReplySrc != "172.17.0.5" || cs[0].BytesReply != 4000 {
		t.Fatalf("%+v", cs)
	}
	s := Summarize(cs, map[string]bool{"192.0.2.10": true, "172.17.0.1": true}, true)
	// outgoing to 1.1.1.1, multicast and container-to-container are not inbound
	if s.Total != 3 || len(s.Inbound) != 2 || len(s.Peers) != 2 {
		t.Fatalf("%+v", s)
	}
	if s.Inbound[0].Port != 22 || s.Inbound[0].Conns != 2 || s.Inbound[0].Peers != 2 {
		t.Errorf("%+v", s.Inbound[0])
	}
	if s.Inbound[1].Port != 8080 || s.Inbound[1].ToIP != "172.17.0.5" {
		t.Errorf("docker port: %+v", s.Inbound[1])
	}
	if s.Peers[0].IP != "203.0.113.9" || s.Peers[0].Bytes != 6000 || len(s.Peers[0].Ports) != 2 {
		t.Errorf("%+v", s.Peers[0])
	}
}

func TestTCCommands(t *testing.T) {
	j := func(cs [][]string) string {
		var l []string
		for _, c := range cs {
			l = append(l, strings.Join(c, " "))
		}
		return strings.Join(l, "\n")
	}
	got := j(TCCommands("veth1", Limit{DownKbit: 8000, UpKbit: 2000}))
	for _, want := range []string{
		"qdisc replace dev veth1 root handle 1: htb default 10",
		"class replace dev veth1 parent 1: classid 1:10 htb rate 8000kbit ceil 8000kbit",
		"qdisc replace dev veth1 parent 1:10 handle 10: fq_codel",
		"qdisc del dev veth1 ingress",
		"filter add dev veth1 parent ffff: protocol all prio 1 matchall action police rate 2000kbit burst 250kb drop",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in\n%s", want, got)
		}
	}
	got = j(TCCommands("veth1", Limit{}))
	if got != "qdisc del dev veth1 root\nqdisc del dev veth1 ingress" {
		t.Errorf("clearing: %s", got)
	}
}
