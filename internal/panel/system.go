package panel

// The server itself (方案 §5.13): overview, network cards, services, processes, disks,
// updates and power. Reads go straight to the agent; every write is a change with a
// preview and, where it makes sense, an undo.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/wangsirco/berth/internal/change"
	"github.com/wangsirco/berth/internal/errs"
)

// agentGet proxies a GET (with its query) to the host's agent.
func (a *App) agentGet(path string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p := path
		for _, k := range []string{"name"} {
			p = strings.ReplaceAll(p, "{"+k+"}", url.PathEscape(r.PathValue(k)))
		}
		if q := r.URL.RawQuery; q != "" {
			p += "?" + q
		}
		var out json.RawMessage
		if err := a.call(r.Context(), r.PathValue("host"), "GET", p, nil, &out); err != nil {
			a.fail(w, r, err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(out)
	}
}

type sysInfo struct {
	Hostname string `json:"hostname"`
	Timezone string `json:"timezone"`
	NTP      bool   `json:"ntp"`
}

type nicSpec struct {
	Name     string   `json:"name"`
	Method4  string   `json:"method4,omitempty"`
	Addrs4   []string `json:"addrs4,omitempty"`
	Gateway4 string   `json:"gateway4,omitempty"`
	DNS4     []string `json:"dns4"` // nil = leave, [] = clear
	OwnDNS   bool     `json:"own_dns,omitempty"`
	MTU      int      `json:"mtu,omitempty"`
	Up       *bool    `json:"up,omitempty"`
	Seconds  int      `json:"seconds,omitempty"`
}

type nicView struct {
	Name    string `json:"name"`
	Default bool   `json:"default"`
	MTU     int    `json:"mtu"`
	State   string `json:"state"`
	Conn    *struct {
		Method4  string   `json:"method4"`
		Addrs4   []string `json:"addrs4"`
		Gateway4 string   `json:"gateway4"`
		DNS4     []string `json:"dns4"`
	} `json:"conn"`
}

func (a *App) nicNow(env change.Env, name string) (nicView, error) {
	var out struct {
		Items []nicView `json:"items"`
	}
	if err := a.call(env.Ctx, env.Host, "GET", "/v1/system/nics", nil, &out); err != nil {
		return nicView{}, err
	}
	for _, n := range out.Items {
		if n.Name == name {
			return n, nil
		}
	}
	return nicView{}, errs.New(404, "system.no_nic", "name", name)
}

func init() {
	routeHooks = append(routeHooks, func(a *App, mux *http.ServeMux) {
		for _, p := range []string{"info", "timezones", "nics", "services", "processes", "disks", "updates"} {
			mux.HandleFunc("GET /api/v1/hosts/{host}/system/"+p, a.authed("system.view", a.agentGet("/v1/system/"+p)))
		}
		mux.HandleFunc("GET /api/v1/hosts/{host}/system/services/{name}/logs", a.authed("system.view", a.agentGet("/v1/system/services/{name}/logs")))
		mux.HandleFunc("GET /api/v1/hosts/{host}/system/nics/{name}/public-ip", a.authed("system.view", func(w http.ResponseWriter, r *http.Request) {
			fam := r.URL.Query().Get("family")
			if fam != "6" {
				fam = "4"
			}
			var out json.RawMessage
			if err := a.call(r.Context(), r.PathValue("host"), "GET", "/v1/net/public-ip?family="+fam+"&iface="+url.QueryEscape(r.PathValue("name")), nil, &out); err != nil {
				a.fail(w, r, err)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.Write(out)
		}))
	})

	str := func(desc string) change.Prop { return change.Prop{Type: "string", Desc: desc} }
	simple := func(name, perm string, props map[string]change.Prop, required []string, cli string,
		plan func(env change.Env, raw json.RawMessage) (change.Plan, error),
		apply func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error)) {
		change.Register(change.Kind{Name: name, Perm: perm, Scope: "system:write", Title: "kind." + name,
			Params: change.ParamSchema{Props: props, Required: required}, CLI: cli, Plan: plan, Apply: apply})
	}
	info := func(env change.Env) sysInfo {
		var i sysInfo
		svc(env).call(env.Ctx, env.Host, "GET", "/v1/system/info", nil, &i)
		return i
	}

	simple("system.hostname", "system.manage", map[string]change.Prop{"name": str("new host name")}, []string{"name"}, "berth system hostname {{.name}}",
		func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			var p struct{ Name string }
			json.Unmarshal(raw, &p)
			return change.Plan{Summary: "plan.system.hostname", SummaryP: map[string]any{"before": info(env).Hostname, "name": p.Name}, Risk: change.RiskLow, Undoable: true,
				Commands: []string{"hostnamectl set-hostname " + p.Name}}, nil
		},
		func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			old := info(env).Hostname
			if err := svc(env).call(env.Ctx, env.Host, "POST", "/v1/system/hostname", raw, nil); err != nil {
				return nil, nil, err
			}
			u, _ := json.Marshal(map[string]string{"name": old})
			return nil, &change.UndoRecipe{Kind: "system.hostname", Params: u}, nil
		})
	simple("system.timezone", "system.manage", map[string]change.Prop{"timezone": str("IANA zone, e.g. Asia/Shanghai")}, []string{"timezone"}, "berth system timezone {{.timezone}}",
		func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			var p struct{ Timezone string }
			json.Unmarshal(raw, &p)
			return change.Plan{Summary: "plan.system.timezone", SummaryP: map[string]any{"before": info(env).Timezone, "tz": p.Timezone}, Risk: change.RiskLow, Undoable: true,
				Commands: []string{"timedatectl set-timezone " + p.Timezone}}, nil
		},
		func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			old := info(env).Timezone
			if err := svc(env).call(env.Ctx, env.Host, "POST", "/v1/system/timezone", raw, nil); err != nil {
				return nil, nil, err
			}
			u, _ := json.Marshal(map[string]string{"timezone": old})
			return nil, &change.UndoRecipe{Kind: "system.timezone", Params: u}, nil
		})
	simple("system.ntp", "system.manage", map[string]change.Prop{"on": {Type: "boolean"}}, []string{"on"}, "berth system ntp {{.on}}",
		func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			var p struct{ On bool }
			json.Unmarshal(raw, &p)
			return change.Plan{Summary: map[bool]string{true: "plan.system.ntp_on", false: "plan.system.ntp_off"}[p.On], Risk: change.RiskLow, Undoable: true,
				Commands: []string{fmt.Sprintf("timedatectl set-ntp %v", p.On)}}, nil
		},
		func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			old := info(env).NTP
			if err := svc(env).call(env.Ctx, env.Host, "POST", "/v1/system/ntp", raw, nil); err != nil {
				return nil, nil, err
			}
			u, _ := json.Marshal(map[string]bool{"on": old})
			return nil, &change.UndoRecipe{Kind: "system.ntp", Params: u}, nil
		})
	simple("system.power", "system.manage", map[string]change.Prop{"action": {Type: "string", Enum: []string{"reboot", "poweroff", "cancel"}}, "minutes": {Type: "integer", Desc: "delay (0 = now)"}},
		[]string{"action"}, "berth system {{.action}}",
		func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			var p struct {
				Action  string
				Minutes int
			}
			json.Unmarshal(raw, &p)
			if p.Action == "cancel" {
				return change.Plan{Summary: "plan.system.power_cancel", Risk: change.RiskLow}, nil
			}
			if p.Action != "reboot" && p.Action != "poweroff" {
				return change.Plan{}, errs.New(400, "params.enum", "name", "action", "allowed", "reboot, poweroff, cancel")
			}
			return change.Plan{Summary: "plan.system." + p.Action, SummaryP: map[string]any{"minutes": p.Minutes, "host": env.Host}, Risk: change.RiskHigh,
				Commands: []string{fmt.Sprintf("systemd-run --on-active %ds systemctl %s", p.Minutes*60+5, p.Action)}}, nil
		},
		func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			if err := svc(env).call(env.Ctx, env.Host, "POST", "/v1/system/power", raw, nil); err != nil {
				return nil, nil, err
			}
			var p struct{ Action string }
			json.Unmarshal(raw, &p)
			if p.Action != "cancel" {
				return nil, &change.UndoRecipe{Kind: "system.power", Params: json.RawMessage(`{"action":"cancel"}`)}, nil
			}
			return nil, nil, nil
		})

	// network cards: under a NetworkManager checkpoint that rolls back unless confirmed
	nicProps := map[string]change.Prop{"name": str("interface"), "method4": {Type: "string", Enum: []string{"", "auto", "manual"}},
		"addrs4": {Type: "array", Desc: `["192.168.1.10/24"]`}, "gateway4": str(""), "dns4": {Type: "array"}, "own_dns": {Type: "boolean"},
		"mtu": {Type: "integer"}, "up": {Type: "boolean"}, "seconds": {Type: "integer", Desc: "rollback after (30-600, default 60)"}}
	simple("system.nic.set", "system.manage", nicProps, []string{"name"}, "berth system nic {{.name}}",
		func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			var p nicSpec
			json.Unmarshal(raw, &p)
			n, err := svc(env).nicNow(env, p.Name)
			if err != nil {
				return change.Plan{}, err
			}
			if n.Conn == nil {
				return change.Plan{}, errs.New(409, "system.nic_unmanaged", "name", p.Name)
			}
			cmds := []string{}
			if p.Up != nil {
				cmds = append(cmds, map[bool]string{true: "nmcli device connect ", false: "nmcli device disconnect "}[*p.Up]+p.Name)
			}
			if p.Method4 == "manual" {
				cmds = append(cmds, fmt.Sprintf("ipv4: manual %s gw %s", strings.Join(p.Addrs4, ", "), p.Gateway4))
			} else if p.Method4 == "auto" {
				cmds = append(cmds, "ipv4: DHCP")
			}
			if p.DNS4 != nil {
				cmds = append(cmds, "dns: "+strings.Join(p.DNS4, " "))
			}
			if p.MTU > 0 {
				cmds = append(cmds, "mtu: "+strconv.Itoa(p.MTU))
			}
			risk := change.RiskMedium
			summary := "plan.system.nic"
			if n.Default {
				risk, summary = change.RiskHigh, "plan.system.nic_default" // the panel may be reached through it
			}
			secs := p.Seconds
			if secs == 0 {
				secs = 60
			}
			return change.Plan{Summary: summary, SummaryP: map[string]any{"name": p.Name, "secs": secs}, Risk: risk, Undoable: true, Commands: cmds}, nil
		},
		func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			a := svc(env)
			var p nicSpec
			json.Unmarshal(raw, &p)
			n, err := a.nicNow(env, p.Name)
			if err != nil {
				return nil, nil, err
			}
			var out map[string]any
			if err := a.streamAgent(env, env.Host, "/v1/system/nics/"+url.PathEscape(p.Name), p, &out); err != nil {
				return nil, nil, err
			}
			// undo puts the previous settings back (under a checkpoint again)
			back := nicSpec{Name: p.Name}
			if p.Up != nil {
				up := !*p.Up
				back.Up = &up
			}
			if n.Conn != nil && (p.Method4 != "" || p.DNS4 != nil) {
				back.Method4, back.Addrs4, back.Gateway4, back.DNS4 = n.Conn.Method4, n.Conn.Addrs4, n.Conn.Gateway4, n.Conn.DNS4
				if back.Method4 != "manual" {
					back.Method4 = "auto"
				}
				if back.DNS4 == nil {
					back.DNS4 = []string{}
				}
			}
			if p.MTU > 0 {
				back.MTU = n.MTU
			}
			u, _ := json.Marshal(back)
			return out, &change.UndoRecipe{Kind: "system.nic.set", Params: u}, nil
		})
	for _, k := range []string{"confirm", "rollback"} {
		k := k
		simple("system.net."+k, "system.manage", map[string]change.Prop{}, nil, "berth system net "+k,
			func(env change.Env, _ json.RawMessage) (change.Plan, error) {
				return change.Plan{Summary: "plan.system.net_" + k, Risk: change.RiskLow}, nil
			},
			func(env change.Env, _ json.RawMessage) (any, *change.UndoRecipe, error) {
				return nil, nil, svc(env).call(env.Ctx, env.Host, "POST", "/v1/system/net/"+k, nil, nil)
			})
	}

	// services
	inverse := map[string]string{"start": "stop", "stop": "start", "enable": "disable", "disable": "enable"}
	simple("system.service", "system.manage", map[string]change.Prop{"name": str("unit, e.g. nginx.service"), "action": {Type: "string", Enum: []string{"start", "stop", "restart", "enable", "disable"}}},
		[]string{"name", "action"}, "berth system service {{.action}} {{.name}}",
		func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			var p struct{ Name, Action string }
			json.Unmarshal(raw, &p)
			if !strings.HasSuffix(p.Name, ".service") {
				p.Name += ".service"
			}
			risk := change.RiskLow
			if p.Action == "stop" || p.Action == "disable" {
				risk = change.RiskMedium
			}
			return change.Plan{Summary: "plan.system.service_" + p.Action, SummaryP: map[string]any{"name": p.Name}, Risk: risk, Undoable: inverse[p.Action] != "",
				Commands: []string{"systemctl " + p.Action + " " + p.Name}}, nil
		},
		func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			var p struct{ Name, Action string }
			json.Unmarshal(raw, &p)
			if !strings.HasSuffix(p.Name, ".service") {
				p.Name += ".service"
			}
			if err := svc(env).call(env.Ctx, env.Host, "POST", "/v1/system/services/"+url.PathEscape(p.Name)+"/"+p.Action, nil, nil); err != nil {
				return nil, nil, err
			}
			if inv := inverse[p.Action]; inv != "" {
				u, _ := json.Marshal(map[string]string{"name": p.Name, "action": inv})
				return nil, &change.UndoRecipe{Kind: "system.service", Params: u}, nil
			}
			return nil, nil, nil
		})

	simple("system.process.signal", "system.manage", map[string]change.Prop{"pid": {Type: "integer"}, "signal": {Type: "string", Enum: []string{"TERM", "KILL"}}, "name": str("shown in the log")},
		[]string{"pid"}, "berth system kill {{.pid}}",
		func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			var p struct {
				PID          int
				Signal, Name string
			}
			json.Unmarshal(raw, &p)
			if p.Signal == "" {
				p.Signal = "TERM"
			}
			return change.Plan{Summary: "plan.system.signal_" + strings.ToLower(p.Signal), SummaryP: map[string]any{"pid": p.PID, "name": p.Name}, Risk: change.RiskMedium,
				Commands: []string{fmt.Sprintf("kill -%s %d", p.Signal, p.PID)}}, nil
		},
		func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			var p struct {
				PID    int
				Signal string
			}
			json.Unmarshal(raw, &p)
			return nil, nil, svc(env).call(env.Ctx, env.Host, "POST", "/v1/system/processes/"+strconv.Itoa(p.PID)+"/signal", map[string]string{"signal": p.Signal}, nil)
		})

	change.Register(change.Kind{
		Name: "system.updates", Perm: "system.manage", Scope: "system:write", Title: "kind.system.updates",
		Params: change.ParamSchema{Props: map[string]change.Prop{"security_only": {Type: "boolean"}}}, CLI: "berth system update", Heavy: true, MemMB: 300,
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			var p struct {
				SecurityOnly bool `json:"security_only"`
			}
			json.Unmarshal(raw, &p)
			var u struct {
				Items []struct {
					Name     string `json:"name"`
					Security bool   `json:"security"`
				} `json:"items"`
			}
			svc(env).call(env.Ctx, env.Host, "GET", "/v1/system/updates", nil, &u)
			n := 0
			for _, i := range u.Items {
				if !p.SecurityOnly || i.Security {
					n++
				}
			}
			cmds := []string{map[bool]string{true: "dnf -y upgrade --security", false: "dnf -y upgrade"}[p.SecurityOnly]}
			if l := svc(env).snapLine(env); l != "" {
				cmds = append([]string{l}, cmds...)
			}
			return change.Plan{Summary: map[bool]string{true: "plan.system.updates_security", false: "plan.system.updates"}[p.SecurityOnly], SummaryP: map[string]any{"n": n},
				Risk: change.RiskMedium, Commands: cmds}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			var out map[string]any
			svc(env).autoSnapshot(env, "system updates")
			return out, nil, svc(env).streamAgent(env, env.Host, "/v1/system/updates", raw, &out)
		},
	})
}
