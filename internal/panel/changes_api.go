package panel

import (
	"encoding/json"
	"net/http"

	"github.com/wangsirco/berth/internal/change"
	"github.com/wangsirco/berth/internal/errs"
)

type changeView struct {
	*change.Change
	Equivalent string `json:"equivalent"`
	// Title, Summary and Action are translated into every UI language, so the web UI, CLI
	// and MCP clients show the same words without their own copy of the messages.
	Title   map[string]string `json:"title"`
	Summary map[string]string `json:"summary"`
	Action  map[string]string `json:"action"`
}

func viewOf(c *change.Change) changeView {
	if c == nil {
		return changeView{}
	}
	v := changeView{Change: c, Equivalent: change.EquivalentCommand(c.Kind, c.Params),
		Title: map[string]string{}, Summary: map[string]string{}, Action: map[string]string{}}
	for _, l := range Locales {
		v.Title[l] = T(l, "kind."+c.Kind, nil)
		v.Summary[l] = T(l, c.Plan.Summary, c.Plan.SummaryP)
		v.Action[l] = T(l, "action."+c.Kind, nil)
	}
	return v
}

// runChange previews (?plan=1) or runs kind on host, after checking the caller may.
func (a *App) runChange(w http.ResponseWriter, r *http.Request, host, kind string, params json.RawMessage) {
	a.runChangeAuthorized(w, r, host, kind, params, nil)
}

// runChangeAuthorized lets a handler that already checked object ownership replace the
// kind's permission check (e.g. people revoking their own tokens). Token scopes still apply.
func (a *App) runChangeAuthorized(w http.ResponseWriter, r *http.Request, host, kind string, params json.RawMessage, authorized func() error) {
	k, ok := change.Get(kind)
	if !ok {
		a.fail(w, r, errs.New(404, "change.unknown_kind", "kind", kind))
		return
	}
	var err error
	if authorized != nil {
		if err = authorized(); err == nil {
			err = a.tokenMayRun(r, k, host)
		}
	} else {
		err = a.mayRun(r, k, host)
	}
	if err != nil {
		a.fail(w, r, err)
		return
	}
	ac := currentActor(r)
	var c *change.Change
	if r.URL.Query().Get("plan") == "1" {
		c, err = a.pipe.Preview(r.Context(), ac, host, kind, params)
	} else {
		c, err = a.pipe.Run(r.Context(), ac, host, kind, params)
	}
	if err != nil && c == nil {
		a.fail(w, r, err)
		return
	}
	code := 200
	if err != nil {
		code = errs.From(err).Status
	}
	writeJSON(w, code, viewOf(c))
}

// mayRun checks the user's permission for the kind and host scope (tokens add scope checks).
func (a *App) mayRun(r *http.Request, k change.Kind, host string) error {
	u := currentUser(r)
	if k.Perm != "" && !u.Can(k.Perm) {
		return errs.New(403, "auth.forbidden", "perm", k.Perm)
	}
	if len(u.Scope.Hosts) > 0 && !contains(u.Scope.Hosts, host) && u.Role != RoleSuper {
		return errs.New(403, "auth.host_forbidden", "host", host)
	}
	if err := a.tokenMayRun(r, k, host); err != nil {
		return err
	}
	return nil
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}
