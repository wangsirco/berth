// Package appstore is the official app catalog (方案 §5.5): one directory per app with
// berth.yaml (metadata, form fields, ports, needs, health check), compose.yaml and an icon.
// The catalog ships inside the panel.
package appstore

import (
	"crypto/rand"
	"embed"
	"fmt"
	"io/fs"
	"math/big"
	"path"
	"regexp"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"
)

//go:embed apps
var embedded embed.FS

type L10n map[string]string

type Port struct {
	Name   string `yaml:"name" json:"name"`
	Port   int    `yaml:"port" json:"port"`               // inside the container
	Field  string `yaml:"field" json:"field"`             // the form field holding the host port
	HTTP   bool   `yaml:"http" json:"http"`               // a web UI (offered for a site)
	Proto  string `yaml:"proto" json:"proto"`             // tcp (default) | udp
	Public bool   `yaml:"public" json:"public,omitempty"` // meant to be reached from outside (offered for Wharf / firewall)
}

type Field struct {
	Key      string   `yaml:"key" json:"key"`
	Type     string   `yaml:"type" json:"type"` // string | password | number | bool | select | port
	Default  string   `yaml:"default" json:"default,omitempty"`
	Options  []string `yaml:"options" json:"options,omitempty"`
	Required bool     `yaml:"required" json:"required,omitempty"`
	Generate bool     `yaml:"generate" json:"generate,omitempty"` // passwords: generated when left empty
	Length   int      `yaml:"length" json:"length,omitempty"`     // generated length (default 24)
	Label    L10n     `yaml:"label" json:"label"`
	Hint     L10n     `yaml:"hint" json:"hint,omitempty"`
}

type Verified struct {
	Arm64_16K string `yaml:"arm64_16k" json:"arm64_16k"` // ok | crash | untested
	At        string `yaml:"at" json:"at,omitempty"`
	Note      string `yaml:"note" json:"note,omitempty"`
}

type App struct {
	ID       string   `yaml:"id" json:"id"`
	Name     L10n     `yaml:"name" json:"name"`
	Summary  L10n     `yaml:"summary" json:"summary"`
	Category string   `yaml:"category" json:"category"`
	Version  string   `yaml:"version" json:"version"`
	Website  string   `yaml:"website" json:"website,omitempty"`
	Arch     []string `yaml:"arch" json:"arch"`
	Verified Verified `yaml:"verified" json:"verified"`
	MemMB    int      `yaml:"mem_mb" json:"mem_mb"`
	DiskMB   int      `yaml:"disk_mb" json:"disk_mb"`
	Ports    []Port   `yaml:"ports" json:"ports"`
	Fields   []Field  `yaml:"fields" json:"fields"`
	Health   struct {
		Service string `yaml:"service" json:"service"`
		Port    int    `yaml:"port" json:"port"`
		Path    string `yaml:"path" json:"path"`
	} `yaml:"health" json:"health"`
	Site struct {
		Service string `yaml:"service" json:"service"`
		Port    int    `yaml:"port" json:"port"`
	} `yaml:"site" json:"site"`
	// DataOwner "uid:gid": the image runs as this user, so its data folders are created
	// for it before the first start (otherwise Docker makes them root-owned).
	DataOwner string `yaml:"data_owner" json:"-"`
	Compose   string `yaml:"-" json:"-"`
	Icon      string `yaml:"-" json:"icon,omitempty"` // SVG
}

var ownerRe = regexp.MustCompile(`^[0-9]{1,6}:[0-9]{1,6}$`)

// DataDirs lists the folders under ${BERTH_DATA} the compose file mounts.
func (a *App) DataDirs() []string {
	var out []string
	for _, m := range mountRe.FindAllStringSubmatch(a.Compose, -1) {
		if rest, ok := strings.CutPrefix(m[1], "${BERTH_DATA}"); ok {
			rest = strings.Trim(rest, "/")
			if rest != "" && !strings.Contains(rest, "..") && !contains(out, rest) {
				out = append(out, rest)
			}
		}
	}
	return out
}

var (
	idRe       = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,40}$`)
	keyRe      = regexp.MustCompile(`^[A-Z][A-Z0-9_]{1,40}$`)
	varRe      = regexp.MustCompile(`\$\{([A-Z][A-Z0-9_]*)(:?-[^}]*)?\}`)
	Categories = []string{"web", "database", "ai", "tools", "files", "media", "security", "dev", "productivity", "monitor", "network", "chat"}
)

// mountRe finds the source of "- source:target" volume lines.
var mountRe = regexp.MustCompile(`(?m)^\s*-\s*["']?([^:"'\s]+):/`)

// hostPaths apps may mount besides their data directory.
var hostPaths = []string{"/var/run/docker.sock", "/etc/localtime", "/etc/timezone", "/proc", "/sys", "/run/dbus", "/etc/os-release", "/dev/net/tun"}

// Builtin variables Berth puts in every .env.
var Builtin = []string{"BERTH_APP", "BERTH_DATA", "TZ", "PUID", "PGID"}

func load(fsys fs.FS) ([]App, error) {
	dirs, err := fs.ReadDir(fsys, "apps")
	if err != nil {
		return nil, err
	}
	var out []App
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		base := path.Join("apps", d.Name())
		b, err := fs.ReadFile(fsys, base+"/berth.yaml")
		if err != nil {
			return nil, fmt.Errorf("%s: %w", d.Name(), err)
		}
		var a App
		dec := yaml.NewDecoder(strings.NewReader(string(b)))
		dec.KnownFields(true)
		if err := dec.Decode(&a); err != nil {
			return nil, fmt.Errorf("%s/berth.yaml: %w", d.Name(), err)
		}
		c, err := fs.ReadFile(fsys, base+"/compose.yaml")
		if err != nil {
			return nil, fmt.Errorf("%s: %w", d.Name(), err)
		}
		a.Compose = string(c)
		if ic, err := fs.ReadFile(fsys, base+"/icon.svg"); err == nil {
			a.Icon = string(ic)
		}
		if a.ID != d.Name() {
			return nil, fmt.Errorf("%s: id %q does not match the directory", d.Name(), a.ID)
		}
		if err := a.Validate(); err != nil {
			return nil, fmt.Errorf("%s: %w", d.Name(), err)
		}
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// Load reads the built-in catalog.
func Load() ([]App, error) { return load(embedded) }

func (a *App) Validate() error {
	if !idRe.MatchString(a.ID) {
		return fmt.Errorf("bad id")
	}
	for _, l := range []string{"zh-CN", "zh-TW", "en"} {
		if a.Name[l] == "" || a.Summary[l] == "" {
			return fmt.Errorf("name/summary missing for %s", l)
		}
	}
	if !contains(Categories, a.Category) {
		return fmt.Errorf("unknown category %q", a.Category)
	}
	if len(a.Arch) == 0 {
		return fmt.Errorf("arch missing")
	}
	switch a.Verified.Arm64_16K {
	case "ok", "crash", "untested":
	default:
		return fmt.Errorf("verified.arm64_16k must be ok, crash or untested")
	}
	keys := map[string]bool{}
	for _, b := range Builtin {
		keys[b] = true
	}
	for _, f := range a.Fields {
		if !keyRe.MatchString(f.Key) || keys[f.Key] {
			return fmt.Errorf("bad or repeated field key %q", f.Key)
		}
		keys[f.Key] = true
		switch f.Type {
		case "string", "password", "number", "bool", "port":
		case "select":
			if len(f.Options) == 0 {
				return fmt.Errorf("select %s without options", f.Key)
			}
		default:
			return fmt.Errorf("field %s: unknown type %q", f.Key, f.Type)
		}
		if f.Label["zh-CN"] == "" || f.Label["en"] == "" {
			return fmt.Errorf("field %s: label missing", f.Key)
		}
	}
	for _, p := range a.Ports {
		if p.Port < 1 || p.Port > 65535 || !keys[p.Field] {
			return fmt.Errorf("port %s: bad port or field", p.Name)
		}
	}
	// every ${VAR} in compose must be a field or a builtin
	for _, m := range varRe.FindAllStringSubmatch(a.Compose, -1) {
		if !keys[m[1]] {
			return fmt.Errorf("compose uses ${%s}, which is not a field", m[1])
		}
	}
	for _, m := range mountRe.FindAllStringSubmatch(a.Compose, -1) {
		src := m[1]
		switch {
		case strings.HasPrefix(src, "${BERTH_DATA}"):
		case strings.HasPrefix(src, "/"):
			if !contains(hostPaths, strings.TrimSuffix(src, "/")) {
				return fmt.Errorf("host path %s is not allowed (data belongs under ${BERTH_DATA})", src)
			}
		default:
			return fmt.Errorf("named volume %q: data belongs under ${BERTH_DATA} so backups find it", src)
		}
	}
	if a.DataOwner != "" && !ownerRe.MatchString(a.DataOwner) {
		return fmt.Errorf("data_owner must be uid:gid")
	}
	if a.Site.Service != "" && a.Site.Port == 0 {
		return fmt.Errorf("site.port missing")
	}
	return nil
}

func contains(l []string, v string) bool {
	for _, x := range l {
		if x == v {
			return true
		}
	}
	return false
}

// Password makes a strong password from letters and digits (safe in .env and URLs).
func Password(n int) string {
	const abc = "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz23456789"
	b := make([]byte, n)
	for i := range b {
		x, _ := rand.Int(rand.Reader, big.NewInt(int64(len(abc))))
		b[i] = abc[x.Int64()]
	}
	return string(b)
}

// Values fills defaults and generated passwords and checks the input.
func (a *App) Values(in map[string]string) (map[string]string, error) {
	out := map[string]string{}
	for _, f := range a.Fields {
		v := strings.TrimSpace(in[f.Key])
		if v == "" {
			v = f.Default
		}
		if v == "" && f.Type == "password" && f.Generate {
			n := f.Length
			if n == 0 {
				n = 24
			}
			v = Password(n)
		}
		if v == "" && f.Required {
			return nil, &FieldError{Key: f.Key, Code: "app.field_required"}
		}
		if strings.ContainsAny(v, "\n\r") {
			return nil, &FieldError{Key: f.Key, Code: "app.field_invalid"}
		}
		switch f.Type {
		case "number", "port":
			var n int
			if _, err := fmt.Sscan(v, &n); v != "" && (err != nil || (f.Type == "port" && (n < 1 || n > 65535))) {
				return nil, &FieldError{Key: f.Key, Code: "app.field_invalid"}
			}
		case "bool":
			if v != "" && v != "true" && v != "false" {
				return nil, &FieldError{Key: f.Key, Code: "app.field_invalid"}
			}
		case "select":
			if v != "" && !contains(f.Options, v) {
				return nil, &FieldError{Key: f.Key, Code: "app.field_invalid"}
			}
		}
		out[f.Key] = v
	}
	return out, nil
}

type FieldError struct{ Key, Code string }

func (e *FieldError) Error() string { return e.Code + " " + e.Key }

// Env renders the .env for an instance (secrets included; the caller seals it).
func (a *App) Env(instance, dataDir string, values map[string]string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Berth app %s (%s) — managed by Berth\n", a.ID, a.Version)
	fmt.Fprintf(&b, "BERTH_APP=%s\nBERTH_DATA=%s\nTZ=%s\nPUID=1000\nPGID=1000\n", instance, dataDir, "Asia/Hong_Kong")
	for _, f := range a.Fields {
		fmt.Fprintf(&b, "%s=%s\n", f.Key, values[f.Key])
	}
	return b.String()
}

// Secret reports whether a field's value must be kept out of logs.
func (a *App) Secret(key string) bool {
	for _, f := range a.Fields {
		if f.Key == key {
			return f.Type == "password"
		}
	}
	return false
}
