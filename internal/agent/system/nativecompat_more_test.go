package system

import (
	"strings"
	"testing"
)

func TestAnalyzeJavaMavenSpring(t *testing.T) {
	pom := []byte(`<project><parent><artifactId>spring-boot-starter-parent</artifactId></parent><properties><java.version>21</java.version></properties>
<dependencies><dependency><artifactId>spring-boot-starter-web</artifactId></dependency><dependency><artifactId>spring-boot-starter-actuator</artifactId></dependency></dependencies></project>`)
	r := AnalyzeJava(map[string][]byte{"pom.xml": pom}, map[string]bool{"mvnw": true}, 4000)
	if r.Kind != "java" || r.Version != "21" || r.Framework != "spring" || r.Health != "/actuator/health" {
		t.Fatalf("%+v", r)
	}
	if !strings.HasPrefix(r.Build, "./mvnw") || !strings.Contains(r.Start, "java -jar") || !strings.Contains(r.Start, "target/") {
		t.Fatalf("commands %+v", r)
	}
	if len(r.Tools) != 0 {
		t.Fatalf("mvnw present, no maven needed: %v", r.Tools)
	}
}

func TestAnalyzeJavaGradleNoWrapper(t *testing.T) {
	g := []byte(`plugins { id 'org.springframework.boot' version '3.5.0' }
java { toolchain { languageVersion = JavaLanguageVersion.of(17) } }`)
	r := AnalyzeJava(map[string][]byte{"build.gradle": g}, nil, 4000)
	if r.Version != "17" || !strings.Contains(r.Build, "gradle") || !contains(r.Tools, "gradle@8") || !strings.Contains(r.Start, "build/libs") {
		t.Fatalf("%+v", r)
	}
	if r.Health != "/" {
		t.Fatal("no actuator: health /")
	}
}

func TestAnalyzeJavaJarOnly(t *testing.T) {
	r := AnalyzeJava(nil, map[string]bool{"app.jar": true}, 4000)
	if r.Build != "" || !strings.Contains(r.Start, "app.jar") || r.Version != "21" {
		t.Fatalf("%+v", r)
	}
}

func TestAnalyzeGo(t *testing.T) {
	mod := []byte("module example.com/svc\n\ngo 1.23.0\n\ntoolchain go1.24.2\n")
	r := AnalyzeGo(mod, true, nil)
	if r.Kind != "go" || r.Version != "1.24" || r.Build != "go build -o .berth/app ." || r.Start != "./.berth/app" {
		t.Fatalf("%+v", r)
	}
	r = AnalyzeGo([]byte("module x\n\ngo 1.22\n"), false, []string{"api", "worker"})
	if r.Version != "1.22" || r.Build != "go build -o .berth/app ./cmd/api" {
		t.Fatalf("%+v", r)
	}
	r = AnalyzeGo([]byte("module x\n\ngo 1.22\n"), false, nil)
	if !strings.Contains(codesAny(r), "go_no_main") {
		t.Fatalf("%+v", r.Findings)
	}
}

func codesAny(r NodeReport) string {
	var out []string
	for _, f := range r.Findings {
		out = append(out, f.Level+":"+f.Code)
	}
	return strings.Join(out, ",")
}

func TestAnalyzePythonDjango(t *testing.T) {
	files := map[string][]byte{"requirements.txt": []byte("Django==5.1\npsycopg2==2.9.9\n"), "manage.py": []byte("os.environ.setdefault('DJANGO_SETTINGS_MODULE', 'mysite.settings')")}
	r := AnalyzePython(files, []string{"3.12.16"}, map[string]bool{"gcc": false}, 4000)
	if r.Kind != "python" || r.Framework != "django" || r.Version != "3.12.16" {
		t.Fatalf("%+v", r)
	}
	if !strings.Contains(r.Start, "gunicorn mysite.wsgi") || !strings.Contains(r.Install, "gunicorn") || !strings.Contains(r.Release, "migrate") {
		t.Fatalf("commands %+v", r)
	}
	if !strings.Contains(codesAny(r), "warn:py_native_build") {
		t.Fatalf("psycopg2 needs a compiler: %v", codesAny(r))
	}
}

func TestAnalyzePythonFastAPI(t *testing.T) {
	files := map[string][]byte{"pyproject.toml": []byte("[project]\nrequires-python = \">=3.11\"\ndependencies = [\"fastapi\", \"uvicorn\"]\n"), "main.py": []byte("from fastapi import FastAPI\napp = FastAPI()\n")}
	r := AnalyzePython(files, nil, nil, 4000)
	if r.Framework != "fastapi" || r.Version != "3.13" || !strings.Contains(r.Start, "uvicorn main:app") || strings.Contains(r.Install, "pip install -q uvicorn") {
		t.Fatalf("%+v", r)
	}
	if !strings.Contains(r.Install, "pip install -q .") {
		t.Fatalf("pyproject: pip install . %q", r.Install)
	}
}

func TestAnalyzePythonFlask(t *testing.T) {
	files := map[string][]byte{"requirements.txt": []byte("flask\n"), "app.py": []byte("from flask import Flask\napp = Flask(__name__)\n")}
	r := AnalyzePython(files, nil, nil, 4000)
	if r.Framework != "flask" || !strings.Contains(r.Start, "gunicorn app:app") || !strings.Contains(r.Install, "gunicorn") {
		t.Fatalf("%+v", r)
	}
}

func TestUnitTextPerLanguage(t *testing.T) {
	j := NativeSpec{Name: "api", Kind: "java", Version: "21", Start: "java -jar app.jar"}.unitText("/opt/berth/mise/installs/java/temurin-21")
	for _, w := range []string{"Environment=JAVA_HOME=/opt/berth/mise/installs/java/temurin-21\n", "Environment=SERVER_PORT=%i\n", "MaxRAMPercentage=75.0 -Duser.home=/srv/berth/projects/api/shared/.home\n", "Environment=PATH=/opt/berth/mise/installs/java/temurin-21/bin:"} {
		if !strings.Contains(j, w) {
			t.Errorf("java unit lacks %q:\n%s", w, j)
		}
	}
	if strings.Contains(j, "NODE_ENV") {
		t.Error("java unit has NODE_ENV")
	}
	p := NativeSpec{Name: "dj", Kind: "python", Version: "3.12", Start: "gunicorn x.wsgi"}.unitText("/py")
	for _, w := range []string{"Environment=PATH=/srv/berth/projects/dj/run-%i/.venv/bin:/py/bin:", "Environment=VIRTUAL_ENV=/srv/berth/projects/dj/run-%i/.venv\n"} {
		if !strings.Contains(p, w) {
			t.Errorf("python unit lacks %q:\n%s", w, p)
		}
	}
	e := buildEnv(NativeSpec{Name: "dj", Kind: "python"}, "/py", "/rel", []string{"/mvn"}, map[string]string{"K": "v"})
	if e[0] != "PATH=/mvn/bin:/rel/.venv/bin:/py/bin:/usr/local/bin:/usr/bin:/bin" {
		t.Error(e[0])
	}
	if miseTool("java", "21") != "java@temurin-21" || miseTool("java", "zulu-17") != "java@zulu-17" || miseTool("go", "1.24") != "go@1.24" {
		t.Error("miseTool")
	}
}

func TestJavaBuildHome(t *testing.T) {
	e := strings.Join(buildEnv(NativeSpec{Name: "api", Kind: "java"}, "/jdk", "/srv/berth/projects/api/releases/20261114-000000", nil, nil), "\n")
	for _, w := range []string{"JAVA_TOOL_OPTIONS=-Duser.home=/srv/berth/projects/api/shared/.home", "MAVEN_OPTS=-Dmaven.repo.local=/srv/berth/projects/api/shared/.home/.m2/repository", "JAVA_HOME=/jdk"} {
		if !strings.Contains(e, w) {
			t.Errorf("build env lacks %q:\n%s", w, e)
		}
	}
	if strings.Contains(e, "SERVER_PORT") {
		t.Error("SERVER_PORT in build env")
	}
}
