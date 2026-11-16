// apptest installs every catalog app on this host (a scratch copy, test ports), waits for
// its health check, looks for 16K-page crashes in the logs, records the result and
// cleans up. Run it as root on an Asahi (16K page) host:
//
//	GOOS=linux GOARCH=arm64 go build -o apptest ./scripts/apptest && scp apptest host: && ssh host ./apptest -out results.json
//
// Then on the dev machine: python3 scripts/apps/apply_results.py results.json
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/wangsirco/berth/internal/appstore"
)

type result struct {
	ID      string `json:"id"`
	Result  string `json:"result"` // ok | crash (16K pages) | fail (anything else)
	Krun    string `json:"krun,omitempty"`
	Note    string `json:"note,omitempty"`
	Seconds int    `json:"seconds"`
	At      string `json:"at"`
}

var pageCrash = regexp.MustCompile(`(?i)(unsupported system page size|<jemalloc>.*page|page ?size.*(not supported|unsupported|mismatch)|failed to map segment from shared object)`)

func sh(dir string, args ...string) (string, error) {
	c := exec.Command(args[0], args[1:]...)
	c.Dir = dir
	c.Env = append(os.Environ(), "COMPOSE_ANSI=never", "COMPOSE_PROGRESS=plain")
	out, err := c.CombinedOutput()
	return string(out), err
}

func healthy(url string, wait time.Duration) bool {
	hc := &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	end := time.Now().Add(wait)
	for time.Now().Before(end) {
		if r, err := hc.Get(url); err == nil {
			r.Body.Close()
			if r.StatusCode < 500 {
				return true
			}
		}
		time.Sleep(3 * time.Second)
	}
	return false
}

// running: every container of the project up and not restarting for a while.
func running(dir, project string) bool {
	for i := 0; i < 4; i++ {
		out, _ := sh(dir, "docker", "compose", "-p", project, "ps", "-a", "--format", "{{.State}} {{.Status}}")
		lines := strings.Split(strings.TrimSpace(out), "\n")
		if len(lines) == 0 || lines[0] == "" {
			return false
		}
		for _, l := range lines {
			if !strings.HasPrefix(l, "running") || strings.Contains(l, "Restarting") {
				return false
			}
		}
		time.Sleep(5 * time.Second)
	}
	return true
}

func testOne(a appstore.App, base int, runtime string) result {
	start := time.Now()
	res := result{ID: a.ID, At: time.Now().Format("2006-01-02")}
	project := "apptest-" + a.ID
	dir := filepath.Join("/tmp/berth-apptest", a.ID)
	os.RemoveAll(dir)
	os.MkdirAll(dir, 0o755)
	in := map[string]string{}
	next := base
	for _, f := range a.Fields {
		switch {
		case f.Type == "port":
			in[f.Key] = strconv.Itoa(next)
			next++
		case f.Required && f.Default == "":
			in[f.Key] = "apptest.lan"
		}
	}
	vals, err := a.Values(in)
	if err != nil {
		res.Result, res.Note = "fail", err.Error()
		return res
	}
	compose := a.Compose
	if runtime != "" {
		compose = strings.ReplaceAll(compose, "    image: ", "    runtime: "+runtime+"\n    image: ")
	}
	os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte(compose), 0o644)
	os.WriteFile(filepath.Join(dir, ".env"), []byte(a.Env(project, filepath.Join(dir, "data"), vals)), 0o600)
	if a.DataOwner != "" { // what the panel's install does (docker.PrepareData)
		u, g, _ := strings.Cut(a.DataOwner, ":")
		uid, _ := strconv.Atoi(u)
		gid, _ := strconv.Atoi(g)
		for _, d := range a.DataDirs() {
			p := filepath.Join(dir, "data", d)
			os.MkdirAll(p, 0o750)
			for q := p; q != filepath.Join(dir, "data"); q = filepath.Dir(q) {
				os.Lchown(q, uid, gid)
			}
		}
	}
	defer func() {
		sh(dir, "docker", "compose", "-p", project, "down", "-v", "--remove-orphans")
		os.RemoveAll(dir)
	}()
	if out, err := sh(dir, "docker", "compose", "-p", project, "up", "-d", "--quiet-pull"); err != nil {
		res.Result, res.Note = "fail", lastLine(out)
		res.Seconds = int(time.Since(start).Seconds())
		return res
	}
	ok := false
	if a.Health.Port > 0 {
		port := ""
		for _, p := range a.Ports {
			if p.Port == a.Health.Port {
				port = vals[p.Field]
			}
		}
		ok = port != "" && healthy("http://127.0.0.1:"+port+a.Health.Path, 4*time.Minute)
	}
	if a.Health.Port == 0 || !ok {
		ok = running(dir, project) && (a.Health.Port == 0 || ok)
	}
	logs, _ := sh(dir, "docker", "compose", "-p", project, "logs", "--no-color", "--tail", "300")
	switch {
	case pageCrash.MatchString(logs):
		res.Result, res.Note = "crash", pageCrash.FindString(logs)
	case ok:
		res.Result = "ok"
	default:
		res.Result, res.Note = "fail", lastLine(logs)
	}
	res.Seconds = int(time.Since(start).Seconds())
	return res
}

// images: the IDs of every image on the host.
func images() map[string]bool {
	out, _ := exec.Command("docker", "images", "-q", "--no-trunc").Output()
	m := map[string]bool{}
	for _, l := range strings.Fields(string(out)) {
		m[l] = true
	}
	return m
}

func lastLine(s string) string {
	l := strings.Split(strings.TrimSpace(s), "\n")
	x := l[len(l)-1]
	if len(x) > 200 {
		x = x[:200]
	}
	return x
}

func main() {
	out := flag.String("out", "apptest-results.json", "results file")
	only := flag.String("only", "", "comma-separated app ids")
	prune := flag.Bool("prune", true, "remove the images each test pulled (images that were already here stay)")
	flag.Parse()
	list, err := appstore.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	want := map[string]bool{}
	for _, id := range strings.Split(*only, ",") {
		if id != "" {
			want[id] = true
		}
	}
	var results []result
	for i, a := range list {
		if len(want) > 0 && !want[a.ID] {
			continue
		}
		before := images()
		r := testOne(a, 41000+i*10, "")
		if r.Result == "crash" { // does the 4K compatibility mode fix it?
			if k := testOne(a, 41000+i*10, "krun"); k.Result == "ok" {
				r.Krun = "ok"
			} else {
				r.Krun = k.Result + ": " + k.Note
			}
		}
		fmt.Printf("%-18s %-5s %4ds %s %s\n", a.ID, r.Result, r.Seconds, r.Krun, r.Note)
		results = append(results, r)
		b, _ := json.MarshalIndent(results, "", "  ")
		os.WriteFile(*out, b, 0o644)
		if *prune {
			for id := range images() {
				if !before[id] {
					exec.Command("docker", "rmi", id).Run() // fails harmlessly if something uses it
				}
			}
		}
	}
}
