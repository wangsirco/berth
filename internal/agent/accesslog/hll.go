package accesslog

import (
	"bytes"
	"compress/flate"
	"hash/fnv"
	"io"
	"math"
	"math/bits"
)

// HLL is a HyperLogLog counter with 2^12 registers: about 1.6% standard error in 4 KB.
type HLL struct {
	R []byte `json:"r"`
}

const hllP = 12
const hllM = 1 << hllP

func NewHLL() *HLL { return &HLL{R: make([]byte, hllM)} }

func hash64(s string) uint64 {
	h := fnv.New64a()
	h.Write([]byte(s))
	x := h.Sum64()
	// fnv's low bits are weak; finish with a mixer (splitmix64)
	x ^= x >> 30
	x *= 0xbf58476d1ce4e5b9
	x ^= x >> 27
	x *= 0x94d049bb133111eb
	x ^= x >> 31
	return x
}

func (h *HLL) Add(s string) {
	x := hash64(s)
	idx := x >> (64 - hllP)
	rank := byte(bits.LeadingZeros64(x<<hllP|1<<(hllP-1)) + 1)
	if rank > h.R[idx] {
		h.R[idx] = rank
	}
}

func (h *HLL) Merge(o *HLL) {
	if o == nil || len(o.R) != hllM {
		return
	}
	for i, v := range o.R {
		if v > h.R[i] {
			h.R[i] = v
		}
	}
}

func (h *HLL) Count() int64 {
	sum, zeros := 0.0, 0
	for _, v := range h.R {
		sum += 1 / math.Pow(2, float64(v))
		if v == 0 {
			zeros++
		}
	}
	m := float64(hllM)
	est := 0.7213 / (1 + 1.079/m) * m * m / sum
	if est <= 2.5*m && zeros > 0 { // small range: linear counting
		est = m * math.Log(m/float64(zeros))
	}
	return int64(est + 0.5)
}

// Pack compresses the registers (mostly zeros for small sites).
func (h *HLL) Pack() []byte {
	var b bytes.Buffer
	w, _ := flate.NewWriter(&b, flate.BestCompression)
	w.Write(h.R)
	w.Close()
	return b.Bytes()
}

func Unpack(b []byte) *HLL {
	r, err := io.ReadAll(io.LimitReader(flate.NewReader(bytes.NewReader(b)), hllM+1))
	if err != nil || len(r) != hllM {
		return NewHLL()
	}
	return &HLL{R: r}
}
