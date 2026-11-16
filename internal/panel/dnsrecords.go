package panel

// DNS record changes. Each is undoable: undoing a create deletes the record, undoing an
// update or delete puts the previous record back.

import (
	"context"
	"encoding/json"
	"net"
	"strings"

	"github.com/wangsirco/berth/internal/change"
	"github.com/wangsirco/berth/internal/errs"
	"github.com/wangsirco/berth/internal/panel/dns"
)

type recordParams struct {
	Integration string     `json:"integration"`
	ZoneID      string     `json:"zone_id"`
	Record      dns.Record `json:"record"`
	ID          string     `json:"id,omitempty"` // delete
}

func recordObj(p recordParams, name string) change.Object {
	return change.Object{Type: "dns_record", ID: p.Integration + "/" + p.ZoneID + "/" + firstNonEmpty(p.Record.ID, p.ID), Name: name}
}

func validRecord(r dns.Record) error {
	r.Name = strings.TrimSuffix(strings.ToLower(r.Name), ".")
	if r.Name == "" || r.Content == "" {
		return errs.New(400, "dns.bad_record")
	}
	switch r.Type {
	case "A":
		if ip := net.ParseIP(r.Content); ip == nil || ip.To4() == nil {
			return errs.New(400, "dns.bad_record")
		}
	case "AAAA":
		if ip := net.ParseIP(r.Content); ip == nil || ip.To4() != nil {
			return errs.New(400, "dns.bad_record")
		}
	case "CNAME", "TXT", "MX", "NS", "CAA", "SRV":
	default:
		return errs.New(400, "dns.bad_record")
	}
	return nil
}

func (a *App) findRecord(ctx context.Context, p dns.Provider, zone, id string) (dns.Record, error) {
	recs, err := p.Records(ctx, zone)
	if err != nil {
		return dns.Record{}, dnsErr(err)
	}
	for _, r := range recs {
		if r.ID == id {
			return r, nil
		}
	}
	return dns.Record{}, errs.New(404, "dns.not_found", "detail", id)
}

func recordSummary(r dns.Record) map[string]any {
	return map[string]any{"name": r.Name, "type": r.Type, "content": r.Content, "proxied": r.Proxied}
}

func init() {
	props := map[string]change.Prop{
		"integration": {Type: "string", Desc: "DNS account id"},
		"zone_id":     {Type: "string"},
		"record":      {Type: "object", Desc: "{id, type, name, content, ttl (1 = auto), proxied, comment}"},
		"id":          {Type: "string", Desc: "record id (delete)"},
	}
	change.Register(change.Kind{
		Name: "dns.record.create", Perm: "sites.manage", Scope: "sites:write", Title: "kind.dns.record.create",
		Params: change.ParamSchema{Props: props, Required: []string{"integration", "zone_id", "record"}},
		CLI:    "berth dns add {{.record_name}} {{.record_type}} {{.record_content}}",
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			var p recordParams
			json.Unmarshal(raw, &p)
			if err := validRecord(p.Record); err != nil {
				return change.Plan{}, err
			}
			if _, err := svc(env).provider(p.Integration); err != nil {
				return change.Plan{}, err
			}
			return change.Plan{Summary: "plan.dns.record.create", SummaryP: recordSummary(p.Record), Risk: change.RiskMedium,
				Affected: []change.Object{recordObj(p, p.Record.Name)}, Undoable: true}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			var p recordParams
			json.Unmarshal(raw, &p)
			prov, err := svc(env).provider(p.Integration)
			if err != nil {
				return nil, nil, err
			}
			p.Record.ID = ""
			rec, err := prov.Create(env.Ctx, p.ZoneID, p.Record)
			if err != nil {
				return nil, nil, dnsErr(err)
			}
			u, _ := json.Marshal(recordParams{Integration: p.Integration, ZoneID: p.ZoneID, ID: rec.ID})
			return rec, &change.UndoRecipe{Kind: "dns.record.delete", Params: u}, nil
		},
	})
	change.Register(change.Kind{
		Name: "dns.record.update", Perm: "sites.manage", Scope: "sites:write", Title: "kind.dns.record.update",
		Params: change.ParamSchema{Props: props, Required: []string{"integration", "zone_id", "record"}},
		CLI:    "berth dns set {{.record_name}} {{.record_type}} {{.record_content}}",
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			var p recordParams
			json.Unmarshal(raw, &p)
			if err := validRecord(p.Record); err != nil {
				return change.Plan{}, err
			}
			prov, err := svc(env).provider(p.Integration)
			if err != nil {
				return change.Plan{}, err
			}
			cur, err := svc(env).findRecord(env.Ctx, prov, p.ZoneID, p.Record.ID)
			if err != nil {
				return change.Plan{}, err
			}
			sp := recordSummary(p.Record)
			sp["before"] = cur.Content
			return change.Plan{Summary: "plan.dns.record.update", SummaryP: sp, Risk: change.RiskMedium,
				Affected: []change.Object{recordObj(p, p.Record.Name)}, Undoable: true}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			var p recordParams
			json.Unmarshal(raw, &p)
			prov, err := svc(env).provider(p.Integration)
			if err != nil {
				return nil, nil, err
			}
			cur, err := svc(env).findRecord(env.Ctx, prov, p.ZoneID, p.Record.ID)
			if err != nil {
				return nil, nil, err
			}
			rec, err := prov.Update(env.Ctx, p.ZoneID, p.Record)
			if err != nil {
				return nil, nil, dnsErr(err)
			}
			u, _ := json.Marshal(recordParams{Integration: p.Integration, ZoneID: p.ZoneID, Record: cur})
			return rec, &change.UndoRecipe{Kind: "dns.record.update", Params: u}, nil
		},
	})
	change.Register(change.Kind{
		Name: "dns.record.delete", Perm: "sites.manage", Scope: "sites:write", Title: "kind.dns.record.delete",
		Params: change.ParamSchema{Props: props, Required: []string{"integration", "zone_id", "id"}},
		CLI:    "berth dns rm {{.id}}",
		Plan: func(env change.Env, raw json.RawMessage) (change.Plan, error) {
			var p recordParams
			json.Unmarshal(raw, &p)
			prov, err := svc(env).provider(p.Integration)
			if err != nil {
				return change.Plan{}, err
			}
			cur, err := svc(env).findRecord(env.Ctx, prov, p.ZoneID, p.ID)
			if err != nil {
				return change.Plan{}, err
			}
			return change.Plan{Summary: "plan.dns.record.delete", SummaryP: recordSummary(cur), Risk: change.RiskHigh,
				Affected: []change.Object{recordObj(p, cur.Name)}, Undoable: true}, nil
		},
		Apply: func(env change.Env, raw json.RawMessage) (any, *change.UndoRecipe, error) {
			var p recordParams
			json.Unmarshal(raw, &p)
			prov, err := svc(env).provider(p.Integration)
			if err != nil {
				return nil, nil, err
			}
			cur, err := svc(env).findRecord(env.Ctx, prov, p.ZoneID, p.ID)
			if err != nil {
				return nil, nil, err
			}
			if err := prov.Delete(env.Ctx, p.ZoneID, p.ID); err != nil {
				return nil, nil, dnsErr(err)
			}
			u, _ := json.Marshal(recordParams{Integration: p.Integration, ZoneID: p.ZoneID, Record: cur})
			return nil, &change.UndoRecipe{Kind: "dns.record.create", Params: u}, nil
		},
	})
}
