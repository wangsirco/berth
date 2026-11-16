package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
)

func TestGeneratedCommandPreviewsThenRuns(t *testing.T) {
	var mu sync.Mutex
	var calls []string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls = append(calls, r.Method+" "+r.URL.RequestURI())
		mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer berth_cli_test" || r.Header.Get("X-Berth-Via") != "cli" {
			w.WriteHeader(401)
			return
		}
		switch r.URL.Path {
		case "/api/v1/kinds":
			json.NewEncoder(w).Encode(map[string]any{"items": []map[string]any{{"name": "container.restart", "cli": "berth container restart {{.name}}",
				"params": map[string]any{"properties": map[string]any{"name": map[string]any{"type": "string"}}, "required": []string{"name"}}}}})
		case "/api/v1/hosts/local/changes/container.restart":
			var p map[string]any
			json.NewDecoder(r.Body).Decode(&p)
			if p["name"] != "n8n" {
				w.WriteHeader(400)
				return
			}
			status := "done"
			if r.URL.Query().Get("plan") == "1" {
				status = "planned"
			}
			json.NewEncoder(w).Encode(map[string]any{"id": "chg_1", "status": status, "plan": map[string]any{"risk": "low"},
				"summary": map[string]string{"en": "Restart container n8n"}})
		}
	}))
	defer srv.Close()
	h := sha256.Sum256(srv.Certificate().Raw)
	t.Setenv("BERTH_CONFIG", filepath.Join(t.TempDir(), "c.json"))
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	cfg := &config{Servers: map[string]server{"t": {URL: srv.URL, Token: "berth_cli_test", CAFP: hex.EncodeToString(h[:])}}, Current: "t"}
	if err := cfg.save(); err != nil {
		t.Fatal(err)
	}
	if code := Main([]string{"container", "restart", "n8n"}); code != 0 {
		t.Fatalf("exit %d", code)
	}
	want := []string{"GET /api/v1/kinds", "POST /api/v1/hosts/local/changes/container.restart?plan=1", "POST /api/v1/hosts/local/changes/container.restart"}
	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 3 || calls[0] != want[0] || calls[1] != want[1] || calls[2] != want[2] {
		t.Fatalf("calls %v", calls)
	}
}

func TestPinRejectsOtherCertificate(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	if _, err := httpClient("00ff").Get(srv.URL); err == nil {
		t.Fatal("wrong pin accepted")
	}
}
