package dbconn

import "testing"

func TestDanger(t *testing.T) {
	cases := map[string]string{
		"DROP TABLE users":                        "drop",
		"drop database shop;":                     "drop",
		"TRUNCATE orders":                         "truncate",
		"DELETE FROM orders":                      "no_where",
		"delete from orders where id = 3":         "",
		"UPDATE users SET admin = 1":              "no_where",
		"update users set a=1 where id=2":         "",
		"ALTER TABLE t DROP COLUMN c":             "drop",
		"SELECT * FROM drop_list":                 "",
		"-- DROP TABLE x\nSELECT 1":               "",
		"/* truncate */ insert into t values (1)": "",
	}
	for q, want := range cases {
		if got := Danger(q); got != want {
			t.Errorf("%q: %q want %q", q, got, want)
		}
	}
	if !returnsRows("  -- note\n with x as (select 1) select * from x") || returnsRows("insert into t values (1)") {
		t.Fatal("returnsRows")
	}
}

func TestAdminRejectsBadNames(t *testing.T) {
	for _, n := range []string{"a b", "x;drop", "", "名字"} {
		if n == "" {
			continue
		}
		if err := Admin(nil, Target{Engine: "postgres", Addr: "127.0.0.1:1"}, "create_db", n, "", ""); err == nil {
			t.Errorf("accepted %q", n)
		}
	}
}
