package panel

// A VM's screen in the browser, like PVE's console: QEMU's VNC server (on a Unix socket the
// agent relays) shown with noVNC, so boot messages, the login prompt and a broken network
// are all visible. Plus the root login shown once right after creating a machine.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/wangsirco/berth/internal/agent/incus"
	"github.com/wangsirco/berth/internal/change"
	"github.com/wangsirco/berth/internal/errs"
)

type vmScreen struct {
	host, name string
	user       int64
	exp        time.Time
}

var vmScreens = struct {
	sync.Mutex
	m map[string]vmScreen
}{m: map[string]vmScreen{}}

// firstLogin: the password of a machine just made, for the person who made it, once.
type firstLogin struct {
	password string
	user     int64
	exp      time.Time
}

var firstLogins = struct {
	sync.Mutex
	m map[string]firstLogin
}{m: map[string]firstLogin{}}

func rememberFirstLogin(host, name, password string, uid int64) {
	firstLogins.Lock()
	defer firstLogins.Unlock()
	for k, v := range firstLogins.m {
		if time.Now().After(v.exp) {
			delete(firstLogins.m, k)
		}
	}
	firstLogins.m[host+"/"+name] = firstLogin{password: password, user: uid, exp: time.Now().Add(15 * time.Minute)}
}

func init() {
	routeHooks = append(routeHooks, func(a *App, mux *http.ServeMux) {
		mux.HandleFunc("POST /api/v1/hosts/{host}/vms/{name}/screen", a.authed("vms.manage", a.handleVMScreenKey))
		mux.HandleFunc("GET /api/v1/vm-screen/{key}", a.authed("vms.manage", a.handleVMScreenWS))
		// the root password of a machine this person just made: shown once, no re-login
		mux.HandleFunc("POST /api/v1/hosts/{host}/vms/{name}/first-login", a.authed("vms.manage", func(w http.ResponseWriter, r *http.Request) {
			k := r.PathValue("host") + "/" + r.PathValue("name")
			firstLogins.Lock()
			f, ok := firstLogins.m[k]
			delete(firstLogins.m, k)
			firstLogins.Unlock()
			if !ok || time.Now().After(f.exp) || f.user != currentUser(r).ID {
				a.fail(w, r, errs.New(404, "vm.no_password"))
				return
			}
			writeJSON(w, 200, map[string]string{"user": "root", "password": f.password})
		}))
	})

	change.Register(change.Kind{
		Name: "vm.screen.enable", Perm: "vms.manage", Scope: "system:write", Title: "kind.vm.screen.enable",
		Params: change.ParamSchema{Props: map[string]change.Prop{"name": {Type: "string"}}, Required: []string{"name"}},
		CLI:    "berth vm screen {{.name}}",
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			var p struct{ Name string }
			json.Unmarshal(raw, &p)
			in, err := svc(env).vm(env.Ctx, env.Host, p.Name)
			if err != nil {
				return change.Plan{}, err
			}
			if in.Type != "virtual-machine" {
				return change.Plan{}, errs.New(400, "vm.not_vm")
			}
			run := in.Status == "Running"
			// Incus takes raw.qemu only while the VM is off
			cmds := []string{"incus config set " + p.Name + " raw.qemu='" + incus.VNCArg(p.Name) + "'"}
			if run {
				cmds = []string{"incus stop " + p.Name + "   # shut down (forced after 60 s)", cmds[0], "incus start " + p.Name}
			}
			return change.Plan{Summary: "plan.vm.screen_enable", SummaryP: map[string]any{"name": p.Name}, Risk: map[bool]change.Risk{true: change.RiskMedium, false: change.RiskLow}[run],
				Affected: []change.Object{{Type: "vm", ID: p.Name, Name: p.Name, Host: env.Host}}, Commands: cmds}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			a := svc(env)
			var p struct{ Name string }
			json.Unmarshal(raw, &p)
			in, err := a.vm(env.Ctx, env.Host, p.Name)
			if err != nil {
				return nil, nil, err
			}
			state := func(action string, force bool) error {
				return a.call(env.Ctx, env.Host, "POST", "/v1/vms/"+url.PathEscape(p.Name)+"/state", map[string]any{"action": action, "force": force}, nil)
			}
			run := in.Status == "Running"
			if run { // Incus takes raw.qemu only while the VM is off: a clean shutdown, forced if it hangs
				if err := state("stop", false); err != nil {
					if err := state("stop", true); err != nil {
						return nil, nil, err
					}
				}
			}
			err = a.call(env.Ctx, env.Host, "POST", "/v1/vms/"+url.PathEscape(p.Name)+"/vnc", nil, nil)
			if run { // back on either way
				if serr := state("start", false); err == nil {
					err = serr
				}
			}
			return nil, nil, err
		},
	})
}

// POST …/vms/{name}/screen: a one-time key for the websocket, bound to this person.
func (a *App) handleVMScreenKey(w http.ResponseWriter, r *http.Request) {
	host, name := r.PathValue("host"), r.PathValue("name")
	in, err := a.vm(r.Context(), host, name)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if !in.VNC {
		a.fail(w, r, errs.New(409, "vm.no_screen", "name", name))
		return
	}
	key := randomPassword(24)
	vmScreens.Lock()
	for k, v := range vmScreens.m {
		if time.Now().After(v.exp) {
			delete(vmScreens.m, k)
		}
	}
	vmScreens.m[key] = vmScreen{host: host, name: name, user: currentUser(r).ID, exp: time.Now().Add(30 * time.Second)}
	vmScreens.Unlock()
	_, _ = a.pipe.Record(currentActor(r), host, "vm.screen", map[string]any{"name": name},
		change.Plan{Summary: "plan.vm.screen", SummaryP: map[string]any{"name": name}, Risk: change.RiskHigh,
			Affected: []change.Object{{Type: "vm", ID: name, Name: name, Host: host}}})
	writeJSON(w, 200, map[string]string{"key": key})
}

// GET /api/v1/vm-screen/{key}: relays noVNC to the agent, which relays to QEMU.
func (a *App) handleVMScreenWS(w http.ResponseWriter, r *http.Request) {
	vmScreens.Lock()
	s, ok := vmScreens.m[r.PathValue("key")]
	delete(vmScreens.m, r.PathValue("key"))
	vmScreens.Unlock()
	if !ok || time.Now().After(s.exp) || s.user != currentUser(r).ID {
		a.fail(w, r, errs.New(404, "pve.console_expired"))
		return
	}
	ag, err := a.agent(s.host)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	up, err := ag.Dial(ctx, "/v1/vms/"+url.PathEscape(s.name)+"/vnc")
	if err != nil {
		a.fail(w, r, errs.New(502, "vm.no_screen", "name", s.name))
		return
	}
	defer up.CloseNow()
	browser, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{"binary"}})
	if err != nil {
		return // a foreign origin: Accept answered 403
	}
	defer browser.CloseNow()
	browser.SetReadLimit(16 << 20)
	up.SetReadLimit(16 << 20)
	pipe := func(from, to *websocket.Conn) {
		defer cancel()
		for {
			typ, b, err := from.Read(ctx)
			if err != nil {
				return
			}
			if err := to.Write(ctx, typ, b); err != nil {
				return
			}
		}
	}
	go pipe(up, browser)
	pipe(browser, up)
}
