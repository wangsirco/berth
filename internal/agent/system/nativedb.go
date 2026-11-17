package system

// Native database servers (R4): MariaDB or MySQL, and PostgreSQL, from the distribution's
// packages — one of each family per host. Berth owns one config file per server (tuning and
// listening) and the berth_db nft table that keeps the ports to this host, its containers and
// the sources the user allows.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/wangsirco/berth/internal/errs"
	"github.com/wangsirco/berth/internal/sysexec"
)

const dbStateDir = "/etc/berth/db"

// DBNative is one native database server.
type DBNative struct {
	Engine  string            `json:"engine"`  // mariadb | mysql | postgres
	Version string            `json:"version"` // 11.8, 12, 8.4, 9, 17, 18
	Port    int               `json:"port"`
	Service string            `json:"service"`
	Active  bool              `json:"active"`
	DataDir string            `json:"data_dir"`
	Allow   []string          `json:"allow"`          // remote sources let through the firewall
	Values  map[string]string `json:"values"`         // tuning Berth wrote
	MemMB   int               `json:"mem_mb"`         // what the tuning was sized for
	Pass    string            `json:"pass,omitempty"` // Redis: its password lives in its config (this file is root-only)
}

func dbFamily(engine string) string {
	switch engine {
	case "postgres", "redis":
		return engine
	}
	return "mysql"
}

var dbFamilies = []string{"mysql", "postgres", "redis"}

func dbStatePath(family string) string { return filepath.Join(dbStateDir, family+".json") }

func readDBNative(family string) (DBNative, bool) {
	var d DBNative
	b, err := os.ReadFile(dbStatePath(family))
	if err != nil || json.Unmarshal(b, &d) != nil {
		return d, false
	}
	if d.Allow == nil {
		d.Allow = []string{}
	}
	if d.Values == nil {
		d.Values = map[string]string{}
	}
	return d, true
}

func writeDBNative(d DBNative) error {
	os.MkdirAll(dbStateDir, 0o700)
	b, _ := json.MarshalIndent(d, "", " ")
	return os.WriteFile(dbStatePath(dbFamily(d.Engine)), b, 0o600)
}

// DBNatives lists the native servers Berth installed, with their service state.
func DBNatives(ctx context.Context) []DBNative {
	out := []DBNative{}
	for _, f := range dbFamilies {
		if d, ok := readDBNative(f); ok {
			o, _ := run(ctx, "systemctl", "is-active", d.Service)
			d.Active = strings.TrimSpace(o) == "active"
			out = append(out, d)
		}
	}
	return out
}

type dbPkg struct {
	pkgs    []string
	service string
	port    int
	dataDir string
}

// dbPackages: what to install for an engine and version on this system.
func dbPackages(engine, version string) (dbPkg, error) {
	deb := debianLike()
	switch engine {
	case "mariadb":
		p := dbPkg{service: "mariadb", port: 3306, dataDir: "/var/lib/mysql"}
		switch {
		case deb:
			p.pkgs = []string{"mariadb-server", "mariadb-client"}
		case version == "11.8":
			p.pkgs = []string{"mariadb11.8-server", "mariadb11.8"}
		case version == "12":
			p.pkgs = []string{"mariadb-server", "mariadb"}
		default:
			return p, errs.New(400, "dbn.bad_version", "version", version)
		}
		return p, nil
	case "mysql":
		p := dbPkg{service: "mysqld", port: 3306, dataDir: "/var/lib/mysql"}
		switch {
		case deb:
			p.pkgs, p.service = []string{"mysql-server", "mysql-client"}, "mysql"
		case version == "8.4":
			p.pkgs = []string{"mysql8.4-server", "mysql8.4"}
		case version == "9":
			p.pkgs = []string{"mysql-server", "mysql"}
		default:
			return p, errs.New(400, "dbn.bad_version", "version", version)
		}
		return p, nil
	case "postgres":
		p := dbPkg{service: "postgresql", port: 5432, dataDir: "/var/lib/pgsql/data"}
		switch {
		case deb:
			p.pkgs, p.dataDir = []string{"postgresql-" + version}, "/var/lib/postgresql/"+version+"/main"
		case version == "17":
			p.pkgs = []string{"postgresql17-server", "postgresql17"}
		case version == "18":
			p.pkgs = []string{"postgresql-server", "postgresql"}
		default:
			return p, errs.New(400, "dbn.bad_version", "version", version)
		}
		return p, nil
	case "redis":
		if debianLike() {
			return dbPkg{pkgs: []string{"redis-server"}, service: "redis-server", port: 6379, dataDir: "/var/lib/redis"}, nil
		}
		return dbPkg{pkgs: []string{"valkey"}, service: "valkey", port: 6379, dataDir: "/var/lib/valkey"}, nil
	}
	return dbPkg{}, errs.New(400, "db.bad_engine", "engine", engine)
}

// pgConfDir is where postgresql.conf lives (Debian keeps it under /etc).
func pgConfDir(d DBNative) string {
	if debianLike() {
		return "/etc/postgresql/" + d.Version + "/main"
	}
	return d.DataDir
}

// portBusy says who listens on a TCP port ("" when free).
func portBusy(ctx context.Context, port int) string {
	out, _ := run(ctx, "ss", "-Hltnp", "sport = :"+strconv.Itoa(port))
	out = strings.TrimSpace(out)
	if out == "" {
		return ""
	}
	if m := regexp.MustCompile(`users:\(\("([^"]+)"`).FindStringSubmatch(out); m != nil {
		return m[1]
	}
	return "?"
}

// mysqlDataEngine tells which server last used a MySQL data folder ("" when empty).
func mysqlDataEngine(dir string) string {
	if ents, err := os.ReadDir(dir); err != nil || len(ents) == 0 {
		return ""
	}
	if _, err := os.Stat(filepath.Join(dir, "aria_log_control")); err == nil {
		return "mariadb"
	}
	return "mysql"
}

// rootSQL runs SQL as the database superuser over the local socket (the panel's stored
// password is not needed). The SQL goes on stdin, so passwords in it stay off the command line.
func rootSQL(ctx context.Context, engine, sql string) (string, error) {
	var cmd *exec.Cmd
	switch engine {
	case "postgres":
		cmd = exec.CommandContext(ctx, "runuser", "-u", "postgres", "--", "psql", "-X", "-q", "-tA", "-v", "ON_ERROR_STOP=1", "-d", "postgres")
	case "mariadb":
		cmd = exec.CommandContext(ctx, "mariadb", "-uroot", "-N", "-B")
	default:
		cmd = exec.CommandContext(ctx, "mysql", "-uroot", "-N", "-B")
	}
	cmd.Stdin = strings.NewReader(sql)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", errors.New(strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

// waitDB waits for the server to answer a query.
func waitDB(ctx context.Context, engine string, wait time.Duration) error {
	deadline := time.Now().Add(wait)
	var last error
	for time.Now().Before(deadline) {
		if engine == "redis" {
			if redisUp() {
				return nil
			}
			last = errors.New("no PONG on 6379")
		} else if _, err := rootSQL(ctx, engine, "SELECT 1;"); err == nil {
			return nil
		} else {
			last = err
		}
		time.Sleep(2 * time.Second)
	}
	return fmt.Errorf("no answer within %s: %v", wait, last)
}

func sqlLit(s string) string {
	return "'" + strings.ReplaceAll(strings.ReplaceAll(s, `\`, `\\`), "'", "''") + "'"
}

// DBInstallResult is what the panel needs to register the server.
type DBInstallResult struct {
	Engine   string `json:"engine"`
	Version  string `json:"version"`
	Addr     string `json:"addr"`
	User     string `json:"user"`
	Password string `json:"password"`
}

// DBNativeInstall installs, starts and secures a server, and makes an admin login for Berth.
func DBNativeInstall(ctx context.Context, engine, version, password string, log func(string)) (DBInstallResult, error) {
	var res DBInstallResult
	p, err := dbPackages(engine, version)
	if err != nil {
		return res, err
	}
	if len(password) < 16 {
		return res, errs.New(400, "dbn.bad_password")
	}
	fam := dbFamily(engine)
	if old, ok := readDBNative(fam); ok {
		return res, errs.New(409, "dbn.exists", "engine", old.Engine, "version", old.Version)
	}
	if who := portBusy(ctx, p.port); who != "" {
		return res, errs.New(409, "dbn.port_busy", "port", strconv.Itoa(p.port), "by", who)
	}
	if fam == "mysql" {
		if was := mysqlDataEngine(p.dataDir); was != "" && was != engine {
			return res, errs.New(409, "dbn.datadir_other", "dir", p.dataDir, "engine", was)
		}
	}
	defer TempSwap(ctx, log)()
	name, args := "dnf", append([]string{"-y", "-q", "install"}, p.pkgs...)
	if debianLike() {
		name, args = "apt-get", append([]string{"-y", "-q", "install"}, p.pkgs...)
	}
	log("$ " + name + " install " + strings.Join(p.pkgs, " "))
	n, a := sysexec.Wrap(name, args...)
	cmd := exec.CommandContext(ctx, n, a...)
	cmd.Env = append(os.Environ(), "DEBIAN_FRONTEND=noninteractive")
	if out, err := cmd.CombinedOutput(); err != nil {
		return res, errs.New(500, "dbn.install_failed", "detail", lastLine(string(out)))
	}
	d := DBNative{Engine: engine, Version: version, Port: p.port, Service: p.service, DataDir: p.dataDir, Allow: []string{}, Values: map[string]string{}}
	if engine == "redis" {
		d.Pass = password
	}
	if engine == "postgres" && !debianLike() {
		if _, err := os.Stat(filepath.Join(d.DataDir, "PG_VERSION")); err != nil {
			log("$ postgresql-setup --initdb")
			if out, err := run(ctx, "postgresql-setup", "--initdb"); err != nil {
				return res, errs.New(500, "dbn.install_failed", "detail", lastLine(out))
			}
		}
	}
	if err := writeDBConfig(d); err != nil {
		return res, err
	}
	log("$ systemctl enable --now " + d.Service)
	if out, err := run(ctx, "systemctl", "enable", "--now", d.Service); err != nil {
		return res, errs.New(500, "dbn.start_failed", "detail", lastLine(out)+" "+dbJournal(ctx, d.Service))
	}
	if err := waitDB(ctx, engine, 90*time.Second); err != nil {
		return res, errs.New(500, "dbn.start_failed", "detail", err.Error())
	}
	log("creating Berth's admin login")
	var sql string
	switch engine {
	case "redis":
		sql = "" // the password is in its config
	case "postgres":
		sql = "ALTER USER postgres PASSWORD " + sqlLit(password) + ";"
		res.User = "postgres"
	case "mysql":
		// root logs in over the socket only, as the system's root (MySQL starts it with no password)
		sql = "INSTALL PLUGIN auth_socket SONAME 'auth_socket.so';\n"
		rootSQL(ctx, engine, sql)
		sql = "ALTER USER 'root'@'localhost' IDENTIFIED WITH auth_socket;\n" +
			"CREATE USER IF NOT EXISTS 'berth_admin'@'%' IDENTIFIED BY " + sqlLit(password) + ";\nALTER USER 'berth_admin'@'%' IDENTIFIED BY " + sqlLit(password) + ";\n" +
			"GRANT ALL PRIVILEGES ON *.* TO 'berth_admin'@'%' WITH GRANT OPTION;\nFLUSH PRIVILEGES;\n"
		res.User = "berth_admin"
	default:
		sql = "CREATE USER IF NOT EXISTS 'berth_admin'@'%' IDENTIFIED BY " + sqlLit(password) + ";\nALTER USER 'berth_admin'@'%' IDENTIFIED BY " + sqlLit(password) + ";\n" +
			"GRANT ALL PRIVILEGES ON *.* TO 'berth_admin'@'%' WITH GRANT OPTION;\nFLUSH PRIVILEGES;\n"
		res.User = "berth_admin"
	}
	if sql != "" {
		if _, err := rootSQL(ctx, engine, sql); err != nil {
			return res, errs.New(500, "dbn.install_failed", "detail", err.Error())
		}
	}
	if err := writeDBNative(d); err != nil {
		return res, err
	}
	if err := ensureDBFirewall(ctx); err != nil {
		return res, err
	}
	res.Engine, res.Version, res.Password = engine, version, password
	res.Addr = net.JoinHostPort("127.0.0.1", strconv.Itoa(d.Port))
	return res, nil
}

// DBNativeRemove stops and uninstalls a server; its data folder stays.
func DBNativeRemove(ctx context.Context, engine string, log func(string)) error {
	d, ok := readDBNative(dbFamily(engine))
	if !ok || d.Engine != engine {
		return errs.New(404, "dbn.not_installed", "engine", engine)
	}
	p, err := dbPackages(d.Engine, d.Version)
	if err != nil {
		return err
	}
	log("$ systemctl disable --now " + d.Service)
	run(ctx, "systemctl", "disable", "--now", d.Service)
	name, args := "dnf", append([]string{"-y", "-q", "remove"}, p.pkgs...)
	if debianLike() {
		name, args = "apt-get", append([]string{"-y", "-q", "remove"}, p.pkgs...)
	}
	log("$ " + name + " remove " + strings.Join(p.pkgs, " "))
	n, a := sysexec.Wrap(name, args...)
	if out, err := exec.CommandContext(ctx, n, a...).CombinedOutput(); err != nil {
		return errs.New(500, "dbn.install_failed", "detail", lastLine(string(out)))
	}
	syncDBFirewalld(ctx, d.Port, d.Allow, nil)
	switch engine {
	case "redis":
		os.Remove(redisConfPath())
	case "postgres":
	default:
		os.Remove(mysqlConfPath)
	}
	os.Remove(dbStatePath(dbFamily(engine)))
	log("the data folder " + d.DataDir + " is kept")
	return ensureDBFirewall(ctx)
}

func dbJournal(ctx context.Context, unit string) string {
	j, _ := run(ctx, "journalctl", "-u", unit, "-n", "8", "--no-pager", "-o", "cat")
	return strings.Join(strings.Fields(j), " ")
}

// ---- config ----

const mysqlConfPath = "/etc/my.cnf.d/zz-berth.cnf"

func mysqlConfDir() string {
	if debianLike() {
		return "/etc/mysql/mariadb.conf.d"
	}
	return "/etc/my.cnf.d"
}

// mysqlConfText is Berth's file for a MySQL-family server.
func mysqlConfText(d DBNative) string {
	var b strings.Builder
	b.WriteString("# Managed by Berth (Databases → tuning). Changes here are overwritten.\n[mysqld]\n")
	// slow queries go to mysql.slow_log, where the slow-query page reads them
	b.WriteString("bind-address = 0.0.0.0\nskip-name-resolve\ncharacter-set-server = utf8mb4\nlog_output = TABLE\n")
	if d.Engine == "mariadb" {
		b.WriteString("collation-server = utf8mb4_unicode_ci\n")
	}
	for _, k := range sortedKeys(d.Values) {
		fmt.Fprintf(&b, "%s = %s\n", k, d.Values[k])
	}
	return b.String()
}

// pgConfText is Berth's conf.d file for PostgreSQL.
func pgConfText(d DBNative) string {
	var b strings.Builder
	b.WriteString("# Managed by Berth (Databases → tuning). Changes here are overwritten.\nlisten_addresses = '*'\n")
	for _, k := range sortedKeys(d.Values) {
		fmt.Fprintf(&b, "%s = '%s'\n", k, d.Values[k])
	}
	return b.String()
}

// pgHBAText lets password logins in from this host, its containers and the allowed sources.
func pgHBAText(d DBNative) string {
	var b strings.Builder
	b.WriteString("# Managed by Berth (Databases → remote access). Changes here are overwritten.\n")
	b.WriteString("local   all  all                 peer\n")
	for _, n := range append([]string{"127.0.0.1/32", "::1/128", "172.16.0.0/12"}, d.Allow...) {
		if !strings.Contains(n, "/") {
			if strings.Contains(n, ":") {
				n += "/128"
			} else {
				n += "/32"
			}
		}
		fmt.Fprintf(&b, "host    all  all  %-18s scram-sha-256\n", n)
	}
	return b.String()
}

func sortedKeys(m map[string]string) []string {
	k := make([]string, 0, len(m))
	for x := range m {
		k = append(k, x)
	}
	sort.Strings(k)
	return k
}

// writeDBConfig writes Berth's files for a server (the caller restarts or reloads it).
func writeDBConfig(d DBNative) error {
	if d.Engine == "redis" {
		return writeRedisConfig(d)
	}
	if d.Engine != "postgres" {
		path := mysqlConfPath
		if debianLike() {
			path = filepath.Join(mysqlConfDir(), "zz-berth.cnf")
		}
		return os.WriteFile(path, []byte(mysqlConfText(d)), 0o644)
	}
	dir := pgConfDir(d)
	conf := filepath.Join(dir, "postgresql.conf")
	b, err := os.ReadFile(conf)
	if err != nil {
		return err
	}
	if !regexp.MustCompile(`(?m)^\s*include_dir\s*=\s*'conf\.d'`).Match(b) {
		b = append(b, []byte("\n# added by Berth\ninclude_dir = 'conf.d'\n")...)
		if err := os.WriteFile(conf, b, 0o600); err != nil {
			return err
		}
	}
	cd := filepath.Join(dir, "conf.d")
	os.MkdirAll(cd, 0o700)
	if err := os.WriteFile(filepath.Join(cd, "berth.conf"), []byte(pgConfText(d)), 0o600); err != nil {
		return err
	}
	hba := filepath.Join(dir, "pg_hba.conf")
	if _, err := os.Stat(hba + ".before-berth"); err != nil {
		if old, err := os.ReadFile(hba); err == nil {
			os.WriteFile(hba+".before-berth", old, 0o600)
		}
	}
	if err := os.WriteFile(hba, []byte(pgHBAText(d)), 0o600); err != nil {
		return err
	}
	// postgres owns its folder (the files were written as root)
	exec.Command("chown", "-R", "postgres:postgres", cd, conf, hba).Run()
	if !debianLike() {
		exec.Command("restorecon", "-R", dir).Run()
	}
	return nil
}

// ensureDBFirewall keeps the database ports to this host, its containers and the allowed
// sources; with no native server left it removes the table.
func ensureDBFirewall(ctx context.Context) error {
	ds := []DBNative{}
	for _, f := range dbFamilies {
		if d, ok := readDBNative(f); ok {
			ds = append(ds, d)
		}
	}
	if len(ds) == 0 {
		dropNftTable(ctx, "berth_db")
		return nil
	}
	return applyNftTable(ctx, "berth_db", dbNftBody(ds))
}

func dbNftBody(ds []DBNative) string {
	var b strings.Builder
	b.WriteString("\tchain input {\n\t\ttype filter hook input priority -5; policy accept;\n")
	b.WriteString("\t\tiifname \"lo\" accept\n\t\tiifname \"docker0\" accept\n\t\tiifname \"br-*\" accept\n\t\tiifname \"podman*\" accept\n")
	ports := []string{}
	for _, d := range ds {
		p := strconv.Itoa(d.Port)
		ports = append(ports, p)
		for _, a := range d.Allow {
			fam := "ip"
			if strings.Contains(a, ":") {
				fam = "ip6"
			}
			fmt.Fprintf(&b, "\t\t%s saddr %s tcp dport %s accept\n", fam, a, p)
		}
	}
	fmt.Fprintf(&b, "\t\ttcp dport { %s } drop\n\t}\n", strings.Join(ports, ", "))
	return b.String()
}

// ---- tuning ----

// the settings the tuning form may write, and what a value may look like
var (
	mysqlTunable = map[string]bool{"innodb_buffer_pool_size": true, "max_connections": true, "innodb_log_file_size": true, "innodb_redo_log_capacity": true,
		"tmp_table_size": true, "max_heap_table_size": true, "table_open_cache": true, "thread_cache_size": true, "max_allowed_packet": true,
		"slow_query_log": true, "long_query_time": true, "sort_buffer_size": true, "join_buffer_size": true, "wait_timeout": true, "innodb_flush_log_at_trx_commit": true}
	pgTunable = map[string]bool{"shared_buffers": true, "effective_cache_size": true, "work_mem": true, "maintenance_work_mem": true, "max_connections": true,
		"random_page_cost": true, "log_min_duration_statement": true, "wal_buffers": true, "checkpoint_completion_target": true, "max_wal_size": true}
	mysqlValRe = regexp.MustCompile(`^([0-9]+(\.[0-9]+)?[KMG]?|ON|OFF)$`)
	pgValRe    = regexp.MustCompile(`^(-?[0-9]+(\.[0-9]+)?(kB|MB|GB|ms|s|min)?|on|off)$`)
)

// ValidateDBValues checks tuning values for an engine.
func ValidateDBValues(engine string, v map[string]string) error {
	for k, x := range v {
		if engine == "redis" {
			if !redisTunable[k] || !redisValRe.MatchString(x) {
				return errs.New(400, "dbn.bad_value", "key", k, "value", x)
			}
			continue
		}
		if engine == "postgres" {
			if !pgTunable[k] || !pgValRe.MatchString(x) {
				return errs.New(400, "dbn.bad_value", "key", k, "value", x)
			}
		} else if !mysqlTunable[k] || !mysqlValRe.MatchString(x) {
			return errs.New(400, "dbn.bad_value", "key", k, "value", x)
		}
	}
	return nil
}

// RecommendDB sizes a server for memMB of memory.
func RecommendDB(engine, version string, memMB int) map[string]string {
	if memMB < 256 {
		memMB = 256
	}
	gb := float64(memMB) / 1024
	clamp := func(v, lo, hi int) int {
		if v < lo {
			return lo
		}
		if v > hi {
			return hi
		}
		return v
	}
	m := func(n int) string { return strconv.Itoa(n) + "M" }
	if engine == "redis" {
		// memory for data; noeviction keeps queues and sessions safe (switch to allkeys-lru for a pure cache)
		return map[string]string{"maxmemory": strconv.Itoa(memMB*80/100) + "mb", "maxmemory-policy": "noeviction", "appendonly": "yes"}
	}
	if engine == "postgres" {
		conns := clamp(int(gb*50), 50, 400)
		sb := memMB / 4
		work := clamp((memMB-sb)/(conns*3), 4, 256)
		return map[string]string{
			"shared_buffers": strconv.Itoa(sb) + "MB", "effective_cache_size": strconv.Itoa(memMB*3/4) + "MB",
			"work_mem": strconv.Itoa(work) + "MB", "maintenance_work_mem": strconv.Itoa(clamp(memMB*6/100, 16, 1024)) + "MB",
			"max_connections": strconv.Itoa(conns), "random_page_cost": "1.1", "log_min_duration_statement": "2000",
		}
	}
	bp := memMB * 60 / 100
	tmp := clamp(memMB*3/100, 16, 256)
	v := map[string]string{
		"innodb_buffer_pool_size": m(bp), "max_connections": strconv.Itoa(clamp(int(gb*100), 100, 1000)),
		"tmp_table_size": m(tmp), "max_heap_table_size": m(tmp), "table_open_cache": strconv.Itoa(clamp(int(gb*1000), 400, 4000)),
		"thread_cache_size": strconv.Itoa(clamp(int(gb*16), 8, 128)), "max_allowed_packet": "64M", "slow_query_log": "ON", "long_query_time": "2",
	}
	logSize := m(clamp(bp/4, 48, 1024))
	if engine == "mysql" {
		v["innodb_redo_log_capacity"] = logSize
	} else {
		v["innodb_log_file_size"] = logSize
	}
	return v
}

// DBTune writes new values, restarts the server and checks it answers; if it does not, the old
// file goes back and the server is restarted with it.
func DBTune(ctx context.Context, engine string, memMB int, values map[string]string, log func(string)) error {
	d, ok := readDBNative(dbFamily(engine))
	if !ok || d.Engine != engine {
		return errs.New(404, "dbn.not_installed", "engine", engine)
	}
	if err := ValidateDBValues(engine, values); err != nil {
		return err
	}
	old := d
	d.Values, d.MemMB = values, memMB
	if err := writeDBConfig(d); err != nil {
		return err
	}
	log("$ systemctl restart " + d.Service)
	_, rerr := run(ctx, "systemctl", "restart", d.Service)
	if rerr == nil {
		rerr = waitDB(ctx, engine, 60*time.Second)
	}
	if rerr != nil {
		why := dbJournal(ctx, d.Service)
		log("the server did not come back; putting the previous settings back")
		writeDBConfig(old)
		run(ctx, "systemctl", "restart", old.Service)
		if err := waitDB(ctx, engine, 60*time.Second); err != nil {
			log("still not answering: " + err.Error())
		}
		return errs.New(500, "dbn.tune_failed", "detail", why)
	}
	return writeDBNative(d)
}

// DBLive reads the running values of the tunable settings.
func DBLive(ctx context.Context, engine string) map[string]string {
	if engine == "redis" {
		d, ok := readDBNative("redis")
		if !ok {
			return map[string]string{}
		}
		return redisLive(ctx, d)
	}
	out := map[string]string{}
	keys := []string{}
	tun := mysqlTunable
	if engine == "postgres" {
		tun = pgTunable
	}
	for k := range tun {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if engine == "postgres" {
		q := "SELECT name, current_setting(name) FROM pg_settings WHERE name IN ('" + strings.Join(keys, "','") + "');"
		res, err := rootSQL(ctx, engine, q)
		if err != nil {
			return out
		}
		for _, l := range strings.Split(res, "\n") {
			if f := strings.SplitN(l, "|", 2); len(f) == 2 {
				out[f[0]] = f[1]
			}
		}
		return out
	}
	q := "SHOW GLOBAL VARIABLES WHERE Variable_name IN ('" + strings.Join(keys, "','") + "');"
	res, err := rootSQL(ctx, engine, q)
	if err != nil {
		return out
	}
	for _, l := range strings.Split(res, "\n") {
		if f := strings.SplitN(l, "\t", 2); len(f) == 2 {
			out[f[0]] = f[1]
		}
	}
	return out
}

// ---- remote access, password ----

var dbSourceRe = regexp.MustCompile(`^[0-9a-fA-F:.]+(/[0-9]{1,3})?$`)

// DBAllow sets the remote sources allowed to reach a server.
func DBAllow(ctx context.Context, engine string, sources []string) error {
	d, ok := readDBNative(dbFamily(engine))
	if !ok || d.Engine != engine {
		return errs.New(404, "dbn.not_installed", "engine", engine)
	}
	clean := []string{}
	for _, s := range sources {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if !dbSourceRe.MatchString(s) {
			return errs.New(400, "dbn.bad_source", "source", s)
		}
		if _, _, err := net.ParseCIDR(s); err != nil && net.ParseIP(s) == nil {
			return errs.New(400, "dbn.bad_source", "source", s)
		}
		clean = append(clean, s)
	}
	syncDBFirewalld(ctx, d.Port, d.Allow, clean)
	d.Allow = clean
	if err := writeDBNative(d); err != nil {
		return err
	}
	if engine == "postgres" {
		if err := writeDBConfig(d); err != nil {
			return err
		}
		run(ctx, "systemctl", "reload", d.Service)
	}
	return ensureDBFirewall(ctx)
}

// firewalld (Fedora, RHEL) drops the port too: allowed sources get a rich rule there as well,
// in the running and the saved configuration. Rules for sources no longer allowed go.
func dbRichRule(src string, port int) string {
	fam := "ipv4"
	if strings.Contains(src, ":") {
		fam = "ipv6"
	}
	return fmt.Sprintf(`rule family="%s" source address="%s" port port="%d" protocol="tcp" accept`, fam, src, port)
}

func syncDBFirewalld(ctx context.Context, port int, old, now []string) {
	if out, err := run(ctx, "firewall-cmd", "--state"); err != nil || strings.TrimSpace(out) != "running" {
		return
	}
	keep := map[string]bool{}
	for _, s := range now {
		keep[s] = true
	}
	for _, s := range old {
		if !keep[s] {
			run(ctx, "firewall-cmd", "--remove-rich-rule="+dbRichRule(s, port))
			run(ctx, "firewall-cmd", "--permanent", "--remove-rich-rule="+dbRichRule(s, port))
		}
	}
	for _, s := range now {
		run(ctx, "firewall-cmd", "--add-rich-rule="+dbRichRule(s, port))
		run(ctx, "firewall-cmd", "--permanent", "--add-rich-rule="+dbRichRule(s, port))
	}
}

var dbUserRe = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,64}$`)

// DBNativePassword sets a login's password as the superuser over the local socket.
func DBNativePassword(ctx context.Context, engine, user, password string) error {
	if engine == "redis" && user == "" {
		user = "default"
	}
	if !dbUserRe.MatchString(user) || len(password) < 8 || strings.ContainsAny(password, "\n\r") {
		return errs.New(400, "dbn.bad_password")
	}
	if engine == "redis" {
		d, ok := readDBNative("redis")
		if !ok {
			return errs.New(404, "dbn.not_installed", "engine", engine)
		}
		if out, err := redisCmd(ctx, d.Pass, "CONFIG", "SET", "requirepass", password); err != nil || !strings.Contains(out, "OK") {
			return errs.New(500, "dbn.password_failed", "detail", out)
		}
		d.Pass = password
		if err := writeRedisConfig(d); err != nil {
			return err
		}
		return writeDBNative(d)
	}
	var sql string
	if engine == "postgres" {
		sql = `ALTER USER "` + user + `" PASSWORD ` + sqlLit(password) + ";"
	} else {
		hosts, err := rootSQL(ctx, engine, "SELECT Host FROM mysql.user WHERE User="+sqlLit(user)+";")
		if err != nil {
			return errs.New(500, "dbn.password_failed", "detail", err.Error())
		}
		for _, h := range strings.Fields(hosts) {
			sql += "ALTER USER " + sqlLit(user) + "@" + sqlLit(h) + " IDENTIFIED BY " + sqlLit(password) + ";\n"
		}
		if sql == "" {
			return errs.New(404, "dbn.no_user", "user", user)
		}
		sql += "FLUSH PRIVILEGES;\n"
	}
	if _, err := rootSQL(ctx, engine, sql); err != nil {
		return errs.New(500, "dbn.password_failed", "detail", err.Error())
	}
	return nil
}

// DBLogTail: the last lines of a native PostgreSQL server's log (where slow statements go).
func DBLogTail(engine string, n int) []string {
	d, ok := readDBNative(dbFamily(engine))
	if !ok || d.Engine != "postgres" {
		return []string{}
	}
	files, _ := filepath.Glob(filepath.Join(d.DataDir, "log", "*.log"))
	if debianLike() {
		files, _ = filepath.Glob("/var/log/postgresql/postgresql-" + d.Version + "-main.log")
	}
	sort.Slice(files, func(i, j int) bool {
		a, _ := os.Stat(files[i])
		b, _ := os.Stat(files[j])
		return a != nil && b != nil && a.ModTime().After(b.ModTime())
	})
	if len(files) == 0 {
		return []string{}
	}
	b, _ := os.ReadFile(files[0])
	if len(b) > 4<<20 {
		b = b[len(b)-4<<20:]
	}
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return lines
}
