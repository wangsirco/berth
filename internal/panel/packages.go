package panel

// System packages (软件包): search, install and remove distribution packages. The preview is
// the package manager's own dry run, so it lists every dependency that comes or goes.

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/wangsirco/berth/internal/change"
)

type pkgStep struct {
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
	Action  string `json:"action"`
	Size    string `json:"size,omitempty"`
}

type pkgDry struct {
	Steps []pkgStep `json:"steps"`
	Note  string    `json:"note,omitempty"`
}

type pkgParams struct {
	Names []string `json:"names"`
}

func (a *App) pkgDryRun(env change.Env, names []string, remove bool) (pkgDry, error) {
	var d pkgDry
	err := a.call(env.Ctx, env.Host, "POST", "/v1/system/packages/dry", map[string]any{"names": names, "remove": remove}, &d)
	return d, err
}

// pkgPlan turns a dry run into the preview.
func pkgPlan(d pkgDry, names []string, remove bool) change.Plan {
	verb := map[bool]string{true: "remove", false: "install"}[remove]
	cmds := []string{"dnf -y " + verb + " " + strings.Join(names, " ")}
	extra := 0
	for _, s := range d.Steps {
		mark := map[string]string{"install": "+", "dep": "+", "weak": "+", "upgrade": "↑", "remove": "−", "unused": "−"}[s.Action]
		line := "  " + mark + " " + s.Name
		if s.Version != "" {
			line += " " + s.Version
		}
		if s.Action == "dep" || s.Action == "weak" || s.Action == "unused" {
			line += "  (" + map[string]string{"dep": "dependency", "weak": "recommended", "unused": "no longer needed"}[s.Action] + ")"
			extra++
		}
		if s.Size != "" {
			line += "  " + s.Size
		}
		cmds = append(cmds, line)
	}
	if d.Note != "" {
		cmds = append(cmds, d.Note)
	}
	risk := change.RiskLow
	if remove {
		risk = change.RiskMedium
		if len(d.Steps) > 10 {
			risk = change.RiskHigh
		}
	}
	affected := []change.Object{}
	for _, n := range names {
		affected = append(affected, change.Object{Type: "package", ID: n, Name: n})
	}
	sum := "plan.pkg." + verb
	if len(d.Steps) == 0 {
		sum = "plan.pkg.nothing"
	}
	return change.Plan{Summary: sum, SummaryP: map[string]any{"names": strings.Join(names, ", "), "n": len(d.Steps), "extra": extra},
		Commands: cmds, Affected: affected, Risk: risk, Undoable: len(d.Steps) > 0}
}

func init() {
	routeHooks = append(routeHooks, func(a *App, mux *http.ServeMux) {
		mux.HandleFunc("GET /api/v1/hosts/{host}/packages", a.authed("system.view", a.agentGet("/v1/system/packages")))
	})
	props := change.ParamSchema{Props: map[string]change.Prop{"names": {Type: "array", Desc: "package names, e.g. [\"htop\"]"}}, Required: []string{"names"}}
	for _, remove := range []bool{false, true} {
		remove := remove
		verb := map[bool]string{true: "remove", false: "install"}[remove]
		change.Register(change.Kind{
			Name: "pkg." + verb, Perm: "system.manage", Scope: "system:write", Title: "kind.pkg." + verb, CLI: "berth pkg " + verb + " {{.names}}",
			Heavy: true, MemMB: 300, Params: props,
			Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
				var p pkgParams
				json.Unmarshal(raw, &p)
				d, err := svc(env).pkgDryRun(env, p.Names, remove)
				if err != nil {
					return change.Plan{}, err
				}
				pl := pkgPlan(d, p.Names, remove)
				if l := svc(env).snapLine(env); l != "" && len(d.Steps) > 0 {
					pl.Commands = append([]string{l}, pl.Commands...)
				}
				return pl, nil
			},
			Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
				var p pkgParams
				json.Unmarshal(raw, &p)
				svc(env).autoSnapshot(env, verb+" "+strings.Join(p.Names, " "))
				var d pkgDry
				if err := svc(env).streamAgent(env, env.Host, "/v1/system/packages", map[string]any{"names": p.Names, "remove": remove}, &d); err != nil {
					return nil, nil, err
				}
				// undo: remove what was newly asked for, or put back everything that went
				back := []string{}
				for _, s := range d.Steps {
					if (!remove && s.Action == "install") || (remove && (s.Action == "remove" || s.Action == "unused")) {
						back = append(back, s.Name)
					}
				}
				if len(back) == 0 {
					return d, nil, nil
				}
				u, _ := json.Marshal(pkgParams{Names: back})
				return d, &change.UndoRecipe{Kind: "pkg." + map[bool]string{true: "install", false: "remove"}[remove], Params: u}, nil
			},
		})
	}
}
