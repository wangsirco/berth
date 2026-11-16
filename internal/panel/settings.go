package panel

import (
	"strconv"
	"strings"
)

func (a *App) loadSettings() {
	m := map[string]string{}
	rows, err := a.db.Query(`SELECT key, value FROM settings`)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var k, v string
			if rows.Scan(&k, &v) == nil {
				m[k] = v
			}
		}
	}
	a.mu.Lock()
	a.settings = m
	a.mu.Unlock()
}

func (a *App) setting(key string) string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.settings[key]
}

func (a *App) settingInt(key string, def int) int {
	if n, err := strconv.Atoi(a.setting(key)); err == nil {
		return n
	}
	return def
}

func (a *App) setSetting(key, value string) error {
	if _, err := a.db.Exec(`INSERT INTO settings(key, value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value); err != nil {
		return err
	}
	a.mu.Lock()
	a.settings[key] = value
	if strings.HasPrefix(key, "access.") {
		a.allow = nil // recomputed on next request
	}
	a.mu.Unlock()
	return nil
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == '\n' || r == ' ' }) {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
