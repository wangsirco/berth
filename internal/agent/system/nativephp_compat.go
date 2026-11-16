package system

// Compatibility check for a PHP site (R2): which PHP version (native or only in a container),
// which extensions, which framework (document root, queue workers, the scheduler).

import (
	"encoding/json"
	"sort"
	"strings"
)

type PHPFiles struct {
	Artisan   bool // Laravel
	WordPress bool
	Symfony   bool // bin/console
	Think     bool // think (ThinkPHP)
	Vendor    bool
}

type PHPWorker struct {
	Name    string `json:"name"`
	Command string `json:"command"`
}

type PHPReport struct {
	Framework  string      `json:"framework"` // laravel | symfony | thinkphp | wordpress | ""
	Version    string      `json:"version"`
	Native     bool        `json:"native"` // false: only a container runtime fits
	RootSub    string      `json:"root_sub"`
	Extensions []string    `json:"extensions"`
	Workers    []PHPWorker `json:"workers"`
	Schedule   string      `json:"schedule,omitempty"`
	Findings   []Finding   `json:"findings"`
}

// extensions every PHP build Berth installs has (no package needed)
var phpBuiltin = map[string]bool{"json": true, "ctype": true, "tokenizer": true, "filter": true, "hash": true, "pcre": true, "spl": true, "reflection": true,
	"session": true, "openssl": true, "zlib": true, "curl": true, "fileinfo": true, "iconv": true, "phar": true, "posix": true, "simplexml": true,
	"dom": true, "xml": true, "xmlwriter": true, "xmlreader": true, "libxml": true, "pdo": true, "mbstring": true, "standard": true, "date": true}

func addPHP(r *PHPReport, level, code string, kv ...string) {
	f := Finding{Level: level, Code: code}
	if len(kv) > 0 {
		f.Params = map[string]string{}
		for i := 0; i+1 < len(kv); i += 2 {
			f.Params[kv[i]] = kv[i+1]
		}
	}
	r.Findings = append(r.Findings, f)
}

// AnalyzePHP reads composer.json/.lock and the site's files. native and container are the PHP
// versions Berth can run each way, newest last.
func AnalyzePHP(composerJSON, composerLock []byte, files PHPFiles, native, container []string) PHPReport {
	r := PHPReport{Extensions: []string{}, Workers: []PHPWorker{}, Findings: []Finding{}}
	var cj struct {
		Require map[string]string `json:"require"`
	}
	if composerJSON != nil {
		if err := json.Unmarshal(composerJSON, &cj); err != nil {
			addPHP(&r, "warn", "php_bad_composer")
		}
	}
	want := strings.TrimSpace(cj.Require["php"])
	exts := map[string]bool{}
	for k := range cj.Require {
		if e, ok := strings.CutPrefix(k, "ext-"); ok {
			exts[strings.ToLower(e)] = true
		}
	}
	if composerLock != nil {
		var l struct {
			Packages []struct {
				Require map[string]string `json:"require"`
			} `json:"packages"`
		}
		json.Unmarshal(composerLock, &l)
		for _, p := range l.Packages {
			for k := range p.Require {
				if e, ok := strings.CutPrefix(k, "ext-"); ok {
					exts[strings.ToLower(e)] = true
				}
			}
		}
	}

	// framework
	switch {
	case files.Artisan || cj.Require["laravel/framework"] != "":
		r.Framework, r.RootSub = "laravel", "public"
		r.Workers = append(r.Workers, PHPWorker{Name: "queue", Command: "php artisan queue:work --sleep=3 --tries=3 --max-time=3600"})
		r.Schedule = "php artisan schedule:run"
		for _, e := range []string{"pdo_mysql", "bcmath", "intl"} {
			exts[e] = true
		}
	case files.Symfony || cj.Require["symfony/framework-bundle"] != "":
		r.Framework, r.RootSub = "symfony", "public"
		exts["intl"] = true
	case files.Think || cj.Require["topthink/framework"] != "":
		r.Framework, r.RootSub = "thinkphp", "public"
	case files.WordPress:
		r.Framework = "wordpress"
		for _, e := range []string{"mysqli", "gd", "zip", "intl", "exif"} {
			exts[e] = true
		}
	}

	// version: the newest native one that fits, else a container one
	for i := len(native) - 1; i >= 0; i-- {
		if satisfies(native[i]+".0", want) {
			r.Version, r.Native = native[i], true
			break
		}
	}
	if r.Version == "" {
		for _, v := range container {
			if satisfies(v+".0", want) {
				r.Version = v
				break
			}
		}
		if r.Version != "" {
			addPHP(&r, "warn", "php_container_only", "version", r.Version, "range", want)
		} else {
			addPHP(&r, "block", "php_version", "range", want)
		}
	} else {
		addPHP(&r, "ok", "php_version", "version", r.Version, "range", want)
	}

	// extensions to install (the ones not built in)
	for e := range exts {
		if e == "opcache" || e == "zend-opcache" {
			continue
		}
		if !phpBuiltin[e] {
			r.Extensions = append(r.Extensions, e)
		}
	}
	sort.Strings(r.Extensions)
	if len(r.Extensions) > 0 {
		addPHP(&r, "ok", "php_extensions", "list", strings.Join(r.Extensions, ", "))
	}
	if composerJSON != nil && !files.Vendor {
		addPHP(&r, "warn", "php_no_vendor")
	}
	if r.Framework == "laravel" {
		addPHP(&r, "ok", "php_laravel")
	}
	return r
}
