package pipeline

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/wangsirco/berth/internal/agent"
	"github.com/wangsirco/berth/internal/errs"
)

var runIDRe = regexp.MustCompile(`^[a-z0-9-]{1,40}$`)

// Register adds the build-host routes to the agent.
func Register(s *agent.Server, x *Executor) {
	var mu sync.Mutex
	running := map[string]context.CancelFunc{} // by run id
	busy := map[string]bool{}                  // by pipeline
	registerDeploy(s, x)

	s.Handle("POST /v1/pipeline/run", func(w http.ResponseWriter, r *http.Request, body []byte) {
		var req RunRequest
		if json.Unmarshal(body, &req) != nil || !nameRe.MatchString(req.Pipeline) || !runIDRe.MatchString(req.RunID) {
			agent.WriteErr(w, errs.New(400, "params.invalid_json"))
			return
		}
		if !req.SpecFromRepo {
			if err := req.Spec.Validate(); err != nil {
				agent.WriteErr(w, specErr(err))
				return
			}
		}
		mu.Lock()
		if busy[req.Pipeline] {
			mu.Unlock()
			agent.WriteErr(w, errs.New(409, "pipeline.busy", "name", req.Pipeline))
			return
		}
		ctx, cancel := context.WithCancel(r.Context())
		busy[req.Pipeline], running[req.RunID] = true, cancel
		mu.Unlock()
		defer func() {
			mu.Lock()
			delete(busy, req.Pipeline)
			delete(running, req.RunID)
			mu.Unlock()
			cancel()
		}()
		st := agent.NewStream(w)
		log := func(l string) { st.Send(map[string]string{"l": l}) }
		res, err := x.Run(ctx, req, log)
		b, _ := json.Marshal(res)
		log("::result " + string(b))
		st.End(err)
	})
	s.Handle("POST /v1/pipeline/cancel", func(w http.ResponseWriter, r *http.Request, body []byte) {
		var in struct {
			RunID string `json:"run_id"`
		}
		json.Unmarshal(body, &in)
		mu.Lock()
		c, ok := running[in.RunID]
		mu.Unlock()
		if ok {
			c()
		}
		agent.WriteJSON(w, 200, map[string]bool{"cancelled": ok})
	})
	s.Handle("PUT /v1/pipeline/{name}/upload", func(w http.ResponseWriter, r *http.Request, body []byte) {
		name := r.PathValue("name")
		if !nameRe.MatchString(name) || len(body) < 2 || body[0] != 0x1f || body[1] != 0x8b {
			agent.WriteErr(w, errs.New(400, "pipeline.bad_upload"))
			return
		}
		// check it extracts cleanly before accepting it
		if err := Untar(bytes.NewReader(body), filepath.Join(os.TempDir(), "berth-check-"+name)); err != nil {
			os.RemoveAll(filepath.Join(os.TempDir(), "berth-check-"+name))
			agent.WriteErr(w, errs.New(400, "pipeline.bad_upload", "detail", err.Error()))
			return
		}
		os.RemoveAll(filepath.Join(os.TempDir(), "berth-check-"+name))
		p := x.UploadPath(name)
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p+".tmp", body, 0o600); err != nil {
			agent.WriteErr(w, err)
			return
		}
		os.Rename(p+".tmp", p)
		agent.WriteJSON(w, 200, map[string]int{"bytes": len(body)})
	})
	s.Handle("GET /v1/binfmt", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		m, _ := filepath.Glob("/proc/sys/fs/binfmt_misc/qemu-*")
		var out []string
		for _, f := range m {
			out = append(out, strings.TrimPrefix(filepath.Base(f), "qemu-"))
		}
		agent.WriteJSON(w, 200, map[string]any{"emulators": out})
	})
	s.Handle("POST /v1/binfmt/install", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		st := agent.NewStream(w)
		st.End(run(r.Context(), func(l string) { st.Send(map[string]string{"l": l}) }, "/", nil,
			x.Docker, "run", "--privileged", "--rm", "tonistiigi/binfmt", "--install", "amd64,arm64"))
	})
}

func specErr(err error) error {
	if p, ok := err.(*Problem); ok {
		kv := []any{}
		for k, v := range p.Params {
			kv = append(kv, k, v)
		}
		return errs.New(400, p.Code, kv...)
	}
	return err
}

// SpecErr converts a validation problem into a translatable error.
func SpecErr(err error) error { return specErr(err) }
