package dbconn

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

func TestSQLite(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "shared", "data", "app.db")
	os.MkdirAll(filepath.Dir(p), 0o755)
	db, _ := sql.Open("sqlite", "file:"+p)
	db.Exec(`CREATE TABLE notes(id INTEGER PRIMARY KEY, body TEXT NOT NULL); INSERT INTO notes(body) VALUES ('a'),('b')`)
	db.Close()
	ctx := context.Background()
	tg := Target{Engine: "sqlite", Addr: p}
	m, err := Meta(ctx, tg, "tables", "")
	if err != nil || len(m.Rows) != 1 || m.Rows[0][0] != "notes" {
		t.Fatalf("tables: %v %v", m, err)
	}
	c, err := Meta(ctx, tg, "columns", "notes")
	if err != nil || len(c.Rows) != 2 || c.Rows[1][2] != "NO" {
		t.Fatalf("columns: %v %v", c, err)
	}
	if _, err := Query(ctx, tg, "INSERT INTO notes(body) VALUES ('x')", true, 10); err == nil {
		t.Fatal("a read-only query wrote")
	}
	// back up, write, restore
	b, err := backupTo(ctx, p, filepath.Join(dir, "bk"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Query(ctx, tg, "DELETE FROM notes", false, 10); err != nil {
		t.Fatal(err)
	}
	r, _ := Query(ctx, tg, "SELECT count(*) FROM notes", true, 10)
	if r.Rows[0][0].(int64) != 0 {
		t.Fatalf("delete: %v", r.Rows)
	}
	if err := restoreFrom(p, filepath.Join(dir, "bk", b.Name)); err != nil {
		t.Fatal(err)
	}
	r, _ = Query(ctx, tg, "SELECT count(*) FROM notes", true, 10)
	if r.Rows[0][0].(int64) != 2 {
		t.Fatalf("restore: %v", r.Rows)
	}
	if out, err := Maintain(ctx, p, "check"); err != nil || out != "ok" {
		t.Fatalf("check: %q %v", out, err)
	}
	if out, err := Maintain(ctx, p, "wal"); err != nil || out != "wal" {
		t.Fatalf("wal: %q %v", out, err)
	}
	SQLiteRoots = map[string]int{filepath.Join(dir, "shared"): 4}
	f := Discover()
	if len(f) != 1 || f[0].Path != p {
		t.Fatalf("discover: %v", f)
	}
	if sqlitePathOK("/var/lib/berth/berth.db") == nil || sqlitePathOK("relative.db") == nil {
		t.Fatal("bad paths accepted")
	}
}
