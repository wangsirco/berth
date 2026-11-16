package panel

// Restoring online (备份与还原 → 还原): files uploaded from the browser in resumable pieces
// (a migration pack or a panel snapshot), putting a panel snapshot back without a shell, and
// reading another server's backup repository to restore from it.

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/wangsirco/berth/internal/agent/backup"
	"github.com/wangsirco/berth/internal/change"
	"github.com/wangsirco/berth/internal/errs"
)

// ---- uploads ----

type upload struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Size     int64  `json:"size"`
	Received int64  `json:"received"`
	Done     bool   `json:"done"`
	Path     string `json:"path"`
	Created  int64  `json:"created"`
	By       int64  `json:"by"`
}

var (
	uploadIDRe  = regexp.MustCompile(`^[0-9a-f]{24}$`)
	uploadExts  = []string{".berthpack", ".berthsnap"}
	uploadChunk = int64(64 << 20)
)

func (a *App) uploadDir() string { return filepath.Join(a.cfg.DataDir, "uploads") }

func (a *App) uploadInfo(id string) (upload, error) {
	var u upload
	if !uploadIDRe.MatchString(id) {
		return u, errs.New(404, "upload.not_found")
	}
	b, err := os.ReadFile(filepath.Join(a.uploadDir(), id+".json"))
	if err != nil {
		return u, errs.New(404, "upload.not_found")
	}
	json.Unmarshal(b, &u)
	if fi, err := os.Stat(u.Path); err == nil {
		u.Received = fi.Size()
	}
	u.Done = u.Received == u.Size
	return u, nil
}

// pruneUploads: pieces left two days ago are not coming back.
func (a *App) pruneUploads() {
	ents, _ := os.ReadDir(a.uploadDir())
	for _, e := range ents {
		if fi, err := e.Info(); err == nil && time.Since(fi.ModTime()) > 48*time.Hour {
			os.Remove(filepath.Join(a.uploadDir(), e.Name()))
		}
	}
}

func (a *App) handleUploadStart(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name string `json:"name"`
		Size int64  `json:"size"`
	}
	if err := readJSON(r, &in); err != nil {
		a.fail(w, r, err)
		return
	}
	ext := strings.ToLower(filepath.Ext(in.Name))
	if !contains(uploadExts, ext) || in.Size <= 0 || in.Size > 2<<40 {
		a.fail(w, r, errs.New(400, "upload.bad_file", "allowed", strings.Join(uploadExts, " ")))
		return
	}
	a.pruneUploads()
	if err := os.MkdirAll(a.uploadDir(), 0o750); err != nil {
		a.fail(w, r, err)
		return
	}
	b := make([]byte, 12)
	rand.Read(b)
	id := hex.EncodeToString(b)
	u := upload{ID: id, Name: filepath.Base(in.Name), Size: in.Size, Path: filepath.Join(a.uploadDir(), id+ext), Created: time.Now().Unix(), By: currentUser(r).ID}
	if err := os.WriteFile(u.Path, nil, 0o640); err != nil {
		a.fail(w, r, err)
		return
	}
	meta, _ := json.Marshal(u)
	if err := os.WriteFile(filepath.Join(a.uploadDir(), id+".json"), meta, 0o640); err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, 200, u)
}

// PUT /api/v1/uploads/{id}?offset=N: the next piece; a piece that repeats what arrived is
// accepted from where the file stands (after a dropped connection).
func (a *App) handleUploadPart(w http.ResponseWriter, r *http.Request) {
	u, err := a.uploadInfo(r.PathValue("id"))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	off, _ := strconv.ParseInt(r.URL.Query().Get("offset"), 10, 64)
	if off > u.Received || off < 0 {
		a.fail(w, r, errs.New(409, "upload.offset", "received", u.Received))
		return
	}
	f, err := os.OpenFile(u.Path, os.O_WRONLY, 0)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	defer f.Close()
	if err := f.Truncate(off); err != nil {
		a.fail(w, r, err)
		return
	}
	if _, err := f.Seek(off, io.SeekStart); err != nil {
		a.fail(w, r, err)
		return
	}
	n, err := io.Copy(f, io.LimitReader(r.Body, min(uploadChunk, u.Size-off)))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	u.Received, u.Done = off+n, off+n == u.Size
	writeJSON(w, 200, u)
}

// uploadedFile: a finished upload of the given kind, for a change to use.
func (a *App) uploadedFile(id, ext string) (upload, error) {
	u, err := a.uploadInfo(id)
	if err != nil {
		return u, err
	}
	if !u.Done || !strings.HasSuffix(u.Path, ext) {
		return u, errs.New(409, "upload.not_finished")
	}
	return u, nil
}

// ---- panel snapshot, restored online ----

type panelRestoreParams struct {
	Upload   string `json:"upload"`
	Password string `json:"password"`
}

func (a *App) panelRestoreCall(env change.Env, p panelRestoreParams, check bool) (map[string]any, error) {
	u, err := a.uploadedFile(p.Upload, ".berthsnap")
	if err != nil {
		return nil, err
	}
	var out map[string]any
	err = a.call(env.Ctx, "local", "POST", "/v1/panel/restore", map[string]any{"file": u.Path, "password": p.Password, "data_dir": a.cfg.DataDir,
		"etc_dir": a.cfg.EtcDir, "pid": os.Getpid(), "check": check}, &out)
	return out, err
}

// ---- another server's backup repository ----

type externalRepo struct {
	Target targetCfg `json:"target"`
}

func (e externalRepo) repo() (backup.Repo, error) {
	if e.Target.Password == "" {
		return backup.Repo{}, errs.New(400, "backup.need_password")
	}
	return e.Target.repo(0)
}

type externalRestore struct {
	externalRepo
	Snapshot string   `json:"snapshot"`
	To       string   `json:"to"`
	Include  []string `json:"include"`
}

func init() {
	routeHooks = append(routeHooks, func(a *App, mux *http.ServeMux) {
		mux.HandleFunc("POST /api/v1/uploads", a.superOnly(a.handleUploadStart))
		mux.HandleFunc("PUT /api/v1/uploads/{id}", a.superOnly(a.handleUploadPart))
		mux.HandleFunc("GET /api/v1/uploads/{id}", a.superOnly(func(w http.ResponseWriter, r *http.Request) {
			u, err := a.uploadInfo(r.PathValue("id"))
			if err != nil {
				a.fail(w, r, err)
				return
			}
			writeJSON(w, 200, u)
		}))
		mux.HandleFunc("DELETE /api/v1/uploads/{id}", a.superOnly(func(w http.ResponseWriter, r *http.Request) {
			u, err := a.uploadInfo(r.PathValue("id"))
			if err != nil {
				a.fail(w, r, err)
				return
			}
			os.Remove(u.Path)
			os.Remove(filepath.Join(a.uploadDir(), u.ID+".json"))
			w.WriteHeader(204)
		}))
		// what a snapshot holds, before restoring it (also checks the password)
		mux.HandleFunc("POST /api/v1/panel/restore/check", a.superOnly(func(w http.ResponseWriter, r *http.Request) {
			var p panelRestoreParams
			if err := readJSON(r, &p); err != nil {
				a.fail(w, r, err)
				return
			}
			out, err := a.panelRestoreCall(change.Env{Ctx: r.Context()}, p, true)
			if err != nil {
				a.fail(w, r, err)
				return
			}
			writeJSON(w, 200, out)
		}))
		ext := func(path string, fn func(w http.ResponseWriter, r *http.Request, repo backup.Repo, body map[string]any)) {
			mux.HandleFunc("POST /api/v1/hosts/{host}/backup/external/"+path, a.authed("backup.restore", func(w http.ResponseWriter, r *http.Request) {
				var raw json.RawMessage
				if err := readJSON(r, &raw); err != nil {
					a.fail(w, r, err)
					return
				}
				var e externalRepo
				json.Unmarshal(raw, &e)
				repo, err := e.repo()
				if err != nil {
					a.fail(w, r, err)
					return
				}
				body := map[string]any{}
				json.Unmarshal(raw, &body)
				fn(w, r, repo, body)
			}))
		}
		ext("snapshots", func(w http.ResponseWriter, r *http.Request, repo backup.Repo, _ map[string]any) {
			var out any
			if err := a.call(r.Context(), r.PathValue("host"), "POST", "/v1/backup/snapshots", map[string]any{"repo": repo, "job": ""}, &out); err != nil {
				a.fail(w, r, err)
				return
			}
			writeJSON(w, 200, map[string]any{"items": out})
		})
		ext("ls", func(w http.ResponseWriter, r *http.Request, repo backup.Repo, body map[string]any) {
			var out any
			if err := a.call(r.Context(), r.PathValue("host"), "POST", "/v1/backup/ls", map[string]any{"repo": repo, "snapshot": body["snapshot"], "dir": body["dir"]}, &out); err != nil {
				a.fail(w, r, err)
				return
			}
			writeJSON(w, 200, map[string]any{"items": out})
		})
	})

	change.Register(change.Kind{
		Name: "panel.restore", Perm: "settings.manage", Scope: "settings:write", Title: "kind.panel.restore", Redact: []string{"password"},
		Params: change.ParamSchema{Props: map[string]change.Prop{"upload": {Type: "string", Desc: "id of an uploaded .berthsnap"}, "password": {Type: "string"}},
			Required: []string{"upload", "password"}},
		CLI: "berth restore-panel <file.berthsnap>",
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			var p panelRestoreParams
			json.Unmarshal(raw, &p)
			out, err := svc(env).panelRestoreCall(env, p, true)
			if err != nil {
				return change.Plan{}, err
			}
			detail, _ := out["detail"].(string)
			return change.Plan{Summary: "plan.panel.restore", SummaryP: map[string]any{"detail": detail}, Risk: change.RiskHigh,
				Commands: []string{"berth restore-panel <file>   # in its own systemd unit; the current folders are kept as *.before-restore-<time>"},
				Affected: []change.Object{{Type: "host", ID: "local", Name: "berth"}}}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			var p panelRestoreParams
			json.Unmarshal(raw, &p)
			env.Log("handing the restore to systemd: the panel stops, its folders are swapped, and it starts again")
			out, err := svc(env).panelRestoreCall(env, p, false)
			return out, nil, err
		},
	})

	change.Register(change.Kind{
		Name: "backup.restore_external", Perm: "backup.restore", Scope: "system:write", Title: "kind.backup.restore_external", Redact: []string{"target"},
		Params: change.ParamSchema{Props: map[string]change.Prop{"target": {Type: "object", Desc: "the repository: {kind, path|endpoint+bucket+access+secret|options, password}"},
			"snapshot": {Type: "string"}, "to": {Type: "string", Desc: "folder to restore into, / = where the files were"}, "include": {Type: "array"}},
			Required: []string{"target", "snapshot", "to"}},
		CLI: "restic restore {{.snapshot}} --target {{.to}}", Heavy: true, MemMB: 256,
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			var p externalRestore
			json.Unmarshal(raw, &p)
			if _, err := p.repo(); err != nil {
				return change.Plan{}, err
			}
			to := filepath.Clean(p.To)
			if !filepath.IsAbs(to) || !regexp.MustCompile(`^[0-9a-f]{8,64}$|^latest$`).MatchString(p.Snapshot) {
				return change.Plan{}, errs.New(400, "backup.bad_restore")
			}
			risk, sum := change.RiskMedium, "plan.backup.restore_external"
			if to == "/" {
				risk, sum = change.RiskHigh, "plan.backup.restore_external_original"
			}
			cmd := "restic restore " + p.Snapshot + " --target " + to + " --verify"
			for _, i := range p.Include {
				cmd += " --include " + i
			}
			return change.Plan{Summary: sum, SummaryP: map[string]any{"snapshot": p.Snapshot, "to": to, "where": describeTarget(p.Target)}, Risk: risk,
				Commands: []string{cmd}, Affected: []change.Object{{Type: "host", ID: env.Host, Name: env.Host}}}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			var p externalRestore
			json.Unmarshal(raw, &p)
			repo, err := p.repo()
			if err != nil {
				return nil, nil, err
			}
			var res map[string]any
			err = svc(env).streamAgent(env, env.Host, "/v1/backup/restore", backup.RestoreRequest{Repo: repo, Job: "external", Snapshot: p.Snapshot, Target: filepath.Clean(p.To), Include: p.Include}, &res)
			return res, nil, err
		},
	})
}
