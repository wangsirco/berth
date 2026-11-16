package system

import (
	"strings"
	"testing"
)

func codes(r NodeReport, level string) string {
	var out []string
	for _, f := range r.Findings {
		if f.Level == level {
			out = append(out, f.Code)
		}
	}
	return strings.Join(out, ",")
}

func TestAnalyzeNodeNext(t *testing.T) {
	pkg := []byte(`{"engines":{"node":">=22"},"scripts":{"build":"next build","start":"next start"},"dependencies":{"next":"16.3.8","react":"19","sharp":"^0.35"}}`)
	lock := map[string][]byte{"package-lock.json": []byte(`{"packages":{"node_modules/sharp":{"version":"0.35.5"},"node_modules/next":{"version":"16.3.8"}}}`)}
	r := AnalyzeNode(pkg, lock, []string{"22.21.0", "24.11.0"}, 6000, map[string]bool{})
	if r.Framework != "next" || r.PackageManager != "npm" || r.Node != "24.11.0" {
		t.Fatalf("%+v", r)
	}
	if r.Build != "npm run build" || !strings.Contains(r.Start, "next start") || r.Health != "/" {
		t.Fatalf("commands: %+v", r)
	}
	if !contains(r.Shared, ".next/cache") || !contains(r.Shared, "data") {
		t.Fatalf("shared: %v", r.Shared)
	}
	if codes(r, "block") != "" || !strings.Contains(codes(r, "ok"), "native_prebuilt") {
		t.Fatalf("findings: %+v", r.Findings)
	}
}

func TestAnalyzeNodeEnginesTooNew(t *testing.T) {
	r := AnalyzeNode([]byte(`{"engines":{"node":">=99"},"dependencies":{"express":"4"}}`), nil, []string{"24.11.0"}, 4000, nil)
	if r.Node != "" || !strings.Contains(codes(r, "block"), "node_version") {
		t.Fatalf("%+v", r)
	}
}

func TestAnalyzeNodeInstallsMissing(t *testing.T) {
	r := AnalyzeNode([]byte(`{"engines":{"node":"^20"},"dependencies":{"express":"4"}}`), map[string][]byte{"pnpm-lock.yaml": []byte("lockfileVersion: '9.0'\n")}, []string{"24.11.0"}, 4000, nil)
	if r.Node != "20" || r.PackageManager != "pnpm" || r.Framework != "express" || !strings.Contains(codes(r, "ok"), "node_install") {
		t.Fatalf("%+v", r)
	}
}

func TestAnalyzeNodeNativeBuild(t *testing.T) {
	lock := map[string][]byte{"package-lock.json": []byte(`{"packages":{"node_modules/better-sqlite3":{"version":"11.0.0"}}}`)}
	r := AnalyzeNode([]byte(`{"dependencies":{"better-sqlite3":"11"}}`), lock, []string{"24.11.0"}, 4000, map[string]bool{"python3": true})
	if !strings.Contains(codes(r, "warn"), "native_build_tools") {
		t.Fatalf("%+v", r.Findings)
	}
	r = AnalyzeNode([]byte(`{"dependencies":{"better-sqlite3":"11"}}`), lock, []string{"24.11.0"}, 4000, map[string]bool{"python3": true, "gcc": true, "make": true, "g++": true})
	if strings.Contains(codes(r, "warn"), "native_build_tools") {
		t.Fatalf("%+v", r.Findings)
	}
}

func TestAnalyzeNodeNoLockAndMemory(t *testing.T) {
	r := AnalyzeNode([]byte(`{"dependencies":{"next":"15"}}`), nil, []string{"24.11.0"}, 1000, nil)
	w := codes(r, "warn")
	if !strings.Contains(w, "no_lockfile") || !strings.Contains(w, "build_memory") {
		t.Fatalf("%+v", r.Findings)
	}
}

func TestSatisfies(t *testing.T) {
	for _, c := range []struct {
		v, r string
		ok   bool
	}{{"24.11.0", ">=22", true}, {"20.1.0", ">=22", false}, {"22.3.1", "^22", true}, {"23.0.0", "^22", false}, {"22.9.9", "22.x", true},
		{"20.18.0", ">=18 <21", true}, {"22.0.0", ">=18 <21", false}, {"20.5.0", "18 || 20", true}, {"24.1.0", "", true}, {"22.1.5", "~22.1", true}, {"22.2.0", "~22.1", false}} {
		if satisfies(c.v, c.r) != c.ok {
			t.Errorf("%s %s want %v", c.v, c.r, c.ok)
		}
	}
}
