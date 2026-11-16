package desktop

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/wangsirco/berth/internal/agent"
	"github.com/wangsirco/berth/internal/errs"
)

// Register adds /v1/desktop/*.
func Register(s *agent.Server) {
	s.Handle("GET /v1/desktop", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		st := Get(r.Context())
		var items []Item
		if st.Installed || r.URL.Query().Get("items") == "1" {
			items = ItemsWithState(r.Context())
		} else {
			items = Items
		}
		agent.WriteJSON(w, 200, map[string]any{"status": st, "items": items, "presets": map[string][]string{"basic": PresetItems("basic"), "dev": PresetItems("dev")}})
	})
	stream := func(path string, fn func(ctx context.Context, body []byte, log func(string)) error) {
		s.Handle("POST "+path, func(w http.ResponseWriter, r *http.Request, body []byte) {
			st := agent.NewStream(w)
			log := func(l string) { st.Send(map[string]string{"l": l}) }
			err := fn(r.Context(), body, log)
			if err == nil {
				b, _ := json.Marshal(Get(r.Context()))
				log("::result " + string(b))
			} else {
				err = errs.New(500, "desktop.failed", "detail", err.Error())
			}
			st.End(err)
		})
	}
	stream("/v1/desktop/install", func(ctx context.Context, body []byte, log func(string)) error {
		var p struct{ Items []string }
		json.Unmarshal(body, &p)
		return Install(ctx, p.Items, log)
	})
	stream("/v1/desktop/enable", func(ctx context.Context, body []byte, log func(string)) error {
		var p struct{ User, Password, Lang string }
		json.Unmarshal(body, &p)
		return Enable(ctx, p.User, p.Password, p.Lang, log)
	})
	stream("/v1/desktop/disable", func(ctx context.Context, _ []byte, log func(string)) error { return Disable(ctx, log) })
	stream("/v1/desktop/uninstall", func(ctx context.Context, _ []byte, log func(string)) error { return Uninstall(ctx, log) })
}
