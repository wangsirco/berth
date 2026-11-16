package pipeline

import (
	"strconv"
	"strings"
	"time"
)

// Cron is a five-field expression: minute hour day-of-month month day-of-week, with
// *, lists (1,5), ranges (1-5), steps (*/15, 0-30/10) and the presets @hourly, @daily,
// @weekly, @monthly.
type Cron struct{ fields [5]map[int]bool }

var cronPresets = map[string]string{"@hourly": "0 * * * *", "@daily": "0 3 * * *", "@weekly": "0 3 * * 1", "@monthly": "0 3 1 * *"}

var cronBounds = [5][2]int{{0, 59}, {0, 23}, {1, 31}, {1, 12}, {0, 7}} // day-of-week 7 = Sunday

func ParseCron(expr string) (*Cron, error) {
	expr = strings.TrimSpace(expr)
	if p, ok := cronPresets[expr]; ok {
		expr = p
	}
	parts := strings.Fields(expr)
	if len(parts) != 5 {
		return nil, bad("pipeline.bad_cron", "value", expr)
	}
	var c Cron
	for i, p := range parts {
		set := map[int]bool{}
		lo, hi := cronBounds[i][0], cronBounds[i][1]
		for _, item := range strings.Split(p, ",") {
			step := 1
			if a, b, ok := strings.Cut(item, "/"); ok {
				n, err := strconv.Atoi(b)
				if err != nil || n < 1 {
					return nil, bad("pipeline.bad_cron", "value", expr)
				}
				item, step = a, n
			}
			from, to := lo, hi
			if item != "*" {
				a, b, isRange := strings.Cut(item, "-")
				x, err := strconv.Atoi(a)
				if err != nil {
					return nil, bad("pipeline.bad_cron", "value", expr)
				}
				from, to = x, x
				if isRange {
					if to, err = strconv.Atoi(b); err != nil {
						return nil, bad("pipeline.bad_cron", "value", expr)
					}
				} else if step > 1 {
					to = hi
				}
			}
			if from < lo || to > hi || from > to {
				return nil, bad("pipeline.bad_cron", "value", expr)
			}
			for v := from; v <= to; v += step {
				if i == 4 {
					v := v % 7
					set[v] = true
					continue
				}
				set[v] = true
			}
		}
		c.fields[i] = set
	}
	return &c, nil
}

// Match reports whether t (to the minute) is a scheduled time.
func (c *Cron) Match(t time.Time) bool {
	return c.fields[0][t.Minute()] && c.fields[1][t.Hour()] && c.fields[2][t.Day()] && c.fields[3][int(t.Month())] && c.fields[4][int(t.Weekday())]
}

// Next is the first scheduled minute after t (zero if none within about four years).
func (c *Cron) Next(t time.Time) time.Time {
	t = t.Truncate(time.Minute).Add(time.Minute)
	end := t.AddDate(4, 0, 0)
	for t.Before(end) {
		if !c.fields[3][int(t.Month())] {
			t = time.Date(t.Year(), t.Month()+1, 1, 0, 0, 0, 0, t.Location())
			continue
		}
		if !c.fields[2][t.Day()] || !c.fields[4][int(t.Weekday())] {
			t = time.Date(t.Year(), t.Month(), t.Day()+1, 0, 0, 0, 0, t.Location())
			continue
		}
		if !c.fields[1][t.Hour()] {
			t = time.Date(t.Year(), t.Month(), t.Day(), t.Hour()+1, 0, 0, 0, t.Location())
			continue
		}
		if c.fields[0][t.Minute()] {
			return t
		}
		t = t.Add(time.Minute)
	}
	return time.Time{}
}
