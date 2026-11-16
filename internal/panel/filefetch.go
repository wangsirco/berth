package panel

// Remote download into a directory (宝塔的“远程下载”).

import (
	"encoding/json"
	"strings"

	"github.com/wangsirco/berth/internal/change"
	"github.com/wangsirco/berth/internal/errs"
)

func init() {
	change.Register(change.Kind{
		Name: "file.fetch", Perm: "files.write", Scope: "files:write", Title: "kind.file.fetch", CLI: "berth file fetch {{.url}} {{.path}}", Heavy: true, MemMB: 32,
		Params: change.ParamSchema{Props: map[string]change.Prop{"url": {Type: "string"}, "path": {Type: "string", Desc: "directory to save into"}, "name": {Type: "string", Desc: "file name (default: from the URL)"}},
			Required: []string{"url", "path"}},
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			var p struct{ URL, Path, Name string }
			json.Unmarshal(raw, &p)
			if !strings.HasPrefix(p.URL, "http://") && !strings.HasPrefix(p.URL, "https://") {
				return change.Plan{}, errs.New(400, "files.bad_url")
			}
			if _, err := svc(env).fileTargets(env, true, p.Path); err != nil {
				return change.Plan{}, err
			}
			return change.Plan{Summary: "plan.file.fetch", SummaryP: map[string]any{"url": truncate(p.URL, 120), "path": p.Path}, Risk: change.RiskLow, Undoable: true,
				Affected: []change.Object{fileObj(env.Host, p.Path)}}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			a := svc(env)
			var p struct{ URL, Path, Name string }
			json.Unmarshal(raw, &p)
			root, err := a.fileTargets(env, true, p.Path)
			if err != nil {
				return nil, nil, err
			}
			var out struct {
				Path string `json:"path"`
			}
			if err := a.streamAgent(env, env.Host, "/v1/files/fetch", map[string]string{"root": root, "path": p.Path, "url": p.URL, "name": p.Name}, &out); err != nil {
				return nil, nil, err
			}
			return out, undo("file.trash", fileParams{Paths: []string{out.Path}}), nil
		},
	})
}
