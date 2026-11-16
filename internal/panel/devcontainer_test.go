package panel

import (
	"strings"
	"testing"
)

func TestJSONC(t *testing.T) {
	src := `{
	// a comment
	"name": "a // not a comment", /* block */
	"image": "x",
	"forwardPorts": [3000, "db:5432",],
}`
	e, err := parseDevcontainer(src, ".devcontainer/devcontainer.json", "/srv/p", "p")
	if err != nil {
		t.Fatal(err)
	}
	if e.Image != "x" || len(e.ForwardPorts) != 2 || e.ForwardPorts[1] != 5432 {
		t.Fatalf("%+v", e)
	}
}

func TestParseDevcontainerBuild(t *testing.T) {
	src := `{
	"build": {"dockerfile": "Dockerfile", "context": "..", "args": {"V": "${localWorkspaceFolderBasename}"}},
	"remoteUser": "node",
	"containerEnv": {"A": "1"},
	"postCreateCommand": ["npm", "ci"],
	"postStartCommand": {"a": "echo a", "b": "echo b"},
	"features": {"ghcr.io/devcontainers/features/go:1": {}},
	"hostRequirements": {"cpus": 4, "memory": "8gb"},
	"customizations": {"vscode": {"extensions": ["golang.go"]}}
}`
	e, err := parseDevcontainer(src, ".devcontainer/devcontainer.json", "/srv/proj", "proj")
	if err != nil {
		t.Fatal(err)
	}
	if e.Build == nil || e.Build.Context != "/srv/proj" || e.Build.Dockerfile != ".devcontainer/Dockerfile" || e.Build.Args["V"] != "proj" {
		t.Fatalf("build %+v", e.Build)
	}
	if e.User != "node" || e.Env["A"] != "1" || e.Folder != "/workspaces/proj" {
		t.Fatalf("%+v", e)
	}
	if !strings.Contains(e.Hooks["post-create"], "'npm' 'ci'") || !strings.Contains(e.Hooks["post-start"], "echo a\n# b\necho b") {
		t.Fatalf("hooks %q", e.Hooks)
	}
	if len(e.Notes) != 1 || !strings.HasPrefix(e.Notes[0], "dev.note_features:") || e.MinCPUs != 4 || e.MinMemMB != 8192 {
		t.Fatalf("%+v", e)
	}
	// a Dockerfile outside the project is refused
	if _, err := parseDevcontainer(`{"build": {"dockerfile": "../../etc/x"}}`, ".devcontainer/devcontainer.json", "/srv/proj", "proj"); err == nil {
		t.Fatal("escaped the project")
	}
	if _, err := parseDevcontainer(`{"dockerComposeFile": "c.yml"}`, ".devcontainer.json", "/srv/proj", "proj"); err == nil {
		t.Fatal("compose configs are not supported")
	}
}

func TestDevCompose(t *testing.T) {
	s := devSpec{Name: "web", Dir: "/srv/berth/dev/web", Env: presetEnv(devPresets[1], "web"), CPUs: 1.5, MemoryMB: 1024,
		Code: true, Access: "lan", SSH: true, SSHPort: 2201, WebPort: 8401}
	c, err := devCompose(s, "zh-CN")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"container_name: berth-dev-web", "/srv/berth/dev/web:/workspaces/web", "2201:2222", "8401:8443",
		"mem_limit: 1024m", `cpus: "1.5"`, "BERTH_LOCALE: zh-cn", "/srv/berth/dev/.tools:/opt/berth:ro", "home:/home"} {
		if !strings.Contains(c, want) {
			t.Errorf("missing %q in\n%s", want, c)
		}
	}
	s.Access = "public"
	s.SSH = false
	c, _ = devCompose(s, "en")
	if strings.Contains(c, "8443") || strings.Contains(c, "2222") {
		t.Fatalf("public/no-ssh publishes no ports:\n%s", c)
	}
}
