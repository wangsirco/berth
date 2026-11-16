package panel

// Plugins (插件, api 1): packages from the plugin market, an upload or a URL. Installing shows
// what a plugin is, who signed it and everything it asks for — Berth API scopes and what it may
// do on the system — and runs it either as a sandboxed container or as a native systemd service
// (its own user and a read-only system, or root when the installer agreed and the package is
// signed). Installed plugins are rows of the extensions table, so their pages, tokens and events
// work like extensions'.

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"mime"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/wangsirco/berth/internal/agent"
	"github.com/wangsirco/berth/internal/change"
	"github.com/wangsirco/berth/internal/errs"
	"github.com/wangsirco/berth/internal/plugin"
)

// DefaultPluginIndex lists the plugins people can install from the market.
const DefaultPluginIndex = "https://raw.githubusercontent.com/wangsirco/berth-plugins/main/index.json"

var pkgRefRe = regexp.MustCompile(`^[0-9a-f]{64}$`)

func (a *App) pluginDir() string { return filepath.Join(a.cfg.DataDir, "plugins") }

func (a *App) pkgPath(ref string) string { return filepath.Join(a.pluginDir(), ref+".berthplugin") }

// storePkg keeps a package under its sha256 and reads it.
func (a *App) storePkg(data []byte) (*plugin.Package, error) {
	p, err := plugin.Read(data, validScope)
	if err != nil {
		return nil, pluginErr(err)
	}
	if err := os.MkdirAll(a.pluginDir(), 0o750); err != nil {
		return nil, err
	}
	if err := os.WriteFile(a.pkgPath(p.SHA256), data, 0o640); err != nil {
		return nil, err
	}
	return p, nil
}

func (a *App) loadPkg(ref string) (*plugin.Package, error) {
	if !pkgRefRe.MatchString(ref) {
		return nil, errs.New(400, "plugin.bad_ref")
	}
	b, err := os.ReadFile(a.pkgPath(ref))
	if err != nil {
		return nil, errs.New(404, "plugin.pkg_gone")
	}
	p, err := plugin.Read(b, validScope)
	if err != nil {
		return nil, pluginErr(err)
	}
	return p, nil
}

func pluginErr(err error) error {
	if e, ok := err.(*plugin.Error); ok {
		return errs.New(400, "plugin.bad_package", "field", e.Field, "detail", e.Detail)
	}
	return err
}

func (a *App) pinnedSigner(id string) string {
	var s string
	a.db.QueryRow(`SELECT signer FROM plugin_keys WHERE id=?`, id).Scan(&s)
	return s
}

// ---- the market ----

type marketItem struct {
	ID          string      `json:"id"`
	Name        plugin.Text `json:"name"`
	Description plugin.Text `json:"description"`
	Author      string      `json:"author"`
	Version     string      `json:"version"`
	URL         string      `json:"url"`
	SHA256      string      `json:"sha256"`
	Signer      string      `json:"signer,omitempty"`
	Icon        string      `json:"icon,omitempty"` // https URL
	Runtime     string      `json:"runtime"`
	RunAs       string      `json:"run_as,omitempty"`
	Homepage    string      `json:"homepage,omitempty"`
	Tags        []string    `json:"tags,omitempty"`
}

var market struct {
	sync.Mutex
	at    time.Time
	url   string
	items []marketItem
}

func (a *App) marketIndex(ctx context.Context, force bool) ([]marketItem, error) {
	u := firstNonEmpty(a.setting("plugins.index"), DefaultPluginIndex)
	market.Lock()
	defer market.Unlock()
	if !force && market.url == u && time.Since(market.at) < 30*time.Minute {
		return market.items, nil
	}
	b, err := fetch(ctx, u, 4<<20)
	if err != nil {
		return nil, errs.New(502, "plugin.market_unreachable", "detail", err.Error())
	}
	var idx struct {
		Plugins []marketItem `json:"plugins"`
	}
	if err := json.Unmarshal(b, &idx); err != nil {
		return nil, errs.New(502, "plugin.market_bad")
	}
	out := []marketItem{}
	for _, it := range idx.Plugins {
		if plugin.IDRe.MatchString(it.ID) && strings.HasPrefix(it.URL, "https://") && pkgRefRe.MatchString(strings.ToLower(it.SHA256)) {
			out = append(out, it)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	// icons come inline: the panel's pages load images only from itself
	for i := range out {
		if strings.HasPrefix(out[i].Icon, "https://") {
			if b, err := fetch(ctx, out[i].Icon, 256<<10); err == nil {
				out[i].Icon = dataURL(path.Base(strings.SplitN(out[i].Icon, "?", 2)[0]), b)
			} else {
				out[i].Icon = ""
			}
		} else {
			out[i].Icon = ""
		}
	}
	market.at, market.url, market.items = time.Now(), u, out
	return out, nil
}

// ---- what a package is, before installing it ----

type inspectView struct {
	Ref         string           `json:"ref"`
	Manifest    *plugin.Manifest `json:"manifest"`
	Size        int64            `json:"size"`
	Signed      bool             `json:"signed"`
	SigOK       bool             `json:"sig_ok"`
	Fingerprint string           `json:"fingerprint,omitempty"`
	PinnedFP    string           `json:"pinned_fingerprint,omitempty"`
	KeyChanged  bool             `json:"key_changed"`
	Installed   string           `json:"installed_version,omitempty"`
	MoreScopes  []string         `json:"more_scopes,omitempty"` // an update that asks for more
	ArchOK      bool             `json:"arch_ok"`
	Icon        string           `json:"icon,omitempty"` // data: URL
	Screenshots []string         `json:"screenshots,omitempty"`
	FromMarket  bool             `json:"from_market"`
	RootBlocked string           `json:"root_blocked,omitempty"` // why a root plugin cannot be installed
}

func dataURL(name string, b []byte) string {
	t := mime.TypeByExtension(path.Ext(name))
	if t == "" || (!strings.HasPrefix(t, "image/")) || len(b) > 2<<20 {
		return ""
	}
	return "data:" + t + ";base64," + base64.StdEncoding.EncodeToString(b)
}

func (a *App) inspect(p *plugin.Package) inspectView {
	m := p.Manifest
	v := inspectView{Ref: p.SHA256, Manifest: m, Size: p.Size, Signed: p.Signed, SigOK: p.SigOK, ArchOK: m.Runtime.Type == "container" || m.Runtime.Binary[runtime.GOARCH] != ""}
	if p.Signed {
		v.Fingerprint = plugin.Fingerprint(p.Signer)
	}
	if pin := a.pinnedSigner(m.ID); pin != "" {
		v.PinnedFP = plugin.Fingerprint(pin)
		v.KeyChanged = !p.SigOK || p.Signer != pin
	}
	if e, ok := a.extension(m.ID); ok {
		v.Installed = e.Version
		for _, s := range m.Permissions {
			if !contains(e.Scopes, s) {
				v.MoreScopes = append(v.MoreScopes, s)
			}
		}
	}
	if m.Icon != "" {
		v.Icon = dataURL(m.Icon, p.Files[path.Clean(m.Icon)])
	}
	for i, s := range m.Screenshots {
		if i < 6 {
			if u := dataURL(s, p.Files[path.Clean(s)]); u != "" {
				v.Screenshots = append(v.Screenshots, u)
			}
		}
	}
	v.RootBlocked = rootBlocked(p, a.pinnedSigner(m.ID))
	return v
}

// rootBlocked: a plugin that runs as root must be signed, by the key this plugin was first seen with.
func rootBlocked(p *plugin.Package, pin string) string {
	if p.Manifest.Runtime.Type != "native" || p.Manifest.Runtime.RunAs != "root" {
		return ""
	}
	if !p.Signed || !p.SigOK {
		return "plugin.root_unsigned"
	}
	if pin != "" && pin != p.Signer {
		return "plugin.root_key_changed"
	}
	return ""
}

// ---- installed ----

type pluginView struct {
	extension
	Kind        string            `json:"kind"` // container | native | extension (an older berth-extension.yaml)
	Plugin      *plugin.Manifest  `json:"plugin,omitempty"`
	Fingerprint string            `json:"fingerprint,omitempty"`
	Settings    map[string]string `json:"settings,omitempty"` // secrets left out
	HasPage     bool              `json:"has_page"`
}

func (a *App) pluginRow(id string) (kind, signer, pkg string, m *plugin.Manifest, settings map[string]string) {
	var pj string
	var enc []byte
	a.db.QueryRow(`SELECT kind, plugin, signer, pkg, settings_enc FROM extensions WHERE id=?`, id).Scan(&kind, &pj, &signer, &pkg, &enc)
	if pj != "" {
		m = &plugin.Manifest{}
		json.Unmarshal([]byte(pj), m)
	}
	settings = map[string]string{}
	if len(enc) > 0 {
		if b, err := a.open(enc); err == nil {
			json.Unmarshal(b, &settings)
		}
	}
	return
}

func (a *App) plugins(ctx context.Context) []pluginView {
	exts := a.extensions()
	running := a.extRunning(ctx)
	natives := []string{}
	out := []pluginView{}
	for _, e := range exts {
		kind, signer, _, m, settings := a.pluginRow(e.ID)
		v := pluginView{extension: e, Kind: kind, Plugin: m, HasPage: true}
		if m == nil {
			v.Kind = "extension"
		} else {
			if signer != "" {
				v.Fingerprint = plugin.Fingerprint(signer)
			}
			v.HasPage = m.Runtime.Type == "container" || m.Runtime.HasPage
			v.Settings = map[string]string{}
			for _, s := range m.Settings {
				if s.Type != "secret" {
					v.Settings[s.Key] = settings[s.Key]
				}
			}
		}
		if v.Kind == "native" {
			natives = append(natives, e.ID)
		} else {
			v.Running = running["ext-"+e.ID]
		}
		out = append(out, v)
	}
	if len(natives) > 0 {
		st := map[string]bool{}
		a.call(ctx, "local", "POST", "/v1/plugins/native/status", map[string]any{"ids": natives}, &st)
		for i := range out {
			if out[i].Kind == "native" {
				out[i].Running = st[out[i].ID]
			}
		}
	}
	return out
}

// ---- install ----

type pluginInstall struct {
	Ref      string            `json:"ref"`
	Grant    []string          `json:"grant"`
	Settings map[string]string `json:"settings"`
}

func settingEnv(m *plugin.Manifest, vals map[string]string) (string, error) {
	var b strings.Builder
	for _, s := range m.Settings {
		v, ok := vals[s.Key]
		if !ok {
			v = s.Default
		}
		if s.Required && v == "" {
			return "", errs.New(400, "plugin.setting_required", "name", s.Label.In("en"))
		}
		if strings.ContainsAny(v, "\n\r") {
			return "", errs.New(400, "plugin.setting_bad", "name", s.Label.In("en"))
		}
		if s.Type == "number" && v != "" {
			if _, err := strconv.ParseFloat(v, 64); err != nil {
				return "", errs.New(400, "plugin.setting_bad", "name", s.Label.In("en"))
			}
		}
		if s.Type == "select" && v != "" && !contains(s.Options, v) {
			return "", errs.New(400, "plugin.setting_bad", "name", s.Label.In("en"))
		}
		fmt.Fprintf(&b, "BERTH_SETTING_%s=%s\n", strings.ToUpper(s.Key), v)
	}
	return b.String(), nil
}

// pluginCompose: a container plugin's compose file. Same walls as extensions: no new
// privileges, a memory cap, the page on loopback only.
func pluginCompose(m *plugin.Manifest, port int) string {
	var b strings.Builder
	b.WriteString("# Berth plugin " + m.ID + " " + m.Version + " (managed by Berth; reinstall to change)\nservices:\n  app:\n")
	if m.Runtime.Image != "" {
		fmt.Fprintf(&b, "    image: %s\n", m.Runtime.Image)
	} else {
		fmt.Fprintf(&b, "    build: %s\n    image: berth-plugin-%s:%s\n", agent.ContainerPluginDir(m.ID)+"/"+path.Clean(strings.TrimPrefix(m.Runtime.Build, "./")), m.ID, m.Version)
	}
	b.WriteString("    restart: unless-stopped\n    env_file: [.env]\n")
	if m.System.HostNetwork {
		fmt.Fprintf(&b, "    network_mode: host\n    environment:\n      PORT: \"%d\"\n", port) // it listens on 127.0.0.1:$PORT
	} else {
		fmt.Fprintf(&b, "    ports: [\"127.0.0.1:%d:%d\"]\n    extra_hosts: [\"host.docker.internal:host-gateway\"]\n", port, m.Runtime.Port)
		for _, p := range m.System.Listen {
			fmt.Fprintf(&b, "    # listens for others on %d\n", p)
		}
		if len(m.System.Listen) > 0 {
			b.WriteString("    expose: [")
			for i, p := range m.System.Listen {
				if i > 0 {
					b.WriteString(", ")
				}
				fmt.Fprintf(&b, "\"%d\"", p)
			}
			b.WriteString("]\n")
		}
	}
	b.WriteString("    volumes: [\"./berth-ca.crt:/etc/berth/ca.crt:ro\", \"./data:/data\"]\n")
	mem := m.Runtime.MemMB
	if mem == 0 {
		mem = 256
	}
	fmt.Fprintf(&b, "    security_opt: [\"no-new-privileges:true\"]\n    cap_drop: [ALL]\n    mem_limit: %dm\n", mem)
	if m.Runtime.CPUs > 0 {
		fmt.Fprintf(&b, "    cpus: %g\n", m.Runtime.CPUs)
	}
	return b.String()
}

// pluginLines: everything an install will do, for the preview.
func pluginLines(p *plugin.Package, grant []string) []string {
	m := p.Manifest
	var l []string
	for _, s := range m.Permissions {
		mark := "✗ "
		if contains(grant, s) {
			mark = "✓ "
		}
		l = append(l, mark+"berth "+s)
	}
	if len(m.System.Packages) > 0 {
		l = append(l, "install packages: "+strings.Join(m.System.Packages, " "))
	}
	switch m.Runtime.Type {
	case "container":
		if m.Runtime.Image != "" {
			l = append(l, "docker pull "+m.Runtime.Image)
		} else {
			l = append(l, "docker build "+agent.ContainerPluginDir(m.ID))
		}
		l = append(l, "docker compose -p ext-"+m.ID+" up -d   # no-new-privileges, cap_drop ALL")
		if m.System.HostNetwork {
			l = append(l, "network_mode: host")
		}
	case "native":
		if m.Runtime.RunAs == "root" {
			l = append(l, "!! runs as root")
		} else {
			l = append(l, "useradd --system "+plugin.User(m.ID))
		}
		l = append(l, "unpack to "+path.Join(plugin.NativeRoot, m.ID, "app"), "write /etc/systemd/system/"+plugin.Unit(m.ID)+".service", "systemctl enable --now "+plugin.Unit(m.ID))
		for _, c := range m.System.Capabilities {
			l = append(l, "capability CAP_"+strings.ToUpper(c))
		}
		for _, w := range m.System.WritePaths {
			l = append(l, "may write "+w)
		}
		for _, r := range m.System.ReadPaths {
			l = append(l, "may read "+r)
		}
	}
	for _, p := range m.System.Listen {
		l = append(l, "listens on port "+strconv.Itoa(p))
	}
	if len(m.Events) > 0 {
		l = append(l, "events: "+strings.Join(m.Events, ", "))
	}
	return l
}

func (a *App) pluginRemove(env change.Env, id string, keepData bool) error {
	e, ok := a.extension(id)
	if !ok {
		return errs.New(404, "ext.not_found")
	}
	kind, _, _, _, _ := a.pluginRow(id)
	a.revokeToken(e.tokenID)
	if kind == "native" {
		if err := a.call(env.Ctx, "local", "POST", "/v1/plugins/native/remove", map[string]any{"id": id, "keep_data": keepData}, nil); err != nil {
			env.Log("removing the service: %v", err)
		}
	} else if err := agentOp(env, "/v1/compose/projects/ext-"+id+"/remove", nil); err != nil {
		env.Log("removing the container: %v", err)
	}
	_, err := a.db.Exec(`DELETE FROM extensions WHERE id=?`, id)
	return err
}

func (a *App) pluginInstall(env change.Env, in pluginInstall) (map[string]any, error) {
	p, err := a.loadPkg(in.Ref)
	if err != nil {
		return nil, err
	}
	m := p.Manifest
	if why := rootBlocked(p, a.pinnedSigner(m.ID)); why != "" {
		return nil, errs.New(403, why)
	}
	envSettings, err := settingEnv(m, in.Settings)
	if err != nil {
		return nil, err
	}
	uid, _ := strconv.ParseInt(env.ActorID, 10, 64)
	owner, err := a.userByID(uid)
	if err != nil || env.ActorType != "user" {
		return nil, errs.New(403, "ext.person_only")
	}
	if len(m.System.Packages) > 0 {
		env.Log("installing %s", strings.Join(m.System.Packages, " "))
		if _, err := a.subApply(env, "pkg.install", map[string]any{"names": m.System.Packages}); err != nil {
			return nil, err
		}
	}
	upgrade := false
	if _, ok := a.extension(m.ID); ok {
		upgrade = true
		env.Log("replacing the installed version (its data is kept)")
		if err := a.pluginRemove(env, m.ID, true); err != nil {
			return nil, err
		}
	}
	port := a.freeExtPort()
	if port == 0 {
		return nil, errs.New(503, "ext.no_port")
	}
	tok, tid, err := a.createToken(owner, tokenSpec{Kind: "ext", Name: m.Name.In("en"), Scopes: orEmpty(in.Grant), Note: "plugin " + m.ID})
	if err != nil {
		return nil, err
	}
	fail := func(err error) (map[string]any, error) {
		a.revokeToken(tid)
		return nil, err
	}
	base := fmt.Sprintf("BERTH_TOKEN=%s\nBERTH_PLUGIN=%s\nBERTH_PLUGIN_VERSION=%s\n", tok, m.ID, m.Version)
	switch m.Runtime.Type {
	case "container":
		project := "ext-" + m.ID
		if m.Runtime.Build != "" {
			if err := a.call(env.Ctx, "local", "POST", "/v1/plugins/unpack", map[string]any{"file": a.pkgPath(p.SHA256), "id": m.ID, "kind": "container"}, nil); err != nil {
				return fail(err)
			}
		}
		url := "https://host.docker.internal:" + a.panelPort()
		if m.System.HostNetwork {
			url = "https://127.0.0.1:" + a.panelPort()
		}
		envFile := "BERTH_URL=" + url + "\nBERTH_CA_FILE=/etc/berth/ca.crt\nBERTH_DATA=/data\n" + base + envSettings
		if err := a.call(env.Ctx, "local", "PUT", "/v1/compose/projects/"+project+"/files", map[string]any{"compose": pluginCompose(m, port), "env": envFile, "create": true}, nil); err != nil {
			return fail(err)
		}
		if err := a.call(env.Ctx, "local", "POST", "/v1/files/write", map[string]any{"root": "/", "path": "/srv/berth/stacks/" + project + "/berth-ca.crt", "content": a.caPEM}, nil); err != nil {
			return fail(err)
		}
		env.Log("building and starting %s", project)
		if err := agentOp(env, "/v1/compose/projects/"+project+"/up", nil); err != nil {
			return fail(err)
		}
	case "native":
		if m.Runtime.Binary[runtime.GOARCH] == "" {
			return fail(errs.New(409, "plugin.no_build", "arch", runtime.GOARCH))
		}
		if err := a.call(env.Ctx, "local", "POST", "/v1/plugins/unpack", map[string]any{"file": a.pkgPath(p.SHA256), "id": m.ID, "kind": "native"}, nil); err != nil {
			return fail(err)
		}
		ca := path.Join(plugin.NativeRoot, "berth-ca.crt")
		if err := a.call(env.Ctx, "local", "POST", "/v1/files/write", map[string]any{"root": "/", "path": ca, "content": a.caPEM}, nil); err != nil {
			return fail(err)
		}
		unit, err := plugin.UnitFile(m, runtime.GOARCH)
		if err != nil {
			return fail(pluginErr(err))
		}
		envFile := fmt.Sprintf("BERTH_URL=https://127.0.0.1:%s\nBERTH_CA_FILE=%s\nBERTH_DATA=%s\nPORT=%d\n", a.panelPort(), ca, path.Join(plugin.NativeRoot, m.ID, "data"), port) + base + envSettings
		env.Log("starting %s", plugin.Unit(m.ID))
		if err := a.call(env.Ctx, "local", "POST", "/v1/plugins/native/install", map[string]any{"id": m.ID, "unit": unit, "env": envFile, "root": m.Runtime.RunAs == "root"}, nil); err != nil {
			return fail(err)
		}
	}
	compat := extManifest{ID: m.ID, Name: m.Name, Version: m.Version, Description: m.Description, Image: m.Runtime.Image, Build: m.Runtime.Build,
		Port: m.Runtime.Port, Permissions: m.Permissions, Events: m.Events, Webhook: m.Webhook}
	if compat.Port == 0 {
		compat.Port = port
	}
	cj, _ := json.Marshal(compat)
	pj, _ := json.Marshal(m)
	sj, _ := json.Marshal(orEmpty(in.Grant))
	setj, _ := json.Marshal(in.Settings)
	senc, _ := a.seal(setj)
	signer := ""
	if p.SigOK {
		signer = p.Signer
	}
	if _, err := a.db.Exec(`INSERT INTO extensions(id, manifest, source, scopes, token_id, port, installed_by, installed_at, kind, plugin, signer, settings_enc, pkg) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		m.ID, string(cj), "plugin:"+p.SHA256, string(sj), tid, port, env.ActorName, time.Now().Unix(), m.Runtime.Type, string(pj), signer, senc, p.SHA256); err != nil {
		return fail(err)
	}
	if signer != "" && a.pinnedSigner(m.ID) == "" {
		a.db.Exec(`INSERT INTO plugin_keys(id, signer, first_seen) VALUES(?,?,?)`, m.ID, signer, time.Now().Unix())
	}
	out := map[string]any{"id": m.ID, "version": m.Version, "port": port, "upgrade": upgrade}
	if m.System.FirewallOpen && len(m.System.Listen) > 0 {
		out["open_ports"] = m.System.Listen // the page offers to open them (firewall changes confirm on their own)
	}
	return out, nil
}

func init() {
	settingDefs["plugins.index"] = settingDef{validate: func(v string) error {
		if v != "" && !strings.HasPrefix(v, "https://") {
			return errs.New(400, "plugin.bad_index")
		}
		return nil
	}}
	uploadExts = append(uploadExts, ".berthplugin")

	routeHooks = append(routeHooks, func(a *App, mux *http.ServeMux) {
		mux.HandleFunc("GET /api/v1/plugins", a.authed("", func(w http.ResponseWriter, r *http.Request) {
			u := currentUser(r)
			list := []pluginView{}
			if u.Role == RoleSuper || u.Can("extensions.use") {
				list = a.plugins(r.Context())
			}
			writeJSON(w, 200, map[string]any{"items": list, "arch": runtime.GOARCH})
		}))
		mux.HandleFunc("GET /api/v1/plugins/market", a.superOnly(func(w http.ResponseWriter, r *http.Request) {
			items, err := a.marketIndex(r.Context(), r.URL.Query().Get("refresh") == "1")
			if err != nil {
				a.fail(w, r, err)
				return
			}
			inst := map[string]string{}
			for _, e := range a.extensions() {
				inst[e.ID] = e.Version
			}
			writeJSON(w, 200, map[string]any{"items": items, "installed": inst, "index": firstNonEmpty(a.setting("plugins.index"), DefaultPluginIndex)})
		}))
		// inspect a package from the market, an upload or a URL: what it is and what it asks for
		mux.HandleFunc("POST /api/v1/plugins/inspect", a.superOnly(func(w http.ResponseWriter, r *http.Request) {
			var in struct {
				Upload string `json:"upload"`
				URL    string `json:"url"`
				SHA256 string `json:"sha256"`
				Market string `json:"market"`
			}
			if err := readJSON(r, &in); err != nil {
				a.fail(w, r, err)
				return
			}
			var data []byte
			fromMarket := false
			switch {
			case in.Upload != "":
				u, err := a.uploadedFile(in.Upload, ".berthplugin")
				if err != nil {
					a.fail(w, r, err)
					return
				}
				data, err = os.ReadFile(u.Path)
				if err != nil {
					a.fail(w, r, err)
					return
				}
				os.Remove(u.Path)
				os.Remove(filepath.Join(a.uploadDir(), u.ID+".json"))
			case in.Market != "":
				items, err := a.marketIndex(r.Context(), false)
				if err != nil {
					a.fail(w, r, err)
					return
				}
				for _, it := range items {
					if it.ID == in.Market {
						in.URL, in.SHA256, fromMarket = it.URL, it.SHA256, true
					}
				}
				if !fromMarket {
					a.fail(w, r, errs.New(404, "plugin.not_in_market"))
					return
				}
				fallthrough
			case in.URL != "":
				if !strings.HasPrefix(in.URL, "https://") {
					a.fail(w, r, errs.New(400, "plugin.bad_url"))
					return
				}
				var err error
				data, err = fetch(r.Context(), in.URL, plugin.MaxPackage)
				if err != nil {
					a.fail(w, r, errs.New(502, "plugin.download_failed", "detail", err.Error()))
					return
				}
				if in.SHA256 != "" {
					sum := sha256.Sum256(data)
					if hex.EncodeToString(sum[:]) != strings.ToLower(in.SHA256) {
						a.fail(w, r, errs.New(400, "plugin.bad_checksum"))
						return
					}
				}
			default:
				a.fail(w, r, errs.New(400, "plugin.no_source"))
				return
			}
			p, err := a.storePkg(data)
			if err != nil {
				a.fail(w, r, err)
				return
			}
			v := a.inspect(p)
			v.FromMarket = fromMarket
			writeJSON(w, 200, v)
		}))
	})

	change.Register(change.Kind{
		Name: "plugin.install", Perm: "extensions.manage", Scope: "settings:write", Title: "kind.plugin.install", Heavy: true, MemMB: 300,
		Redact: []string{"settings"},
		Params: change.ParamSchema{Props: map[string]change.Prop{"ref": {Type: "string", Desc: "sha256 of an inspected package"},
			"grant": {Type: "array", Desc: "Berth scopes to grant (a subset of what it asks for)"}, "settings": {Type: "object"}}, Required: []string{"ref"}},
		CLI: "berth plugin install {{.ref}}",
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			a := svc(env)
			var in pluginInstall
			json.Unmarshal(raw, &in)
			if env.Host != "local" {
				return change.Plan{}, errs.New(400, "ext.local_only")
			}
			p, err := a.loadPkg(in.Ref)
			if err != nil {
				return change.Plan{}, err
			}
			m := p.Manifest
			if why := rootBlocked(p, a.pinnedSigner(m.ID)); why != "" {
				return change.Plan{}, errs.New(403, why)
			}
			for _, s := range in.Grant {
				if !contains(m.Permissions, s) {
					return change.Plan{}, errs.New(400, "ext.grant_extra", "scope", s)
				}
			}
			if _, err := settingEnv(m, in.Settings); err != nil {
				return change.Plan{}, err
			}
			sum := "plan.plugin.install"
			if e, ok := a.extension(m.ID); ok {
				sum = "plan.plugin.upgrade"
				_ = e
			}
			risk := change.RiskHigh
			if m.Runtime.RunAs == "root" {
				sum += "_root"
			}
			return change.Plan{Summary: sum, SummaryP: map[string]any{"name": m.Name.In("en"), "id": m.ID, "version": m.Version, "n": len(in.Grant)}, Risk: risk, Undoable: true,
				Commands: pluginLines(p, in.Grant), Affected: []change.Object{{Type: "extension", ID: m.ID, Name: m.Name.In("en")}}}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			var in pluginInstall
			json.Unmarshal(raw, &in)
			out, err := svc(env).pluginInstall(env, in)
			if err != nil {
				return nil, nil, err
			}
			u, _ := json.Marshal(map[string]any{"id": out["id"]})
			return out, &change.UndoRecipe{Kind: "plugin.remove", Params: u}, nil
		},
	})
	change.Register(change.Kind{
		Name: "plugin.remove", Perm: "extensions.manage", Scope: "settings:write", Title: "kind.plugin.remove", Heavy: true,
		Params: change.ParamSchema{Props: map[string]change.Prop{"id": {Type: "string"}, "keep_data": {Type: "boolean", Desc: "keep a native plugin's data folder"}}, Required: []string{"id"}},
		CLI:    "berth plugin remove {{.id}}",
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			var p struct {
				ID       string
				KeepData bool `json:"keep_data"`
			}
			json.Unmarshal(raw, &p)
			a := svc(env)
			e, ok := a.extension(p.ID)
			if !ok {
				return change.Plan{}, errs.New(404, "ext.not_found")
			}
			kind, _, pkg, _, _ := a.pluginRow(p.ID)
			cmds := []string{"revoke its token"}
			if kind == "native" {
				cmds = append(cmds, "systemctl disable --now "+plugin.Unit(p.ID), "rm "+path.Join(plugin.NativeRoot, p.ID, "app"))
				if !p.KeepData {
					cmds = append(cmds, "rm -r "+path.Join(plugin.NativeRoot, p.ID), "userdel "+plugin.User(p.ID))
				}
			} else {
				cmds = append(cmds, "docker compose -p ext-"+p.ID+" down")
			}
			return change.Plan{Summary: "plan.plugin.remove", SummaryP: map[string]any{"name": e.title("en")}, Risk: change.RiskMedium, Undoable: pkg != "",
				Commands: cmds, Affected: []change.Object{{Type: "extension", ID: p.ID, Name: e.title("en")}}}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			var p struct {
				ID       string
				KeepData bool `json:"keep_data"`
			}
			json.Unmarshal(raw, &p)
			a := svc(env)
			e, _ := a.extension(p.ID)
			_, _, pkg, m, settings := a.pluginRow(p.ID)
			if m != nil { // secrets do not go into the undo record
				for _, s := range m.Settings {
					if s.Type == "secret" {
						delete(settings, s.Key)
					}
				}
			}
			if err := a.pluginRemove(env, p.ID, p.KeepData); err != nil {
				return nil, nil, err
			}
			if pkg == "" {
				return nil, nil, nil
			}
			u, _ := json.Marshal(pluginInstall{Ref: pkg, Grant: e.Scopes, Settings: settings})
			return nil, &change.UndoRecipe{Kind: "plugin.install", Params: u}, nil
		},
	})
}
