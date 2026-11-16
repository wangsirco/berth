package accesslog

// Tail: the last lines of one site's access log, parsed for the site's log view.

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
)

type LogLine struct {
	TS     float64 `json:"ts"`
	IP     string  `json:"ip"`
	Method string  `json:"method"`
	URI    string  `json:"uri"`
	Status int     `json:"status"`
	Size   int64   `json:"size"`
	MS     int     `json:"ms"`
	UA     string  `json:"ua,omitempty"`
	Ref    string  `json:"referer,omitempty"`
}

// Tail returns up to n entries, newest first, optionally only those whose URI, IP or status
// contains q.
func Tail(logsDir, domain string, n int, q string) ([]LogLine, error) {
	if domain == "" || strings.ContainsAny(domain, "/\\") || strings.HasPrefix(domain, ".") {
		return nil, errors.New("bad domain")
	}
	f, err := os.Open(filepath.Join(logsDir, domain+".log"))
	if err != nil {
		return []LogLine{}, nil
	}
	defer f.Close()
	fi, _ := f.Stat()
	start := fi.Size() - 4<<20 // the last 4 MB is plenty for a view
	if start < 0 {
		start = 0
	}
	f.Seek(start, io.SeekStart)
	b, _ := io.ReadAll(f)
	lines := bytes.Split(b, []byte("\n"))
	out := []LogLine{}
	for i := len(lines) - 1; i >= 0 && len(out) < n; i-- {
		e, ok := parse(lines[i])
		if !ok {
			continue
		}
		l := LogLine{TS: e.TS, IP: firstOf(e.Request.ClientIP, e.Request.RemoteIP), Method: e.Request.Method, URI: e.Request.URI, Status: e.Status, Size: e.Size, MS: int(e.Duration * 1000)}
		if ua := e.Request.Headers["User-Agent"]; len(ua) > 0 {
			l.UA = ua[0]
		}
		if rf := e.Request.Headers["Referer"]; len(rf) > 0 {
			l.Ref = rf[0]
		}
		if q != "" && !strings.Contains(l.URI, q) && !strings.Contains(l.IP, q) && !strings.Contains(strings.TrimSpace(itoa(l.Status)), q) {
			continue
		}
		out = append(out, l)
	}
	return out, nil
}

func firstOf(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	s := ""
	for n > 0 {
		s = string(rune('0'+n%10)) + s
		n /= 10
	}
	return s
}
