package panel

// 我的域名: every domain this server deals with, grouped by the registered domain — which DNS
// account holds it, when its registration runs out (RDAP), where each name points and what uses it.

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/wangsirco/berth/internal/panel/caddycfg"
	"github.com/wangsirco/berth/internal/panel/dns"
)

// Two-part public suffixes in common use (the rest of the world registers directly under the TLD).
var multiSuffix = map[string]bool{}

func init() {
	for _, s := range strings.Fields(`com.cn net.cn org.cn gov.cn edu.cn ac.cn
		com.hk net.hk org.hk edu.hk gov.hk idv.hk com.tw net.tw org.tw edu.tw gov.tw idv.tw com.mo
		co.uk org.uk me.uk ltd.uk plc.uk net.uk ac.uk gov.uk co.jp ne.jp or.jp ac.jp go.jp
		com.au net.au org.au edu.au gov.au co.nz net.nz org.nz co.kr or.kr com.sg net.sg org.sg edu.sg
		com.my net.my org.my co.th in.th co.id or.id web.id com.ph com.vn net.vn co.in net.in org.in
		com.br net.br org.br com.mx com.ar co.za com.tr com.ru org.ru co.il com.ua eu.org`) {
		multiSuffix[s] = true
	}
}

// registrable: the name a registrar sells (shop.example.co.uk → example.co.uk); "" for IPs and
// names under no public suffix (localhost, .lan, .local).
func registrable(name string) string {
	name = strings.TrimSuffix(strings.ToLower(strings.TrimPrefix(name, "*.")), ".")
	if name == "" || net.ParseIP(name) != nil || localSuffix(name) {
		return ""
	}
	parts := strings.Split(name, ".")
	if len(parts) < 2 {
		return ""
	}
	n := 2
	if len(parts) >= 3 && multiSuffix[strings.Join(parts[len(parts)-2:], ".")] {
		n = 3
	}
	if len(parts) < n {
		return ""
	}
	return strings.Join(parts[len(parts)-n:], ".")
}

type domUse struct {
	Type string `json:"type"` // site | project | tunnel
	ID   string `json:"id"`
	Name string `json:"name"`
}

type domSub struct {
	Name        string   `json:"name"`
	Records     []string `json:"records"`
	Points      string   `json:"points"` // here | cloudflare | elsewhere | none | unknown
	Proxied     bool     `json:"proxied"`
	RecordID    string   `json:"record_id,omitempty"`
	RecordType  string   `json:"record_type,omitempty"`
	UsedBy      []domUse `json:"used_by"`
	TLS         string   `json:"tls,omitempty"` // auto | internal | dns | custom | off
	CertExpires int64    `json:"cert_expires,omitempty"`
}

type domRoot struct {
	Root      string   `json:"root"`
	Account   string   `json:"account,omitempty"` // DNS account id
	Provider  string   `json:"provider,omitempty"`
	ZoneID    string   `json:"zone_id,omitempty"`
	Expires   int64    `json:"expires,omitempty"`
	Registrar string   `json:"registrar,omitempty"`
	Subs      []domSub `json:"subs"`
	Zone      bool     `json:"zone"` // listed by a DNS account
}

// classify: where a name's addresses lead, given this server's public addresses.
func classify(ips []string, proxied bool, here map[string]bool) string {
	if len(ips) == 0 {
		return "none"
	}
	for _, ip := range ips {
		if here[ip] {
			return "here"
		}
	}
	if proxied || allIn(ips, func(ip string) bool { return inRanges(ip, caddycfg.CloudflareRanges) }) {
		return "cloudflare"
	}
	return "elsewhere"
}

var zoneCache struct {
	sync.Mutex
	at   time.Time
	data map[string][]dns.Zone // account → zones
	errs map[string]string
}

// accountZones: the zones each DNS account holds (cached for five minutes).
func (a *App) accountZones(ctx context.Context) (map[string][]dns.Zone, map[string]string, []integration) {
	ins, _ := a.integrations()
	zoneCache.Lock()
	defer zoneCache.Unlock()
	if zoneCache.data != nil && time.Since(zoneCache.at) < 5*time.Minute {
		return zoneCache.data, zoneCache.errs, ins
	}
	data, errsOut := map[string][]dns.Zone{}, map[string]string{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, in := range ins {
		p, err := a.provider(in.ID)
		if err != nil {
			continue
		}
		wg.Add(1)
		go func(id string, p dns.Provider) {
			defer wg.Done()
			c, cancel := context.WithTimeout(ctx, 15*time.Second)
			defer cancel()
			zs, err := p.Zones(c)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errsOut[id] = err.Error()
				return
			}
			data[id] = zs
		}(in.ID, p)
	}
	wg.Wait()
	zoneCache.data, zoneCache.errs, zoneCache.at = data, errsOut, time.Now()
	return data, errsOut, ins
}

func (a *App) domainsOverview(ctx context.Context, host string) ([]domRoot, error) {
	roots := map[string]*domRoot{}
	sub := func(name string) *domSub {
		r := registrable(name)
		if r == "" {
			return nil
		}
		dr := roots[r]
		if dr == nil {
			dr = &domRoot{Root: r, Subs: []domSub{}}
			roots[r] = dr
		}
		for i := range dr.Subs {
			if dr.Subs[i].Name == name {
				return &dr.Subs[i]
			}
		}
		dr.Subs = append(dr.Subs, domSub{Name: name, UsedBy: []domUse{}, Points: "unknown"})
		return &dr.Subs[len(dr.Subs)-1]
	}

	// what uses each name
	sites, _ := a.sites(host)
	certs, _ := a.certs()
	certByID := map[int64]certRow{}
	for _, c := range certs {
		certByID[c.ID] = c
	}
	for _, s := range sites {
		label := s.Options.Note
		if label == "" {
			label = s.Primary()
		}
		for _, d := range s.Domains {
			x := sub(d)
			if x == nil {
				continue
			}
			x.UsedBy = append(x.UsedBy, domUse{"site", strconv.FormatInt(s.ID, 10), label})
			if s.Target.Type == "project" && s.Target.Project != "" {
				x.UsedBy = append(x.UsedBy, domUse{"project", s.Target.Project, s.Target.Project})
			}
			x.TLS = s.TLS.Mode
			if s.TLS.Mode == "custom" {
				if c, ok := certByID[s.TLS.CertID]; ok {
					x.CertExpires = c.Info.NotAfter.Unix()
				}
			}
		}
	}

	// which account holds each registered domain
	zones, _, ins := a.accountZones(ctx)
	kind := map[string]string{}
	for _, in := range ins {
		kind[in.ID] = in.Kind
	}
	for acc, zs := range zones {
		for _, z := range zs {
			r := registrable(z.Name)
			if r == "" {
				continue
			}
			dr := roots[r]
			if dr == nil {
				dr = &domRoot{Root: r, Subs: []domSub{}}
				roots[r] = dr
			}
			if dr.Account == "" {
				dr.Account, dr.Provider, dr.ZoneID, dr.Zone = acc, kind[acc], z.ID, true
			}
		}
	}

	// where each used name points: the account's records when there is one, else a lookup
	here := map[string]bool{}
	for _, typ := range []string{"A", "AAAA"} {
		if ip, err := a.publicIP(ctx, host, typ); err == nil && ip != "" {
			here[ip] = true
		}
	}
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	for _, dr := range roots {
		if len(dr.Subs) == 0 {
			continue
		}
		wg.Add(1)
		go func(dr *domRoot) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			if dr.Account != "" {
				if p, err := a.provider(dr.Account); err == nil {
					c, cancel := context.WithTimeout(ctx, 15*time.Second)
					recs, err := p.Records(c, dr.ZoneID)
					cancel()
					if err == nil {
						for i := range dr.Subs {
							x := &dr.Subs[i]
							x.Records = []string{}
							for _, r := range recs {
								if r.Name != x.Name || (r.Type != "A" && r.Type != "AAAA" && r.Type != "CNAME") {
									continue
								}
								x.Records = append(x.Records, r.Content)
								x.Proxied = x.Proxied || r.Proxied
								if x.RecordID == "" || r.Type == "A" {
									x.RecordID, x.RecordType = r.ID, r.Type
								}
							}
							x.Points = classify(x.Records, x.Proxied, here)
						}
						return
					}
				}
			}
			for i := range dr.Subs {
				x := &dr.Subs[i]
				if strings.HasPrefix(x.Name, "*.") {
					continue
				}
				var res []dnsRes
				c, cancel := context.WithTimeout(ctx, 10*time.Second)
				err := a.call(c, host, "POST", "/v1/probe/dns", map[string]any{"name": x.Name, "resolvers": []string{"1.1.1.1"}}, &res)
				cancel()
				if err != nil {
					continue
				}
				x.Records = []string{}
				for _, r := range res {
					x.Records = append(x.Records, r.IPs...)
				}
				x.Points = classify(x.Records, false, here)
			}
		}(dr)
	}
	wg.Wait()

	out := make([]domRoot, 0, len(roots))
	for _, dr := range roots {
		dr.Expires, dr.Registrar = a.rdapCached(dr.Root)
		sort.Slice(dr.Subs, func(i, j int) bool { return dr.Subs[i].Name < dr.Subs[j].Name })
		out = append(out, *dr)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Root < out[j].Root })
	go a.rdapRefresh(out) // fills in what is missing or a day old, for the next look
	return out, nil
}

// ---- RDAP (registration expiry), asked of the registry itself through IANA's bootstrap list ----

var rdapHTTP = &http.Client{Timeout: 12 * time.Second}

var rdapBoot struct {
	sync.Mutex
	at    time.Time
	bases map[string]string // tld → RDAP base URL
}

func (a *App) rdapCached(root string) (int64, string) {
	var exp int64
	var reg string
	a.db.QueryRow(`SELECT expires, registrar FROM domain_rdap WHERE root=?`, root).Scan(&exp, &reg)
	return exp, reg
}

var rdapBusy sync.Mutex

func (a *App) rdapRefresh(roots []domRoot) {
	if !rdapBusy.TryLock() {
		return
	}
	defer rdapBusy.Unlock()
	now := time.Now().Unix()
	for _, r := range roots {
		var checked int64
		a.db.QueryRow(`SELECT checked FROM domain_rdap WHERE root=?`, r.Root).Scan(&checked)
		if now-checked < 86400 {
			continue
		}
		exp, reg, err := rdapLookup(context.Background(), r.Root)
		msg := ""
		if err != nil {
			msg = err.Error()
		}
		a.db.Exec(`INSERT INTO domain_rdap(root, expires, registrar, checked, error) VALUES(?,?,?,?,?)
			ON CONFLICT(root) DO UPDATE SET expires=CASE WHEN excluded.expires>0 THEN excluded.expires ELSE domain_rdap.expires END,
			registrar=CASE WHEN excluded.registrar<>'' THEN excluded.registrar ELSE domain_rdap.registrar END, checked=excluded.checked, error=excluded.error`,
			r.Root, exp, reg, now, msg)
	}
}

var rdapBootURL = "https://data.iana.org/rdap/dns.json"

func rdapBase(ctx context.Context, tld string) (string, error) {
	rdapBoot.Lock()
	defer rdapBoot.Unlock()
	if rdapBoot.bases == nil || time.Since(rdapBoot.at) > 7*24*time.Hour {
		b, err := fetch(ctx, rdapBootURL, 2<<20)
		if err != nil {
			return "", err
		}
		var boot struct {
			Services [][][]string `json:"services"`
		}
		if err := json.Unmarshal(b, &boot); err != nil {
			return "", err
		}
		m := map[string]string{}
		for _, s := range boot.Services {
			if len(s) < 2 || len(s[1]) == 0 {
				continue
			}
			for _, t := range s[0] {
				m[strings.ToLower(t)] = s[1][0]
			}
		}
		rdapBoot.bases, rdapBoot.at = m, time.Now()
	}
	base := rdapBoot.bases[tld]
	if base == "" {
		return "", errText2("no RDAP service for ." + tld)
	}
	return base, nil
}

func rdapLookup(ctx context.Context, root string) (int64, string, error) {
	tld := root[strings.LastIndex(root, ".")+1:]
	base, err := rdapBase(ctx, tld)
	if err != nil {
		return 0, "", err
	}
	b, err := fetch(ctx, strings.TrimSuffix(base, "/")+"/domain/"+root, 1<<20)
	if err != nil {
		return 0, "", err
	}
	return parseRDAP(b)
}

func parseRDAP(b []byte) (int64, string, error) {
	var d struct {
		Events []struct {
			Action string `json:"eventAction"`
			Date   string `json:"eventDate"`
		} `json:"events"`
		Entities []struct {
			Roles  []string `json:"roles"`
			Handle string   `json:"handle"`
			Vcard  []any    `json:"vcardArray"`
		} `json:"entities"`
	}
	if err := json.Unmarshal(b, &d); err != nil {
		return 0, "", err
	}
	var exp int64
	for _, e := range d.Events {
		if e.Action == "expiration" {
			if t, err := time.Parse(time.RFC3339, e.Date); err == nil {
				exp = t.Unix()
			}
		}
	}
	reg := ""
	for _, e := range d.Entities {
		for _, r := range e.Roles {
			if r == "registrar" {
				reg = vcardName(e.Vcard)
				if reg == "" {
					reg = e.Handle
				}
			}
		}
	}
	return exp, reg, nil
}

// vcardName: the "fn" of a jCard (["vcard", [["fn", {}, "text", "Name"], …]]).
func vcardName(v []any) string {
	if len(v) < 2 {
		return ""
	}
	props, _ := v[1].([]any)
	for _, p := range props {
		f, _ := p.([]any)
		if len(f) >= 4 && f[0] == "fn" {
			s, _ := f[3].(string)
			return s
		}
	}
	return ""
}

func init() {
	routeHooks = append(routeHooks, func(a *App, mux *http.ServeMux) {
		mux.HandleFunc("GET /api/v1/hosts/{host}/domains/overview", a.authed("sites.view", func(w http.ResponseWriter, r *http.Request) {
			out, err := a.domainsOverview(r.Context(), r.PathValue("host"))
			if err != nil {
				a.fail(w, r, err)
				return
			}
			here := []string{}
			for _, typ := range []string{"A", "AAAA"} {
				if ip, err := a.publicIP(r.Context(), r.PathValue("host"), typ); err == nil && ip != "" {
					here = append(here, ip)
				}
			}
			writeJSON(w, 200, map[string]any{"items": out, "here": here})
		}))
	})
}
