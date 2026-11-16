package devenv

import (
	"context"
	"encoding/json"
	"net/http"
	"os/exec"
	"strings"

	"github.com/wangsirco/berth/internal/agent"
	"github.com/wangsirco/berth/internal/errs"
)

// Register adds /v1/dev/*.
func Register(s *agent.Server) {
	KeepGuards()
	s.Handle("GET /v1/dev/tools", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		agent.WriteJSON(w, 200, Tools())
	})
	s.Handle("POST /v1/dev/tools/install", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		st := agent.NewStream(w)
		st.End(InstallCodeServer(r.Context(), func(l string) { st.Send(map[string]string{"l": l}) }))
	})
	// inspect: an existing folder's devcontainer.json, for the new-workspace dialog
	s.Handle("GET /v1/dev/inspect", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		dir := r.URL.Query().Get("dir")
		if err := CheckDir(dir); err != nil {
			agent.WriteErr(w, err)
			return
		}
		p, t := FindDevcontainer(dir)
		agent.WriteJSON(w, 200, map[string]string{"devcontainer_path": p, "devcontainer": t})
	})
	s.Handle("POST /v1/dev/prepare", func(w http.ResponseWriter, r *http.Request, body []byte) {
		var in PrepareIn
		json.Unmarshal(body, &in)
		st := agent.NewStream(w)
		log := func(l string) { st.Send(map[string]string{"l": l}) }
		if err := WriteBoot(); err != nil {
			st.End(err)
			return
		}
		out, err := Prepare(r.Context(), in, log)
		if err == nil {
			b, _ := json.Marshal(out)
			log("::result " + string(b))
		}
		st.End(err)
	})
	s.Handle("PUT /v1/dev/{name}/state", func(w http.ResponseWriter, r *http.Request, body []byte) {
		var in StateIn
		if err := json.Unmarshal(body, &in); err != nil {
			agent.WriteErr(w, errs.New(400, "params.invalid"))
			return
		}
		if err := WriteBoot(); err != nil {
			agent.WriteErr(w, err)
			return
		}
		if err := WriteState(r.PathValue("name"), in); err != nil {
			agent.WriteErr(w, err)
			return
		}
		agent.WriteJSON(w, 200, map[string]bool{"ok": true})
	})
	s.Handle("GET /v1/dev/{name}/status", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		if !ValidName(r.PathValue("name")) {
			agent.WriteErr(w, errs.New(400, "dev.bad_name"))
			return
		}
		agent.WriteJSON(w, 200, Status(r.PathValue("name")))
	})
	s.Handle("POST /v1/dev/claude/install", func(w http.ResponseWriter, r *http.Request, body []byte) {
		var in struct {
			Update bool `json:"update"`
		}
		json.Unmarshal(body, &in)
		st := agent.NewStream(w)
		st.End(InstallClaude(r.Context(), in.Update, func(l string) { st.Send(map[string]string{"l": l}) }))
	})
	s.Handle("GET /v1/dev/claude/latest", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		v, err := ClaudeLatest(r.Context())
		if err != nil {
			agent.WriteErr(w, err)
			return
		}
		agent.WriteJSON(w, 200, map[string]string{"latest": v, "installed": ClaudeVersion()})
	})
	s.Handle("GET /v1/dev/{name}/claude", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		if !ValidName(r.PathValue("name")) {
			agent.WriteErr(w, errs.New(400, "dev.bad_name"))
			return
		}
		agent.WriteJSON(w, 200, ClaudeState(r.Context(), r.PathValue("name")))
	})
	// run: one task, streamed; the last line is the result with the diff
	s.Handle("POST /v1/dev/{name}/claude/run", func(w http.ResponseWriter, r *http.Request, body []byte) {
		var in RunIn
		if err := json.Unmarshal(body, &in); err != nil {
			agent.WriteErr(w, errs.New(400, "params.invalid"))
			return
		}
		st := agent.NewStream(w)
		log := func(l string) { st.Send(map[string]string{"l": l}) }
		out, err := RunClaude(r.Context(), r.PathValue("name"), in, log)
		if out != nil {
			b, _ := json.Marshal(out)
			log("::result " + string(b))
		}
		st.End(err)
	})
	s.Handle("POST /v1/dev/{name}/claude/patch", func(w http.ResponseWriter, r *http.Request, body []byte) {
		var in struct {
			Dir     string `json:"dir"`
			From    string `json:"from"`
			To      string `json:"to"`
			Reverse bool   `json:"reverse"`
		}
		json.Unmarshal(body, &in)
		if !ValidName(r.PathValue("name")) || !hexRe.MatchString(in.From) || !hexRe.MatchString(in.To) {
			agent.WriteErr(w, errs.New(400, "params.invalid"))
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 2*60e9)
		defer cancel()
		if err := Patch(ctx, r.PathValue("name"), in.Dir, in.From, in.To, in.Reverse); err != nil {
			agent.WriteErr(w, err)
			return
		}
		agent.WriteJSON(w, 200, map[string]bool{"ok": true})
	})
	s.Handle("GET /v1/dev/{name}/claude/diff", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		q := r.URL.Query()
		if !ValidName(r.PathValue("name")) || !hexRe.MatchString(q.Get("from")) || !hexRe.MatchString(q.Get("to")) || CheckDir(q.Get("dir")) != nil {
			agent.WriteErr(w, errs.New(400, "params.invalid"))
			return
		}
		files, d, cut, err := Diff(r.Context(), r.PathValue("name"), q.Get("dir"), q.Get("from"), q.Get("to"))
		if err != nil {
			agent.WriteErr(w, err)
			return
		}
		agent.WriteJSON(w, 200, map[string]any{"files": files, "diff": d, "diff_cut": cut})
	})
	// sync: a running workspace picks up Claude Code and its token without a restart
	s.Handle("POST /v1/dev/{name}/claude/sync", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		if !ValidName(r.PathValue("name")) {
			agent.WriteErr(w, errs.New(400, "dev.bad_name"))
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*60e9)
		defer cancel()
		out, err := exec.CommandContext(ctx, "docker", "exec", "-u", "0", "berth-dev-"+r.PathValue("name"), "sh", "/opt/berth/boot.sh", "sync-claude").CombinedOutput()
		if err != nil {
			agent.WriteErr(w, errs.New(500, "dev.claude_failed", "detail", tail(strings.TrimSpace(string(out)), 600)))
			return
		}
		agent.WriteJSON(w, 200, map[string]bool{"ok": true})
	})
	s.Handle("POST /v1/dev/{name}/guard", func(w http.ResponseWriter, r *http.Request, body []byte) {
		var in struct {
			On bool `json:"on"`
		}
		json.Unmarshal(body, &in)
		if err := Guard(r.Context(), r.PathValue("name"), in.On); err != nil {
			agent.WriteErr(w, err)
			return
		}
		agent.WriteJSON(w, 200, map[string]any{"ok": true, "bridge": BridgeName(r.PathValue("name"))})
	})
	s.Handle("POST /v1/dev/{name}/claude/stop", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		if !ValidName(r.PathValue("name")) {
			agent.WriteErr(w, errs.New(400, "dev.bad_name"))
			return
		}
		StopClaude(r.PathValue("name"))
		agent.WriteJSON(w, 200, map[string]bool{"ok": true})
	})
	s.Handle("DELETE /v1/dev/{name}/state", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		if err := RemoveState(r.PathValue("name")); err != nil {
			agent.WriteErr(w, err)
			return
		}
		agent.WriteJSON(w, 200, map[string]bool{"ok": true})
	})
}
