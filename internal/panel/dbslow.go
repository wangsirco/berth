package panel

// Slow query log (慢查询): on/off and a threshold per instance, and the recent slow
// statements to look at.
//
// MySQL and MariaDB: SET GLOBAL (at once, no restart) with the log kept in the
// mysql.slow_log table. MariaDB forgets SET GLOBAL when it restarts, so Berth keeps the
// wanted setting and puts it back when it finds it off. PostgreSQL: ALTER SYSTEM, which
// the server keeps by itself; slow statements land in the container's log.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/wangsirco/berth/internal/agent/dbconn"
	"github.com/wangsirco/berth/internal/agent/docker"
	"github.com/wangsirco/berth/internal/change"
	"github.com/wangsirco/berth/internal/errs"
)

type slowEntry struct {
	At   string  `json:"at"`
	MS   float64 `json:"ms"`
	Rows int64   `json:"rows,omitempty"`
	DB   string  `json:"db,omitempty"`
	SQL  string  `json:"sql"`
}

type slowState struct {
	Supported bool        `json:"supported"`
	On        bool        `json:"on"`
	Seconds   float64     `json:"seconds"`
	Items     []slowEntry `json:"items"`
}

func (a *App) dbSQL(ctx context.Context, d dbInstance, sql string, readonly bool) (dbconn.Result, error) {
	var res dbconn.Result
	t, err := a.dbTarget(ctx, d, "")
	if err != nil {
		return res, err
	}
	body := targetBody(t)
	body["sql"], body["readonly"], body["limit"] = sql, readonly, 200
	err = a.call(ctx, d.Host, "POST", "/v1/db/query", body, &res)
	return res, err
}

func cell(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	default:
		return fmt.Sprint(x)
	}
}

var pgDurationRe = regexp.MustCompile(`duration: ([0-9.]+) ms\s+(?:statement|execute [^:]*): (.*)$`)

func (a *App) slowLog(ctx context.Context, d dbInstance) (slowState, error) {
	st := slowState{Items: []slowEntry{}}
	switch d.Engine {
	case "mysql", "mariadb":
		st.Supported = true
		r, err := a.dbSQL(ctx, d, "SHOW GLOBAL VARIABLES WHERE Variable_name IN ('slow_query_log','long_query_time')", true)
		if err != nil {
			return st, err
		}
		for _, row := range r.Rows {
			switch cell(row[0]) {
			case "slow_query_log":
				st.On = strings.EqualFold(cell(row[1]), "ON") || cell(row[1]) == "1"
			case "long_query_time":
				st.Seconds, _ = strconv.ParseFloat(cell(row[1]), 64)
			}
		}
		r, err = a.dbSQL(ctx, d, "SELECT start_time, TIME_TO_SEC(query_time)*1000, rows_examined, db, LEFT(CONVERT(sql_text USING utf8mb4), 1000) FROM mysql.slow_log ORDER BY start_time DESC LIMIT 100", true)
		if err == nil {
			for _, row := range r.Rows {
				ms, _ := strconv.ParseFloat(cell(row[1]), 64)
				rows, _ := strconv.ParseInt(cell(row[2]), 10, 64)
				st.Items = append(st.Items, slowEntry{At: cell(row[0]), MS: ms, Rows: rows, DB: cell(row[3]), SQL: cell(row[4])})
			}
		}
	case "postgres":
		st.Supported = true
		r, err := a.dbSQL(ctx, d, "SHOW log_min_duration_statement", true)
		if err != nil {
			return st, err
		}
		if len(r.Rows) > 0 {
			v := cell(r.Rows[0][0])
			st.On = v != "-1"
			st.Seconds = pgDuration(v)
		}
		if d.Native != "" {
			var lg struct{ Lines []string }
			if a.call(ctx, d.Host, "GET", "/v1/dbnative/postgres/log", nil, &lg) == nil {
				for _, l := range lg.Lines {
					if m := pgDurationRe.FindStringSubmatch(l); m != nil {
						ms, _ := strconv.ParseFloat(m[1], 64)
						at := ""
						if len(l) >= 19 {
							at = l[:19]
						}
						st.Items = append([]slowEntry{{At: at, MS: ms, SQL: firstNChars(m[2], 1000)}}, st.Items...)
					}
				}
				if len(st.Items) > 100 {
					st.Items = st.Items[:100]
				}
			}
		}
		if d.Container != "" {
			if ag, err := a.agent(d.Host); err == nil {
				cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
				defer cancel()
				if rc, err := ag.Stream(cctx, "GET", "/v1/docker/containers/"+url.PathEscape(d.Container)+"/logs?tail=3000", nil); err == nil {
					defer rc.Close()
					streamLogLines(rc, func(l docker.LogLine) bool {
						if m := pgDurationRe.FindStringSubmatch(l.Line); m != nil {
							ms, _ := strconv.ParseFloat(m[1], 64)
							st.Items = append([]slowEntry{{At: l.TS, MS: ms, SQL: firstNChars(m[2], 1000)}}, st.Items...)
						}
						return true
					})
					if len(st.Items) > 100 {
						st.Items = st.Items[:100]
					}
				}
			}
		}
	}
	return st, nil
}

func firstNChars(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

// pgDuration turns "1s", "500ms", "2min" or a bare number of ms into seconds.
func pgDuration(v string) float64 {
	n := strings.TrimRight(v, "abcdefghijklmnopqrstuvwxyz")
	f, _ := strconv.ParseFloat(n, 64)
	switch strings.TrimPrefix(v, n) {
	case "s":
		return f
	case "min":
		return f * 60
	case "h":
		return f * 3600
	default: // ms
		return f / 1000
	}
}

func slowKey(host string, id int64) string { return fmt.Sprintf("db.slow.%s.%d", host, id) }

func (a *App) applySlow(ctx context.Context, d dbInstance, on bool, secs float64) error {
	switch d.Engine {
	case "mysql", "mariadb":
		stmts := []string{fmt.Sprintf("SET GLOBAL long_query_time = %g", secs), "SET GLOBAL log_output = 'TABLE'", "SET GLOBAL slow_query_log = 1"}
		if !on {
			stmts = []string{"SET GLOBAL slow_query_log = 0"}
		}
		for _, s := range stmts {
			if _, err := a.dbSQL(ctx, d, s, false); err != nil {
				return err
			}
		}
	case "postgres":
		q := "ALTER SYSTEM RESET log_min_duration_statement" // back to the server's own setting
		if on {
			q = fmt.Sprintf("ALTER SYSTEM SET log_min_duration_statement = '%dms'", int(secs*1000))
		}
		if _, err := a.dbSQL(ctx, d, q, false); err != nil {
			return err
		}
		if _, err := a.dbSQL(ctx, d, "SELECT pg_reload_conf()", false); err != nil {
			return err
		}
	default:
		return errs.New(400, "db.slow_unsupported", "engine", d.Engine)
	}
	return nil
}

func init() {
	routeHooks = append(routeHooks, func(a *App, mux *http.ServeMux) {
		mux.HandleFunc("GET /api/v1/hosts/{host}/databases/{id}/slowlog", a.authed("db.view", func(w http.ResponseWriter, r *http.Request) {
			id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
			d, err := a.dbInstance(r.PathValue("host"), id)
			if err != nil {
				a.fail(w, r, err)
				return
			}
			st, err := a.slowLog(r.Context(), d)
			if err != nil {
				a.fail(w, r, err)
				return
			}
			writeJSON(w, 200, st)
		}))
	})
	// MariaDB forgets SET GLOBAL on restart: put a wanted slow log back (every few minutes)
	cronHooks = append(cronHooks, func(a *App, at time.Time) {
		if at.Minute()%5 != 0 {
			return
		}
		rows, err := a.db.Query(`SELECT key, value FROM settings WHERE key LIKE 'db.slow.%'`)
		if err != nil {
			return
		}
		type want struct {
			host string
			id   int64
			secs float64
		}
		var ws []want
		for rows.Next() {
			var k, v string
			rows.Scan(&k, &v)
			parts := strings.Split(k, ".")
			if len(parts) < 4 || v == "" {
				continue
			}
			id, _ := strconv.ParseInt(parts[len(parts)-1], 10, 64)
			secs, _ := strconv.ParseFloat(v, 64)
			ws = append(ws, want{strings.Join(parts[2:len(parts)-1], "."), id, secs})
		}
		rows.Close()
		for _, w := range ws {
			d, err := a.dbInstance(w.host, w.id)
			if err != nil || (d.Engine != "mysql" && d.Engine != "mariadb") {
				continue
			}
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			if st, err := a.slowLog(ctx, d); err == nil && !st.On {
				a.applySlow(ctx, d, true, w.secs)
			}
			cancel()
		}
	})
	change.Register(change.Kind{
		Name: "db.slowlog", Perm: "db.manage", Scope: "system:write", Title: "kind.db.slowlog", CLI: "berth db slowlog {{.instance}}",
		Params: change.ParamSchema{Props: map[string]change.Prop{"instance": {Type: "integer"}, "on": {Type: "boolean"},
			"seconds": {Type: "number", Desc: "log statements slower than this (default 1)"}}, Required: []string{"instance"}},
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			var p struct {
				Instance int64   `json:"instance"`
				On       bool    `json:"on"`
				Seconds  float64 `json:"seconds"`
			}
			json.Unmarshal(raw, &p)
			d, err := svc(env).dbInstance(env.Host, p.Instance)
			if err != nil {
				return change.Plan{}, err
			}
			if p.Seconds <= 0 {
				p.Seconds = 1
			}
			if p.Seconds > 3600 {
				return change.Plan{}, errs.New(400, "db.slow_bad_seconds")
			}
			cmds := map[string][]string{
				"mysql":    {fmt.Sprintf("SET GLOBAL long_query_time = %g; SET GLOBAL log_output = 'TABLE'; SET GLOBAL slow_query_log = %d", p.Seconds, map[bool]int{true: 1}[p.On])},
				"mariadb":  {fmt.Sprintf("SET GLOBAL long_query_time = %g; SET GLOBAL log_output = 'TABLE'; SET GLOBAL slow_query_log = %d", p.Seconds, map[bool]int{true: 1}[p.On]), "# Berth puts it back if the database restarts"},
				"postgres": {map[bool]string{true: fmt.Sprintf("ALTER SYSTEM SET log_min_duration_statement = '%dms'", int(p.Seconds*1000)), false: "ALTER SYSTEM RESET log_min_duration_statement"}[p.On] + "; SELECT pg_reload_conf()"},
			}[d.Engine]
			if cmds == nil {
				return change.Plan{}, errs.New(400, "db.slow_unsupported", "engine", d.Engine)
			}
			return change.Plan{Summary: map[bool]string{true: "plan.db.slowlog_on", false: "plan.db.slowlog_off"}[p.On], SummaryP: map[string]any{"name": d.Name, "seconds": p.Seconds},
				Risk: change.RiskLow, Undoable: true, Commands: cmds}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			a := svc(env)
			var p struct {
				Instance int64   `json:"instance"`
				On       bool    `json:"on"`
				Seconds  float64 `json:"seconds"`
			}
			json.Unmarshal(raw, &p)
			if p.Seconds <= 0 {
				p.Seconds = 1
			}
			d, err := a.dbInstance(env.Host, p.Instance)
			if err != nil {
				return nil, nil, err
			}
			before, _ := a.slowLog(env.Ctx, d)
			if err := a.applySlow(env.Ctx, d, p.On, p.Seconds); err != nil {
				return nil, nil, err
			}
			if p.On {
				a.setSetting(slowKey(env.Host, d.ID), strconv.FormatFloat(p.Seconds, 'f', -1, 64))
			} else {
				a.setSetting(slowKey(env.Host, d.ID), "")
			}
			secs := before.Seconds
			if secs <= 0 {
				secs = 1
			}
			u, _ := json.Marshal(map[string]any{"instance": d.ID, "on": before.On, "seconds": secs})
			return nil, &change.UndoRecipe{Kind: "db.slowlog", Params: u}, nil
		},
	})
}
