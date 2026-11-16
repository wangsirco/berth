package system

// Compatibility check for a Node project before it is deployed natively (兼容性检测): which
// Node it needs, which package manager, which native modules (and whether this ARM host can
// use them without compiling), which framework (for default commands), and whether the build
// fits in memory. Pure functions over the project's files, so they are easy to test.

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"
)

type Finding struct {
	Level  string            `json:"level"` // ok | warn | block
	Code   string            `json:"code"`
	Params map[string]string `json:"params,omitempty"`
}

// NodeReport is the check of any project (the name stays from R1): Kind says which language.
type NodeReport struct {
	Kind           string    `json:"kind"`              // node | java | go | python
	Version        string    `json:"version"`           // runtime version to use
	Release        string    `json:"release,omitempty"` // suggested command before start (migrations)
	Tools          []string  `json:"tools,omitempty"`   // extra mise tools the build needs (maven@3 …)
	Framework      string    `json:"framework"`         // next | nuxt | remix | astro | nest | express | spring | django | flask | fastapi | ""
	PackageManager string    `json:"package_manager"`   // npm | pnpm | yarn
	Node           string    `json:"node"`              // version to use: an installed one, or a major to install
	Install        string    `json:"install"`
	Build          string    `json:"build"`
	Start          string    `json:"start"`
	Health         string    `json:"health"`
	Shared         []string  `json:"shared"`
	Findings       []Finding `json:"findings"`
}

// native modules: true = prebuilt for linux-arm64 glibc (no compiler needed)
var nativeMods = map[string]bool{
	"sharp": true, "@img/sharp-linux-arm64": true, "esbuild": true, "@swc/core": true, "lightningcss": true, "@parcel/watcher": true,
	"@node-rs/argon2": true, "@node-rs/bcrypt": true, "@libsql/client": true, "bufferutil": true, "utf-8-validate": true, "@prisma/client": true,
	"better-sqlite3": false, "bcrypt": false, "canvas": false, "sqlite3": false, "argon2": false, "node-sass": false, "re2": false,
}

// packages known to crash on 16K-page kernels (jemalloc built for 4K pages, …)
var pageSize16kBad = map[string]string{"node-rdkafka": "librdkafka bundles jemalloc"}

func add(r *NodeReport, level, code string, kv ...string) {
	f := Finding{Level: level, Code: code}
	if len(kv) > 0 {
		f.Params = map[string]string{}
		for i := 0; i+1 < len(kv); i += 2 {
			f.Params[kv[i]] = kv[i+1]
		}
	}
	r.Findings = append(r.Findings, f)
}

// lockPackages lists package names a lockfile mentions (npm v2/v3, pnpm, yarn).
func lockPackages(lock map[string][]byte) map[string]bool {
	out := map[string]bool{}
	if b, ok := lock["package-lock.json"]; ok {
		var l struct {
			Packages map[string]json.RawMessage `json:"packages"`
		}
		json.Unmarshal(b, &l)
		for k := range l.Packages {
			if i := strings.LastIndex(k, "node_modules/"); i >= 0 {
				out[k[i+len("node_modules/"):]] = true
			}
		}
	}
	for _, f := range []string{"pnpm-lock.yaml", "yarn.lock"} {
		if b, ok := lock[f]; ok {
			s := string(b)
			for name := range nativeMods {
				if strings.Contains(s, "/"+name+"@") || strings.Contains(s, "\n"+name+"@") || strings.Contains(s, "'"+name+"@") || strings.Contains(s, "\""+name+"@") || strings.Contains(s, " "+name+"@") {
					out[name] = true
				}
			}
			for name := range pageSize16kBad {
				if strings.Contains(s, name+"@") {
					out[name] = true
				}
			}
		}
	}
	return out
}

// AnalyzeNode looks at package.json and its lockfile. installed are the Node versions mise has;
// freeMB is free memory plus swap; have says which build tools exist (gcc, g++, make, python3).
func AnalyzeNode(pkgJSON []byte, lock map[string][]byte, installed []string, freeMB int, have map[string]bool) NodeReport {
	r := NodeReport{Kind: "node", Findings: []Finding{}, Health: "/"}
	var p struct {
		Engines         map[string]string `json:"engines"`
		Scripts         map[string]string `json:"scripts"`
		Dependencies    map[string]string `json:"dependencies"`
		DevDependencies map[string]string `json:"devDependencies"`
		PackageManager  string            `json:"packageManager"`
	}
	if err := json.Unmarshal(pkgJSON, &p); err != nil {
		add(&r, "block", "no_package_json")
		return r
	}
	deps := map[string]bool{}
	for k := range p.Dependencies {
		deps[k] = true
	}
	for k := range p.DevDependencies {
		deps[k] = true
	}

	// package manager
	switch {
	case lock["pnpm-lock.yaml"] != nil || strings.HasPrefix(p.PackageManager, "pnpm"):
		r.PackageManager, r.Install = "pnpm", "corepack pnpm install --frozen-lockfile"
	case lock["yarn.lock"] != nil || strings.HasPrefix(p.PackageManager, "yarn"):
		r.PackageManager, r.Install = "yarn", "corepack yarn install --immutable"
	default:
		r.PackageManager, r.Install = "npm", "npm ci --no-audit --no-fund"
		if lock["package-lock.json"] == nil {
			r.Install = "npm install --no-audit --no-fund"
		}
	}
	if len(lock) == 0 {
		add(&r, "warn", "no_lockfile")
	}
	run := map[string]string{"npm": "npm run ", "pnpm": "corepack pnpm run ", "yarn": "corepack yarn run "}[r.PackageManager]

	// framework and default commands
	switch {
	case deps["next"]:
		r.Framework, r.Start = "next", "npx next start -p $PORT"
		r.Shared = []string{".next/cache", "data"}
	case deps["nuxt"]:
		r.Framework, r.Start = "nuxt", "node .output/server/index.mjs"
		r.Shared = []string{"data"}
	case deps["@remix-run/serve"] || deps["@remix-run/node"]:
		r.Framework, r.Start = "remix", run+"start"
		r.Shared = []string{"data"}
	case deps["astro"]:
		r.Framework, r.Start = "astro", "node ./dist/server/entry.mjs"
		r.Shared = []string{"data"}
	case deps["@nestjs/core"]:
		r.Framework, r.Start = "nest", "node dist/main.js"
		r.Shared = []string{"data", "uploads"}
	case deps["express"] || deps["fastify"] || deps["koa"]:
		if deps["fastify"] {
			r.Framework = "fastify"
		} else if deps["koa"] {
			r.Framework = "koa"
		} else {
			r.Framework = "express"
		}
		r.Shared = []string{"data", "uploads"}
	default:
		r.Shared = []string{"data"}
	}
	if _, ok := p.Scripts["build"]; ok {
		r.Build = run + "build"
	} else if r.Framework == "next" {
		r.Build = "npx next build"
	} else if r.Framework == "nuxt" {
		r.Build = "npx nuxt build"
	}
	if r.Start == "" {
		if _, ok := p.Scripts["start"]; ok {
			r.Start = run + "start"
		} else {
			r.Start = "node index.js"
			add(&r, "warn", "no_start_script")
		}
	}

	// Node version
	want := strings.TrimSpace(p.Engines["node"])
	sort.Slice(installed, func(i, j int) bool { return verLess(installed[j], installed[i]) }) // newest first
	for _, v := range installed {
		if satisfies(v, want) {
			r.Node = v
			break
		}
	}
	if r.Node != "" {
		add(&r, "ok", "node_version", "version", r.Node, "range", want)
	} else {
		for _, major := range []string{"24", "22", "20", "18"} {
			if satisfies(major+".99.0", want) {
				r.Node = major
				break
			}
		}
		if r.Node == "" {
			add(&r, "block", "node_version", "range", want)
		} else {
			add(&r, "ok", "node_install", "version", r.Node, "range", want)
		}
	}

	// native modules
	pk := lockPackages(lock)
	for k := range deps {
		pk[k] = true
	}
	var pre, build []string
	for name, prebuilt := range nativeMods {
		if !pk[name] {
			continue
		}
		if prebuilt {
			pre = append(pre, name)
		} else {
			build = append(build, name)
		}
	}
	sort.Strings(pre)
	sort.Strings(build)
	if len(pre) > 0 {
		add(&r, "ok", "native_prebuilt", "list", strings.Join(pre, ", "))
	}
	if len(build) > 0 {
		missing := []string{}
		for _, t := range []string{"gcc", "g++", "make", "python3"} {
			if !have[t] {
				missing = append(missing, t)
			}
		}
		if len(missing) > 0 {
			add(&r, "warn", "native_build_tools", "list", strings.Join(build, ", "), "missing", strings.Join(missing, " "))
		} else {
			add(&r, "ok", "native_build", "list", strings.Join(build, ", "))
		}
	}
	for name, why := range pageSize16kBad {
		if pk[name] {
			add(&r, "warn", "page16k", "name", name, "why", why)
		}
	}

	// memory for the build
	need := 600
	if r.Framework == "next" {
		need = 2500
	} else if r.Framework == "nuxt" || r.Framework == "astro" || r.Framework == "remix" {
		need = 1500
	}
	if r.Build != "" && freeMB > 0 && freeMB < need {
		add(&r, "warn", "build_memory", "need", strconv.Itoa(need), "free", strconv.Itoa(freeMB))
	}
	r.Version = r.Node
	return r
}

// ---- version ranges (the forms package.json engines use in practice) ----

func parseVer(v string) [3]int {
	var out [3]int
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	for i, p := range strings.SplitN(v, ".", 3) {
		if i > 2 {
			break
		}
		n, _ := strconv.Atoi(strings.TrimRight(p, "xX*-abcdefghijklmnopqrstuvwyz"))
		out[i] = n
	}
	return out
}

func verLess(a, b string) bool {
	x, y := parseVer(a), parseVer(b)
	for i := 0; i < 3; i++ {
		if x[i] != y[i] {
			return x[i] < y[i]
		}
	}
	return false
}

func cmpParts(v [3]int, w [3]int) int {
	for i := 0; i < 3; i++ {
		if v[i] != w[i] {
			if v[i] < w[i] {
				return -1
			}
			return 1
		}
	}
	return 0
}

// satisfies handles "", ">=22", ">18 <21", "^22", "~22.1", "22", "22.x", "18 || 20".
func satisfies(version, rng string) bool {
	rng = strings.TrimSpace(rng)
	if rng == "" || rng == "*" {
		return true
	}
	v := parseVer(version)
	for _, alt := range strings.Split(rng, "||") {
		ok := true
		for _, c := range strings.Fields(alt) {
			if !satisfiesOne(v, c) {
				ok = false
				break
			}
		}
		if ok && strings.TrimSpace(alt) != "" {
			return true
		}
	}
	return false
}

func satisfiesOne(v [3]int, c string) bool {
	for _, op := range []string{">=", "<=", ">", "<", "^", "~", "="} {
		if strings.HasPrefix(c, op) {
			w := parseVer(c[len(op):])
			parts := len(strings.Split(strings.TrimPrefix(c[len(op):], "v"), "."))
			switch op {
			case ">=":
				return cmpParts(v, w) >= 0
			case "<=":
				return cmpParts(v, w) <= 0
			case ">":
				return cmpParts(v, w) > 0
			case "<":
				return cmpParts(v, w) < 0
			case "^":
				return v[0] == w[0] && cmpParts(v, w) >= 0
			case "~":
				if parts >= 2 {
					return v[0] == w[0] && v[1] == w[1] && cmpParts(v, w) >= 0
				}
				return v[0] == w[0]
			case "=":
				return cmpParts(v, w) == 0
			}
		}
	}
	// bare: 22 / 22.x / 22.1 / 22.1.3
	parts := strings.Split(strings.TrimPrefix(c, "v"), ".")
	w := parseVer(c)
	for i, p := range parts {
		if i > 2 || p == "x" || p == "X" || p == "*" {
			break
		}
		if v[i] != w[i] {
			return false
		}
	}
	return true
}
