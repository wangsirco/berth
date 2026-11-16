package panel

// The internal image registry (方案 §5.3.3): registry:2 on one host, TLS from the
// panel's local CA, a generated password; every managed host trusts it and is logged in.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/wangsirco/berth/internal/change"
	"github.com/wangsirco/berth/internal/errs"
	"github.com/wangsirco/berth/internal/panel/certkit"
	"github.com/wangsirco/berth/internal/sign"
)

const (
	registryProject = "berth-registry"
	registryPort    = 5000
	registryName    = "registry.berth.lan"
)

func registryCompose() string {
	return fmt.Sprintf(`# Managed by Berth: the internal image registry.
name: %[1]s
services:
  registry:
    image: registry:2
    container_name: berth-registry
    restart: unless-stopped
    ports:
      - "%[2]d:5000"
    environment:
      REGISTRY_HTTP_TLS_CERTIFICATE: /certs/cert.pem
      REGISTRY_HTTP_TLS_KEY: /certs/key.pem
      REGISTRY_AUTH: htpasswd
      REGISTRY_AUTH_HTPASSWD_REALM: berth
      REGISTRY_AUTH_HTPASSWD_PATH: /auth/htpasswd
      REGISTRY_STORAGE_DELETE_ENABLED: "true"
    volumes:
      - /srv/berth/registry/data:/var/lib/registry
      - /srv/berth/registry/certs:/certs:ro
      - /srv/berth/registry/auth:/auth:ro
    mem_limit: 128m
`, registryProject, registryPort)
}

type registryInfo struct {
	Host string `json:"host"`
	Addr string `json:"addr"` // ip:5000, what images are tagged with
	User string `json:"user"`
	pass string
}

func (a *App) registry() (registryInfo, bool) {
	r := registryInfo{Host: a.setting("registry.host"), Addr: a.setting("registry.addr"), User: a.setting("registry.user")}
	if r.Addr == "" {
		return r, false
	}
	if enc, err := base64.StdEncoding.DecodeString(a.setting("registry.pass_enc")); err == nil {
		if p, err := a.open(enc); err == nil {
			r.pass = string(p)
		}
	}
	return r, true
}

// trustRegistry makes host's Docker trust and log in to the registry.
func (a *App) trustRegistry(ctx context.Context, host string) error {
	r, ok := a.registry()
	if !ok {
		return nil
	}
	return a.call(ctx, host, "POST", "/v1/docker/trust-registry", map[string]string{"addr": r.Addr, "ca": string(a.caPEM), "user": r.User, "pass": r.pass}, nil)
}

func (a *App) trustRegistryEverywhere(ctx context.Context) {
	for _, h := range a.connectedHosts() {
		c, cancel := context.WithTimeout(ctx, 10*time.Second)
		_ = a.trustRegistry(c, h)
		cancel()
	}
}

// registryCall is a registry API request made from the registry's own host.
func (a *App) registryCall(ctx context.Context, method, path string, accept ...string) (int, string, json.RawMessage, error) {
	r, ok := a.registry()
	if !ok {
		return 0, "", nil, errs.New(409, "registry.not_installed")
	}
	var out struct {
		Status int             `json:"status"`
		Digest string          `json:"digest"`
		Body   json.RawMessage `json:"body"`
	}
	err := a.call(ctx, r.Host, "POST", "/v1/registry/request", map[string]any{"method": method, "path": path, "addr": r.Addr,
		"ca": string(a.caPEM), "user": r.User, "pass": r.pass, "accept": accept}, &out)
	return out.Status, out.Digest, out.Body, err
}

var manifestTypes = []string{
	"application/vnd.oci.image.index.v1+json", "application/vnd.docker.distribution.manifest.list.v2+json",
	"application/vnd.oci.image.manifest.v1+json", "application/vnd.docker.distribution.manifest.v2+json",
}

type regTag struct {
	Repo     string `json:"repo"`
	Tag      string `json:"tag"`
	Digest   string `json:"digest"`
	PushedAt int64  `json:"pushed_at,omitempty"` // known when a Berth pipeline pushed it
}

func (a *App) registryTags(ctx context.Context) ([]regTag, error) {
	st, _, body, err := a.registryCall(ctx, "GET", "/v2/_catalog?n=1000")
	if err != nil {
		return nil, err
	}
	if st != 200 {
		return nil, errs.New(502, "registry.unreachable", "detail", "HTTP "+strconv.Itoa(st))
	}
	var cat struct{ Repositories []string }
	json.Unmarshal(body, &cat)
	known := map[string]int64{}
	rows, err := a.db.Query(`SELECT repo, tag, pushed_at FROM registry_tags`)
	if err == nil {
		for rows.Next() {
			var r, t string
			var at int64
			rows.Scan(&r, &t, &at)
			known[r+":"+t] = at
		}
		rows.Close()
	}
	var out []regTag
	for _, repo := range cat.Repositories {
		_, _, body, err := a.registryCall(ctx, "GET", "/v2/"+repo+"/tags/list")
		if err != nil {
			return nil, err
		}
		var tl struct{ Tags []string }
		json.Unmarshal(body, &tl)
		for _, t := range tl.Tags {
			_, dg, _, err := a.registryCall(ctx, "HEAD", "/v2/"+repo+"/manifests/"+t, manifestTypes...)
			if err != nil {
				return nil, err
			}
			out = append(out, regTag{Repo: repo, Tag: t, Digest: dg, PushedAt: known[repo+":"+t]})
		}
	}
	return out, nil
}

// pruneSelect picks digests to delete: per repository, everything older than the newest
// keep tags pushed by Berth. Tags Berth did not push (no time known), digests still
// referenced by a kept tag, and protected digests (deployed releases) stay.
func pruneSelect(tags []regTag, keep int, protected map[string]bool) []regTag {
	byRepo := map[string][]regTag{}
	for _, t := range tags {
		byRepo[t.Repo] = append(byRepo[t.Repo], t)
	}
	var out []regTag
	for _, list := range byRepo {
		var known []regTag
		keepDigest := map[string]bool{}
		for _, t := range list {
			if t.PushedAt == 0 || t.Tag == "latest" {
				keepDigest[t.Digest] = true
				continue
			}
			known = append(known, t)
		}
		sort.Slice(known, func(i, j int) bool { return known[i].PushedAt > known[j].PushedAt })
		for i, t := range known {
			if i < keep {
				keepDigest[t.Digest] = true
			}
		}
		seen := map[string]bool{}
		for _, t := range known {
			if keepDigest[t.Digest] || protected[t.Digest] || seen[t.Digest] || t.Digest == "" {
				continue
			}
			seen[t.Digest] = true
			out = append(out, t)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Repo+out[i].Tag < out[j].Repo+out[j].Tag })
	return out
}

// protectedDigests: images of current and recent releases (filled by the release module).
var protectedDigestsHook func(a *App) map[string]bool

func init() {
	routeHooks = append(routeHooks, func(a *App, mux *http.ServeMux) {
		mux.HandleFunc("GET /api/v1/registry", a.authed("pipeline.run", func(w http.ResponseWriter, r *http.Request) {
			reg, ok := a.registry()
			out := map[string]any{"installed": ok, "registry": reg}
			if ok && r.URL.Query().Get("tags") == "1" {
				ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
				defer cancel()
				tags, err := a.registryTags(ctx)
				if err != nil {
					a.fail(w, r, err)
					return
				}
				out["tags"] = tags
			}
			writeJSON(w, 200, out)
		}))
	})

	change.Register(change.Kind{
		Name: "registry.install", Perm: "pipeline.manage", Scope: "pipeline:write", Title: "kind.registry.install",
		Params: change.ParamSchema{Props: map[string]change.Prop{}}, CLI: "berth registry install", Heavy: true, MemMB: 128,
		Plan: func(env change.Env, _ json.RawMessage) (change.Plan, error) {
			a := svc(env)
			if r, ok := a.registry(); ok {
				return change.Plan{}, errs.New(409, "registry.installed", "addr", r.Addr, "host", r.Host)
			}
			if a.localSigner == nil {
				return change.Plan{}, errs.New(500, "cert.no_local_ca")
			}
			ip, err := a.registryIP(env.Ctx, env.Host)
			if err != nil {
				return change.Plan{}, err
			}
			addr := net.JoinHostPort(ip, strconv.Itoa(registryPort))
			return change.Plan{Summary: "plan.registry.install", SummaryP: map[string]any{"addr": addr}, Risk: change.RiskMedium,
				Files: []change.FileDiff{{Path: "/srv/berth/stacks/" + registryProject + "/compose.yaml", After: registryCompose()}},
				Commands: []string{"docker compose -p " + registryProject + " up -d",
					"# every managed host: /etc/docker/certs.d/" + addr + "/ca.crt and docker login " + addr},
				Affected: []change.Object{projObj(registryProject, env.Host)}, Undoable: true}, nil
		},
		Apply: func(env change.Env, _ json.RawMessage) (any, *change.UndoRecipe, error) {
			a := svc(env)
			ip, err := a.registryIP(env.Ctx, env.Host)
			if err != nil {
				return nil, nil, err
			}
			addr := net.JoinHostPort(ip, strconv.Itoa(registryPort))
			b, err := a.localSigner.Issue(certkit.Fields{DNS: []string{registryName}, IPs: []string{ip}, Days: 397})
			if err != nil {
				return nil, nil, certProblem(err)
			}
			pass := sign.RandomHex(16)
			hash, _ := bcrypt.GenerateFromPassword([]byte(pass), bcrypt.DefaultCost)
			if err := a.call(env.Ctx, env.Host, "PUT", "/v1/registry/files", map[string]string{"cert": string(b.CertPEM), "key": string(b.KeyPEM), "htpasswd": "berth:" + string(hash) + "\n"}, nil); err != nil {
				return nil, nil, err
			}
			if err := a.call(env.Ctx, env.Host, "PUT", "/v1/compose/projects/"+registryProject+"/files", map[string]any{"compose": registryCompose(), "create": true}, nil); err != nil {
				if e := errs.From(err); e.Code != "compose.exists" {
					return nil, nil, err
				}
				a.call(env.Ctx, env.Host, "PUT", "/v1/compose/projects/"+registryProject+"/files", map[string]any{"compose": registryCompose()}, nil)
			}
			if err := agentOp(env, "/v1/compose/projects/"+registryProject+"/up", nil); err != nil {
				return nil, nil, err
			}
			// an existing container keeps the old certificate and password until restarted
			if err := agentOp(env, "/v1/compose/projects/"+registryProject+"/restart", nil); err != nil {
				return nil, nil, err
			}
			enc, err := a.seal([]byte(pass))
			if err != nil {
				return nil, nil, err
			}
			for k, v := range map[string]string{"registry.host": env.Host, "registry.addr": addr, "registry.user": "berth", "registry.pass_enc": base64.StdEncoding.EncodeToString(enc)} {
				if err := a.setSetting(k, v); err != nil {
					return nil, nil, err
				}
			}
			a.trustRegistryEverywhere(env.Ctx)
			env.Log("registry %s is ready; managed hosts trust it", addr)
			return map[string]string{"addr": addr}, &change.UndoRecipe{Kind: "registry.remove", Params: json.RawMessage(`{}`)}, nil
		},
	})

	change.Register(change.Kind{
		Name: "registry.remove", Perm: "pipeline.manage", Scope: "pipeline:write", Title: "kind.registry.remove",
		Params: change.ParamSchema{Props: map[string]change.Prop{}}, CLI: "berth registry remove", Heavy: true,
		Plan: func(env change.Env, _ json.RawMessage) (change.Plan, error) {
			r, ok := svc(env).registry()
			if !ok {
				return change.Plan{}, errs.New(409, "registry.not_installed")
			}
			return change.Plan{Summary: "plan.registry.remove", SummaryP: map[string]any{"addr": r.Addr}, Risk: change.RiskHigh,
				Commands: []string{"docker compose -p " + registryProject + " down   # images in /srv/berth/registry/data are kept"},
				Affected: []change.Object{projObj(registryProject, r.Host)}}, nil
		},
		Apply: func(env change.Env, _ json.RawMessage) (any, *change.UndoRecipe, error) {
			a := svc(env)
			r, ok := a.registry()
			if !ok {
				return nil, nil, errs.New(409, "registry.not_installed")
			}
			if err := agentOp(change.Env{Ctx: env.Ctx, Host: r.Host, Log: env.Log, Svc: env.Svc}, "/v1/compose/projects/"+registryProject+"/down", nil); err != nil {
				return nil, nil, err
			}
			for _, k := range []string{"registry.host", "registry.addr", "registry.user", "registry.pass_enc"} {
				a.setSetting(k, "")
			}
			return nil, nil, nil
		},
	})

	change.Register(change.Kind{
		Name: "registry.prune", Perm: "pipeline.manage", Scope: "pipeline:write", Title: "kind.registry.prune",
		Params: change.ParamSchema{Props: map[string]change.Prop{"keep": {Type: "integer", Desc: "tags to keep per repository (default 10)"}}},
		CLI:    "berth registry prune --keep {{.keep}}", Heavy: true,
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			a := svc(env)
			sel, err := a.pruneCandidates(env.Ctx, raw)
			if err != nil {
				return change.Plan{}, err
			}
			p := change.Plan{Summary: "plan.registry.prune", SummaryP: map[string]any{"count": len(sel)}, Risk: change.RiskMedium}
			for _, t := range sel {
				p.Affected = append(p.Affected, change.Object{Type: "image", ID: t.Repo + "@" + t.Digest, Name: t.Repo + ":" + t.Tag})
			}
			return p, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			a := svc(env)
			sel, err := a.pruneCandidates(env.Ctx, raw)
			if err != nil {
				return nil, nil, err
			}
			for _, t := range sel {
				st, _, _, err := a.registryCall(env.Ctx, "DELETE", "/v2/"+t.Repo+"/manifests/"+t.Digest)
				if err != nil {
					return nil, nil, err
				}
				env.Log("deleted %s:%s (HTTP %d)", t.Repo, t.Tag, st)
				a.db.Exec(`DELETE FROM registry_tags WHERE repo=? AND tag=?`, t.Repo, t.Tag)
			}
			r, _ := a.registry()
			var gc struct {
				Output string `json:"output"`
			}
			if err := a.call(env.Ctx, r.Host, "POST", "/v1/registry/gc", nil, &gc); err != nil {
				env.Log("garbage collection: %v", err)
			}
			return map[string]int{"deleted": len(sel)}, nil, nil
		},
	})
}

func (a *App) pruneCandidates(ctx context.Context, raw json.RawMessage) ([]regTag, error) {
	var p struct{ Keep int }
	json.Unmarshal(raw, &p)
	if p.Keep <= 0 {
		p.Keep = 10
	}
	tags, err := a.registryTags(ctx)
	if err != nil {
		return nil, err
	}
	prot := map[string]bool{}
	if protectedDigestsHook != nil {
		prot = protectedDigestsHook(a)
	}
	return pruneSelect(tags, p.Keep, prot), nil
}

// registryIP: the host's first private address.
func (a *App) registryIP(ctx context.Context, host string) (string, error) {
	var addrs []string
	if err := a.call(ctx, host, "GET", "/v1/net/addrs", nil, &addrs); err != nil {
		return "", err
	}
	for _, s := range addrs {
		if ip := net.ParseIP(s); ip != nil && ip.To4() != nil && !ip.IsLoopback() && strings.Count(s, ".") == 3 {
			return s, nil
		}
	}
	return "", errs.New(409, "registry.no_address")
}
