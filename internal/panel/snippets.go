package panel

// Saved commands (常用命令): the ones people retype — restart a service, follow a log, clear a
// cache. Each person keeps their own; admins can share some with everyone. The terminal, SSH
// sessions and remote desktops insert them with one click; {{name}} or {{name:default}} parts are
// asked for first. Most-used come first.

import (
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/wangsirco/berth/internal/errs"
)

type snippet struct {
	ID        int64  `json:"id"`
	UserID    int64  `json:"user_id"`
	Owner     string `json:"owner,omitempty"`
	Shared    bool   `json:"shared"`
	Name      string `json:"name"`
	Command   string `json:"command"`
	Note      string `json:"note"`
	Folder    string `json:"folder"`
	Danger    bool   `json:"danger"`
	Uses      int64  `json:"uses"`
	LastUsed  int64  `json:"last_used"`
	CreatedAt int64  `json:"created_at"`
	Mine      bool   `json:"mine"`
}

const snippetCols = `s.id, s.user_id, COALESCE(u.username,''), s.shared, s.name, s.command, s.note, s.folder, s.danger, s.uses, s.last_used, s.created_at`

func scanSnippets(rows *sql.Rows, me int64) []snippet {
	out := []snippet{}
	for rows.Next() {
		var x snippet
		var shared, danger int
		if rows.Scan(&x.ID, &x.UserID, &x.Owner, &shared, &x.Name, &x.Command, &x.Note, &x.Folder, &danger, &x.Uses, &x.LastUsed, &x.CreatedAt) == nil {
			x.Shared, x.Danger, x.Mine = shared == 1, danger == 1, x.UserID == me
			out = append(out, x)
		}
	}
	return out
}

func (in *snippet) clean() error {
	in.Name, in.Folder, in.Note = strings.TrimSpace(in.Name), strings.TrimSpace(in.Folder), strings.TrimSpace(in.Note)
	in.Command = strings.TrimRight(in.Command, " \t\r\n")
	if in.Name == "" || utf8.RuneCountInString(in.Name) > 60 || utf8.RuneCountInString(in.Folder) > 30 || utf8.RuneCountInString(in.Note) > 300 {
		return errs.New(400, "snippet.bad_name")
	}
	if strings.TrimSpace(in.Command) == "" || len(in.Command) > 8000 {
		return errs.New(400, "snippet.bad_command")
	}
	return nil
}

func (a *App) ownSnippet(u *User, id string) (snippet, error) {
	var x snippet
	n, _ := strconv.ParseInt(id, 10, 64)
	rows, err := a.db.Query(`SELECT `+snippetCols+` FROM snippets s LEFT JOIN users u ON u.id=s.user_id WHERE s.id=?`, n)
	if err != nil {
		return x, err
	}
	defer rows.Close()
	list := scanSnippets(rows, u.ID)
	if len(list) == 0 || (list[0].UserID != u.ID && !(list[0].Shared && u.Can("settings.manage"))) {
		return x, errs.New(404, "snippet.not_found")
	}
	return list[0], nil
}

func init() {
	routeHooks = append(routeHooks, func(a *App, mux *http.ServeMux) {
		mux.HandleFunc("GET /api/v1/snippets", a.authed("", func(w http.ResponseWriter, r *http.Request) {
			u := currentUser(r)
			rows, err := a.db.Query(`SELECT `+snippetCols+` FROM snippets s LEFT JOIN users u ON u.id=s.user_id WHERE s.user_id=? OR s.shared=1
				ORDER BY s.uses DESC, s.last_used DESC, s.id`, u.ID)
			if err != nil {
				a.fail(w, r, err)
				return
			}
			defer rows.Close()
			writeJSON(w, 200, map[string]any{"items": scanSnippets(rows, u.ID), "can_share": u.Can("settings.manage")})
		}))
		mux.HandleFunc("POST /api/v1/snippets", a.authed("", func(w http.ResponseWriter, r *http.Request) {
			u := currentUser(r)
			var in snippet
			if err := readJSON(r, &in); err != nil {
				a.fail(w, r, err)
				return
			}
			if err := in.clean(); err != nil {
				a.fail(w, r, err)
				return
			}
			if in.Shared && !u.Can("settings.manage") {
				a.fail(w, r, errs.New(403, "snippet.share_admin"))
				return
			}
			now := time.Now().Unix()
			res, err := a.db.Exec(`INSERT INTO snippets(user_id, shared, name, command, note, folder, danger, created_at, updated_at) VALUES(?,?,?,?,?,?,?,?,?)`,
				u.ID, b2i(in.Shared), in.Name, in.Command, in.Note, in.Folder, b2i(in.Danger), now, now)
			if err != nil {
				a.fail(w, r, err)
				return
			}
			in.ID, _ = res.LastInsertId()
			in.UserID, in.Mine, in.CreatedAt = u.ID, true, now
			writeJSON(w, 200, in)
		}))
		mux.HandleFunc("PUT /api/v1/snippets/{id}", a.authed("", func(w http.ResponseWriter, r *http.Request) {
			u := currentUser(r)
			cur, err := a.ownSnippet(u, r.PathValue("id"))
			if err != nil {
				a.fail(w, r, err)
				return
			}
			var in snippet
			if err := readJSON(r, &in); err != nil {
				a.fail(w, r, err)
				return
			}
			if err := in.clean(); err != nil {
				a.fail(w, r, err)
				return
			}
			if in.Shared && !u.Can("settings.manage") {
				a.fail(w, r, errs.New(403, "snippet.share_admin"))
				return
			}
			_, err = a.db.Exec(`UPDATE snippets SET shared=?, name=?, command=?, note=?, folder=?, danger=?, updated_at=? WHERE id=?`,
				b2i(in.Shared), in.Name, in.Command, in.Note, in.Folder, b2i(in.Danger), time.Now().Unix(), cur.ID)
			if err != nil {
				a.fail(w, r, err)
				return
			}
			w.WriteHeader(204)
		}))
		mux.HandleFunc("DELETE /api/v1/snippets/{id}", a.authed("", func(w http.ResponseWriter, r *http.Request) {
			cur, err := a.ownSnippet(currentUser(r), r.PathValue("id"))
			if err != nil {
				a.fail(w, r, err)
				return
			}
			a.db.Exec(`DELETE FROM snippets WHERE id=?`, cur.ID)
			w.WriteHeader(204)
		}))
		// used: counts toward the order (shared ones too, for everyone)
		mux.HandleFunc("POST /api/v1/snippets/{id}/used", a.authed("", func(w http.ResponseWriter, r *http.Request) {
			u := currentUser(r)
			n, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
			res, err := a.db.Exec(`UPDATE snippets SET uses=uses+1, last_used=? WHERE id=? AND (user_id=? OR shared=1)`, time.Now().Unix(), n, u.ID)
			if err == nil {
				if k, _ := res.RowsAffected(); k == 0 {
					err = errs.New(404, "snippet.not_found")
				}
			}
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				a.fail(w, r, err)
				return
			}
			w.WriteHeader(204)
		}))
	})
}
