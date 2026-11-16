package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestAgentConfigInitOnceAndPanelKeyMatches(t *testing.T) {
	etc := t.TempDir()
	c1, err := loadOrInitAgentConfig(etc)
	if err != nil {
		t.Fatal(err)
	}
	st, _ := os.Stat(filepath.Join(etc, "agent.json"))
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("agent.json mode %v", st.Mode().Perm())
	}
	var pk panelKey
	b, _ := os.ReadFile(filepath.Join(etc, "panel-agent.key"))
	json.Unmarshal(b, &pk)
	if c1.Keys[pk.KeyID] == "" || c1.Keys[pk.KeyID] != pk.Secret {
		t.Fatalf("panel key %+v does not match agent keys %v", pk, c1.Keys)
	}
	c2, _ := loadOrInitAgentConfig(etc)
	if c2.Keys[pk.KeyID] != pk.Secret {
		t.Fatal("second start must reuse the key")
	}
}
