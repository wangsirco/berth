package panel

import (
	"time"

	"github.com/wangsirco/berth/internal/change"
)

// Relation is an edge in the object graph (方案 §4.6).
type Relation struct {
	From   change.Object `json:"from"`
	To     change.Object `json:"to"`
	Kind   string        `json:"kind"` // manages | contains | uses | joins
	Source string        `json:"source"`
}

func (a *App) relate(host string, from, to change.Object, kind, source string) {
	_, _ = a.db.Exec(`INSERT INTO relations(from_type, from_id, to_type, to_id, kind, host, source) VALUES(?,?,?,?,?,?,?)
		ON CONFLICT DO UPDATE SET source=CASE WHEN relations.source='module' THEN 'module' ELSE excluded.source END`,
		from.Type, from.ID, to.Type, to.ID, kind, host, source)
}

func (a *App) unrelate(host string, from, to change.Object, kind string) {
	_, _ = a.db.Exec(`DELETE FROM relations WHERE host=? AND from_type=? AND from_id=? AND to_type=? AND to_id=? AND kind=?`,
		host, from.Type, from.ID, to.Type, to.ID, kind)
}

// related lists every edge touching (t, id) on host.
func (a *App) related(host, t, id string) []Relation {
	rows, err := a.db.Query(`SELECT from_type, from_id, to_type, to_id, kind, source FROM relations
		WHERE host=? AND ((from_type=? AND from_id=?) OR (to_type=? AND to_id=?)) ORDER BY kind, to_type, to_id`, host, t, id, t, id)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := []Relation{}
	for rows.Next() {
		var r Relation
		if rows.Scan(&r.From.Type, &r.From.ID, &r.To.Type, &r.To.ID, &r.Kind, &r.Source) == nil {
			r.From.Host, r.To.Host = host, host
			out = append(out, r)
		}
	}
	return out
}

// impact lists objects affected by deleting (t, id): neighbours over uses/contains/joins
// edges in both directions, one hop. "manages" (host → project) is bookkeeping, not impact.
func (a *App) impact(host, t, id string) []change.Object {
	seen := map[string]bool{t + "/" + id: true}
	out := []change.Object{}
	for _, r := range a.related(host, t, id) {
		if r.Kind == "manages" {
			continue
		}
		o := r.To
		if o.Type == t && o.ID == id {
			o = r.From
		}
		if k := o.Type + "/" + o.ID; !seen[k] {
			seen[k] = true
			o.Name = o.ID
			out = append(out, o)
		}
	}
	return out
}

// replaceScanned swaps all scan-sourced edges of the given kinds on host for fresh ones,
// keeping module-written edges (they come from explicit actions such as adopt).
func (a *App) replaceScanned(host string, kinds []string, edges []Relation) {
	tx, err := a.db.Begin()
	if err != nil {
		return
	}
	defer tx.Rollback()
	for _, k := range kinds {
		if _, err := tx.Exec(`DELETE FROM relations WHERE host=? AND kind=? AND source='scan'`, host, k); err != nil {
			return
		}
	}
	for _, e := range edges {
		if _, err := tx.Exec(`INSERT OR IGNORE INTO relations(from_type, from_id, to_type, to_id, kind, host, source) VALUES(?,?,?,?,?,?, 'scan')`,
			e.From.Type, e.From.ID, e.To.Type, e.To.ID, e.Kind, host); err != nil {
			return
		}
	}
	_ = tx.Commit()
	a.mu.Lock()
	a.lastScan[host] = time.Now()
	a.mu.Unlock()
}
