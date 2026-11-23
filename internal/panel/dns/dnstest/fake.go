// Package dnstest is a fake Cloudflare API for tests.
package dnstest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
)

type Fake struct {
	*httptest.Server
	Token    string
	ReadOnly bool // DNS writes are forbidden (a token without DNS:Edit)
	mu       sync.Mutex
	Zones    []map[string]any
	records  map[string][]map[string]any // zone id -> records
	nextID   int

	// beyond DNS
	Deny          map[string]bool // feature -> forbidden: tunnel | access | firewall | settings | analytics
	Tunnels       map[string]map[string]any
	Ingress       map[string][]any // tunnel id -> ingress rules
	AccessApps    map[string]map[string]any
	Policies      map[string][]any // app id -> policies
	AccessRules   map[string]map[string]any
	SecurityLevel map[string]string // zone -> level
	Purges        []map[string]any
}

func New(token string, zones ...string) *Fake {
	f := &Fake{Token: token, records: map[string][]map[string]any{}, Deny: map[string]bool{}, Tunnels: map[string]map[string]any{},
		Ingress: map[string][]any{}, AccessApps: map[string]map[string]any{}, Policies: map[string][]any{}, AccessRules: map[string]map[string]any{},
		SecurityLevel: map[string]string{}}
	for i, z := range zones {
		f.Zones = append(f.Zones, map[string]any{"id": fmt.Sprintf("z%d", i+1), "name": z, "status": "active", "account": map[string]string{"id": "acc1", "name": "Bufan"}})
	}
	f.Server = httptest.NewServer(http.HandlerFunc(f.serve))
	return f
}

func write(w http.ResponseWriter, code int, result any, info map[string]int, errs ...map[string]any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if errs == nil {
		errs = []map[string]any{}
	}
	json.NewEncoder(w).Encode(map[string]any{"success": code < 400, "errors": errs, "result": result, "result_info": info})
}

// Records returns a copy of a zone's records.
func (f *Fake) Records(zone string) []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]map[string]any{}, f.records[zone]...)
}

func (f *Fake) serve(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer "+f.Token {
		write(w, 401, nil, nil, map[string]any{"code": 1000, "message": "Invalid API Token"})
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page == 0 {
		page = 1
	}
	switch {
	case r.URL.Path == "/user/tokens/verify":
		write(w, 200, map[string]any{"id": "tok", "status": "active"}, nil)
	case r.URL.Path == "/zones":
		// one zone per page, to exercise pagination
		if page > len(f.Zones) {
			write(w, 200, []any{}, map[string]int{"page": page, "total_pages": len(f.Zones)})
			return
		}
		write(w, 200, []any{f.Zones[page-1]}, map[string]int{"page": page, "total_pages": len(f.Zones)})
	case len(parts) >= 3 && parts[0] == "zones" && parts[2] == "dns_records":
		zone := parts[1]
		if r.Method != "GET" && f.ReadOnly {
			write(w, 403, nil, nil, map[string]any{"code": 10000, "message": "Authentication error"})
			return
		}
		switch {
		case r.Method == "GET":
			write(w, 200, f.records[zone], map[string]int{"page": 1, "total_pages": 1})
		case r.Method == "POST":
			var rec map[string]any
			json.NewDecoder(r.Body).Decode(&rec)
			for _, o := range f.records[zone] {
				if o["name"] == rec["name"] && o["type"] == rec["type"] && rec["type"] == "CNAME" {
					write(w, 400, nil, nil, map[string]any{"code": 81053, "message": "An A, AAAA, or CNAME record with that host already exists."})
					return
				}
			}
			f.nextID++
			rec["id"] = fmt.Sprintf("r%d", f.nextID)
			f.records[zone] = append(f.records[zone], rec)
			write(w, 200, rec, nil)
		case (r.Method == "PATCH" || r.Method == "DELETE") && len(parts) == 4:
			for i, o := range f.records[zone] {
				if o["id"] != parts[3] {
					continue
				}
				if r.Method == "DELETE" {
					f.records[zone] = append(f.records[zone][:i], f.records[zone][i+1:]...)
					write(w, 200, map[string]string{"id": parts[3]}, nil)
					return
				}
				var upd map[string]any
				json.NewDecoder(r.Body).Decode(&upd)
				for k, v := range upd {
					o[k] = v
				}
				write(w, 200, o, nil)
				return
			}
			write(w, 404, nil, nil, map[string]any{"code": 81044, "message": "Record does not exist."})
		}
	default:
		if !f.serveExtra(w, r, parts) {
			write(w, 404, nil, nil, map[string]any{"code": 7003, "message": "Could not route"})
		}
	}
}

func (f *Fake) id(prefix string) string {
	f.nextID++
	return fmt.Sprintf("%s%d", prefix, f.nextID)
}

func forbidden(w http.ResponseWriter) {
	write(w, 403, nil, nil, map[string]any{"code": 10000, "message": "Authentication error"})
}

// serveExtra: Tunnel, Access, IP access rules, settings, purge, GraphQL (called with f.mu held).
func (f *Fake) serveExtra(w http.ResponseWriter, r *http.Request, p []string) bool {
	body := map[string]any{}
	if r.Body != nil {
		json.NewDecoder(r.Body).Decode(&body)
	}
	switch {
	case r.URL.Path == "/graphql":
		if f.Deny["analytics"] {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"data": nil, "errors": []any{map[string]string{"message": "not authorized for that account"}}})
			return true
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"viewer": map[string]any{"zones": []any{map[string]any{"zoneTag": "z1",
			"httpRequests1dGroups": []any{
				map[string]any{"dimensions": map[string]string{"date": "2026-11-10"}, "sum": map[string]int{"requests": 1200, "cachedRequests": 800, "bytes": 52428800, "threats": 3, "pageViews": 400}, "uniq": map[string]int{"uniques": 90}},
				map[string]any{"dimensions": map[string]string{"date": "2026-11-11"}, "sum": map[string]int{"requests": 1500, "cachedRequests": 900, "bytes": 62914560, "threats": 7, "pageViews": 500}, "uniq": map[string]int{"uniques": 110}},
			}}}}}})
		return true
	case len(p) >= 4 && p[0] == "zones" && p[2] == "settings" && p[3] == "security_level":
		if f.Deny["settings"] {
			forbidden(w)
			return true
		}
		if r.Method == "PATCH" {
			f.SecurityLevel[p[1]], _ = body["value"].(string)
		}
		lvl := f.SecurityLevel[p[1]]
		if lvl == "" {
			lvl = "medium"
		}
		write(w, 200, map[string]any{"id": "security_level", "value": lvl}, nil)
		return true
	case len(p) == 3 && p[0] == "zones" && p[2] == "purge_cache":
		f.Purges = append(f.Purges, body)
		write(w, 200, map[string]string{"id": p[1]}, nil)
		return true
	case len(p) >= 3 && p[0] == "accounts" && p[2] == "cfd_tunnel":
		if f.Deny["tunnel"] {
			forbidden(w)
			return true
		}
		switch {
		case len(p) == 3 && r.Method == "GET":
			out := []any{}
			for _, t := range f.Tunnels {
				out = append(out, t)
			}
			write(w, 200, out, nil)
		case len(p) == 3 && r.Method == "POST":
			id := f.id("tun-")
			t := map[string]any{"id": id, "name": body["name"]}
			f.Tunnels[id] = t
			write(w, 200, t, nil)
		case len(p) == 5 && p[4] == "token":
			write(w, 200, "eyJ-token-"+p[3], nil)
		case len(p) == 5 && p[4] == "configurations" && r.Method == "PUT":
			cfg, _ := body["config"].(map[string]any)
			ing, _ := cfg["ingress"].([]any)
			f.Ingress[p[3]] = ing
			write(w, 200, map[string]any{"tunnel_id": p[3]}, nil)
		case len(p) == 4 && r.Method == "DELETE":
			delete(f.Tunnels, p[3])
			delete(f.Ingress, p[3])
			write(w, 200, map[string]string{"id": p[3]}, nil)
		default:
			return false
		}
		return true
	case len(p) >= 4 && p[0] == "accounts" && p[2] == "access" && p[3] == "apps":
		if f.Deny["access"] {
			forbidden(w)
			return true
		}
		switch {
		case len(p) == 4 && r.Method == "GET":
			out := []any{}
			for _, a := range f.AccessApps {
				out = append(out, a)
			}
			write(w, 200, out, nil)
		case len(p) == 4 && r.Method == "POST":
			id := f.id("app-")
			a := map[string]any{"id": id, "name": body["name"], "domain": body["domain"], "self_hosted_domains": body["self_hosted_domains"]}
			f.AccessApps[id] = a
			write(w, 200, a, nil)
		case len(p) == 6 && p[5] == "policies" && r.Method == "POST":
			f.Policies[p[4]] = append(f.Policies[p[4]], body)
			write(w, 200, map[string]any{"id": f.id("pol-")}, nil)
		case len(p) == 5 && r.Method == "DELETE":
			delete(f.AccessApps, p[4])
			delete(f.Policies, p[4])
			write(w, 200, map[string]string{"id": p[4]}, nil)
		default:
			return false
		}
		return true
	case len(p) >= 5 && p[0] == "accounts" && p[2] == "firewall" && p[3] == "access_rules":
		if f.Deny["firewall"] {
			forbidden(w)
			return true
		}
		switch {
		case len(p) == 5 && r.Method == "GET":
			out := []any{}
			for _, x := range f.AccessRules {
				out = append(out, x)
			}
			write(w, 200, out, nil)
		case len(p) == 5 && r.Method == "POST":
			id := f.id("rule-")
			body["id"] = id
			f.AccessRules[id] = body
			write(w, 200, body, nil)
		case len(p) == 6 && r.Method == "DELETE":
			if _, ok := f.AccessRules[p[5]]; !ok {
				write(w, 404, nil, nil, map[string]any{"code": 10001, "message": "not found"})
				return true
			}
			delete(f.AccessRules, p[5])
			write(w, 200, map[string]string{"id": p[5]}, nil)
		default:
			return false
		}
		return true
	}
	return false
}
