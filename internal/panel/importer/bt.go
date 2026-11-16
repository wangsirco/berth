package importer

// 宝塔 (BT Panel): its websites, MySQL databases with their logins, and scheduled jobs, read
// from /www/server/panel/data/default.db and the Nginx files it writes, turned into what Berth
// needs to move them (server migration, R6). Columns are checked first: the schema changes
// between 宝塔 versions.

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"strconv"
	"strings"
)

const BTDB = "/www/server/panel/data/default.db"

// BTSource reads the 宝塔 server's files.
type BTSource struct {
	DB   *sql.DB
	Read func(path string) string // a text file, "" when missing
	List func(dir string) []string
}

type BTSite struct {
	ID       int      `json:"id"`
	Name     string   `json:"name"`
	Domains  []string `json:"domains"`
	Path     string   `json:"path"`     // its folder on the 宝塔 server
	RootSub  string   `json:"root_sub"` // 运行目录, e.g. public
	Kind     string   `json:"kind"`     // php | static | proxy | project
	PHP      string   `json:"php"`      // 7.4, 8.1 …
	Upstream string   `json:"upstream"` // proxy, project: host:port
	Rewrite  bool     `json:"rewrite"`  // has its own rewrite rules (伪静态)
	SSL      bool     `json:"ssl"`
	Notes    []string `json:"notes"`
}

type BTDatabase struct {
	Name string `json:"name"`
	User string `json:"user"`
	Pass string `json:"-"`
}

type BTCron struct {
	ID     int    `json:"id"`
	Name   string `json:"name"`
	Spec   string `json:"spec"`
	Script string `json:"script,omitempty"`
	OK     bool   `json:"ok"`
	Reason string `json:"reason,omitempty"`
}

type BTData struct {
	Sites     []BTSite     `json:"sites"`
	Databases []BTDatabase `json:"databases"`
	RootPass  string       `json:"-"`
	Crons     []BTCron     `json:"crons"`
}

var (
	btPHPRe     = regexp.MustCompile(`enable-php-(\d+)\.conf`)
	btRootRe    = regexp.MustCompile(`(?m)^\s*root\s+([^;]+);`)
	btProxyRe   = regexp.MustCompile(`(?m)^\s*proxy_pass\s+([^;]+);`)
	btIncludeRe = regexp.MustCompile(`(?m)^\s*include\s+([^;]+);`)
	btSSLRe     = regexp.MustCompile(`(?m)^\s*listen\s+443\b`)
)

func str(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case []byte:
		return string(x)
	case string:
		return x
	case int64:
		return strconv.FormatInt(x, 10)
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	}
	return fmt.Sprint(v)
}

// rowsMap reads a table as column → value maps.
func rowsMap(db *sql.DB, q string, args ...any) []map[string]any {
	rs, err := db.Query(q, args...)
	if err != nil {
		return nil
	}
	defer rs.Close()
	cols, _ := rs.Columns()
	var out []map[string]any
	for rs.Next() {
		v := make([]any, len(cols))
		p := make([]any, len(cols))
		for i := range v {
			p[i] = &v[i]
		}
		if rs.Scan(p...) != nil {
			continue
		}
		m := map[string]any{}
		for i, c := range cols {
			m[c] = v[i]
		}
		out = append(out, m)
	}
	return out
}

func phpDot(n string) string { // "74" → "7.4"
	if len(n) == 2 {
		return n[:1] + "." + n[1:]
	}
	return ""
}

// BT reads a 宝塔 server.
func BT(src BTSource) (BTData, error) {
	var d BTData
	if len(colSet(src.DB, "sites")) == 0 {
		return d, fmt.Errorf("not a 宝塔 database (no sites table)")
	}
	for _, r := range rowsMap(src.DB, `SELECT * FROM sites ORDER BY id`) {
		id, _ := strconv.Atoi(str(r["id"]))
		s := BTSite{ID: id, Name: str(r["name"]), Path: strings.TrimRight(str(r["path"]), "/"), Notes: []string{}}
		s.Domains = []string{s.Name}
		for _, dr := range rowsMap(src.DB, `SELECT name FROM domain WHERE pid=?`, id) {
			if n := str(dr["name"]); n != "" && n != s.Name && !strings.Contains(n, ":") {
				s.Domains = append(s.Domains, n)
			}
		}
		pt := strings.ToLower(str(r["project_type"]))
		conf := src.Read("/www/server/panel/vhost/nginx/" + s.Name + ".conf")
		if conf == "" { // project sites keep theirs elsewhere in newer versions
			for _, sub := range []string{"node_", "java_", "go_", "python_", "other_", "proxy_"} {
				if c := src.Read("/www/server/panel/vhost/nginx/" + sub + s.Name + ".conf"); c != "" {
					conf = c
					break
				}
			}
		}
		s.SSL = btSSLRe.MatchString(conf)
		if m := btRootRe.FindStringSubmatch(conf); m != nil {
			root := strings.TrimRight(strings.TrimSpace(m[1]), "/")
			if s.Path != "" && strings.HasPrefix(root, s.Path+"/") {
				s.RootSub = strings.TrimPrefix(root, s.Path+"/")
			}
		}
		proxy := ""
		if m := btProxyRe.FindStringSubmatch(conf); m != nil {
			proxy = strings.TrimSpace(m[1])
		}
		for _, inc := range btIncludeRe.FindAllStringSubmatch(conf, -1) {
			p := strings.TrimSpace(inc[1])
			if !strings.Contains(p, "/proxy/") {
				continue
			}
			dir := path.Dir(p)
			for _, f := range src.List(dir) {
				if m := btProxyRe.FindStringSubmatch(src.Read(dir + "/" + f)); m != nil && proxy == "" {
					proxy = strings.TrimSpace(m[1])
				}
			}
		}
		if rw := strings.TrimSpace(src.Read("/www/server/panel/vhost/rewrite/" + s.Name + ".conf")); rw != "" && !strings.HasPrefix(rw, "#") {
			s.Rewrite = true
		}
		switch {
		case proxy != "":
			s.Kind = "proxy"
			if up, ok := upstream(proxy); ok {
				s.Upstream = up
			} else {
				s.Notes = append(s.Notes, "bt.proxy_remote:"+proxy)
			}
		case pt != "" && pt != "php" && pt != "proxy":
			s.Kind = "project"
			var pc struct {
				Port any `json:"port"`
			}
			json.Unmarshal([]byte(str(r["project_config"])), &pc)
			if p := str(pc.Port); p != "" && p != "0" {
				s.Upstream = "127.0.0.1:" + p
			}
			s.Notes = append(s.Notes, "bt.project:"+pt)
		default:
			if m := btPHPRe.FindStringSubmatch(conf); m != nil && m[1] != "00" {
				s.Kind, s.PHP = "php", phpDot(m[1])
			} else {
				s.Kind = "static"
			}
		}
		if s.Rewrite && s.Kind == "php" {
			s.Notes = append(s.Notes, "bt.rewrite")
		}
		d.Sites = append(d.Sites, s)
	}
	for _, r := range rowsMap(src.DB, `SELECT * FROM databases`) {
		if t := strings.ToLower(str(r["type"])); t != "" && t != "mysql" {
			continue
		}
		if sid := str(r["sid"]); sid != "" && sid != "0" { // a remote MySQL 宝塔 only manages
			continue
		}
		d.Databases = append(d.Databases, BTDatabase{Name: str(r["name"]), User: str(r["username"]), Pass: str(r["password"])})
	}
	for _, r := range rowsMap(src.DB, `SELECT * FROM config WHERE id=1`) {
		d.RootPass = str(r["mysql_root"])
	}
	for _, r := range rowsMap(src.DB, `SELECT * FROM crontab ORDER BY id`) {
		id, _ := strconv.Atoi(str(r["id"]))
		c := BTCron{ID: id, Name: str(r["name"])}
		spec, ok := btSpec(str(r["type"]), str(r["where1"]), str(r["where_hour"]), str(r["where_minute"]))
		c.Spec = spec
		switch {
		case !ok:
			c.Reason = "bt.cron_when"
		case str(r["sType"]) != "toShell":
			c.Reason = "bt.cron_kind:" + str(r["sType"])
		case strings.TrimSpace(str(r["sBody"])) == "":
			c.Reason = "bt.cron_empty"
		default:
			c.OK, c.Script = true, str(r["sBody"])
		}
		d.Crons = append(d.Crons, c)
	}
	return d, nil
}

// btSpec turns 宝塔's schedule columns into a cron line.
func btSpec(typ, where1, hour, minute string) (string, bool) {
	n := func(s string) bool { _, err := strconv.Atoi(s); return err == nil }
	if minute == "" {
		minute = "0"
	}
	if hour == "" {
		hour = "0"
	}
	if !n(minute) || !n(hour) {
		return "", false
	}
	switch typ {
	case "day":
		return minute + " " + hour + " * * *", true
	case "day-n":
		if n(where1) {
			return minute + " " + hour + " */" + where1 + " * *", true
		}
	case "hour":
		return minute + " * * * *", true
	case "hour-n":
		if n(where1) {
			return minute + " */" + where1 + " * * *", true
		}
	case "minute-n":
		if n(where1) {
			return "*/" + where1 + " * * * *", true
		}
	case "week":
		if n(where1) {
			return minute + " " + hour + " * * " + where1, true
		}
	case "month":
		if n(where1) {
			return minute + " " + hour + " " + where1 + " * *", true
		}
	}
	return "", false
}

var sqlQuoteR = strings.NewReplacer(`\`, `\\`, `'`, `\'`)

// BTUsersSQL recreates 宝塔's database logins with their passwords and grants, for an app on
// the same machine (localhost and 127.0.0.1, as 宝塔 sets them up).
func BTUsersSQL(dbs []BTDatabase) string {
	var b strings.Builder
	for _, d := range dbs {
		if d.User == "" || d.User == "root" {
			continue
		}
		for _, h := range []string{"localhost", "127.0.0.1"} {
			fmt.Fprintf(&b, "CREATE USER IF NOT EXISTS '%s'@'%s' IDENTIFIED BY '%s';\n", sqlQuoteR.Replace(d.User), h, sqlQuoteR.Replace(d.Pass))
			fmt.Fprintf(&b, "GRANT ALL PRIVILEGES ON `%s`.* TO '%s'@'%s';\n", strings.ReplaceAll(d.Name, "`", ""), sqlQuoteR.Replace(d.User), h)
		}
	}
	b.WriteString("FLUSH PRIVILEGES;\n")
	return b.String()
}
