package dns

import (
	"context"
	"errors"
	"testing"

	"github.com/wangsirco/berth/internal/panel/dns/dnstest"
)

func TestCloudflare(t *testing.T) {
	f := dnstest.New("good", "example.com", "shop.example.com", "other.net")
	defer f.Close()
	cf := NewCloudflare("good")
	cf.Base = f.URL
	ctx := context.Background()

	ch, err := cf.Verify(ctx)
	if err != nil || !ch.Active || !ch.ZonesRead || !ch.DNSRead || ch.ZoneCount != 3 {
		t.Fatalf("verify %v %+v", err, ch)
	}
	zones, _ := cf.Zones(ctx)
	if z, ok := ZoneFor(zones, "a.shop.example.com"); !ok || z.Name != "shop.example.com" {
		t.Fatalf("zone for: %+v", z)
	}
	if _, ok := ZoneFor(zones, "example.org"); ok {
		t.Fatal("matched a foreign name")
	}

	rec, err := cf.Create(ctx, "z1", Record{Type: "A", Name: "www.example.com", Content: "1.2.3.4", Proxied: true})
	if err != nil || rec.ID == "" || !rec.Proxied {
		t.Fatalf("create %v %+v", err, rec)
	}
	rec.Content = "5.6.7.8"
	if out, err := cf.Update(ctx, "z1", rec); err != nil || out.Content != "5.6.7.8" {
		t.Fatalf("update %v %+v", err, out)
	}
	list, _ := cf.Records(ctx, "z1")
	if len(list) != 1 {
		t.Fatalf("records %v", list)
	}
	if err := cf.Delete(ctx, "z1", rec.ID); err != nil {
		t.Fatal(err)
	}
	var de *Error
	if err := cf.Delete(ctx, "z1", rec.ID); !errors.As(err, &de) || de.Code != "dns.not_found" {
		t.Fatalf("delete twice: %v", err)
	}

	f.ReadOnly = true
	if _, err := cf.Create(ctx, "z1", Record{Type: "A", Name: "x.example.com", Content: "1.1.1.1"}); !errors.As(err, &de) || de.Code != "dns.forbidden" {
		t.Fatalf("read-only token: %v", err)
	}
	bad := NewCloudflare("nope")
	bad.Base = f.URL
	if _, err := bad.Verify(ctx); !errors.As(err, &de) || de.Code != "dns.auth" {
		t.Fatalf("bad token: %v", err)
	}
}
