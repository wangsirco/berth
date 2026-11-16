package panel

// Who each container and virtual machine talks to (方案 §5.12), kept by the hour: the
// agent reports connection deltas, the panel sums them per object, direction, peer and
// port. Sites get their visitors from the access logs instead (they arrive through the
// web entry, so the connection table only shows the entry).

import (
	"context"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

type flowRow struct {
	Obj     string `json:"obj"`
	Dir     string `json:"dir"`
	Peer    string `json:"peer"`
	Proto   string `json:"proto"`
	Port    int    `json:"port"`
	Conns   int    `json:"conns"`
	Bytes   int64  `json:"bytes"`
	Country string `json:"country,omitempty"`
	PTR     string `json:"ptr,omitempty"`
}

const flowsKeepDays = 14

func (a *App) flowsLoop(ctx context.Context) {
	tick := time.NewTicker(time.Minute)
	defer tick.Stop()
	lastPrune := time.Time{}
	for {
		for _, host := range a.connectedHosts() {
			a.sampleFlows(ctx, host)
		}
		if time.Since(lastPrune) > 6*time.Hour {
			a.db.Exec(`DELETE FROM flows_hourly WHERE hour < ?`, time.Now().Add(-flowsKeepDays*24*time.Hour).Unix())
			lastPrune = time.Now()
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

func (a *App) sampleFlows(ctx context.Context, host string) {
	c, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	var out struct {
		Rows []flowRow `json:"rows"`
	}
	if err := a.call(c, host, "GET", "/v1/net/flows?consumer=panel-"+a.caFP()[:16], nil, &out); err != nil {
		return
	}
	hour := hourOf(time.Now())
	tx, err := a.db.Begin()
	if err != nil {
		return
	}
	now := time.Now().Unix()
	for _, r := range out.Rows {
		tx.Exec(`INSERT INTO flows_hourly(host, obj, dir, peer, proto, port, hour, conns, bytes) VALUES(?,?,?,?,?,?,?,?,?)
			ON CONFLICT(host, obj, dir, peer, proto, port, hour) DO UPDATE SET conns=conns+excluded.conns, bytes=bytes+excluded.bytes`,
			host, r.Obj, r.Dir, r.Peer, r.Proto, r.Port, hour, r.Conns, r.Bytes)
		if r.Country != "" || r.PTR != "" {
			tx.Exec(`INSERT INTO flow_peers(ip, ptr, country, seen) VALUES(?,?,?,?)
				ON CONFLICT(ip) DO UPDATE SET ptr=CASE WHEN excluded.ptr<>'' THEN excluded.ptr ELSE ptr END,
				country=CASE WHEN excluded.country<>'' THEN excluded.country ELSE country END, seen=excluded.seen`, r.Peer, r.PTR, r.Country, now)
		}
	}
	tx.Commit()
}

type flowPeer struct {
	Peer    string   `json:"peer"`
	PTR     string   `json:"ptr,omitempty"`
	Country string   `json:"country,omitempty"`
	Conns   int      `json:"conns"`
	Bytes   int64    `json:"bytes"`
	Ports   []string `json:"ports"` // "443/tcp", most used first
}

var flowRanges = map[string]time.Duration{"1h": time.Hour, "24h": 24 * time.Hour, "7d": 7 * 24 * time.Hour, "14d": 14 * 24 * time.Hour}

// GET /api/v1/hosts/{host}/flows?obj=container:NAME&range=24h
// Without obj: the objects that moved the most, each direction.
func (a *App) handleFlows(w http.ResponseWriter, r *http.Request) {
	host := r.PathValue("host")
	q := r.URL.Query()
	span, ok := flowRanges[q.Get("range")]
	if !ok {
		span = 24 * time.Hour
	}
	since := hourOf(time.Now().Add(-span))
	obj := q.Get("obj")
	if obj == "" {
		rows, err := a.db.Query(`SELECT obj, dir, SUM(conns), SUM(bytes) FROM flows_hourly WHERE host=? AND hour>=? GROUP BY obj, dir ORDER BY SUM(bytes) DESC LIMIT 60`, host, since)
		if err != nil {
			a.fail(w, r, err)
			return
		}
		defer rows.Close()
		type objUse struct {
			Obj   string `json:"obj"`
			Dir   string `json:"dir"`
			Conns int    `json:"conns"`
			Bytes int64  `json:"bytes"`
		}
		list := []objUse{}
		for rows.Next() {
			var o objUse
			rows.Scan(&o.Obj, &o.Dir, &o.Conns, &o.Bytes)
			list = append(list, o)
		}
		writeJSON(w, 200, map[string]any{"objects": list, "since": since})
		return
	}
	if !strings.HasPrefix(obj, "container:") && !strings.HasPrefix(obj, "vm:") {
		writeJSON(w, 400, map[string]string{"error": "obj"})
		return
	}
	rows, err := a.db.Query(`SELECT f.dir, f.peer, f.proto, f.port, SUM(f.conns), SUM(f.bytes), COALESCE(p.ptr,''), COALESCE(p.country,'')
		FROM flows_hourly f LEFT JOIN flow_peers p ON p.ip=f.peer
		WHERE f.host=? AND f.obj=? AND f.hour>=? GROUP BY f.dir, f.peer, f.proto, f.port`, host, obj, since)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	defer rows.Close()
	type portUse struct {
		key   string
		bytes int64
	}
	peers := map[string]*flowPeer{}
	ports := map[string][]portUse{}
	totals := map[string]map[string]int64{"in": {"bytes": 0, "conns": 0}, "out": {"bytes": 0, "conns": 0}}
	for rows.Next() {
		var dir, peer, proto, ptr, country string
		var port, conns int
		var bytes int64
		rows.Scan(&dir, &peer, &proto, &port, &conns, &bytes, &ptr, &country)
		k := dir + "|" + peer
		p := peers[k]
		if p == nil {
			p = &flowPeer{Peer: peer, PTR: ptr, Country: country}
			peers[k] = p
		}
		p.Conns += conns
		p.Bytes += bytes
		ports[k] = append(ports[k], portUse{key: strconv.Itoa(port) + "/" + proto, bytes: bytes})
		if t := totals[dir]; t != nil {
			t["bytes"] += bytes
			t["conns"] += int64(conns)
		}
	}
	out := map[string][]flowPeer{"in": {}, "out": {}}
	for k, p := range peers {
		pu := ports[k]
		sort.Slice(pu, func(i, j int) bool { return pu[i].bytes > pu[j].bytes })
		for i, x := range pu {
			if i == 6 {
				break
			}
			p.Ports = append(p.Ports, x.key)
		}
		dir, _, _ := strings.Cut(k, "|")
		out[dir] = append(out[dir], *p)
	}
	for _, d := range []string{"in", "out"} {
		l := out[d]
		sort.Slice(l, func(i, j int) bool {
			if l[i].Bytes != l[j].Bytes {
				return l[i].Bytes > l[j].Bytes
			}
			return l[i].Conns > l[j].Conns
		})
		if len(l) > 50 {
			l = l[:50]
		}
		out[d] = l
	}
	writeJSON(w, 200, map[string]any{"in": out["in"], "out": out["out"], "totals": totals, "since": since})
}

func init() {
	routeHooks = append(routeHooks, func(a *App, mux *http.ServeMux) {
		mux.HandleFunc("GET /api/v1/hosts/{host}/flows", a.authed("network.view", a.handleFlows))
	})
}
