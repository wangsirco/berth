package panel

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wangsirco/berth/internal/change"
	"github.com/wangsirco/berth/internal/plugin"
)

const testPluginYAML = `api: 1
id: speed
name: {en: Speed test}
version: 1.0.0
description: {en: iperf3 with a page}
author: Berth
runtime: {type: native, binary: {arm64: bin/a, amd64: bin/a}, page: true, RUNAS}
system: {listen: [5201], firewall_open: true, packages: [iperf3]}
permissions: [overview:read, sites:write]
settings:
  - {key: mode, label: {en: Mode}, type: select, options: [client, server], default: client}
  - {key: secret, label: {en: Key}, type: secret}
`

func testPkg(t *testing.T, runAs string, priv ed25519.PrivateKey) []byte {
	d := t.TempDir()
	y := strings.Replace(testPluginYAML, "RUNAS", "run_as: "+runAs, 1)
	os.WriteFile(filepath.Join(d, plugin.ManifestName), []byte(y), 0o644)
	os.MkdirAll(filepath.Join(d, "bin"), 0o755)
	os.WriteFile(filepath.Join(d, "bin/a"), []byte("\x7fELF"), 0o755)
	b, _, err := plugin.Pack(d, priv)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestPluginInspectAndPlan(t *testing.T) {
	c := newTC(t)
	u := c.mkUser("boss", "correct horse 9", "super")
	actor := change.Actor{Type: "user", ID: itoa(u.ID), Name: "boss"}
	preview := func(params string) (*change.Change, error) {
		return c.app.pipe.Preview(context.Background(), actor, "local", "plugin.install", []byte(params))
	}

	// a root plugin without a signature cannot be installed
	p, err := c.app.storePkg(testPkg(t, "root", nil))
	if err != nil {
		t.Fatal(err)
	}
	v := c.app.inspect(p)
	if v.Signed || v.RootBlocked != "plugin.root_unsigned" || !v.ArchOK {
		t.Fatalf("%+v", v)
	}
	if _, err := preview(`{"ref":"` + p.SHA256 + `"}`); err == nil || !strings.Contains(err.Error(), "root_unsigned") {
		t.Fatalf("unsigned root plugin planned: %v", err)
	}

	// signed: allowed, and the preview says it runs as root and lists the grants
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	p, _ = c.app.storePkg(testPkg(t, "root", priv))
	v = c.app.inspect(p)
	if !v.Signed || !v.SigOK || v.RootBlocked != "" || v.Fingerprint == "" {
		t.Fatalf("%+v", v)
	}
	ch, err := preview(`{"ref":"` + p.SHA256 + `","grant":["overview:read"]}`)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(ch.Plan.Commands, "\n")
	if ch.Plan.Summary != "plan.plugin.install_root" || !strings.Contains(joined, "✓ berth overview:read") || !strings.Contains(joined, "✗ berth sites:write") || !strings.Contains(joined, "!! runs as root") {
		t.Fatalf("%s\n%s", ch.Plan.Summary, joined)
	}
	// a grant it did not ask for, and a setting outside its options, are refused
	if _, err := preview(`{"ref":"` + p.SHA256 + `","grant":["users:write"]}`); err == nil {
		t.Fatal("extra grant accepted")
	}
	if _, err := preview(`{"ref":"` + p.SHA256 + `","settings":{"mode":"flood"}}`); err == nil {
		t.Fatal("bad setting accepted")
	}

	// once a key is pinned, a root package signed by another key is blocked
	c.app.db.Exec(`INSERT INTO plugin_keys(id, signer, first_seen) VALUES('speed', ?, 1)`, p.Signer)
	_, other, _ := ed25519.GenerateKey(rand.Reader)
	p2, _ := c.app.storePkg(testPkg(t, "root", other))
	v = c.app.inspect(p2)
	if !v.KeyChanged || v.RootBlocked != "plugin.root_key_changed" {
		t.Fatalf("%+v", v)
	}
	// a plugin-user one with a new key is only flagged
	p3, _ := c.app.storePkg(testPkg(t, "plugin", other))
	if v := c.app.inspect(p3); !v.KeyChanged || v.RootBlocked != "" {
		t.Fatalf("%+v", v)
	}
}

func TestSettingEnvAndCompose(t *testing.T) {
	m, _ := plugin.Parse([]byte(strings.Replace(testPluginYAML, "RUNAS", "run_as: plugin", 1)))
	env, err := settingEnv(m, map[string]string{"secret": "s3"})
	if err != nil || !strings.Contains(env, "BERTH_SETTING_MODE=client\n") || !strings.Contains(env, "BERTH_SETTING_SECRET=s3\n") {
		t.Fatal(env, err)
	}
	if _, err := settingEnv(m, map[string]string{"secret": "a\nb"}); err == nil {
		t.Fatal("newline in a setting accepted")
	}
	cm, _ := plugin.Parse([]byte("api: 1\nid: web\nname: {en: W}\nversion: 1.0.0\ndescription: {en: d}\nauthor: x\nruntime: {type: container, image: nginx:1, port: 80}\n"))
	y := pluginCompose(cm, 17201)
	for _, want := range []string{"image: nginx:1", `127.0.0.1:17201:80`, "no-new-privileges:true", "cap_drop: [ALL]", "mem_limit: 256m"} {
		if !strings.Contains(y, want) {
			t.Errorf("compose lacks %q:\n%s", want, y)
		}
	}
}
