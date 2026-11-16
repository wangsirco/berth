package migrate

import (
	"archive/tar"
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/wangsirco/berth/internal/agent"
	"github.com/wangsirco/berth/internal/agent/system"
	"github.com/wangsirco/berth/internal/errs"
)

func Register(s *agent.Server) {
	s.Handle("POST /v1/migrate/scan", func(w http.ResponseWriter, r *http.Request, body []byte) {
		var in ScanReq
		if json.Unmarshal(body, &in) != nil {
			agent.WriteErr(w, errs.New(400, "params.invalid_json"))
			return
		}
		agent.WriteJSON(w, 200, Scan(r.Context(), in))
	})
	// SFTP accounts with their password hashes and keys: only the panel, only to move them
	s.Handle("GET /v1/migrate/sftp", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		agent.WriteJSON(w, 200, system.ExportSFTP())
	})
	s.Handle("POST /v1/migrate/space", func(w http.ResponseWriter, r *http.Request, body []byte) {
		var in struct{ Paths []string }
		json.Unmarshal(body, &in)
		agent.WriteJSON(w, 200, Space(in.Paths))
	})
	// the item as a tar stream; its last entry, x/status, says whether everything was read
	s.Handle("POST /v1/migrate/export", func(w http.ResponseWriter, r *http.Request, body []byte) {
		var in ExportReq
		if json.Unmarshal(body, &in) != nil {
			agent.WriteErr(w, errs.New(400, "params.invalid_json"))
			return
		}
		w.Header().Set("Content-Type", "application/x-tar")
		w.WriteHeader(200)
		tw := tar.NewWriter(w)
		err := Export(r.Context(), in, tw, func(string) {})
		msg := "ok"
		if err != nil {
			msg = err.Error()
			if e, ok := err.(*errs.E); ok {
				b, _ := json.Marshal(e)
				msg = string(b)
			}
		}
		// a failure in the middle of a file leaves the writer unusable: the stream is cut,
		// which the reader also takes as a failure
		if tw.WriteHeader(&tar.Header{Name: "x/status", Mode: 0o600, Size: int64(len(msg)), Typeflag: tar.TypeReg}) == nil {
			tw.Write([]byte(msg))
			tw.Close()
		}
	})
	// start a restored compose project (it has no containers yet, so the normal lookup can't find it)
	s.Handle("POST /v1/migrate/compose-up", func(w http.ResponseWriter, r *http.Request, body []byte) {
		var in struct {
			Name  string   `json:"name"`
			Dir   string   `json:"dir"`
			Files []string `json:"files"`
		}
		json.Unmarshal(body, &in)
		st := agent.NewStream(w)
		st.End(ComposeUp(r.Context(), in.Name, in.Dir, in.Files, func(l string) { st.Send(map[string]string{"l": l}) }))
	})
	s.Handle("POST /v1/migrate/import/begin", func(w http.ResponseWriter, r *http.Request, body []byte) {
		var in ImportReq
		if json.Unmarshal(body, &in) != nil {
			agent.WriteErr(w, errs.New(400, "params.invalid_json"))
			return
		}
		id, err := Begin(in)
		if err != nil {
			agent.WriteErr(w, err)
			return
		}
		agent.WriteJSON(w, 200, map[string]string{"id": id})
	})
	s.Handle("POST /v1/migrate/import/{id}/chunk", func(w http.ResponseWriter, r *http.Request, body []byte) {
		off, _ := strconv.ParseInt(r.URL.Query().Get("off"), 10, 64)
		n, err := Chunk(r.PathValue("id"), off, body)
		if err != nil {
			agent.WriteErr(w, err)
			return
		}
		agent.WriteJSON(w, 200, map[string]int64{"received": n})
	})
	s.Handle("POST /v1/migrate/import/{id}/finish", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		res, err := Finish(r.PathValue("id"))
		if err != nil {
			agent.WriteErr(w, err)
			return
		}
		agent.WriteJSON(w, 200, res)
	})
	s.Handle("POST /v1/migrate/import/{id}/abort", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		Abort(r.PathValue("id"))
		agent.WriteJSON(w, 200, map[string]bool{"ok": true})
	})
}
