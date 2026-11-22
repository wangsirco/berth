package panel

// Remote hosts (方案 §4.3). An agent on another machine is joined with a one-time code,
// then keeps a WebSocket open to the panel. A yamux session runs over it; the panel
// opens a stream per request and speaks the same signed HTTP as with the local agent.

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/hashicorp/yamux"

	"github.com/wangsirco/berth/internal/agent"
	"github.com/wangsirco/berth/internal/agentproto"
	"github.com/wangsirco/berth/internal/change"
	"github.com/wangsirco/berth/internal/errs"
	"github.com/wangsirco/berth/internal/sign"
)

const joinCodeTTL = 30 * time.Minute

var hostNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,30}$`)

func init() {
	routeHooks = append(routeHooks, func(a *App, mux *http.ServeMux) {
		mux.HandleFunc("POST /api/v1/hosts/join-codes", a.superOnly(a.handleJoinCode))
		mux.HandleFunc("GET /join/{code}", a.handleJoinScript)
		mux.HandleFunc("GET /join/{code}/berth", a.handleJoinBinary)
		mux.HandleFunc("POST /api/v1/agents/enroll", a.handleEnroll)
		mux.HandleFunc("GET /api/v1/agents/connect", a.handleAgentConnect)
	})

	change.Register(change.Kind{
		Name: "host.remove", Perm: "settings.manage", Scope: "settings:write", Title: "kind.host.remove",
		Params: change.ParamSchema{Props: map[string]change.Prop{"name": {Type: "string"}}, Required: []string{"name"}},
		CLI:    "berth host rm {{.name}}",
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			var p struct{ Name string }
			json.Unmarshal(raw, &p)
			if _, err := svc(env).remoteHost(p.Name); err != nil {
				return change.Plan{}, err
			}
			return change.Plan{Summary: "plan.host.remove", SummaryP: map[string]any{"name": p.Name}, Risk: change.RiskHigh,
				Affected: []change.Object{{Type: "host", ID: p.Name}}}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			var p struct{ Name string }
			json.Unmarshal(raw, &p)
			a := svc(env)
			if _, err := a.db.Exec(`DELETE FROM hosts WHERE name=?`, p.Name); err != nil {
				return nil, nil, err
			}
			a.dropRemote(p.Name, nil)
			return map[string]string{"name": p.Name}, nil, nil
		},
	})
}

type remoteHost struct {
	Name, KeyID, OS, Arch, Version, Addr string
	Secret                               []byte
	AddedAt, LastSeen                    int64
}

func (a *App) remoteHosts() []remoteHost {
	rows, err := a.db.Query(`SELECT name, key_id, os, arch, version, addr, added_at, last_seen FROM hosts ORDER BY name`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []remoteHost
	for rows.Next() {
		var h remoteHost
		if rows.Scan(&h.Name, &h.KeyID, &h.OS, &h.Arch, &h.Version, &h.Addr, &h.AddedAt, &h.LastSeen) == nil {
			out = append(out, h)
		}
	}
	return out
}

func (a *App) remoteHost(name string) (remoteHost, error) {
	return a.remoteHostBy("name", name)
}

func (a *App) remoteHostBy(col, v string) (remoteHost, error) {
	var h remoteHost
	var enc []byte
	err := a.db.QueryRow(`SELECT name, key_id, secret_enc, os, arch, version, addr, added_at, last_seen FROM hosts WHERE `+col+`=?`, v).
		Scan(&h.Name, &h.KeyID, &enc, &h.OS, &h.Arch, &h.Version, &h.Addr, &h.AddedAt, &h.LastSeen)
	if errors.Is(err, sql.ErrNoRows) {
		return h, errs.New(404, "host.not_found", "host", v)
	}
	if err != nil {
		return h, err
	}
	h.Secret, err = a.open(enc)
	return h, err
}

func codeHash(code string) string {
	h := sha256.Sum256([]byte("join:" + code))
	return hex.EncodeToString(h[:])
}

// panelBase is the URL remote agents should use: the one this request reached.
func panelBase(r *http.Request) string { return "https://" + r.Host }

// caPEMHash is what the join command checks the downloaded CA file against.
func (a *App) caPEMHash() string {
	h := sha256.Sum256(a.caPEM)
	return hex.EncodeToString(h[:])
}

func (a *App) handleJoinCode(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name string `json:"name"`
	}
	if err := readJSON(r, &in); err != nil {
		a.fail(w, r, err)
		return
	}
	in.Name = strings.ToLower(strings.TrimSpace(in.Name))
	if !hostNameRe.MatchString(in.Name) || in.Name == "local" {
		a.fail(w, r, errs.New(400, "host.bad_name"))
		return
	}
	if _, err := a.remoteHost(in.Name); err == nil {
		a.fail(w, r, errs.New(409, "host.exists", "name", in.Name))
		return
	}
	code := sign.RandomHex(12)
	exp := time.Now().Add(joinCodeTTL)
	if _, err := a.db.Exec(`INSERT INTO join_codes(code_hash, name, created_by, expires_at) VALUES(?,?,?,?)`,
		codeHash(code), in.Name, currentUser(r).ID, exp.Unix()); err != nil {
		a.fail(w, r, err)
		return
	}
	// Fetch the CA without verification, check its hash, then use it for everything else.
	mk := func(base string) string {
		return fmt.Sprintf(`curl -fsSk %[1]s/api/v1/ca.crt -o /tmp/berth-ca.crt && echo "%[2]s  /tmp/berth-ca.crt" | sha256sum -c --quiet && curl -fsS --cacert /tmp/berth-ca.crt %[1]s/join/%[3]s | { if [ "$(id -u)" = 0 ]; then sh; else sudo sh; fi; }`,
			base, a.caPEMHash(), code)
	}
	// one command per way to reach this panel: the LAN address, the public one and
	// the address this browser used (when it differs)
	type via struct {
		Via     string `json:"via"`
		Base    string `json:"base"`
		Command string `json:"command"`
	}
	var cmds []via
	seen := map[string]bool{}
	for _, v := range []via{{Via: "lan", Base: a.panelURL("panel.lan_host")}, {Via: "public", Base: a.panelURL("panel.public_host")}, {Via: "current", Base: panelBase(r)}} {
		if v.Base == "" || seen[v.Base] {
			continue
		}
		seen[v.Base] = true
		v.Command = mk(v.Base)
		cmds = append(cmds, v)
	}
	a.recordHostEvent(currentActor(r), "host.join_code", in.Name, map[string]any{"name": in.Name, "expires_at": exp.Unix()})
	writeJSON(w, 200, map[string]any{"code": code, "name": in.Name, "expires_at": exp.Unix(), "command": cmds[0].Command, "commands": cmds})
}

// pendingJoin returns the host name for a valid, unused code.
func (a *App) pendingJoin(code string) (string, error) {
	var name string
	var exp, used int64
	err := a.db.QueryRow(`SELECT name, expires_at, used_at FROM join_codes WHERE code_hash=?`, codeHash(code)).Scan(&name, &exp, &used)
	if err != nil || used != 0 || time.Now().Unix() > exp {
		return "", errs.New(404, "host.join_invalid")
	}
	return name, nil
}

var joinScript = `#!/bin/sh
# Berth: join this machine to %[1]s as host "%[2]s"
set -eu
[ "$(id -u)" = 0 ] || { echo "run as root" >&2; exit 1; }
# a server with its own Berth panel: its agent belongs to that panel; taking it over (and
# replacing the berth program the panel runs) would leave that panel without its server
if [ -f /etc/systemd/system/berth-panel.service ] || systemctl cat berth-panel.service >/dev/null 2>&1; then
  echo "这台服务器已经装了 Berth 面板，不能作为主机加入另一个面板（会让它自己的面板失去对这台服务器的控制）。什么也没改。" >&2
  echo "This server runs its own Berth panel; it cannot join another panel as a host (its own panel would lose it). Nothing was changed." >&2
  exit 2
fi
case "$(uname -m)" in x86_64|amd64) ARCH=amd64 ;; aarch64|arm64) ARCH=arm64 ;; *) echo "unsupported CPU: $(uname -m)" >&2; exit 1 ;; esac
CA=/tmp/berth-ca.crt
curl -fsS --cacert "$CA" -o /usr/local/bin/berth.new "%[1]s/join/%[3]s/berth?arch=$ARCH"
chmod 0755 /usr/local/bin/berth.new && mv /usr/local/bin/berth.new /usr/local/bin/berth
/usr/local/bin/berth agent enroll --panel "%[1]s" --code "%[3]s" --fp "%[4]s" --name "%[2]s"
cat > /etc/systemd/system/berth-agent.service <<'UNIT'
[Unit]
Description=Berth agent
After=network-online.target docker.service
Wants=network-online.target

[Service]
ExecStart=/usr/local/bin/berth agent
RuntimeDirectory=berth
RuntimeDirectoryMode=0750
Restart=always
RestartSec=2
MemoryHigh=48M
MemoryMax=96M

[Install]
WantedBy=multi-user.target
UNIT
systemctl daemon-reload
systemctl enable berth-agent >/dev/null 2>&1
systemctl restart berth-agent
rm -f "$CA"
echo "Berth agent installed; it will show up in the panel as \"%[2]s\" in a few seconds."
`

func (a *App) handleJoinScript(w http.ResponseWriter, r *http.Request) {
	code := r.PathValue("code")
	name, err := a.pendingJoin(code)
	if err != nil {
		blank404(w)
		return
	}
	w.Header().Set("Content-Type", "text/x-shellscript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	fmt.Fprintf(w, joinScript, panelBase(r), name, code, a.caFingerprint)
}

// agentBinary finds the berth binary for arch: a release copy in <data>/dist, or the
// one installed next to the panel when the architecture matches.
func (a *App) agentBinary(arch string) string {
	if arch != "amd64" && arch != "arm64" {
		return ""
	}
	cands := []string{filepath.Join(a.cfg.DataDir, "dist", "berth-linux-"+arch)}
	if arch == runtime.GOARCH {
		cands = append(cands, "/usr/local/bin/berth")
	}
	for _, c := range cands {
		if st, err := os.Stat(c); err == nil && st.Mode().IsRegular() {
			return c
		}
	}
	return ""
}

func (a *App) handleJoinBinary(w http.ResponseWriter, r *http.Request) {
	if _, err := a.pendingJoin(r.PathValue("code")); err != nil {
		blank404(w)
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

func (a *App) handleEnroll(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Code    string `json:"code"`
		Name    string `json:"name"`
		OS      string `json:"os"`
		Arch    string `json:"arch"`
		Version string `json:"version"`
	}
	if err := readJSON(r, &in); err != nil {
		a.fail(w, r, err)
		return
	}
	tx, err := a.db.Begin()
	if err != nil {
		a.fail(w, r, err)
		return
	}
	defer tx.Rollback()
	var name string
	var by int64
	res, err := tx.Exec(`UPDATE join_codes SET used_at=? WHERE code_hash=? AND used_at=0 AND expires_at>=?`,
		time.Now().Unix(), codeHash(in.Code), time.Now().Unix())
	if n, _ := res.RowsAffected(); err != nil || n != 1 {
		a.fail(w, r, errs.New(404, "host.join_invalid"))
		return
	}
	tx.QueryRow(`SELECT name, created_by FROM join_codes WHERE code_hash=?`, codeHash(in.Code)).Scan(&name, &by)
	if in.Name != "" && in.Name != name {
		a.fail(w, r, errs.New(400, "host.join_name_mismatch", "name", name))
		return
	}
	secret := []byte(sign.RandomHex(32))
	enc, err := a.seal(secret)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	keyID := "host_" + sign.RandomHex(6)
	if _, err := tx.Exec(`INSERT INTO hosts(name, key_id, secret_enc, os, arch, version, added_at, addr) VALUES(?,?,?,?,?,?,?,?)`,
		name, keyID, enc, in.OS, in.Arch, in.Version, time.Now().Unix(), a.clientIP(r)); err != nil {
		a.fail(w, r, errs.New(409, "host.exists", "name", name))
		return
	}
	if err := tx.Commit(); err != nil {
		a.fail(w, r, err)
		return
	}
	a.recordHostEvent(change.Actor{Type: "agent", ID: keyID, Name: name, Via: "api"}, "host.enroll", name,
		map[string]any{"name": name, "arch": in.Arch, "os": in.OS, "addr": a.clientIP(r), "code_by": by})
	writeJSON(w, 200, map[string]any{"name": name, "key_id": keyID, "secret": base64.StdEncoding.EncodeToString(secret), "fp": a.caFingerprint})
}

func (a *App) handleAgentConnect(w http.ResponseWriter, r *http.Request) {
	h, err := a.remoteHostBy("key_id", r.Header.Get("X-Berth-Agent-Key"))
	if err != nil {
		blank404(w)
		return
	}
	if _, err := sign.NewVerifier(map[string][]byte{h.KeyID: h.Secret}).Verify(r); err != nil {
		http.Error(w, "bad signature", http.StatusUnauthorized)
		return
	}
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	conn.SetReadLimit(-1)
	ctx, cancel := context.WithCancel(context.Background()) // outlives the handler's request context
	nc := websocket.NetConn(ctx, conn, websocket.MessageBinary)
	sess, err := yamux.Client(nc, agent.YamuxConfig())
	if err != nil {
		cancel()
		nc.Close()
		return
	}
	client := agentproto.NewDialClient(func(ctx context.Context) (net.Conn, error) {
		type res struct {
			c   net.Conn
			err error
		}
		ch := make(chan res, 1)
		go func() { c, err := sess.Open(); ch <- res{c, err} }()
		select {
		case v := <-ch:
			return v.c, v.err
		case <-ctx.Done():
			go func() {
				if v := <-ch; v.c != nil {
					v.c.Close()
				}
			}()
			return nil, ctx.Err()
		}
	}, h.KeyID, h.Secret)
	addr := a.clientIP(r)
	a.db.Exec(`UPDATE hosts SET version=?, arch=COALESCE(NULLIF(?,''),arch), addr=?, last_seen=? WHERE name=?`,
		r.Header.Get("X-Berth-Agent-Version"), r.Header.Get("X-Berth-Agent-Arch"), addr, time.Now().Unix(), h.Name)
	a.agentsMu.Lock()
	if old, ok := a.remotes[h.Name]; ok {
		old.Close() // a reconnect replaces the old session
	}
	a.agents[h.Name] = client
	a.remotes[h.Name] = sess
	a.agentsMu.Unlock()
	log.Printf("panel: host %s connected from %s", h.Name, addr)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if err := a.trustRegistry(ctx, h.Name); err != nil {
			log.Printf("panel: registry trust on %s: %v", h.Name, err)
		}
	}()
	a.hub.publish(event{Type: "host.online", Data: map[string]any{"host": h.Name}})

	<-sess.CloseChan()
	cancel()
	if a.dropRemote(h.Name, sess) {
		a.db.Exec(`UPDATE hosts SET last_seen=? WHERE name=?`, time.Now().Unix(), h.Name)
		log.Printf("panel: host %s disconnected", h.Name)
		a.hub.publish(event{Type: "host.offline", Data: map[string]any{"host": h.Name}})
	}
}

func (a *App) recordHostEvent(ac change.Actor, kind, name string, params any) {
	_, _ = a.pipe.Record(ac, name, kind, params, change.Plan{
		Summary: "plan." + kind, SummaryP: map[string]any{"name": name}, Risk: change.RiskHigh,
		Affected: []change.Object{{Type: "host", ID: name}},
	})
}

// dropRemote forgets host's session (only if it is still sess, when sess is given).
func (a *App) dropRemote(host string, sess *yamux.Session) bool {
	a.agentsMu.Lock()
	defer a.agentsMu.Unlock()
	cur, ok := a.remotes[host]
	if !ok || (sess != nil && cur != sess) {
		return false
	}
	cur.Close()
	delete(a.remotes, host)
	delete(a.agents, host)
	return true
}
