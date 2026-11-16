package panel

import (
	"encoding/json"
	"io"
	"log"
	"net/http"

	"github.com/wangsirco/berth/internal/errs"
)

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("panel: write json: %v", err)
	}
}

type errBody struct {
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Params  map[string]any `json:"params,omitempty"`
}

// fail writes err as {"error":{code,message,params}} translated for the request.
func (a *App) fail(w http.ResponseWriter, r *http.Request, err error) {
	e := errs.From(err)
	if e.Status >= 500 {
		log.Printf("panel: %s %s: %v %v", r.Method, r.URL.Path, e.Code, e.Params)
	}
	writeJSON(w, e.Status, map[string]errBody{"error": {Code: e.Code, Message: T(a.locale(r), e.Code, e.Params), Params: e.Params}})
}

func (a *App) locale(r *http.Request) string {
	if u := currentUser(r); u != nil && u.Locale != "" {
		return u.Locale
	}
	return pickLocale(r.Header.Get("Accept-Language"))
}

func readJSON(r *http.Request, v any) error {
	if err := json.NewDecoder(io.LimitReader(r.Body, 4<<20)).Decode(v); err != nil && err != io.EOF {
		return errs.New(400, "params.invalid_json")
	}
	return nil
}
