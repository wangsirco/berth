package panel

import "testing"

func TestTimelineMergesChangesAndLogins(t *testing.T) {
	c := newTC(t)
	c.mkUser("iwsheng", "correct horse 9", "super")
	c.login("iwsheng", "correct horse 9")
	c.do("POST", "/api/v1/hosts/local/changes/audittest.set", map[string]string{"key": "tl", "value": "1"})
	_, out := c.do("GET", "/api/v1/timeline", nil)
	items := out["items"].([]any)
	types := map[string]bool{}
	for _, it := range items {
		m := it.(map[string]any)
		types[m["type"].(string)] = true
		if m["type"] == "login" && m["login"].(map[string]any)["method"] != "password" {
			t.Fatalf("%v", m)
		}
	}
	if !types["change"] || !types["login"] {
		t.Fatalf("%v", items)
	}
}
