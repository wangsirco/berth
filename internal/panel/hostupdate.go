package panel

// One-click agent upgrade for remote hosts (远程主机一键升级): the remote agent fetches this
// panel's own berth binary (signed request, pinned certificate), swaps it in, and a watchdog
// on that host restores the old one if the new agent does not reconnect.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/wangsirco/berth/internal/change"
	"github.com/wangsirco/berth/internal/errs"
	"github.com/wangsirco/berth/internal/sign"
	"github.com/wangsirco/berth/internal/version"
)

var binVerCache sync.Map // path|mtime -> version

// binVersion asks a berth binary for its version (when it can run here), else assumes ours.
func binVersion(p string) string {
	st, err := os.Stat(p)
	if err != nil {
		return version.Version
	}
	key := p + "|" + st.ModTime().String()
	if v, ok := binVerCache.Load(key); ok {
		return v.(string)
	}
	v := version.Version
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if out, err := exec.CommandContext(ctx, p, "version").Output(); err == nil {
		if s, ok := strings.CutPrefix(strings.TrimSpace(string(out)), "berth "); ok && s != "" {
			v = s
		}
	}
	binVerCache.Store(key, v)
	return v
}

func fileSHA256(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// handleAgentBinary serves the berth binary to an enrolled agent (signed with its own key).
func (a *App) handleAgentBinary(w http.ResponseWriter, r *http.Request) {
	h, err := a.remoteHostBy("key_id", r.Header.Get("X-Berth-Agent-Key"))
	if err != nil {
		blank404(w)
		return
	}
	if _, err := sign.NewVerifier(map[string][]byte{h.KeyID: h.Secret}).Verify(r); err != nil {
		http.Error(w, "bad signature", http.StatusUnauthorized)
		return
	}
	p := a.agentBinary(r.URL.Query().Get("arch"))
	if p == "" {
		http.Error(w, "no berth binary for this architecture on the panel", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	http.ServeFile(w, r, p)
}

func init() {
	routeHooks = append(routeHooks, func(a *App, mux *http.ServeMux) {
		mux.HandleFunc("GET /api/v1/agents/binary", a.handleAgentBinary)
	})
	change.Register(change.Kind{
		Name: "host.agent_update", Perm: "settings.manage", Scope: "settings:write", Title: "kind.host.agent_update",
		Params: change.ParamSchema{Props: map[string]change.Prop{"name": {Type: "string", Desc: "a remote host"}}, Required: []string{"name"}},
		CLI:    "berth host upgrade {{.name}}",
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			var p struct{ Name string }
			json.Unmarshal(raw, &p)
			a := svc(env)
			h, err := a.remoteHost(p.Name)
			if err != nil {
				return change.Plan{}, err
			}
			bin := a.agentBinary(h.Arch)
			if bin == "" {
				return change.Plan{}, errs.New(400, "host.no_binary", "arch", h.Arch)
			}
			to := binVersion(bin)
			return change.Plan{Summary: "plan.host.agent_update", SummaryP: map[string]any{"name": p.Name, "from": h.Version, "to": to}, Risk: change.RiskMedium,
				Commands: []string{
					"(on " + p.Name + ") download berth " + to + " from this panel, check its SHA-256 and that it runs",
					"keep the current binary in /var/lib/berth/rollback/agent-…",
					"restart berth-agent; if " + to + " has not reconnected in 60 s, put the old binary back",
				},
				Affected: []change.Object{{Type: "host", ID: p.Name}}}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			var p struct{ Name string }
			json.Unmarshal(raw, &p)
			a := svc(env)
			h, err := a.remoteHost(p.Name)
			if err != nil {
				return nil, nil, err
			}
			bin := a.agentBinary(h.Arch)
			sum, err := fileSHA256(bin)
			if err != nil {
				return nil, nil, err
			}
			to := binVersion(bin)
			env.Log("asking %s to fetch berth %s (sha256 %s…)", p.Name, to, sum[:12])
			var out map[string]string
			if err := a.call(env.Ctx, p.Name, "POST", "/v1/self/agent-update", map[string]string{"sha256": sum, "version": to}, &out); err != nil {
				return nil, nil, err
			}
			if out["to"] != "" {
				to = out["to"]
			}
			env.Log("installed %s; waiting for it to reconnect", to)
			start := time.Now()
			time.Sleep(3 * time.Second)
			for time.Since(start) < 90*time.Second {
				if h2, err := a.remoteHost(p.Name); err == nil && h2.Version == to && h2.LastSeen >= start.Unix() {
					env.Log("%s is back on %s", p.Name, to)
					return map[string]string{"from": h.Version, "to": to}, nil, nil
				}
				time.Sleep(2 * time.Second)
			}
			return nil, nil, errs.New(504, "host.agent_update_timeout", "name", p.Name)
		},
	})
}
