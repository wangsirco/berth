package docker

import (
	"strings"
	"testing"
)

func TestSetPortBind(t *testing.T) {
	flow := "services:\n  db:\n    image: mariadb\n    ports: [\"127.0.0.1:${DB_PORT}:3306\"] # local\n"
	out, old, err := SetPortBind([]byte(flow), "db", 3306, "0.0.0.0")
	if err != nil || old != "127.0.0.1" || !strings.Contains(string(out), `ports: ["${DB_PORT}:3306"] # local`) {
		t.Fatalf("flow: %v %q\n%s", err, old, out)
	}
	back, old2, _ := SetPortBind(out, "db", 3306, "127.0.0.1")
	if string(back) != flow || old2 != "0.0.0.0" {
		t.Fatalf("round trip:\n%s", back)
	}
	block := "services:\n  pg:\n    ports:\n      - \"8080:80\"\n      - 5432:5432/tcp\n"
	out, _, err = SetPortBind([]byte(block), "pg", 5432, "192.0.2.10")
	if err != nil || !strings.Contains(string(out), "      - 192.0.2.10:5432:5432/tcp\n") || !strings.Contains(string(out), `"8080:80"`) {
		t.Fatalf("block: %v\n%s", err, out)
	}
	if _, _, err := SetPortBind([]byte(block), "pg", 3306, "0.0.0.0"); err == nil {
		t.Fatal("missing port should fail")
	}
}
