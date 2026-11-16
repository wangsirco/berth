package pty

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/user"
	"regexp"
	"time"

	"github.com/coder/websocket"

	"github.com/wangsirco/berth/internal/agent"
	"github.com/wangsirco/berth/internal/errs"
)

type createReq struct {
	Target    string `json:"target"` // host | container | instance (Incus shell) | instance-console (VM serial console)
	Container string `json:"container"`
	User      string `json:"user"`
	Cols      int    `json:"cols"`
	Rows      int    `json:"rows"`
	Record    bool   `json:"record"`
	Owner     string `json:"owner"`
	Title     string `json:"title"`
	Dir       string `json:"dir"`     // dev: the project folder inside the container
	Program   string `json:"program"` // dev: "claude" starts Claude Code (then a shell)
}

var (
	safeName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,127}$`)
	devName  = regexp.MustCompile(`^berth-dev-[a-z0-9][a-z0-9-]{0,30}$`)
	devUser  = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)
	devDir   = regexp.MustCompile(`^/[A-Za-z0-9._/-]{0,200}$`)
)

// argv builds the command for a session. Host shells are login shells of the given user.
func argv(in createReq) ([]string, []string, string, error) {
	switch in.Target {
	case "container":
		if !safeName.MatchString(in.Container) {
			return nil, nil, "", errs.New(400, "container.not_found", "name", in.Container)
		}
		return []string{"docker", "exec", "-it", "-e", "TERM=xterm-256color", in.Container, "sh", "-c",
			"command -v bash >/dev/null 2>&1 && exec bash || exec sh"}, nil, "", nil
	case "dev": // a dev workspace: its user, in the project folder, as a login shell
		if !devName.MatchString(in.Container) || !devUser.MatchString(in.User) || !devDir.MatchString(in.Dir) {
			return nil, nil, "", errs.New(400, "dev.not_found", "name", in.Container)
		}
		shell := "command -v bash >/dev/null 2>&1 && exec bash -l || exec sh -l"
		if in.Program == "claude" { // the last conversation in this folder, else a new one; a shell after it
			shell = "command -v bash >/dev/null 2>&1 && exec bash -l /opt/berth/claude-chat.sh || exec sh -l /opt/berth/claude-chat.sh"
		}
		return []string{"docker", "exec", "-it", "-e", "TERM=xterm-256color", "-u", in.User, "-w", in.Dir, in.Container, "sh", "-c", shell}, nil, "", nil
	case "instance":
		if !safeName.MatchString(in.Container) {
			return nil, nil, "", errs.New(400, "vm.not_found", "name", in.Container)
		}
		return []string{"incus", "exec", in.Container, "--env", "TERM=xterm-256color", "--", "sh", "-c",
			"command -v bash >/dev/null 2>&1 && exec bash -l || exec sh -l"}, nil, "", nil
	case "instance-console":
		if !safeName.MatchString(in.Container) {
			return nil, nil, "", errs.New(400, "vm.not_found", "name", in.Container)
		}
		return []string{"incus", "console", in.Container}, nil, "", nil
	case "host", "":
		u, err := user.Lookup(in.User)
		if err != nil {
			return nil, nil, "", errs.New(400, "terminal.no_user", "user", in.User)
		}
		if in.User == "root" {
			shell := "/bin/bash"
			if _, err := os.Stat(shell); err != nil {
				shell = "/bin/sh"
			}
			return []string{shell, "-l"}, []string{"HOME=" + u.HomeDir, "USER=root", "LOGNAME=root"}, u.HomeDir, nil
		}
		return []string{"runuser", "-l", in.User}, nil, u.HomeDir, nil
	}
	return nil, nil, "", errs.New(400, "params.enum", "name", "target", "allowed", "host, container, instance, instance-console, dev")
}

func Register(s *agent.Server, m *Manager) {
	go func() {
		for range time.Tick(10 * time.Minute) {
			m.Reap()
		}
	}()
	s.Handle("POST /v1/pty", func(w http.ResponseWriter, r *http.Request, body []byte) {
		var in createReq
		if err := json.Unmarshal(body, &in); err != nil {
			agent.WriteErr(w, errs.New(400, "params.invalid_json"))
			return
		}
		args, env, dir, err := argv(in)
		if err != nil {
			agent.WriteErr(w, err)
			return
		}
		sess, err := m.Start(StartOpts{Argv: args, Env: env, Dir: dir, Cols: in.Cols, Rows: in.Rows, Record: in.Record,
			Target: in.Target, Container: in.Container, User: in.User, Owner: in.Owner, Title: in.Title})
		if err != nil {
			agent.WriteErr(w, err)
			return
		}
		agent.WriteJSON(w, 200, sess.Info())
	})
	s.Handle("GET /v1/pty", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		agent.WriteJSON(w, 200, m.List())
	})
	s.Handle("DELETE /v1/pty/{id}", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		if err := m.Kill(r.PathValue("id")); err != nil {
			agent.WriteErr(w, err)
			return
		}
		agent.WriteJSON(w, 200, map[string]bool{"ok": true})
	})
	s.Handle("GET /v1/pty/{id}/recording", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		p, err := m.RecordingPath(r.PathValue("id"))
		if err != nil {
			agent.WriteErr(w, err)
			return
		}
		w.Header().Set("Content-Type", "application/x-asciicast")
		http.ServeFile(w, r, p)
	})
	s.Handle("GET /v1/pty/{id}/attach", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		sess, ok := m.Get(r.PathValue("id"))
		if !ok {
			agent.WriteErr(w, errs.New(404, "terminal.not_found"))
			return
		}
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		conn.SetReadLimit(1 << 20)
		Bridge(r.Context(), conn, sess)
	})
}

type control struct {
	T    string `json:"t"`
	Cols int    `json:"cols"`
	Rows int    `json:"rows"`
}

// Bridge connects a WebSocket to a session: binary frames are terminal bytes both ways,
// text frames are control messages ({"t":"resize","cols":..,"rows":..}).
func Bridge(ctx context.Context, conn *websocket.Conn, sess *Session) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	out := sess.Attach()
	defer sess.Detach(out)
	go func() {
		defer cancel()
		for {
			typ, b, err := conn.Read(ctx)
			if err != nil {
				return
			}
			if typ == websocket.MessageText {
				var c control
				if json.Unmarshal(b, &c) == nil && c.T == "resize" {
					_ = sess.Resize(c.Cols, c.Rows)
				}
				continue
			}
			if err := sess.Write(b); err != nil {
				return
			}
		}
	}()
	for {
		select {
		case <-ctx.Done():
			conn.Close(websocket.StatusNormalClosure, "")
			return
		case b, ok := <-out:
			if !ok {
				conn.Close(websocket.StatusNormalClosure, "exited")
				return
			}
			wctx, c := context.WithTimeout(ctx, 10*time.Second)
			err := conn.Write(wctx, websocket.MessageBinary, b)
			c()
			if err != nil {
				return
			}
		}
	}
}
