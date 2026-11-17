package system

// System packages (软件包): search the distribution's repositories, see what was installed
// by hand, and install or remove packages through dnf or apt. Every change is first run as
// a dry run so the preview lists exactly what comes and goes, dependencies included.

import (
	"context"
	"errors"
	"regexp"
	"sort"
	"strings"

	"github.com/wangsirco/berth/internal/errs"
)

type Pkg struct {
	Name      string `json:"name"`
	Version   string `json:"version"`
	Summary   string `json:"summary,omitempty"`
	Installed string `json:"installed,omitempty"` // installed version, if any
}

// PkgStep is one line of a dry run: what happens to which package.
type PkgStep struct {
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
	Action  string `json:"action"` // install | dep | weak | upgrade | remove | unused
	Size    string `json:"size,omitempty"`
}

type PkgDryRun struct {
	Steps []PkgStep `json:"steps"`
	Note  string    `json:"note,omitempty"` // "After this operation, 6 MiB extra will be used"
}

var pkgNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]{0,99}$`)

// Removing these would take the server, its login or Berth itself down.
var pkgProtected = regexp.MustCompile(`^(kernel(-16k)?(-core|-modules.*)?|linux-image-.*|systemd(-libs|-udev)?|glibc|libc6|openssh-server|openssh|sudo|dnf5?|rpm|python3-dnf|apt|dpkg|bash|coreutils|util-linux|selinux-policy.*|nftables|NetworkManager|docker-ce|moby-engine|containerd(\.io)?|incus|berth.*|shadow-utils|passwd|pam|grub2.*|asahi-.*|filesystem)$`)

func validPkgs(names []string) error {
	if len(names) == 0 || len(names) > 60 {
		return errs.New(400, "pkg.count", "", "")
	}
	for _, n := range names {
		if !pkgNameRe.MatchString(n) {
			return errs.New(400, "pkg.bad_name", "name", n)
		}
	}
	return nil
}

func installedSet(ctx context.Context) map[string]string {
	m := map[string]string{}
	var out string
	if has("rpm") && has("dnf") {
		out, _ = run(ctx, "rpm", "-qa", "--qf", "%{NAME}|%{VERSION}-%{RELEASE}\n")
	} else if has("dpkg-query") {
		out, _ = run(ctx, "dpkg-query", "-W", "-f", "${db:Status-Abbrev}|${Package}|${Version}\n")
		var b strings.Builder
		for _, l := range strings.Split(out, "\n") {
			if f := strings.SplitN(l, "|", 3); len(f) == 3 && strings.HasPrefix(f[0], "ii") {
				b.WriteString(f[1] + "|" + f[2] + "\n")
			}
		}
		out = b.String()
	}
	for _, l := range strings.Split(out, "\n") {
		if f := strings.SplitN(l, "|", 2); len(f) == 2 {
			m[f[0]] = f[1]
		}
	}
	return m
}

// rank: exact name, then prefix, then the rest; -devel/-debuginfo and language packs last.
func pkgRank(name, q string) int {
	r := 3
	switch {
	case name == q:
		r = 0
	case strings.HasPrefix(name, q):
		r = 1
	case strings.Contains(name, q):
		r = 2
	}
	if strings.HasSuffix(name, "-devel") || strings.HasSuffix(name, "-dev") || strings.Contains(name, "-debug") || strings.Contains(name, "-doc") || strings.HasPrefix(name, "mingw") {
		r += 4
	}
	return r
}

func sortPkgs(list []Pkg, q string) []Pkg {
	sort.SliceStable(list, func(i, j int) bool {
		ri, rj := pkgRank(list[i].Name, q), pkgRank(list[j].Name, q)
		if ri != rj {
			return ri < rj
		}
		if len(list[i].Name) != len(list[j].Name) {
			return len(list[i].Name) < len(list[j].Name)
		}
		return list[i].Name < list[j].Name
	})
	if len(list) > 100 {
		list = list[:100]
	}
	return list
}

// SearchPackages finds packages whose name contains q (newest version per name).
func SearchPackages(ctx context.Context, q string) ([]Pkg, error) {
	q = strings.ToLower(strings.TrimSpace(q))
	if len(q) < 2 || !pkgNameRe.MatchString(q) {
		return []Pkg{}, nil
	}
	inst := installedSet(ctx)
	list := []Pkg{}
	seen := map[string]bool{}
	switch {
	case has("dnf"):
		out, err := run(ctx, "dnf", "-q", "repoquery", "--latest-limit", "1", "--qf", "%{name}|%{evr}|%{summary}\n", "*"+q+"*")
		if err != nil {
			return nil, errors.New(lastLine(out))
		}
		for _, l := range strings.Split(out, "\n") {
			f := strings.SplitN(l, "|", 3)
			if len(f) != 3 || seen[f[0]] {
				continue
			}
			seen[f[0]] = true
			v := f[1]
			if i := strings.Index(v, ":"); i >= 0 && i < 4 { // drop "0:" epochs
				v = v[i+1:]
			}
			list = append(list, Pkg{Name: f[0], Version: v, Summary: f[2], Installed: inst[f[0]]})
		}
	case has("apt-cache"):
		out, _ := run(ctx, "apt-cache", "search", "--names-only", q)
		for _, l := range strings.Split(out, "\n") {
			name, sum, ok := strings.Cut(l, " - ")
			if !ok || seen[name] {
				continue
			}
			seen[name] = true
			list = append(list, Pkg{Name: name, Summary: sum, Installed: inst[name]})
		}
	default:
		return nil, errors.New("no dnf or apt")
	}
	return sortPkgs(list, q), nil
}

// UserPackages lists what was installed on purpose (not as a dependency or with the OS image).
func UserPackages(ctx context.Context) ([]Pkg, error) {
	list := []Pkg{}
	switch {
	case has("dnf"):
		out, err := run(ctx, "dnf", "-q", "repoquery", "--userinstalled", "--qf", "%{name}|%{evr}|%{summary}\n")
		if err != nil {
			return nil, errors.New(lastLine(out))
		}
		for _, l := range strings.Split(out, "\n") {
			if f := strings.SplitN(l, "|", 3); len(f) == 3 {
				v := f[1]
				if i := strings.Index(v, ":"); i >= 0 && i < 4 {
					v = v[i+1:]
				}
				list = append(list, Pkg{Name: f[0], Version: v, Summary: f[2], Installed: v})
			}
		}
	case has("apt-mark"):
		out, _ := run(ctx, "apt-mark", "showmanual")
		inst := installedSet(ctx)
		for _, n := range strings.Fields(out) {
			list = append(list, Pkg{Name: n, Version: inst[n], Installed: inst[n]})
		}
	default:
		return nil, errors.New("no dnf or apt")
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })
	return list, nil
}

var dnfSection = map[string]string{
	"Installing:": "install", "Installing dependencies:": "dep", "Installing weak dependencies:": "weak",
	"Upgrading:": "upgrade", "Reinstalling:": "upgrade", "Removing:": "remove", "Removing unused dependencies:": "unused",
	"Removing dependent packages:": "remove", "Downgrading:": "upgrade",
}

// parseDnfDry reads `dnf install|remove --assumeno` output.
func parseDnfDry(out string) PkgDryRun {
	d := PkgDryRun{Steps: []PkgStep{}}
	sec := ""
	for _, l := range strings.Split(out, "\n") {
		t := strings.TrimSpace(l)
		if a, ok := dnfSection[t]; ok {
			sec = a
			continue
		}
		if strings.HasPrefix(t, "After this operation") {
			d.Note = t
		}
		if !strings.HasPrefix(l, " ") { // a new heading we do not list ("Transaction Summary:")
			sec = ""
			continue
		}
		if sec == "" {
			continue
		}
		f := strings.Fields(t) // name arch epoch:version repo size unit
		if len(f) < 4 {
			continue
		}
		v := f[2]
		if i := strings.Index(v, ":"); i >= 0 && i < 4 {
			v = v[i+1:]
		}
		s := PkgStep{Name: f[0], Version: v, Action: sec}
		if len(f) >= 6 {
			s.Size = f[len(f)-2] + " " + f[len(f)-1]
		}
		d.Steps = append(d.Steps, s)
	}
	return d
}

var aptLineRe = regexp.MustCompile(`^(Inst|Remv) (\S+)(?: \[[^\]]*\])?(?: \((\S+))?`)

func parseAptDry(out string, names []string) PkgDryRun {
	d := PkgDryRun{Steps: []PkgStep{}}
	want := map[string]bool{}
	for _, n := range names {
		want[n] = true
	}
	for _, l := range strings.Split(out, "\n") {
		m := aptLineRe.FindStringSubmatch(l)
		if m == nil {
			continue
		}
		a := map[string]string{"Inst": "dep", "Remv": "unused"}[m[1]]
		if want[m[2]] {
			a = map[string]string{"Inst": "install", "Remv": "remove"}[m[1]]
		}
		d.Steps = append(d.Steps, PkgStep{Name: m[2], Version: m[3], Action: a})
	}
	return d
}

// DryRunPackages shows what installing or removing these packages would do, without doing it.
func DryRunPackages(ctx context.Context, names []string, remove bool) (PkgDryRun, error) {
	if err := validPkgs(names); err != nil {
		return PkgDryRun{}, err
	}
	var d PkgDryRun
	switch {
	case has("dnf"):
		verb := map[bool]string{true: "remove", false: "install"}[remove]
		out, _ := run(ctx, "dnf", append([]string{verb, "--assumeno"}, names...)...)
		if strings.Contains(out, "No match for argument") || strings.Contains(out, "No packages to remove") {
			return d, errs.New(404, "pkg.not_found", "detail", firstMatch(out, "No match for argument", "No packages to remove"))
		}
		d = parseDnfDry(out)
		if len(d.Steps) == 0 && strings.Contains(out, "Nothing to do") {
			d.Note = "Nothing to do"
		} else if len(d.Steps) == 0 {
			return d, errors.New(lastLine(out))
		}
	case has("apt-get"):
		verb := map[bool]string{true: "remove", false: "install"}[remove]
		out, err := run(ctx, "apt-get", append([]string{"-s", verb}, names...)...)
		if err != nil {
			return d, errs.New(404, "pkg.not_found", "detail", lastLine(out))
		}
		d = parseAptDry(out, names)
	default:
		return d, errors.New("no dnf or apt")
	}
	if remove {
		for _, s := range d.Steps {
			if pkgProtected.MatchString(s.Name) {
				return d, errs.New(409, "pkg.protected", "name", s.Name)
			}
		}
	}
	return d, nil
}

func firstMatch(out string, subs ...string) string {
	for _, l := range strings.Split(out, "\n") {
		for _, s := range subs {
			if strings.Contains(l, s) {
				return strings.TrimSpace(l)
			}
		}
	}
	return lastLine(out)
}

// ApplyPackages installs or removes; it re-checks the protected list against a fresh dry run.
func ApplyPackages(ctx context.Context, names []string, remove bool, log func(string)) (PkgDryRun, error) {
	d, err := DryRunPackages(ctx, names, remove)
	if err != nil {
		return d, err
	}
	var name string
	var args []string
	verb := map[bool]string{true: "remove", false: "install"}[remove]
	switch {
	case has("dnf"):
		name, args = "dnf", append([]string{"-y", verb}, names...)
	case has("apt-get"):
		name, args = "env", append([]string{"DEBIAN_FRONTEND=noninteractive", "apt-get", "-y", "-o", "Dpkg::Options::=--force-confold", verb}, names...)
	}
	if !remove {
		defer TempSwap(ctx, log)()
	}
	log("$ " + name + " " + strings.Join(args, " "))
	out, err := run(ctx, name, args...)
	n := 0
	for _, l := range strings.Split(out, "\n") {
		if l = strings.TrimSpace(l); l != "" && !strings.Contains(l, "% |") && n < 300 {
			log(l)
			n++
		}
	}
	if err != nil {
		if k := Killed(err); k != err {
			return d, errs.New(500, "pkg.failed", "detail", k.Error())
		}
		return d, errs.New(500, "pkg.failed", "detail", lastLine(out))
	}
	updMu.Lock()
	updCache = Updates{}
	updMu.Unlock()
	return d, nil
}
