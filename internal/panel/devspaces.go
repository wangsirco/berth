package panel

// Dev workspaces (方案 §5.9): one container per project with only that project's folder
// mounted, its own CPU and memory, SSH for desktop editors (VS Code, Cursor, Claude Code)
// and web VS Code (code-server). One dialog makes all of it: the folder (new, git clone or
// an existing one), the environment (the project's devcontainer.json or a ready image), the
// size, and how the web editor is reached (this LAN over HTTPS with the panel's own CA, or a
// public site with a domain). The editor has a password the panel keeps sealed.

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/wangsirco/berth/internal/change"
	"github.com/wangsirco/berth/internal/errs"
	"github.com/wangsirco/berth/internal/panel/certkit"
)

// devSpec is one workspace as stored (dev_workspaces.spec).
type devSpec struct {
	Name     string   `json:"name"`
	Source   string   `json:"source"` // new | git | dir
	GitURL   string   `json:"git_url,omitempty"`
	GitRef   string   `json:"git_ref,omitempty"`
	Dir      string   `json:"dir"`  // the host folder mounted at Env.Folder
	Made     bool     `json:"made"` // Berth made the folder (delete may trash it)
	EnvMode  string   `json:"env"`  // auto | preset
	Preset   string   `json:"preset"`
	DevPath  string   `json:"devcontainer_path,omitempty"` // when devcontainer.json was used
	Env      devEnv   `json:"environment"`
	CPUs     float64  `json:"cpus"`
	MemoryMB int      `json:"memory_mb"`
	Code     bool     `json:"code"`
	Access   string   `json:"access"` // lan | public
	Domains  []string `json:"domains,omitempty"`
	SiteID   int64    `json:"site_id,omitempty"`
	SSH      bool     `json:"ssh"`
	SSHPort  int      `json:"ssh_port,omitempty"`
	WebPort  int      `json:"web_port,omitempty"`
	OwnerID  int64    `json:"owner_id"` // whose SSH keys go in
	// Claude Code (方案 §5.9): installed, how tasks may act (auto: anything inside the
	// container; edits: change files only), how it signs in (login | api_key | token)
	Claude     bool   `json:"claude,omitempty"`
	ClaudeMode string `json:"claude_mode,omitempty"`
	ClaudeAuth string `json:"claude_auth,omitempty"`
	// NoLAN: the internet only — nothing on this LAN, other private networks or the server
	NoLAN bool `json:"no_lan,omitempty"`
}

type devParams struct {
	Name     string   `json:"name"`
	Source   string   `json:"source"`
	GitURL   string   `json:"git_url"`
	GitRef   string   `json:"git_ref"`
	Dir      string   `json:"dir"`
	Env      string   `json:"env"`
	Preset   string   `json:"preset"`
	CPUs     float64  `json:"cpus"`
	MemoryMB int      `json:"memory_mb"`
	Code     *bool    `json:"code"`
	Access   string   `json:"access"`
	Domains  []string `json:"domains"`
	SSH      *bool    `json:"ssh"`
	Claude   *bool    `json:"claude"`
	NoLAN    *bool    `json:"no_lan"`
	// undo of delete: the same ports as before when still free
	SSHPort int `json:"ssh_port"`
	WebPort int `json:"web_port"`
}

var (
	devNameRe  = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,30}$`)
	devSizes   = []map[string]any{{"id": "small", "cpus": 1, "memory_mb": 1024}, {"id": "medium", "cpus": 2, "memory_mb": 2048}, {"id": "large", "cpus": 4, "memory_mb": 4096}}
	devSSHBase = 2201
	devWebBase = 8401
)

func (p *devParams) defaults() {
	if p.Source == "" {
		p.Source = "new"
	}
	if p.Env == "" {
		p.Env = "auto"
	}
	if p.Preset == "" {
		p.Preset = "base"
	}
	if p.CPUs == 0 {
		p.CPUs = 2
	}
	if p.MemoryMB == 0 {
		p.MemoryMB = 2048
	}
	if p.Code == nil {
		t := true
		p.Code = &t
	}
	if p.SSH == nil {
		t := true
		p.SSH = &t
	}
	if p.Access == "" {
		p.Access = "lan"
	}
	if p.Claude == nil {
		f := false
		p.Claude = &f
	}
	if p.NoLAN == nil { // an autonomous assistant stays off the LAN unless asked
		v := *p.Claude
		p.NoLAN = &v
	}
}

func (p devParams) check() error {
	if !devNameRe.MatchString(p.Name) {
		return errs.New(400, "dev.bad_name")
	}
	if !contains([]string{"new", "git", "dir"}, p.Source) {
		return errs.New(400, "dev.bad_source")
	}
	if p.Source == "git" && p.GitURL == "" {
		return errs.New(400, "dev.bad_git_url")
	}
	if p.Source == "dir" && p.Dir == "" {
		return errs.New(400, "dev.bad_dir", "dir", "")
	}
	if !contains([]string{"auto", "preset"}, p.Env) {
		return errs.New(400, "params.enum", "name", "env", "allowed", "auto, preset")
	}
	if _, ok := findPreset(p.Preset); !ok {
		return errs.New(400, "dev.bad_preset", "preset", p.Preset)
	}
	if p.CPUs < 0.25 || p.CPUs > 64 || p.MemoryMB < 256 || p.MemoryMB > 256*1024 {
		return errs.New(400, "dev.bad_size")
	}
	if !contains([]string{"lan", "public"}, p.Access) {
		return errs.New(400, "params.enum", "name", "access", "allowed", "lan, public")
	}
	if *p.Code && p.Access == "public" && len(p.Domains) == 0 {
		return errs.New(400, "dev.needs_domain")
	}
	return nil
}

func (a *App) devWorkspace(host, name string) (devSpec, error) {
	var raw string
	err := a.db.QueryRow(`SELECT spec FROM dev_workspaces WHERE host=? AND name=?`, host, name).Scan(&raw)
	if err != nil {
		return devSpec{}, errs.New(404, "dev.not_found", "name", name)
	}
	var s devSpec
	json.Unmarshal([]byte(raw), &s)
	return s, nil
}

func (a *App) devWorkspaces(host string) []devSpec {
	rows, err := a.db.Query(`SELECT spec FROM dev_workspaces WHERE host=? ORDER BY created_at`, host)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []devSpec
	for rows.Next() {
		var raw string
		rows.Scan(&raw)
		var s devSpec
		json.Unmarshal([]byte(raw), &s)
		out = append(out, s)
	}
	return out
}

func (a *App) saveDev(host string, s devSpec) {
	b, _ := json.Marshal(s)
	a.db.Exec(`UPDATE dev_workspaces SET spec=? WHERE host=? AND name=?`, string(b), host, s.Name)
}

// devPorts picks free host ports for SSH and the web editor (wanted ones when still free).
func (a *App) devPorts(ctx context.Context, host string, wantSSH, wantWeb int) (int, int, error) {
	used, err := a.hostPortsInUse(ctx, host)
	if err != nil {
		return 0, 0, err
	}
	for _, w := range a.devWorkspaces(host) {
		used[w.SSHPort], used[w.WebPort] = true, true
	}
	pick := func(want, base int) int {
		if want > 0 && !used[want] {
			used[want] = true
			return want
		}
		for p := base; p < base+500; p++ {
			if !used[p] {
				used[p] = true
				return p
			}
		}
		return 0
	}
	s, w := pick(wantSSH, devSSHBase), pick(wantWeb, devWebBase)
	if s == 0 || w == 0 {
		return 0, 0, errs.New(409, "dev.no_ports")
	}
	return s, w, nil
}

func (a *App) devLANIP(ctx context.Context, host string) string {
	var lan struct {
		IP string `json:"ip"`
	}
	a.call(ctx, host, "GET", "/v1/net/lan-ip", nil, &lan)
	return lan.IP
}

// devCert: the web editor's HTTPS certificate on the LAN, from the panel's own CA, for
// every address of the host (LAN, Tailscale, …) so any of them opens without a warning
// once the CA is trusted.
func (a *App) devCert(ctx context.Context, host, name string) (string, string, error) {
	if a.localSigner == nil {
		return "", "", errs.New(500, "cert.no_local_ca")
	}
	var addrs []string
	a.call(ctx, host, "GET", "/v1/net/addrs", nil, &addrs)
	var ips []string
	for _, s := range addrs {
		if ip := net.ParseIP(s); ip != nil && ip.To4() != nil && !ip.IsLoopback() && len(ips) < 16 {
			ips = append(ips, s)
		}
	}
	f := certkit.Fields{CN: "Berth dev " + name, DNS: []string{"localhost"}, IPs: ips, Days: 397}
	b, err := a.localSigner.Issue(f)
	if err != nil && len(ips) > 1 { // a name constraint may refuse some addresses: the LAN one then
		f.IPs = []string{a.devLANIP(ctx, host)}
		b, err = a.localSigner.Issue(f)
	}
	if err != nil {
		return "", "", certProblem(err)
	}
	return string(b.CertPEM), string(b.KeyPEM), nil
}

// devResolveEnv: the project's devcontainer.json when asked and present, else the preset.
func devResolveEnv(s *devSpec, devPath, devText string, log func(string, ...any)) {
	p, _ := findPreset(s.Preset)
	s.DevPath = ""
	if s.EnvMode == "auto" && devText != "" {
		e, err := parseDevcontainer(devText, devPath, s.Dir, s.Name)
		if err == nil {
			s.Env, s.DevPath = e, devPath
			log("using %s", devPath)
			return
		}
		log("devcontainer.json not used: %v", err)
		s.Env = presetEnv(p, s.Name)
		s.Env.Notes = append(s.Env.Notes, "dev.note_parse_failed:"+err.Error())
		return
	}
	s.Env = presetEnv(p, s.Name)
}

// devState sends the per-workspace secrets and scripts (fresh: postCreate runs again).
func (a *App) devState(env change.Env, s devSpec, chown, fresh bool) error {
	pw, err := a.devPassword(env.Host, s.Name)
	if err != nil {
		return err
	}
	st := map[string]any{"authorized_keys": strings.Join(a.sshKeyLines(s.OwnerID), "\n") + "\n", "password": pw,
		"hooks": s.Env.Hooks, "extensions": devExtensions(s, a.deskLocale(env)), "chown": chown, "fresh": fresh,
		"claude_env": a.claudeEnv(env.Host, s.Name)}
	if s.Code && s.Access == "lan" {
		crt, key, err := a.devCert(env.Ctx, env.Host, s.Name)
		if err != nil {
			return err
		}
		st["tls_cert"], st["tls_key"] = crt, key
	}
	return a.call(env.Ctx, env.Host, "PUT", "/v1/dev/"+s.Name+"/state", st, nil)
}

func devExtensions(s devSpec, locale string) []string {
	ext := append([]string{}, s.Env.Extensions...)
	switch locale {
	case "zh-CN":
		ext = append(ext, "MS-CEINTL.vscode-language-pack-zh-hans")
	case "zh-TW":
		ext = append(ext, "MS-CEINTL.vscode-language-pack-zh-hant")
	}
	return ext
}

func (a *App) devPassword(host, name string) (string, error) {
	var enc []byte
	a.db.QueryRow(`SELECT secret_enc FROM dev_workspaces WHERE host=? AND name=?`, host, name).Scan(&enc)
	if len(enc) == 0 {
		return "", nil
	}
	b, err := a.open(enc)
	return string(b), err
}

// devApplyCompose writes the compose file and brings the container up (building first
// when the project has a Dockerfile; recreate: replace a running container).
func (a *App) devApplyCompose(env change.Env, s devSpec, create, recreate bool) error {
	compose, err := devCompose(s, a.deskLocale(env))
	if err != nil {
		return err
	}
	body := map[string]any{"compose": compose, "create": create}
	if err := a.call(env.Ctx, env.Host, "PUT", "/v1/compose/projects/"+devProject(s.Name)+"/files", body, nil); err != nil {
		if e := errs.From(err); !(create && e.Code == "compose.exists") {
			return err
		}
		body["create"] = false
		if err := a.call(env.Ctx, env.Host, "PUT", "/v1/compose/projects/"+devProject(s.Name)+"/files", body, nil); err != nil {
			return err
		}
	}
	if s.Env.Build != nil {
		env.Log("::step 2 build")
		if err := agentOp(env, "/v1/compose/projects/"+devProject(s.Name)+"/build", nil); err != nil {
			return err
		}
	} else if recreate {
		env.Log("::step 2 pull")
		agentOp(env, "/v1/compose/projects/"+devProject(s.Name)+"/pull", nil)
	}
	if recreate {
		a.call(env.Ctx, env.Host, "POST", "/v1/docker/containers/"+devContainer(s.Name)+"/remove", map[string]bool{"force": true}, nil)
	}
	// the guard first: the container never runs a moment with the LAN open
	if err := a.call(env.Ctx, env.Host, "POST", "/v1/dev/"+s.Name+"/guard", map[string]bool{"on": s.NoLAN}, nil); err != nil {
		return err
	}
	env.Log("::step 3 up")
	return agentOp(env, "/v1/compose/projects/"+devProject(s.Name)+"/up", nil)
}

// devSite makes the public site for the web editor (HTTPS by Caddy, then the password).
func (a *App) devSite(env change.Env, s *devSpec) error {
	k, _ := change.Get("site.create")
	raw, _ := json.Marshal(map[string]any{"domains": s.Domains, "tls": map[string]string{"mode": "auto"},
		"target": map[string]any{"type": "container", "container": devContainer(s.Name), "port": 8443}})
	out, _, err := k.Apply(env, raw)
	if err != nil {
		return err
	}
	if m, ok := out.(map[string]any); ok {
		if id, ok := m["id"].(int64); ok {
			s.SiteID = id
		}
	}
	return nil
}

// devSites: the sites aimed at a workspace's container (its own and any made later).
func (a *App) devSites(host, name string) []Site {
	all, _ := a.sites(host)
	var out []Site
	for _, st := range all {
		if st.Target.Type == "container" && st.Target.Container == devContainer(name) {
			out = append(out, st)
		}
	}
	return out
}

func devObj(name, host string) change.Object {
	return change.Object{Type: "dev", ID: name, Name: name, Host: host}
}

func init() {
	routeHooks = append(routeHooks, func(a *App, mux *http.ServeMux) {
		mux.HandleFunc("GET /api/v1/hosts/{host}/dev", a.authed("dev.use", a.handleDevList))
		mux.HandleFunc("GET /api/v1/hosts/{host}/dev/inspect", a.authed("dev.use", func(w http.ResponseWriter, r *http.Request) {
			var out map[string]any
			if err := a.call(r.Context(), r.PathValue("host"), "GET", "/v1/dev/inspect?dir="+url.QueryEscape(r.URL.Query().Get("dir")), nil, &out); err != nil {
				a.fail(w, r, err)
				return
			}
			if t, _ := out["devcontainer"].(string); t != "" {
				e, err := parseDevcontainer(t, out["devcontainer_path"].(string), r.URL.Query().Get("dir"), "x")
				if err != nil {
					out["error"] = err.Error()
				} else {
					out["environment"] = e
				}
				delete(out, "devcontainer")
			}
			writeJSON(w, 200, out)
		}))
		mux.HandleFunc("GET /api/v1/hosts/{host}/dev/{name}", a.authed("dev.use", func(w http.ResponseWriter, r *http.Request) {
			s, err := a.devWorkspace(r.PathValue("host"), r.PathValue("name"))
			if err != nil {
				a.fail(w, r, err)
				return
			}
			var st map[string]any
			a.call(r.Context(), r.PathValue("host"), "GET", "/v1/dev/"+s.Name+"/status", nil, &st)
			writeJSON(w, 200, map[string]any{"status": st})
		}))
		mux.HandleFunc("GET /api/v1/hosts/{host}/dev/{name}/password", a.authed("dev.use", func(w http.ResponseWriter, r *http.Request) {
			if _, err := a.devWorkspace(r.PathValue("host"), r.PathValue("name")); err != nil {
				a.fail(w, r, err)
				return
			}
			pw, err := a.devPassword(r.PathValue("host"), r.PathValue("name"))
			if err != nil {
				a.fail(w, r, err)
				return
			}
			writeJSON(w, 200, map[string]string{"password": pw})
		}))
	})

	nameOnly := change.ParamSchema{Props: map[string]change.Prop{"name": {Type: "string", Desc: "workspace name"}}, Required: []string{"name"}}
	sizeProps := map[string]change.Prop{
		"cpus":      {Type: "number", Desc: "CPU cores (default 2)"},
		"memory_mb": {Type: "integer", Desc: "memory limit in MB (default 2048)"},
		"code":      {Type: "boolean", Desc: "web VS Code (default true)"},
		"access":    {Type: "string", Enum: []string{"lan", "public"}, Desc: "web VS Code: lan (HTTPS on a port, panel CA) or public (a site with domains)"},
		"domains":   {Type: "array", Desc: "with access=public"},
		"ssh":       {Type: "boolean", Desc: "SSH with your saved public keys (default true)"},
		"claude":    {Type: "boolean", Desc: "Claude Code in the workspace: chat in the terminal, send it tasks (default false)"},
		"no_lan":    {Type: "boolean", Desc: "internet only: no access to this LAN, private networks or the server itself (default: on with Claude Code)"},
	}
	createProps := map[string]change.Prop{
		"name":    {Type: "string", Desc: "a-z, 0-9, -"},
		"source":  {Type: "string", Enum: []string{"new", "git", "dir"}, Desc: "new: an empty folder; git: clone git_url; dir: mount an existing folder"},
		"git_url": {Type: "string"}, "git_ref": {Type: "string", Desc: "branch or tag"},
		"dir":    {Type: "string", Desc: "with source=dir: under /srv, /home, /opt, /data, /mnt, /media or /var/www"},
		"env":    {Type: "string", Enum: []string{"auto", "preset"}, Desc: "auto: the project's devcontainer.json when it has one"},
		"preset": {Type: "string", Desc: "base | node | python | go | rust | java"},
	}
	for k, v := range sizeProps {
		createProps[k] = v
	}

	change.Register(change.Kind{
		Name: "dev.create", Perm: "dev.use", Scope: "dev:write", Title: "kind.dev.create", Params: change.ParamSchema{Props: createProps, Required: []string{"name"}},
		CLI: "berth dev create {{.name}}", Heavy: true, MemMB: 512,
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			a := svc(env)
			var p devParams
			json.Unmarshal(raw, &p)
			p.defaults()
			if err := p.check(); err != nil {
				return change.Plan{}, err
			}
			if _, err := a.devWorkspace(env.Host, p.Name); err == nil {
				return change.Plan{}, errs.New(409, "dev.exists", "name", p.Name)
			}
			if p.Source == "dir" {
				var out map[string]any
				if err := a.call(env.Ctx, env.Host, "GET", "/v1/dev/inspect?dir="+url.QueryEscape(p.Dir), nil, &out); err != nil {
					return change.Plan{}, err
				}
			}
			sshP, webP, err := a.devPorts(env.Ctx, env.Host, p.SSHPort, p.WebPort)
			if err != nil {
				return change.Plan{}, err
			}
			if *p.Code && p.Access == "public" {
				st := Site{ID: -1, Domains: p.Domains}
				if err := a.checkDomains(st); err != nil {
					return change.Plan{}, err
				}
			}
			pr, _ := findPreset(p.Preset)
			from := map[string]string{"new": devRoot + "/" + p.Name + " (new)", "git": p.GitURL, "dir": p.Dir}[p.Source]
			pl := change.Plan{Summary: "plan.dev.create", SummaryP: map[string]any{"name": p.Name, "cpus": p.CPUs, "mem": p.MemoryMB},
				Risk: change.RiskLow, Undoable: true, Affected: []change.Object{devObj(p.Name, env.Host)},
				Commands: []string{"# folder: " + from}}
			if p.Env == "auto" {
				pl.Commands = append(pl.Commands, "# environment: the project's devcontainer.json if it has one, else "+pr.Image)
			} else {
				pl.Commands = append(pl.Commands, "# environment: "+pr.Image)
			}
			pl.Commands = append(pl.Commands, fmt.Sprintf("docker compose -p %s up -d   # %g CPU, %d MB", devProject(p.Name), p.CPUs, p.MemoryMB))
			if *p.SSH {
				pl.Commands = append(pl.Commands, fmt.Sprintf("# SSH on port %d (keys only: %d saved key(s))", sshP, len(a.sshKeys(actorUID(env)))))
			}
			if *p.NoLAN {
				pl.Commands = append(pl.Commands, "# internet only: this LAN, private networks and the server itself are blocked (iptables on its own bridge)")
			}
			if *p.Claude {
				pl.Commands = append(pl.Commands, "# Claude Code (one shared copy, about 360 MB the first time); you sign it in yourself")
			}
			if *p.Code {
				if p.Access == "lan" {
					pl.Commands = append(pl.Commands, fmt.Sprintf("# web VS Code: https://<LAN address>:%d (certificate from the panel's CA, password)", webP))
				} else {
					pl.Commands = append(pl.Commands, "# web VS Code: https://"+p.Domains[0]+" (site, password)")
				}
			}
			return pl, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			a := svc(env)
			var p devParams
			json.Unmarshal(raw, &p)
			p.defaults()
			if err := p.check(); err != nil {
				return nil, nil, err
			}
			sshP, webP, err := a.devPorts(env.Ctx, env.Host, p.SSHPort, p.WebPort)
			if err != nil {
				return nil, nil, err
			}
			s := devSpec{Name: p.Name, Source: p.Source, GitURL: p.GitURL, GitRef: p.GitRef, EnvMode: p.Env, Preset: p.Preset,
				CPUs: p.CPUs, MemoryMB: p.MemoryMB, Code: *p.Code, Access: p.Access, Domains: p.Domains, SSH: *p.SSH,
				SSHPort: sshP, WebPort: webP, OwnerID: actorUID(env), Claude: *p.Claude, NoLAN: *p.NoLAN}
			if s.Claude {
				s.ClaudeMode, s.ClaudeAuth = "auto", "login"
				env.Log("::step 0 claude")
				if err := agentOp(env, "/v1/dev/claude/install", nil); err != nil {
					return nil, nil, err
				}
			}
			if s.Code {
				env.Log("::step 0 tools")
				if err := agentOp(env, "/v1/dev/tools/install", nil); err != nil {
					return nil, nil, err
				}
			}
			env.Log("::step 1 folder")
			var prep struct {
				Dir          string `json:"dir"`
				Devcontainer string `json:"devcontainer"`
				DevPath      string `json:"devcontainer_path"`
				Made         bool   `json:"made"`
			}
			if err := a.streamAgent(env, env.Host, "/v1/dev/prepare", map[string]string{"name": p.Name, "source": p.Source, "git_url": p.GitURL, "git_ref": p.GitRef, "dir": p.Dir}, &prep); err != nil {
				return nil, nil, err
			}
			s.Dir, s.Made = prep.Dir, prep.Made
			devResolveEnv(&s, prep.DevPath, prep.Devcontainer, env.Log)
			sealed, err := a.seal([]byte(randomPassword(16)))
			if err != nil {
				return nil, nil, err
			}
			b, _ := json.Marshal(s)
			if _, err := a.db.Exec(`INSERT INTO dev_workspaces(host, name, spec, secret_enc, created_by, created_at) VALUES(?,?,?,?,?,?)`,
				env.Host, s.Name, string(b), sealed, env.ActorName, time.Now().Unix()); err != nil {
				return nil, nil, err
			}
			undo := &change.UndoRecipe{Kind: "dev.delete", Params: mustJSON(map[string]any{"name": s.Name})}
			fail := func(err error) (any, *change.UndoRecipe, error) {
				// keep the record: the workspace page shows it and delete cleans up
				return nil, undo, err
			}
			if err := a.devState(env, s, s.Made, true); err != nil {
				return fail(err)
			}
			if err := a.devApplyCompose(env, s, true, false); err != nil {
				return fail(err)
			}
			a.relate(env.Host, change.Object{Type: "host", ID: env.Host}, devObj(s.Name, env.Host), "manages", "module")
			res := map[string]any{"name": s.Name, "ssh_port": s.SSHPort, "web_port": s.WebPort, "devcontainer": s.DevPath, "notes": s.Env.Notes}
			if s.Code && s.Access == "public" {
				env.Log("::step 4 site")
				if err := a.devSite(env, &s); err != nil {
					env.Log("site: %v", err)
					res["site_error"] = errs.From(err).Code
				}
				a.saveDev(env.Host, s)
			}
			return res, undo, nil
		},
	})

	change.Register(change.Kind{
		Name: "dev.delete", Perm: "dev.use", Scope: "dev:write", Title: "kind.dev.delete",
		Params: change.ParamSchema{Props: map[string]change.Prop{
			"name":  {Type: "string"},
			"files": {Type: "boolean", Desc: "also move the project folder to the trash (only folders Berth made); otherwise it stays"}}, Required: []string{"name"}},
		CLI: "berth dev delete {{.name}}",
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			a := svc(env)
			var p struct {
				Name  string
				Files bool
			}
			json.Unmarshal(raw, &p)
			s, err := a.devWorkspace(env.Host, p.Name)
			if err != nil {
				return change.Plan{}, err
			}
			pl := change.Plan{Summary: "plan.dev.delete", SummaryP: map[string]any{"name": s.Name, "dir": s.Dir}, Risk: change.RiskMedium,
				Undoable: !p.Files, Affected: []change.Object{devObj(s.Name, env.Host)},
				Commands: []string{"docker rm -f " + devContainer(s.Name)}}
			for _, st := range a.devSites(env.Host, s.Name) {
				pl.Commands = append(pl.Commands, "delete site "+st.Primary())
				pl.Affected = append(pl.Affected, change.Object{Type: "site", ID: fmt.Sprint(st.ID), Name: st.Primary(), Host: env.Host})
			}
			if p.Files && s.Made {
				pl.Summary = "plan.dev.delete_files"
				pl.Risk = change.RiskHigh
				pl.Commands = append(pl.Commands, "trash "+s.Dir+"   # restorable from the files trash", "docker volume rm "+devProject(s.Name)+"_home")
			} else {
				pl.Commands = append(pl.Commands, "# kept: "+s.Dir+" and the home folder (volume "+devProject(s.Name)+"_home)")
			}
			return pl, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			a := svc(env)
			var p struct {
				Name  string
				Files bool
			}
			json.Unmarshal(raw, &p)
			s, err := a.devWorkspace(env.Host, p.Name)
			if err != nil {
				return nil, nil, err
			}
			// its Claude tasks stop first (queued ones do not start)
			a.db.Exec(`UPDATE claude_tasks SET status='canceled', finished_at=? WHERE host=? AND ws=? AND status='queued'`, time.Now().Unix(), env.Host, s.Name)
			for _, t := range a.claudeTasks(env.Host, s.Name, 5) {
				claudeRun.mu.Lock()
				c := claudeRun.cancel[t.ID]
				claudeRun.mu.Unlock()
				if c != nil {
					c()
				}
			}
			k, _ := change.Get("site.delete")
			for _, st := range a.devSites(env.Host, s.Name) {
				ref, _ := json.Marshal(map[string]int64{"id": st.ID})
				if _, _, err := k.Apply(env, ref); err != nil {
					env.Log("site %s: %v", st.Primary(), err)
				}
			}
			a.call(env.Ctx, env.Host, "POST", "/v1/dev/"+s.Name+"/guard", map[string]bool{"on": false}, nil)
			if err := agentOp(env, "/v1/compose/projects/"+devProject(s.Name)+"/remove", nil); err != nil {
				if e := errs.From(err); e.Code != "compose.not_found" {
					return nil, nil, err
				}
				a.call(env.Ctx, env.Host, "POST", "/v1/docker/containers/"+devContainer(s.Name)+"/remove", map[string]bool{"force": true}, nil)
			}
			if p.Files && s.Made {
				a.call(env.Ctx, env.Host, "POST", "/v1/docker/volumes/remove", map[string]any{"names": []string{devProject(s.Name) + "_home"}}, nil)
				var out map[string]string
				if err := a.call(env.Ctx, env.Host, "POST", "/v1/files/trash", map[string]any{"root": "/", "paths": []string{s.Dir}}, &out); err != nil {
					env.Log("trash %s: %v", s.Dir, err)
				}
				a.call(env.Ctx, env.Host, "DELETE", "/v1/dev/"+s.Name+"/state", nil, nil)
				a.db.Exec(`DELETE FROM claude_tasks WHERE host=? AND ws=?`, env.Host, s.Name) // their snapshots went with the state
			}
			a.db.Exec(`DELETE FROM dev_workspaces WHERE host=? AND name=?`, env.Host, s.Name)
			a.unrelate(env.Host, change.Object{Type: "host", ID: env.Host}, devObj(s.Name, env.Host), "manages")
			if p.Files && s.Made {
				return map[string]any{"name": s.Name}, nil, nil
			}
			// undo: the same workspace on the same folder (and ports, when still free)
			u := map[string]any{"name": s.Name, "source": "dir", "dir": s.Dir, "env": s.EnvMode, "preset": s.Preset, "cpus": s.CPUs,
				"memory_mb": s.MemoryMB, "code": s.Code, "access": s.Access, "domains": s.Domains, "ssh": s.SSH, "ssh_port": s.SSHPort, "web_port": s.WebPort, "claude": s.Claude, "no_lan": s.NoLAN}
			return map[string]any{"name": s.Name}, &change.UndoRecipe{Kind: "dev.create", Params: mustJSON(u)}, nil
		},
	})

	power := func(name, action, inverse string) {
		change.Register(change.Kind{
			Name: name, Perm: "dev.use", Scope: "dev:write", Title: "kind." + name, Params: nameOnly,
			CLI: "berth dev " + action + " {{.name}}",
			Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
				var p struct{ Name string }
				json.Unmarshal(raw, &p)
				if _, err := svc(env).devWorkspace(env.Host, p.Name); err != nil {
					return change.Plan{}, err
				}
				return change.Plan{Summary: "plan." + name, SummaryP: map[string]any{"name": p.Name}, Risk: change.RiskLow, Undoable: inverse != "",
					Commands: []string{"docker " + action + " " + devContainer(p.Name)}, Affected: []change.Object{devObj(p.Name, env.Host)}}, nil
			},
			Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
				a := svc(env)
				var p struct{ Name string }
				json.Unmarshal(raw, &p)
				s, err := a.devWorkspace(env.Host, p.Name)
				if err != nil {
					return nil, nil, err
				}
				if action == "start" { // the container may be gone (removed by hand): make it again
					if err := a.devApplyCompose(env, s, false, false); err != nil {
						return nil, nil, err
					}
				} else if err := a.call(env.Ctx, env.Host, "POST", "/v1/docker/containers/"+devContainer(s.Name)+"/"+action, nil, nil); err != nil {
					return nil, nil, err
				}
				var u *change.UndoRecipe
				if inverse != "" {
					u = &change.UndoRecipe{Kind: inverse, Params: mustJSON(map[string]string{"name": s.Name})}
				}
				return map[string]any{"name": s.Name}, u, nil
			},
		})
	}
	power("dev.start", "start", "dev.stop")
	power("dev.stop", "stop", "dev.start")
	power("dev.restart", "restart", "")

	change.Register(change.Kind{
		Name: "dev.rebuild", Perm: "dev.use", Scope: "dev:write", Title: "kind.dev.rebuild",
		Params: change.ParamSchema{Props: map[string]change.Prop{
			"name":   {Type: "string"},
			"env":    {Type: "string", Enum: []string{"", "auto", "preset"}, Desc: "switch between the project's devcontainer.json and a preset"},
			"preset": {Type: "string"}}, Required: []string{"name"}},
		CLI: "berth dev rebuild {{.name}}", Heavy: true, MemMB: 512,
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			var p struct{ Name, Env, Preset string }
			json.Unmarshal(raw, &p)
			s, err := svc(env).devWorkspace(env.Host, p.Name)
			if err != nil {
				return change.Plan{}, err
			}
			if p.Preset != "" {
				if _, ok := findPreset(p.Preset); !ok {
					return change.Plan{}, errs.New(400, "dev.bad_preset", "preset", p.Preset)
				}
			}
			return change.Plan{Summary: "plan.dev.rebuild", SummaryP: map[string]any{"name": s.Name}, Risk: change.RiskMedium,
				Commands: []string{"# re-read " + firstNonEmpty(s.DevPath, ".devcontainer/devcontainer.json"), "docker compose -p " + devProject(s.Name) + " pull/build, then recreate",
					"# kept: " + s.Dir + " and /home; lost: anything installed elsewhere in the container"},
				Affected: []change.Object{devObj(s.Name, env.Host)}}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			a := svc(env)
			var p struct{ Name, Env, Preset string }
			json.Unmarshal(raw, &p)
			s, err := a.devWorkspace(env.Host, p.Name)
			if err != nil {
				return nil, nil, err
			}
			if p.Env != "" {
				s.EnvMode = p.Env
			}
			if p.Preset != "" {
				s.Preset = p.Preset
			}
			if s.Code {
				if err := agentOp(env, "/v1/dev/tools/install", nil); err != nil {
					return nil, nil, err
				}
			}
			var prep struct {
				Devcontainer string `json:"devcontainer"`
				DevPath      string `json:"devcontainer_path"`
			}
			if err := a.streamAgent(env, env.Host, "/v1/dev/prepare", map[string]string{"name": s.Name, "source": "dir", "dir": s.Dir}, &prep); err != nil {
				return nil, nil, err
			}
			devResolveEnv(&s, prep.DevPath, prep.Devcontainer, env.Log)
			a.saveDev(env.Host, s)
			if err := a.devState(env, s, false, true); err != nil {
				return nil, nil, err
			}
			if err := a.devApplyCompose(env, s, false, true); err != nil {
				return nil, nil, err
			}
			return map[string]any{"name": s.Name, "devcontainer": s.DevPath, "notes": s.Env.Notes}, nil, nil
		},
	})

	setProps := map[string]change.Prop{"name": {Type: "string"}}
	for k, v := range sizeProps {
		setProps[k] = v
	}
	change.Register(change.Kind{
		Name: "dev.set", Perm: "dev.use", Scope: "dev:write", Title: "kind.dev.set", Params: change.ParamSchema{Props: setProps, Required: []string{"name"}},
		CLI: "berth dev set {{.name}}",
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			a := svc(env)
			old, next, err := a.devSetParse(env, raw)
			if err != nil {
				return change.Plan{}, err
			}
			pl := change.Plan{Summary: "plan.dev.set", SummaryP: map[string]any{"name": old.Name, "cpus": next.CPUs, "mem": next.MemoryMB}, Risk: change.RiskLow, Undoable: true,
				Affected: []change.Object{devObj(old.Name, env.Host)},
				Commands: []string{"# the container restarts (files, /home and installed tools in /home stay)"}}
			if next.Code && next.Access == "public" && !(old.Code && old.Access == "public") {
				if err := a.checkDomains(Site{ID: -1, Domains: next.Domains}); err != nil {
					return change.Plan{}, err
				}
				pl.Commands = append(pl.Commands, "create site "+next.Domains[0])
			}
			if next.NoLAN != old.NoLAN {
				pl.Commands = append(pl.Commands, map[bool]string{true: "# internet only: block this LAN, private networks and the server", false: "# allow the LAN again"}[next.NoLAN])
			}
			if next.Claude != old.Claude {
				pl.Commands = append(pl.Commands, map[bool]string{true: "# Claude Code on (one shared copy, about 360 MB the first time)", false: "# Claude Code off (its tasks and logins stay)"}[next.Claude])
			}
			if old.Code && old.Access == "public" && !(next.Code && next.Access == "public") {
				for _, st := range a.devSites(env.Host, old.Name) {
					if st.ID == old.SiteID {
						pl.Commands = append(pl.Commands, "delete site "+st.Primary())
					}
				}
			}
			return pl, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			a := svc(env)
			old, next, err := a.devSetParse(env, raw)
			if err != nil {
				return nil, nil, err
			}
			if next.Code {
				if err := agentOp(env, "/v1/dev/tools/install", nil); err != nil {
					return nil, nil, err
				}
			}
			if next.Claude {
				if err := agentOp(env, "/v1/dev/claude/install", nil); err != nil {
					return nil, nil, err
				}
			}
			if old.Code && old.Access == "public" && !(next.Code && next.Access == "public") && old.SiteID != 0 {
				k, _ := change.Get("site.delete")
				ref, _ := json.Marshal(map[string]int64{"id": old.SiteID})
				if _, _, err := k.Apply(env, ref); err != nil {
					env.Log("site: %v", err)
				}
				next.SiteID = 0
			}
			if next.Code && next.Access == "public" && !(old.Code && old.Access == "public") {
				if err := a.devSite(env, &next); err != nil {
					return nil, nil, err
				}
			}
			a.saveDev(env.Host, next)
			if err := a.devState(env, next, false, false); err != nil {
				return nil, nil, err
			}
			if err := a.devApplyCompose(env, next, false, true); err != nil {
				return nil, nil, err
			}
			u := map[string]any{"name": old.Name, "cpus": old.CPUs, "memory_mb": old.MemoryMB, "code": old.Code, "access": old.Access, "domains": old.Domains, "ssh": old.SSH, "claude": old.Claude, "no_lan": old.NoLAN}
			return map[string]any{"name": next.Name}, &change.UndoRecipe{Kind: "dev.set", Params: mustJSON(u)}, nil
		},
	})
}

// devSetParse: the stored workspace and what it becomes (missing fields keep their value).
func (a *App) devSetParse(env change.Env, raw json.RawMessage) (devSpec, devSpec, error) {
	var p devParams
	json.Unmarshal(raw, &p)
	old, err := a.devWorkspace(env.Host, p.Name)
	if err != nil {
		return old, old, err
	}
	n := old
	if p.CPUs != 0 {
		n.CPUs = p.CPUs
	}
	if p.MemoryMB != 0 {
		n.MemoryMB = p.MemoryMB
	}
	if p.Code != nil {
		n.Code = *p.Code
	}
	if p.SSH != nil {
		n.SSH = *p.SSH
	}
	if p.NoLAN != nil {
		n.NoLAN = *p.NoLAN
	}
	if p.Claude != nil {
		n.Claude = *p.Claude
		if n.Claude && n.ClaudeMode == "" {
			n.ClaudeMode, n.ClaudeAuth = "auto", "login"
		}
	}
	if p.Access != "" {
		n.Access = p.Access
	}
	if p.Domains != nil {
		n.Domains = p.Domains
	}
	chk := devParams{Name: n.Name, Source: "new", Env: "auto", Preset: "base", CPUs: n.CPUs, MemoryMB: n.MemoryMB, Code: &n.Code, Access: n.Access, Domains: n.Domains, SSH: &n.SSH}
	if err := chk.check(); err != nil {
		return old, n, err
	}
	return old, n, nil
}

func actorUID(env change.Env) int64 {
	var uid int64
	fmt.Sscanf(env.ActorID, "%d", &uid)
	return uid
}

func mustJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

// GET /api/v1/hosts/{host}/dev: workspaces with their container state and how to reach
// them, plus what the new-workspace dialog needs.
func (a *App) handleDevList(w http.ResponseWriter, r *http.Request) {
	host := r.PathValue("host")
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	list, _ := a.containers(ctx, host)
	byName := map[string]map[string]any{}
	for _, c := range list {
		byName[c.Name] = map[string]any{"state": c.State, "status": c.Status, "id": c.ID}
	}
	lan := a.devLANIP(ctx, host)
	var tools map[string]string
	a.call(ctx, host, "GET", "/v1/dev/tools", nil, &tools)
	sites, _ := a.sites(host)
	out := []map[string]any{}
	for _, s := range a.devWorkspaces(host) {
		c := byName[devContainer(s.Name)]
		state, id := "missing", ""
		if c != nil {
			state, id = c["state"].(string), c["id"].(string)
		}
		item := map[string]any{"spec": s, "state": state, "container": devContainer(s.Name), "id": id}
		user := s.Env.User
		var st struct {
			User string `json:"user"`
		}
		if a.call(ctx, host, "GET", "/v1/dev/"+s.Name+"/status", nil, &st) == nil && st.User != "" {
			user = st.User
		}
		if user == "" {
			user = "root"
		}
		item["user"] = user
		if s.SSH && lan != "" {
			item["ssh"] = map[string]any{"host": lan, "port": s.SSHPort, "user": user, "alias": "berth-" + s.Name,
				"command": fmt.Sprintf("ssh -p %d %s@%s", s.SSHPort, user, lan), "folder": s.Env.Folder}
		}
		if s.Code {
			if s.Access == "lan" && lan != "" {
				item["web"] = fmt.Sprintf("https://%s:%d/", lan, s.WebPort)
			} else if s.Access == "public" && len(s.Domains) > 0 {
				item["web"] = "https://" + s.Domains[0] + "/"
			}
		}
		var linked []map[string]any
		for _, st := range sites {
			if st.Target.Type == "container" && st.Target.Container == devContainer(s.Name) {
				linked = append(linked, map[string]any{"id": st.ID, "domain": st.Primary(), "port": st.Target.Port, "enabled": st.Enabled})
			}
		}
		item["sites"] = linked
		if s.Claude {
			var running, queued, asking int
			a.db.QueryRow(`SELECT COALESCE(SUM(status='running'),0), COALESCE(SUM(status='queued'),0), COALESCE(SUM(status='asking'),0) FROM claude_tasks WHERE host=? AND ws=?`, host, s.Name).Scan(&running, &queued, &asking)
			item["claude"] = map[string]int{"running": running, "queued": queued, "asking": asking}
		}
		out = append(out, item)
	}
	presets := []map[string]any{}
	for _, p := range devPresets {
		presets = append(presets, map[string]any{"id": p.ID, "image": p.Image, "size_mb": p.SizeMB})
	}
	var host2 struct {
		Cores    int   `json:"cores"`
		MemTotal int64 `json:"mem_total"`
	}
	a.call(ctx, host, "GET", "/v1/system/info", nil, &host2)
	writeJSON(w, 200, map[string]any{"items": out, "presets": presets, "sizes": devSizes, "lan_ip": lan, "tools": tools,
		"my_keys": len(a.sshKeys(currentUser(r).ID)), "root": devRoot, "host": host2})
}
