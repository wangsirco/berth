package panel

// On start the panel puts each host's site edge back to what its database says. The edge keeps
// whatever was loaded last — after a crash, a restore, or another panel on the same machine
// (the end-to-end test panel shares the dev server's edge) that may not be this panel's sites.

import (
	"context"
	"log"
	"time"

	"github.com/wangsirco/berth/internal/change"
)

func (a *App) syncEdges(ctx context.Context) {
	select {
	case <-ctx.Done():
		return
	case <-time.After(5 * time.Second): // let the agents connect
	}
	for _, h := range a.connectedHosts() {
		if on, _ := a.online(h); !on {
			continue
		}
		var n int
		a.db.QueryRow(`SELECT COUNT(*) FROM sites WHERE host=?`, h).Scan(&n)
		if n == 0 {
			continue // nothing of ours there; leave the edge alone
		}
		c, cancel := context.WithTimeout(ctx, time.Minute)
		if err := a.applyEdge(change.Env{Ctx: c, Host: h}); err != nil {
			log.Printf("panel: edge on %s: %v", h, err)
		}
		cancel()
	}
}

func init() {
	startHooks = append(startHooks, func(a *App, ctx context.Context) { go a.syncEdges(ctx) })
}
