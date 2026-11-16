package accesslog

import (
	"fmt"
	"math"
	"testing"
)

func TestHLLError(t *testing.T) {
	for _, n := range []int{10, 1000, 50000, 200000} {
		h := NewHLL()
		for i := 0; i < n; i++ {
			h.Add(fmt.Sprintf("203.0.%d.%d|ua", i/256, i))
		}
		got := h.Count()
		if e := math.Abs(float64(got)-float64(n)) / float64(n); e > 0.05 {
			t.Errorf("n=%d got %d (%.1f%%)", n, got, e*100)
		}
	}
	a, b := NewHLL(), NewHLL()
	for i := 0; i < 5000; i++ {
		a.Add(fmt.Sprint("x", i))
		b.Add(fmt.Sprint("x", i+2500))
	}
	a.Merge(Unpack(b.Pack()))
	if c := a.Count(); c < 7200 || c > 7800 {
		t.Fatalf("merged %d, want ~7500", c)
	}
	if len(NewHLL().Pack()) > 64 {
		t.Fatal("empty registers should pack small")
	}
}
