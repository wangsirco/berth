package system

// Server migration (R6), the native database part: logical dumps that restore on any version
// of the same family, the sizes the target needs for them, and the logins with their password
// hashes so applications keep connecting with what they had.

import (
	"bufio"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/wangsirco/berth/internal/errs"
)

// EnsureNativeUser makes a native project's system user (site-<name>) if it is missing.
func EnsureNativeUser(ctx context.Context, name string) error {
	if !nativeNameRe.MatchString(name) {
		return errs.New(400, "native.bad_name")
	}
	_, _, err := ensureNativeUser(ctx, name)
	return err
}

// NativeDB returns the native server of a family ("mysql", "postgres", "redis").
func NativeDB(family string) (DBNative, bool) { return readDBNative(family) }

var mysqlSystemDBs = map[string]bool{"mysql": true, "information_schema": true, "performance_schema": true, "sys": true}
var pgSystemDBs = map[string]bool{"postgres": true, "template0": true, "template1": true}

// DBSizes: the user databases of a native server and how much each takes on disk.
func DBSizes(ctx context.Context, engine string) (map[string]int64, error) {
	out := map[string]int64{}
	switch dbFamily(engine) {
	case "mysql":
		s, err := rootSQL(ctx, engine, "SELECT s.schema_name, COALESCE(SUM(t.data_length+t.index_length),0) FROM information_schema.schemata s LEFT JOIN information_schema.tables t ON t.table_schema=s.schema_name GROUP BY s.schema_name;")
		if err != nil {
			return nil, err
		}
		for _, l := range strings.Split(s, "\n") {
			f := strings.Split(l, "\t")
			if len(f) == 2 && !mysqlSystemDBs[f[0]] {
				n, _ := strconv.ParseFloat(f[1], 64)
				out[f[0]] = int64(n)
			}
		}
	case "postgres":
		s, err := rootSQL(ctx, engine, "SELECT datname, pg_database_size(datname) FROM pg_database WHERE NOT datistemplate;")
		if err != nil {
			return nil, err
		}
		for _, l := range strings.Split(s, "\n") {
			f := strings.Split(l, "|")
			if len(f) == 2 && !pgSystemDBs[f[0]] {
				n, _ := strconv.ParseInt(f[1], 10, 64)
				out[f[0]] = n
			}
		}
	case "redis":
		if fi, err := os.Stat(redisRDB()); err == nil {
			out["data"] = fi.Size()
		} else {
			out["data"] = 0
		}
	}
	return out, nil
}

func redisDataDir() string {
	if debianLike() {
		return "/var/lib/redis"
	}
	return "/var/lib/valkey"
}

func redisRDB() string { return filepath.Join(redisDataDir(), "dump.rdb") }

func client(engine string) string {
	if engine == "mariadb" {
		return "mariadb"
	}
	return "mysql"
}

func dumpBin(engine string) string {
	if engine == "mariadb" {
		return "mariadb-dump"
	}
	return "mysqldump"
}

// DumpDB writes a gzipped logical dump of one database to w. The dump creates the database
// itself, so restoring it needs nothing else.
func DumpDB(ctx context.Context, engine, db string, w io.Writer) error {
	gz := gzip.NewWriter(w)
	var cmd *exec.Cmd
	switch dbFamily(engine) {
	case "mysql":
		if mysqlSystemDBs[db] || !dbIdentRe.MatchString(db) {
			return errs.New(400, "db.bad_request")
		}
		cmd = exec.CommandContext(ctx, dumpBin(engine), "-uroot", "--single-transaction", "--routines", "--events", "--triggers", "--hex-blob", "--databases", db)
	case "postgres":
		if pgSystemDBs[db] || !dbIdentRe.MatchString(db) {
			return errs.New(400, "db.bad_request")
		}
		cmd = exec.CommandContext(ctx, "runuser", "-u", "postgres", "--", "pg_dump", "--create", "--no-password", db)
	default:
		return errs.New(400, "db.bad_engine", "engine", engine)
	}
	cmd.Stdout = gz
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("dump %s: %v %s", db, err, strings.TrimSpace(stderr.String()))
	}
	return gz.Close()
}

var dbIdentRe = regexp.MustCompile(`^[A-Za-z0-9_$-]{1,64}$`)

// DumpUsers writes SQL that recreates the server's application logins with their password
// hashes and grants (root and the server's own accounts are left out).
func DumpUsers(ctx context.Context, engine string, w io.Writer) error {
	switch dbFamily(engine) {
	case "mysql":
		s, err := rootSQL(ctx, engine, "SELECT CONCAT(QUOTE(user),'@',QUOTE(host)) FROM mysql.user WHERE user NOT IN ('root','mysql','mysql.sys','mysql.session','mysql.infoschema','mariadb.sys','') AND user NOT LIKE 'berth%';")
		if err != nil {
			return err
		}
		for _, u := range strings.Split(s, "\n") {
			if u == "" {
				continue
			}
			pre := ""
			if engine == "mysql" { // caching_sha2 hashes are binary; print them as hex
				pre = "SET SESSION print_identified_with_as_hex=ON; "
			}
			cu, err := rawSQL(ctx, engine, pre+"SHOW CREATE USER "+u+";")
			if err != nil {
				continue
			}
			stmt := strings.Replace(cu, "CREATE USER ", "CREATE USER IF NOT EXISTS ", 1)
			fmt.Fprintf(w, "%s;\n", stmt)
			gr, _ := rawSQL(ctx, engine, "SHOW GRANTS FOR "+u+";")
			for _, g := range strings.Split(gr, "\n") {
				if g != "" && !strings.HasPrefix(g, "GRANT USAGE") {
					fmt.Fprintf(w, "%s;\n", g)
				}
			}
		}
		fmt.Fprintln(w, "FLUSH PRIVILEGES;")
		return nil
	case "postgres":
		cmd := exec.CommandContext(ctx, "runuser", "-u", "postgres", "--", "pg_dumpall", "--roles-only", "--no-password")
		out, err := cmd.Output()
		if err != nil {
			return fmt.Errorf("pg_dumpall: %v", err)
		}
		sc := bufio.NewScanner(strings.NewReader(string(out)))
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		for sc.Scan() {
			l := sc.Text()
			// the postgres superuser exists on the target already; berth's own login too
			if strings.Contains(l, "ROLE postgres") || strings.Contains(l, "ROLE berth") {
				continue
			}
			fmt.Fprintln(w, l)
		}
		return nil
	}
	return errs.New(400, "db.bad_engine", "engine", engine)
}

// RestoreSQL runs SQL (gzipped when gz) against a native server as its administrator.
// Users already there are kept (lenient), databases must not exist yet (strict).
func RestoreSQL(ctx context.Context, engine string, r io.Reader, gz, lenient bool) error {
	in := r
	if gz {
		z, err := gzip.NewReader(r)
		if err != nil {
			return err
		}
		defer z.Close()
		in = z
	}
	var cmd *exec.Cmd
	switch dbFamily(engine) {
	case "mysql":
		args := []string{"-uroot"}
		if lenient {
			args = append(args, "--force")
		}
		cmd = exec.CommandContext(ctx, client(engine), args...)
	case "postgres":
		stop := "ON_ERROR_STOP=1"
		if lenient {
			stop = "ON_ERROR_STOP=0"
		}
		cmd = exec.CommandContext(ctx, "runuser", "-u", "postgres", "--", "psql", "-X", "-q", "-v", stop, "-d", "postgres")
	default:
		return errs.New(400, "db.bad_engine", "engine", engine)
	}
	cmd.Stdin = in
	var out strings.Builder
	cmd.Stdout, cmd.Stderr = io.Discard, &out
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(out.String())
		if len(msg) > 400 {
			msg = msg[len(msg)-400:]
		}
		return fmt.Errorf("restore: %v %s", err, msg)
	}
	return nil
}

// RedisDump writes a point-in-time RDB of the native Redis (Valkey) to w.
func RedisDump(ctx context.Context, w io.Writer) error {
	d, ok := readDBNative("redis")
	if !ok {
		return errs.New(404, "dbn.not_installed")
	}
	before, _ := redisCmd(ctx, d.Pass, "LASTSAVE")
	if out, err := redisCmd(ctx, d.Pass, "BGSAVE"); err != nil && !strings.Contains(out, "already") {
		return errors.New("BGSAVE: " + out)
	}
	for i := 0; i < 600; i++ {
		now, _ := redisCmd(ctx, d.Pass, "LASTSAVE")
		if now != before {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	f, err := os.Open(redisRDB())
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(w, f)
	return err
}

// RedisRestore replaces the native Redis data with an RDB. With append-only on, Redis would
// ignore the RDB at start, so it starts once with append-only off and then switches it back on,
// which writes the AOF from what was loaded.
func RedisRestore(ctx context.Context, r io.Reader) error {
	d, ok := readDBNative("redis")
	if !ok {
		return errs.New(404, "dbn.not_installed")
	}
	svc := d.Service
	run(ctx, "systemctl", "stop", svc)
	dir := redisDataDir()
	tmp := filepath.Join(dir, "dump.rdb.berth")
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o640)
	if err != nil {
		run(ctx, "systemctl", "start", svc)
		return err
	}
	_, err = io.Copy(f, r)
	f.Close()
	if err != nil {
		os.Remove(tmp)
		run(ctx, "systemctl", "start", svc)
		return err
	}
	os.Rename(tmp, redisRDB())
	exec.CommandContext(ctx, "chown", "--reference="+dir, redisRDB()).Run()
	os.RemoveAll(filepath.Join(dir, "appendonlydir"))
	aof := d.Values["appendonly"] != "no"
	if aof {
		tmpD := d
		tmpD.Values = map[string]string{}
		for k, v := range d.Values {
			tmpD.Values[k] = v
		}
		tmpD.Values["appendonly"] = "no"
		writeRedisConfig(tmpD)
	}
	if out, err := run(ctx, "systemctl", "start", svc); err != nil {
		writeRedisConfig(d)
		return errors.New(out)
	}
	if err := waitDB(ctx, "redis", 60*time.Second); err != nil {
		writeRedisConfig(d)
		return err
	}
	if aof {
		redisCmd(ctx, d.Pass, "CONFIG", "SET", "appendonly", "yes")
		writeRedisConfig(d)
	}
	return nil
}

// rawSQL is rootSQL without batch-mode escaping, for statements printed back as SQL.
func rawSQL(ctx context.Context, engine, sql string) (string, error) {
	cmd := exec.CommandContext(ctx, client(engine), "-uroot", "-N", "-B", "-r")
	cmd.Stdin = strings.NewReader(sql)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", errors.New(strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

// DumpDBAuth dumps one database of a MySQL server Berth did not install (宝塔's), logging in
// as root with a password (given in the environment, never on the command line).
func DumpDBAuth(ctx context.Context, db, pass string, w io.Writer) error {
	if mysqlSystemDBs[db] || !dbIdentRe.MatchString(db) {
		return errs.New(400, "db.bad_request")
	}
	bin := "mysqldump"
	for _, b := range []string{"/www/server/mysql/bin/mysqldump", "/usr/bin/mariadb-dump"} {
		if _, err := os.Stat(b); err == nil {
			bin = b
			break
		}
	}
	args := []string{"-uroot", "--single-transaction", "--routines", "--events", "--triggers", "--hex-blob", "--databases", db}
	if _, err := os.Stat("/tmp/mysql.sock"); err == nil {
		args = append([]string{"-S", "/tmp/mysql.sock"}, args...)
	}
	gz := gzip.NewWriter(w)
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = append(os.Environ(), "MYSQL_PWD="+pass)
	cmd.Stdout = gz
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("dump %s: %v %s", db, err, strings.TrimSpace(stderr.String()))
	}
	return gz.Close()
}
