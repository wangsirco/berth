package panel

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/wangsirco/berth/internal/change"
	"github.com/wangsirco/berth/internal/tasks"
)

// hub fans out panel events (changes, metrics) to SSE subscribers.
type hub struct {
	mu   sync.Mutex
	next int
	subs map[int]chan event
}

type event struct {
	Type    string `json:"type"`
	Data    any    `json:"data"`
	visible func(r *http.Request) bool
}

func (h *hub) subscribe() (<-chan event, func()) {
	ch := make(chan event, 128)
	h.mu.Lock()
	if h.subs == nil {
		h.subs = map[int]chan event{}
	}
	id := h.next
	h.next++
	h.subs[id] = ch
	h.mu.Unlock()
	return ch, func() { h.mu.Lock(); delete(h.subs, id); h.mu.Unlock() }
}

func (h *hub) publish(e event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, ch := range h.subs {
		select {
		case ch <- e:
		default:
		}
	}
}

func (h *hub) count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.subs)
}

func (a *App) publishChange(typ string, c *change.Change) {
	v := viewOf(c)
	a.hub.publish(event{Type: typ, Data: v, visible: func(r *http.Request) bool {
		return canSee(r.WithContext(contextWith(r, appKey, a)), c)
	}})
}

// GET /api/v1/events — Server-Sent Events: task.update, task.log, change.done,
// change.pending, metrics.tick. Heartbeat every 15 s.
func (a *App) handleEvents(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", 500)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(200)
	fmt.Fprint(w, ": hello\n\n")
	fl.Flush()

	he, stopH := a.hub.subscribe()
	defer stopH()
	var te <-chan tasks.Event
	if a.queue != nil {
		var stopT func()
		te, stopT = a.queue.Subscribe()
		defer stopT()
	}
	u := currentUser(r)
	canTasks := u.Can("overview.view")
	beat := time.NewTicker(15 * time.Second)
	defer beat.Stop()
	send := func(typ string, data any) bool {
		b, err := json.Marshal(data)
		if err != nil {
			return true
		}
		_, err = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", typ, b)
		fl.Flush()
		return err == nil
	}
	for {
		select {
		case <-r.Context().Done():
			return
		case <-beat.C:
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
				return
			}
			fl.Flush()
		case e := <-he:
			if e.visible != nil && !e.visible(r) {
				continue
			}
			if !send(e.Type, e.Data) {
				return
			}
		case e := <-te:
			if !canTasks {
				continue
			}
			if !send(e.Type, e) {
				return
			}
		}
	}
}
