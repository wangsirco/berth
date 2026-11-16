package panel

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"

	"github.com/wangsirco/berth/internal/agent/files"
	"github.com/wangsirco/berth/internal/change"
	"github.com/wangsirco/berth/internal/errs"
)

const (
	undoContentMax = 2 << 20 // larger files are written without an undo copy (stated in the plan)
	uploadChunkMax = 8 << 20
)

// sensitivePaths: writing here needs a super admin even inside an allowed root.
var sensitivePaths = []string{"/etc/berth", "/etc/shadow", "/etc/gshadow", "/etc/sudoers", "/etc/sudoers.d", "/root/.ssh", "/etc/ssh"}

func isSensitive(p string) bool {
	p = path.Clean(p)
	for _, s := range sensitivePaths {
		if p == s || strings.HasPrefix(p, s+"/") {
			return true
		}
	}
	return false
}

// fileRoots: the user's allowed roots. Super admins default to "/", others to /srv/berth.
func fileRoots(u *User) []string {
	if len(u.Scope.Paths) > 0 {
		return u.Scope.Paths
	}
	if u.Role == RoleSuper {
		return []string{"/"}
	}
	return []string{"/srv/berth"}
}

func underRoot(root, p string) bool {
	root, p = path.Clean(root), path.Clean(p)
	return root == "/" || p == root || strings.HasPrefix(p, root+"/")
}

// rootFor picks the allowed root that contains every path, or fails.
func rootFor(u *User, paths ...string) (string, error) {
	for _, r := range fileRoots(u) {
		all := true
		for _, p := range paths {
			if !path.IsAbs(p) || !underRoot(r, p) {
				all = false
				break
			}
		}
		if all {
			return r, nil
		}
	}
	p := ""
	if len(paths) > 0 {
		p = paths[0]
	}
	return "", errs.New(403, "files.outside_root", "path", p)
}

// fileTargets checks write access to paths for the change's actor.
func (a *App) fileTargets(env change.Env, write bool, paths ...string) (string, error) {
	u := a.actorUser(env)
	if u == nil {
		return "", errs.New(403, "auth.forbidden")
	}
	root, err := rootFor(u, paths...)
	if err != nil {
		return "", err
	}
	if write && u.Role != RoleSuper {
		for _, p := range paths {
			if isSensitive(p) {
				return "", errs.New(403, "files.sensitive", "path", p)
			}
		}
	}
	return root, nil
}

// actorUser is the signed-in person behind the request running this change (for a token,
// its owner). The pipeline passes the request context to Plan and Apply.
func (a *App) actorUser(env change.Env) *User {
	u, _ := env.Ctx.Value(userKey).(*User)
	return u
}

func fileObj(host, p string) change.Object {
	return change.Object{Type: "file", ID: p, Name: path.Base(p), Host: host}
}

type fileParams struct {
	Path          string             `json:"path"`
	Paths         []string           `json:"paths"`
	To            string             `json:"to"`
	Dest          string             `json:"dest"`
	Content       string             `json:"content"` // text
	ContentB64    string             `json:"content_b64"`
	ContentSealed string             `json:"content_sealed"`
	Mode          string             `json:"mode"`
	Owner         string             `json:"owner"`
	Group         string             `json:"group"`
	Recursive     bool               `json:"recursive"`
	ExpectMtime   int64              `json:"expect_mtime"`
	ID            string             `json:"id"`
	Prev          []files.ModeChange `json:"prev"`
}

func parseFile(raw json.RawMessage) fileParams {
	var p fileParams
	_ = json.Unmarshal(raw, &p)
	return p
}

func (a *App) fileContent(p fileParams) ([]byte, error) {
	switch {
	case p.ContentSealed != "":
		b, err := base64.StdEncoding.DecodeString(p.ContentSealed)
		if err != nil {
			return nil, err
		}
		return a.open(b)
	case p.ContentB64 != "":
		return base64.StdEncoding.DecodeString(p.ContentB64)
	}
	return []byte(p.Content), nil
}

func (a *App) fileCall(env change.Env, op string, body map[string]any, out any) error {
	return a.call(env.Ctx, env.Host, "POST", "/v1/files/"+op, body, out)
}

func fileKind(name, risk, cli string, required []string, plan func(env change.Env, a *App, root string, p fileParams) (change.Plan, error), apply func(env change.Env, a *App, root string, p fileParams) (any, *change.UndoRecipe, error), paths func(p fileParams) []string) change.Kind {
	props := map[string]change.Prop{}
	for _, k := range []string{"path", "to", "dest", "content", "content_b64", "content_sealed", "mode", "owner", "group", "id"} {
		props[k] = change.Prop{Type: "string"}
	}
	props["paths"], props["prev"] = change.Prop{Type: "array"}, change.Prop{Type: "array"}
	props["recursive"], props["expect_mtime"] = change.Prop{Type: "boolean"}, change.Prop{Type: "integer"}
	return change.Kind{
		Name: name, Perm: "files.write", Scope: "files:write", Title: "kind." + name, CLI: cli,
		Params: change.ParamSchema{Props: props, Required: required}, Redact: []string{"content", "content_b64", "content_sealed"},
		Heavy: name == "file.archive" || name == "file.extract", MemMB: map[bool]int{true: 128}[name == "file.archive" || name == "file.extract"],
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			p := parseFile(raw)
			a := svc(env)
			root, err := a.fileTargets(env, true, paths(p)...)
			if err != nil {
				return change.Plan{}, err
			}
			pl, err := plan(env, a, root, p)
			if pl.Risk == "" {
				pl.Risk = change.Risk(risk)
			}
			return pl, err
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			p := parseFile(raw)
			a := svc(env)
			root, err := a.fileTargets(env, true, paths(p)...)
			if err != nil {
				return nil, nil, err
			}
			return apply(env, a, root, p)
		},
	}
}

func undo(kind string, v any) *change.UndoRecipe {
	b, _ := json.Marshal(v)
	return &change.UndoRecipe{Kind: kind, Params: b}
}

func one(p fileParams) []string  { return []string{p.Path} }
func many(p fileParams) []string { return p.Paths }

func init() {
	change.Register(fileKind("file.write", "medium", "berth file write {{.path}}", []string{"path"},
		func(env change.Env, a *App, root string, p fileParams) (change.Plan, error) {
			content, err := a.fileContent(p)
			if err != nil {
				return change.Plan{}, err
			}
			var before files.ReadResult
			exists := a.fileCall(env, "read", map[string]any{"root": root, "path": p.Path, "max": undoContentMax}, &before) == nil
			pl := change.Plan{Summary: "plan.file.write", SummaryP: map[string]any{"path": p.Path, "size": humanBytes(int64(len(content)))},
				Affected: []change.Object{fileObj(env.Host, p.Path)}, Undoable: true}
			if !exists {
				pl.Summary = "plan.file.create"
			} else if before.Truncated {
				pl.Summary, pl.Undoable = "plan.file.write_large", false
			}
			if !before.Binary && !isBinaryText(content) && len(content) <= undoContentMax {
				pl.Files = []change.FileDiff{{Path: p.Path, Before: string(before.Content), After: string(content)}}
			}
			if isSensitive(p.Path) {
				pl.Risk = change.RiskHigh
			}
			return pl, nil
		},
		func(env change.Env, a *App, root string, p fileParams) (any, *change.UndoRecipe, error) {
			content, err := a.fileContent(p)
			if err != nil {
				return nil, nil, err
			}
			var before files.ReadResult
			exists := a.fileCall(env, "read", map[string]any{"root": root, "path": p.Path, "max": undoContentMax}, &before) == nil
			var st files.Entry
			if err := a.fileCall(env, "write", map[string]any{"root": root, "path": p.Path, "content": content, "mode": p.Mode, "expect_mtime": p.ExpectMtime}, &st); err != nil {
				return nil, nil, err
			}
			switch {
			case !exists:
				return st, undo("file.trash", fileParams{Paths: []string{p.Path}}), nil
			case before.Truncated:
				return st, nil, nil
			}
			sealed, err := a.seal(before.Content) // the old content may hold secrets
			if err != nil {
				return nil, nil, err
			}
			return st, undo("file.write", fileParams{Path: p.Path, ContentSealed: base64.StdEncoding.EncodeToString(sealed)}), nil
		}, one))

	change.Register(fileKind("file.mkdir", "low", "berth file mkdir {{.path}}", []string{"path"},
		func(env change.Env, a *App, root string, p fileParams) (change.Plan, error) {
			return change.Plan{Summary: "plan.file.mkdir", SummaryP: map[string]any{"path": p.Path}, Affected: []change.Object{fileObj(env.Host, p.Path)}, Undoable: true}, nil
		},
		func(env change.Env, a *App, root string, p fileParams) (any, *change.UndoRecipe, error) {
			if err := a.fileCall(env, "mkdir", map[string]any{"root": root, "path": p.Path}, nil); err != nil {
				return nil, nil, err
			}
			return nil, undo("file.trash", fileParams{Paths: []string{p.Path}}), nil
		}, one))

	change.Register(fileKind("file.rename", "medium", "berth file mv {{.path}} {{.to}}", []string{"path", "to"},
		func(env change.Env, a *App, root string, p fileParams) (change.Plan, error) {
			return change.Plan{Summary: "plan.file.rename", SummaryP: map[string]any{"path": p.Path, "to": p.To},
				Affected: []change.Object{fileObj(env.Host, p.Path)}, Undoable: true, Commands: []string{"mv " + p.Path + " " + p.To}}, nil
		},
		func(env change.Env, a *App, root string, p fileParams) (any, *change.UndoRecipe, error) {
			if err := a.fileCall(env, "rename", map[string]any{"root": root, "path": p.Path, "to": p.To}, nil); err != nil {
				return nil, nil, err
			}
			return nil, undo("file.rename", fileParams{Path: p.To, To: p.Path}), nil
		}, func(p fileParams) []string { return []string{p.Path, p.To} }))

	change.Register(fileKind("file.trash", "medium", "berth file rm {{.paths}}", []string{"paths"},
		func(env change.Env, a *App, root string, p fileParams) (change.Plan, error) {
			objs := make([]change.Object, 0, len(p.Paths))
			for _, x := range p.Paths {
				objs = append(objs, fileObj(env.Host, x))
			}
			return change.Plan{Summary: "plan.file.trash", SummaryP: map[string]any{"count": len(p.Paths)}, Affected: objs, Undoable: true}, nil
		},
		func(env change.Env, a *App, root string, p fileParams) (any, *change.UndoRecipe, error) {
			var r struct{ ID string }
			if err := a.fileCall(env, "trash", map[string]any{"root": root, "paths": p.Paths}, &r); err != nil {
				return nil, nil, err
			}
			return r, undo("file.restore", fileParams{ID: r.ID, Paths: p.Paths}), nil
		}, many))

	change.Register(fileKind("file.restore", "low", "berth file restore {{.id}}", []string{"id"},
		func(env change.Env, a *App, root string, p fileParams) (change.Plan, error) {
			return change.Plan{Summary: "plan.file.restore", SummaryP: map[string]any{"count": len(p.Paths)}, Undoable: true}, nil
		},
		func(env change.Env, a *App, root string, p fileParams) (any, *change.UndoRecipe, error) {
			if err := a.fileCall(env, "trash/restore", map[string]any{"id": p.ID}, nil); err != nil {
				return nil, nil, err
			}
			return nil, undo("file.trash", fileParams{Paths: p.Paths}), nil
		}, many))

	change.Register(fileKind("file.purge", "destructive", "berth file purge {{.id}}", []string{"id"},
		func(env change.Env, a *App, root string, p fileParams) (change.Plan, error) {
			return change.Plan{Summary: "plan.file.purge", SummaryP: map[string]any{"count": len(p.Paths)}, Risk: change.RiskDestructive}, nil
		},
		func(env change.Env, a *App, root string, p fileParams) (any, *change.UndoRecipe, error) {
			return nil, nil, a.fileCall(env, "trash/purge", map[string]any{"id": p.ID}, nil)
		}, many))

	change.Register(fileKind("file.chmod", "medium", "berth file chmod {{.path}}", []string{"path"},
		func(env change.Env, a *App, root string, p fileParams) (change.Plan, error) {
			return change.Plan{Summary: "plan.file.chmod", SummaryP: map[string]any{"path": p.Path, "mode": p.Mode, "owner": p.Owner, "group": p.Group},
				Affected: []change.Object{fileObj(env.Host, p.Path)}, Undoable: true}, nil
		},
		func(env change.Env, a *App, root string, p fileParams) (any, *change.UndoRecipe, error) {
			var r struct {
				Prev      []files.ModeChange
				Truncated bool
			}
			if err := a.fileCall(env, "chmod", map[string]any{"root": root, "path": p.Path, "mode": p.Mode, "owner": p.Owner, "group": p.Group, "recursive": p.Recursive}, &r); err != nil {
				return nil, nil, err
			}
			if r.Truncated {
				env.Log("more than 5000 files changed; this change cannot be undone")
				return nil, nil, nil
			}
			return nil, undo("file.restore_modes", fileParams{Path: p.Path, Prev: r.Prev}), nil
		}, one))

	// internal: undo of chmod (no CLI, not offered as a tool)
	change.Register(fileKind("file.restore_modes", "medium", "", []string{"path"},
		func(env change.Env, a *App, root string, p fileParams) (change.Plan, error) {
			return change.Plan{Summary: "plan.file.restore_modes", SummaryP: map[string]any{"path": p.Path}, Affected: []change.Object{fileObj(env.Host, p.Path)}}, nil
		},
		func(env change.Env, a *App, root string, p fileParams) (any, *change.UndoRecipe, error) {
			return nil, nil, a.fileCall(env, "restore-modes", map[string]any{"root": root, "prev": p.Prev}, nil)
		}, one))

	streamFile := func(name, op string) func(env change.Env, a *App, root string, p fileParams) (any, *change.UndoRecipe, error) {
		return func(env change.Env, a *App, root string, p fileParams) (any, *change.UndoRecipe, error) {
			env.Log("::step 0 %s", op)
			if err := agentOp(env, "/v1/files/"+op, map[string]any{"root": root, "path": p.Path, "paths": p.Paths, "dest": p.Dest}); err != nil {
				return nil, nil, err
			}
			if op == "archive" {
				return nil, undo("file.trash", fileParams{Paths: []string{p.Dest}}), nil
			}
			return nil, nil, nil
		}
	}
	change.Register(fileKind("file.archive", "low", "berth file archive {{.dest}}", []string{"paths", "dest"},
		func(env change.Env, a *App, root string, p fileParams) (change.Plan, error) {
			return change.Plan{Summary: "plan.file.archive", SummaryP: map[string]any{"count": len(p.Paths), "dest": p.Dest}, Affected: []change.Object{fileObj(env.Host, p.Dest)}, Undoable: true}, nil
		}, streamFile("file.archive", "archive"), func(p fileParams) []string { return append(append([]string{}, p.Paths...), p.Dest) }))
	change.Register(fileKind("file.extract", "medium", "berth file extract {{.path}} {{.dest}}", []string{"path", "dest"},
		func(env change.Env, a *App, root string, p fileParams) (change.Plan, error) {
			return change.Plan{Summary: "plan.file.extract", SummaryP: map[string]any{"path": p.Path, "dest": p.Dest}, Affected: []change.Object{fileObj(env.Host, p.Dest)}}, nil
		}, streamFile("file.extract", "extract"), func(p fileParams) []string { return []string{p.Path, p.Dest} }))

	change.Register(fileKind("file.upload", "low", "", []string{"id", "path"},
		func(env change.Env, a *App, root string, p fileParams) (change.Plan, error) {
			return change.Plan{Summary: "plan.file.upload", SummaryP: map[string]any{"path": p.Path}, Affected: []change.Object{fileObj(env.Host, p.Path)}, Undoable: true}, nil
		},
		func(env change.Env, a *App, root string, p fileParams) (any, *change.UndoRecipe, error) {
			if err := a.fileCall(env, "upload/finish", map[string]any{"id": p.ID}, nil); err != nil {
				return nil, nil, err
			}
			return nil, undo("file.trash", fileParams{Paths: []string{p.Path}}), nil
		}, one))
}

func isBinaryText(b []byte) bool {
	for _, c := range b[:min(len(b), 8000)] {
		if c == 0 {
			return true
		}
	}
	return false
}

// ---- routes ----

func init() {
	routeHooks = append(routeHooks, func(a *App, mux *http.ServeMux) {
		mux.HandleFunc("GET /api/v1/hosts/{host}/files/roots", a.authed("files.read", a.handleFileRoots))
		for _, op := range []string{"list", "stat", "read", "search"} {
			mux.HandleFunc("GET /api/v1/hosts/{host}/files/"+op, a.authed("files.read", a.fileRead(op)))
		}
		mux.HandleFunc("GET /api/v1/hosts/{host}/files/download", a.authed("files.read", a.handleDownload))
		mux.HandleFunc("GET /api/v1/hosts/{host}/files/trash", a.authed("files.write", a.handleTrashList))
		mux.HandleFunc("POST /api/v1/hosts/{host}/files/upload", a.authed("files.write", a.handleUploadBegin))
		mux.HandleFunc("PUT /api/v1/hosts/{host}/files/upload/{id}", a.authed("files.write", a.handleUploadChunk))
		mux.HandleFunc("GET /api/v1/hosts/{host}/files/upload/{id}", a.authed("files.write", a.handleUploadStatus))
		mux.HandleFunc("POST /api/v1/hosts/{host}/files/upload/{id}/finish", a.authed("files.write", a.handleUploadFinish))
	})
}

func (a *App) handleFileRoots(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{"roots": fileRoots(currentUser(r)), "sensitive": sensitivePaths})
}

func (a *App) fileRead(op string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		p := q.Get("path")
		root, err := rootFor(currentUser(r), p)
		if err != nil {
			a.fail(w, r, err)
			return
		}
		max, _ := strconv.ParseInt(q.Get("max"), 10, 64)
		var out json.RawMessage
		body := map[string]any{"root": root, "path": p, "hidden": q.Get("hidden") == "1", "max": max, "name": q.Get("name"), "query": q.Get("q")}
		if err := a.call(r.Context(), r.PathValue("host"), "POST", "/v1/files/"+op, body, &out); err != nil {
			a.fail(w, r, err)
			return
		}
		writeJSON(w, 200, map[string]any{"root": root, "result": out})
	}
}

// GET .../files/download?path=a (one file) or ?path=a&path=b (zip)
func (a *App) handleDownload(w http.ResponseWriter, r *http.Request) {
	paths := r.URL.Query()["path"]
	if len(paths) == 0 {
		a.fail(w, r, errs.New(400, "params.required", "name", "path"))
		return
	}
	root, err := rootFor(currentUser(r), paths...)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	ag, err := a.agent(r.PathValue("host"))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	op, name := "download", path.Base(paths[0])
	body := map[string]any{"root": root, "path": paths[0]}
	if len(paths) > 1 || r.URL.Query().Get("zip") == "1" {
		op, name = "zip", "files.zip"
		body = map[string]any{"root": root, "paths": paths}
	}
	rc, err := ag.Stream(r.Context(), "POST", "/v1/files/"+op, body)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	defer rc.Close()
	ct, disp := "application/octet-stream", "attachment"
	if r.URL.Query().Get("inline") == "1" && op == "download" {
		// only types a browser cannot run as a page: never HTML or SVG on the panel's origin
		if m := mime.TypeByExtension(strings.ToLower(path.Ext(name))); inlineSafe(m) {
			ct, disp = m, "inline"
		}
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Content-Disposition", disp+"; filename*=UTF-8''"+url.PathEscape(name))
	_, _ = io.Copy(w, rc)
}

func inlineSafe(m string) bool {
	m, _, _ = strings.Cut(m, ";")
	switch {
	case m == "image/png", m == "image/jpeg", m == "image/gif", m == "image/webp", m == "image/avif", m == "application/pdf":
		return true
	case strings.HasPrefix(m, "video/"), strings.HasPrefix(m, "audio/"):
		return true
	}
	return false
}

func (a *App) handleTrashList(w http.ResponseWriter, r *http.Request) {
	var list []files.TrashItem
	if err := a.call(r.Context(), r.PathValue("host"), "GET", "/v1/files/trash", nil, &list); err != nil {
		a.fail(w, r, err)
		return
	}
	u := currentUser(r)
	out := []files.TrashItem{}
	for _, t := range list {
		if _, err := rootFor(u, t.Paths...); err == nil {
			out = append(out, t)
		}
	}
	writeJSON(w, 200, map[string]any{"items": out})
}

func (a *App) uploadRoot(r *http.Request, p string) (string, error) {
	u := currentUser(r)
	root, err := rootFor(u, p)
	if err != nil {
		return "", err
	}
	if isSensitive(p) && u.Role != RoleSuper {
		return "", errs.New(403, "files.sensitive", "path", p)
	}
	return root, nil
}

func (a *App) handleUploadBegin(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Path string `json:"path"`
		Size int64  `json:"size"`
	}
	if err := readJSON(r, &in); err != nil {
		a.fail(w, r, err)
		return
	}
	if err := a.tokenScope(r, "files:write"); err != nil {
		a.fail(w, r, err)
		return
	}
	root, err := a.uploadRoot(r, in.Path)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	var out struct {
		ID string `json:"id"`
	}
	if err := a.call(r.Context(), r.PathValue("host"), "POST", "/v1/files/upload/begin", map[string]any{"root": root, "path": in.Path, "size": in.Size}, &out); err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"id": out.ID, "chunk": uploadChunkMax})
}

// PUT .../upload/{id}?offset=N — raw body, at most 8 MB.
func (a *App) handleUploadChunk(w http.ResponseWriter, r *http.Request) {
	off, _ := strconv.ParseInt(r.URL.Query().Get("offset"), 10, 64)
	b, err := io.ReadAll(io.LimitReader(r.Body, uploadChunkMax+1))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if len(b) > uploadChunkMax {
		a.fail(w, r, errs.New(413, "files.chunk_too_big", "max", uploadChunkMax))
		return
	}
	var out json.RawMessage
	if err := a.call(r.Context(), r.PathValue("host"), "POST", "/v1/files/upload/chunk", map[string]any{"id": r.PathValue("id"), "offset": off, "content": b}, &out); err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, 200, out)
}

func (a *App) handleUploadStatus(w http.ResponseWriter, r *http.Request) {
	var out json.RawMessage
	if err := a.call(r.Context(), r.PathValue("host"), "POST", "/v1/files/upload/status", map[string]any{"id": r.PathValue("id")}, &out); err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, 200, out)
}

func (a *App) handleUploadFinish(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Path string `json:"path"`
	}
	if err := readJSON(r, &in); err != nil {
		a.fail(w, r, err)
		return
	}
	raw, _ := json.Marshal(map[string]string{"id": r.PathValue("id"), "path": in.Path})
	a.runChange(w, r, r.PathValue("host"), "file.upload", raw)
}
