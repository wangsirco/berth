package change

import "testing"

func TestEquivalentCommandFlattens(t *testing.T) {
	Register(Kind{Name: "test.cli", CLI: "x {{.domains}} {{.record_name}} {{if .on}}on{{else}}off{{end}}", Plan: nil})
	got := EquivalentCommand("test.cli", []byte(`{"domains":["a.com","b c"],"record":{"name":"r.a.com"},"on":false}`))
	if got != "x a.com 'b c' r.a.com off" {
		t.Fatalf("%q", got)
	}
}
