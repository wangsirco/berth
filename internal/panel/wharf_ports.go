package panel

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/wangsirco/berth/internal/change"
	"github.com/wangsirco/berth/internal/errs"
	"github.com/wangsirco/berth/internal/panel/wharfapi"
)

// Port check with a paired Wharf router: can port N on the router reach this server?
// It puts together what Wharf already maps on that port, Wharf's own verdict for a new
// mapping (conflicts with its own services, other devices, grants), and whether anything
// on this server listens on the target port. wharf.forward then maps it in one click.

type portCheckFwd struct {
	Name       string `json:"name"`
	Proto      string `json:"proto"`
	TargetIP   string `json:"target_ip"`
	TargetPort string `json:"target_port"`
	BerthHost  string `json:"berth_host,omitempty"`
	Mine       bool   `json:"mine"` // points at this server
	ID         string `json:"id"`
	Berth      bool   `json:"berth"` // made by Berth (so Berth may remove it)
}

type portCheck struct {
	Wharf     string            `json:"wharf"`
	TargetIP  string            `json:"target_ip"`
	WANPort   int               `json:"wan_port"`
	LocalPort int               `json:"local_port"`
	Proto     string            `json:"proto"`
	Forwards  []portCheckFwd    `json:"forwards"`            // existing mappings of this router port
	Issues    []json.RawMessage `json:"issues"`              // Wharf's verdict for a new mapping ({level, msg})
	OK        bool              `json:"ok"`                  // a new mapping would be accepted
	Listening string            `json:"listening,omitempty"` // the process on the local port ("" = nothing)
	Listens   bool              `json:"listens"`
}

func (a *App) handleWharfPortCheck(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	wan, err := strconv.Atoi(q.Get("port"))
	if err != nil || wan < 1 || wan > 65535 {
		a.fail(w, r, errs.New(400, "wharf.bad_port"))
		return
	}
	local, _ := strconv.Atoi(q.Get("local_port"))
	if local < 1 || local > 65535 {
		local = wan
	}
	proto := q.Get("proto")
	if proto != "udp" {
		proto = "tcp"
	}
	c, l, err := a.wharfClient("forwards")
	if err != nil {
		a.fail(w, r, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	out := portCheck{Wharf: l.Name, WANPort: wan, LocalPort: local, Proto: proto, Forwards: []portCheckFwd{}, Issues: []json.RawMessage{}}
	if len(l.TargetIPs) > 0 {
		out.TargetIP = l.TargetIPs[0]
	}
	fwds, err := c.Forwards(ctx)
	if err != nil {
		a.fail(w, r, a.wharfErr(err))
		return
	}
	for _, f := range fwds {
		if f.WANPort != strconv.Itoa(wan) {
			continue
		}
		out.Forwards = append(out.Forwards, portCheckFwd{Name: f.Name, Proto: f.Proto, TargetIP: f.TargetIP, TargetPort: f.TargetPort, BerthHost: f.BerthHost,
			Mine: slices.Contains(l.TargetIPs, f.TargetIP), ID: f.ID, Berth: f.Kind == "berth"})
	}
	if out.TargetIP != "" {
		raw, err := c.PlanForward(ctx, wharfapi.Forward{Name: "berth: port " + strconv.Itoa(wan), Proto: proto, WANPort: strconv.Itoa(wan), TargetIP: out.TargetIP, TargetPort: strconv.Itoa(local)})
		var we *wharfapi.Error
		switch {
		case err == nil:
			var p struct {
				Issues []json.RawMessage `json:"issues"`
				OK     *bool             `json:"ok"`
			}
			json.Unmarshal(raw, &p)
			if p.Issues != nil {
				out.Issues = p.Issues
			}
			out.OK = p.OK == nil || *p.OK
		case errors.As(err, &we) && (we.Status == 409 || we.Status == 400):
			// Wharf refused the mapping outright: its message says why
			msg, _ := json.Marshal(map[string]string{"level": "error", "msg": we.Message})
			out.Issues = append(out.Issues, msg)
		default:
			a.fail(w, r, a.wharfErr(err))
			return
		}
	}
	var fw struct {
		Listening []struct {
			Proto   string `json:"proto"`
			Port    int    `json:"port"`
			Public  bool   `json:"public"`
			Process string `json:"process"`
		} `json:"listening"`
	}
	if a.call(ctx, "local", "GET", "/v1/firewall", nil, &fw) == nil {
		for _, x := range fw.Listening {
			if x.Port == local && x.Proto == proto && x.Public {
				out.Listens = true
				out.Listening = firstNonEmpty(x.Process, "?")
			}
		}
	}
	writeJSON(w, 200, out)
}

type wharfFwdParams struct {
	WANPort   string `json:"wan_port"`
	LocalPort string `json:"local_port,omitempty"`
	Proto     string `json:"proto,omitempty"`
	Note      string `json:"note,omitempty"`
	ID        string `json:"id,omitempty"` // wharf.unforward
}

func (p *wharfFwdParams) norm() error {
	if p.LocalPort == "" {
		p.LocalPort = p.WANPort
	}
	if p.Proto != "udp" {
		p.Proto = "tcp"
	}
	for _, s := range []string{p.WANPort, p.LocalPort} {
		if n, err := strconv.Atoi(s); err != nil || n < 1 || n > 65535 {
			return errs.New(400, "wharf.bad_port")
		}
	}
	return nil
}

func init() {
	routeHooks = append(routeHooks, func(a *App, mux *http.ServeMux) {
		mux.HandleFunc("GET /api/v1/wharf/port-check", a.authed("network.view", a.handleWharfPortCheck))
	})
	props := map[string]change.Prop{
		"wan_port": {Type: "string", Desc: "router port"}, "local_port": {Type: "string", Desc: "port on this server (default: the same)"},
		"proto": {Type: "string", Enum: []string{"tcp", "udp"}}, "note": {Type: "string"},
	}
	change.Register(change.Kind{
		Name: "wharf.forward", Perm: "network.manage", Scope: "network:write", Title: "kind.wharf.forward",
		Params: change.ParamSchema{Props: props, Required: []string{"wan_port"}}, CLI: "berth wharf forward {{.wan_port}}",
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			var p wharfFwdParams
			json.Unmarshal(raw, &p)
			if err := p.norm(); err != nil {
				return change.Plan{}, err
			}
			_, l, err := svc(env).wharfClient("forwards")
			if err != nil {
				return change.Plan{}, err
			}
			if len(l.TargetIPs) == 0 {
				return change.Plan{}, errs.New(400, "wharf.no_target")
			}
			return change.Plan{Summary: "plan.wharf.forward", SummaryP: map[string]any{"wharf": l.Name, "port": p.WANPort, "proto": p.Proto, "ip": l.TargetIPs[0], "local": p.LocalPort},
				Risk: change.RiskMedium, Undoable: true, Affected: []change.Object{{Type: "wharf_forward", ID: p.WANPort, Name: p.Proto + "/" + p.WANPort, Host: env.Host}}}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			var p wharfFwdParams
			json.Unmarshal(raw, &p)
			p.norm()
			a := svc(env)
			c, l, err := a.wharfClient("forwards")
			if err != nil {
				return nil, nil, err
			}
			f, err := c.CreateForward(env.Ctx, actorOf(env, "端口映射 "+p.WANPort), wharfapi.Forward{Name: firstNonEmpty(p.Note, "berth: port "+p.WANPort), Proto: p.Proto, WANPort: p.WANPort,
				TargetIP: l.TargetIPs[0], TargetPort: p.LocalPort, BerthRef: "port:" + p.WANPort, BerthHost: env.Host, Note: p.Note})
			if err != nil {
				return nil, nil, a.wharfErr(err)
			}
			u, _ := json.Marshal(wharfFwdParams{ID: f.ID, WANPort: p.WANPort, LocalPort: p.LocalPort, Proto: p.Proto, Note: p.Note})
			return f, &change.UndoRecipe{Kind: "wharf.unforward", Params: u}, nil
		},
	})
	change.Register(change.Kind{
		Name: "wharf.unforward", Perm: "network.manage", Scope: "network:write", Title: "kind.wharf.unforward",
		Params: change.ParamSchema{Props: map[string]change.Prop{"id": {Type: "string"}, "wan_port": {Type: "string"}}, Required: []string{"id"}}, CLI: "berth wharf unforward {{.id}}",
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			var p wharfFwdParams
			json.Unmarshal(raw, &p)
			if p.ID == "" {
				return change.Plan{}, errs.New(400, "wharf.bad_port")
			}
			return change.Plan{Summary: "plan.wharf.unforward", SummaryP: map[string]any{"port": p.WANPort}, Risk: change.RiskMedium, Undoable: p.WANPort != "",
				Affected: []change.Object{{Type: "wharf_forward", ID: p.WANPort, Name: p.WANPort, Host: env.Host}}}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			var p wharfFwdParams
			json.Unmarshal(raw, &p)
			a := svc(env)
			c, _, err := a.wharfClient("forwards")
			if err != nil {
				return nil, nil, err
			}
			if err := c.DeleteForward(env.Ctx, actorOf(env, "取消端口映射 "+p.WANPort), p.ID); err != nil {
				return nil, nil, a.wharfErr(err)
			}
			if p.WANPort == "" {
				return nil, nil, nil
			}
			u, _ := json.Marshal(wharfFwdParams{WANPort: p.WANPort, LocalPort: p.LocalPort, Proto: p.Proto, Note: p.Note})
			return nil, &change.UndoRecipe{Kind: "wharf.forward", Params: u}, nil
		},
	})
}
