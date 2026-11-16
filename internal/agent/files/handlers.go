package files

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/wangsirco/berth/internal/agent"
	"github.com/wangsirco/berth/internal/errs"
)

// Req is the common body of file requests. Root is chosen by the panel per user.
type Req struct {
	Root        string       `json:"root"`
	Path        string       `json:"path"`
	Paths       []string     `json:"paths"`
	To          string       `json:"to"`
	Dest        string       `json:"dest"`
	Content     []byte       `json:"content"`
	Mode        string       `json:"mode"`
	Owner       string       `json:"owner"`
	Group       string       `json:"group"`
	Recursive   bool         `json:"recursive"`
	ExpectMtime int64        `json:"expect_mtime"`
	Size        int64        `json:"size"`
	Offset      int64        `json:"offset"`
	Max         int64        `json:"max"`
	ID          string       `json:"id"`
	Prev        []ModeChange `json:"prev"`
	Hidden      bool         `json:"hidden"`
	Name        string       `json:"name"`
	Query       string       `json:"query"`
}

func Register(s *agent.Server, m *Manager) {
	go func() {
		for range time.Tick(time.Hour) {
			m.ExpireUploads()
		}
	}()
	h := func(pattern string, fn func(r *http.Request, in Req) (any, error)) {
		s.Handle(pattern, func(w http.ResponseWriter, r *http.Request, body []byte) {
			var in Req
			if len(body) > 0 && json.Unmarshal(body, &in) != nil {
				agent.WriteErr(w, errs.New(400, "params.invalid_json"))
				return
			}
			out, err := fn(r, in)
			if err != nil {
				agent.WriteErr(w, err)
				return
			}
			agent.WriteJSON(w, 200, out)
		})
	}
	ok := map[string]bool{"ok": true}
	s.Handle("POST /v1/files/fetch", func(w http.ResponseWriter, r *http.Request, body []byte) {
		var in struct {
			Root, Path, URL, Name string
		}
		json.Unmarshal(body, &in)
		st := agent.NewStream(w)
		dest, err := Fetch(r.Context(), in.Root, in.Path, in.URL, in.Name, func(l string) { st.Send(map[string]string{"l": l}) })
		if err == nil {
			b, _ := json.Marshal(map[string]string{"path": dest})
			st.Send(map[string]string{"l": "::result " + string(b)})
		}
		st.End(err)
	})
	h("POST /v1/files/list", func(r *http.Request, in Req) (any, error) { return List(in.Root, in.Path, in.Hidden) })
	h("POST /v1/files/stat", func(r *http.Request, in Req) (any, error) { return Stat(in.Root, in.Path) })
	h("POST /v1/files/read", func(r *http.Request, in Req) (any, error) { return Read(in.Root, in.Path, in.Max) })
	h("POST /v1/files/write", func(r *http.Request, in Req) (any, error) {
		var mode os.FileMode
		if in.Mode != "" {
			n, err := strconv.ParseUint(in.Mode, 8, 32)
			if err != nil {
				return nil, errs.New(400, "files.bad_mode", "mode", in.Mode)
			}
			mode = os.FileMode(n)
		}
		if err := Write(in.Root, in.Path, in.Content, mode, in.ExpectMtime); err != nil {
			return nil, err
		}
		return Stat(in.Root, in.Path)
	})
	h("POST /v1/files/mkdir", func(r *http.Request, in Req) (any, error) { return ok, Mkdir(in.Root, in.Path) })
	h("POST /v1/files/rename", func(r *http.Request, in Req) (any, error) { return ok, Rename(in.Root, in.Path, in.To) })
	h("POST /v1/files/chmod", func(r *http.Request, in Req) (any, error) {
		prev, trunc, err := ChmodChown(in.Root, in.Path, in.Mode, in.Owner, in.Group, in.Recursive, 5000)
		return map[string]any{"prev": prev, "truncated": trunc}, err
	})
	h("POST /v1/files/restore-modes", func(r *http.Request, in Req) (any, error) { return ok, RestoreModes(in.Root, in.Prev) })
	h("POST /v1/files/trash", func(r *http.Request, in Req) (any, error) {
		id, err := m.Trash(in.Root, in.Paths)
		return map[string]string{"id": id}, err
	})
	h("POST /v1/files/trash/restore", func(r *http.Request, in Req) (any, error) { return ok, m.Restore(in.ID) })
	h("POST /v1/files/trash/purge", func(r *http.Request, in Req) (any, error) { return ok, m.Purge(in.ID) })
	h("GET /v1/files/trash", func(r *http.Request, in Req) (any, error) { return m.TrashList(), nil })
	h("POST /v1/files/upload/begin", func(r *http.Request, in Req) (any, error) {
		id, err := m.UploadBegin(in.Root, in.Path, in.Size)
		return map[string]string{"id": id}, err
	})
	h("POST /v1/files/upload/chunk", func(r *http.Request, in Req) (any, error) {
		if err := m.UploadChunk(in.ID, in.Offset, in.Content); err != nil {
			return nil, err
		}
		n, err := m.UploadStatus(in.ID)
		return map[string]int64{"received": n}, err
	})
	h("POST /v1/files/upload/status", func(r *http.Request, in Req) (any, error) {
		n, err := m.UploadStatus(in.ID)
		return map[string]int64{"received": n}, err
	})
	h("POST /v1/files/upload/finish", func(r *http.Request, in Req) (any, error) { return ok, m.UploadFinish(in.ID) })
	h("POST /v1/files/search", func(r *http.Request, in Req) (any, error) {
		return Search(r.Context(), in.Root, in.Path, in.Name, in.Query, 500)
	})

	// raw download of one file
	s.Handle("POST /v1/files/download", func(w http.ResponseWriter, r *http.Request, body []byte) {
		var in Req
		_ = json.Unmarshal(body, &in)
		p, err := resolve(in.Root, in.Path)
		if err != nil {
			agent.WriteErr(w, err)
			return
		}
		f, err := os.Open(p)
		if err != nil {
			agent.WriteErr(w, fsErr(err, p))
			return
		}
		defer f.Close()
		if fi, err := f.Stat(); err == nil {
			if fi.IsDir() {
				agent.WriteErr(w, errs.New(400, "files.is_dir", "path", p))
				return
			}
			w.Header().Set("Content-Length", strconv.FormatInt(fi.Size(), 10))
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("X-File-Name", filepath.Base(p))
		_, _ = io.Copy(w, f)
	})
	s.Handle("POST /v1/files/zip", func(w http.ResponseWriter, r *http.Request, body []byte) {
		var in Req
		_ = json.Unmarshal(body, &in)
		for _, p := range in.Paths {
			if _, err := resolve(in.Root, p); err != nil {
				agent.WriteErr(w, err)
				return
			}
		}
		w.Header().Set("Content-Type", "application/zip")
		_ = WriteZip(in.Root, in.Paths, w) // errors after the first byte can only cut the stream
	})
	s.Handle("POST /v1/files/archive", func(w http.ResponseWriter, r *http.Request, body []byte) {
		var in Req
		_ = json.Unmarshal(body, &in)
		st := agent.NewStream(w)
		st.End(Archive(r.Context(), in.Root, in.Paths, in.Dest, func(l string) { st.Send(map[string]string{"l": l}) }))
	})
	s.Handle("POST /v1/files/extract", func(w http.ResponseWriter, r *http.Request, body []byte) {
		var in Req
		_ = json.Unmarshal(body, &in)
		st := agent.NewStream(w)
		st.End(Extract(r.Context(), in.Root, in.Path, in.Dest, func(l string) { st.Send(map[string]string{"l": l}) }))
	})
}
