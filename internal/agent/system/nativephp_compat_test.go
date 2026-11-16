package system

import (
	"strings"
	"testing"
)

func phpCodes(r PHPReport, level string) string {
	var out []string
	for _, f := range r.Findings {
		if f.Level == level {
			out = append(out, f.Code)
		}
	}
	return strings.Join(out, ",")
}

var nativeVers = []string{"8.2", "8.3", "8.4", "8.5"}

func TestAnalyzePHPLaravel(t *testing.T) {
	cj := []byte(`{"require":{"php":"^8.2","laravel/framework":"^11.0","ext-intl":"*","ext-redis":"*"}}`)
	r := AnalyzePHP(cj, nil, PHPFiles{Artisan: true, Vendor: false}, nativeVers, []string{"8.1", "7.4"})
	if r.Framework != "laravel" || r.Version != "8.5" || r.RootSub != "public" || !r.Native {
		t.Fatalf("%+v", r)
	}
	if !contains(r.Extensions, "intl") || !contains(r.Extensions, "redis") {
		t.Fatalf("exts %v", r.Extensions)
	}
	if len(r.Workers) != 1 || !strings.Contains(r.Workers[0].Command, "queue:work") || !strings.Contains(r.Schedule, "schedule:run") {
		t.Fatalf("workers %+v %q", r.Workers, r.Schedule)
	}
	if !strings.Contains(phpCodes(r, "warn"), "php_no_vendor") {
		t.Fatalf("%+v", r.Findings)
	}
}

func TestAnalyzePHPOldVersion(t *testing.T) {
	r := AnalyzePHP([]byte(`{"require":{"php":"~7.4"}}`), nil, PHPFiles{}, nativeVers, []string{"8.1", "7.4"})
	if r.Native || r.Version != "7.4" || !strings.Contains(phpCodes(r, "warn"), "php_container_only") {
		t.Fatalf("%+v", r)
	}
	r = AnalyzePHP([]byte(`{"require":{"php":">=9"}}`), nil, PHPFiles{}, nativeVers, []string{"8.1", "7.4"})
	if !strings.Contains(phpCodes(r, "block"), "php_version") {
		t.Fatalf("%+v", r)
	}
}

func TestAnalyzePHPPlainAndWordPress(t *testing.T) {
	r := AnalyzePHP(nil, nil, PHPFiles{}, nativeVers, nil)
	if r.Framework != "" || r.Version != "8.5" || r.RootSub != "" || !r.Native {
		t.Fatalf("%+v", r)
	}
	r = AnalyzePHP(nil, nil, PHPFiles{WordPress: true}, nativeVers, nil)
	if r.Framework != "wordpress" || !contains(r.Extensions, "mysqli") {
		t.Fatalf("%+v", r)
	}
}

func TestAnalyzePHPLockExtensions(t *testing.T) {
	lock := []byte(`{"packages":[{"name":"a/b","require":{"ext-gmp":"*","php":">=8.1"}},{"name":"c/d","require":{"ext-sodium":"*"}}]}`)
	r := AnalyzePHP([]byte(`{"require":{"php":">=8.1"}}`), lock, PHPFiles{Vendor: true}, nativeVers, nil)
	if !contains(r.Extensions, "gmp") || !contains(r.Extensions, "sodium") {
		t.Fatalf("%v", r.Extensions)
	}
	if strings.Contains(phpCodes(r, "warn"), "php_no_vendor") {
		t.Fatal("vendor is there")
	}
}
