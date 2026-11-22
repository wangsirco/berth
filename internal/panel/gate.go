package panel

import (
	"github.com/wangsirco/berth/internal/change"
	"github.com/wangsirco/berth/internal/errs"
)

// gate decides whether a change runs now, waits for a person to confirm, or is refused.
// People, schedules and the system run directly; API/CLI tokens run within their scopes;
// Agent tokens follow their danger policy (方案 §7.2).
type gate struct{ app *App }

func (g gate) Check(a change.Actor, k change.Kind, p change.Plan) (bool, error) {
	need, err := tokenGate{app: g.app}.check(a, string(p.Risk))
	if err != nil || need {
		return need, err
	}
	return g.app.needsApproval(a, k, p), nil
}

type tokenGate struct{ app *App }

func (g tokenGate) check(a change.Actor, risk string) (bool, error) {
	switch a.Type {
	case "user", "system", "schedule", "pat", "cli", "ext", "link": // link: a linked main panel, already limited to the servers handed over
		return false, nil
	case "agt":
		pol, _ := tokenPolicies.Load(a.ID)
		switch pol {
		case "readonly":
			return false, errs.New(403, "token.readonly")
		case "confirm":
			return risk == string(change.RiskHigh) || risk == string(change.RiskDestructive), nil
		case "run":
			return false, nil
		}
	}
	return false, errs.New(403, "auth.forbidden")
}
