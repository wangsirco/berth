package metrics

import "testing"

func TestCgroupMemory(t *testing.T) {
	if got := containerMem("testdata/cgroup", "abc"); got != 100000000 {
		t.Fatalf("container mem %d", got)
	}
	s := services("testdata/cgroup")
	if len(s) != 1 || s[0].Name != "nginx.service" || s[0].MemBytes != 20971520 {
		t.Fatalf("%+v", s)
	}
}

func TestBerthsAccountForEverything(t *testing.T) {
	saved := cgroupRoot
	cgroupRoot = "testdata/cgroup"
	defer func() { cgroupRoot = saved }()
	h := Host{MemTotal: 8 << 30, MemUsed: 1 << 30, MemAvail: 7 << 30}
	b := Berths(h, []ContainerRef{{ID: "abc", Name: "n8n", Project: "n8n"}})
	var sum int64
	types := map[string]bool{}
	for _, x := range b {
		sum += x.MemBytes
		types[x.Type] = true
	}
	if sum != h.MemUsed+h.MemAvail {
		t.Fatalf("segments sum %d, want %d", sum, h.MemUsed+h.MemAvail)
	}
	for _, want := range []string{"container", "service", "kernel", "free"} {
		if !types[want] {
			t.Fatalf("missing %s in %+v", want, b)
		}
	}
}
