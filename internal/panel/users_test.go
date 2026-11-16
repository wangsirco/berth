package panel

import "testing"

func TestCan(t *testing.T) {
	super := &User{Role: "super"}
	admin := &User{Role: "admin", Perms: []string{"containers.view", "containers.manage"}}
	viewer := &User{Role: "viewer"}
	if !super.Can("users.manage") || admin.Can("users.manage") {
		t.Fatal("super-only key")
	}
	if !admin.Can("containers.manage") || admin.Can("terminal.use") {
		t.Fatal("admin perms")
	}
	if !viewer.Can("containers.view") || viewer.Can("containers.manage") || viewer.Can("terminal.use") {
		t.Fatal("viewer gets *.view except dangerous")
	}
	if (&User{Role: "super", Disabled: true}).Can("overview.view") {
		t.Fatal("disabled")
	}
}

func TestPasswordPolicy(t *testing.T) {
	for _, bad := range []string{"short", "aaaaaaaaaa"} {
		if validPassword(bad) == nil {
			t.Fatalf("%q accepted", bad)
		}
	}
	if validPassword("correct horse 9") != nil {
		t.Fatal("good rejected")
	}
}
