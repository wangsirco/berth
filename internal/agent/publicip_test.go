package agent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func ipServer(ip string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(ip + "\n")) }))
}

func TestPublicIPMajority(t *testing.T) {
	a, b, c := ipServer("203.0.113.7"), ipServer("203.0.113.7"), ipServer("198.51.100.1")
	defer a.Close()
	defer b.Close()
	defer c.Close()
	ip, agree, total, err := PublicIP(context.Background(), "4", []string{a.URL, b.URL, c.URL})
	if err != nil || ip != "203.0.113.7" || agree != 2 || total != 3 {
		t.Fatalf("%v %s %d/%d", err, ip, agree, total)
	}
	// two sources disagreeing: no answer rather than a guess
	if _, _, _, err := PublicIP(context.Background(), "4", []string{a.URL, c.URL}); err == nil {
		t.Fatal("split vote accepted")
	}
	junk := ipServer("<html>")
	defer junk.Close()
	if _, _, _, err := PublicIP(context.Background(), "4", []string{junk.URL}); err == nil {
		t.Fatal("garbage accepted")
	}
}
