package panel

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/wangsirco/berth/internal/agent/docker"
	"github.com/wangsirco/berth/internal/change"
	"github.com/wangsirco/berth/internal/errs"
	"github.com/wangsirco/berth/internal/sign"
	"github.com/wangsirco/berth/internal/version"
)

// MCP server (Streamable HTTP transport, JSON responses only), authenticated with Agent
// tokens. Tools come from the change kinds the token may run plus read-only queries (方案 §8).

const mcpProtocol = "2025-06-18"

type rpcReq struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcErr struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func init() {
	routeHooks = append(routeHooks, func(a *App, mux *http.ServeMux) {
		mux.HandleFunc("POST /mcp", a.handleMCP)
		mux.HandleFunc("GET /mcp", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Allow", "POST")
			http.Error(w, "use POST", http.StatusMethodNotAllowed)
		})
		mux.HandleFunc("DELETE /mcp", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })
	})
}

func rpcReply(w http.ResponseWriter, id json.RawMessage, result any, e *rpcErr) {
	out := map[string]any{"jsonrpc": "2.0", "id": id}
	if e != nil {
		out["error"] = e
	} else {
		out["result"] = result
	}
	writeJSON(w, 200, out)
}

func (a *App) handleMCP(w http.ResponseWriter, r *http.Request) {
	var req rpcReq
	if err := readJSON(r, &req); err != nil {
		rpcReply(w, nil, nil, &rpcErr{Code: -32700, Message: "parse error"})
		return
	}
	if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer berth_agt_") {
		w.Header().Set("WWW-Authenticate", `Bearer realm="berth"`)
		rpcReply(w, req.ID, nil, &rpcErr{Code: -32001, Message: T(pickLocale(r.Header.Get("Accept-Language")), "mcp.agent_token_required", nil)})
		return
	}
	r.Header.Set("X-Berth-Via", "mcp")
	if r.Header.Get("X-Berth-Session") == "" {
		r.Header.Set("X-Berth-Session", r.Header.Get("Mcp-Session-Id"))
	}
	r2, err := a.bearerAuth(r, "")
	if err != nil {
		e := errs.From(err)
		rpcReply(w, req.ID, nil, &rpcErr{Code: -32001, Message: T(a.locale(r), e.Code, e.Params)})
		return
	}
	if len(req.ID) == 0 { // notification (e.g. notifications/initialized)
		w.WriteHeader(http.StatusAccepted)
		return
	}
	switch req.Method {
	case "initialize":
		w.Header().Set("Mcp-Session-Id", "mcp_"+sign.RandomHex(12))
		rpcReply(w, req.ID, map[string]any{
			"protocolVersion": mcpProtocol,
			"capabilities":    map[string]any{"tools": map[string]any{}, "resources": map[string]any{}},
			"serverInfo":      map[string]any{"name": "berth", "version": version.Version},
			"instructions": "Berth manages a Linux server. Every write is a change with a preview, an audit entry and (often) undo. " +
				"Use plan_only=true or plan_change to preview first. High-risk changes may wait for a person to approve them; poll get_change.",
		}, nil)
	case "ping":
		rpcReply(w, req.ID, map[string]any{}, nil)
	case "tools/list":
		rpcReply(w, req.ID, map[string]any{"tools": a.mcpTools(r2)}, nil)
	case "tools/call":
		var p struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		_ = json.Unmarshal(req.Params, &p)
		rpcReply(w, req.ID, a.mcpCall(r2, p.Name, p.Arguments), nil)
	case "resources/list":
		rpcReply(w, req.ID, map[string]any{"resources": a.mcpResources(r2)}, nil)
	case "resources/read":
		var p struct {
			URI string `json:"uri"`
		}
		_ = json.Unmarshal(req.Params, &p)
		text, mime, err := a.mcpRead(r2, p.URI)
		if err != nil {
			e := errs.From(err)
			rpcReply(w, req.ID, nil, &rpcErr{Code: -32002, Message: T("en", e.Code, e.Params)})
			return
		}
		rpcReply(w, req.ID, map[string]any{"contents": []map[string]string{{"uri": p.URI, "mimeType": mime, "text": text}}}, nil)
	default:
		rpcReply(w, req.ID, nil, &rpcErr{Code: -32601, Message: "method not found: " + req.Method})
	}
}

type mcpTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
	Annotations map[string]any `json:"annotations,omitempty"`
}

func schemaOf(ps change.ParamSchema, extra map[string]any) map[string]any {
	props := map[string]any{}
	for k, p := range ps.Props {
		m := map[string]any{"type": p.Type}
		if p.Desc != "" {
			m["description"] = p.Desc
		}
		if len(p.Enum) > 0 {
			m["enum"] = p.Enum
		}
		if p.Type == "array" {
			m["items"] = map[string]any{"type": "string"}
		}
		props[k] = m
	}
	for k, v := range extra {
		props[k] = v
	}
	req := ps.Required
	if req == nil {
		req = []string{}
	}
	return map[string]any{"type": "object", "properties": props, "required": req}
}

var hostProp = map[string]any{"type": "string", "description": "host name (default: local)"}

type readTool struct {
	scope, desc string
	props       map[string]any
	required    []string
	run         func(a *App, r *http.Request, host string, args map[string]any) (any, error)
}

func str(args map[string]any, k string) string { s, _ := args[k].(string); return s }

var readTools = map[string]readTool{
	"list_hosts": {scope: "overview:read", desc: "List managed hosts and whether they are online.",
		run: func(a *App, r *http.Request, host string, args map[string]any) (any, error) {
			out := []map[string]any{}
			for _, h := range a.connectedHosts() {
				on, _ := a.online(h)
				out = append(out, map[string]any{"name": h, "online": on})
			}
			return out, nil
		}},
	"get_host_status": {scope: "overview:read", desc: "CPU, memory, swap/zram, disks, network and who uses the memory (berths).",
		run: func(a *App, r *http.Request, host string, args map[string]any) (any, error) {
			var h, b any
			if err := a.call(r.Context(), host, "GET", "/v1/metrics/host", nil, &h); err != nil {
				return nil, err
			}
			_ = a.call(r.Context(), host, "GET", "/v1/metrics/berths", nil, &b)
			return map[string]any{"host": h, "berths": b}, nil
		}},
	"list_containers": {scope: "containers:read", desc: "List all containers with state, image, ports and compose project.",
		run: func(a *App, r *http.Request, host string, args map[string]any) (any, error) {
			return a.containers(r.Context(), host)
		}},
	"get_container": {scope: "containers:read", desc: "Details of one container (env values that look secret are masked).", required: []string{"name"},
		props: map[string]any{"name": map[string]any{"type": "string"}},
		run: func(a *App, r *http.Request, host string, args map[string]any) (any, error) {
			c, err := a.findContainer(r.Context(), host, str(args, "name"))
			if err != nil {
				return nil, err
			}
			var d docker.ContainerDetail
			err = a.call(r.Context(), host, "GET", "/v1/docker/containers/"+url.PathEscape(c.ID), nil, &d)
			return d, err
		}},
	"tail_logs": {scope: "containers:read", desc: "Last lines of a container's logs (max 500).", required: []string{"container"},
		props: map[string]any{"container": map[string]any{"type": "string"}, "lines": map[string]any{"type": "integer"}},
		run: func(a *App, r *http.Request, host string, args map[string]any) (any, error) {
			n, _ := args["lines"].(float64)
			return a.tailLogs(r.Context(), host, str(args, "container"), int(n))
		}},
	"list_compose_projects": {scope: "containers:read", desc: "Compose projects, their directories, and whether Berth manages them.",
		run: func(a *App, r *http.Request, host string, args map[string]any) (any, error) {
			return a.projects(r.Context(), host)
		}},
	"list_changes": {scope: "audit:read", desc: "Recent changes (audit log), newest first.",
		props: map[string]any{"limit": map[string]any{"type": "integer"}},
		run: func(a *App, r *http.Request, host string, args map[string]any) (any, error) {
			n, _ := args["limit"].(float64)
			list, err := a.pipe.List(change.ListFilter{Limit: max(1, min(int(n), 100))})
			if err != nil {
				return nil, err
			}
			rr := r.WithContext(contextWith(r, appKey, a))
			out := []changeView{}
			for _, c := range list {
				if canSee(rr, c) {
					out = append(out, viewOf(c))
				}
			}
			return out, nil
		}},
	"get_change": {scope: "", desc: "One change by id: status (e.g. pending_approval, done), preview, log and result.", required: []string{"id"},
		props: map[string]any{"id": map[string]any{"type": "string"}},
		run: func(a *App, r *http.Request, host string, args map[string]any) (any, error) {
			c, err := a.pipe.Get(str(args, "id"))
			if err != nil || !canSee(r.WithContext(contextWith(r, appKey, a)), c) {
				return nil, errs.New(404, "change.not_found")
			}
			return viewOf(c), nil
		}},
	"plan_change": {scope: "", desc: "Preview any change kind without running it: files diff, commands, affected objects, risk.", required: []string{"kind"},
		props: map[string]any{"kind": map[string]any{"type": "string"}, "params": map[string]any{"type": "object"}},
		run: func(a *App, r *http.Request, host string, args map[string]any) (any, error) {
			k, ok := change.Get(str(args, "kind"))
			if !ok {
				return nil, errs.New(404, "change.unknown_kind", "kind", str(args, "kind"))
			}
			if err := a.mayRun(r, k, host); err != nil {
				return nil, err
			}
			raw, _ := json.Marshal(args["params"])
			if string(raw) == "null" {
				raw = []byte("{}")
			}
			c, err := a.pipe.Preview(r.Context(), currentActor(r), host, k.Name, raw)
			if err != nil {
				return nil, err
			}
			return viewOf(c), nil
		}},
	"list_tasks": {scope: "tasks:read", desc: "Running, waiting and recent tasks (long changes such as image pulls).",
		run: func(a *App, r *http.Request, host string, args map[string]any) (any, error) {
			return a.queue.List(host, 20)
		}},
	"list_files": {scope: "files:read", desc: "List a directory.", required: []string{"path"},
		props: map[string]any{"path": map[string]any{"type": "string"}},
		run: func(a *App, r *http.Request, host string, args map[string]any) (any, error) {
			return a.mcpFiles(r, host, "list", str(args, "path"), 0)
		}},
	"read_file": {scope: "files:read", desc: "Read a text file (up to 256 KB).", required: []string{"path"},
		props: map[string]any{"path": map[string]any{"type": "string"}},
		run: func(a *App, r *http.Request, host string, args map[string]any) (any, error) {
			out, err := a.mcpFiles(r, host, "read", str(args, "path"), 256<<10)
			if err != nil {
				return nil, err
			}
			var rr struct {
				Content   []byte `json:"content"`
				Truncated bool   `json:"truncated"`
				Binary    bool   `json:"binary"`
			}
			b, _ := json.Marshal(out)
			_ = json.Unmarshal(b, &rr)
			if rr.Binary {
				return map[string]any{"binary": true}, nil
			}
			return map[string]any{"text": string(rr.Content), "truncated": rr.Truncated}, nil
		}},
}

func (a *App) mcpFiles(r *http.Request, host, op, p string, max int64) (any, error) {
	if !currentUser(r).Can("files.read") {
		return nil, errs.New(403, "auth.forbidden", "perm", "files.read")
	}
	root, err := rootFor(currentUser(r), p)
	if err != nil {
		return nil, err
	}
	var out any
	err = a.call(r.Context(), host, "POST", "/v1/files/"+op, map[string]any{"root": root, "path": p, "max": max}, &out)
	return out, err
}

func (a *App) tailLogs(ctx context.Context, host, name string, n int) ([]docker.LogLine, error) {
	if n <= 0 || n > 500 {
		n = 100
	}
	c, err := a.findContainer(ctx, host, name)
	if err != nil {
		return nil, err
	}
	ag, err := a.agent(host)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	rc, err := ag.Stream(ctx, "GET", fmt.Sprintf("/v1/docker/containers/%s/logs?tail=%d", url.PathEscape(c.ID), n), nil)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	out := []docker.LogLine{}
	_ = streamLogLines(rc, func(l docker.LogLine) bool { out = append(out, l); return true })
	return out, nil
}

func (a *App) readToolAllowed(r *http.Request, rt readTool) bool {
	t := currentToken(r)
	if rt.scope == "" {
		return true
	}
	if !t.has(rt.scope) {
		return false
	}
	res, _, _ := strings.Cut(rt.scope, ":")
	perm := map[string]string{"overview": "overview.view", "containers": "containers.view", "audit": "", "tasks": "overview.view", "files": "files.read"}[res]
	return perm == "" || currentUser(r).Can(perm)
}

func (a *App) mcpTools(r *http.Request) []mcpTool {
	u, t := currentUser(r), currentToken(r)
	out := []mcpTool{}
	names := make([]string, 0, len(readTools))
	for n := range readTools {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		rt := readTools[n]
		if !a.readToolAllowed(r, rt) {
			continue
		}
		props := map[string]any{"host": hostProp}
		for k, v := range rt.props {
			props[k] = v
		}
		req := rt.required
		if req == nil {
			req = []string{}
		}
		out = append(out, mcpTool{Name: n, Description: rt.desc, InputSchema: map[string]any{"type": "object", "properties": props, "required": req},
			Annotations: map[string]any{"readOnlyHint": true}})
	}
	for _, k := range change.All() {
		if k.CLI == "" || (k.Perm != "" && !u.Can(k.Perm)) || (k.Scope != "" && !t.has(k.Scope)) {
			continue
		}
		risk := ""
		if k.Name == "container.remove" || strings.HasSuffix(k.Name, ".remove") || k.Name == "file.purge" {
			risk = " Destructive."
		}
		desc := T("en", k.Title, nil) + "." + risk + " Runs as a Berth change (audited; undoable when possible). Set plan_only=true to preview."
		out = append(out, mcpTool{Name: strings.ReplaceAll(k.Name, ".", "_"), Description: desc,
			InputSchema: schemaOf(k.Params, map[string]any{"host": hostProp, "plan_only": map[string]any{"type": "boolean", "description": "only preview"}}),
			Annotations: map[string]any{"destructiveHint": risk != ""}})
	}
	return out
}

func toolResult(v any, isErr bool, summary string) map[string]any {
	b, _ := json.MarshalIndent(v, "", "  ")
	text := string(b)
	if summary != "" {
		text = summary + "\n\n" + text
	}
	res := map[string]any{"content": []map[string]string{{"type": "text", "text": text}}, "isError": isErr}
	var obj map[string]any
	if json.Unmarshal(b, &obj) == nil { // structuredContent must be an object
		res["structuredContent"] = obj
	}
	return res
}

func (a *App) mcpCall(r *http.Request, name string, raw json.RawMessage) map[string]any {
	args := map[string]any{}
	_ = json.Unmarshal(raw, &args)
	host := str(args, "host")
	if host == "" {
		host = "local"
	}
	delete(args, "host")
	fail := func(err error) map[string]any {
		e := errs.From(err)
		return toolResult(map[string]any{"error": map[string]any{"code": e.Code, "message": T("en", e.Code, e.Params)}}, true, T("en", e.Code, e.Params))
	}
	if rt, ok := readTools[name]; ok {
		if !a.readToolAllowed(r, rt) {
			return fail(errs.New(403, "token.out_of_scope", "scope", rt.scope))
		}
		v, err := rt.run(a, r, host, args)
		a.mcpActivity(r, name, args, host, nil, err)
		if err != nil {
			return fail(err)
		}
		return toolResult(v, false, "")
	}
	k, ok := change.Get(strings.ReplaceAll(name, "_", "."))
	if !ok || k.CLI == "" {
		// names with underscores inside the kind (file.restore_modes) are not tools anyway
		return fail(errs.New(404, "change.unknown_kind", "kind", name))
	}
	if err := a.mayRun(r, k, host); err != nil {
		return fail(err)
	}
	planOnly, _ := args["plan_only"].(bool)
	delete(args, "plan_only")
	params, _ := json.Marshal(args)
	var c *change.Change
	var err error
	if planOnly {
		c, err = a.pipe.Preview(r.Context(), currentActor(r), host, k.Name, params)
	} else {
		c, err = a.pipe.Run(r.Context(), currentActor(r), host, k.Name, params)
	}
	a.mcpActivity(r, name, args, host, c, err)
	if c == nil {
		return fail(err)
	}
	v := viewOf(c)
	var summary string
	switch c.Status {
	case "pending_approval":
		summary = fmt.Sprintf("Submitted. A person must approve it in the Berth panel first (change %s). Poll get_change for the outcome.", c.ID)
	case "planned":
		summary = "Preview only; nothing changed. Equivalent command: " + v.Equivalent
	case "running":
		summary = fmt.Sprintf("Started as a background task (change %s). Poll get_change.", c.ID)
	case "done":
		summary = "Done. Equivalent command: " + v.Equivalent
		if c.Undo != nil {
			summary += fmt.Sprintf(". It can be undone from the audit log (change %s).", c.ID)
		}
	}
	return toolResult(v, err != nil, summary)
}

func (a *App) mcpResources(r *http.Request) []map[string]string {
	out := []map[string]string{}
	t := currentToken(r)
	if t.has("audit:read") && currentUser(r).Can("audit.view") {
		out = append(out, map[string]string{"uri": "berth://audit/recent", "name": "Recent changes", "mimeType": "application/json"})
	}
	if t.has("containers:read") {
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		for _, h := range a.connectedHosts() {
			if cs, err := a.containers(ctx, h); err == nil {
				for _, c := range cs {
					out = append(out, map[string]string{"uri": "berth://hosts/" + h + "/containers/" + c.Name + "/logs", "name": c.Name + " logs (" + h + ")", "mimeType": "text/plain"})
				}
			}
			if ps, err := a.projects(ctx, h); err == nil {
				for _, p := range ps {
					out = append(out, map[string]string{"uri": "berth://hosts/" + h + "/compose/" + p.Name + "/compose.yaml", "name": p.Name + " compose file (" + h + ")", "mimeType": "application/yaml"})
				}
			}
		}
	}
	return out
}

func (a *App) mcpRead(r *http.Request, uri string) (string, string, error) {
	t, u := currentToken(r), currentUser(r)
	if uri == "berth://audit/recent" {
		if !t.has("audit:read") || !u.Can("audit.view") {
			return "", "", errs.New(403, "token.out_of_scope", "scope", "audit:read")
		}
		list, err := a.pipe.List(change.ListFilter{Limit: 50})
		if err != nil {
			return "", "", err
		}
		b, _ := json.MarshalIndent(list, "", "  ")
		return string(b), "application/json", nil
	}
	rest, ok := strings.CutPrefix(uri, "berth://hosts/")
	parts := strings.Split(rest, "/")
	if !ok || len(parts) != 4 || !t.has("containers:read") || !u.Can("containers.view") {
		return "", "", errs.New(404, "mcp.no_resource")
	}
	host := parts[0]
	switch {
	case parts[1] == "containers" && parts[3] == "logs":
		lines, err := a.tailLogs(r.Context(), host, parts[2], 300)
		if err != nil {
			return "", "", err
		}
		var b strings.Builder
		for _, l := range lines {
			fmt.Fprintf(&b, "%s %s %s\n", l.TS, l.Stream, l.Line)
		}
		return b.String(), "text/plain", nil
	case parts[1] == "compose" && parts[3] == "compose.yaml":
		f, err := a.composeFiles(r.Context(), host, parts[2])
		if err != nil {
			return "", "", err
		}
		return f.Compose, "application/yaml", nil
	}
	return "", "", errs.New(404, "mcp.no_resource")
}
