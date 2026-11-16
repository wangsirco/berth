package panel

import "testing"

func TestRDMayUse(t *testing.T) {
	c := rdConn{Owner: 7, Users: []int64{9}}
	op := &User{ID: 9, Role: RoleAdmin, Perms: []string{"remote.use"}}
	other := &User{ID: 8, Role: RoleAdmin, Perms: []string{"remote.use"}}
	owner := &User{ID: 7, Role: RoleAdmin, Perms: []string{"remote.use"}}
	if !c.mayUse(op) || !c.mayUse(owner) || c.mayUse(other) {
		t.Fatal("sharing rules")
	}
	op.Perms = nil
	if c.mayUse(op) {
		t.Fatal("remote.use is required")
	}
}

func TestGuacArgs(t *testing.T) {
	m := guacArgs(rdConn{ID: 3, Protocol: "rdp", Host: "10.0.0.5", Port: 3389, Username: "u", IgnoreCert: true, Files: true}, map[string]string{"password": "p"})
	if m["hostname"] != "10.0.0.5" || m["password"] != "p" || m["ignore-cert"] != "true" || m["disable-copy"] != "true" || m["drive-path"] != "/drive/3" {
		t.Fatalf("%v", m)
	}
	v := guacArgs(rdConn{Protocol: "vnc", Host: "h", Port: 5900, Username: "x", Clipboard: true}, nil)
	if _, ok := v["username"]; ok || v["disable-copy"] != "" {
		t.Fatalf("%v", v)
	}
}
