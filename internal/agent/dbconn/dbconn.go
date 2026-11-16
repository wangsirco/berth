// Package dbconn lets the agent talk to MySQL/MariaDB, PostgreSQL and Redis on behalf of
// the panel (方案 §5.7): metadata, queries (read-only by default), admin statements and
// logical dumps (run inside the database's own container).
package dbconn

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/redis/go-redis/v9"

	"github.com/wangsirco/berth/internal/agent"
	"github.com/wangsirco/berth/internal/errs"
)

// Target says where a database is and how to sign in.
type Target struct {
	Engine   string `json:"engine"` // postgres | mysql | mariadb | redis
	Addr     string `json:"addr"`   // host:port
	User     string `json:"user"`
	Password string `json:"password"`
	Database string `json:"database,omitempty"`
}

func (t Target) driver() (string, string, error) {
	switch t.Engine {
	case "postgres":
		db := t.Database
		if db == "" {
			db = "postgres"
		}
		u := url.URL{Scheme: "postgres", User: url.UserPassword(t.User, t.Password), Host: t.Addr, Path: "/" + db, RawQuery: "sslmode=disable&connect_timeout=8"}
		return "pgx", u.String(), nil
	case "mysql", "mariadb":
		c := mysql.NewConfig()
		c.User, c.Passwd, c.Net, c.Addr, c.DBName = t.User, t.Password, "tcp", t.Addr, t.Database
		c.Timeout, c.ReadTimeout = 8*time.Second, 60*time.Second
		c.ParseTime = false
		return "mysql", c.FormatDSN(), nil
	}
	return "", "", errs.New(400, "db.bad_engine", "engine", t.Engine)
}

func open(t Target) (*sql.DB, error) { return openMode(t, false) }

func openMode(t Target, readonly bool) (*sql.DB, error) {
	if t.Engine == "sqlite" {
		return openSQLite(t.Addr, readonly)
	}
	if _, _, err := net.SplitHostPort(t.Addr); err != nil {
		return nil, errs.New(400, "db.bad_addr", "addr", t.Addr)
	}
	drv, dsn, err := t.driver()
	if err != nil {
		return nil, err
	}
	db, err := sql.Open(drv, dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(2)
	db.SetConnMaxLifetime(time.Minute)
	return db, nil
}

func connErr(err error) error {
	if err == nil {
		return nil
	}
	return errs.New(502, "db.error", "detail", err.Error())
}

// Result is a query result (capped).
type Result struct {
	Columns   []string `json:"columns"`
	Rows      [][]any  `json:"rows"`
	Affected  int64    `json:"affected"`
	Truncated bool     `json:"truncated"`
	MS        int64    `json:"ms"`
}

const maxRows = 1000

func scan(rows *sql.Rows, limit int) (Result, error) {
	cols, _ := rows.Columns()
	res := Result{Columns: cols, Rows: [][]any{}}
	for rows.Next() {
		if len(res.Rows) >= limit {
			res.Truncated = true
			break
		}
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return res, err
		}
		for i, v := range vals {
			switch x := v.(type) {
			case []byte:
				if len(x) > 4096 {
					vals[i] = string(x[:4096]) + "…"
				} else {
					vals[i] = string(x)
				}
			case time.Time:
				vals[i] = x.Format(time.RFC3339)
			}
		}
		res.Rows = append(res.Rows, vals)
	}
	return res, rows.Err()
}

// Query runs sql. readonly wraps it in a read-only transaction that is rolled back.
func Query(ctx context.Context, t Target, q string, readonly bool, limit int) (Result, error) {
	if limit <= 0 || limit > maxRows {
		limit = maxRows
	}
	db, err := openMode(t, readonly)
	if err != nil {
		return Result{}, err
	}
	defer db.Close()
	if t.Engine == "sqlite" && !readonly {
		defer fixSQLiteOwner(t.Addr)
	}
	start := time.Now()
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	if !readonly && t.Engine == "postgres" && noTxRe.MatchString(stripComments(q)) {
		// PostgreSQL refuses these inside a transaction block
		if returnsRows(q) {
			rows, err := db.QueryContext(ctx, q)
			if err != nil {
				return Result{}, connErr(err)
			}
			defer rows.Close()
			res, err := scan(rows, limit)
			res.MS = time.Since(start).Milliseconds()
			return res, connErr(err)
		}
		r, err := db.ExecContext(ctx, q)
		if err != nil {
			return Result{}, connErr(err)
		}
		res := Result{Columns: []string{}, Rows: [][]any{}}
		res.Affected, _ = r.RowsAffected()
		res.MS = time.Since(start).Milliseconds()
		return res, nil
	}
	// a read-only SQLite connection is already read-only (its driver has no read-only transactions)
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: readonly && t.Engine != "sqlite"})
	if err != nil {
		return Result{}, connErr(err)
	}
	defer tx.Rollback()
	var res Result
	if returnsRows(q) {
		rows, err := tx.QueryContext(ctx, q)
		if err != nil {
			return Result{}, connErr(err)
		}
		res, err = scan(rows, limit)
		rows.Close()
		if err != nil {
			return res, connErr(err)
		}
	} else {
		r, err := tx.ExecContext(ctx, q)
		if err != nil {
			return Result{}, connErr(err)
		}
		res.Affected, _ = r.RowsAffected()
		res.Columns, res.Rows = []string{}, [][]any{}
	}
	if !readonly {
		if err := tx.Commit(); err != nil {
			return res, connErr(err)
		}
	}
	res.MS = time.Since(start).Milliseconds()
	return res, nil
}

var noTxRe = regexp.MustCompile(`(?is)^\s*(alter\s+system|vacuum|create\s+database|drop\s+database|reindex\s+(database|system)|create\s+index\s+concurrently|drop\s+index\s+concurrently|cluster)\b`)

var rowsRe = regexp.MustCompile(`(?is)^\s*(select|with|show|describe|desc|explain|values|table)\b`)

func returnsRows(q string) bool { return rowsRe.MatchString(stripComments(q)) }

var commentRe = regexp.MustCompile(`(?s)/\*.*?\*/|--[^\n]*`)

func stripComments(q string) string { return strings.TrimSpace(commentRe.ReplaceAllString(q, " ")) }

// Danger classifies statements that need a second confirmation.
func Danger(q string) string {
	s := strings.ToLower(stripComments(q))
	switch {
	case regexp.MustCompile(`\bdrop\s+(database|schema|table|user|role)\b`).MatchString(s):
		return "drop"
	case regexp.MustCompile(`\btruncate\b`).MatchString(s):
		return "truncate"
	case regexp.MustCompile(`\b(delete\s+from|update)\b`).MatchString(s) && !regexp.MustCompile(`\bwhere\b`).MatchString(s):
		return "no_where"
	case regexp.MustCompile(`\balter\s+table\b.*\bdrop\b`).MatchString(s):
		return "drop"
	}
	return ""
}

// Meta answers the browser's questions: databases, tables, columns, users, size.
func Meta(ctx context.Context, t Target, what, table string) (Result, error) {
	var q string
	switch t.Engine {
	case "postgres":
		switch what {
		case "databases":
			q = `SELECT datname AS name, pg_database_size(datname) AS bytes FROM pg_database WHERE NOT datistemplate ORDER BY 1`
		case "tables":
			q = `SELECT table_schema || '.' || table_name AS name, (SELECT reltuples::bigint FROM pg_class WHERE oid = (quote_ident(table_schema)||'.'||quote_ident(table_name))::regclass) AS rows_est FROM information_schema.tables WHERE table_schema NOT IN ('pg_catalog','information_schema') ORDER BY 1`
		case "users":
			q = `SELECT rolname AS name, rolsuper AS superuser, rolcanlogin AS login FROM pg_roles WHERE rolname NOT LIKE 'pg\_%' ORDER BY 1`
		case "status":
			q = `SELECT version() AS version, (SELECT count(*) FROM pg_stat_activity) AS connections`
		}
	case "mysql", "mariadb":
		switch what {
		case "databases":
			q = `SELECT s.schema_name AS name, COALESCE(SUM(t.data_length + t.index_length), 0) AS bytes FROM information_schema.schemata s LEFT JOIN information_schema.tables t ON t.table_schema = s.schema_name GROUP BY s.schema_name ORDER BY 1`
		case "tables":
			q = `SELECT table_name AS name, table_rows AS rows_est FROM information_schema.tables WHERE table_schema = DATABASE() ORDER BY 1`
		case "users":
			q = `SELECT CONCAT(user, '@', host) AS name FROM mysql.user ORDER BY 1`
		case "status":
			q = `SELECT VERSION() AS version, (SELECT COUNT(*) FROM information_schema.processlist) AS connections`
		}
	}
	if t.Engine == "sqlite" && what != "columns" {
		q = sqliteMetaQuery(what, "")
	}
	if what == "columns" && table != "" {
		if !regexp.MustCompile(`^[A-Za-z0-9_.$-]{1,128}$`).MatchString(table) {
			return Result{}, errs.New(400, "db.bad_name")
		}
		schema, name := "", table
		if i := strings.LastIndex(table, "."); i > 0 {
			schema, name = table[:i], table[i+1:]
		}
		if t.Engine == "sqlite" {
			q = sqliteMetaQuery("columns", table)
		} else if t.Engine == "postgres" {
			q = fmt.Sprintf(`SELECT column_name AS name, data_type AS type, is_nullable AS nullable FROM information_schema.columns WHERE table_name = '%s' AND table_schema = '%s' ORDER BY ordinal_position`, name, firstNonEmpty(schema, "public"))
		} else {
			q = fmt.Sprintf(`SELECT column_name AS name, column_type AS type, is_nullable AS nullable FROM information_schema.columns WHERE table_schema = DATABASE() AND table_name = '%s' ORDER BY ordinal_position`, name)
		}
	}
	if q == "" {
		return Result{}, errs.New(400, "db.bad_request")
	}
	return Query(ctx, t, q, true, maxRows)
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// ---- admin ----

var identRe = regexp.MustCompile(`^[A-Za-z0-9_]{1,63}$`)

func quote(engine, s string) string {
	if engine == "postgres" {
		return `"` + s + `"`
	}
	return "`" + s + "`"
}

func lit(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

// Admin runs a fixed set of administration actions with checked identifiers.
func Admin(ctx context.Context, t Target, action, name, user, password string) error {
	if t.Engine == "sqlite" {
		return errs.New(400, "sqlite.no_admin")
	}
	for _, v := range []string{name, user} {
		if v != "" && !identRe.MatchString(v) {
			return errs.New(400, "db.bad_name")
		}
	}
	var stmts []string
	q := func(s string) string { return quote(t.Engine, s) }
	switch t.Engine + ":" + action {
	case "postgres:create_db":
		stmts = []string{"CREATE DATABASE " + q(name)}
		if user != "" {
			stmts[0] += " OWNER " + q(user)
		}
	case "postgres:drop_db":
		stmts = []string{"DROP DATABASE " + q(name)}
	case "postgres:create_user":
		stmts = []string{"CREATE ROLE " + q(user) + " LOGIN PASSWORD " + lit(password)}
		if name != "" {
			stmts = append(stmts, "GRANT ALL PRIVILEGES ON DATABASE "+q(name)+" TO "+q(user))
		}
	case "postgres:password":
		stmts = []string{"ALTER ROLE " + q(user) + " PASSWORD " + lit(password)}
	case "postgres:drop_user":
		stmts = []string{"DROP ROLE " + q(user)}
	case "mysql:create_db", "mariadb:create_db":
		stmts = []string{"CREATE DATABASE " + q(name) + " CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci"}
	case "mysql:drop_db", "mariadb:drop_db":
		stmts = []string{"DROP DATABASE " + q(name)}
	case "mysql:create_user", "mariadb:create_user":
		stmts = []string{"CREATE USER " + lit(user) + "@'%' IDENTIFIED BY " + lit(password)}
		if name != "" {
			stmts = append(stmts, "GRANT ALL PRIVILEGES ON "+q(name)+".* TO "+lit(user)+"@'%'")
		}
	case "mysql:password", "mariadb:password":
		stmts = []string{"ALTER USER " + lit(user) + "@'%' IDENTIFIED BY " + lit(password)}
	case "mysql:drop_user", "mariadb:drop_user":
		stmts = []string{"DROP USER " + lit(user) + "@'%'"}
	default:
		return errs.New(400, "db.bad_request")
	}
	db, err := open(t)
	if err != nil {
		return err
	}
	defer db.Close()
	for _, s := range stmts {
		if _, err := db.ExecContext(ctx, s); err != nil {
			return connErr(err)
		}
	}
	return nil
}

// ---- Redis ----

var redisRead = map[string]bool{"scan": true, "get": true, "type": true, "ttl": true, "hgetall": true, "lrange": true, "smembers": true,
	"zrange": true, "info": true, "dbsize": true, "strlen": true, "hlen": true, "llen": true, "scard": true, "zcard": true, "exists": true}

// Redis runs one command; writes are refused unless allowWrite.
func Redis(ctx context.Context, t Target, args []string, allowWrite bool) (any, error) {
	if len(args) == 0 {
		return nil, errs.New(400, "db.bad_request")
	}
	if !redisRead[strings.ToLower(args[0])] && !allowWrite {
		return nil, errs.New(403, "db.redis_write")
	}
	c := redis.NewClient(&redis.Options{Addr: t.Addr, Username: t.User, Password: t.Password, DialTimeout: 5 * time.Second, ReadTimeout: 20 * time.Second})
	defer c.Close()
	a := make([]any, len(args))
	for i, s := range args {
		a[i] = s
	}
	v, err := c.Do(ctx, a...).Result()
	if err == redis.Nil {
		return nil, nil
	}
	return v, connErr(err)
}

// ---- dumps ----

// Dump writes a logical backup (gzip) of one database by running the engine's dump tool
// inside the database container, or on this host for a native database.
func Dump(ctx context.Context, container string, t Target, path string) (int64, error) {
	if !identRe.MatchString(t.Database) {
		return 0, errs.New(400, "db.bad_name")
	}
	var dump *exec.Cmd
	switch t.Engine {
	case "postgres":
		dump = toolCmd(ctx, container, t, "dump", "-U", t.User, "--clean", "--if-exists", t.Database)
	case "mysql", "mariadb":
		dump = toolCmd(ctx, container, t, "dump", "-u", t.User, "--single-transaction", "--routines", "--triggers", "--databases", t.Database)
	default:
		return 0, errs.New(400, "db.bad_engine", "engine", t.Engine)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return 0, err
	}
	f, err := os.OpenFile(path+".tmp", os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return 0, err
	}
	gz := exec.CommandContext(ctx, "gzip", "-c")
	pipe, _ := dump.StdoutPipe()
	gz.Stdin, gz.Stdout = pipe, f
	var stderr strings.Builder
	dump.Stderr = &stderr
	if err := gz.Start(); err != nil {
		f.Close()
		return 0, err
	}
	if err := dump.Run(); err != nil {
		gz.Wait()
		f.Close()
		os.Remove(path + ".tmp")
		return 0, errs.New(500, "db.dump_failed", "detail", strings.TrimSpace(stderr.String()+" "+err.Error()))
	}
	gz.Wait()
	f.Close()
	fi, _ := os.Stat(path + ".tmp")
	return fi.Size(), os.Rename(path+".tmp", path)
}

// ---- routes ----

func Register(s *agent.Server) {
	s.Handle("POST /v1/db/query", func(w http.ResponseWriter, r *http.Request, body []byte) {
		var in struct {
			Target
			SQL      string `json:"sql"`
			ReadOnly bool   `json:"readonly"`
			Limit    int    `json:"limit"`
		}
		json.Unmarshal(body, &in)
		res, err := Query(r.Context(), in.Target, in.SQL, in.ReadOnly, in.Limit)
		reply(w, res, err)
	})
	s.Handle("POST /v1/db/meta", func(w http.ResponseWriter, r *http.Request, body []byte) {
		var in struct {
			Target
			What  string `json:"what"`
			Table string `json:"table"`
		}
		json.Unmarshal(body, &in)
		res, err := Meta(r.Context(), in.Target, in.What, in.Table)
		reply(w, res, err)
	})
	s.Handle("POST /v1/db/admin", func(w http.ResponseWriter, r *http.Request, body []byte) {
		var in struct {
			Target
			Action, Name, User, NewPassword string
		}
		json.Unmarshal(body, &in)
		reply(w, map[string]bool{"ok": true}, Admin(r.Context(), in.Target, in.Action, in.Name, in.User, in.NewPassword))
	})
	s.Handle("POST /v1/db/redis", func(w http.ResponseWriter, r *http.Request, body []byte) {
		var in struct {
			Target
			Args  []string `json:"args"`
			Write bool     `json:"write"`
		}
		json.Unmarshal(body, &in)
		v, err := Redis(r.Context(), in.Target, in.Args, in.Write)
		reply(w, map[string]any{"value": v}, err)
	})
	s.Handle("POST /v1/db/import", func(w http.ResponseWriter, r *http.Request, body []byte) {
		var in struct {
			Target
			Container string `json:"container"`
			Path      string `json:"path"`
			Replace   bool   `json:"replace"`
		}
		json.Unmarshal(body, &in)
		reply(w, map[string]bool{"ok": true}, Import(r.Context(), in.Container, in.Target, in.Path, in.Replace))
	})
	s.Handle("POST /v1/sqlite/info", func(w http.ResponseWriter, r *http.Request, body []byte) {
		var in struct{ Path, Key string }
		json.Unmarshal(body, &in)
		res, err := Info(r.Context(), in.Path, in.Key)
		reply(w, res, err)
	})
	s.Handle("POST /v1/sqlite/backup", func(w http.ResponseWriter, r *http.Request, body []byte) {
		var in struct{ Path, Key string }
		json.Unmarshal(body, &in)
		res, err := Backup(r.Context(), in.Path, in.Key)
		reply(w, res, err)
	})
	s.Handle("POST /v1/sqlite/restore", func(w http.ResponseWriter, r *http.Request, body []byte) {
		var in struct{ Path, Key, Name string }
		json.Unmarshal(body, &in)
		reply(w, map[string]bool{"ok": true}, Restore(in.Path, in.Key, in.Name))
	})
	s.Handle("POST /v1/sqlite/maintain", func(w http.ResponseWriter, r *http.Request, body []byte) {
		var in struct{ Path, Action string }
		json.Unmarshal(body, &in)
		out, err := Maintain(r.Context(), in.Path, in.Action)
		reply(w, map[string]string{"result": out}, err)
	})
	s.Handle("GET /v1/sqlite/discover", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		reply(w, map[string]any{"items": Discover()}, nil)
	})
	s.Handle("POST /v1/db/dump", func(w http.ResponseWriter, r *http.Request, body []byte) {
		var in struct {
			Target
			Container string `json:"container"`
			Path      string `json:"path"`
		}
		json.Unmarshal(body, &in)
		if !strings.HasPrefix(filepath.Clean(in.Path), "/srv/berth/backups/") {
			agent.WriteErr(w, errs.New(400, "db.bad_path"))
			return
		}
		n, err := Dump(r.Context(), in.Container, in.Target, filepath.Clean(in.Path))
		reply(w, map[string]any{"bytes": n, "path": in.Path}, err)
	})
}

func reply(w http.ResponseWriter, v any, err error) {
	if err != nil {
		agent.WriteErr(w, err)
		return
	}
	agent.WriteJSON(w, 200, v)
}
