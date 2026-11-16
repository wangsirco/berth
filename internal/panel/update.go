package panel

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/wangsirco/berth/internal/agent"
	"github.com/wangsirco/berth/internal/change"
	"github.com/wangsirco/berth/internal/errs"
	"github.com/wangsirco/berth/internal/release"
	"github.com/wangsirco/berth/internal/version"
)

// DefaultUpdateURL is where release manifests are published (the domain is not decided
// yet, 方案 §17.1); settings.update.url overrides it.
const DefaultUpdateURL = "https://github.com/wangsirco/berth/releases/latest/download"

type updateInfo struct {
	Current   string            `json:"current"`
	Latest    string            `json:"latest"`
	Available bool              `json:"available"`
	WebOnly   bool              `json:"web_only"`
	Released  int64             `json:"released"`
	Channel   string            `json:"channel"`
	Notes     map[string]string `json:"notes,omitempty"`
	WebBytes  int64             `json:"web_bytes"`
	CoreBytes int64             `json:"core_bytes"`
	Checked   int64             `json:"checked"`
	manifest  *release.Manifest
	base      string
}

// lastCheck: the latest check's answer, for the top-bar badge (no network on page loads).
var lastCheck atomic.Pointer[updateInfo]

var updateHTTP = &http.Client{Timeout: 60 * time.Second}

func fetch(ctx context.Context, url string, max int64) ([]byte, error) {
	req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
	req.Header.Set("User-Agent", "berth-panel/"+version.Version)
	resp, err := updateHTTP.Do(req)
	if err != nil {
		return nil, errs.New(502, "update.unreachable", "detail", err.Error())
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, errs.New(502, "update.unreachable", "detail", resp.Status+" "+url)
	}
	return io.ReadAll(io.LimitReader(resp.Body, max))
}

// semver-ish comparison: 0.1.10 > 0.1.9; 0.2.0-beta.1 < 0.2.0.
func cmpVersion(a, b string) int {
	split := func(v string) ([]int, string) {
		v = strings.TrimPrefix(v, "v")
		core, pre, _ := strings.Cut(v, "-")
		var nums []int
		for _, p := range strings.Split(core, ".") {
			n, _ := strconv.Atoi(p)
			nums = append(nums, n)
		}
		for len(nums) < 3 {
			nums = append(nums, 0)
		}
		return nums, pre
	}
	an, ap := split(a)
	bn, bp := split(b)
	for i := 0; i < 3; i++ {
		if an[i] != bn[i] {
			if an[i] < bn[i] {
				return -1
			}
			return 1
		}
	}
	switch {
	case ap == bp:
		return 0
	case ap == "":
		return 1
	case bp == "":
		return -1
	case ap < bp:
		return -1
	}
	return 1
}

func (a *App) checkUpdate(ctx context.Context) (*updateInfo, error) {
	if version.ReleasePubKey == "" {
		return nil, errs.New(409, "update.disabled")
	}
	base := strings.TrimRight(firstNonEmpty(a.setting("update.url"), DefaultUpdateURL), "/")
	man, err := fetch(ctx, base+"/"+release.ManifestName, 1<<20)
	if err != nil {
		return nil, err
	}
	sig, err := fetch(ctx, base+"/"+release.SigName, 4<<10)
	if err != nil {
		return nil, err
	}
	if err := release.Verify(man, sig, version.ReleasePubKey); err != nil {
		return nil, errs.New(400, "update.bad_signature")
	}
	var m release.Manifest
	if err := json.Unmarshal(man, &m); err != nil {
		return nil, errs.New(400, "update.bad_manifest")
	}
	channel := firstNonEmpty(a.setting("update.channel"), "stable")
	info := &updateInfo{Current: version.Version, Latest: m.Version, Released: m.Released, Channel: channel, Notes: m.Notes,
		WebBytes: m.Web.Asset.Size, CoreBytes: m.Core.Packages[runtime.GOARCH].Size, Checked: time.Now().Unix(), manifest: &m, base: base}
	defer lastCheck.Store(info)
	if channel == "stable" && strings.Contains(m.Version, "-") {
		return info, nil // beta builds are not offered on the stable channel
	}
	info.Available = version.Version == "dev" || strings.HasPrefix(version.Version, "0.0.0") || cmpVersion(m.Version, version.Version) > 0
	info.WebOnly = info.Available && version.CoreHash != "" && m.Core.Hash == version.CoreHash
	return info, nil
}

// download fetches asset into dir and checks its size and sha256.
func download(ctx context.Context, base string, as release.Asset, dir string) (string, error) {
	b, err := fetch(ctx, base+"/"+as.Name, 512<<20)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	if int64(len(b)) != as.Size || hex.EncodeToString(sum[:]) != strings.ToLower(as.SHA256) {
		return "", errs.New(400, "update.bad_checksum")
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", err
	}
	p := filepath.Join(dir, filepath.Base(as.Name))
	return p, os.WriteFile(p, b, 0o640)
}

func (a *App) webDir() string { return filepath.Join(a.cfg.DataDir, "web") }

// loadWebOverride serves a hot-swapped UI if it was made for this core.
func (a *App) loadWebOverride() {
	b, err := os.ReadFile(filepath.Join(a.webDir(), ".core"))
	if err != nil || version.CoreHash == "" || strings.TrimSpace(string(b)) != version.CoreHash {
		return
	}
	a.web = os.DirFS(a.webDir())
}

func init() {
	change.Register(change.Kind{
		Name: "panel.update", Perm: "settings.manage", Scope: "settings:write", Title: "kind.panel.update", Heavy: true, MemMB: 128,
		Params: change.ParamSchema{Props: map[string]change.Prop{"version": {Type: "string"}}, Required: []string{"version"}},
		CLI:    "berth panel update {{.version}}",
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			var p struct{ Version string }
			json.Unmarshal(raw, &p)
			info, err := svc(env).checkUpdate(env.Ctx)
			if err != nil {
				return change.Plan{}, err
			}
			if !info.Available || info.Latest != p.Version {
				return change.Plan{}, errs.New(409, "update.not_available", "version", p.Version)
			}
			summary := "plan.panel.update"
			if info.WebOnly {
				summary = "plan.panel.update_web"
			}
			return change.Plan{Summary: summary, SummaryP: map[string]any{"from": info.Current, "to": info.Latest}, Risk: change.RiskMedium,
				Affected: []change.Object{{Type: "host", ID: env.Host, Name: "berth"}}}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			a := svc(env)
			env.Log("::step 0 check")
			info, err := a.checkUpdate(env.Ctx)
			if err != nil {
				return nil, nil, err
			}
			m := info.manifest
			if info.WebOnly {
				env.Log("::step 1 download web")
				p, err := download(env.Ctx, info.base, m.Web.Asset, filepath.Join(a.cfg.DataDir, "updates"))
				if err != nil {
					return nil, nil, err
				}
				b, _ := os.ReadFile(p)
				tmp := a.webDir() + ".new"
				_ = os.RemoveAll(tmp)
				if err := release.ExtractWeb(bytes.NewReader(b), tmp); err != nil {
					return nil, nil, err
				}
				_ = os.WriteFile(filepath.Join(tmp, ".core"), []byte(version.CoreHash), 0o640)
				_ = os.RemoveAll(a.webDir())
				if err := os.Rename(tmp, a.webDir()); err != nil {
					return nil, nil, err
				}
				a.web = os.DirFS(a.webDir())
				env.Log("web UI %s is live (no restart)", m.Version)
				return map[string]string{"version": m.Version, "mode": "web"}, nil, nil
			}
			as, ok := m.Core.Packages[runtime.GOARCH]
			if !ok {
				return nil, nil, errs.New(409, "update.no_package", "arch", runtime.GOARCH)
			}
			env.Log("::step 1 download %s", as.Name)
			p, err := download(env.Ctx, info.base, as, filepath.Join(a.cfg.DataDir, "updates"))
			if err != nil {
				return nil, nil, err
			}
			env.Log("::step 2 install")
			var out map[string]any
			if err := a.call(env.Ctx, env.Host, "POST", "/v1/self/update", map[string]string{"tarball": p, "sha256": as.SHA256, "from": version.Version, "to": m.Version}, &out); err != nil {
				return nil, nil, err
			}
			env.Log("installed %v; the services restart in a moment, and the update is rolled back automatically if the new panel does not come up within 60 s", out["installed"])
			return out, nil, nil
		},
	})
	change.Register(change.Kind{
		Name: "panel.web_reset", Perm: "settings.manage", Scope: "settings:write", Title: "kind.panel.web_reset",
		CLI: "berth panel web-reset",
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			if !svc(env).webOverridden() {
				return change.Plan{}, errs.New(409, "update.no_web_override")
			}
			return change.Plan{Summary: "plan.panel.web_reset", SummaryP: map[string]any{"version": version.Version}, Risk: change.RiskLow,
				Affected: []change.Object{{Type: "host", ID: env.Host, Name: "berth"}}}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			a := svc(env)
			if err := os.RemoveAll(a.webDir()); err != nil {
				return nil, nil, err
			}
			a.web = a.builtinWeb
			env.Log("serving the web UI built into berth-panel %s again", version.Version)
			return map[string]string{"version": version.Version}, nil, nil
		},
	})
	for _, k := range []string{"update.auto", "update.check"} {
		settingDefs[k] = settingDef{validate: func(v string) error {
			if v != "" && v != "0" && v != "1" {
				return errs.New(400, "params.enum", "name", "value", "allowed", "0, 1")
			}
			return nil
		}}
	}
	settingDefs["update.channel"] = settingDef{validate: func(v string) error {
		if v != "" && v != "stable" && v != "beta" {
			return errs.New(400, "params.enum", "name", "channel", "allowed", "stable, beta")
		}
		return nil
	}}
	settingDefs["update.window"] = settingDef{validate: func(v string) error {
		if v != "" && !windowRe.MatchString(v) {
			return errs.New(400, "update.bad_window")
		}
		return nil
	}}
	settingDefs["update.url"] = settingDef{validate: func(v string) error {
		if v != "" && !strings.HasPrefix(v, "https://") {
			return errs.New(400, "update.bad_url")
		}
		return nil
	}}
	routeHooks = append(routeHooks, func(a *App, mux *http.ServeMux) {
		mux.HandleFunc("GET /api/v1/update/check", a.superOnly(func(w http.ResponseWriter, r *http.Request) {
			info, err := a.checkUpdate(r.Context())
			if err != nil {
				a.fail(w, r, err)
				return
			}
			writeJSON(w, 200, info)
		}))
		// what the page and the top bar show without going to the network
		mux.HandleFunc("GET /api/v1/update/status", a.superOnly(func(w http.ResponseWriter, r *http.Request) {
			out := map[string]any{"current": version.Version, "enabled": version.ReleasePubKey != "", "web_override": a.webOverridden(),
				"channel": firstNonEmpty(a.setting("update.channel"), "stable"), "auto": a.setting("update.auto") == "1", "window": a.setting("update.window"),
				"check": a.setting("update.check") != "0", "url": a.setting("update.url"), "default_url": DefaultUpdateURL}
			if l := lastCheck.Load(); l != nil {
				out["last"] = l
			}
			writeJSON(w, 200, out)
		}))
	})
}

// reportUpdateResult records the watchdog's outcome after a restart.
func (a *App) reportUpdateResult() {
	p := filepath.Join(a.cfg.DataDir, "update-result.json")
	b, err := os.ReadFile(p)
	if err != nil {
		return
	}
	_ = os.Remove(p)
	var r agent.UpdateResult
	if json.Unmarshal(b, &r) != nil {
		return
	}
	summary, risk := "plan.panel.updated", change.RiskLow
	if r.RolledBack {
		summary, risk = "plan.panel.rolled_back", change.RiskHigh
	}
	_, _ = a.pipe.Record(change.Actor{Type: "system", ID: "update", Name: "berth", Via: "api"}, "local", "panel.update_result", r,
		change.Plan{Summary: summary, SummaryP: map[string]any{"from": r.From, "to": r.To, "detail": r.Detail}, Risk: risk})
}

var windowRe = regexp.MustCompile(`^([01]?\d|2[0-3]):[0-5]\d-([01]?\d|2[0-3]):[0-5]\d$`)

// webOverridden: a downloaded UI is being served instead of the one built in.
func (a *App) webOverridden() bool {
	_, err := os.Stat(filepath.Join(a.webDir(), ".core"))
	return err == nil
}

// autoUpdate runs once an hour: checks every 12 hours (unless turned off) so the top bar can
// say a new version is out, and updates inside the configured window ("03:00-05:00").
func (a *App) autoUpdate(ctx context.Context) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	var checked time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			if version.ReleasePubKey != "" && a.setting("update.check") != "0" && now.Sub(checked) >= 12*time.Hour {
				checked = now
				_, _ = a.checkUpdate(ctx)
			}
			if a.setting("update.auto") != "1" || !inWindow(a.setting("update.window"), now) {
				continue
			}
			info, err := a.checkUpdate(ctx)
			if err != nil || !info.Available {
				continue
			}
			raw, _ := json.Marshal(map[string]string{"version": info.Latest})
			_, _ = a.pipe.Run(ctx, change.Actor{Type: "schedule", ID: "auto-update", Name: "auto-update", Via: "api"}, "local", "panel.update", raw)
		}
	}
}

func inWindow(w string, now time.Time) bool {
	from, to, ok := strings.Cut(w, "-")
	if !ok {
		return false
	}
	parse := func(s string) int {
		h, m, _ := strings.Cut(strings.TrimSpace(s), ":")
		hh, _ := strconv.Atoi(h)
		mm, _ := strconv.Atoi(m)
		return hh*60 + mm
	}
	f, t, n := parse(from), parse(to), now.Hour()*60+now.Minute()
	if f <= t {
		return n >= f && n < t
	}
	return n >= f || n < t // window over midnight
}
