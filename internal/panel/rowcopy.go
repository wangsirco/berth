package panel

// Rows that travel in a migration pack (R6). Columns ending in _enc are sealed with this
// panel's key; they leave opened (the pack itself is sealed with the user's password) and are
// sealed again with the receiving panel's key.

import (
	"encoding/base64"
	"fmt"
	"regexp"
	"strings"
)

type row map[string]any

var identRe = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)

// rowsOf reads rows as column → value, opening sealed columns.
func (a *App) rowsOf(table, where string, args ...any) ([]row, error) {
	if !identRe.MatchString(table) {
		return nil, fmt.Errorf("bad table %q", table)
	}
	rs, err := a.db.Query(`SELECT * FROM `+table+` WHERE `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rs.Close()
	cols, _ := rs.Columns()
	out := []row{}
	for rs.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rs.Scan(ptrs...); err != nil {
			return nil, err
		}
		r := row{}
		for i, c := range cols {
			v := vals[i]
			if b, ok := v.([]byte); ok {
				if strings.HasSuffix(c, "_enc") {
					if len(b) == 0 {
						r[c] = nil
						continue
					}
					plain, err := a.open(b)
					if err != nil {
						r[c] = nil // sealed with a key we no longer have: nothing to carry
						continue
					}
					r[c] = map[string]string{"$plain": base64.StdEncoding.EncodeToString(plain)}
					continue
				}
				v = string(b)
			}
			r[c] = v
		}
		out = append(out, r)
	}
	return out, rs.Err()
}

// insertRow writes a row from a pack, sealing opened columns with this panel's key. Columns in
// drop are left to their defaults (autoincrement ids).
func (a *App) insertRow(table string, r row, set map[string]any, drop ...string) (int64, error) {
	if !identRe.MatchString(table) {
		return 0, fmt.Errorf("bad table %q", table)
	}
	skip := map[string]bool{}
	for _, d := range drop {
		skip[d] = true
	}
	var cols []string
	var vals []any
	add := func(c string, v any) error {
		if !identRe.MatchString(c) {
			return fmt.Errorf("bad column %q", c)
		}
		if m, ok := v.(map[string]any); ok {
			if p, ok := m["$plain"].(string); ok {
				plain, err := base64.StdEncoding.DecodeString(p)
				if err != nil {
					return err
				}
				enc, err := a.seal(plain)
				if err != nil {
					return err
				}
				v = enc
			}
		}
		if f, ok := v.(float64); ok && f == float64(int64(f)) { // JSON numbers back to integers
			v = int64(f)
		}
		cols, vals = append(cols, c), append(vals, v)
		return nil
	}
	for c, v := range r {
		if skip[c] {
			continue
		}
		if _, over := set[c]; over {
			continue
		}
		if err := add(c, v); err != nil {
			return 0, err
		}
	}
	for c, v := range set {
		if err := add(c, v); err != nil {
			return 0, err
		}
	}
	q := `INSERT INTO ` + table + `(` + strings.Join(cols, ",") + `) VALUES(` + strings.TrimSuffix(strings.Repeat("?,", len(cols)), ",") + `)`
	res, err := a.db.Exec(q, vals...)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// plainOf returns an opened column's bytes.
func plainOf(v any) []byte {
	if m, ok := v.(map[string]any); ok {
		if p, ok := m["$plain"].(string); ok {
			b, _ := base64.StdEncoding.DecodeString(p)
			return b
		}
	}
	if m, ok := v.(map[string]string); ok {
		b, _ := base64.StdEncoding.DecodeString(m["$plain"])
		return b
	}
	return nil
}
