package system

// Native PHP (R2): PHP versions from the distribution's multi-version repository (Remi's
// /opt/remi/phpXX on Fedora/EL, sury's /usr/sbin/php-fpmX.Y on Debian/Ubuntu), and one PHP-FPM
// per site, run by the site's own user under a systemd sandbox that can write only the site's
// folder. Caddy (in its container) reaches the site's FPM over TCP on the host; outside access
// to those ports is blocked by the same nftables table native projects use.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/wangsirco/berth/internal/errs"
	"github.com/wangsirco/berth/internal/sysexec"
)

const (
	PHPSitesRoot  = "/srv/berth/caddy/sites"
	phpConfRoot   = "/etc/berth-php" // readable by the site users (no secrets here; /etc/berth is not)
	phpSitePortLo = 25000
	composerPhar  = "/opt/berth/composer/composer.phar"
)

// PHPNativeVersions Berth can install natively (Remi/sury have ARM builds of these).
var PHPNativeVersions = []string{"8.2", "8.3", "8.4", "8.5"}

var (
	phpVerNativeRe = regexp.MustCompile(`^8\.[2-5]$`)
	domainDirRe    = regexp.MustCompile(`^[a-z0-9*][a-z0-9.*-]{0,252}$`)
	workerNameRe   = regexp.MustCompile(`^[a-z][a-z0-9-]{0,20}$`)
	rootSubRe      = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_./-]{0,60}$`)
)

func PHPSitePort(id int64) int    { return phpSitePortLo + int(id) }
func phpSiteUser(id int64) string { return "site-web" + strconv.FormatInt(id, 10) }
func phpUnit(id int64) string     { return "berth-php-" + strconv.FormatInt(id, 10) + ".service" }
func phpWorkerUnit(id int64, w string) string {
	return "berth-php-" + strconv.FormatInt(id, 10) + "-" + w + ".service"
}
func phpSchedUnit(id int64) string { return "berth-php-" + strconv.FormatInt(id, 10) + "-schedule" }

func debianLike() bool { return has("apt-get") && !has("dnf") }

func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }

// phpPaths: where a version's binaries live.
func phpPaths(v string) (bin, fpm string) {
	if debianLike() {
		return "/usr/bin/php" + v, "/usr/sbin/php-fpm" + v
	}
	t := strings.ReplaceAll(v, ".", "")
	return "/opt/remi/php" + t + "/root/usr/bin/php", "/opt/remi/php" + t + "/root/usr/sbin/php-fpm"
}

// phpPathEnv puts the version's php (and composer) first on PATH.
func phpPathEnv(v string) string {
	bin, _ := phpPaths(v)
	return "/opt/berth/php-shims/" + v + ":" + filepath.Dir(bin) + ":/usr/local/bin:/usr/bin:/bin"
}

// a "php" and "composer" for the version, so `php artisan …` and `composer install` just work
func ensureShims(v string) {
	dir := "/opt/berth/php-shims/" + v
	os.MkdirAll(dir, 0o755)
	bin, _ := phpPaths(v)
	os.WriteFile(filepath.Join(dir, "php"), []byte("#!/bin/sh\nexec "+bin+" \"$@\"\n"), 0o755)
	os.WriteFile(filepath.Join(dir, "composer"), []byte("#!/bin/sh\nexec "+bin+" "+composerPhar+" \"$@\"\n"), 0o755)
}

type PHPRuntime struct {
	Version    string   `json:"version"`
	Installed  bool     `json:"installed"`
	Extensions []string `json:"extensions"`
}

// PHPNativeRuntimes lists the versions and which are installed (with their extensions).
func PHPNativeRuntimes(ctx context.Context) []PHPRuntime {
	out := []PHPRuntime{}
	for _, v := range PHPNativeVersions {
		_, fpm := phpPaths(v)
		rt := PHPRuntime{Version: v, Extensions: []string{}}
		if _, err := os.Stat(fpm); err == nil {
			rt.Installed = true
			bin, _ := phpPaths(v)
			if o, err := exec.CommandContext(ctx, bin, "-m").Output(); err == nil {
				for _, l := range strings.Split(string(o), "\n") {
					l = strings.ToLower(strings.TrimSpace(l))
					if l != "" && !strings.HasPrefix(l, "[") {
						rt.Extensions = append(rt.Extensions, strings.ReplaceAll(l, "zend opcache", "opcache"))
					}
				}
			}
		}
		out = append(out, rt)
	}
	return out
}

// extension name → package suffix (Remi: phpXX-php-<suffix>; sury: phpX.Y-<suffix>)
var phpExtPkg = map[string][2]string{
	"mysqli": {"mysqlnd", "mysql"}, "pdo_mysql": {"mysqlnd", "mysql"}, "mysqlnd": {"mysqlnd", "mysql"},
	"pdo_pgsql": {"pgsql", "pgsql"}, "pgsql": {"pgsql", "pgsql"}, "pdo_sqlite": {"pdo", "sqlite3"}, "sqlite3": {"pdo", "sqlite3"},
	"gd": {"gd", "gd"}, "zip": {"pecl-zip", "zip"}, "intl": {"intl", "intl"}, "opcache": {"opcache", "opcache"}, "bcmath": {"bcmath", "bcmath"},
	"mbstring": {"mbstring", "mbstring"}, "xml": {"xml", "xml"}, "xsl": {"xml", "xsl"}, "soap": {"soap", "soap"}, "gmp": {"gmp", "gmp"},
	"redis": {"pecl-redis6", "redis"}, "imagick": {"pecl-imagick-im7", "imagick"}, "apcu": {"pecl-apcu", "apcu"}, "memcached": {"pecl-memcached", "memcached"},
	"mongodb": {"pecl-mongodb", "mongodb"}, "sodium": {"sodium", ""}, "exif": {"common", ""}, "sockets": {"common", ""}, "calendar": {"common", ""},
	"ldap": {"ldap", "ldap"}, "pcntl": {"process", ""}, "posix": {"process", ""}, "tidy": {"tidy", "tidy"}, "bz2": {"bz2", "bz2"}, "curl": {"common", "curl"},
}

var phpDefaultNativeExt = []string{"mysqli", "pdo_mysql", "pdo_sqlite", "gd", "zip", "intl", "opcache", "mbstring", "xml", "bcmath", "redis", "sodium", "pcntl", "exif", "curl"}

func phpPackages(v string, exts []string) ([]string, error) {
	deb := debianLike()
	t := strings.ReplaceAll(v, ".", "")
	seen := map[string]bool{}
	var pk []string
	add := func(p string) {
		if p != "" && !seen[p] {
			seen[p] = true
			pk = append(pk, p)
		}
	}
	if deb {
		add("php" + v + "-fpm")
		add("php" + v + "-cli")
	} else {
		add("php" + t + "-php-fpm")
		add("php" + t + "-php-cli")
	}
	for _, e := range exts {
		m, ok := phpExtPkg[e]
		if !ok {
			return nil, errs.New(400, "php.bad_ext", "ext", e)
		}
		if deb {
			if m[1] != "" {
				add("php" + v + "-" + m[1])
			}
		} else {
			add("php" + t + "-php-" + m[0])
		}
	}
	return pk, nil
}

// ensurePHPRepo adds Remi (Fedora/EL) or sury (Debian/Ubuntu) if it is not there.
func ensurePHPRepo(ctx context.Context, log func(string)) error {
	if debianLike() {
		if _, err := os.Stat("/etc/apt/sources.list.d/php.list"); err == nil {
			return nil
		}
		log("adding the sury PHP repository")
		script := `set -e; apt-get update -qq; DEBIAN_FRONTEND=noninteractive apt-get -y -qq install ca-certificates curl lsb-release >/dev/null
curl -fsSL -o /usr/share/keyrings/sury-php.gpg https://packages.sury.org/php/apt.gpg
. /etc/os-release; echo "deb [signed-by=/usr/share/keyrings/sury-php.gpg] https://packages.sury.org/php/ ${VERSION_CODENAME} main" > /etc/apt/sources.list.d/php.list
apt-get update -qq`
		n, a := sysexec.Scoped("sh", "-c", script)
		if out, err := exec.CommandContext(ctx, n, a...).CombinedOutput(); err != nil {
			return errors.New("sury: " + lastLine(string(out)))
		}
		return nil
	}
	if out, _ := run(ctx, "rpm", "-q", "remi-release"); strings.HasPrefix(out, "remi-release") {
		return nil
	}
	log("adding the Remi PHP repository")
	rel, _ := run(ctx, "rpm", "-E", "%fedora")
	url := "https://rpms.remirepo.net/fedora/remi-release-" + strings.TrimSpace(rel) + ".rpm"
	if strings.Contains(rel, "%") { // EL
		el, _ := run(ctx, "rpm", "-E", "%rhel")
		url = "https://rpms.remirepo.net/enterprise/remi-release-" + strings.TrimSpace(el) + ".rpm"
	}
	n, a := sysexec.Wrap("dnf", "-y", "-q", "install", url)
	if out, err := exec.CommandContext(ctx, n, a...).CombinedOutput(); err != nil {
		return errors.New("remi-release: " + lastLine(string(out)))
	}
	return nil
}

func ensureComposer(ctx context.Context, log func(string)) error {
	if _, err := os.Stat(composerPhar); err == nil {
		return nil
	}
	log("downloading composer")
	os.MkdirAll(filepath.Dir(composerPhar), 0o755)
	hc := &http.Client{Timeout: 2 * time.Minute}
	get := func(u string) ([]byte, error) {
		resp, err := hc.Get(u)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			return nil, fmt.Errorf("%s: %s", u, resp.Status)
		}
		return io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	}
	phar, err := get("https://getcomposer.org/download/latest-stable/composer.phar")
	if err != nil {
		return err
	}
	sum, err := get("https://getcomposer.org/download/latest-stable/composer.phar.sha256")
	if err != nil {
		return err
	}
	if fmt.Sprintf("%x", sha256Sum(phar)) != strings.Fields(string(sum))[0] {
		return errors.New("composer checksum mismatch")
	}
	return os.WriteFile(composerPhar, phar, 0o755)
}

// PHPInstall installs a version (and extensions) natively.
func PHPInstall(ctx context.Context, v string, exts []string, log func(string)) error {
	if !phpVerNativeRe.MatchString(v) {
		return errs.New(400, "php.bad_version", "version", v)
	}
	if len(exts) == 0 {
		exts = phpDefaultNativeExt
	}
	if err := ensurePHPRepo(ctx, log); err != nil {
		return err
	}
	pk, err := phpPackages(v, exts)
	if err != nil {
		return err
	}
	var name string
	var args []string
	if debianLike() {
		name, args = "apt-get", append([]string{"-y", "-q", "install"}, pk...)
	} else {
		name, args = "dnf", append([]string{"-y", "-q", "install"}, pk...)
	}
	defer TempSwap(ctx, log)()
	log("$ " + name + " install " + strings.Join(pk, " "))
	n, a := sysexec.Wrap(name, args...)
	cmd := exec.CommandContext(ctx, n, a...)
	cmd.Env = append(os.Environ(), "DEBIAN_FRONTEND=noninteractive")
	if out, err := cmd.CombinedOutput(); err != nil {
		return errs.New(500, "php.install_failed", "detail", lastLine(string(out)))
	}
	ensureShims(v)
	if err := ensureComposer(ctx, log); err != nil {
		log("composer: " + err.Error() + " (sites can still run; composer install will not)")
	}
	return nil
}

// PHPUninstall removes a version's packages (the panel checks no site uses it).
func PHPUninstall(ctx context.Context, v string, log func(string)) error {
	if !phpVerNativeRe.MatchString(v) {
		return errs.New(400, "php.bad_version", "version", v)
	}
	m, _ := filepath.Glob(phpConfRoot + "/*/version")
	for _, f := range m {
		if b, _ := os.ReadFile(f); strings.TrimSpace(string(b)) == v {
			return errs.New(409, "php.in_use", "site", "")
		}
	}
	var name string
	var args []string
	if debianLike() {
		name, args = "apt-get", []string{"-y", "-q", "purge", "php" + v + "-*"}
	} else {
		name, args = "dnf", []string{"-y", "-q", "remove", "php" + strings.ReplaceAll(v, ".", "") + "-*"}
	}
	log("$ " + name + " " + strings.Join(args, " "))
	n, a := sysexec.Wrap(name, args...)
	if out, err := exec.CommandContext(ctx, n, a...).CombinedOutput(); err != nil {
		return errs.New(500, "php.install_failed", "detail", lastLine(string(out)))
	}
	os.RemoveAll("/opt/berth/php-shims/" + v)
	return nil
}

// ---- a site's FPM ----

type PHPSite struct {
	ID          int64             `json:"id"`
	Domain      string            `json:"domain"` // its folder under /srv/berth/caddy/sites
	Version     string            `json:"version"`
	RootSub     string            `json:"root_sub,omitempty"`
	Ini         map[string]string `json:"ini,omitempty"`
	MaxChildren int               `json:"max_children,omitempty"`
	MemMB       int               `json:"mem_mb,omitempty"`
	Workers     []PHPWorker       `json:"workers,omitempty"`
	Schedule    string            `json:"schedule,omitempty"`
}

var phpIniKeyRe = regexp.MustCompile(`^[a-z_.]{1,40}$`)

func (s *PHPSite) Validate() error {
	if s.ID <= 0 || s.ID > 4999 {
		return errs.New(400, "php.bad_site")
	}
	if !domainDirRe.MatchString(s.Domain) || strings.Contains(s.Domain, "..") {
		return errs.New(400, "php.bad_site")
	}
	if !phpVerNativeRe.MatchString(s.Version) {
		return errs.New(400, "php.bad_version", "version", s.Version)
	}
	if s.RootSub != "" && (!rootSubRe.MatchString(s.RootSub) || strings.Contains(s.RootSub, "..")) {
		return errs.New(400, "php.bad_root_sub")
	}
	for k, v := range s.Ini {
		if !phpIniKeyRe.MatchString(k) || strings.ContainsAny(v, "\n\r\"") {
			return errs.New(400, "php.bad_ini", "key", k)
		}
	}
	for _, w := range s.Workers {
		if !workerNameRe.MatchString(w.Name) || w.Name == "schedule" || strings.TrimSpace(w.Command) == "" || strings.ContainsAny(w.Command, "\n\r") {
			return errs.New(400, "php.bad_worker", "name", w.Name)
		}
	}
	if strings.ContainsAny(s.Schedule, "\n\r") || s.MaxChildren < 0 || s.MaxChildren > 200 || s.MemMB < 0 {
		return errs.New(400, "php.bad_site")
	}
	return nil
}

func (s PHPSite) root() string { return filepath.Join(PHPSitesRoot, s.Domain) }

func (s PHPSite) fpmConf() string {
	var b strings.Builder
	root := s.root()
	max := s.MaxChildren
	if max == 0 {
		max = 10
	}
	// stderr under systemd is a journal socket FPM cannot open as a file: log through syslog
	fmt.Fprintf(&b, "; Managed by Berth: PHP-FPM of site %d (%s)\n[global]\nerror_log = syslog\nsyslog.ident = berth-php-%d\ndaemonize = no\nlog_limit = 8192\n\n", s.ID, s.Domain, s.ID)
	fmt.Fprintf(&b, "[site%d]\nlisten = 0.0.0.0:%d\npm = ondemand\npm.max_children = %d\npm.process_idle_timeout = 30s\npm.max_requests = 1000\n", s.ID, PHPSitePort(s.ID), max)
	b.WriteString("ping.path = /berth-ping\nping.response = pong\ncatch_workers_output = yes\ndecorate_workers_output = no\nclear_env = no\n")
	bin, _ := phpPaths(s.Version)
	share := filepath.Join(filepath.Dir(filepath.Dir(bin)), "share", "pear")
	vals := map[string]string{
		"open_basedir":      root + ":/tmp:" + share,
		"session.save_path": "/tmp",
		"upload_tmp_dir":    "/tmp",
		"sys_temp_dir":      "/tmp",
		"expose_php":        "Off",
	}
	for k, v := range s.Ini {
		vals[k] = v
	}
	keys := make([]string, 0, len(vals))
	for k := range vals {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(&b, "php_admin_value[%s] = %s\n", k, vals[k])
	}
	return b.String()
}

func (s PHPSite) sandbox() string {
	var b strings.Builder
	fmt.Fprintf(&b, "User=%s\nGroup=%s\nWorkingDirectory=%s\n", phpSiteUser(s.ID), phpSiteUser(s.ID), s.root())
	fmt.Fprintf(&b, "Environment=PATH=%s\nEnvironment=HOME=%s\n", phpPathEnv(s.Version), s.root())
	fmt.Fprintf(&b, "ProtectSystem=strict\nReadWritePaths=%s\n", s.root())
	b.WriteString("PrivateTmp=yes\nProtectHome=yes\nNoNewPrivileges=yes\nRestrictSUIDSGID=yes\nProtectKernelTunables=yes\nProtectKernelModules=yes\nProtectKernelLogs=yes\nProtectControlGroups=yes\nProtectClock=yes\nRestrictNamespaces=yes\nLockPersonality=yes\nRestrictRealtime=yes\nPrivateDevices=yes\n")
	if s.MemMB > 0 {
		fmt.Fprintf(&b, "MemoryMax=%dM\n", s.MemMB)
	}
	return b.String()
}

// ensurePHPSELinux: php-fpm is labeled for the confined web server domain (httpd_t), which may
// bind only HTTP ports and read only web content. Each site already has its own user and a
// systemd sandbox, so it runs like other services (unconfined_service_t) instead; this
// one-rule module lets that domain start php-fpm.
func ensurePHPSELinux(ctx context.Context, log func(string)) error {
	if !selinuxOn() {
		return nil
	}
	if out, _ := run(ctx, "semodule", "-l"); strings.Contains("\n"+out+"\n", "\nberth_php\n") {
		return nil
	}
	f := "/var/lib/berth/berth_php.cil"
	os.MkdirAll(filepath.Dir(f), 0o755)
	if err := os.WriteFile(f, []byte("; Berth: run a site's php-fpm as an ordinary service (the site is sandboxed by systemd)\n(allow unconfined_service_t httpd_exec_t (file (entrypoint)))\n"), 0o644); err != nil {
		return err
	}
	log("semodule -i berth_php.cil")
	if out, err := run(ctx, "semodule", "-i", f); err != nil {
		return errors.New("semodule: " + out)
	}
	return nil
}

func (s PHPSite) fpmUnit() string {
	_, fpm := phpPaths(s.Version)
	return fmt.Sprintf("# Managed by Berth: PHP-FPM of site %d (%s).\n[Unit]\nDescription=Berth PHP %s for %s\nAfter=network-online.target\n\n[Service]\nSELinuxContext=-system_u:system_r:unconfined_service_t:s0\n%sExecStart=%s --nodaemonize --fpm-config %s/%d/fpm.conf\nExecReload=/bin/kill -USR2 $MAINPID\nRestart=always\nRestartSec=3\n\n[Install]\nWantedBy=multi-user.target\n",
		s.ID, s.Domain, s.Version, s.Domain, s.sandbox(), fpm, phpConfRoot, s.ID)
}

func (s PHPSite) workerUnit(w PHPWorker) string {
	return fmt.Sprintf("# Managed by Berth: worker %s of site %d (%s).\n[Unit]\nDescription=Berth %s worker %s\nAfter=network-online.target %s\n\n[Service]\n%sExecStart=/bin/sh -c %s\nRestart=always\nRestartSec=5\n\n[Install]\nWantedBy=multi-user.target\n",
		w.Name, s.ID, s.Domain, s.Domain, w.Name, phpUnit(s.ID), s.sandbox(), strconv.Quote("exec "+w.Command))
}

func (s PHPSite) schedUnits() (service, timer string) {
	service = fmt.Sprintf("# Managed by Berth: scheduler of site %d (%s).\n[Unit]\nDescription=Berth %s scheduler\n\n[Service]\nType=oneshot\n%sExecStart=/bin/sh -c %s\n",
		s.ID, s.Domain, s.Domain, s.sandbox(), strconv.Quote("exec "+s.Schedule))
	timer = fmt.Sprintf("# Managed by Berth.\n[Unit]\nDescription=Berth %s scheduler, every minute\n\n[Timer]\nOnCalendar=*-*-* *:*:00\nAccuracySec=1s\n\n[Install]\nWantedBy=timers.target\n", s.Domain)
	return
}

func ownerOf(p string) string {
	fi, err := os.Stat(p)
	if err != nil {
		return ""
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return ""
	}
	return fmt.Sprintf("%d:%d", st.Uid, st.Gid)
}

// fcgiPing asks FPM's ping page over FastCGI (no file needed in the site).
func fcgiPing(port int) error {
	c, err := net.DialTimeout("tcp", "127.0.0.1:"+strconv.Itoa(port), 3*time.Second)
	if err != nil {
		return err
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(5 * time.Second))
	rec := func(typ byte, content []byte) []byte {
		var b bytes.Buffer
		pad := (8 - len(content)%8) % 8
		b.Write([]byte{1, typ, 0, 1, byte(len(content) >> 8), byte(len(content)), byte(pad), 0})
		b.Write(content)
		b.Write(make([]byte, pad))
		return b.Bytes()
	}
	pair := func(k, v string) []byte {
		return append(append([]byte{byte(len(k)), byte(len(v))}, k...), v...)
	}
	var params []byte
	for _, kv := range [][2]string{{"SCRIPT_NAME", "/berth-ping"}, {"SCRIPT_FILENAME", "/berth-ping"}, {"REQUEST_METHOD", "GET"}, {"REQUEST_URI", "/berth-ping"}, {"SERVER_PROTOCOL", "HTTP/1.1"}} {
		params = append(params, pair(kv[0], kv[1])...)
	}
	var req []byte
	req = append(req, rec(1, []byte{0, 1, 0, 0, 0, 0, 0, 0})...) // BEGIN_REQUEST, responder
	req = append(req, rec(4, params)...)
	req = append(req, rec(4, nil)...)
	req = append(req, rec(5, nil)...)
	if _, err := c.Write(req); err != nil {
		return err
	}
	var out bytes.Buffer
	hdr := make([]byte, 8)
	for {
		if _, err := io.ReadFull(c, hdr); err != nil {
			return err
		}
		n := int(binary.BigEndian.Uint16(hdr[4:6]))
		body := make([]byte, n+int(hdr[6]))
		if _, err := io.ReadFull(c, body); err != nil {
			return err
		}
		if hdr[1] == 6 {
			out.Write(body[:n])
		}
		if hdr[1] == 3 { // END_REQUEST
			break
		}
	}
	if !strings.Contains(out.String(), "pong") {
		return errors.New("FPM answered without pong: " + strings.TrimSpace(out.String()))
	}
	return nil
}

type PHPSiteResult struct {
	Port      int    `json:"port"`
	PrevOwner string `json:"prev_owner,omitempty"` // the folder's owner before (to give it back)
}

// PHPSiteSet creates or updates a site's sandboxed FPM (and workers), and waits for it to answer.
func PHPSiteSet(ctx context.Context, s PHPSite, log func(string)) (PHPSiteResult, error) {
	var res PHPSiteResult
	if err := s.Validate(); err != nil {
		return res, err
	}
	if _, fpm := phpPaths(s.Version); !fileExists(fpm) {
		return res, errs.New(409, "php.not_installed", "version", s.Version)
	}
	ensureShims(s.Version)
	if err := ensurePHPSELinux(ctx, log); err != nil {
		return res, err
	}
	root := s.root()
	if err := os.MkdirAll(root, 0o755); err != nil {
		return res, err
	}
	user := phpSiteUser(s.ID)
	if out, err := run(ctx, "id", "-u", user); err != nil || strings.TrimSpace(out) == "" {
		if out, err := run(ctx, "useradd", "--system", "--home-dir", root, "--no-create-home", "--shell", "/sbin/nologin", "--comment", "Berth PHP site "+s.Domain, user); err != nil {
			return res, errors.New("useradd: " + out)
		}
	}
	owner := ownerOf(root)
	uid, _ := run(ctx, "id", "-u", user)
	gid, _ := run(ctx, "id", "-g", user)
	mine := strings.TrimSpace(uid) + ":" + strings.TrimSpace(gid)
	if owner != mine {
		res.PrevOwner = owner
		log("chown -R " + user + " " + root)
		if out, err := exec.CommandContext(ctx, "chown", "-R", mine, root).CombinedOutput(); err != nil {
			return res, errors.New("chown: " + strings.TrimSpace(string(out)))
		}
		exec.CommandContext(ctx, "chmod", "755", root).Run()
		exec.CommandContext(ctx, "restorecon", "-R", root).Run()
	}
	dir := filepath.Join(phpConfRoot, strconv.FormatInt(s.ID, 10))
	os.MkdirAll(dir, 0o755)
	if err := os.WriteFile(filepath.Join(dir, "fpm.conf"), []byte(s.fpmConf()), 0o644); err != nil {
		return res, err
	}
	os.WriteFile(filepath.Join(dir, "version"), []byte(s.Version), 0o644)
	unitDir := "/etc/systemd/system"
	os.WriteFile(filepath.Join(unitDir, phpUnit(s.ID)), []byte(s.fpmUnit()), 0o644)
	// workers: write the wanted ones, remove the others
	want := map[string]bool{}
	for _, w := range s.Workers {
		want[phpWorkerUnit(s.ID, w.Name)] = true
		os.WriteFile(filepath.Join(unitDir, phpWorkerUnit(s.ID, w.Name)), []byte(s.workerUnit(w)), 0o644)
	}
	old, _ := filepath.Glob(filepath.Join(unitDir, "berth-php-"+strconv.FormatInt(s.ID, 10)+"-*.service"))
	for _, f := range old {
		b := filepath.Base(f)
		if b == phpSchedUnit(s.ID)+".service" || want[b] {
			continue
		}
		run(ctx, "systemctl", "disable", "--now", b)
		os.Remove(f)
	}
	if s.Schedule != "" {
		svc, tm := s.schedUnits()
		os.WriteFile(filepath.Join(unitDir, phpSchedUnit(s.ID)+".service"), []byte(svc), 0o644)
		os.WriteFile(filepath.Join(unitDir, phpSchedUnit(s.ID)+".timer"), []byte(tm), 0o644)
	} else {
		run(ctx, "systemctl", "disable", "--now", phpSchedUnit(s.ID)+".timer")
		os.Remove(filepath.Join(unitDir, phpSchedUnit(s.ID)+".service"))
		os.Remove(filepath.Join(unitDir, phpSchedUnit(s.ID)+".timer"))
	}
	run(ctx, "systemctl", "daemon-reload")
	ensureNativeFirewall(ctx)
	log("systemctl restart " + phpUnit(s.ID))
	run(ctx, "systemctl", "enable", phpUnit(s.ID))
	if out, err := run(ctx, "systemctl", "restart", phpUnit(s.ID)); err != nil {
		return res, errors.New(out)
	}
	var perr error
	for i := 0; i < 20; i++ {
		time.Sleep(500 * time.Millisecond)
		if perr = fcgiPing(PHPSitePort(s.ID)); perr == nil {
			break
		}
	}
	if perr != nil {
		j, _ := run(ctx, "journalctl", "-u", phpUnit(s.ID), "-n", "15", "--no-pager", "-o", "cat")
		for _, l := range strings.Split(j, "\n") {
			log("│ " + l)
		}
		return res, errs.New(500, "php.fpm_down", "detail", perr.Error())
	}
	log("PHP-FPM answers on port " + strconv.Itoa(PHPSitePort(s.ID)))
	for _, w := range s.Workers {
		log("systemctl restart " + phpWorkerUnit(s.ID, w.Name))
		run(ctx, "systemctl", "enable", phpWorkerUnit(s.ID, w.Name))
		run(ctx, "systemctl", "restart", phpWorkerUnit(s.ID, w.Name))
	}
	if s.Schedule != "" {
		run(ctx, "systemctl", "enable", "--now", phpSchedUnit(s.ID)+".timer")
	}
	res.Port = PHPSitePort(s.ID)
	return res, nil
}

// PHPSiteRemove stops a site's FPM and workers; owner, when given ("uid:gid"), gets the folder back.
func PHPSiteRemove(ctx context.Context, id int64, domain, owner string) error {
	if id <= 0 || !domainDirRe.MatchString(domain) || strings.Contains(domain, "..") {
		return errs.New(400, "php.bad_site")
	}
	unitDir := "/etc/systemd/system"
	m, _ := filepath.Glob(filepath.Join(unitDir, "berth-php-"+strconv.FormatInt(id, 10)+"-*"))
	for _, f := range m {
		run(ctx, "systemctl", "disable", "--now", filepath.Base(f))
		os.Remove(f)
	}
	run(ctx, "systemctl", "disable", "--now", phpUnit(id))
	os.Remove(filepath.Join(unitDir, phpUnit(id)))
	os.RemoveAll(filepath.Join(phpConfRoot, strconv.FormatInt(id, 10)))
	run(ctx, "systemctl", "daemon-reload")
	root := filepath.Join(PHPSitesRoot, domain)
	if regexp.MustCompile(`^[0-9]+:[0-9]+$`).MatchString(owner) {
		if _, err := os.Stat(root); err == nil {
			exec.CommandContext(ctx, "chown", "-R", owner, root).Run()
		}
	}
	run(ctx, "userdel", phpSiteUser(id))
	dropNativeFirewallIfUnused(ctx)
	return nil
}

type PHPSiteStatus struct {
	FPM      string            `json:"fpm"` // systemd state
	Port     int               `json:"port"`
	User     string            `json:"user"`
	Workers  map[string]string `json:"workers"`
	Schedule string            `json:"schedule,omitempty"` // last run result
}

func PHPSiteStat(ctx context.Context, id int64) PHPSiteStatus {
	st := PHPSiteStatus{Port: PHPSitePort(id), User: phpSiteUser(id), Workers: map[string]string{}}
	o, _ := run(ctx, "systemctl", "is-active", phpUnit(id))
	st.FPM = strings.TrimSpace(o)
	m, _ := filepath.Glob(filepath.Join("/etc/systemd/system", "berth-php-"+strconv.FormatInt(id, 10)+"-*.service"))
	for _, f := range m {
		b := filepath.Base(f)
		name := strings.TrimSuffix(strings.TrimPrefix(b, "berth-php-"+strconv.FormatInt(id, 10)+"-"), ".service")
		if name == "schedule" {
			r, _ := run(ctx, "systemctl", "show", "-p", "Result", "--value", b)
			st.Schedule = strings.TrimSpace(r)
			continue
		}
		o, _ := run(ctx, "systemctl", "is-active", b)
		st.Workers[name] = strings.TrimSpace(o)
	}
	return st
}

// PHPSiteExec runs a command in the site's folder as the site's user (composer install, artisan…).
func PHPSiteExec(ctx context.Context, id int64, domain, version, line string, log func(string)) error {
	if id <= 0 || !domainDirRe.MatchString(domain) || !phpVerNativeRe.MatchString(version) || strings.TrimSpace(line) == "" {
		return errs.New(400, "php.bad_site")
	}
	root := filepath.Join(PHPSitesRoot, domain)
	env := []string{"PATH=" + phpPathEnv(version), "HOME=" + root, "COMPOSER_HOME=" + root + "/.composer", "COMPOSER_NO_INTERACTION=1", "LANG=C.UTF-8"}
	return asUserNamed(ctx, phpSiteUser(id), root, line, env, 2048, log)
}

// CheckPHPDir runs the compatibility check on a site's folder.
func CheckPHPDir(dir string) PHPReport {
	read := func(n string) []byte {
		b, err := os.ReadFile(filepath.Join(dir, n))
		if err != nil {
			return nil
		}
		return b
	}
	files := PHPFiles{Artisan: fileExists(filepath.Join(dir, "artisan")), WordPress: fileExists(filepath.Join(dir, "wp-load.php")),
		Symfony: fileExists(filepath.Join(dir, "bin", "console")), Think: fileExists(filepath.Join(dir, "think")), Vendor: fileExists(filepath.Join(dir, "vendor", "autoload.php"))}
	return AnalyzePHP(read("composer.json"), read("composer.lock"), files, PHPNativeVersions, []string{"8.1", "7.4"})
}

func sha256Sum(b []byte) [32]byte { return sha256.Sum256(b) }
