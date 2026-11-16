package panel

// The AI assistant (方案 §9.1). Off until a person saves their own key (OpenAI-compatible,
// Anthropic Claude or xAI Grok). It answers with the same read-only tools MCP offers and,
// for anything that would change the server, proposes changes: previews made as that
// person, which they then confirm in the usual dialog. It never runs a change itself, so it
// can never do more than the person may. Token use is counted per month with an optional cap.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/wangsirco/berth/internal/change"
	"github.com/wangsirco/berth/internal/errs"
)

type aiConfig struct {
	Provider string `json:"provider"` // openai | anthropic | xai
	BaseURL  string `json:"base_url,omitempty"`
	Model    string `json:"model,omitempty"`
	Limit    int    `json:"monthly_limit,omitempty"` // tokens per month, 0 = no cap
	HasKey   bool   `json:"has_key"`
	key      string
}

var aiDefaults = map[string][2]string{ // base URL, model
	"openai":    {"https://api.openai.com/v1", "gpt-5"},
	"anthropic": {"https://api.anthropic.com/v1", "claude-sonnet-5-5"},
	"xai":       {"https://api.x.ai/v1", "grok-4"},
}

func (a *App) aiConfigOf(uid int64) (aiConfig, bool) {
	var c aiConfig
	var enc []byte
	if a.db.QueryRow(`SELECT provider, base_url, model, monthly_limit, key_enc FROM ai_settings WHERE user_id=?`, uid).Scan(&c.Provider, &c.BaseURL, &c.Model, &c.Limit, &enc) != nil {
		return c, false
	}
	if k, err := a.open(enc); err == nil {
		c.key, c.HasKey = string(k), len(k) > 0
	}
	d := aiDefaults[c.Provider]
	c.BaseURL = strings.TrimRight(firstNonEmpty(c.BaseURL, d[0]), "/")
	c.Model = firstNonEmpty(c.Model, d[1])
	return c, c.HasKey
}

func aiMonth() string { return time.Now().Format("2006-01") }

func (a *App) aiUsed(uid int64) (in, out int) {
	a.db.QueryRow(`SELECT tokens_in, tokens_out FROM ai_usage WHERE user_id=? AND month=?`, uid, aiMonth()).Scan(&in, &out)
	return
}

func (a *App) aiCount(uid int64, in, out int) {
	a.db.Exec(`INSERT INTO ai_usage(user_id, month, tokens_in, tokens_out) VALUES(?,?,?,?)
		ON CONFLICT(user_id, month) DO UPDATE SET tokens_in=tokens_in+excluded.tokens_in, tokens_out=tokens_out+excluded.tokens_out`, uid, aiMonth(), in, out)
}

// ---- tools ----

type aiTool struct {
	Name, Desc string
	Schema     map[string]any
}

var aiReadPerm = map[string]string{"overview": "overview.view", "containers": "containers.view", "audit": "audit.view", "tasks": "overview.view", "files": "files.read"}

func (a *App) aiTools(u *User) []aiTool {
	var out []aiTool
	for name, rt := range readTools {
		if rt.scope != "" {
			res, _, _ := strings.Cut(rt.scope, ":")
			if p := aiReadPerm[res]; p != "" && !u.Can(p) {
				continue
			}
		}
		props := map[string]any{"host": hostProp}
		for k, v := range rt.props {
			props[k] = v
		}
		req := rt.required
		if req == nil {
			req = []string{}
		}
		out = append(out, aiTool{Name: name, Desc: rt.desc, Schema: map[string]any{"type": "object", "properties": props, "required": req}})
	}
	out = append(out, aiTool{Name: "propose_change", Desc: "Propose a change for the person to confirm (it is NOT run). Use the kind names from list_kinds, with their parameters. " +
		"Returns the preview: what would change, and the risk.",
		Schema: map[string]any{"type": "object", "properties": map[string]any{"kind": map[string]any{"type": "string"}, "host": hostProp,
			"params": map[string]any{"type": "object"}, "why": map[string]any{"type": "string", "description": "one sentence for the person"}}, "required": []string{"kind", "params"}}})
	out = append(out, aiTool{Name: "list_kinds", Desc: "List the change kinds this person may propose, with their parameters.",
		Schema: map[string]any{"type": "object", "properties": map[string]any{"filter": map[string]any{"type": "string", "description": "substring of the kind name"}}}})
	return out
}

type aiProposal struct {
	Change changeView `json:"change"`
	Why    string     `json:"why,omitempty"`
}

func (a *App) aiRunTool(r *http.Request, u *User, name string, args map[string]any, props *[]aiProposal) string {
	host, _ := args["host"].(string)
	if host == "" {
		host = "local"
	}
	if len(u.Scope.Hosts) > 0 && !contains(u.Scope.Hosts, host) && u.Role != RoleSuper {
		return `{"error":"this person may not use host ` + host + `"}`
	}
	asJSON := func(v any) string {
		b, _ := json.Marshal(v)
		if len(b) > 24000 {
			b = append(b[:24000], []byte("…(truncated)")...)
		}
		return string(b)
	}
	switch name {
	case "list_kinds":
		f, _ := args["filter"].(string)
		var out []map[string]any
		for _, k := range change.All() {
			if k.CLI == "" || (k.Perm != "" && !u.Can(k.Perm)) || f != "" && !strings.Contains(k.Name, f) {
				continue
			}
			out = append(out, map[string]any{"kind": k.Name, "title": T("en", k.Title, nil), "params": schemaOf(k.Params, nil)})
		}
		return asJSON(out)
	case "propose_change":
		kind, _ := args["kind"].(string)
		k, ok := change.Get(kind)
		if !ok || k.CLI == "" {
			return `{"error":"unknown kind"}`
		}
		if k.Perm != "" && !u.Can(k.Perm) {
			return `{"error":"this person may not do ` + kind + `"}`
		}
		params, _ := json.Marshal(args["params"])
		c, err := a.pipe.Preview(r.Context(), currentActor(r), host, kind, params)
		if err != nil {
			e := errs.From(err)
			return asJSON(map[string]any{"error": T("en", e.Code, e.Params)})
		}
		why, _ := args["why"].(string)
		*props = append(*props, aiProposal{Change: viewOf(c), Why: why})
		return asJSON(map[string]any{"proposed": true, "summary": T("en", c.Plan.Summary, c.Plan.SummaryP), "risk": c.Plan.Risk, "note": "shown to the person; they decide"})
	}
	rt, ok := readTools[name]
	if !ok {
		return `{"error":"unknown tool"}`
	}
	if rt.scope != "" {
		res, _, _ := strings.Cut(rt.scope, ":")
		if p := aiReadPerm[res]; p != "" && !u.Can(p) {
			return `{"error":"not allowed"}`
		}
	}
	delete(args, "host")
	v, err := rt.run(a, r, host, args)
	if err != nil {
		e := errs.From(err)
		return asJSON(map[string]any{"error": T("en", e.Code, e.Params)})
	}
	return asJSON(v)
}

// ---- providers ----

type aiMsg struct {
	Role    string `json:"role"` // user | assistant
	Content string `json:"content"`
}

type aiCall struct {
	ID, Name string
	Args     map[string]any
}

type aiTurn struct {
	Text    string
	Calls   []aiCall
	In, Out int
}

var aiClient = &http.Client{Timeout: 120 * time.Second}

func aiPost(ctx context.Context, url string, headers map[string]string, body any, out any) error {
	b, _ := json.Marshal(body)
	req, _ := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := aiClient.Do(req)
	if err != nil {
		return errs.New(502, "ai.unreachable", "detail", err.Error())
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode >= 300 {
		return errs.New(502, "ai.provider_error", "status", resp.StatusCode, "detail", truncate(string(data), 300))
	}
	return json.Unmarshal(data, out)
}

// conversation keeps the provider-specific message list while tools are called.
type conversation struct {
	cfg    aiConfig
	system string
	tools  []aiTool
	oa     []map[string]any // OpenAI-style messages
	an     []map[string]any // Anthropic-style messages
}

func newConversation(cfg aiConfig, system string, tools []aiTool, msgs []aiMsg) *conversation {
	c := &conversation{cfg: cfg, system: system, tools: tools}
	c.oa = append(c.oa, map[string]any{"role": "system", "content": system})
	for _, m := range msgs {
		if m.Role != "user" && m.Role != "assistant" {
			continue
		}
		c.oa = append(c.oa, map[string]any{"role": m.Role, "content": m.Content})
		c.an = append(c.an, map[string]any{"role": m.Role, "content": m.Content})
	}
	return c
}

func (c *conversation) step(ctx context.Context) (aiTurn, error) {
	var t aiTurn
	if c.cfg.Provider == "anthropic" {
		tools := []map[string]any{}
		for _, x := range c.tools {
			tools = append(tools, map[string]any{"name": x.Name, "description": x.Desc, "input_schema": x.Schema})
		}
		var r struct {
			Content []struct {
				Type  string         `json:"type"`
				Text  string         `json:"text"`
				ID    string         `json:"id"`
				Name  string         `json:"name"`
				Input map[string]any `json:"input"`
			} `json:"content"`
			Usage struct {
				In  int `json:"input_tokens"`
				Out int `json:"output_tokens"`
			} `json:"usage"`
		}
		err := aiPost(ctx, c.cfg.BaseURL+"/messages", map[string]string{"x-api-key": c.cfg.key, "anthropic-version": "2023-06-01"},
			map[string]any{"model": c.cfg.Model, "max_tokens": 4096, "system": c.system, "messages": c.an, "tools": tools}, &r)
		if err != nil {
			return t, err
		}
		blocks := []map[string]any{}
		for _, b := range r.Content {
			switch b.Type {
			case "text":
				t.Text += b.Text
				blocks = append(blocks, map[string]any{"type": "text", "text": b.Text})
			case "tool_use":
				t.Calls = append(t.Calls, aiCall{ID: b.ID, Name: b.Name, Args: b.Input})
				blocks = append(blocks, map[string]any{"type": "tool_use", "id": b.ID, "name": b.Name, "input": b.Input})
			}
		}
		c.an = append(c.an, map[string]any{"role": "assistant", "content": blocks})
		t.In, t.Out = r.Usage.In, r.Usage.Out
		return t, nil
	}
	tools := []map[string]any{}
	for _, x := range c.tools {
		tools = append(tools, map[string]any{"type": "function", "function": map[string]any{"name": x.Name, "description": x.Desc, "parameters": x.Schema}})
	}
	var r struct {
		Choices []struct {
			Message struct {
				Content   string `json:"content"`
				ToolCalls []struct {
					ID       string `json:"id"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			In  int `json:"prompt_tokens"`
			Out int `json:"completion_tokens"`
		} `json:"usage"`
	}
	err := aiPost(ctx, c.cfg.BaseURL+"/chat/completions", map[string]string{"Authorization": "Bearer " + c.cfg.key},
		map[string]any{"model": c.cfg.Model, "messages": c.oa, "tools": tools}, &r)
	if err != nil {
		return t, err
	}
	if len(r.Choices) == 0 {
		return t, errs.New(502, "ai.provider_error", "status", 200, "detail", "no answer")
	}
	m := r.Choices[0].Message
	t.Text = m.Content
	msg := map[string]any{"role": "assistant", "content": m.Content}
	if len(m.ToolCalls) > 0 {
		calls := []map[string]any{}
		for _, tc := range m.ToolCalls {
			args := map[string]any{}
			json.Unmarshal([]byte(tc.Function.Arguments), &args)
			t.Calls = append(t.Calls, aiCall{ID: tc.ID, Name: tc.Function.Name, Args: args})
			calls = append(calls, map[string]any{"id": tc.ID, "type": "function", "function": map[string]any{"name": tc.Function.Name, "arguments": tc.Function.Arguments}})
		}
		msg["tool_calls"] = calls
	}
	c.oa = append(c.oa, msg)
	t.In, t.Out = r.Usage.In, r.Usage.Out
	return t, nil
}

func (c *conversation) results(calls []aiCall, outs []string) {
	if c.cfg.Provider == "anthropic" {
		blocks := []map[string]any{}
		for i, call := range calls {
			blocks = append(blocks, map[string]any{"type": "tool_result", "tool_use_id": call.ID, "content": outs[i]})
		}
		c.an = append(c.an, map[string]any{"role": "user", "content": blocks})
		return
	}
	for i, call := range calls {
		c.oa = append(c.oa, map[string]any{"role": "tool", "tool_call_id": call.ID, "content": outs[i]})
	}
}

// ---- API ----

func (a *App) handleAIChat(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	if currentToken(r) != nil {
		a.fail(w, r, errs.New(403, "ai.session_only"))
		return
	}
	cfg, ok := a.aiConfigOf(u.ID)
	if !ok {
		a.fail(w, r, errs.New(409, "ai.not_set"))
		return
	}
	in, out := a.aiUsed(u.ID)
	if cfg.Limit > 0 && in+out >= cfg.Limit {
		a.fail(w, r, errs.New(429, "ai.over_limit", "limit", cfg.Limit))
		return
	}
	var req struct {
		Messages []aiMsg `json:"messages"`
		Host     string  `json:"host"`
		Context  string  `json:"context"` // what the person was looking at, e.g. "container web on local: exited (1)"
		Locale   string  `json:"locale"`
	}
	if err := readJSON(r, &req); err != nil {
		a.fail(w, r, err)
		return
	}
	if len(req.Messages) == 0 || len(req.Messages) > 40 {
		a.fail(w, r, errs.New(400, "ai.bad_messages"))
		return
	}
	lang := map[string]string{"zh-CN": "Simplified Chinese", "zh-TW": "Traditional Chinese", "en": "English"}[req.Locale]
	system := "You are the assistant inside Berth, a Linux server panel. Answer in " + firstNonEmpty(lang, "the person's language") + ", briefly and plainly. " +
		"Use the read-only tools to look before you answer; say what you found and why. " +
		"You cannot change anything yourself: when something should change, call propose_change (find kinds with list_kinds) — the person sees the preview and decides. " +
		"Never invent output you did not read. Current host: " + firstNonEmpty(req.Host, "local") + "."
	if req.Context != "" {
		system += "\nThe person is looking at: " + truncate(req.Context, 2000)
	}
	conv := newConversation(cfg, system, a.aiTools(u), req.Messages)
	props := []aiProposal{}
	var text string
	var tin, tout int
	ctx, cancel := context.WithTimeout(r.Context(), 4*time.Minute)
	defer cancel()
	for i := 0; i < 10; i++ {
		t, err := conv.step(ctx)
		tin, tout = tin+t.In, tout+t.Out
		if err != nil {
			a.aiCount(u.ID, tin, tout)
			a.fail(w, r, err)
			return
		}
		text = t.Text
		if len(t.Calls) == 0 {
			break
		}
		outs := make([]string, len(t.Calls))
		for j, call := range t.Calls {
			if call.Args == nil {
				call.Args = map[string]any{}
			}
			outs[j] = a.aiRunTool(r.WithContext(ctx), u, call.Name, call.Args, &props)
		}
		conv.results(t.Calls, outs)
		if i == 9 {
			text = fmt.Sprintf("%s\n\n(stopped after %d steps)", text, i+1)
		}
	}
	a.aiCount(u.ID, tin, tout)
	in, out = a.aiUsed(u.ID)
	writeJSON(w, 200, map[string]any{"reply": text, "proposals": props, "usage": map[string]int{"in": tin, "out": tout}, "month": in + out, "limit": cfg.Limit})
}

func init() {
	routeHooks = append(routeHooks, func(a *App, mux *http.ServeMux) {
		mux.HandleFunc("GET /api/v1/ai", a.authed("", func(w http.ResponseWriter, r *http.Request) {
			u := currentUser(r)
			cfg, _ := a.aiConfigOf(u.ID)
			in, out := a.aiUsed(u.ID)
			writeJSON(w, 200, map[string]any{"config": cfg, "month": in + out, "providers": aiDefaults})
		}))
		mux.HandleFunc("POST /api/v1/ai/chat", a.authed("", a.handleAIChat))
	})
	change.Register(change.Kind{
		Name: "ai.set", Perm: "overview.view", Scope: "", Title: "kind.ai.set", CLI: "berth ai set {{.provider}}", Redact: []string{"key"},
		Params: change.ParamSchema{Props: map[string]change.Prop{"provider": {Type: "string", Enum: []string{"openai", "anthropic", "xai", ""}},
			"base_url": {Type: "string", Desc: "OpenAI-compatible endpoint (optional)"}, "model": {Type: "string"}, "key": {Type: "string", Desc: "API key ('' keeps the saved one)"},
			"monthly_limit": {Type: "integer", Desc: "tokens per month (0 = no cap)"}}},
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			var p struct {
				Provider string
				BaseURL  string `json:"base_url"`
				Model    string
			}
			json.Unmarshal(raw, &p)
			if p.Provider == "" {
				return change.Plan{Summary: "plan.ai.off", Risk: change.RiskLow}, nil
			}
			if _, ok := aiDefaults[p.Provider]; !ok {
				return change.Plan{}, errs.New(400, "params.enum", "name", "provider", "allowed", "openai, anthropic, xai")
			}
			if p.BaseURL != "" && !strings.HasPrefix(p.BaseURL, "https://") && !strings.HasPrefix(p.BaseURL, "http://") {
				return change.Plan{}, errs.New(400, "ai.bad_url")
			}
			return change.Plan{Summary: "plan.ai.set", SummaryP: map[string]any{"provider": p.Provider, "model": firstNonEmpty(p.Model, aiDefaults[p.Provider][1])}, Risk: change.RiskLow}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			a := svc(env)
			var p struct {
				Provider, Model, Key string
				BaseURL              string `json:"base_url"`
				Limit                int    `json:"monthly_limit"`
			}
			json.Unmarshal(raw, &p)
			var uid int64
			fmt.Sscan(env.ActorID, &uid)
			if env.ActorType != "user" || uid == 0 {
				return nil, nil, errs.New(403, "ai.session_only")
			}
			if p.Provider == "" {
				a.db.Exec(`DELETE FROM ai_settings WHERE user_id=?`, uid)
				return nil, nil, nil
			}
			enc := []byte(nil)
			if p.Key == "" {
				a.db.QueryRow(`SELECT key_enc FROM ai_settings WHERE user_id=?`, uid).Scan(&enc)
			} else {
				var err error
				if enc, err = a.seal([]byte(p.Key)); err != nil {
					return nil, nil, err
				}
			}
			if enc == nil {
				return nil, nil, errs.New(400, "ai.need_key")
			}
			_, err := a.db.Exec(`INSERT INTO ai_settings(user_id, provider, base_url, model, monthly_limit, key_enc) VALUES(?,?,?,?,?,?)
				ON CONFLICT(user_id) DO UPDATE SET provider=excluded.provider, base_url=excluded.base_url, model=excluded.model, monthly_limit=excluded.monthly_limit, key_enc=excluded.key_enc`,
				uid, p.Provider, p.BaseURL, p.Model, p.Limit, enc)
			return nil, nil, err
		},
	})
}
