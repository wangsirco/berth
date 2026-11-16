// Package pty keeps terminal sessions alive in berth agent, like tmux: closing the browser
// detaches, the shell keeps running, and re-attaching replays recent output.
package pty

import (
	"encoding/json"

	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/creack/pty"

	"github.com/wangsirco/berth/internal/errs"
	"github.com/wangsirco/berth/internal/sign"
)

const scrollback = 256 << 10

// ring keeps the last n bytes written.
type ring struct {
	buf  []byte
	size int
}

func newRing(size int) *ring { return &ring{size: size} }

func (r *ring) Write(p []byte) {
	r.buf = append(r.buf, p...)
	if over := len(r.buf) - r.size; over > 0 {
		r.buf = append([]byte{}, r.buf[over:]...)
	}
}

func (r *ring) Bytes() []byte { return append([]byte{}, r.buf...) }

type StartOpts struct {
	Argv      []string
	Env       []string
	Dir       string
	Cols      int
	Rows      int
	Record    bool
	Target    string // host | container
	Container string
	User      string
	Owner     string // opaque, set by the panel (who opened it)
	Title     string
}

type Info struct {
	ID         string `json:"id"`
	Target     string `json:"target"`
	Container  string `json:"container,omitempty"`
	User       string `json:"user"`
	Owner      string `json:"owner"`
	Title      string `json:"title"`
	Created    int64  `json:"created"`
	LastActive int64  `json:"last_active"`
	Clients    int    `json:"clients"`
	Recording  bool   `json:"recording"`
	Exited     bool   `json:"exited"`
}

type Session struct {
	ID   string
	info Info
	cmd  *exec.Cmd
	f    *os.File

	mu      sync.Mutex
	ring    *ring
	clients map[chan []byte]struct{}
	cast    *os.File
	start   time.Time
	exited  chan struct{}
}

type Manager struct {
	recDir string
	mu     sync.Mutex
	m      map[string]*Session
	// IdleTTL removes detached sessions that saw no output or input for this long.
	IdleTTL time.Duration
}

func NewManager(recDir string) *Manager {
	return &Manager{recDir: recDir, m: map[string]*Session{}, IdleTTL: 24 * time.Hour}
}

func (m *Manager) Start(o StartOpts) (*Session, error) {
	if len(o.Argv) == 0 {
		return nil, errs.New(400, "terminal.no_command")
	}
	cmd := exec.Command(o.Argv[0], o.Argv[1:]...)
	cmd.Env = append(append([]string{}, os.Environ()...), "TERM=xterm-256color", "COLORTERM=truecolor")
	cmd.Env = append(cmd.Env, o.Env...)
	cmd.Dir = o.Dir
	size := &pty.Winsize{Cols: uint16(max(o.Cols, 20)), Rows: uint16(max(o.Rows, 5))}
	f, err := pty.StartWithSize(cmd, size)
	if err != nil {
		return nil, errs.New(500, "terminal.start_failed", "detail", err.Error())
	}
	now := time.Now()
	s := &Session{ID: "pty_" + sign.RandomHex(8), cmd: cmd, f: f, ring: newRing(scrollback), clients: map[chan []byte]struct{}{}, start: now, exited: make(chan struct{})}
	s.info = Info{ID: s.ID, Target: o.Target, Container: o.Container, User: o.User, Owner: o.Owner, Title: o.Title, Created: now.Unix(), LastActive: now.Unix()}
	if o.Record && m.recDir != "" {
		if err := os.MkdirAll(m.recDir, 0o700); err == nil {
			if c, err := os.OpenFile(filepath.Join(m.recDir, s.ID+".cast"), os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600); err == nil {
				hdr, _ := json.Marshal(map[string]any{"version": 2, "width": size.Cols, "height": size.Rows, "timestamp": now.Unix(), "title": o.Title})
				c.Write(append(hdr, '\n'))
				s.cast = c
				s.info.Recording = true
			}
		}
	}
	m.mu.Lock()
	m.m[s.ID] = s
	m.mu.Unlock()
	go s.pump()
	go func() {
		_ = cmd.Wait()
		s.mu.Lock()
		s.info.Exited = true
		s.mu.Unlock()
		close(s.exited)
		time.AfterFunc(5*time.Minute, func() { m.remove(s.ID) })
	}()
	return s, nil
}

// pump copies PTY output to the scrollback, the recording and every attached client.
func (s *Session) pump() {
	buf := make([]byte, 32<<10)
	for {
		n, err := s.f.Read(buf)
		if n > 0 {
			chunk := append([]byte{}, buf[:n]...)
			s.mu.Lock()
			s.ring.Write(chunk)
			s.info.LastActive = time.Now().Unix()
			if s.cast != nil {
				ev, _ := json.Marshal([]any{time.Since(s.start).Seconds(), "o", string(chunk)})
				s.cast.Write(append(ev, '\n'))
			}
			for ch := range s.clients {
				select {
				case ch <- chunk:
				default: // a slow client misses output rather than stalling the shell
				}
			}
			s.mu.Unlock()
		}
		if err != nil {
			s.mu.Lock()
			for ch := range s.clients {
				close(ch)
			}
			s.clients = map[chan []byte]struct{}{}
			if s.cast != nil {
				s.cast.Close()
				s.cast = nil
			}
			s.mu.Unlock()
			return
		}
	}
}

// Attach returns a channel that first gets the scrollback, then live output. It is
// closed when the shell exits.
func (s *Session) Attach() chan []byte {
	ch := make(chan []byte, 256)
	s.mu.Lock()
	defer s.mu.Unlock()
	if back := s.ring.Bytes(); len(back) > 0 {
		ch <- back
	}
	if s.info.Exited {
		close(ch)
		return ch
	}
	s.clients[ch] = struct{}{}
	return ch
}

func (s *Session) Detach(ch chan []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.clients[ch]; ok {
		delete(s.clients, ch)
		close(ch)
	}
}

func (s *Session) Write(p []byte) error {
	s.mu.Lock()
	s.info.LastActive = time.Now().Unix()
	s.mu.Unlock()
	_, err := s.f.Write(p)
	return err
}

func (s *Session) Resize(cols, rows int) error {
	if cols <= 0 || rows <= 0 || cols > 1000 || rows > 500 {
		return nil
	}
	return pty.Setsize(s.f, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)})
}

func (s *Session) Info() Info {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := s.info
	i.Clients = len(s.clients)
	return i
}

func (m *Manager) Get(id string) (*Session, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.m[id]
	return s, ok
}

func (m *Manager) Alive(id string) bool {
	s, ok := m.Get(id)
	if !ok {
		return false
	}
	select {
	case <-s.exited:
		return false
	default:
		return true
	}
}

func (m *Manager) List() []Info {
	m.mu.Lock()
	ss := make([]*Session, 0, len(m.m))
	for _, s := range m.m {
		ss = append(ss, s)
	}
	m.mu.Unlock()
	out := make([]Info, 0, len(ss))
	for _, s := range ss {
		out = append(out, s.Info())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Created < out[j].Created })
	return out
}

// Kill ends a session (SIGHUP like closing a terminal, then SIGKILL after 3 s).
func (m *Manager) Kill(id string) error {
	s, ok := m.Get(id)
	if !ok {
		return errs.New(404, "terminal.not_found")
	}
	if s.cmd.Process != nil {
		_ = s.cmd.Process.Signal(hangup)
		select {
		case <-s.exited:
		case <-time.After(3 * time.Second):
			_ = s.cmd.Process.Kill()
		}
	}
	s.f.Close()
	m.remove(id)
	return nil
}

func (m *Manager) remove(id string) {
	m.mu.Lock()
	delete(m.m, id)
	m.mu.Unlock()
}

// Reap kills detached sessions idle longer than IdleTTL; call it periodically.
func (m *Manager) Reap() {
	cut := time.Now().Add(-m.IdleTTL).Unix()
	for _, i := range m.List() {
		if i.Clients == 0 && i.LastActive < cut {
			_ = m.Kill(i.ID)
		}
	}
}

func (m *Manager) RecordingPath(id string) (string, error) {
	p := filepath.Join(m.recDir, filepath.Base(id)+".cast")
	if _, err := os.Stat(p); err != nil {
		return "", errs.New(404, "terminal.no_recording")
	}
	return p, nil
}
