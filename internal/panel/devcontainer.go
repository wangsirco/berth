package panel

// devcontainer.json → a dev workspace's container (方案 §5.9). The common subset is
// honoured: image or build (Dockerfile), remoteUser/containerUser, containerEnv/remoteEnv,
// workspaceFolder, the lifecycle commands, forwardPorts, VS Code extensions and
// hostRequirements. Features, docker compose based configs, mounts and runArgs are listed
// as notes instead (they would change what the container may touch on the host).

import (
	"encoding/json"
	"fmt"
	"github.com/wangsirco/berth/internal/agent/devenv"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

// jsonc strips // and /* */ comments and trailing commas (devcontainer.json is JSONC).
func jsonc(src string) []byte {
	var b strings.Builder
	inStr, esc := false, false
	for i := 0; i < len(src); i++ {
		c := src[i]
		if inStr {
			b.WriteByte(c)
			switch {
			case esc:
				esc = false
			case c == '\\':
				esc = true
			case c == '"':
				inStr = false
			}
			continue
		}
		switch {
		case c == '"':
			inStr = true
			b.WriteByte(c)
		case c == '/' && i+1 < len(src) && src[i+1] == '/':
			for i < len(src) && src[i] != '\n' {
				i++
			}
			b.WriteByte('\n')
		case c == '/' && i+1 < len(src) && src[i+1] == '*':
			i += 2
			for i+1 < len(src) && !(src[i] == '*' && src[i+1] == '/') {
				i++
			}
			i++
		default:
			b.WriteByte(c)
		}
	}
	// trailing commas: ", }" and ", ]" outside strings (comments are gone already)
	out := []byte(b.String())
	res := make([]byte, 0, len(out))
	inStr, esc = false, false
	for i := 0; i < len(out); i++ {
		c := out[i]
		if inStr {
			res = append(res, c)
			if esc {
				esc = false
			} else if c == '\\' {
				esc = true
			} else if c == '"' {
				inStr = false
			}
			continue
		}
		if c == '"' {
			inStr = true
		}
		if c == ',' {
			j := i + 1
			for j < len(out) && (out[j] == ' ' || out[j] == '\t' || out[j] == '\n' || out[j] == '\r') {
				j++
			}
			if j < len(out) && (out[j] == '}' || out[j] == ']') {
				continue
			}
		}
		res = append(res, c)
	}
	return res
}

type devcontainerJSON struct {
	Name             string            `json:"name"`
	Image            string            `json:"image"`
	DockerFile       string            `json:"dockerFile"`
	Build            *devBuildJSON     `json:"build"`
	DockerCompose    any               `json:"dockerComposeFile"`
	RemoteUser       string            `json:"remoteUser"`
	ContainerUser    string            `json:"containerUser"`
	ContainerEnv     map[string]string `json:"containerEnv"`
	RemoteEnv        map[string]string `json:"remoteEnv"`
	WorkspaceFolder  string            `json:"workspaceFolder"`
	ForwardPorts     []any             `json:"forwardPorts"`
	OnCreate         any               `json:"onCreateCommand"`
	UpdateContent    any               `json:"updateContentCommand"`
	PostCreate       any               `json:"postCreateCommand"`
	PostStart        any               `json:"postStartCommand"`
	Features         map[string]any    `json:"features"`
	Mounts           []any             `json:"mounts"`
	RunArgs          []string          `json:"runArgs"`
	Privileged       bool              `json:"privileged"`
	HostRequirements *struct {
		CPUs   int    `json:"cpus"`
		Memory string `json:"memory"`
	} `json:"hostRequirements"`
	Customizations struct {
		VSCode struct {
			Extensions []string `json:"extensions"`
		} `json:"vscode"`
	} `json:"customizations"`
}

type devBuildJSON struct {
	Dockerfile string            `json:"dockerfile"`
	Context    string            `json:"context"`
	Args       map[string]string `json:"args"`
	Target     string            `json:"target"`
}

// devBuild: a Dockerfile build, paths on the host.
type devBuild struct {
	Context    string            `json:"context"`
	Dockerfile string            `json:"dockerfile"` // relative to Context
	Args       map[string]string `json:"args,omitempty"`
	Target     string            `json:"target,omitempty"`
}

// devEnv is what the container is made from: a preset or a project's devcontainer.json.
type devEnv struct {
	Image        string            `json:"image,omitempty"`
	Build        *devBuild         `json:"build,omitempty"`
	User         string            `json:"user,omitempty"`
	Env          map[string]string `json:"env,omitempty"`
	Folder       string            `json:"folder"` // where the project is inside the container
	Hooks        map[string]string `json:"hooks,omitempty"`
	Extensions   []string          `json:"extensions,omitempty"`
	ForwardPorts []int             `json:"forward_ports,omitempty"`
	Notes        []string          `json:"notes,omitempty"` // i18n codes: what was not applied
	MinCPUs      int               `json:"min_cpus,omitempty"`
	MinMemMB     int               `json:"min_mem_mb,omitempty"`
}

// devPreset: ready environments (all published for arm64).
type devPreset struct {
	ID         string   `json:"id"`
	Image      string   `json:"image"`
	Extensions []string `json:"extensions"`
	SizeMB     int      `json:"size_mb"` // download, roughly
}

var devPresets = []devPreset{
	{ID: "base", Image: "mcr.microsoft.com/devcontainers/base:bookworm", SizeMB: 300},
	{ID: "node", Image: "mcr.microsoft.com/devcontainers/typescript-node:22-bookworm", Extensions: []string{"dbaeumer.vscode-eslint", "esbenp.prettier-vscode"}, SizeMB: 700},
	{ID: "python", Image: "mcr.microsoft.com/devcontainers/python:3.12-bookworm", Extensions: []string{"ms-python.python"}, SizeMB: 600},
	{ID: "go", Image: "mcr.microsoft.com/devcontainers/go:1.24-bookworm", Extensions: []string{"golang.go"}, SizeMB: 900},
	{ID: "rust", Image: "mcr.microsoft.com/devcontainers/rust:1-bookworm", Extensions: []string{"rust-lang.rust-analyzer"}, SizeMB: 900},
	{ID: "java", Image: "mcr.microsoft.com/devcontainers/java:21-bookworm", Extensions: []string{"redhat.java"}, SizeMB: 800},
}

func findPreset(id string) (devPreset, bool) {
	for _, p := range devPresets {
		if p.ID == id {
			return p, true
		}
	}
	return devPreset{}, false
}

func presetEnv(p devPreset, name string) devEnv {
	return devEnv{Image: p.Image, User: "vscode", Folder: "/workspaces/" + name, Extensions: p.Extensions}
}

var devVarRe = regexp.MustCompile(`\$\{([A-Za-z]+)(?::([A-Za-z0-9_]+))?\}`)

// parseDevcontainer turns devcontainer.json (at rel inside the host folder dir) into a devEnv.
func parseDevcontainer(text, rel, dir, name string) (devEnv, error) {
	var d devcontainerJSON
	if err := json.Unmarshal(jsonc(text), &d); err != nil {
		return devEnv{}, fmt.Errorf("%s: %v", rel, err)
	}
	e := devEnv{Image: d.Image, User: d.RemoteUser, Folder: "/workspaces/" + name}
	if e.User == "" {
		e.User = d.ContainerUser
	}
	if d.WorkspaceFolder != "" && strings.HasPrefix(d.WorkspaceFolder, "/") {
		e.Folder = path.Clean(d.WorkspaceFolder)
	}
	sub := func(s string) string {
		return devVarRe.ReplaceAllStringFunc(s, func(m string) string {
			g := devVarRe.FindStringSubmatch(m)
			switch g[1] {
			case "localWorkspaceFolder":
				return dir
			case "containerWorkspaceFolder":
				return e.Folder
			case "localWorkspaceFolderBasename", "containerWorkspaceFolderBasename":
				return path.Base(dir)
			case "localEnv", "containerEnv":
				return "" // the panel's environment is not the user's laptop
			}
			return m
		})
	}
	e.Folder = sub(e.Folder)
	cfgDir := path.Join(dir, path.Dir(rel)) // the folder devcontainer.json is in
	df, ctx := d.DockerFile, ""
	var args map[string]string
	target := ""
	if d.Build != nil {
		if d.Build.Dockerfile != "" {
			df = d.Build.Dockerfile
		}
		ctx, args, target = d.Build.Context, d.Build.Args, d.Build.Target
	}
	if df != "" {
		ctxAbs := path.Clean(path.Join(cfgDir, sub(ctx)))
		dfAbs := path.Clean(path.Join(cfgDir, sub(df)))
		if !inside(ctxAbs, dir) || !inside(dfAbs, dir) {
			return devEnv{}, fmt.Errorf("%s: the build context and Dockerfile must be inside the project", rel)
		}
		r := strings.TrimPrefix(strings.TrimPrefix(dfAbs, ctxAbs), "/")
		if !strings.HasPrefix(dfAbs, ctxAbs+"/") {
			r = dfAbs // outside the context: compose takes an absolute path
		}
		for k, v := range args {
			args[k] = sub(v)
		}
		e.Build = &devBuild{Context: ctxAbs, Dockerfile: r, Args: args, Target: target}
		e.Image = ""
	}
	if e.Image == "" && e.Build == nil {
		if d.DockerCompose != nil {
			return devEnv{}, fmt.Errorf("%s: dockerComposeFile is not supported; use an image or a Dockerfile", rel)
		}
		return devEnv{}, fmt.Errorf("%s: no image or Dockerfile", rel)
	}
	e.Env = map[string]string{}
	for k, v := range d.ContainerEnv {
		e.Env[k] = sub(v)
	}
	for k, v := range d.RemoteEnv {
		e.Env[k] = sub(v)
	}
	e.Hooks = map[string]string{}
	add := func(hook string, cmds ...any) {
		var parts []string
		for _, c := range cmds {
			if s := hookScript(c); s != "" {
				parts = append(parts, sub(s))
			}
		}
		if len(parts) > 0 {
			e.Hooks[hook] = "set -e\n" + strings.Join(parts, "\n") + "\n"
		}
	}
	add("on-create", d.OnCreate, d.UpdateContent)
	add("post-create", d.PostCreate)
	add("post-start", d.PostStart)
	for _, p := range d.ForwardPorts {
		switch v := p.(type) {
		case float64:
			e.ForwardPorts = append(e.ForwardPorts, int(v))
		case string: // "service:port" or "port"
			if n, err := strconv.Atoi(v[strings.LastIndex(v, ":")+1:]); err == nil {
				e.ForwardPorts = append(e.ForwardPorts, n)
			}
		}
	}
	e.Extensions = d.Customizations.VSCode.Extensions
	if len(d.Features) > 0 {
		ids := make([]string, 0, len(d.Features))
		for id := range d.Features {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		e.Notes = append(e.Notes, "dev.note_features:"+strings.Join(ids, ", "))
	}
	if len(d.Mounts) > 0 {
		e.Notes = append(e.Notes, "dev.note_mounts")
	}
	if len(d.RunArgs) > 0 || d.Privileged {
		e.Notes = append(e.Notes, "dev.note_runargs")
	}
	if h := d.HostRequirements; h != nil {
		e.MinCPUs = h.CPUs
		e.MinMemMB = memMB(h.Memory)
	}
	return e, nil
}

func inside(p, dir string) bool { return p == dir || strings.HasPrefix(p, dir+"/") }

// memMB: "4gb", "512mb", "8G" → megabytes.
func memMB(s string) int {
	s = strings.ToLower(strings.TrimSpace(s))
	m := regexp.MustCompile(`^([0-9.]+)\s*([kmgt]?)b?$`).FindStringSubmatch(s)
	if m == nil {
		return 0
	}
	f, _ := strconv.ParseFloat(m[1], 64)
	switch m[2] {
	case "k":
		f /= 1024
	case "", "m":
	case "g":
		f *= 1024
	case "t":
		f *= 1024 * 1024
	}
	return int(f)
}

// hookScript: a lifecycle command (string, argv array, or {name: command}) as shell.
func hookScript(c any) string {
	switch v := c.(type) {
	case string:
		return v
	case []any:
		parts := make([]string, 0, len(v))
		for _, a := range v {
			parts = append(parts, shQuote(fmt.Sprint(a)))
		}
		return strings.Join(parts, " ")
	case map[string]any:
		keys := make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var out []string
		for _, k := range keys {
			if s := hookScript(v[k]); s != "" {
				out = append(out, "# "+k+"\n"+s)
			}
		}
		return strings.Join(out, "\n")
	}
	return ""
}

func shQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// devCompose renders the workspace's compose file.
func devCompose(w devSpec, locale string) (string, error) {
	env := map[string]string{}
	for k, v := range w.Env.Env {
		env[k] = v
	}
	env["BERTH_NAME"] = w.Name
	env["BERTH_WORKSPACE"] = w.Env.Folder
	env["BERTH_USER"] = w.Env.User
	env["BERTH_SSH"] = boolStr(w.SSH)
	env["BERTH_CODE"] = boolStr(w.Code)
	env["BERTH_LOCALE"] = map[string]string{"zh-CN": "zh-cn", "zh-TW": "zh-tw"}[locale]
	env["BERTH_CLAUDE"] = boolStr(w.Claude)
	if w.Claude {
		env["DISABLE_AUTOUPDATER"] = "1" // one shared copy, updated from the panel
	}
	svc := map[string]any{
		"container_name": devContainer(w.Name),
		"hostname":       w.Name,
		"init":           true,
		"restart":        "unless-stopped",
		"entrypoint":     []string{"/bin/sh", "/opt/berth/boot.sh"},
		"command":        []string{},
		"working_dir":    w.Env.Folder,
		"environment":    env,
		"volumes": []string{
			w.Dir + ":" + w.Env.Folder,
			devToolsDir + ":/opt/berth:ro",
			devStateDir + "/" + w.Name + ":/opt/berth-state",
			"home:/home",
		},
		"labels":    map[string]string{"berth.dev": w.Name},
		"cpus":      strconv.FormatFloat(w.CPUs, 'f', -1, 64),
		"mem_limit": strconv.Itoa(w.MemoryMB) + "m",
	}
	if w.Env.Build != nil {
		b := map[string]any{"context": w.Env.Build.Context, "dockerfile": w.Env.Build.Dockerfile}
		if len(w.Env.Build.Args) > 0 {
			b["args"] = w.Env.Build.Args
		}
		if w.Env.Build.Target != "" {
			b["target"] = w.Env.Build.Target
		}
		svc["build"] = b
		svc["image"] = "berth-dev/" + w.Name + ":latest"
	} else {
		svc["image"] = w.Env.Image
	}
	var ports []string
	if w.SSH {
		ports = append(ports, fmt.Sprintf("%d:2222", w.SSHPort))
	}
	if w.Code && w.Access == "lan" {
		ports = append(ports, fmt.Sprintf("%d:8443", w.WebPort))
	}
	if len(ports) > 0 {
		svc["ports"] = ports
	}
	// a fixed bridge name, so the agent can keep the workspace off the LAN (no_lan)
	nets := map[string]any{"default": map[string]any{"driver_opts": map[string]string{"com.docker.network.bridge.name": devenv.BridgeName(w.Name)}}}
	doc := map[string]any{"services": map[string]any{"dev": svc}, "volumes": map[string]any{"home": map[string]any{}}, "networks": nets}
	b, err := yaml.Marshal(doc)
	return string(b), err
}

func boolStr(b bool) string {
	if b {
		return "1"
	}
	return "0"
}

const (
	devRoot     = "/srv/berth/dev"
	devToolsDir = devRoot + "/.tools"
	devStateDir = devRoot + "/.state"
)

func devContainer(name string) string { return "berth-dev-" + name }
func devProject(name string) string   { return "dev-" + name }
