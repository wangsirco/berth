package dns

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// AliDNS is Alibaba Cloud DNS (阿里云解析) over its RPC API (signature version 1.0).
// Zone IDs are the domain names themselves.
type AliDNS struct {
	KeyID, Secret string
	Base          string // https://alidns.aliyuncs.com
	HTTP          *http.Client
}

func NewAliDNS(id, secret string) *AliDNS {
	return &AliDNS{KeyID: id, Secret: secret, Base: "https://alidns.aliyuncs.com", HTTP: &http.Client{Timeout: 20 * time.Second}}
}

func aliEscape(s string) string {
	e := url.QueryEscape(s)
	e = strings.ReplaceAll(e, "+", "%20")
	e = strings.ReplaceAll(e, "*", "%2A")
	return strings.ReplaceAll(e, "%7E", "~")
}

// AliSign computes the RPC signature for a GET with these parameters.
func AliSign(params map[string]string, secret string) string {
	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = aliEscape(k) + "=" + aliEscape(params[k])
	}
	sts := "GET&%2F&" + aliEscape(strings.Join(parts, "&"))
	m := hmac.New(sha1.New, []byte(secret+"&"))
	m.Write([]byte(sts))
	return base64.StdEncoding.EncodeToString(m.Sum(nil))
}

func (a *AliDNS) call(ctx context.Context, action string, args map[string]string, out any) error {
	nonce := make([]byte, 12)
	rand.Read(nonce)
	p := map[string]string{"Format": "JSON", "Version": "2015-01-09", "AccessKeyId": a.KeyID, "SignatureMethod": "HMAC-SHA1",
		"Timestamp": time.Now().UTC().Format("2006-01-02T15:04:05Z"), "SignatureVersion": "1.0", "SignatureNonce": hex.EncodeToString(nonce), "Action": action}
	for k, v := range args {
		p[k] = v
	}
	p["Signature"] = AliSign(p, a.Secret)
	q := url.Values{}
	for k, v := range p {
		q.Set(k, v)
	}
	req, _ := http.NewRequestWithContext(ctx, "GET", a.Base+"/?"+q.Encode(), nil)
	resp, err := a.HTTP.Do(req)
	if err != nil {
		return &Error{Code: "dns.api", Detail: err.Error()}
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode >= 400 {
		var e struct{ Code, Message string }
		json.Unmarshal(b, &e)
		code := "dns.api"
		switch {
		case strings.HasPrefix(e.Code, "InvalidAccessKeyId"), strings.HasPrefix(e.Code, "SignatureDoesNotMatch"):
			code = "dns.auth"
		case strings.HasPrefix(e.Code, "Forbidden"), e.Code == "NoPermission":
			code = "dns.forbidden"
		case strings.Contains(e.Code, "NotExist"), strings.Contains(e.Code, "NotFound"):
			code = "dns.not_found"
		case e.Code == "DomainRecordDuplicate":
			code = "dns.conflict"
		}
		return &Error{Code: code, Detail: e.Code + ": " + e.Message}
	}
	if out != nil {
		return json.Unmarshal(b, out)
	}
	return nil
}

func (a *AliDNS) Verify(ctx context.Context) (Check, error) {
	z, err := a.Zones(ctx)
	if err != nil {
		return Check{}, err
	}
	return Check{Active: true, ZonesRead: true, DNSRead: true, ZoneCount: len(z)}, nil
}

func (a *AliDNS) Zones(ctx context.Context) ([]Zone, error) {
	var out struct {
		Domains struct {
			Domain []struct{ DomainId, DomainName string }
		}
	}
	if err := a.call(ctx, "DescribeDomains", map[string]string{"PageSize": "100"}, &out); err != nil {
		return nil, err
	}
	zones := []Zone{}
	for _, d := range out.Domains.Domain {
		zones = append(zones, Zone{ID: d.DomainName, Name: d.DomainName, Status: "active"})
	}
	return zones, nil
}

// rr: the part of name before the zone ("@" for the apex)
func rrOf(name, zone string) string {
	if name == zone {
		return "@"
	}
	return strings.TrimSuffix(name, "."+zone)
}

func fullOf(rr, zone string) string {
	if rr == "@" || rr == "" {
		return zone
	}
	return rr + "." + zone
}

func (a *AliDNS) Records(ctx context.Context, zone string) ([]Record, error) {
	var out struct {
		DomainRecords struct {
			Record []struct {
				RecordId, RR, Type, Value string
				TTL                       int
			}
		}
	}
	if err := a.call(ctx, "DescribeDomainRecords", map[string]string{"DomainName": zone, "PageSize": "500"}, &out); err != nil {
		return nil, err
	}
	recs := []Record{}
	for _, r := range out.DomainRecords.Record {
		recs = append(recs, Record{ID: r.RecordId, Type: r.Type, Name: fullOf(r.RR, zone), Content: r.Value, TTL: r.TTL})
	}
	return recs, nil
}

func aliTTL(t int) string {
	if t < 600 {
		t = 600 // the free edition's minimum
	}
	return fmt.Sprint(t)
}

func (a *AliDNS) Create(ctx context.Context, zone string, r Record) (Record, error) {
	var out struct{ RecordId string }
	err := a.call(ctx, "AddDomainRecord", map[string]string{"DomainName": zone, "RR": rrOf(r.Name, zone), "Type": r.Type, "Value": r.Content, "TTL": aliTTL(r.TTL)}, &out)
	r.ID = out.RecordId
	return r, err
}

func (a *AliDNS) Update(ctx context.Context, zone string, r Record) (Record, error) {
	err := a.call(ctx, "UpdateDomainRecord", map[string]string{"RecordId": r.ID, "RR": rrOf(r.Name, zone), "Type": r.Type, "Value": r.Content, "TTL": aliTTL(r.TTL)}, nil)
	return r, err
}

func (a *AliDNS) Delete(ctx context.Context, zone, id string) error {
	return a.call(ctx, "DeleteDomainRecord", map[string]string{"RecordId": id}, nil)
}
