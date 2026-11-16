package system

import (
	"strings"
	"testing"
)

func TestPHPSiteConfig(t *testing.T) {
	s := PHPSite{ID: 7, Domain: "blog.example.com", Version: "8.4", Ini: map[string]string{"memory_limit": "512M"}, MemMB: 512,
		Workers: []PHPWorker{{Name: "queue", Command: "php artisan queue:work"}}, Schedule: "php artisan schedule:run"}
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	c := s.fpmConf()
	for _, want := range []string{"listen = 0.0.0.0:25007", "pm = ondemand", "ping.path = /berth-ping",
		"php_admin_value[open_basedir] = /srv/berth/caddy/sites/blog.example.com:/tmp:", "php_admin_value[memory_limit] = 512M", "daemonize = no", "error_log = syslog"} {
		if !strings.Contains(c, want) {
			t.Errorf("fpm.conf missing %q\n%s", want, c)
		}
	}
	if strings.Contains(c, "\nuser =") {
		t.Error("the pool must not switch users (the unit runs it as the site's user)")
	}
	u := s.fpmUnit()
	for _, want := range []string{"User=site-web7", "ProtectSystem=strict", "ReadWritePaths=/srv/berth/caddy/sites/blog.example.com", "MemoryMax=512M",
		"--fpm-config /etc/berth-php/7/fpm.conf", "NoNewPrivileges=yes"} {
		if !strings.Contains(u, want) {
			t.Errorf("unit missing %q", want)
		}
	}
	w := s.workerUnit(s.Workers[0])
	if !strings.Contains(w, "User=site-web7") || !strings.Contains(w, "queue:work") {
		t.Errorf("worker unit: %s", w)
	}
	svc, tm := s.schedUnits()
	if !strings.Contains(svc, "Type=oneshot") || !strings.Contains(tm, "OnCalendar=*-*-* *:*:00") {
		t.Error("scheduler units")
	}
}

func TestPHPSiteValidate(t *testing.T) {
	bad := []PHPSite{
		{ID: 0, Domain: "a.com", Version: "8.4"},
		{ID: 1, Domain: "../etc", Version: "8.4"},
		{ID: 1, Domain: "a.com", Version: "7.4"},
		{ID: 1, Domain: "a.com", Version: "8.4", RootSub: "../x"},
		{ID: 1, Domain: "a.com", Version: "8.4", Ini: map[string]string{"memory_limit": "1G\nextension=evil.so"}},
		{ID: 1, Domain: "a.com", Version: "8.4", Workers: []PHPWorker{{Name: "Bad Name", Command: "x"}}},
		{ID: 1, Domain: "a.com", Version: "8.4", Workers: []PHPWorker{{Name: "schedule", Command: "x"}}},
	}
	for i, b := range bad {
		if b.Validate() == nil {
			t.Errorf("bad %d accepted", i)
		}
	}
}

func TestPHPPackages(t *testing.T) {
	p, err := phpPackages("8.4", []string{"mysqli", "pdo_mysql", "redis", "gd"})
	if err != nil {
		t.Fatal(err)
	}
	j := strings.Join(p, " ")
	if debianLike() {
		t.Skip("package names differ on Debian")
	}
	for _, want := range []string{"php84-php-fpm", "php84-php-cli", "php84-php-mysqlnd", "php84-php-pecl-redis6", "php84-php-gd"} {
		if !strings.Contains(j, want) {
			t.Errorf("missing %s in %s", want, j)
		}
	}
	if strings.Count(j, "mysqlnd") != 1 {
		t.Error("mysqlnd listed twice")
	}
	if _, err := phpPackages("8.4", []string{"nonsense"}); err == nil {
		t.Error("unknown extension accepted")
	}
}
