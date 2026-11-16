package appstore

import (
	"strings"
	"testing"
)

func TestCatalogIsValid(t *testing.T) {
	list, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) == 0 {
		t.Fatal("empty catalog")
	}
	for _, a := range list {
		in := map[string]string{}
		for _, f := range a.Fields {
			if f.Required && f.Default == "" {
				in[f.Key] = "example.com" // the user has to fill these
			}
		}
		vals, err := a.Values(in)
		if err != nil {
			t.Errorf("%s: defaults do not validate: %v", a.ID, err)
			continue
		}
		env := a.Env(a.ID, "/srv/berth/apps/"+a.ID, vals)
		for _, f := range a.Fields {
			if !strings.Contains(env, f.Key+"=") {
				t.Errorf("%s: %s missing from .env", a.ID, f.Key)
			}
			if f.Type == "password" && f.Generate && len(vals[f.Key]) < 16 {
				t.Errorf("%s: %s not generated", a.ID, f.Key)
			}
		}
	}
}

func TestValuesChecksInput(t *testing.T) {
	a := App{Fields: []Field{
		{Key: "PORT", Type: "port", Required: true, Default: "8080"},
		{Key: "PASS", Type: "password", Generate: true},
		{Key: "MODE", Type: "select", Options: []string{"a", "b"}},
	}}
	v, err := a.Values(map[string]string{"MODE": "b"})
	if err != nil || v["PORT"] != "8080" || len(v["PASS"]) != 24 || v["MODE"] != "b" {
		t.Fatalf("%v %v", err, v)
	}
	for _, bad := range []map[string]string{{"PORT": "70000"}, {"MODE": "c"}, {"PASS": "x\ny"}} {
		if _, err := a.Values(bad); err == nil {
			t.Errorf("accepted %v", bad)
		}
	}
	if Password(24) == Password(24) {
		t.Fatal("passwords repeat")
	}
}
