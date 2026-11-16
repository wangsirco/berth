package metrics

import "testing"

func TestMemInfo(t *testing.T) {
	m, err := readMemInfo("testdata/proc/meminfo")
	if err != nil {
		t.Fatal(err)
	}
	// testdata: MemTotal 8000000 kB, MemAvailable 5000000 kB, SwapTotal 16777212 kB, SwapFree 16000000 kB
	if m.MemTotal != 8000000*1024 || m.MemUsed != 3000000*1024 || m.SwapUsed != 777212*1024 {
		t.Fatalf("%+v", m)
	}
}

func TestCPUDelta(t *testing.T) {
	a := cpuTimes{user: 100, system: 50, idle: 850}
	b := cpuTimes{user: 150, system: 100, idle: 950} // busy +100, idle +100
	if got := cpuPercent(a, b); got != 50 {
		t.Fatalf("got %v", got)
	}
}

func TestLoopbackAndContainerIfacesSkipped(t *testing.T) {
	for n, skip := range map[string]bool{"lo": true, "veth12": true, "docker0": true, "br-1a": true, "end0": false, "tailscale0": false} {
		if skipIface(n) != skip {
			t.Errorf("%s", n)
		}
	}
}
