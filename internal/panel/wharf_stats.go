package panel

// Wharf's statistics shown in Berth (protocol 3, scope stats): read-only, passed through as
// Wharf's own pages get them, drawn by Berth. Only while someone looks: the page asks, Berth
// asks Wharf (the summary at most every 25 seconds).

import (
	"encoding/json"
	"net/http"
	"net/url"
	"regexp"
	"sync"
	"time"

	"github.com/wangsirco/berth/internal/errs"
	"github.com/wangsirco/berth/internal/panel/wharfapi"
)

// the statistics Berth may read, and nothing else
var wharfStatsPath = regexp.MustCompile(`^(summary|traffic|geo|visitors|devices|dns|devices/[0-9a-fA-F:]{17}/(traffic|presence)|forwards/[A-Za-z0-9_.-]{1,64})$`)

var wharfSummaryCache = struct {
	sync.Mutex
	at   time.Time
	body json.RawMessage
}{}

func init() {
	routeHooks = append(routeHooks, func(a *App, mux *http.ServeMux) {
		mux.HandleFunc("GET /api/v1/wharf/stats/{what...}", a.authed("network.view", func(w http.ResponseWriter, r *http.Request) {
			what := r.PathValue("what")
			if !wharfStatsPath.MatchString(what) {
				a.fail(w, r, errs.New(404, "wharf.stats_unknown"))
				return
			}
			l, err := a.wharf()
			if err != nil {
				a.fail(w, r, err)
				return
			}
			if l.Protocol > 0 && l.Protocol < 3 {
				a.fail(w, r, errs.New(409, "wharf.stats_needs_upgrade"))
				return
			}
			if !l.has("stats") {
				a.fail(w, r, errs.New(403, "wharf.stats_no_scope"))
				return
			}
			if what == "summary" {
				wharfSummaryCache.Lock()
				if time.Since(wharfSummaryCache.at) < 25*time.Second && wharfSummaryCache.body != nil {
					b := wharfSummaryCache.body
					wharfSummaryCache.Unlock()
					w.Header().Set("Content-Type", "application/json")
					w.Write(b)
					return
				}
				wharfSummaryCache.Unlock()
			}
			q := url.Values{}
			for _, k := range []string{"from", "to", "range", "limit", "before_id", "q", "match", "mac", "cc"} {
				if v := r.URL.Query().Get(k); v != "" {
					q.Set(k, v)
				}
			}
			path := "/stats/" + what
			if len(q) > 0 {
				path += "?" + q.Encode()
			}
			var out json.RawMessage
			if err := l.api().Do(r.Context(), "GET", path, "", nil, &out); err != nil {
				switch {
				case wharfapi.IsStatus(err, 404) && what != "dns":
					a.fail(w, r, errs.New(409, "wharf.stats_needs_upgrade"))
				case wharfapi.IsStatus(err, 404):
					a.fail(w, r, errs.New(404, "wharf.stats_no_dns"))
				case wharfapi.IsStatus(err, 403):
					a.fail(w, r, errs.New(403, "wharf.stats_no_scope"))
				default:
					a.fail(w, r, a.wharfErr(err))
				}
				return
			}
			if what == "summary" {
				wharfSummaryCache.Lock()
				wharfSummaryCache.at, wharfSummaryCache.body = time.Now(), out
				wharfSummaryCache.Unlock()
			}
			w.Header().Set("Content-Type", "application/json")
			w.Write(out)
		}))
	})
}
