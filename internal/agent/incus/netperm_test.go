package incus

import "testing"

func TestReadNetPerms(t *testing.T) {
	r := rawInstance{Config: map[string]string{"user.berth.egress": "lan", "boot.autostart": "true", "security.nesting": "true", "volatile.eth0.hwaddr": "10:66:6a:00:00:01"},
		ExpandedDevices: map[string]map[string]string{
			"eth0":               {"type": "nic", "network": "incusbr0", "security.acls": "berth-x"},
			"berth-share-a1":     {"type": "disk", "source": "/srv/data", "path": "/mnt/data", "readonly": "true"},
			"berth-fwd-tcp-2222": {"type": "proxy", "listen": "tcp:0.0.0.0:2222", "connect": "tcp:127.0.0.1:22"},
		}}
	in := r.convert()
	if in.Net.Mode != "nat" || in.Net.Egress != "lan" || in.Net.MAC == "" {
		t.Fatalf("%+v", in.Net)
	}
	if !in.Perms.Autostart || !in.Perms.Nesting || in.Perms.Privileged || len(in.Perms.Shares) != 1 || !in.Perms.Shares[0].ReadOnly || in.Perms.Shares[0].ID != "a1" {
		t.Fatalf("%+v", in.Perms)
	}
	r.ExpandedDevices["eth0"] = map[string]string{"type": "nic", "nictype": "macvlan", "parent": "end0"}
	if n := r.convert().Net; n.Mode != "lan" || n.Parent != "end0" || n.Egress != "all" {
		t.Fatalf("%+v", n)
	}
	r.ExpandedDevices["eth0"] = map[string]string{"type": "none"}
	if n := r.convert().Net; n.Mode != "isolated" {
		t.Fatalf("%+v", n)
	}
	if !skipIface.MatchString("docker0") || !skipIface.MatchString("br-49cb8351e8b7") || skipIface.MatchString("end0") || skipIface.MatchString("br0") {
		t.Fatal("interface filter")
	}
}
