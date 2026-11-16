// Package plugin is the Berth plugin format (api 1): the manifest berth-plugin.yaml, the
// package (.berthplugin, a tar.gz), its optional ed25519 signature, and the systemd unit a
// native plugin runs in. It has no panel dependencies, so the CLI's developer kit and the
// panel read packages the same way.
package plugin

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

const (
	ManifestName = "berth-plugin.yaml"
	SigName      = "SIGNATURE" // base64 ed25519 signature of Digest()
	SignerName   = "SIGNER"    // base64 public key
	API          = 1
	MaxPackage   = 256 << 20
)

type Text map[string]string // per UI language: zh-CN, zh-TW, en

func (t Text) In(locale string) string {
	for _, l := range []string{locale, "en", "zh-CN"} {
		if s := t[l]; s != "" {
			return s
		}
	}
	return ""
}

type Runtime struct {
	Type    string            `yaml:"type" json:"type"`                         // container | native
	Image   string            `yaml:"image,omitempty" json:"image,omitempty"`   // container: an image…
	Build   string            `yaml:"build,omitempty" json:"build,omitempty"`   // …or a folder in the package with a Dockerfile
	Port    int               `yaml:"port,omitempty" json:"port,omitempty"`     // container: where its page listens inside
	Binary  map[string]string `yaml:"binary,omitempty" json:"binary,omitempty"` // native: GOARCH → file in the package (arm64 required)
	Args    []string          `yaml:"args,omitempty" json:"args,omitempty"`
	RunAs   string            `yaml:"run_as,omitempty" json:"run_as,omitempty"` // native: plugin (default) | root
	MemMB   int               `yaml:"mem_mb,omitempty" json:"mem_mb,omitempty"`
	CPUs    float64           `yaml:"cpus,omitempty" json:"cpus,omitempty"`
	HasPage bool              `yaml:"page,omitempty" json:"page,omitempty"` // native: it serves a page on $PORT
}

type System struct {
	Listen       []int    `yaml:"listen,omitempty" json:"listen,omitempty"`               // ports it listens on for others
	FirewallOpen bool     `yaml:"firewall_open,omitempty" json:"firewall_open,omitempty"` // those ports should be reachable from outside
	Capabilities []string `yaml:"capabilities,omitempty" json:"capabilities,omitempty"`   // net_raw | net_admin | net_bind_service
	ReadPaths    []string `yaml:"read_paths,omitempty" json:"read_paths,omitempty"`
	WritePaths   []string `yaml:"write_paths,omitempty" json:"write_paths,omitempty"`
	HostNetwork  bool     `yaml:"host_network,omitempty" json:"host_network,omitempty"` // container only
	Packages     []string `yaml:"packages,omitempty" json:"packages,omitempty"`         // system packages Berth installs first (iperf3, …)
}

type Setting struct {
	Key      string   `yaml:"key" json:"key"`
	Label    Text     `yaml:"label" json:"label"`
	Hint     Text     `yaml:"hint,omitempty" json:"hint,omitempty"`
	Type     string   `yaml:"type" json:"type"` // text | number | bool | select | secret
	Default  string   `yaml:"default,omitempty" json:"default,omitempty"`
	Options  []string `yaml:"options,omitempty" json:"options,omitempty"`
	Required bool     `yaml:"required,omitempty" json:"required,omitempty"`
}

type Manifest struct {
	API         int       `yaml:"api" json:"api"`
	ID          string    `yaml:"id" json:"id"`
	Name        Text      `yaml:"name" json:"name"`
	Version     string    `yaml:"version" json:"version"`
	Description Text      `yaml:"description" json:"description"`
	Author      string    `yaml:"author" json:"author"`
	Homepage    string    `yaml:"homepage,omitempty" json:"homepage,omitempty"`
	Icon        string    `yaml:"icon,omitempty" json:"icon,omitempty"`               // a file in the package (png/svg)
	Screenshots []string  `yaml:"screenshots,omitempty" json:"screenshots,omitempty"` // files in the package
	Runtime     Runtime   `yaml:"runtime" json:"runtime"`
	System      System    `yaml:"system,omitempty" json:"system"`
	Permissions []string  `yaml:"permissions,omitempty" json:"permissions"` // Berth API scopes: sites:read …
	Events      []string  `yaml:"events,omitempty" json:"events,omitempty"`
	Webhook     string    `yaml:"webhook,omitempty" json:"webhook,omitempty"`
	Settings    []Setting `yaml:"settings,omitempty" json:"settings,omitempty"`
}

var (
	IDRe       = regexp.MustCompile(`^[a-z][a-z0-9-]{1,30}$`)
	versionRe  = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+([-+][0-9A-Za-z.-]+)?$`)
	imageRe    = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._/:@-]{0,200}$`)
	hookRe     = regexp.MustCompile(`^/[A-Za-z0-9/_.-]{0,100}$`)
	keyRe      = regexp.MustCompile(`^[a-z][a-z0-9_]{0,30}$`)
	pkgRe      = regexp.MustCompile(`^[a-z0-9][a-z0-9.+-]{0,60}$`)
	argRe      = regexp.MustCompile(`^[^\x00-\x1f"\\]{0,200}$`)
	Caps       = []string{"net_raw", "net_admin", "net_bind_service"}
	Events     = []string{"change.done", "alert", "uptime", "drift", "host.online", "host.offline", "backup.run"}
	forbidden  = []string{"/", "/etc", "/root", "/var/lib/berth", "/etc/berth", "/boot", "/usr", "/bin", "/sbin", "/lib", "/proc", "/sys", "/dev"}
	settingTyp = []string{"text", "number", "bool", "select", "secret"}
)

// Error says which part of a manifest or package is wrong.
type Error struct{ Field, Detail string }

func (e *Error) Error() string { return e.Field + ": " + e.Detail }

func bad(field, detail string) error { return &Error{field, detail} }

func has(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

// Validate checks a manifest; validScope says which Berth scopes exist.
func (m *Manifest) Validate(validScope func(string) bool) error {
	if m.Permissions == nil { // [] rather than null in JSON
		m.Permissions = []string{}
	}
	if m.API != API {
		return bad("api", "must be 1")
	}
	if !IDRe.MatchString(m.ID) {
		return bad("id", "lowercase letters, digits and -, 2-31 long, starting with a letter")
	}
	if !versionRe.MatchString(m.Version) {
		return bad("version", "like 1.2.3")
	}
	if m.Name.In("en") == "" {
		return bad("name", "required")
	}
	if m.Description.In("en") == "" {
		return bad("description", "required")
	}
	if strings.TrimSpace(m.Author) == "" {
		return bad("author", "required")
	}
	r := &m.Runtime
	switch r.Type {
	case "container":
		if (r.Image == "") == (r.Build == "") {
			return bad("runtime", "a container plugin has image or build")
		}
		if r.Image != "" && !imageRe.MatchString(r.Image) {
			return bad("runtime.image", "not an image name")
		}
		if r.Build != "" && path.Clean(r.Build) != "." && !safeRel(r.Build) {
			return bad("runtime.build", "a folder inside the package")
		}
		if r.Port < 1 || r.Port > 65535 {
			return bad("runtime.port", "1-65535")
		}
		if r.RunAs == "root" || len(r.Binary) > 0 {
			return bad("runtime", "run_as and binary are for native plugins")
		}
	case "native":
		if r.Binary["arm64"] == "" {
			return bad("runtime.binary", "arm64 is required")
		}
		for arch, b := range r.Binary {
			if (arch != "arm64" && arch != "amd64") || !safeRel(b) {
				return bad("runtime.binary", arch+": a file inside the package")
			}
		}
		if r.RunAs == "" {
			r.RunAs = "plugin"
		}
		if r.RunAs != "plugin" && r.RunAs != "root" {
			return bad("runtime.run_as", "plugin or root")
		}
		if m.System.HostNetwork {
			return bad("system.host_network", "container plugins only")
		}
		for _, a := range r.Args {
			if !argRe.MatchString(a) {
				return bad("runtime.args", "plain text")
			}
		}
	default:
		return bad("runtime.type", "container or native")
	}
	if r.MemMB < 0 || r.MemMB > 8192 || r.CPUs < 0 || r.CPUs > 16 {
		return bad("runtime", "mem_mb 0-8192, cpus 0-16")
	}
	s := m.System
	for _, p := range s.Listen {
		if p < 1 || p > 65535 {
			return bad("system.listen", "ports 1-65535")
		}
	}
	for _, c := range s.Capabilities {
		if !has(Caps, c) {
			return bad("system.capabilities", c+" (allowed: "+strings.Join(Caps, ", ")+")")
		}
	}
	if len(s.Capabilities) > 0 && r.Type != "native" {
		return bad("system.capabilities", "native plugins only")
	}
	for _, p := range append(append([]string{}, s.ReadPaths...), s.WritePaths...) {
		c := path.Clean(p)
		if !strings.HasPrefix(p, "/") || c != p || has(forbidden, c) {
			return bad("system paths", p+" is not allowed")
		}
	}
	for _, p := range s.Packages {
		if !pkgRe.MatchString(p) {
			return bad("system.packages", p)
		}
	}
	for _, sc := range m.Permissions {
		if validScope != nil && !validScope(sc) {
			return bad("permissions", sc)
		}
	}
	for _, e := range m.Events {
		if !has(Events, e) {
			return bad("events", e)
		}
	}
	if m.Webhook != "" && !hookRe.MatchString(m.Webhook) {
		return bad("webhook", "a path like /hooks/berth")
	}
	if len(m.Events) > 0 && m.Webhook == "" {
		return bad("webhook", "needed for events")
	}
	seen := map[string]bool{}
	for _, st := range m.Settings {
		if !keyRe.MatchString(st.Key) || seen[st.Key] {
			return bad("settings", "key "+st.Key)
		}
		seen[st.Key] = true
		if !has(settingTyp, st.Type) {
			return bad("settings", st.Key+": type "+st.Type)
		}
		if st.Type == "select" && len(st.Options) == 0 {
			return bad("settings", st.Key+": options")
		}
	}
	for _, f := range append([]string{m.Icon}, m.Screenshots...) {
		if f != "" && !safeRel(f) {
			return bad("icon/screenshots", f)
		}
	}
	return nil
}

func safeRel(p string) bool {
	c := path.Clean(strings.TrimPrefix(p, "./"))
	return c != "." && !strings.HasPrefix(c, "/") && !strings.HasPrefix(c, "..") && !strings.Contains(c, "/../")
}

// Parse reads a manifest.
func Parse(b []byte) (*Manifest, error) {
	var m Manifest
	if err := yaml.Unmarshal(b, &m); err != nil {
		return nil, bad("berth-plugin.yaml", err.Error())
	}
	return &m, nil
}

// ---- packages ----

type Package struct {
	Manifest *Manifest
	Files    map[string][]byte // without SIGNATURE and SIGNER
	Signer   string            // base64 public key, when signed
	Signed   bool
	SigOK    bool
	Size     int64
	SHA256   string // of the package file
}

// Digest is what a signature covers: every file's sha256 and name, sorted.
func Digest(files map[string][]byte) []byte {
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	var b bytes.Buffer
	b.WriteString("berth-plugin-v1\n")
	for _, n := range names {
		sum := sha256.Sum256(files[n])
		fmt.Fprintf(&b, "%s  %s\n", hex.EncodeToString(sum[:]), n)
	}
	return b.Bytes()
}

// Fingerprint: a short, readable form of a signer key, for people to compare.
func Fingerprint(pubB64 string) string {
	pub, err := base64.StdEncoding.DecodeString(pubB64)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(pub)
	h := strings.ToUpper(hex.EncodeToString(sum[:10]))
	var parts []string
	for i := 0; i < len(h); i += 4 {
		parts = append(parts, h[i:i+4])
	}
	return strings.Join(parts, " ")
}

// Read opens a package and checks its manifest and signature (a bad signature is reported, not
// fatal: the panel decides what to allow).
func Read(data []byte, validScope func(string) bool) (*Package, error) {
	if len(data) > MaxPackage {
		return nil, bad("package", "larger than 256 MB")
	}
	sum := sha256.Sum256(data)
	p := &Package{Files: map[string][]byte{}, Size: int64(len(data)), SHA256: hex.EncodeToString(sum[:])}
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, bad("package", "not a .berthplugin (tar.gz)")
	}
	tr := tar.NewReader(gz)
	var sig []byte
	var total int64
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, bad("package", "damaged")
		}
		if h.Typeflag == tar.TypeDir {
			continue
		}
		name := path.Clean(strings.TrimPrefix(h.Name, "./"))
		if h.Typeflag != tar.TypeReg || !safeRel(name) {
			return nil, bad("package", "unexpected entry "+h.Name)
		}
		total += h.Size
		if total > 4*MaxPackage {
			return nil, bad("package", "too large unpacked")
		}
		b, err := io.ReadAll(tr)
		if err != nil {
			return nil, bad("package", "damaged")
		}
		switch name {
		case SigName:
			sig, _ = base64.StdEncoding.DecodeString(strings.TrimSpace(string(b)))
		case SignerName:
			p.Signer = strings.TrimSpace(string(b))
		default:
			p.Files[name] = b
		}
	}
	mb, ok := p.Files[ManifestName]
	if !ok {
		return nil, bad("package", "no "+ManifestName)
	}
	m, err := Parse(mb)
	if err != nil {
		return nil, err
	}
	if err := m.Validate(validScope); err != nil {
		return nil, err
	}
	p.Manifest = m
	for _, f := range append(append([]string{m.Icon}, m.Screenshots...), binaries(m)...) {
		if f != "" {
			if _, ok := p.Files[path.Clean(strings.TrimPrefix(f, "./"))]; !ok {
				return nil, bad("package", "missing "+f)
			}
		}
	}
	if len(sig) > 0 || p.Signer != "" {
		p.Signed = true
		pub, err := base64.StdEncoding.DecodeString(p.Signer)
		p.SigOK = err == nil && len(pub) == ed25519.PublicKeySize && len(sig) == ed25519.SignatureSize && ed25519.Verify(pub, Digest(p.Files), sig)
	}
	return p, nil
}

func binaries(m *Manifest) []string {
	var out []string
	for _, b := range m.Runtime.Binary {
		out = append(out, b)
	}
	return out
}

// Pack makes a package from a folder; with a private key it is signed. Files are stored in
// name order with fixed times, so the same folder gives the same package.
func Pack(dir string, priv ed25519.PrivateKey) ([]byte, *Package, error) {
	files := map[string][]byte{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if rel != "." && (strings.HasPrefix(d.Name(), ".") || d.Name() == "node_modules") {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasPrefix(d.Name(), ".") || rel == SigName || rel == SignerName || strings.HasSuffix(rel, ".berthplugin") {
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		files[rel] = b
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	add := func(name string, b []byte, mode int64) error {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: mode, Size: int64(len(b)), Typeflag: tar.TypeReg, Format: tar.FormatPAX}); err != nil {
			return err
		}
		_, err := tw.Write(b)
		return err
	}
	for _, n := range names {
		mode := int64(0o644)
		if fi, err := os.Stat(filepath.Join(dir, n)); err == nil && fi.Mode()&0o111 != 0 {
			mode = 0o755
		}
		if err := add(n, files[n], mode); err != nil {
			return nil, nil, err
		}
	}
	if priv != nil {
		pub := priv.Public().(ed25519.PublicKey)
		add(SignerName, []byte(base64.StdEncoding.EncodeToString(pub)+"\n"), 0o644)
		add(SigName, []byte(base64.StdEncoding.EncodeToString(ed25519.Sign(priv, Digest(files)))+"\n"), 0o644)
	}
	tw.Close()
	gz.Close()
	p, err := Read(buf.Bytes(), nil)
	if err != nil {
		return nil, nil, err
	}
	return buf.Bytes(), p, nil
}

// ---- running a native plugin ----

const (
	NativeRoot = "/var/lib/berth-plugins" // <id>/app (the package, read-only) and <id>/data
	EnvDir     = "/etc/berth-plugins"     // <id>.env, 0600
)

func Unit(id string) string { return "berth-plugin-" + id }
func User(id string) string { return "berth-p-" + id }

// UnitFile renders the systemd service of a native plugin. A plugin-user one gets a read-only
// system, its own /tmp and only its data folder and granted paths to write; root runs without
// those walls (the installer was warned), but with the same memory and CPU limits.
func UnitFile(m *Manifest, arch string) (string, error) {
	bin := m.Runtime.Binary[arch]
	if bin == "" {
		return "", bad("runtime.binary", "no build for "+arch)
	}
	app := path.Join(NativeRoot, m.ID, "app")
	data := path.Join(NativeRoot, m.ID, "data")
	var b strings.Builder
	fmt.Fprintf(&b, "# Berth plugin %s %s — managed by Berth; reinstall to change\n[Unit]\nDescription=Berth plugin %s\nAfter=network-online.target\nWants=network-online.target\n\n[Service]\n",
		m.ID, m.Version, m.Name.In("en"))
	exec := path.Join(app, path.Clean(strings.TrimPrefix(bin, "./")))
	for _, a := range m.Runtime.Args {
		exec += " " + strconv.Quote(a)
	}
	fmt.Fprintf(&b, "ExecStart=%s\nWorkingDirectory=%s\nEnvironmentFile=%s\nRestart=on-failure\nRestartSec=3\n", exec, data, path.Join(EnvDir, m.ID+".env"))
	mem := m.Runtime.MemMB
	if mem == 0 {
		mem = 256
	}
	fmt.Fprintf(&b, "MemoryMax=%dM\n", mem)
	cpus := m.Runtime.CPUs
	if cpus == 0 {
		cpus = 1
	}
	fmt.Fprintf(&b, "CPUQuota=%d%%\n", int(cpus*100))
	if m.Runtime.RunAs == "root" {
		b.WriteString("User=root\n")
		return b.String(), nil
	}
	fmt.Fprintf(&b, "User=%s\nGroup=%s\nNoNewPrivileges=yes\nProtectSystem=strict\nProtectHome=yes\nPrivateTmp=yes\nPrivateDevices=yes\nProtectKernelTunables=yes\nProtectKernelModules=yes\nProtectControlGroups=yes\nRestrictSUIDSGID=yes\nLockPersonality=yes\n",
		User(m.ID), User(m.ID))
	rw := append([]string{data}, m.System.WritePaths...)
	fmt.Fprintf(&b, "ReadWritePaths=%s\n", strings.Join(rw, " "))
	if len(m.System.ReadPaths) > 0 {
		fmt.Fprintf(&b, "ReadOnlyPaths=%s\n", strings.Join(m.System.ReadPaths, " "))
	}
	caps := []string{}
	for _, c := range m.System.Capabilities {
		caps = append(caps, "CAP_"+strings.ToUpper(c))
	}
	listenLow := false
	for _, p := range m.System.Listen {
		listenLow = listenLow || p < 1024
	}
	if listenLow && !has(m.System.Capabilities, "net_bind_service") {
		caps = append(caps, "CAP_NET_BIND_SERVICE")
	}
	if len(caps) > 0 {
		fmt.Fprintf(&b, "AmbientCapabilities=%s\nCapabilityBoundingSet=%s\n", strings.Join(caps, " "), strings.Join(caps, " "))
	} else {
		b.WriteString("CapabilityBoundingSet=\n")
	}
	return b.String(), nil
}

var ErrUnsigned = errors.New("unsigned")
