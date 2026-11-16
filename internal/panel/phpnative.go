package panel

// Native PHP (R2): installing PHP versions on the host, and keeping each native PHP site's own
// sandboxed FPM (plus its workers and scheduler) in step with the site's settings. Moving a
// site between the shared container and its own FPM is just a site update.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/wangsirco/berth/internal/agent/system"
	"github.com/wangsirco/berth/internal/change"
	"github.com/wangsirco/berth/internal/errs"
)

func isNativePHP(s *Site) bool { return s != nil && s.Target.Type == "php" && s.Target.Native }

func phpDotted(tag string) string { // "84" → "8.4"
	if len(tag) == 2 {
		return tag[:1] + "." + tag[1:]
	}
	return tag
}

func (a *App) phpSiteSpec(s Site) system.PHPSite {
	ini := map[string]string{}
	for k, v := range phpIniDefaults {
		ini[k] = v
	}
	for k, v := range s.Target.PHPIni {
		ini[k] = v
	}
	w := []system.PHPWorker{}
	for _, x := range s.Target.PHPWorkers {
		w = append(w, system.PHPWorker{Name: x.Name, Command: x.Command})
	}
	return system.PHPSite{ID: s.ID, Domain: s.Primary(), Version: phpDotted(s.Target.PHP), RootSub: s.Target.RootSub, Ini: ini,
		MemMB: s.Target.PHPMemMB, Workers: w, Schedule: s.Target.Schedule}
}

func ownerKey(id int64) string { return "phpsite.owner." + strconv.FormatInt(id, 10) }

// syncPHPSite starts, updates or stops a site's own FPM to match s (old is what it was, or nil).
func (a *App) syncPHPSite(env change.Env, s *Site, old *Site) error {
	if isNativePHP(s) {
		var res system.PHPSiteResult
		if err := a.streamAgent(env, env.Host, "/v1/phpsite/set", a.phpSiteSpec(*s), &res); err != nil {
			return err
		}
		if res.PrevOwner != "" && a.setting(ownerKey(s.ID)) == "" {
			a.setSetting(ownerKey(s.ID), res.PrevOwner) // give the folder back if it ever returns to the container
		}
		return nil
	}
	if isNativePHP(old) {
		owner := a.setting(ownerKey(old.ID))
		if err := a.call(env.Ctx, env.Host, "POST", "/v1/phpsite/remove", map[string]any{"id": old.ID, "domain": old.Primary(), "owner": owner}, nil); err != nil {
			return err
		}
		a.setSetting(ownerKey(old.ID), "")
	}
	return nil
}

// sitePHPRun runs a shell script in a PHP site's folder: in the shared container, or (native)
// on the host as the site's user.
func (a *App) sitePHPRun(env change.Env, s Site, script string) (string, error) {
	if !isNativePHP(&s) {
		return a.phpExec(env, versionOfTag(s.Target.PHP), "sh", "-c", script)
	}
	var lines []string
	sub := env
	sub.Log = func(f string, args ...any) {
		lines = append(lines, strings.TrimSpace(strings.ReplaceAll(fmt.Sprintf(f, args...), "\n", " ")))
		env.Log(f, args...)
	}
	err := a.streamAgent(sub, env.Host, "/v1/phpsite/exec", map[string]any{"id": s.ID, "domain": s.Primary(), "version": phpDotted(s.Target.PHP), "command": script}, nil)
	return strings.Join(lines, "\n"), err
}

func init() {
	routeHooks = append(routeHooks, func(a *App, mux *http.ServeMux) {
		mux.HandleFunc("GET /api/v1/hosts/{host}/php/native", a.authed("sites.view", func(w http.ResponseWriter, r *http.Request) {
			host := r.PathValue("host")
			var rt struct {
				Items []system.PHPRuntime `json:"items"`
			}
			if err := a.call(r.Context(), host, "GET", "/v1/phpnative", nil, &rt); err != nil {
				a.fail(w, r, err)
				return
			}
			sites, _ := a.sites(host)
			type view struct {
				system.PHPRuntime
				Sites []string `json:"sites"`
			}
			out := []view{}
			for _, x := range rt.Items {
				v := view{PHPRuntime: x, Sites: []string{}}
				for _, s := range sites {
					if isNativePHP(&s) && phpDotted(s.Target.PHP) == x.Version {
						v.Sites = append(v.Sites, s.Primary())
					}
				}
				out = append(out, v)
			}
			writeJSON(w, 200, map[string]any{"items": out, "extensions": PHPExtensions})
		}))
		mux.HandleFunc("GET /api/v1/hosts/{host}/sites/{id}/php", a.authed("sites.view", func(w http.ResponseWriter, r *http.Request) {
			host := r.PathValue("host")
			id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
			s, err := a.site(host, id)
			if err != nil {
				a.fail(w, r, err)
				return
			}
			out := map[string]any{"native": isNativePHP(&s)}
			var rep system.PHPReport
			if a.call(r.Context(), host, "POST", "/v1/phpsite/check", map[string]string{"domain": s.Primary()}, &rep) == nil {
				out["check"] = rep
			}
			if isNativePHP(&s) {
				var st system.PHPSiteStatus
				if a.call(r.Context(), host, "GET", "/v1/phpsite/"+strconv.FormatInt(id, 10), nil, &st) == nil {
					out["status"] = st
				}
			}
			writeJSON(w, 200, out)
		}))
	})

	verProp := map[string]change.Prop{"version": {Type: "string", Enum: system.PHPNativeVersions}, "extensions": {Type: "array", Desc: "default: the common set"}}
	change.Register(change.Kind{
		Name: "php.native.install", Perm: "sites.manage", Scope: "sites:write", Title: "kind.php.native.install", CLI: "berth php native install {{.version}}",
		Heavy: true, MemMB: 400, Params: change.ParamSchema{Props: verProp, Required: []string{"version"}},
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			var p struct {
				Version    string
				Extensions []string
			}
			json.Unmarshal(raw, &p)
			if !contains(system.PHPNativeVersions, p.Version) {
				return change.Plan{}, errs.New(400, "php.bad_version", "version", p.Version)
			}
			for _, e := range p.Extensions {
				if !contains(PHPExtensions, e) && e != "sodium" && e != "pcntl" && e != "mbstring" && e != "xml" && e != "curl" && e != "pdo_sqlite" {
					return change.Plan{}, errs.New(400, "php.bad_ext", "ext", e)
				}
			}
			return change.Plan{Summary: "plan.php.native.install", SummaryP: map[string]any{"version": p.Version}, Risk: change.RiskLow, Undoable: true,
				Commands: []string{"add the Remi (Fedora/EL) or sury (Debian/Ubuntu) PHP repository if missing",
					"install php" + strings.ReplaceAll(p.Version, ".", "") + "-php-fpm, -cli and the extensions (beside any other PHP; nothing system-wide changes)",
					"download composer into /opt/berth/composer"}}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			var p struct {
				Version    string
				Extensions []string
			}
			json.Unmarshal(raw, &p)
			if err := agentOp(env, "/v1/phpnative/install", map[string]any{"version": p.Version, "extensions": p.Extensions}); err != nil {
				return nil, nil, err
			}
			u, _ := json.Marshal(map[string]string{"version": p.Version})
			return nil, &change.UndoRecipe{Kind: "php.native.remove", Params: u}, nil
		},
	})
	change.Register(change.Kind{
		Name: "php.native.remove", Perm: "sites.manage", Scope: "sites:write", Title: "kind.php.native.remove", CLI: "berth php native remove {{.version}}",
		Heavy: true, Params: change.ParamSchema{Props: verProp, Required: []string{"version"}},
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			var p struct{ Version string }
			json.Unmarshal(raw, &p)
			sites, _ := svc(env).sites(env.Host)
			for _, s := range sites {
				if isNativePHP(&s) && phpDotted(s.Target.PHP) == p.Version {
					return change.Plan{}, errs.New(409, "php.in_use", "site", s.Primary())
				}
			}
			return change.Plan{Summary: "plan.php.native.remove", SummaryP: map[string]any{"version": p.Version}, Risk: change.RiskLow, Undoable: true}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			var p struct{ Version string }
			json.Unmarshal(raw, &p)
			if err := agentOp(env, "/v1/phpnative/install", map[string]any{"version": p.Version, "remove": true}); err != nil {
				return nil, nil, err
			}
			u, _ := json.Marshal(map[string]string{"version": p.Version})
			return nil, &change.UndoRecipe{Kind: "php.native.install", Params: u}, nil
		},
	})
	// composer install, php artisan migrate … in a native PHP site's folder, as the site's user
	change.Register(change.Kind{
		Name: "site.php_run", Perm: "sites.manage", Scope: "sites:write", Title: "kind.site.php_run", CLI: "berth site php-run {{.id}} {{.command}}",
		Heavy: true, MemMB: 300,
		Params: change.ParamSchema{Props: map[string]change.Prop{"id": {Type: "integer"}, "command": {Type: "string", Desc: "e.g. composer install --no-dev -o, php artisan migrate --force"}}, Required: []string{"id", "command"}},
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			var p struct {
				ID      int64
				Command string
			}
			json.Unmarshal(raw, &p)
			s, err := svc(env).site(env.Host, p.ID)
			if err != nil {
				return change.Plan{}, err
			}
			if !isNativePHP(&s) {
				return change.Plan{}, errs.New(409, "php.not_native_site")
			}
			if strings.TrimSpace(p.Command) == "" || strings.ContainsAny(p.Command, "\n\r") {
				return change.Plan{}, errs.New(400, "php.bad_command")
			}
			return change.Plan{Summary: "plan.site.php_run", SummaryP: map[string]any{"domain": s.Primary(), "command": p.Command}, Risk: change.RiskMedium,
				Commands: []string{"cd /srv/berth/caddy/sites/" + s.Primary() + " && " + p.Command + "   (as site-web" + strconv.FormatInt(s.ID, 10) + ", PHP " + phpDotted(s.Target.PHP) + ")"}}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			a := svc(env)
			var p struct {
				ID      int64
				Command string
			}
			json.Unmarshal(raw, &p)
			s, err := a.site(env.Host, p.ID)
			if err != nil {
				return nil, nil, err
			}
			_, err = a.sitePHPRun(env, s, p.Command)
			return nil, nil, err
		},
	})
}
