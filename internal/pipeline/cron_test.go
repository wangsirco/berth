package pipeline

import (
	"testing"
	"time"
)

func TestCron(t *testing.T) {
	at := func(s string) time.Time { v, _ := time.Parse("2006-01-02 15:04", s); return v }
	cases := []struct {
		expr, at string
		want     bool
	}{
		{"*/15 * * * *", "2026-11-11 10:30", true},
		{"*/15 * * * *", "2026-11-11 10:31", false},
		{"0 3 * * 1-5", "2026-11-11 03:00", true},  // Wednesday
		{"0 3 * * 1-5", "2026-11-15 03:00", false}, // Sunday
		{"0 3 * * 7", "2026-11-15 03:00", true},
		{"30 2 1,15 * *", "2026-11-15 02:30", true},
		{"@daily", "2026-11-11 03:00", true},
		{"5-10/5 * * * *", "2026-11-11 00:10", true},
	}
	for _, c := range cases {
		cr, err := ParseCron(c.expr)
		if err != nil {
			t.Fatalf("%s: %v", c.expr, err)
		}
		if cr.Match(at(c.at)) != c.want {
			t.Errorf("%s at %s: %v", c.expr, c.at, !c.want)
		}
	}
	for _, b := range []string{"", "* * * *", "61 * * * *", "*/0 * * * *", "a * * * *", "5-1 * * * *"} {
		if _, err := ParseCron(b); err == nil {
			t.Errorf("accepted %q", b)
		}
	}
}

func TestCronNext(t *testing.T) {
	loc := time.UTC
	for _, c := range []struct{ expr, from, want string }{
		{"*/15 * * * *", "2026-11-13 10:07", "2026-11-13 10:15"},
		{"0 3 * * *", "2026-11-13 10:07", "2026-11-14 03:00"},
		{"30 2 1 * *", "2026-12-15 00:00", "2027-01-01 02:30"},
		{"0 9 * * 1", "2026-11-13 10:07", "2026-11-16 09:00"}, // Friday → Monday
	} {
		cr, err := ParseCron(c.expr)
		if err != nil {
			t.Fatal(err)
		}
		from, _ := time.ParseInLocation("2006-01-02 15:04", c.from, loc)
		if got := cr.Next(from).Format("2006-01-02 15:04"); got != c.want {
			t.Errorf("%s from %s = %s, want %s", c.expr, c.from, got, c.want)
		}
	}
}
