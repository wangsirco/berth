package dbconn

// SQLite files as database instances (R4): the address is the file's path. Browsing and
// read-only queries open the file read-only; writes are preceded by a consistent copy
// (VACUUM INTO) the change can be undone from, and leave the journal files owned by whoever
// owns the database (the site's user), so the site can keep writing.

import (
	"context"
	"database/sql"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/wangsirco/berth/internal/errs"
	_ "modernc.org/sqlite"
)

const SQLiteBackups = "/var/lib/berth/sqlite-backups"

var sqliteHeader = []byte("SQLite format 3\x00")

// sqlitePathOK: an absolute path to an existing SQLite file outside Berth's own state.
func sqlitePathOK(p string) error {
	c := filepath.Clean(p)
	if !filepath.IsAbs(p) || c != p || strings.Contains(p, "..") {
		return errs.New(400, "sqlite.bad_path")
	}
	// Berth's own state (the panel's database among it) and system folders are off limits
	for _, deny := range []string{"/var/lib/berth", "/etc/", "/proc/", "/sys/", "/dev/", "/run/"} {
		if strings.HasPrefix(c, deny) {
			return errs.New(400, "sqlite.bad_path")
		}
	}
	if !isSQLite(c) {
		return errs.New(404, "sqlite.not_sqlite", "path", p)
	}
	return nil
}

func isSQLite(p string) bool {
	fi, err := os.Stat(p)
	if err != nil || !fi.Mode().IsRegular() || fi.Size() < 512 {
		return false
	}
	f, err := os.Open(p)
	if err != nil {
		return false
	}
	defer f.Close()
	b := make([]byte, 16)
	if _, err := io.ReadFull(f, b); err != nil {
		return false
	}
	return string(b) == string(sqliteHeader)
}

func openSQLite(path string, readonly bool) (*sql.DB, error) {
	if err := sqlitePathOK(path); err != nil {
		return nil, err
	}
	mode := "rw"
	if readonly {
		mode = "ro"
	}
	db, err := sql.Open("sqlite", "file:"+path+"?mode="+mode+"&_pragma=busy_timeout(8000)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	return db, nil
}

// fixSQLiteOwner gives the journal files the database file's owner.
func fixSQLiteOwner(path string) {
	fi, err := os.Stat(path)
	if err != nil {
		return
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return
	}
	for _, s := range []string{"", "-wal", "-shm", "-journal"} {
		os.Chown(path+s, int(st.Uid), int(st.Gid))
	}
}

func sqliteMetaQuery(what, table string) string {
	switch what {
	case "databases":
		return `SELECT 'main' AS name, (SELECT page_count FROM pragma_page_count) * (SELECT page_size FROM pragma_page_size) AS bytes`
	case "tables":
		return `SELECT name, NULL AS rows_est FROM sqlite_master WHERE type IN ('table','view') AND name NOT LIKE 'sqlite\_%' ESCAPE '\' ORDER BY 1`
	case "users":
		return `SELECT '' AS name WHERE 0`
	case "status":
		return `SELECT 'SQLite ' || sqlite_version() AS version, 0 AS connections`
	case "columns":
		return `SELECT name, type, CASE WHEN "notnull" THEN 'NO' ELSE 'YES' END AS nullable FROM pragma_table_info(` + lit(table) + `) ORDER BY cid`
	}
	return ""
}

// SQLiteInfo describes a file: size, settings, tables with row counts, the backups kept.
type SQLiteInfo struct {
	Path     string         `json:"path"`
	Bytes    int64          `json:"bytes"`
	WALBytes int64          `json:"wal_bytes"`
	PageSize int64          `json:"page_size"`
	Journal  string         `json:"journal_mode"`
	Version  string         `json:"version"`
	Owner    string         `json:"owner"`
	Tables   []SQLiteTable  `json:"tables"`
	Backups  []SQLiteBackup `json:"backups"`
	Modified int64          `json:"modified"`
}

type SQLiteTable struct {
	Name string `json:"name"`
	Rows int64  `json:"rows"`
}

type SQLiteBackup struct {
	Name  string `json:"name"`
	Bytes int64  `json:"bytes"`
	At    int64  `json:"at"`
}

func Info(ctx context.Context, path, key string) (SQLiteInfo, error) {
	in := SQLiteInfo{Path: path, Tables: []SQLiteTable{}, Backups: listSQLiteBackups(key)}
	db, err := openSQLite(path, true)
	if err != nil {
		return in, err
	}
	defer db.Close()
	fi, _ := os.Stat(path)
	in.Bytes, in.Modified = fi.Size(), fi.ModTime().Unix()
	if w, err := os.Stat(path + "-wal"); err == nil {
		in.WALBytes = w.Size()
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		in.Owner = ownerName(st.Uid)
	}
	db.QueryRowContext(ctx, `PRAGMA page_size`).Scan(&in.PageSize)
	db.QueryRowContext(ctx, `PRAGMA journal_mode`).Scan(&in.Journal)
	db.QueryRowContext(ctx, `SELECT sqlite_version()`).Scan(&in.Version)
	rows, err := db.QueryContext(ctx, `SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite\_%' ESCAPE '\' ORDER BY 1`)
	if err != nil {
		return in, connErr(err)
	}
	var names []string
	for rows.Next() {
		var n string
		rows.Scan(&n)
		names = append(names, n)
	}
	rows.Close()
	for _, n := range names {
		t := SQLiteTable{Name: n}
		db.QueryRowContext(ctx, `SELECT count(*) FROM "`+strings.ReplaceAll(n, `"`, `""`)+`"`).Scan(&t.Rows)
		in.Tables = append(in.Tables, t)
	}
	return in, nil
}

func ownerName(uid uint32) string {
	b, _ := os.ReadFile("/etc/passwd")
	for _, l := range strings.Split(string(b), "\n") {
		f := strings.Split(l, ":")
		if len(f) > 2 && f[2] == strconv.Itoa(int(uid)) {
			return f[0]
		}
	}
	return strconv.Itoa(int(uid))
}

var backupKeyRe = identRe

func listSQLiteBackups(key string) []SQLiteBackup {
	out := []SQLiteBackup{}
	if !backupKeyRe.MatchString(key) {
		return out
	}
	ents, _ := os.ReadDir(filepath.Join(SQLiteBackups, key))
	for _, e := range ents {
		if fi, err := e.Info(); err == nil && strings.HasSuffix(e.Name(), ".db") {
			out = append(out, SQLiteBackup{Name: e.Name(), Bytes: fi.Size(), At: fi.ModTime().Unix()})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name > out[j].Name })
	return out
}

// Backup writes a consistent copy (VACUUM INTO) to the instance's backup folder and keeps
// the newest 20. key names the folder (the instance id).
func Backup(ctx context.Context, path, key string) (SQLiteBackup, error) {
	if !backupKeyRe.MatchString(key) {
		return SQLiteBackup{}, errs.New(400, "db.bad_name")
	}
	db, err := openSQLite(path, true)
	if err != nil {
		return SQLiteBackup{}, err
	}
	defer db.Close()
	db.Close()
	dir := filepath.Join(SQLiteBackups, key)
	b, err := backupTo(ctx, path, dir)
	if err != nil {
		return b, err
	}
	list := listSQLiteBackups(key)
	for _, x := range list[min(len(list), 20):] {
		os.Remove(filepath.Join(dir, x.Name))
	}
	return b, nil
}

func backupTo(ctx context.Context, path, dir string) (SQLiteBackup, error) {
	db, err := openSQLite(path, true)
	if err != nil {
		return SQLiteBackup{}, err
	}
	defer db.Close()
	os.MkdirAll(dir, 0o700)
	name := time.Now().Format("20060102-150405.000") + ".db"
	dst := filepath.Join(dir, name)
	if _, err := db.ExecContext(ctx, `VACUUM INTO `+lit(dst)); err != nil {
		return SQLiteBackup{}, errs.New(500, "sqlite.backup_failed", "detail", err.Error())
	}
	os.Chmod(dst, 0o600)
	fi, _ := os.Stat(dst)
	return SQLiteBackup{Name: name, Bytes: fi.Size(), At: fi.ModTime().Unix()}, nil
}

// Restore puts a backup back over the file (its owner and mode are kept). Stale journal files
// go, or SQLite would replay them onto the restored copy.
func Restore(path, key, name string) error {
	if err := sqlitePathOK(path); err != nil {
		return err
	}
	if !backupKeyRe.MatchString(key) || strings.ContainsAny(name, "/\\") || !strings.HasSuffix(name, ".db") {
		return errs.New(400, "db.bad_name")
	}
	return restoreFrom(path, filepath.Join(SQLiteBackups, key, name))
}

func restoreFrom(path, src string) error {
	fi, err := os.Stat(path)
	if err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return errs.New(404, "sqlite.no_backup", "name", filepath.Base(src))
	}
	defer in.Close()
	tmp := path + ".berth-restore"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, fi.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	out.Close()
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		os.Chown(tmp, int(st.Uid), int(st.Gid))
	}
	os.Remove(path + "-wal")
	os.Remove(path + "-shm")
	os.Remove(path + "-journal")
	return os.Rename(tmp, path)
}

// Maintain: check (integrity, read-only), vacuum (VACUUM + ANALYZE), wal (journal_mode=WAL).
func Maintain(ctx context.Context, path, action string) (string, error) {
	db, err := openSQLite(path, action == "check")
	if err != nil {
		return "", err
	}
	defer db.Close()
	defer func() {
		if action != "check" {
			fixSQLiteOwner(path)
		}
	}()
	var out string
	switch action {
	case "check":
		rows, err := db.QueryContext(ctx, `PRAGMA integrity_check(20)`)
		if err != nil {
			return "", connErr(err)
		}
		var lines []string
		for rows.Next() {
			var l string
			rows.Scan(&l)
			lines = append(lines, l)
		}
		rows.Close()
		out = strings.Join(lines, "\n")
	case "vacuum":
		if _, err := db.ExecContext(ctx, `VACUUM`); err != nil {
			return "", connErr(err)
		}
		if _, err := db.ExecContext(ctx, `ANALYZE; PRAGMA optimize`); err != nil {
			return "", connErr(err)
		}
		fi, _ := os.Stat(path)
		out = strconv.FormatInt(fi.Size(), 10)
	case "wal":
		if err := db.QueryRowContext(ctx, `PRAGMA journal_mode=WAL`).Scan(&out); err != nil {
			return "", connErr(err)
		}
	default:
		return "", errs.New(400, "db.bad_request")
	}
	return out, nil
}

// Found is a SQLite file found on disk.
type Found struct {
	Path     string `json:"path"`
	Bytes    int64  `json:"bytes"`
	Modified int64  `json:"modified"`
	Owner    string `json:"owner"`
}

// SQLiteRoots are where Discover looks (with how deep it goes below each).
var SQLiteRoots = map[string]int{"/srv/berth/projects": 6, "/srv/berth/caddy/sites": 5, "/srv/berth/apps": 5, "/srv/code": 5, "/srv/berth/sftp": 5}

var skipDirs = map[string]bool{"node_modules": true, ".git": true, "vendor": true, ".next": true, "releases": true, ".venv": true, "__pycache__": true, "proc": true}

// Discover walks the roots for SQLite files (at most 5000 files looked at).
func Discover() []Found {
	out := []Found{}
	seen := 0
	for root, depth := range SQLiteRoots {
		base := strings.Count(root, "/")
		filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() {
				if skipDirs[d.Name()] || strings.Count(p, "/")-base >= depth {
					return filepath.SkipDir
				}
				return nil
			}
			if seen++; seen > 5000 {
				return filepath.SkipAll
			}
			if d.Type()&os.ModeSymlink != 0 || strings.HasSuffix(p, "-wal") || strings.HasSuffix(p, "-shm") || strings.HasSuffix(p, "-journal") {
				return nil
			}
			if isSQLite(p) {
				fi, _ := os.Stat(p)
				f := Found{Path: p, Bytes: fi.Size(), Modified: fi.ModTime().Unix()}
				if st, ok := fi.Sys().(*syscall.Stat_t); ok {
					f.Owner = ownerName(st.Uid)
				}
				out = append(out, f)
			}
			return nil
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}
