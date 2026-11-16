package panel

// Existing Proxmox VE clusters (方案 §5.4.2): added under 设置 → 集成 with an API token (the
// panel shows how to make one with the least rights, and the server certificate's fingerprint
// to confirm); their nodes, VMs and containers then show on the 虚拟机 page next to this
// server's own, with power, snapshots, the noVNC console (through the panel, so the browser
// needs neither a PVE login nor to trust its certificate) and "make a site" from a guest's IP.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/wangsirco/berth/internal/change"
	"github.com/wangsirco/berth/internal/errs"
	"github.com/wangsirco/berth/internal/pve"
)

type pveCluster struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	URL         string `json:"url"`
	TokenID     string `json:"token_id"`
	Fingerprint string `json:"fingerprint,omitempty"`
	CreatedAt   int64  `json:"created_at"`
}

var pveIDRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)

func (a *App) pveClusters() []pveCluster {
	out := []pveCluster{}
	rows, err := a.db.Query(`SELECT id, name, url, token_id, fingerprint, created_at FROM pve_clusters ORDER BY created_at`)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var c pveCluster
		rows.Scan(&c.ID, &c.Name, &c.URL, &c.TokenID, &c.Fingerprint, &c.CreatedAt)
		out = append(out, c)
	}
	return out
}

func (a *App) pveClient(id string) (*pve.Client, pveCluster, error) {
	var c pveCluster
	var enc []byte
	err := a.db.QueryRow(`SELECT id, name, url, token_id, fingerprint, secret_enc FROM pve_clusters WHERE id=?`, id).
		Scan(&c.ID, &c.Name, &c.URL, &c.TokenID, &c.Fingerprint, &enc)
	if err != nil {
		return nil, c, errs.New(404, "pve.not_found", "id", id)
	}
	sec, err := a.open(enc)
	if err != nil {
		return nil, c, err
	}
	return pve.New(pve.Config{URL: c.URL, TokenID: c.TokenID, Secret: string(sec), Fingerprint: c.Fingerprint}), c, nil
}

// guestRef names one guest in params of the pve.* kinds.
type guestRef struct {
	Cluster string `json:"cluster"`
	Node    string `json:"node"`
	Type    string `json:"type"`
	VMID    int    `json:"vmid"`
	Name    string `json:"name"` // display only
}

func (g guestRef) obj() change.Object {
	n := g.Name
	if n == "" {
		n = strconv.Itoa(g.VMID)
	}
	return change.Object{Type: "pve", ID: fmt.Sprintf("%s/%s/%d", g.Cluster, g.Node, g.VMID), Name: n + " (" + g.Cluster + ")"}
}

func (a *App) pveGuest(raw json.RawMessage) (*pve.Client, guestRef, error) {
	var g guestRef
	json.Unmarshal(raw, &g)
	c, _, err := a.pveClient(g.Cluster)
	if err != nil {
		return nil, g, err
	}
	if g.Type != "qemu" && g.Type != "lxc" || g.VMID < 100 || g.Node == "" {
		return nil, g, errs.New(400, "pve.bad_guest")
	}
	return c, g, nil
}

// --- the console: a one-time ticket, then a websocket relayed through the panel ------

type pveConsole struct {
	cluster, node, typ string
	vmid               int
	ticket             pve.VNCTicket
	user               int64
	exp                time.Time
}

var pveConsoles = struct {
	sync.Mutex
	m map[string]pveConsole
}{m: map[string]pveConsole{}}

func init() {
	routeHooks = append(routeHooks, func(a *App, mux *http.ServeMux) {
		mux.HandleFunc("GET /api/v1/pve", a.authed("vms.view", a.handlePVE))
		mux.HandleFunc("POST /api/v1/pve/probe", a.authed("settings.manage", func(w http.ResponseWriter, r *http.Request) {
			var in struct {
				URL string `json:"url"`
			}
			if err := readJSON(r, &in); err != nil {
				a.fail(w, r, err)
				return
			}
			u, err := pve.NormalizeURL(in.URL)
			if err != nil {
				a.fail(w, r, err)
				return
			}
			ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
			defer cancel()
			fp, subject, trusted, err := pve.Probe(ctx, u)
			if err != nil {
				a.fail(w, r, err)
				return
			}
			writeJSON(w, 200, map[string]any{"url": u, "fingerprint": fp, "fingerprint_text": pve.FormatFP(fp), "subject": subject, "trusted": trusted})
		}))
		mux.HandleFunc("GET /api/v1/pve/{id}/guests/{node}/{type}/{vmid}", a.authed("vms.view", a.handlePVEGuest))
		mux.HandleFunc("POST /api/v1/pve/{id}/guests/{node}/{type}/{vmid}/console", a.authed("vms.manage", a.handlePVEConsoleTicket))
		mux.HandleFunc("GET /api/v1/pve/console/{key}", a.authed("vms.manage", a.handlePVEConsoleWS))
	})

	setProps := map[string]change.Prop{
		"id":          {Type: "string", Desc: "short id (a-z, 0-9, -); a new one when absent"},
		"name":        {Type: "string", Desc: "display name"},
		"url":         {Type: "string", Desc: "https://pve.lan:8006 (any node of the cluster)"},
		"token_id":    {Type: "string", Desc: "user@realm!tokenname, e.g. berth@pve!panel"},
		"secret":      {Type: "string", Desc: "the token's secret (UUID); kept sealed"},
		"fingerprint": {Type: "string", Desc: "SHA-256 of the server certificate (from /api/v1/pve/probe); empty when it has a trusted certificate"},
	}
	change.Register(change.Kind{
		Name: "pve.set", Perm: "settings.manage", Scope: "settings:write", Title: "kind.pve.set",
		Params: change.ParamSchema{Props: setProps, Required: []string{"url", "token_id"}}, Redact: []string{"secret"},
		CLI: "berth pve set {{.url}} --token-id {{.token_id}}",
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			a := svc(env)
			p, old, err := a.pveSetParams(raw)
			if err != nil {
				return change.Plan{}, err
			}
			pl := change.Plan{Summary: "plan.pve.set", SummaryP: map[string]any{"name": p.Name, "url": p.URL}, Risk: change.RiskLow, Undoable: old == nil,
				Affected: []change.Object{{Type: "pve", ID: p.ID, Name: p.Name}},
				Commands: []string{"GET " + p.URL + "/api2/json/version   # token " + p.TokenID}}
			if p.Fingerprint != "" {
				pl.Commands = append(pl.Commands, "# certificate pinned: "+pve.FormatFP(p.Fingerprint))
			}
			return pl, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			a := svc(env)
			p, old, err := a.pveSetParams(raw)
			if err != nil {
				return nil, nil, err
			}
			secret := p.Secret
			if secret == "" && old != nil { // keeping the stored one
				var enc []byte
				a.db.QueryRow(`SELECT secret_enc FROM pve_clusters WHERE id=?`, p.ID).Scan(&enc)
				b, err := a.open(enc)
				if err != nil {
					return nil, nil, err
				}
				secret = string(b)
			}
			if secret == "" {
				return nil, nil, errs.New(400, "pve.no_secret")
			}
			// it must answer, with this token, before it is saved
			c := pve.New(pve.Config{URL: p.URL, TokenID: p.TokenID, Secret: secret, Fingerprint: p.Fingerprint})
			ctx, cancel := context.WithTimeout(env.Ctx, 15*time.Second)
			defer cancel()
			v, err := c.Version(ctx)
			if err != nil {
				return nil, nil, err
			}
			if _, err := c.Guests(ctx); err != nil {
				return nil, nil, err
			}
			env.Log("Proxmox VE %s", v.Version)
			enc, err := a.seal([]byte(secret))
			if err != nil {
				return nil, nil, err
			}
			if _, err := a.db.Exec(`INSERT INTO pve_clusters(id, name, url, token_id, fingerprint, secret_enc, created_by, created_at) VALUES(?,?,?,?,?,?,?,?)
				ON CONFLICT(id) DO UPDATE SET name=excluded.name, url=excluded.url, token_id=excluded.token_id, fingerprint=excluded.fingerprint, secret_enc=excluded.secret_enc`,
				p.ID, p.Name, p.URL, p.TokenID, p.Fingerprint, enc, env.ActorName, time.Now().Unix()); err != nil {
				return nil, nil, err
			}
			var undo *change.UndoRecipe
			if old == nil {
				undo = &change.UndoRecipe{Kind: "pve.delete", Params: mustJSON(map[string]string{"id": p.ID})}
			}
			return map[string]any{"id": p.ID, "version": v.Version}, undo, nil
		},
	})
	change.Register(change.Kind{
		Name: "pve.delete", Perm: "settings.manage", Scope: "settings:write", Title: "kind.pve.delete",
		Params: change.ParamSchema{Props: map[string]change.Prop{"id": {Type: "string"}}, Required: []string{"id"}}, CLI: "berth pve delete {{.id}}",
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			var p struct{ ID string }
			json.Unmarshal(raw, &p)
			_, c, err := svc(env).pveClient(p.ID)
			if err != nil {
				return change.Plan{}, err
			}
			return change.Plan{Summary: "plan.pve.delete", SummaryP: map[string]any{"name": c.Name}, Risk: change.RiskLow,
				Affected: []change.Object{{Type: "pve", ID: c.ID, Name: c.Name}}, Commands: []string{"# only the panel forgets it: nothing changes on the PVE side (revoke the token there if you like)"}}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			var p struct{ ID string }
			json.Unmarshal(raw, &p)
			_, err := svc(env).db.Exec(`DELETE FROM pve_clusters WHERE id=?`, p.ID)
			return map[string]string{"id": p.ID}, nil, err
		},
	})

	refProps := func(extra map[string]change.Prop) map[string]change.Prop {
		m := map[string]change.Prop{"cluster": {Type: "string"}, "node": {Type: "string"}, "type": {Type: "string", Enum: []string{"qemu", "lxc"}},
			"vmid": {Type: "integer"}, "name": {Type: "string", Desc: "the guest's name (for the record)"}}
		for k, v := range extra {
			m[k] = v
		}
		return m
	}
	inverse := map[string]string{"start": "shutdown", "shutdown": "start", "stop": "start", "suspend": "resume", "resume": "suspend"}
	change.Register(change.Kind{
		Name: "pve.power", Perm: "vms.manage", Scope: "vms:write", Title: "kind.pve.power",
		Params: change.ParamSchema{Props: refProps(map[string]change.Prop{"action": {Type: "string", Enum: []string{"start", "shutdown", "stop", "reboot", "suspend", "resume"}}}),
			Required: []string{"cluster", "node", "type", "vmid", "action"}},
		CLI: "berth pve {{.action}} {{.cluster}} {{.vmid}}",
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			_, g, err := svc(env).pveGuest(raw)
			if err != nil {
				return change.Plan{}, err
			}
			var p struct{ Action string }
			json.Unmarshal(raw, &p)
			if !pve.ValidPower(p.Action) {
				return change.Plan{}, errs.New(400, "pve.bad_action", "action", p.Action)
			}
			risk := change.RiskLow
			if p.Action == "stop" || p.Action == "reboot" {
				risk = change.RiskMedium
			}
			return change.Plan{Summary: "plan.pve.power." + p.Action, SummaryP: map[string]any{"name": g.obj().Name}, Risk: risk, Undoable: inverse[p.Action] != "",
				Affected: []change.Object{g.obj()}, Commands: []string{fmt.Sprintf("POST /nodes/%s/%s/%d/status/%s", g.Node, g.Type, g.VMID, p.Action)}}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			c, g, err := svc(env).pveGuest(raw)
			if err != nil {
				return nil, nil, err
			}
			var p struct{ Action string }
			json.Unmarshal(raw, &p)
			if err := c.Power(env.Ctx, g.Node, g.Type, g.VMID, p.Action); err != nil {
				return nil, nil, err
			}
			var undo *change.UndoRecipe
			if inv := inverse[p.Action]; inv != "" {
				undo = &change.UndoRecipe{Kind: "pve.power", Params: mustJSON(map[string]any{"cluster": g.Cluster, "node": g.Node, "type": g.Type, "vmid": g.VMID, "name": g.Name, "action": inv})}
			}
			return map[string]any{"vmid": g.VMID, "action": p.Action}, undo, nil
		},
	})
	change.Register(change.Kind{
		Name: "pve.snapshot", Perm: "vms.manage", Scope: "vms:write", Title: "kind.pve.snapshot",
		Params: change.ParamSchema{Props: refProps(map[string]change.Prop{"snapshot": {Type: "string", Desc: "letters, digits, - and _ (starts with a letter)"},
			"description": {Type: "string"}, "vmstate": {Type: "boolean", Desc: "VMs: include the RAM, so a restore resumes where it was"}}),
			Required: []string{"cluster", "node", "type", "vmid", "snapshot"}},
		CLI: "berth pve snapshot {{.cluster}} {{.vmid}} {{.snapshot}}",
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			_, g, err := svc(env).pveGuest(raw)
			if err != nil {
				return change.Plan{}, err
			}
			var p struct{ Snapshot string }
			json.Unmarshal(raw, &p)
			if !pve.ValidSnapshot(p.Snapshot) {
				return change.Plan{}, errs.New(400, "pve.bad_snapshot", "name", p.Snapshot)
			}
			return change.Plan{Summary: "plan.pve.snapshot", SummaryP: map[string]any{"name": g.obj().Name, "snapshot": p.Snapshot}, Risk: change.RiskLow, Undoable: true,
				Affected: []change.Object{g.obj()}, Commands: []string{fmt.Sprintf("POST /nodes/%s/%s/%d/snapshot snapname=%s", g.Node, g.Type, g.VMID, p.Snapshot)}}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			c, g, err := svc(env).pveGuest(raw)
			if err != nil {
				return nil, nil, err
			}
			var p struct {
				Snapshot, Description string
				VMState               bool
			}
			json.Unmarshal(raw, &p)
			if err := c.Snapshot(env.Ctx, g.Node, g.Type, g.VMID, p.Snapshot, p.Description, p.VMState); err != nil {
				return nil, nil, err
			}
			undo := &change.UndoRecipe{Kind: "pve.snapshot_delete", Params: mustJSON(map[string]any{"cluster": g.Cluster, "node": g.Node, "type": g.Type, "vmid": g.VMID, "name": g.Name, "snapshot": p.Snapshot})}
			return map[string]any{"snapshot": p.Snapshot}, undo, nil
		},
	})
	snapOp := func(name, summary string, risk change.Risk, cmd func(g guestRef, s string) string, do func(ctx context.Context, c *pve.Client, g guestRef, s string) error) {
		change.Register(change.Kind{
			Name: name, Perm: "vms.manage", Scope: "vms:write", Title: "kind." + name,
			Params: change.ParamSchema{Props: refProps(map[string]change.Prop{"snapshot": {Type: "string"}}), Required: []string{"cluster", "node", "type", "vmid", "snapshot"}},
			CLI:    "berth pve " + strings.TrimPrefix(name, "pve.") + " {{.cluster}} {{.vmid}} {{.snapshot}}",
			Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
				_, g, err := svc(env).pveGuest(raw)
				if err != nil {
					return change.Plan{}, err
				}
				var p struct{ Snapshot string }
				json.Unmarshal(raw, &p)
				if !pve.ValidSnapshot(p.Snapshot) {
					return change.Plan{}, errs.New(400, "pve.bad_snapshot", "name", p.Snapshot)
				}
				return change.Plan{Summary: summary, SummaryP: map[string]any{"name": g.obj().Name, "snapshot": p.Snapshot}, Risk: risk,
					Affected: []change.Object{g.obj()}, Commands: []string{cmd(g, p.Snapshot)}}, nil
			},
			Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
				c, g, err := svc(env).pveGuest(raw)
				if err != nil {
					return nil, nil, err
				}
				var p struct{ Snapshot string }
				json.Unmarshal(raw, &p)
				return map[string]any{"snapshot": p.Snapshot}, nil, do(env.Ctx, c, g, p.Snapshot)
			},
		})
	}
	snapOp("pve.snapshot_delete", "plan.pve.snapshot_delete", change.RiskMedium,
		func(g guestRef, s string) string {
			return fmt.Sprintf("DELETE /nodes/%s/%s/%d/snapshot/%s", g.Node, g.Type, g.VMID, s)
		},
		func(ctx context.Context, c *pve.Client, g guestRef, s string) error {
			return c.DeleteSnapshot(ctx, g.Node, g.Type, g.VMID, s)
		})
	snapOp("pve.rollback", "plan.pve.rollback", change.RiskHigh,
		func(g guestRef, s string) string {
			return fmt.Sprintf("POST /nodes/%s/%s/%d/snapshot/%s/rollback   # everything after it is lost", g.Node, g.Type, g.VMID, s)
		},
		func(ctx context.Context, c *pve.Client, g guestRef, s string) error {
			return c.Rollback(ctx, g.Node, g.Type, g.VMID, s)
		})
}

type pveSetIn struct {
	ID, Name, URL, Secret, Fingerprint string
	TokenID                            string `json:"token_id"`
}

func (a *App) pveSetParams(raw json.RawMessage) (pveSetIn, *pveCluster, error) {
	var p pveSetIn
	json.Unmarshal(raw, &p)
	u, err := pve.NormalizeURL(p.URL)
	if err != nil {
		return p, nil, err
	}
	p.URL, p.TokenID, p.Secret = u, strings.TrimSpace(p.TokenID), strings.TrimSpace(p.Secret)
	p.Fingerprint = strings.ToLower(strings.ReplaceAll(strings.TrimSpace(p.Fingerprint), ":", ""))
	if !pve.ValidTokenID(p.TokenID) {
		return p, nil, errs.New(400, "pve.bad_token_id")
	}
	if p.Fingerprint != "" && !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(p.Fingerprint) {
		return p, nil, errs.New(400, "pve.bad_fingerprint")
	}
	if p.Name == "" {
		p.Name = strings.TrimPrefix(strings.TrimPrefix(u, "https://"), "http://")
	}
	if p.ID == "" {
		p.ID = slugID(p.Name)
		for i, base := 2, p.ID; ; i++ {
			if _, _, err := a.pveClient(p.ID); err != nil {
				break
			}
			p.ID = fmt.Sprintf("%s-%d", base, i)
		}
		return p, nil, nil
	}
	if !pveIDRe.MatchString(p.ID) {
		return p, nil, errs.New(400, "pve.bad_id")
	}
	if _, c, err := a.pveClient(p.ID); err == nil {
		return p, &c, nil
	}
	return p, nil, nil
}

func slugID(s string) string {
	s = strings.ToLower(s)
	s = regexp.MustCompile(`[^a-z0-9]+`).ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if len(s) > 24 {
		s = strings.Trim(s[:24], "-")
	}
	if s == "" {
		s = "pve"
	}
	return s
}

// GET /api/v1/pve: every cluster with its nodes and guests (a cluster that does not answer
// carries its error instead; the others still show).
func (a *App) handlePVE(w http.ResponseWriter, r *http.Request) {
	list := a.pveClusters()
	out := make([]map[string]any, len(list))
	var wg sync.WaitGroup
	for i, c := range list {
		out[i] = map[string]any{"cluster": c, "fingerprint_text": pve.FormatFP(c.Fingerprint)}
		wg.Add(1)
		go func(i int, id string) {
			defer wg.Done()
			cl, _, err := a.pveClient(id)
			if err != nil {
				out[i]["error"] = errs.From(err)
				return
			}
			ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
			defer cancel()
			v, err := cl.Version(ctx)
			if err != nil {
				out[i]["error"] = errs.From(err)
				return
			}
			nodes, _ := cl.Nodes(ctx)
			guests, err := cl.Guests(ctx)
			if err != nil {
				out[i]["error"] = errs.From(err)
			}
			if guests == nil {
				guests = []pve.Guest{}
			}
			out[i]["version"], out[i]["nodes"], out[i]["guests"] = v.Version, nodes, guests
		}(i, c.ID)
	}
	wg.Wait()
	writeJSON(w, 200, map[string]any{"items": out})
}

func pveGuestPath(r *http.Request) (string, string, int) {
	vmid, _ := strconv.Atoi(r.PathValue("vmid"))
	return r.PathValue("node"), r.PathValue("type"), vmid
}

// GET /api/v1/pve/{id}/guests/{node}/{type}/{vmid}: settings, addresses, snapshots.
func (a *App) handlePVEGuest(w http.ResponseWriter, r *http.Request) {
	c, _, err := a.pveClient(r.PathValue("id"))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	node, typ, vmid := pveGuestPath(r)
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	cfg, err := c.Config(ctx, node, typ, vmid)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	snaps, err := c.Snapshots(ctx, node, typ, vmid)
	if snaps == nil {
		snaps = []pve.Snapshot{}
	}
	res := map[string]any{"config": cfg, "snapshots": snaps, "ips": c.Addresses(ctx, node, typ, vmid)}
	if err != nil {
		res["snapshots_error"] = errs.From(err)
	}
	writeJSON(w, 200, res)
}

// POST …/console: a VNC ticket from PVE; the browser gets a one-time key for the websocket
// and the ticket (it is the VNC password noVNC answers with).
func (a *App) handlePVEConsoleTicket(w http.ResponseWriter, r *http.Request) {
	if err := a.tokenScope(r, "vms:write"); err != nil {
		a.fail(w, r, err)
		return
	}
	c, cl, err := a.pveClient(r.PathValue("id"))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	node, typ, vmid := pveGuestPath(r)
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	t, err := c.VNCProxy(ctx, node, typ, vmid)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	key := randomPassword(24)
	pveConsoles.Lock()
	for k, v := range pveConsoles.m {
		if time.Now().After(v.exp) {
			delete(pveConsoles.m, k)
		}
	}
	pveConsoles.m[key] = pveConsole{cluster: cl.ID, node: node, typ: typ, vmid: vmid, ticket: t, user: currentUser(r).ID, exp: time.Now().Add(30 * time.Second)}
	pveConsoles.Unlock()
	_, _ = a.pipe.Record(currentActor(r), "local", "pve.console", map[string]any{"cluster": cl.ID, "node": node, "type": typ, "vmid": vmid},
		change.Plan{Summary: "plan.pve.console", SummaryP: map[string]any{"name": fmt.Sprintf("%d (%s)", vmid, cl.Name)}, Risk: change.RiskHigh,
			Affected: []change.Object{guestRef{Cluster: cl.ID, Node: node, Type: typ, VMID: vmid}.obj()}})
	writeJSON(w, 200, map[string]string{"key": key, "password": t.Ticket})
}

// GET /api/v1/pve/console/{key}: relays the browser's websocket (noVNC) to PVE's vncwebsocket.
func (a *App) handlePVEConsoleWS(w http.ResponseWriter, r *http.Request) {
	pveConsoles.Lock()
	s, ok := pveConsoles.m[r.PathValue("key")]
	delete(pveConsoles.m, r.PathValue("key"))
	pveConsoles.Unlock()
	if !ok || time.Now().After(s.exp) || s.user != currentUser(r).ID {
		a.fail(w, r, errs.New(404, "pve.console_expired"))
		return
	}
	c, _, err := a.pveClient(s.cluster)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	u, h, hc, err := c.VNCSocket(s.node, s.typ, s.vmid, s.ticket)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	up, _, err := websocket.Dial(ctx, u, &websocket.DialOptions{HTTPHeader: h, HTTPClient: hc, Subprotocols: []string{"binary"}})
	if err != nil {
		a.fail(w, r, errs.New(502, "pve.unreachable", "url", "vncwebsocket", "detail", err.Error()))
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
