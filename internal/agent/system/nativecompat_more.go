package system

// Compatibility checks for Java, Go and Python projects (R3): the runtime version, default
// build and start commands, the health path, and what may go wrong on this host.

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
)

func newReport(kind string) NodeReport {
	return NodeReport{Kind: kind, Health: "/", Findings: []Finding{}, Shared: []string{"data"}}
}

var (
	pomJavaRe    = regexp.MustCompile(`<(?:java\.version|maven\.compiler\.release|maven\.compiler\.target|release)>\s*(?:1\.)?(\d+)\s*<`)
	gradleJavaRe = regexp.MustCompile(`(?:languageVersion\s*=\s*JavaLanguageVersion\.of\(\s*|sourceCompatibility\s*=\s*['"]?(?:JavaVersion\.VERSION_)?(?:1_)?)(\d+)`)
)

// AnalyzeJava: files are pom.xml / build.gradle(.kts) contents; have says which other files exist
// (mvnw, gradlew, and jars at the top level).
func AnalyzeJava(files map[string][]byte, have map[string]bool, freeMB int) NodeReport {
	r := newReport("java")
	r.Version = "21"
	pom, gradle := files["pom.xml"], files["build.gradle"]
	if gradle == nil {
		gradle = files["build.gradle.kts"]
	}
	text := string(pom) + string(gradle)
	switch {
	case pom != nil:
		if m := pomJavaRe.FindSubmatch(pom); m != nil {
			r.Version = string(m[1])
		}
		if have["mvnw"] {
			r.Build = "./mvnw -q -B -DskipTests package"
		} else {
			r.Build = "mvn -q -B -DskipTests package"
			r.Tools = append(r.Tools, "maven@3")
		}
		r.Start = "java -jar $(ls target/*.jar | grep -v -e '-plain' -e '-sources' | head -1)"
	case gradle != nil:
		if m := gradleJavaRe.FindSubmatch(gradle); m != nil {
			r.Version = string(m[1])
		}
		task := "build -x test"
		if strings.Contains(text, "org.springframework.boot") {
			task = "bootJar"
		}
		if have["gradlew"] {
			r.Build = "./gradlew -q " + task
		} else {
			r.Build = "gradle -q " + task
			r.Tools = append(r.Tools, "gradle@8")
		}
		r.Start = "java -jar $(ls build/libs/*.jar | grep -v -e '-plain' -e '-sources' | head -1)"
	default:
		var jars []string
		for f := range have {
			if strings.HasSuffix(f, ".jar") {
				jars = append(jars, f)
			}
		}
		sort.Strings(jars)
		if len(jars) == 0 {
			addR(&r, "block", "java_no_build")
			return r
		}
		r.Start = "java -jar " + jars[0]
	}
	if strings.Contains(text, "spring-boot") || strings.Contains(text, "org.springframework.boot") {
		r.Framework = "spring"
		if strings.Contains(text, "spring-boot-starter-actuator") {
			r.Health = "/actuator/health"
		}
	}
	addR(&r, "ok", "java_version", "version", r.Version)
	if r.Build != "" && freeMB > 0 && freeMB < 1500 {
		addR(&r, "warn", "build_memory", "need", "1500", "free", itoa(freeMB))
	}
	return r
}

var (
	goVerRe       = regexp.MustCompile(`(?m)^go\s+(\d+\.\d+)`)
	goToolchainRe = regexp.MustCompile(`(?m)^toolchain\s+go(\d+\.\d+)`)
)

// AnalyzeGo: rootMain says the top folder holds package main; cmds lists cmd/<name> folders with one.
func AnalyzeGo(goMod []byte, rootMain bool, cmds []string) NodeReport {
	r := newReport("go")
	r.Version = "1.24"
	if m := goVerRe.FindSubmatch(goMod); m != nil {
		r.Version = string(m[1])
	}
	if m := goToolchainRe.FindSubmatch(goMod); m != nil {
		r.Version = string(m[1])
	}
	pkg := ""
	switch {
	case rootMain:
		pkg = "."
	case len(cmds) > 0:
		sort.Strings(cmds)
		pkg = "./cmd/" + cmds[0]
		if len(cmds) > 1 {
			addR(&r, "warn", "go_many_mains", "list", strings.Join(cmds, ", "), "used", cmds[0])
		}
	default:
		addR(&r, "block", "go_no_main")
		return r
	}
	r.Install = "go mod download"
	r.Build = "go build -o .berth/app " + pkg
	r.Start = "./.berth/app"
	addR(&r, "ok", "go_version", "version", r.Version)
	return r
}

var (
	pyRequiresRe = regexp.MustCompile(`requires-python\s*=\s*["']([^"']+)["']`)
	djangoSetRe  = regexp.MustCompile(`DJANGO_SETTINGS_MODULE['"]\s*,\s*['"]([A-Za-z0-9_]+)\.settings`)
	pyAppRe      = regexp.MustCompile(`(?m)^(\w+)\s*=\s*(FastAPI|Flask)\(`)
)

// packages that compile C code when no wheel fits (the hint: use their binary sibling)
var pyNativeBuild = map[string]string{"psycopg2": "psycopg2-binary", "mysqlclient": "PyMySQL", "pycairo": "", "pygraphviz": ""}

// AnalyzePython: files holds requirements.txt, pyproject.toml, .python-version, manage.py, and the
// app modules (app.py, main.py, wsgi.py …) by name.
func AnalyzePython(files map[string][]byte, installed []string, have map[string]bool, freeMB int) NodeReport {
	r := newReport("python")
	req, pyproj := string(files["requirements.txt"]), string(files["pyproject.toml"])
	deps := strings.ToLower(req + "\n" + pyproj)
	want := ""
	if m := pyRequiresRe.FindStringSubmatch(pyproj); m != nil {
		want = m[1]
	}
	if v := strings.TrimSpace(string(files[".python-version"])); v != "" {
		want = v
	}
	sort.Slice(installed, func(i, j int) bool { return verLess(installed[j], installed[i]) })
	for _, v := range installed {
		if satisfies(v, want) && (want == "" || !strings.Contains(want, ".") || strings.Contains(want, ">") || strings.HasPrefix(v, want)) {
			r.Version = v
			break
		}
	}
	if r.Version == "" {
		for _, v := range []string{"3.13", "3.12", "3.11", "3.10"} {
			if satisfies(v+".0", want) || strings.HasPrefix(want, v) {
				r.Version = v
				break
			}
		}
	}
	if r.Version == "" {
		addR(&r, "block", "py_version", "range", want)
		return r
	}
	if want == "" {
		addR(&r, "ok", "py_version_any", "version", r.Version)
	} else {
		addR(&r, "ok", "py_version", "version", r.Version, "range", want)
	}

	has := func(name string) bool {
		return regexp.MustCompile(`(?m)(^|["'\s,\[])` + regexp.QuoteMeta(name) + `([=<>~!\[;\s"',]|$)`).MatchString(deps)
	}
	install := "python -m venv .venv && .venv/bin/pip install -q --upgrade pip"
	switch {
	case req != "":
		install += " && .venv/bin/pip install -q -r requirements.txt"
	case pyproj != "":
		install += " && .venv/bin/pip install -q ."
	}
	server := ""
	switch {
	case files["manage.py"] != nil || has("django"):
		r.Framework = "django"
		proj := "config"
		if m := djangoSetRe.FindSubmatch(files["manage.py"]); m != nil {
			proj = string(m[1])
		}
		r.Start = "gunicorn " + proj + ".wsgi --bind 0.0.0.0:$PORT --workers 2"
		r.Release = "python manage.py migrate --noinput"
		server = "gunicorn"
		r.Shared = []string{"data", "media"}
	default:
		mod, kind, varName := "", "", ""
		names := make([]string, 0, len(files))
		for n := range files {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			if !strings.HasSuffix(n, ".py") || n == "manage.py" {
				continue
			}
			if m := pyAppRe.FindSubmatch(files[n]); m != nil {
				mod, varName, kind = strings.TrimSuffix(n, ".py"), string(m[1]), string(m[2])
				break
			}
		}
		switch {
		case kind == "FastAPI" || (mod == "" && has("fastapi")):
			r.Framework = "fastapi"
			if mod == "" {
				mod, varName = "main", "app"
			}
			r.Start = "uvicorn " + mod + ":" + varName + " --host 0.0.0.0 --port $PORT"
			server = "uvicorn"
		case kind == "Flask" || (mod == "" && has("flask")):
			r.Framework = "flask"
			if mod == "" {
				mod, varName = "app", "app"
			}
			r.Start = "gunicorn " + mod + ":" + varName + " --bind 0.0.0.0:$PORT --workers 2"
			server = "gunicorn"
		default:
			r.Start = "python main.py"
			addR(&r, "warn", "py_no_app")
		}
	}
	if server != "" && !has(server) {
		install += " && .venv/bin/pip install -q " + server
		addR(&r, "ok", "py_server_added", "name", server)
	}
	r.Install = install
	for name, alt := range pyNativeBuild {
		if has(name) {
			if !have["gcc"] {
				f := Finding{Level: "warn", Code: "py_native_build", Params: map[string]string{"name": name}}
				if alt != "" {
					f.Params["alt"] = alt
				}
				r.Findings = append(r.Findings, f)
			}
		}
	}
	if freeMB > 0 && freeMB < 500 {
		addR(&r, "warn", "build_memory", "need", "500", "free", itoa(freeMB))
	}
	return r
}

func addR(r *NodeReport, level, code string, kv ...string) { add(r, level, code, kv...) }

func itoa(n int) string { return strconv.Itoa(n) }
