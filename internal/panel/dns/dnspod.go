package dns

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// DNSPod is Tencent Cloud DNS (腾讯云 DNSPod) over API 3.0 with TC3-HMAC-SHA256 signing.
// Zone IDs are the domain names.
type DNSPod struct {
	ID, Key string
	Base    string // https://dnspod.tencentcloudapi.com
	Host    string // signed Host header
	HTTP    *http.Client
	Now     func() time.Time
}

func NewDNSPod(id, key string) *DNSPod {
	return &DNSPod{ID: id, Key: key, Base: "https://dnspod.tencentcloudapi.com", Host: "dnspod.tencentcloudapi.com",
		HTTP: &http.Client{Timeout: 20 * time.Second}, Now: time.Now}
}

func hmacSHA256(key []byte, s string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(s))
	return m.Sum(nil)
}

func sha256hex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// TC3Authorization builds the Authorization header for one JSON POST.
func TC3Authorization(id, key, service, host, action string, payload []byte, ts time.Time) string {
	date := ts.UTC().Format("2006-01-02")
	canonical := "POST\n/\n\ncontent-type:application/json; charset=utf-8\nhost:" + host + "\nx-tc-action:" + strings.ToLower(action) +
		"\n\ncontent-type;host;x-tc-action\n" + sha256hex(payload)
	scope := date + "/" + service + "/tc3_request"
	sts := "TC3-HMAC-SHA256\n" + strconv.FormatInt(ts.Unix(), 10) + "\n" + scope + "\n" + sha256hex([]byte(canonical))
	kDate := hmacSHA256([]byte("TC3"+key), date)
	kService := hmacSHA256(kDate, service)
	kSigning := hmacSHA256(kService, "tc3_request")
	sig := hex.EncodeToString(hmacSHA256(kSigning, sts))
	return "TC3-HMAC-SHA256 Credential=" + id + "/" + scope + ", SignedHeaders=content-type;host;x-tc-action, Signature=" + sig
}

func (d *DNSPod) call(ctx context.Context, action string, in map[string]any, out any) error {
	payload, _ := json.Marshal(in)
	now := d.Now()
	req, _ := http.NewRequestWithContext(ctx, "POST", d.Base+"/", bytes.NewReader(payload))
	req.Host = d.Host
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req.Header.Set("X-TC-Action", action)
	req.Header.Set("X-TC-Version", "2021-03-23")
	req.Header.Set("X-TC-Timestamp", strconv.FormatInt(now.Unix(), 10))
	req.Header.Set("Authorization", TC3Authorization(d.ID, d.Key, "dnspod", d.Host, action, payload, now))
	resp, err := d.HTTP.Do(req)
	if err != nil {
		return &Error{Code: "dns.api", Detail: err.Error()}
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	var env struct {
		Response json.RawMessage
	}
	if err := json.Unmarshal(b, &env); err != nil {
		return &Error{Code: "dns.api", Detail: string(b)}
	}
	var e struct {
		Error *struct{ Code, Message string }
	}
	json.Unmarshal(env.Response, &e)
	if e.Error != nil {
		if e.Error.Code == "ResourceNotFound.NoDataOfRecord" {
			return nil // an empty zone: not an error
		}
		code := "dns.api"
		switch {
		case strings.HasPrefix(e.Error.Code, "AuthFailure"):
			code = "dns.auth"
		case strings.HasPrefix(e.Error.Code, "UnauthorizedOperation"):
			code = "dns.forbidden"
		case strings.HasPrefix(e.Error.Code, "ResourceNotFound"), strings.Contains(e.Error.Code, "NotExist"):
			code = "dns.not_found"
		case strings.Contains(e.Error.Code, "RecordExist"):
			code = "dns.conflict"
		}
		return &Error{Code: code, Detail: e.Error.Code + ": " + e.Error.Message}
	}
	if out != nil {
		return json.Unmarshal(env.Response, out)
	}
	return nil
}

func (d *DNSPod) Verify(ctx context.Context) (Check, error) {
	z, err := d.Zones(ctx)
	if err != nil {
		return Check{}, err
	}
	return Check{Active: true, ZonesRead: true, DNSRead: true, ZoneCount: len(z)}, nil
}

func (d *DNSPod) Zones(ctx context.Context) ([]Zone, error) {
	var out struct {
		DomainList []struct {
			Name   string
			Status string
		}
	}
	if err := d.call(ctx, "DescribeDomainList", map[string]any{"Limit": 3000}, &out); err != nil {
		return nil, err
	}
	zones := []Zone{}
	for _, z := range out.DomainList {
		zones = append(zones, Zone{ID: z.Name, Name: z.Name, Status: strings.ToLower(z.Status)})
	}
	return zones, nil
}

func (d *DNSPod) Records(ctx context.Context, zone string) ([]Record, error) {
	var out struct {
		RecordList []struct {
			RecordId          uint64
			Name, Type, Value string
			TTL               int
		}
	}
	if err := d.call(ctx, "DescribeRecordList", map[string]any{"Domain": zone, "Limit": 3000}, &out); err != nil {
		return nil, err
	}
	recs := []Record{}
	for _, r := range out.RecordList {
		recs = append(recs, Record{ID: strconv.FormatUint(r.RecordId, 10), Type: r.Type, Name: fullOf(r.Name, zone), Content: r.Value, TTL: r.TTL})
	}
	return recs, nil
}

func podTTL(t int) int {
	if t < 600 {
		return 600 // the free plan's minimum
	}
	return t
}

func (d *DNSPod) Create(ctx context.Context, zone string, r Record) (Record, error) {
	var out struct{ RecordId uint64 }
	err := d.call(ctx, "CreateRecord", map[string]any{"Domain": zone, "SubDomain": rrOf(r.Name, zone), "RecordType": r.Type, "RecordLine": "默认",
		"Value": r.Content, "TTL": podTTL(r.TTL)}, &out)
	r.ID = strconv.FormatUint(out.RecordId, 10)
	return r, err
}

func (d *DNSPod) Update(ctx context.Context, zone string, r Record) (Record, error) {
	id, _ := strconv.ParseUint(r.ID, 10, 64)
	err := d.call(ctx, "ModifyRecord", map[string]any{"Domain": zone, "RecordId": id, "SubDomain": rrOf(r.Name, zone), "RecordType": r.Type,
		"RecordLine": "默认", "Value": r.Content, "TTL": podTTL(r.TTL)}, nil)
	return r, err
}

func (d *DNSPod) Delete(ctx context.Context, zone, id string) error {
	n, _ := strconv.ParseUint(id, 10, 64)
	return d.call(ctx, "DeleteRecord", map[string]any{"Domain": zone, "RecordId": n}, nil)
}
