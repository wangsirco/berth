package cli

import "testing"

func TestCommandFromTemplate(t *testing.T) {
	k := kindInfo{Name: "container.remove", CLI: "berth container rm {{.name}}{{if .volumes}} --volumes{{end}}",
		Params: paramSchema{Props: map[string]prop{"name": {Type: "string"}, "volumes": {Type: "boolean"}}, Required: []string{"name"}}}
	path, pos := commandShape(k)
	if path != "container rm" || len(pos) != 1 || pos[0] != "name" {
		t.Fatalf("%q %v", path, pos)
	}
	params, err := parseArgs(k, []string{"n8n", "--volumes"})
	if err != nil || params["name"] != "n8n" || params["volumes"] != true {
		t.Fatalf("%v %v", params, err)
	}
	if _, err := parseArgs(k, []string{}); err == nil {
		t.Fatal("missing name")
	}
}
