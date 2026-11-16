package panel

// Virus scan (ClamAV, 1Panel 工具箱的病毒扫描): scan a directory or a site, list what was
// found, raise an alert. A scheduled task can run it as a Berth operation.

import (
	"encoding/json"
	"path"
	"strings"

	"github.com/wangsirco/berth/internal/change"
	"github.com/wangsirco/berth/internal/errs"
)

type avResult struct {
	Paths    []string `json:"paths"`
	Files    int      `json:"files"`
	Infected []struct {
		Path string `json:"path"`
		Sig  string `json:"sig"`
	} `json:"infected"`
	Seconds int    `json:"seconds"`
	DBDate  string `json:"db_date,omitempty"`
}

func init() {
	change.Register(change.Kind{
		Name: "av.scan", Perm: "system.manage", Scope: "system:write", Title: "kind.av.scan", CLI: "berth scan {{.paths}}", Heavy: true, MemMB: 1200,
		Params: change.ParamSchema{Props: map[string]change.Prop{"paths": {Type: "array", Desc: "files or directories to scan, e.g. [\"/srv/berth/caddy/sites\"]"}}, Required: []string{"paths"}},
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			var p struct{ Paths []string }
			json.Unmarshal(raw, &p)
			if len(p.Paths) == 0 || len(p.Paths) > 20 {
				return change.Plan{}, errs.New(400, "av.paths_count")
			}
			for _, x := range p.Paths {
				c := path.Clean(x)
				if !path.IsAbs(c) {
					return change.Plan{}, errs.New(400, "av.bad_path", "path", x)
				}
				for _, d := range []string{"/proc", "/sys", "/dev", "/run"} {
					if c == d || strings.HasPrefix(c, d+"/") {
						return change.Plan{}, errs.New(400, "av.bad_path", "path", x)
					}
				}
			}
			if st, err := svc(env).avState(env.Ctx, env.Host); err == nil && !st.Installed {
				return change.Plan{}, errs.New(409, "av.not_installed")
			}
			return change.Plan{Summary: "plan.av.scan", SummaryP: map[string]any{"paths": strings.Join(p.Paths, ", ")}, Risk: change.RiskLow,
				Commands: []string{"freshclam  (virus signatures, at most once a day)",
					"docker run --rm --network none -v /:/host:ro clamav/clamav-debian clamscan -r " + strings.Join(p.Paths, " ") + "  (read only; nothing is changed or deleted)"}}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			a := svc(env)
			var p struct{ Paths []string }
			json.Unmarshal(raw, &p)
			var res avResult
			if err := a.streamAgent(env, env.Host, "/v1/av/scan", map[string]any{"paths": p.Paths}, &res); err != nil {
				return nil, nil, err
			}
			r, _ := a.alertRule("virus_found")
			subj := strings.Join(p.Paths, ",")
			if len(res.Infected) > 0 {
				list := []string{}
				for i, x := range res.Infected {
					if i == 5 {
						list = append(list, "…")
						break
					}
					list = append(list, x.Path+" ("+x.Sig+")")
				}
				a.fire(r, subj, env.Host, map[string]any{"n": len(res.Infected), "paths": subj, "list": strings.Join(list, "\n")})
			} else {
				a.resolve(r.Key, subj)
			}
			return res, nil, nil
		},
	})
}
