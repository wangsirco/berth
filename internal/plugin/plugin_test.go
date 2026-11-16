package plugin

import (
	"crypto/ed25519"
	"crypto/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const nativeYAML = `api: 1
id: iperf3
name: {zh-CN: 网速测试, en: Speed test}
version: 1.0.0
description: {en: Measure bandwidth with iperf3}
author: Berth
icon: icon.svg
runtime:
  type: native
  binary: {arm64: bin/iperf3-arm64}
  page: true
system:
  listen: [5201]
  packages: [iperf3]
permissions: [overview:read]
settings:
  - {key: port, label: {en: Port}, type: number, default: "5201"}
`

func scope(s string) bool { return strings.HasSuffix(s, ":read") || strings.HasSuffix(s, ":write") }

func dir(t *testing.T, manifest string) string {
	d := t.TempDir()
	os.WriteFile(filepath.Join(d, ManifestName), []byte(manifest), 0o644)
	os.WriteFile(filepath.Join(d, "icon.svg"), []byte("<svg/>"), 0o644)
	os.MkdirAll(filepath.Join(d, "bin"), 0o755)
	os.WriteFile(filepath.Join(d, "bin/iperf3-arm64"), []byte("\x7fELF"), 0o755)
	os.WriteFile(filepath.Join(d, ".git"), []byte("x"), 0o644) // hidden: left out
	return d
}

func TestValidate(t *testing.T) {
	m, _ := Parse([]byte(nativeYAML))
	if err := m.Validate(scope); err != nil || m.Runtime.RunAs != "plugin" {
		t.Fatal(err, m.Runtime.RunAs)
	}
	bad := map[string]string{
		"no arm64":       strings.Replace(nativeYAML, "arm64: bin", "amd64: bin", 1),
		"bad id":         strings.Replace(nativeYAML, "id: iperf3", "id: Iperf3", 1),
		"escaping file":  strings.Replace(nativeYAML, "bin/iperf3-arm64", "../x", 1),
		"bad cap":        strings.Replace(nativeYAML, "listen: [5201]", "capabilities: [sys_admin]", 1),
		"system path":    strings.Replace(nativeYAML, "listen: [5201]", "write_paths: [/etc]", 1),
		"bad scope":      strings.Replace(nativeYAML, "[overview:read]", "[root]", 1),
		"bad run_as":     strings.Replace(nativeYAML, "page: true", "page: true\n  run_as: admin", 1),
		"bad setting":    strings.Replace(nativeYAML, "type: number", "type: color", 1),
		"events no hook": strings.Replace(nativeYAML, "permissions:", "events: [alert]\npermissions:", 1),
		"api 2":          strings.Replace(nativeYAML, "api: 1", "api: 2", 1),
	}
	for name, y := range bad {
		m, err := Parse([]byte(y))
		if err == nil {
			err = m.Validate(scope)
		}
		if err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestPackSignVerify(t *testing.T) {
	d := dir(t, nativeYAML)
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	b, p, err := Pack(d, priv)
	if err != nil {
		t.Fatal(err)
	}
	if !p.Signed || !p.SigOK || p.Manifest.ID != "iperf3" || len(p.Files) != 3 {
		t.Fatalf("%+v files=%v", p, len(p.Files))
	}
	if Fingerprint(p.Signer) == "" {
		t.Fatal("no fingerprint")
	}
	again, _, _ := Pack(d, priv)
	if string(again) != string(b) {
		t.Fatal("pack is not reproducible")
	}
	// unsigned
	_, u, _ := Pack(d, nil)
	if u.Signed || u.SigOK {
		t.Fatal("unsigned package says signed")
	}
	// a changed file breaks the signature
	os.WriteFile(filepath.Join(d, "bin/iperf3-arm64"), []byte("evil"), 0o755)
	files := map[string][]byte{}
	for k, v := range p.Files {
		files[k] = v
	}
	files["bin/iperf3-arm64"] = []byte("evil")
	if string(Digest(files)) == string(Digest(p.Files)) {
		t.Fatal("digest ignores content")
	}
	tampered := retar(t, b, "bin/iperf3-arm64", []byte("evil"))
	tp, err := Read(tampered, scope)
	if err != nil || !tp.Signed || tp.SigOK {
		t.Fatalf("tampered: %+v %v", tp, err)
	}
}

func TestMissingBinary(t *testing.T) {
	d := dir(t, nativeYAML)
	os.Remove(filepath.Join(d, "bin/iperf3-arm64"))
	if _, _, err := Pack(d, nil); err == nil {
		t.Fatal("package without its binary accepted")
	}
}

func TestUnitFile(t *testing.T) {
	m, _ := Parse([]byte(nativeYAML))
	m.Validate(scope)
	u, err := UnitFile(m, "arm64")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"User=berth-p-iperf3", "ProtectSystem=strict", "NoNewPrivileges=yes", "ReadWritePaths=/var/lib/berth-plugins/iperf3/data", "MemoryMax=256M",
		"ExecStart=/var/lib/berth-plugins/iperf3/app/bin/iperf3-arm64", "EnvironmentFile=/etc/berth-plugins/iperf3.env", "CapabilityBoundingSet=\n"} {
		if !strings.Contains(u, want) {
			t.Errorf("unit lacks %q:\n%s", want, u)
		}
	}
	if _, err := UnitFile(m, "amd64"); err == nil {
		t.Fatal("amd64 without a build")
	}
	m.System.Capabilities = []string{"net_raw"}
	u, _ = UnitFile(m, "arm64")
	if !strings.Contains(u, "AmbientCapabilities=CAP_NET_RAW") {
		t.Fatal(u)
	}
	m.Runtime.RunAs = "root"
	u, _ = UnitFile(m, "arm64")
	if !strings.Contains(u, "User=root") || strings.Contains(u, "ProtectSystem") || !strings.Contains(u, "MemoryMax") {
		t.Fatal(u)
	}
}
