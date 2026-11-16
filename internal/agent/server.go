package agent

import (
	"encoding/json"
	"errors"
	"log"
	"net"
	"net/http"
	"os"
	"os/user"
	"strconv"
	"sync"

	"github.com/wangsirco/berth/internal/agentproto"
	"github.com/wangsirco/berth/internal/errs"
	"github.com/wangsirco/berth/internal/sign"
)

type Config struct {
	Socket string
	Keys   map[string][]byte
	Group  string // 可连接 socket 的组（berth），为空则 0600
}

type HandlerFunc func(w http.ResponseWriter, r *http.Request, body []byte)

type Server struct {
	cfg   Config
	mux   *http.ServeMux
	ver   *sign.Verifier
	ln    net.Listener
	ready chan struct{}
	once  sync.Once
	srv   *http.Server
}

func NewServer(cfg Config) *Server {
	s := &Server{cfg: cfg, mux: http.NewServeMux(), ver: sign.NewVerifier(cfg.Keys), ready: make(chan struct{})}
	s.Handle("GET /v1/ping", func(w http.ResponseWriter, r *http.Request, _ []byte) { WriteJSON(w, 200, map[string]bool{"ok": true}) })
	s.Handle("GET /v1/caps", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		if r.URL.Query().Get("fresh") == "1" { // a decision rests on it (migration checks): look again
			ForgetCaps()
		}
		WriteJSON(w, 200, Caps())
	})
	registerSystem(s)
	registerSelfUpdate(s)
	registerSync(s)
	registerPublicIP(s)
	registerRegistry(s)
	registerCompat(s)
	registerHTTPProbe(s)
	registerRegistryAuth(s)
	registerPanelSnapshot(s)
	registerPanelRestore(s)
	registerPlugins(s)
	registerAV(s)
	return s
}

func (s *Server) Handle(pattern string, h HandlerFunc) {
	s.mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
		body, err := s.ver.Verify(r)
		if err != nil {
			WriteErr(w, errs.New(401, "agent.bad_signature", "detail", err.Error()))
			return
		}
		h(w, r, body)
	})
}

func (s *Server) ListenAndServe() error {
	_ = os.Remove(s.cfg.Socket)
	ln, err := net.Listen("unix", s.cfg.Socket)
	if err != nil {
		return err
	}
	mode := os.FileMode(0o600)
	if s.cfg.Group != "" {
		if g, err := user.LookupGroup(s.cfg.Group); err == nil {
			gid, _ := strconv.Atoi(g.Gid)
			_ = os.Chown(s.cfg.Socket, 0, gid)
			mode = 0o660
		}
	}
	_ = os.Chmod(s.cfg.Socket, mode)
	s.ln = ln
	s.srv = &http.Server{Handler: s.mux}
	s.once.Do(func() { close(s.ready) })
	err = s.srv.Serve(ln)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// ServeListener serves the same signed routes on another listener (a remote agent's
// multiplexed session to the panel). It returns when ln is closed.
func (s *Server) ServeListener(ln net.Listener) error {
	srv := &http.Server{Handler: s.mux}
	s.once.Do(func() { close(s.ready) })
	err := srv.Serve(ln)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func (s *Server) WaitReady() { <-s.ready }
func (s *Server) Close() error {
	if s.srv != nil {
		return s.srv.Close()
	}
	return nil
}

func WriteJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("agent: write: %v", err)
	}
}

func WriteErr(w http.ResponseWriter, err error) {
	e := errs.From(err)
	var b agentproto.ErrBody
	b.Error.Code, b.Error.Params = e.Code, e.Params
	WriteJSON(w, e.Status, b)
}
