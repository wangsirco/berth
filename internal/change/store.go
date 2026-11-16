package change

import (
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/wangsirco/berth/internal/errs"
)

const changeCols = `id, ts, host, kind, actor_type, actor_id, actor_name, via, ip, ai_session, intent,
	params, plan, risk, status, result, log, undo, reverts, reverted_by, finished_at`

func (p *Pipeline) save(c *Change) error {
	plan, err := json.Marshal(c.Plan)
	if err != nil {
		return err
	}
	undo := ""
	if c.Undo != nil {
		b, _ := json.Marshal(c.Undo)
		undo = string(b)
	}
	if c.Status == "done" || c.Status == "failed" || c.Status == "rejected" {
		if c.FinishedAt == 0 {
			c.FinishedAt = time.Now().Unix()
		}
	}
	params := c.Params
	if k, ok := Get(c.Kind); ok && len(k.Redact) > 0 {
		params = redact(params, k.Redact)
	}
	a := c.Actor
	_, err = p.db.Exec(`INSERT INTO changes (`+changeCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET status=excluded.status, plan=excluded.plan, risk=excluded.risk,
		result=excluded.result, log=excluded.log, undo=excluded.undo, finished_at=excluded.finished_at`,
		c.ID, c.TS, c.Host, c.Kind, a.Type, a.ID, a.Name, a.Via, a.IP, a.AISession, a.Intent,
		string(params), string(plan), string(c.Plan.Risk), c.Status, string(c.Result), c.Log, undo,
		c.Reverts, c.RevertedBy, c.FinishedAt)
	return err
}

func scanChange(row interface{ Scan(...any) error }) (*Change, error) {
	var c Change
	var params, plan, risk, result, undo string
	a := &c.Actor
	if err := row.Scan(&c.ID, &c.TS, &c.Host, &c.Kind, &a.Type, &a.ID, &a.Name, &a.Via, &a.IP, &a.AISession, &a.Intent,
		&params, &plan, &risk, &c.Status, &result, &c.Log, &undo, &c.Reverts, &c.RevertedBy, &c.FinishedAt); err != nil {
		return nil, err
	}
	c.Params = json.RawMessage(params)
	if err := json.Unmarshal([]byte(plan), &c.Plan); err != nil {
		return nil, err
	}
	if result != "" {
		c.Result = json.RawMessage(result)
	}
	if undo != "" {
		c.Undo = &UndoRecipe{}
		if err := json.Unmarshal([]byte(undo), c.Undo); err != nil {
			return nil, err
		}
	}
	return &c, nil
}

func (p *Pipeline) Get(id string) (*Change, error) {
	c, err := scanChange(p.db.QueryRow(`SELECT `+changeCols+` FROM changes WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errs.New(404, "change.not_found", "id", id)
	}
	return c, err
}

type ListFilter struct {
	Host, Kind, ActorID, Via, Status, ObjectType, ObjectID, AISession string
	Since, Until                                                      int64
	Limit                                                             int
	Before                                                            string // change id; returns older entries
}

func (p *Pipeline) List(f ListFilter) ([]*Change, error) {
	var where []string
	var args []any
	add := func(cond string, v any) { where = append(where, cond); args = append(args, v) }
	if f.Host != "" {
		add("host=?", f.Host)
	}
	if f.Kind != "" {
		add("kind=?", f.Kind)
	}
	if f.ActorID != "" {
		add("actor_id=?", f.ActorID)
	}
	if f.Via != "" {
		add("via=?", f.Via)
	}
	if f.Status != "" {
		add("status=?", f.Status)
	}
	if f.AISession != "" {
		add("ai_session=?", f.AISession)
	}
	if f.Since > 0 {
		add("ts>=?", f.Since)
	}
	if f.Until > 0 {
		add("ts<=?", f.Until)
	}
	if f.ObjectType != "" && f.ObjectID != "" {
		obj, _ := json.Marshal(Object{Type: f.ObjectType, ID: f.ObjectID})
		// Affected objects are stored as JSON; match the {"type":..,"id":..} prefix.
		add("plan LIKE ?", "%"+strings.TrimSuffix(string(obj), "}")+"%")
	}
	if f.Before != "" {
		add("(ts, id) < (SELECT ts, id FROM changes WHERE id=?)", f.Before)
	}
	if f.Limit <= 0 {
		f.Limit = 50
	}
	if f.Limit > 500 {
		f.Limit = 500
	}
	q := `SELECT ` + changeCols + ` FROM changes`
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += " ORDER BY ts DESC, id DESC LIMIT ?"
	args = append(args, f.Limit)
	rows, err := p.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Change
	for rows.Next() {
		c, err := scanChange(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// redact replaces the named top-level params with a placeholder.
func redact(raw json.RawMessage, names []string) json.RawMessage {
	m := map[string]any{}
	if json.Unmarshal(raw, &m) != nil {
		return json.RawMessage(`{}`)
	}
	for _, n := range names {
		if _, ok := m[n]; ok {
			m[n] = "[redacted]"
		}
	}
	b, _ := json.Marshal(m)
	return b
}
