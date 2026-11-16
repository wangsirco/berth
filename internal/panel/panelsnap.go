package panel

// Panel snapshot for moving the panel to another machine: its database, keys and
// certificates in one password-sealed file. Restore with `berth restore-panel <file>`.

import (
	"io"
	"net/http"
	"path"
	"time"

	"github.com/wangsirco/berth/internal/change"
	"github.com/wangsirco/berth/internal/errs"
	"github.com/wangsirco/berth/internal/snapshot"
)

func (a *App) handlePanelSnapshot(w http.ResponseWriter, r *http.Request) {
	if err := requireSession(r); err != nil {
		a.fail(w, r, err)
		return
	}
	if err := a.requireReauth(r); err != nil {
		a.fail(w, r, err)
		return
	}
	var in struct {
		Password string `json:"password"`
	}
	if err := readJSON(r, &in); err != nil {
		a.fail(w, r, err)
		return
	}
	if len(in.Password) < 8 {
		a.fail(w, r, errs.New(400, "snapshot.weak_password"))
		return
	}
	db := path.Join(a.cfg.DataDir, "panel-snapshot.db")
	removeFile(db)
	defer removeFile(db)
	a.db.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`)
	if _, err := a.db.Exec(`VACUUM INTO ?`, db); err != nil {
		a.fail(w, r, err)
		return
	}
	ag, err := a.agent("local")
	if err != nil {
		a.fail(w, r, err)
		return
	}
	rc, err := ag.Stream(r.Context(), "POST", "/v1/panel/snapshot", map[string]string{"db": db, "data_dir": a.cfg.DataDir, "etc_dir": a.cfg.EtcDir})
	if err != nil {
		a.fail(w, r, err)
		return
	}
	defer rc.Close()
	a.pipe.Record(currentActor(r), "local", "panel.snapshot", map[string]any{}, change.Plan{Summary: "plan.panel.snapshot", Risk: change.RiskHigh})
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="berth-`+time.Now().Format("20060102-1504")+`.berthsnap"`)
	if err := snapshot.Seal(w, rc, in.Password); err != nil && err != io.EOF {
		return // the client sees a cut download; Open reports it as damaged
	}
}

func init() {
	routeHooks = append(routeHooks, func(a *App, mux *http.ServeMux) {
		mux.HandleFunc("POST /api/v1/panel/snapshot", a.superOnly(a.handlePanelSnapshot))
	})
}
