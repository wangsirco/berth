package store

import (
	"path/filepath"
	"testing"
)

func TestOpenMigratesAndIsIdempotent(t *testing.T) {
	p := filepath.Join(t.TempDir(), "b.db")
	db, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	var v int
	db.QueryRow(`SELECT MAX(v) FROM schema_version`).Scan(&v)
	if v != len(migrations) {
		t.Fatalf("version %d want %d", v, len(migrations))
	}
	db.Close()
	db, err = Open(p) // second open must not re-run
	if err != nil {
		t.Fatal(err)
	}
	var mode string
	db.QueryRow(`PRAGMA journal_mode`).Scan(&mode)
	if mode != "wal" {
		t.Fatalf("journal_mode=%s", mode)
	}
	for _, tbl := range []string{"users", "sessions", "changes", "tasks", "tokens", "relations"} {
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name=?`, tbl).Scan(&n); err != nil || n != 1 {
			t.Fatalf("table %s missing", tbl)
		}
	}
}

func TestFailedMigrationRollsBack(t *testing.T) {
	saved := migrations
	defer func() { migrations = saved }()
	migrations = append(append([]string{}, saved...), `CREATE TABLE ok_part (x INT); SELECT nope FROM nowhere`)
	p := filepath.Join(t.TempDir(), "b.db")
	if _, err := Open(p); err == nil {
		t.Fatal("expected error")
	}
	migrations = saved
	db, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	var n int
	db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name='ok_part'`).Scan(&n)
	if n != 0 {
		t.Fatal("failed migration left ok_part behind")
	}
}
