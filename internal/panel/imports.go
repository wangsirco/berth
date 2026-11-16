package panel

// Import (方案 §14): read another panel's configuration and propose Berth changes. The
// scan only reads; the page then runs the ticked items one by one as ordinary changes.

import (
	"bytes"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"

	"github.com/wangsirco/berth/internal/errs"
	"github.com/wangsirco/berth/internal/panel/importer"
)

// default places to look on the host
var importPaths = map[string][]string{
	"npm":    {"/data/database.sqlite", "/opt/npm/data/database.sqlite", "/srv/npm/data/database.sqlite", "/root/npm/data/database.sqlite"},
	"1panel": {"/opt/1panel/db/1Panel.db", "/opt/1panel/db/agent.db"},
}

// fetchHostFile copies a file from the host (as root) into a temporary file.
func (a *App) fetchHostFile(r *http.Request, host, path string) (string, error) {
	ag, err := a.agent(host)
	if err != nil {
		return "", err
	}
	rc, err := ag.Stream(r.Context(), "POST", "/v1/files/download", map[string]any{"root": "/", "path": path})
	if err != nil {
		return "", err
	}
	defer rc.Close()
	f, err := os.CreateTemp("", "berth-import-*.db")
	if err != nil {
		return "", err
	}
	defer f.Close()
	if _, err := io.Copy(f, io.LimitReader(rc, 512<<20)); err != nil {
		os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}

func (a *App) handleImportScan(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Source string `json:"source"` // caddyfile | npm | 1panel
		Text   string `json:"text"`   // Caddyfile content
		Path   string `json:"path"`   // a file on the host
		File   string `json:"file"`   // an uploaded database, base64
	}
	// an uploaded database can be large: a higher limit than readJSON's
	if err := json.NewDecoder(io.LimitReader(r.Body, 96<<20)).Decode(&in); err != nil {
		a.fail(w, r, errs.New(400, "params.invalid_json"))
		return
	}
	host := r.PathValue("host")
	if in.Source == "caddyfile" {
		text := in.Text
		if text == "" && in.Path != "" {
			tmp, err := a.fetchHostFile(r, host, in.Path)
			if err != nil {
				a.fail(w, r, err)
				return
			}
			b, _ := os.ReadFile(tmp)
			os.Remove(tmp)
			text = string(b)
		}
		writeJSON(w, 200, map[string]any{"items": nonNil(importer.Caddyfile(text))})
		return
	}
	if in.Source != "npm" && in.Source != "1panel" {
		a.fail(w, r, errs.New(400, "params.enum", "name", "source", "allowed", "caddyfile, npm, 1panel"))
		return
	}
	var tmp string
	switch {
	case in.File != "":
		b, err := base64.StdEncoding.DecodeString(in.File)
		if err != nil || !bytes.HasPrefix(b, []byte("SQLite format 3")) {
			a.fail(w, r, errs.New(400, "import.not_sqlite"))
			return
		}
		f, _ := os.CreateTemp("", "berth-import-*.db")
		f.Write(b)
		f.Close()
		tmp = f.Name()
	default:
		paths := importPaths[in.Source]
		if in.Path != "" {
			paths = []string{in.Path}
		}
		var err error
		for _, p := range paths {
			if tmp, err = a.fetchHostFile(r, host, p); err == nil {
				in.Path = p
				break
			}
		}
		if tmp == "" {
			a.fail(w, r, errs.New(404, "import.not_found", "path", firstNonEmpty(in.Path, paths[0])))
			return
		}
	}
	defer os.Remove(tmp)
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(tmp)+"?mode=ro")
	if err != nil {
		a.fail(w, r, err)
		return
	}
	defer db.Close()
	var items []importer.Item
	if in.Source == "npm" {
		items, err = importer.NPM(db)
	} else {
		items, err = importer.OnePanel(db)
	}
	if err != nil {
		a.fail(w, r, errs.New(400, "import.wrong_db", "detail", err.Error()))
		return
	}
	writeJSON(w, 200, map[string]any{"items": nonNil(items), "path": in.Path})
}

func nonNil(items []importer.Item) []importer.Item {
	if items == nil {
		return []importer.Item{}
	}
	return items
}

func init() {
	routeHooks = append(routeHooks, func(a *App, mux *http.ServeMux) {
		mux.HandleFunc("POST /api/v1/hosts/{host}/import/scan", a.superOnly(a.handleImportScan))
	})
}
