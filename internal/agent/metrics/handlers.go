package metrics

import (
	"context"
	"net/http"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/wangsirco/berth/internal/agent"
	"github.com/wangsirco/berth/internal/agent/docker"
)

// DiskUse is one entry of the disk "berth" view (方案 §5.1): a directory and its size.
type DiskUse struct {
	Path  string `json:"path"`
	Bytes int64  `json:"bytes"`
}

type duCache struct {
	mu      sync.Mutex
	at      time.Time
	items   []DiskUse
	running bool
}

// refresh runs `du -sxb` over the stacks and Docker's data in the background (it can take
// minutes on a busy disk) at most every 30 minutes.
func (c *duCache) get() ([]DiskUse, time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.running && time.Since(c.at) > 30*time.Minute {
		c.running = true
		go func() {
			items := du()
			c.mu.Lock()
			c.items, c.at, c.running = items, time.Now(), false
			c.mu.Unlock()
		}()
	}
	return c.items, c.at
}

func du() []DiskUse {
	paths, _ := filepath.Glob(filepath.Join(docker.StacksDir, "*"))
	paths = append(paths, "/var/lib/docker", "/var/lib/berth", "/srv/berth/backups")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	var out []DiskUse
	for _, p := range paths {
		b, err := exec.CommandContext(ctx, "nice", "-n", "19", "du", "-sxb", p).Output()
		if err != nil && len(b) == 0 {
			continue
		}
		f := strings.Fields(string(b))
		if len(f) < 1 {
			continue
		}
		n, _ := strconv.ParseInt(f[0], 10, 64)
		out = append(out, DiskUse{Path: p, Bytes: n})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Bytes > out[j].Bytes })
	return out
}

func Register(s *agent.Server, smp *Sampler, dc *docker.Client) {
	var cache duCache
	s.Handle("GET /v1/metrics/host", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		agent.WriteJSON(w, 200, smp.Host())
	})
	s.Handle("GET /v1/metrics/berths", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		var running []ContainerRef
		if dc != nil {
			ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
			list, err := dc.Containers(ctx)
			cancel()
			if err == nil {
				for _, c := range list {
					if c.State == "running" {
						running = append(running, ContainerRef{ID: c.ID, Name: c.Name, Project: c.Project})
					}
				}
			}
		}
		agent.WriteJSON(w, 200, Berths(smp.Host(), running))
	})
	s.Handle("GET /v1/metrics/disk-berths", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		items, at := cache.get()
		if items == nil {
			items = []DiskUse{}
		}
		agent.WriteJSON(w, 200, map[string]any{"disks": smp.Host().Disks, "dirs": items, "measured_at": at.Unix()})
	})
}
