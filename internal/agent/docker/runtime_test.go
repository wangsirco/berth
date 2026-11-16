package docker

import "testing"

func TestSetRuntime(t *testing.T) {
	src := "services:\n  db:\n    image: redis:5   # cache\n    restart: always\n  web:\n    image: nginx\n"
	out, old, err := SetRuntime([]byte(src), "db", "krun")
	if err != nil || old != "" {
		t.Fatal(err, old)
	}
	want := "services:\n  db:\n    runtime: krun\n    image: redis:5   # cache\n    restart: always\n  web:\n    image: nginx\n"
	if string(out) != want {
		t.Fatalf("add:\n%s", out)
	}
	back, old, err := SetRuntime(out, "db", "")
	if err != nil || old != "krun" || string(back) != src {
		t.Fatalf("remove: %v %q\n%s", err, old, back)
	}
	if _, _, err := SetRuntime([]byte(src), "cache", "krun"); err == nil {
		t.Fatal("missing service accepted")
	}
	same, _, _ := SetRuntime(out, "db", "krun")
	if string(same) != want {
		t.Fatal("setting the same value changed the file")
	}
}
