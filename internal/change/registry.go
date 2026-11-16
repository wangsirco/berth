package change

import (
	"encoding/json"
	"sort"
	"strings"
	"sync"
	"text/template"

	"github.com/wangsirco/berth/internal/errs"
)

var (
	regMu sync.RWMutex
	reg   = map[string]Kind{}
)

func Register(k Kind) {
	regMu.Lock()
	defer regMu.Unlock()
	if _, dup := reg[k.Name]; dup {
		panic("change: duplicate kind " + k.Name)
	}
	reg[k.Name] = k
}

func Get(name string) (Kind, bool) {
	regMu.RLock()
	defer regMu.RUnlock()
	k, ok := reg[name]
	return k, ok
}

func All() []Kind {
	regMu.RLock()
	defer regMu.RUnlock()
	out := make([]Kind, 0, len(reg))
	for _, k := range reg {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (s ParamSchema) Validate(raw json.RawMessage) error {
	m := map[string]any{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &m); err != nil {
			return errs.New(400, "params.invalid_json")
		}
	}
	for _, r := range s.Required {
		v, ok := m[r]
		if !ok || v == "" || v == nil {
			return errs.New(400, "params.required", "name", r)
		}
	}
	for name, v := range m {
		p, ok := s.Props[name]
		if !ok {
			return errs.New(400, "params.unknown", "name", name)
		}
		if len(p.Enum) > 0 {
			sv, _ := v.(string)
			found := false
			for _, e := range p.Enum {
				found = found || e == sv
			}
			if !found {
				return errs.New(400, "params.enum", "name", name, "allowed", strings.Join(p.Enum, ", "))
			}
		}
	}
	return nil
}

// shellQuote quotes for POSIX sh only when needed.
func shellQuote(s string) string {
	if s != "" && strings.IndexFunc(s, func(r rune) bool {
		return !(r == '-' || r == '_' || r == '.' || r == '/' || r == ':' || r == '=' || r == '@' ||
			(r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z'))
	}) < 0 {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func EquivalentCommand(kindName string, params json.RawMessage) string {
	k, ok := Get(kindName)
	if !ok || k.CLI == "" {
		return ""
	}
	m := map[string]any{}
	_ = json.Unmarshal(params, &m)
	q := map[string]string{}
	var put func(key string, v any)
	put = func(key string, v any) {
		switch t := v.(type) {
		case string:
			q[key] = shellQuote(t)
		case []any: // a list of plain values becomes separate arguments
			parts := make([]string, 0, len(t))
			for _, e := range t {
				s, ok := e.(string)
				if !ok {
					b, _ := json.Marshal(t)
					q[key] = shellQuote(string(b))
					return
				}
				parts = append(parts, shellQuote(s))
			}
			q[key] = strings.Join(parts, " ")
		case bool: // empty when false, so {{if .flag}} works
			if t {
				q[key] = "true"
			} else {
				q[key] = ""
			}
		case map[string]any: // nested objects are also reachable as parent_child
			b, _ := json.Marshal(t)
			q[key] = shellQuote(string(b))
			for k, sub := range t {
				put(key+"_"+k, sub)
			}
		default:
			b, _ := json.Marshal(t)
			q[key] = shellQuote(string(b))
		}
	}
	for key, v := range m {
		put(key, v)
	}
	tpl, err := template.New("").Option("missingkey=zero").Parse(k.CLI)
	if err != nil {
		return ""
	}
	var b strings.Builder
	_ = tpl.Execute(&b, q)
	return strings.Join(strings.Fields(b.String()), " ")
}
