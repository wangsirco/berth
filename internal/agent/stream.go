package agent

import (
	"encoding/json"
	"net/http"
	"sync"

	"github.com/wangsirco/berth/internal/errs"
)

// StreamWriter writes NDJSON lines and flushes each one.
type StreamWriter struct {
	mu sync.Mutex
	w  http.ResponseWriter
	f  http.Flusher
}

func NewStream(w http.ResponseWriter) *StreamWriter {
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.WriteHeader(200)
	f, _ := w.(http.Flusher)
	return &StreamWriter{w: w, f: f}
}

func (s *StreamWriter) Send(v any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, _ := json.Marshal(v)
	s.w.Write(append(b, '\n'))
	if s.f != nil {
		s.f.Flush()
	}
}

// StreamEnd is the last line of a streamed operation.
type StreamEnd struct {
	Done  bool    `json:"done"`
	Error *errs.E `json:"error,omitempty"`
}

func (s *StreamWriter) End(err error) {
	e := StreamEnd{Done: true}
	if err != nil {
		e.Error = errs.From(err)
	}
	s.Send(e)
}
