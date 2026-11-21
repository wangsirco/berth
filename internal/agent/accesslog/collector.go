package accesslog

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/wangsirco/berth/internal/sign"
)

// rotatedRe: lumberjack (Caddy's log writer) renames full files to
// <name>-<timestamp>.log and may gzip them.
var rotatedRe = regexp.MustCompile(`-\d{4}-\d{2}-\d{2}T\d{2}-\d{2}-\d{2}(\.\d+)?\.log(\.gz)?$`)

const (
	keepDone  = 6 * time.Hour // closed minutes waiting for the panel
	lateGrace = 90 * time.Second
	maxRead   = 64 << 20 // per file per poll, so a huge backlog cannot stall the agent
)

type fileState struct {
	Ino uint64 `json:"ino"`
	Off int64  `json:"off"`
}

// Batch is what the panel collects: closed minutes since its last acknowledgement.
type Batch struct {
	Boot    string    `json:"boot"`
	Seq     int64     `json:"seq"` // the newest minute's sequence number; acknowledge with it
	Minutes []*Minute `json:"minutes"`
}

type doneMinute struct {
	seq int64
	m   *Minute
}

type Collector struct {
	Dir       string
	StatePath string
	Country   Country
	Now       func() time.Time

	mu    sync.Mutex
	boot  string
	files map[string]*fileState // by site
	open  map[string]map[int64]*Minute
	done  []doneMinute
	seq   int64
	seen  *seenSet
	saved time.Time
}

func NewCollector(dir, statePath string, country Country) *Collector {
	c := &Collector{Dir: dir, StatePath: statePath, Country: country, Now: time.Now, boot: sign.RandomHex(6),
		files: map[string]*fileState{}, open: map[string]map[int64]*Minute{}}
	if b, err := os.ReadFile(statePath); err == nil {
		json.Unmarshal(b, &c.files)
	}
	c.seen = loadSeen(statePath + ".visitors")
	return c
}

func inode(fi os.FileInfo) uint64 {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return uint64(st.Ino)
	}
	return 0
}

// Poll reads what was appended since the last poll, following rotations.
func (c *Collector) Poll() {
	c.mu.Lock()
	defer c.mu.Unlock()
	entries, err := os.ReadDir(c.Dir)
	if err != nil {
		return
	}
	changed := false
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".log") || rotatedRe.MatchString(name) {
			continue
		}
		site := strings.TrimSuffix(name, ".log")
		path := filepath.Join(c.Dir, name)
		fi, err := os.Stat(path)
		if err != nil {
			continue
		}
		ino := inode(fi)
		st := c.files[site]
		if st == nil {
			st = &fileState{Ino: ino}
			c.files[site] = st
			changed = true
		}
		if st.Ino != ino {
			// rotated: finish the old file first, then start the new one from the top
			if old := c.findInode(entries, site, st.Ino); old != "" {
				c.readFrom(site, old, st)
			}
			st.Ino, st.Off = ino, 0
			changed = true
		}
		if fi.Size() < st.Off { // truncated
			st.Off = 0
		}
		if fi.Size() > st.Off {
			c.readFrom(site, path, st)
			changed = true
		}
	}
	c.closeMinutes()
	if c.seen != nil && c.Now().Sub(c.saved) > 5*time.Minute {
		c.seen.save(c.StatePath + ".visitors")
		c.saved = c.Now()
	}
	if changed {
		if b, err := json.Marshal(c.files); err == nil {
			tmp := c.StatePath + ".tmp"
			if os.WriteFile(tmp, b, 0o600) == nil {
				os.Rename(tmp, c.StatePath)
			}
		}
	}
}

func (c *Collector) findInode(entries []os.DirEntry, site string, ino uint64) string {
	for _, e := range entries {
		n := e.Name()
		if !strings.HasPrefix(n, site+"-") || !rotatedRe.MatchString(n) || strings.HasSuffix(n, ".gz") {
			continue
		}
		p := filepath.Join(c.Dir, n)
		if fi, err := os.Stat(p); err == nil && inode(fi) == ino {
			return p
		}
	}
	return ""
}

// readFrom consumes complete lines from st.Off.
func (c *Collector) readFrom(site, path string, st *fileState) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	if _, err := f.Seek(st.Off, io.SeekStart); err != nil {
		return
	}
	r := bufio.NewReaderSize(io.LimitReader(f, maxRead), 256<<10)
	for {
		line, err := r.ReadBytes('\n')
		if err != nil { // a partial last line waits for the next poll
			return
		}
		st.Off += int64(len(line))
		if e, ok := parse(line); ok {
			c.minute(site, minuteOf(e.TS)).add(e, c.Country, c.seen)
		}
	}
}

func (c *Collector) minute(site string, ts int64) *Minute {
	ms := c.open[site]
	if ms == nil {
		ms = map[int64]*Minute{}
		c.open[site] = ms
	}
	m := ms[ts]
	if m == nil {
		m = newMinute(site, ts)
		ms[ts] = m
	}
	return m
}

// closeMinutes hands minutes that can no longer grow to the panel queue.
func (c *Collector) closeMinutes() {
	now := c.Now()
	cut := now.Add(-lateGrace).Unix()
	var closed []*Minute
	for site, ms := range c.open {
		for ts, m := range ms {
			if ts+60 <= cut {
				m.close()
				closed = append(closed, m)
				delete(ms, ts)
			}
		}
		if len(ms) == 0 {
			delete(c.open, site)
		}
	}
	sort.Slice(closed, func(i, j int) bool {
		return closed[i].TS < closed[j].TS || (closed[i].TS == closed[j].TS && closed[i].Site < closed[j].Site)
	})
	for _, m := range closed {
		c.seq++
		c.done = append(c.done, doneMinute{c.seq, m})
	}
	old := now.Add(-keepDone).Unix()
	i := 0
	for i < len(c.done) && c.done[i].m.TS < old {
		i++
	}
	c.done = c.done[i:]
}

// Take drops what the panel acknowledged (ack is only valid for the same boot) and
// returns the rest.
func (c *Collector) Take(boot string, ack int64) Batch {
	c.mu.Lock()
	defer c.mu.Unlock()
	if boot == c.boot {
		i := 0
		for i < len(c.done) && c.done[i].seq <= ack {
			i++
		}
		c.done = c.done[i:]
	}
	b := Batch{Boot: c.boot, Minutes: []*Minute{}}
	for _, d := range c.done {
		b.Minutes = append(b.Minutes, d.m)
		b.Seq = d.seq
	}
	if len(b.Minutes) > 600 { // send in pieces
		b.Minutes = b.Minutes[:600]
		b.Seq = c.done[599].seq
	}
	return b
}

// Run polls every interval until stop is closed.
func (c *Collector) Run(every time.Duration, stop <-chan struct{}) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		c.Poll()
		select {
		case <-stop:
			return
		case <-t.C:
		}
	}
}
