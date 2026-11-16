package panel

// PHP sites (方案 §5.2.5). Each PHP version is one runtime — a php-fpm container
// (berth-php84 …) built from the official image with the extensions you tick, on the
// berth-edge network, seeing the site directories at /srv/sites. Sites pick a version;
// Caddy sends *.php to it and everything else is served as files. One click puts
// WordPress or Typecho into a site, with its database created and filled in.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/wangsirco/berth/internal/agent/system"
	"github.com/wangsirco/berth/internal/change"
	"github.com/wangsirco/berth/internal/errs"
)

// PHPVersions offered (the official images are multi-arch, ARM included).
var PHPVersions = []string{"8.4", "8.3", "8.2", "8.1", "7.4"}

// PHPExtensions that can be ticked; the defaults cover WordPress, Laravel, Typecho.
var PHPExtensions = []string{"mysqli", "pdo_mysql", "pdo_pgsql", "pgsql", "gd", "zip", "intl", "opcache", "exif", "bcmath", "redis", "imagick", "soap", "xsl", "gmp", "sockets", "calendar", "apcu", "memcached", "mongodb", "swoole"}
var phpDefaultExt = []string{"mysqli", "pdo_mysql", "gd", "zip", "intl", "opcache", "exif", "bcmath"}

// php.ini settings shown in the form.
var phpIniKeys = map[string]*regexp.Regexp{
	"memory_limit":        regexp.MustCompile(`^(-1|[0-9]{1,5}[MG])$`),
	"upload_max_filesize": regexp.MustCompile(`^[0-9]{1,5}[MG]$`),
	"post_max_size":       regexp.MustCompile(`^[0-9]{1,5}[MG]$`),
	"max_execution_time":  regexp.MustCompile(`^[0-9]{1,5}$`),
	"max_input_vars":      regexp.MustCompile(`^[0-9]{1,6}$`),
	"display_errors":      regexp.MustCompile(`^(On|Off)$`),
	"date.timezone":       regexp.MustCompile(`^[A-Za-z_]+(/[A-Za-z_+-]+){0,2}$`),
}
var phpIniDefaults = map[string]string{"memory_limit": "256M", "upload_max_filesize": "64M", "post_max_size": "64M", "max_execution_time": "120", "max_input_vars": "3000", "display_errors": "Off"}

type phpRuntime struct {
	Version    string            `json:"version"`
	Name       string            `json:"name"` // berth-php84
	Installed  bool              `json:"installed"`
	Running    bool              `json:"running"`
	Extensions []string          `json:"extensions"`
	Ini        map[string]string `json:"ini"`
	Sites      []string          `json:"sites"` // domains using it
}

func phpTag(v string) string  { return strings.ReplaceAll(v, ".", "") }
func phpName(v string) string { return "berth-php" + phpTag(v) }

func phpSpec(v string, ext []string, ini map[string]string) (dockerfile, iniFile, compose string) {
	ext = append([]string{}, ext...)
	sort.Strings(ext)
	h := sha256.Sum256([]byte(v + strings.Join(ext, ",")))
	var b strings.Builder
	fmt.Fprintf(&b, "# Managed by Berth (Sites → PHP). Change it there.\nFROM php:%s-fpm-alpine\n", v)
	b.WriteString("ADD --chmod=0755 https://github.com/mlocati/docker-php-extension-installer/releases/latest/download/install-php-extensions /usr/local/bin/\n")
	if len(ext) > 0 {
		b.WriteString("RUN install-php-extensions " + strings.Join(ext, " ") + "\n")
	}
	b.WriteString("RUN apk add --no-cache curl tar && mkdir -p /srv/sites\n")
	dockerfile = b.String()
	keys := make([]string, 0, len(ini))
	for k := range ini {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var ib strings.Builder
	ib.WriteString("; Managed by Berth (Sites → PHP)\n")
	for _, k := range keys {
		fmt.Fprintf(&ib, "%s = %s\n", k, ini[k])
	}
	iniFile = ib.String()
	tag := phpTag(v)
	compose = fmt.Sprintf(`# Berth PHP %[1]s runtime (managed by Berth)
services:
  php:
    build: .
    image: berth-php:%[2]s-%[3]s
    container_name: berth-php%[2]s
    restart: unless-stopped
    volumes:
      - /srv/berth/caddy/sites:/srv/sites
      - ./php.ini:/usr/local/etc/php/conf.d/zz-berth.ini:ro
    networks: [berth-edge]
    labels:
      io.berth.php: "%[1]s"
networks:
  berth-edge:
    external: true
`, v, tag, hex.EncodeToString(h[:4]))
	return
}

// phpRuntimes reads the installed runtimes from their stack directories.
func (a *App) phpRuntimes(r *http.Request, host string) []phpRuntime {
	var projects []struct {
		Name    string `json:"name"`
		Running int    `json:"running"`
	}
	a.call(r.Context(), host, "GET", "/v1/compose/projects", nil, &projects)
	running := map[string]bool{}
	installed := map[string]bool{}
	for _, p := range projects {
		installed[p.Name] = true
		running[p.Name] = p.Running > 0
	}
	sites, _ := a.sites(host)
	out := []phpRuntime{}
	for _, v := range PHPVersions {
		n := phpName(v)
		rt := phpRuntime{Version: v, Name: n, Installed: installed[n], Running: running[n], Extensions: []string{}, Ini: map[string]string{}, Sites: []string{}}
		if rt.Installed {
			var st struct {
				Ext []string          `json:"ext"`
				Ini map[string]string `json:"ini"`
			}
			if json.Unmarshal([]byte(a.setting("php."+host+"."+phpTag(v))), &st) == nil {
				rt.Extensions, rt.Ini = st.Ext, st.Ini
			}
		}
		for _, s := range sites {
			if s.Target.Type == "php" && s.Target.PHP == phpTag(v) {
				rt.Sites = append(rt.Sites, s.Primary())
			}
		}
		out = append(out, rt)
	}
	return out
}

func validPHP(v string, ext []string, ini map[string]string) error {
	if !contains(PHPVersions, v) {
		return errs.New(400, "php.bad_version", "version", v)
	}
	for _, e := range ext {
		if !contains(PHPExtensions, e) {
			return errs.New(400, "php.bad_ext", "ext", e)
		}
	}
	for k, val := range ini {
		re, ok := phpIniKeys[k]
		if !ok || !re.MatchString(val) {
			return errs.New(400, "php.bad_ini", "key", k)
		}
	}
	return nil
}

// phpExec runs a command in a runtime container.
func (a *App) phpExec(env change.Env, ver string, cmd ...string) (string, error) {
	var out struct {
		Output string `json:"output"`
		Code   int    `json:"code"`
	}
	if err := a.call(env.Ctx, env.Host, "POST", "/v1/php/exec", map[string]any{"container": phpName(ver), "cmd": cmd}, &out); err != nil {
		return "", err
	}
	if out.Code != 0 {
		return out.Output, errs.New(500, "php.exec_failed", "detail", truncate(strings.TrimSpace(out.Output), 400))
	}
	return out.Output, nil
}

func versionOfTag(tag string) string {
	for _, v := range PHPVersions {
		if phpTag(v) == tag {
			return v
		}
	}
	return ""
}

func init() {
	routeHooks = append(routeHooks, func(a *App, mux *http.ServeMux) {
		mux.HandleFunc("GET /api/v1/hosts/{host}/php", a.authed("sites.view", func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, 200, map[string]any{"items": a.phpRuntimes(r, r.PathValue("host")), "extensions": PHPExtensions, "default_ext": phpDefaultExt, "ini_defaults": phpIniDefaults})
		}))
	})
	phpProps := map[string]change.Prop{"version": {Type: "string", Enum: PHPVersions}, "extensions": {Type: "array", Desc: "e.g. mysqli gd zip intl (default: what WordPress and Laravel need)"},
		"ini": {Type: "object", Desc: `{"memory_limit":"256M","upload_max_filesize":"64M"}`}}
	change.Register(change.Kind{
		Name: "php.set", Perm: "sites.manage", Scope: "sites:write", Title: "kind.php.set", CLI: "berth php set {{.version}}", Heavy: true, MemMB: 300,
		Params: change.ParamSchema{Props: phpProps, Required: []string{"version"}},
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			var p struct {
				Version    string
				Extensions []string
				Ini        map[string]string
			}
			json.Unmarshal(raw, &p)
			if p.Extensions == nil {
				p.Extensions = phpDefaultExt
			}
			if err := validPHP(p.Version, p.Extensions, p.Ini); err != nil {
				return change.Plan{}, err
			}
			ini := map[string]string{}
			for k, v := range phpIniDefaults {
				ini[k] = v
			}
			for k, v := range p.Ini {
				ini[k] = v
			}
			df, inf, _ := phpSpec(p.Version, p.Extensions, ini)
			return change.Plan{Summary: "plan.php.set", SummaryP: map[string]any{"version": p.Version, "n": len(p.Extensions)}, Risk: change.RiskMedium, Undoable: true,
				Files: []change.FileDiff{{Path: "/srv/berth/stacks/" + phpName(p.Version) + "/Dockerfile", After: df}, {Path: "php.ini", After: inf}}}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			a := svc(env)
			var p struct {
				Version    string
				Extensions []string
				Ini        map[string]string
			}
			json.Unmarshal(raw, &p)
			if p.Extensions == nil {
				p.Extensions = phpDefaultExt
			}
			ini := map[string]string{}
			for k, v := range phpIniDefaults {
				ini[k] = v
			}
			for k, v := range p.Ini {
				ini[k] = v
			}
			name := phpName(p.Version)
			key := "php." + env.Host + "." + phpTag(p.Version)
			prev := a.setting(key)
			df, inf, compose := phpSpec(p.Version, p.Extensions, ini)
			dir := "/srv/berth/stacks/" + name
			if err := a.call(env.Ctx, env.Host, "PUT", "/v1/compose/projects/"+name+"/files", map[string]any{"compose": compose, "env": "", "create": prev == ""}, nil); err != nil {
				return nil, nil, err
			}
			for f, c := range map[string]string{"Dockerfile": df, "php.ini": inf} {
				if err := a.call(env.Ctx, env.Host, "POST", "/v1/files/write", map[string]any{"root": "/", "path": dir + "/" + f, "content": []byte(c)}, nil); err != nil {
					return nil, nil, err
				}
			}
			env.Log("building PHP %s with %s (the first build takes a few minutes)", p.Version, strings.Join(p.Extensions, " "))
			if err := agentOp(env, "/v1/compose/projects/"+name+"/up", nil); err != nil {
				return nil, nil, err
			}
			agentOp(env, "/v1/compose/projects/"+name+"/restart", nil) // php.ini is read at start
			st, _ := json.Marshal(map[string]any{"ext": p.Extensions, "ini": ini})
			a.setSetting(key, string(st))
			if prev == "" {
				u, _ := json.Marshal(map[string]string{"version": p.Version})
				return nil, &change.UndoRecipe{Kind: "php.remove", Params: u}, nil
			}
			var old struct {
				Ext []string          `json:"ext"`
				Ini map[string]string `json:"ini"`
			}
			json.Unmarshal([]byte(prev), &old)
			u, _ := json.Marshal(map[string]any{"version": p.Version, "extensions": old.Ext, "ini": old.Ini})
			return nil, &change.UndoRecipe{Kind: "php.set", Params: u}, nil
		},
	})
	change.Register(change.Kind{
		Name: "php.remove", Perm: "sites.manage", Scope: "sites:write", Title: "kind.php.remove", CLI: "berth php remove {{.version}}", Heavy: true,
		Params: change.ParamSchema{Props: map[string]change.Prop{"version": {Type: "string", Enum: PHPVersions}}, Required: []string{"version"}},
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			var p struct{ Version string }
			json.Unmarshal(raw, &p)
			sites, _ := svc(env).sites(env.Host)
			for _, s := range sites {
				if s.Target.Type == "php" && s.Target.PHP == phpTag(p.Version) {
					return change.Plan{}, errs.New(409, "php.in_use", "site", s.Primary())
				}
			}
			return change.Plan{Summary: "plan.php.remove", SummaryP: map[string]any{"version": p.Version}, Risk: change.RiskMedium, Undoable: true}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			a := svc(env)
			var p struct{ Version string }
			json.Unmarshal(raw, &p)
			key := "php." + env.Host + "." + phpTag(p.Version)
			prev := a.setting(key)
			if err := agentOp(env, "/v1/compose/projects/"+phpName(p.Version)+"/remove", nil); err != nil {
				return nil, nil, err
			}
			a.setSetting(key, "")
			var old struct {
				Ext []string          `json:"ext"`
				Ini map[string]string `json:"ini"`
			}
			json.Unmarshal([]byte(prev), &old)
			u, _ := json.Marshal(map[string]any{"version": p.Version, "extensions": old.Ext, "ini": old.Ini})
			return nil, &change.UndoRecipe{Kind: "php.set", Params: u}, nil
		},
	})

	// ---- one-click CMS into a PHP site ----
	change.Register(change.Kind{
		Name: "site.cms", Perm: "sites.manage", Scope: "sites:write", Title: "kind.site.cms", CLI: "berth site cms {{.id}} {{.app}}", Heavy: true,
		Params: change.ParamSchema{Props: map[string]change.Prop{"id": {Type: "integer", Desc: "a PHP site"}, "app": {Type: "string", Enum: []string{"wordpress", "typecho", "discuz", "blank"}},
			"db": {Type: "integer", Desc: "MySQL/MariaDB instance for the new database (wordpress, typecho, discuz)"}}, Required: []string{"id", "app"}},
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			a := svc(env)
			var p struct {
				ID  int64
				App string
				DB  int64
			}
			json.Unmarshal(raw, &p)
			s, err := a.site(env.Host, p.ID)
			if err != nil {
				return change.Plan{}, err
			}
			if s.Target.Type != "php" {
				return change.Plan{}, errs.New(409, "php.not_php_site")
			}
			db := "—"
			if p.App != "blank" {
				d, err := a.dbInstance(env.Host, p.DB)
				if err != nil || (d.Engine != "mysql" && d.Engine != "mariadb") {
					return change.Plan{}, errs.New(400, "php.need_mysql")
				}
				db = d.Name
			}
			return change.Plan{Summary: "plan.site.cms_" + p.App, SummaryP: map[string]any{"domain": s.Primary(), "db": db}, Risk: change.RiskMedium,
				Commands: []string{"/srv/berth/caddy/sites/" + s.Primary() + "  (must be empty)"}}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			a := svc(env)
			var p struct {
				ID  int64
				App string
				DB  int64
			}
			json.Unmarshal(raw, &p)
			s, err := a.site(env.Host, p.ID)
			if err != nil {
				return nil, nil, err
			}
			native := isNativePHP(&s)
			dir := "/srv/sites/" + s.Primary()
			if native {
				dir = system.PHPSitesRoot + "/" + s.Primary()
			}
			run := func(script string) (string, error) { return a.sitePHPRun(env, s, script) }
			if out, _ := run("mkdir -p " + dir + " && ls -A " + dir + " | grep -v '^index.php$' | grep -v '^.composer$' | head -1"); strings.TrimSpace(out) != "" {
				return nil, nil, errs.New(409, "php.dir_not_empty", "dir", dir)
			}
			result := map[string]any{"url": "https://" + s.Primary() + "/"}
			var dbName, dbUser, dbPass, dbHost string
			if p.App != "blank" {
				d, err := a.dbInstance(env.Host, p.DB)
				if err != nil {
					return nil, nil, err
				}
				base := regexp.MustCompile(`[^a-z0-9]+`).ReplaceAllString(strings.ToLower(strings.SplitN(s.Primary(), ".", 2)[0]), "_")
				dbName, dbUser, dbPass = truncate(p.App[:2]+"_"+base, 32), truncate(p.App[:2]+"_"+base, 30), randomPassword(20)
				dbHost = d.Container
				if dbHost == "" {
					dbHost = d.Addr
				} else if native {
					// the site's PHP runs on the host: reach the database where its container publishes it
					st, err := a.dbRemoteState(env.Ctx, env.Host, d.ID)
					if err != nil || st.Port == 0 {
						return nil, nil, errs.New(409, "php.db_not_published", "name", d.Name)
					}
					dbHost = "127.0.0.1:" + strconv.Itoa(st.Port)
				} else {
					// the PHP runtime lives on berth-edge: put the database there too (no ports opened)
					if err := a.call(env.Ctx, env.Host, "POST", "/v1/edge/connect", map[string]string{"container": d.Container}, nil); err != nil {
						return nil, nil, err
					}
				}
				env.Log("creating database %s and user %s on %s", dbName, dbUser, d.Name)
				if err := a.dbAdmin(env, dbParams{Instance: d.ID, Name: dbName}, "create_db"); err != nil {
					return nil, nil, err
				}
				if err := a.dbAdmin(env, dbParams{Instance: d.ID, Name: dbName, User: dbUser, Password: dbPass}, "create_user"); err != nil {
					return nil, nil, err
				}
				result["db"] = map[string]string{"host": dbHost, "name": dbName, "user": dbUser, "password": dbPass}
			}
			switch p.App {
			case "wordpress":
				env.Log("downloading WordPress")
				if _, err := run("set -e; cd " + dir + "; rm -f index.php; curl -fsSL https://wordpress.org/latest.tar.gz | tar xz --strip-components=1"); err != nil {
					return nil, nil, err
				}
				salts := ""
				for _, k := range []string{"AUTH_KEY", "SECURE_AUTH_KEY", "LOGGED_IN_KEY", "NONCE_KEY", "AUTH_SALT", "SECURE_AUTH_SALT", "LOGGED_IN_SALT", "NONCE_SALT"} {
					salts += fmt.Sprintf("define('%s', '%s');\n", k, randomPassword(64))
				}
				cfg := fmt.Sprintf("<?php\n// Written by Berth\ndefine('DB_NAME', '%s');\ndefine('DB_USER', '%s');\ndefine('DB_PASSWORD', '%s');\ndefine('DB_HOST', '%s');\ndefine('DB_CHARSET', 'utf8mb4');\ndefine('DB_COLLATE', '');\n%s$table_prefix = 'wp_';\ndefine('WP_DEBUG', false);\n"+
					"if (isset($_SERVER['HTTP_X_FORWARDED_PROTO']) && $_SERVER['HTTP_X_FORWARDED_PROTO'] === 'https') { $_SERVER['HTTPS'] = 'on'; }\ndefine('FS_METHOD', 'direct');\n"+
					"if (!defined('ABSPATH')) { define('ABSPATH', __DIR__ . '/'); }\nrequire_once ABSPATH . 'wp-settings.php';\n", dbName, dbUser, dbPass, dbHost, salts)
				if _, err := run("cat > " + dir + "/wp-config.php <<'BERTHEOF'\n" + cfg + "BERTHEOF"); err != nil {
					return nil, nil, err
				}
				result["next"] = "wordpress"
			case "typecho":
				env.Log("downloading Typecho")
				if _, err := run("set -e; cd " + dir + "; rm -f index.php; curl -fsSL -o /tmp/typecho.zip https://github.com/typecho/typecho/releases/latest/download/typecho.zip; unzip -oq /tmp/typecho.zip; rm /tmp/typecho.zip"); err != nil {
					return nil, nil, err
				}
				result["next"] = "typecho"
			case "discuz":
				// Discuz! X3.5 from its official Gitee repository: the newest dated release tag
				env.Log("downloading Discuz! X3.5")
				script := `set -e; cd /tmp; rm -rf dzx dzx.zip
tag=$(curl -fsSL https://gitee.com/api/v5/repos/Discuz/DiscuzX/tags | grep -o '"name":"v3\.5-[0-9]\{8\}"' | cut -d'"' -f4 | sort | tail -1)
[ -n "$tag" ] || { echo "no Discuz release found"; exit 1; }
echo "release $tag"
curl -fsSL -o dzx.zip "https://gitee.com/Discuz/DiscuzX/repository/archive/$tag.zip"
mkdir dzx && unzip -q dzx.zip -d dzx
rm -f ` + dir + `/index.php
cp -a dzx/*/upload/. ` + dir + `/
rm -rf dzx dzx.zip`
				out, err := run(script)
				for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
					env.Log("%s", l)
				}
				if err != nil {
					return nil, nil, err
				}
				result["next"] = "discuz"
				result["url"] = "https://" + s.Primary() + "/install/"
			case "blank":
				run("[ -e " + dir + "/index.php ] || printf '<?php\\nphpinfo();\\n' > " + dir + "/index.php")
			}
			if native { // already the site's own user's files
			} else if _, err := a.phpExec(env, versionOfTag(s.Target.PHP), "chown", "-R", "www-data:www-data", dir); err != nil {
				return nil, nil, err
			}
			return result, nil, nil
		},
	})
	change.Register(change.Kind{
		Name: "site.fix_perms", Perm: "sites.manage", Scope: "sites:write", Title: "kind.site.fix_perms", CLI: "berth site fix-perms {{.id}}",
		Params: change.ParamSchema{Props: map[string]change.Prop{"id": {Type: "integer"}}, Required: []string{"id"}},
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			var p struct{ ID int64 }
			json.Unmarshal(raw, &p)
			s, err := svc(env).site(env.Host, p.ID)
			if err != nil {
				return change.Plan{}, err
			}
			if s.Target.Type != "php" {
				return change.Plan{}, errs.New(409, "php.not_php_site")
			}
			return change.Plan{Summary: "plan.site.fix_perms", SummaryP: map[string]any{"domain": s.Primary()}, Risk: change.RiskLow,
				Commands: []string{"chown -R www-data:www-data /srv/sites/" + s.Primary(), "find … -type d -exec chmod 755; find … -type f -exec chmod 644"}}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			a := svc(env)
			var p struct{ ID int64 }
			json.Unmarshal(raw, &p)
			s, err := a.site(env.Host, p.ID)
			if err != nil {
				return nil, nil, err
			}
			if isNativePHP(&s) { // the site's own FPM sets the owner back to the site's user
				return nil, nil, a.syncPHPSite(env, &s, &s)
			}
			dir := "/srv/sites/" + s.Primary()
			_, err = a.phpExec(env, versionOfTag(s.Target.PHP), "sh", "-c", "chown -R www-data:www-data "+dir+" && find "+dir+" -type d -exec chmod 755 {} + && find "+dir+" -type f -exec chmod 644 {} +")
			return nil, nil, err
		},
	})
	_ = strconv.Itoa
}
