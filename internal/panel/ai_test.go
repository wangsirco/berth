package panel

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A fake model: first asks for list_kinds, then proposes a change, then answers.
func fakeModel(t *testing.T, anthropic bool) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		body := string(b)
		results := strings.Count(body, `"tool_call_id"`) + strings.Count(body, `"tool_result"`)
		call := func(id, name, args string) {
			if anthropic {
				w.Write([]byte(`{"content":[{"type":"text","text":"looking"},{"type":"tool_use","id":"` + id + `","name":"` + name + `","input":` + args + `}],"usage":{"input_tokens":100,"output_tokens":10}}`))
				return
			}
			a, _ := json.Marshal(args)
			w.Write([]byte(`{"choices":[{"message":{"content":"","tool_calls":[{"id":"` + id + `","type":"function","function":{"name":"` + name + `","arguments":` + string(a) + `}}]}}],"usage":{"prompt_tokens":100,"completion_tokens":10}}`))
		}
		switch results {
		case 0:
			call("c1", "list_kinds", `{"filter":"uptime"}`)
		case 1:
			if !strings.Contains(body, "uptime.set") {
				t.Errorf("tool result missing: %s", body)
			}
			call("c2", "propose_change", `{"kind":"uptime.set","params":{"name":"web","kind":"http","target":"https://example.com"},"why":"watch it"}`)
		default:
			if anthropic {
				w.Write([]byte(`{"content":[{"type":"text","text":"I proposed a check."}],"usage":{"input_tokens":50,"output_tokens":5}}`))
				return
			}
			w.Write([]byte(`{"choices":[{"message":{"content":"I proposed a check."}}],"usage":{"prompt_tokens":50,"completion_tokens":5}}`))
		}
	}))
}

func TestAIChat(t *testing.T) {
	for _, prov := range []string{"openai", "anthropic"} {
		c := newTC(t)
		c.mkUser("ops", "pw-ops-123456", RoleSuper)
		c.login("ops", "pw-ops-123456")
		srv := fakeModel(t, prov == "anthropic")
		resp, _ := c.do("POST", "/api/v1/ai/chat", map[string]any{"messages": []map[string]string{{"role": "user", "content": "hi"}}})
		if resp.StatusCode != 409 {
			t.Fatalf("%s: no key yet should be 409, got %d", prov, resp.StatusCode)
		}
		resp, out := c.do("POST", "/api/v1/hosts/local/changes/ai.set", map[string]any{"provider": prov, "base_url": srv.URL, "key": "sk-test", "monthly_limit": 1000})
		if resp.StatusCode != 200 || out["status"] != "done" {
			t.Fatalf("%s: set: %d %v", prov, resp.StatusCode, out)
		}
		resp, out = c.do("POST", "/api/v1/ai/chat", map[string]any{"messages": []map[string]string{{"role": "user", "content": "watch example.com"}}, "locale": "en"})
		if resp.StatusCode != 200 {
			t.Fatalf("%s: chat %d %v", prov, resp.StatusCode, out)
		}
		props, _ := out["proposals"].([]any)
		if out["reply"] != "I proposed a check." || len(props) != 1 {
			t.Fatalf("%s: %v", prov, out)
		}
		p := props[0].(map[string]any)["change"].(map[string]any)
		if p["kind"] != "uptime.set" || p["status"] != "planned" {
			t.Fatalf("%s: proposal %v", prov, p)
		}
		// nothing ran: a proposal is only a preview
		var n int
		c.app.db.QueryRow(`SELECT COUNT(*) FROM uptime_checks`).Scan(&n)
		if n != 0 {
			t.Fatalf("%s: the proposal ran", prov)
		}
		// usage is counted and capped
		if out["month"].(float64) != 275 {
			t.Fatalf("%s: usage %v", prov, out["month"])
		}
		c.app.db.Exec(`UPDATE ai_usage SET tokens_in=5000`)
		if resp, _ := c.do("POST", "/api/v1/ai/chat", map[string]any{"messages": []map[string]string{{"role": "user", "content": "again"}}}); resp.StatusCode != 429 {
			t.Fatalf("%s: over the cap should be 429, got %d", prov, resp.StatusCode)
		}
		srv.Close()
	}
}
