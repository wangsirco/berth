package incus

import (
	"encoding/json"
	"testing"
)

// a VM running Docker: its address is the LAN card's, not docker0's or a bridge's
func TestAddressesSkipInnerNetworks(t *testing.T) {
	var r rawInstance
	json.Unmarshal([]byte(`{"name":"fedora-1","type":"virtual-machine","status":"Running","config":{},"expanded_devices":{},
	 "state":{"processes":12,"network":{
	  "docker0":{"addresses":[{"family":"inet","address":"172.17.0.1","scope":"global"}]},
	  "br-1a2b3c4d5e6f":{"addresses":[{"family":"inet","address":"172.18.0.1","scope":"global"}]},
	  "lo":{"addresses":[{"family":"inet","address":"127.0.0.1","scope":"local"}]},
	  "tailscale0":{"addresses":[{"family":"inet","address":"100.70.1.2","scope":"global"}]},
	  "enp5s0":{"addresses":[{"family":"inet","address":"192.168.1.50","scope":"global"},{"family":"inet6","address":"fe80::1","scope":"link"}]}}}}`), &r)
	in := r.convert()
	if len(in.IPv4) != 2 || in.IPv4[0] != "192.168.1.50" || in.IPv4[1] != "100.70.1.2" {
		t.Fatalf("addresses: %v", in.IPv4)
	}
}
