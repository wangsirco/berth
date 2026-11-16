package accesslog

import "sort"

// Merge adds o into m (same site; any period). Unique visitors are merged through the
// HLL registers when both carry them.
func (m *Minute) Merge(o *Minute) {
	if m.Status == nil {
		m.Status = map[string]int64{}
	}
	if len(m.Lat) != len(LatencyBounds)+1 {
		m.Lat = make([]int64, len(LatencyBounds)+1)
	}
	m.Req += o.Req
	m.Bytes += o.Bytes
	m.Scans += o.Scans
	m.Bots += o.Bots
	for k, v := range o.Status {
		m.Status[k] += v
	}
	for i := range o.Lat {
		if i < len(m.Lat) {
			m.Lat[i] += o.Lat[i]
		}
	}
	m.Countries = addMap(m.Countries, o.Countries)
	m.Paths = Top(addMap(m.Paths, o.Paths), 200)
	m.Refs = Top(addMap(m.Refs, o.Refs), 200)
	m.ScanPaths = Top(addMap(m.ScanPaths, o.ScanPaths), 100)
	m.Slow = append(m.Slow, o.Slow...)
	if len(m.Slow) > 20 {
		sort.Slice(m.Slow, func(i, j int) bool { return m.Slow[i].TS > m.Slow[j].TS })
		m.Slow = m.Slow[:20]
	}
	if len(o.HLL) > 0 {
		h := NewHLL()
		if len(m.HLL) > 0 {
			h = Unpack(m.HLL)
		}
		h.Merge(Unpack(o.HLL))
		m.HLL = h.Pack()
		m.Uniq = h.Count()
	} else {
		m.Uniq += o.Uniq
	}
}

func addMap(a, b map[string]int64) map[string]int64 {
	if a == nil {
		a = map[string]int64{}
	}
	for k, v := range b {
		a[k] += v
	}
	return a
}

// Percentile estimates the q-th latency percentile (ms) from the buckets: the upper
// bound of the bucket it falls in (the open bucket reports twice the last bound).
func Percentile(lat []int64, q float64) int64 {
	var n int64
	for _, v := range lat {
		n += v
	}
	if n == 0 {
		return 0
	}
	want := int64(float64(n)*q + 0.5)
	var acc int64
	for i, v := range lat {
		acc += v
		if acc >= want {
			if i < len(LatencyBounds) {
				return int64(LatencyBounds[i])
			}
			return int64(LatencyBounds[len(LatencyBounds)-1] * 2)
		}
	}
	return 0
}
