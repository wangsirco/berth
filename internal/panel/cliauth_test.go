package panel

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"
)

func TestCLIDeviceFlow(t *testing.T) {
	c := newTC(t)
	c.mkUser("iwsheng", "correct horse 9", "super")
	post := func(path string, body any) (int, map[string]any) {
		b, _ := json.Marshal(body)
		resp, err := c.srv.Client().Post(c.srv.URL+path, "application/json", bytes.NewReader(b))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		out := map[string]any{}
		json.NewDecoder(resp.Body).Decode(&out)
		return resp.StatusCode, out
	}
	_, auth := post("/api/v1/cli/authorize", map[string]string{"device": "MacBook"})
	dc, uc := auth["device_code"].(string), auth["user_code"].(string)
	if code, _ := post("/api/v1/cli/token", map[string]string{"device_code": dc}); code != 202 {
		t.Fatalf("should be pending, got %d", code)
	}
	c.login("iwsheng", "correct horse 9")
	if r, _ := c.do("POST", "/api/v1/cli/approve", map[string]any{"code": uc, "allow": true}); r.StatusCode != 200 {
		t.Fatalf("approve %d", r.StatusCode)
	}
	code, tok := post("/api/v1/cli/token", map[string]string{"device_code": dc})
	if code != 200 || len(tok["token"].(string)) < 20 || tok["token"].(string)[:10] != "berth_cli_" {
		t.Fatalf("%d %v", code, tok)
	}
	if code, _ := post("/api/v1/cli/token", map[string]string{"device_code": dc}); code != 410 {
		t.Fatal("token must be handed out once")
	}
	req, _ := http.NewRequest("GET", c.srv.URL+"/api/v1/me", nil)
	req.Header.Set("Authorization", "Bearer "+tok["token"].(string))
	resp, _ := c.srv.Client().Do(req)
	if resp.StatusCode != 200 {
		t.Fatalf("cli token rejected: %d", resp.StatusCode)
	}
}
