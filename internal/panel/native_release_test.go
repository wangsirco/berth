package panel

import (
	"encoding/json"
	"testing"

	"github.com/wangsirco/berth/internal/agent/system"
)

// The release command (run before start) and the serving release must both survive a save.
func TestNativeProjectKeepsReleaseCommand(t *testing.T) {
	p := nativeProject{NativeSpec: system.NativeSpec{Name: "dj", Release: "python manage.py migrate"}, Current: "20261114-210000"}
	b, _ := json.Marshal(p)
	var q nativeProject
	json.Unmarshal(b, &q)
	q.fixOld()
	if q.NativeSpec.Release != "python manage.py migrate" || q.Current != "20261114-210000" {
		t.Fatalf("lost a field: %s", b)
	}
	// a record saved before R3 kept the serving release under "release"
	var old nativeProject
	json.Unmarshal([]byte(`{"mode":"release","name":"web","release":"20261101-120000"}`), &old)
	old.fixOld()
	if old.Current != "20261101-120000" || old.NativeSpec.Release != "" {
		t.Fatalf("old record: %+v", old)
	}
}
