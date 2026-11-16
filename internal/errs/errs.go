// Package errs carries translatable errors: a stable code (i18n key) plus params.
// The panel translates Code with Accept-Language; CLI and MCP show the translated message.
package errs

import "errors"

type E struct {
	Status int            `json:"-"`
	Code   string         `json:"code"`
	Params map[string]any `json:"params,omitempty"`
}

func (e *E) Error() string { return e.Code }

// New builds an error; kv are alternating param names and values.
func New(status int, code string, kv ...any) *E {
	e := &E{Status: status, Code: code, Params: map[string]any{}}
	for i := 0; i+1 < len(kv); i += 2 {
		if k, ok := kv[i].(string); ok {
			e.Params[k] = kv[i+1]
		}
	}
	return e
}

// From converts any error into *E; unknown errors become 500 "internal".
func From(err error) *E {
	var e *E
	if errors.As(err, &e) {
		return e
	}
	return New(500, "internal", "detail", err.Error())
}
