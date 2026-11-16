package panel

import "testing"

func TestRegistrable(t *testing.T) {
	cases := map[string]string{
		"shop.example.com":    "example.com",
		"example.com":         "example.com",
		"*.example.com":       "example.com",
		"a.b.example.co.uk":   "example.co.uk",
		"www.acme.com.hk": "acme.com.hk",
		"blog.example.com.cn": "example.com.cn",
		"nas.lan":             "",
		"localhost":           "",
		"192.0.2.10":          "",
		"Example.COM.":        "example.com",
	}
	for in, want := range cases {
		if got := registrable(in); got != want {
			t.Errorf("registrable(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestClassify(t *testing.T) {
	here := map[string]bool{"203.0.113.5": true}
	if c := classify(nil, false, here); c != "none" {
		t.Fatal(c)
	}
	if c := classify([]string{"203.0.113.5"}, false, here); c != "here" {
		t.Fatal(c)
	}
	if c := classify([]string{"198.51.100.1"}, true, here); c != "cloudflare" {
		t.Fatal(c)
	}
	if c := classify([]string{"104.16.1.1"}, false, here); c != "cloudflare" {
		t.Fatal(c)
	}
	if c := classify([]string{"198.51.100.1"}, false, here); c != "elsewhere" {
		t.Fatal(c)
	}
}

func TestParseRDAP(t *testing.T) {
	b := []byte(`{"events":[{"eventAction":"registration","eventDate":"2020-01-01T00:00:00Z"},{"eventAction":"expiration","eventDate":"2027-03-04T05:06:07Z"}],
		"entities":[{"roles":["registrar"],"handle":"292","vcardArray":["vcard",[["version",{},"text","4.0"],["fn",{},"text","MarkMonitor Inc."]]]}]}`)
	exp, reg, err := parseRDAP(b)
	if err != nil || exp != 1804136767 || reg != "MarkMonitor Inc." {
		t.Fatal(exp, reg, err)
	}
}
