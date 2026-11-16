package panel

import (
	"context"
	"net/http"
	"strconv"

	"github.com/wangsirco/berth/internal/change"
	"github.com/wangsirco/berth/internal/errs"
	"github.com/wangsirco/berth/internal/tasks"
)

func init() {
	routeHooks = append(routeHooks, func(a *App, mux *http.ServeMux) {
		mux.HandleFunc("GET /api/v1/events", a.authed("", a.handleEvents))
		mux.HandleFunc("GET /api/v1/tasks", a.authed("overview.view", a.handleTasks))
		mux.HandleFunc("GET /api/v1/tasks/{id}", a.authed("overview.view", a.handleTask))
		mux.HandleFunc("GET /api/v1/tasks/{id}/logs", a.authed("overview.view", a.handleTaskLogs))
		mux.HandleFunc("POST /api/v1/tasks/{id}/cancel", a.authed("", a.handleTaskCancel))
	})
}

// setupQueue wires the task queue into the pipeline and cleans up after a restart.
func (a *App) setupQueue() {
	a.queue = tasks.New(a.db, a.memAvailableMB, a.pipe.Finish)
	a.queue.HeavyParallel = func() int { return max(1, a.settingInt("tasks.heavy_parallel", 1)) }
	a.pipe.Runner = a.queue
	if ids, err := tasks.MarkInterrupted(a.db); err == nil {
		for _, id := range ids {
			_, _ = a.db.Exec(`UPDATE changes SET status='failed', result='{"error":{"code":"tasks.interrupted"}}' WHERE id=? AND status='running'`, id)
		}
	}
}

// memAvailableMB asks the host's agent; if it cannot tell, it does not hold tasks back.
func (a *App) memAvailableMB(host string) int {
	c, err := a.agent(host)
	if err != nil {
		return 1 << 20
	}
	var m struct {
		MemAvail int64 `json:"mem_avail"`
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3e9)
	defer cancel()
	if err := c.Do(ctx, "GET", "/v1/metrics/host", nil, &m); err != nil || m.MemAvail == 0 {
		return 1 << 20
	}
	return int(m.MemAvail >> 20)
}

func (a *App) handleTasks(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	list, err := a.queue.List(r.URL.Query().Get("host"), limit)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	running, waiting := a.queue.Counts()
	writeJSON(w, 200, map[string]any{"items": list, "running": running, "waiting": waiting})
}

func (a *App) handleTask(w http.ResponseWriter, r *http.Request) {
	t, err := a.queue.Get(r.PathValue("id"))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, 200, t)
}

func (a *App) handleTaskLogs(w http.ResponseWriter, r *http.Request) {
	after, _ := strconv.Atoi(r.URL.Query().Get("after"))
	t, err := a.queue.Get(r.PathValue("id"))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	lines, err := a.queue.Logs(t.ID, after, 0)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"items": lines})
}

// Cancelling needs the permission of the change being run, or being the one who started it.
func (a *App) handleTaskCancel(w http.ResponseWriter, r *http.Request) {
	t, err := a.queue.Get(r.PathValue("id"))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	c, err := a.pipe.Get(t.ChangeID)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	k, _ := change.Get(c.Kind)
	if c.Actor.ID != currentActor(r).ID && !currentUser(r).Can(k.Perm) {
		a.fail(w, r, errs.New(403, "auth.forbidden", "perm", k.Perm))
		return
	}
	if err := a.queue.Cancel(t.ID); err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}
