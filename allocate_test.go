package planadjust

import (
	"math/rand"
	"testing"
)

func sum(vs []int64) int64 {
	var s int64
	for _, v := range vs {
		s += v
	}
	return s
}

func TestAllocateExactSum(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	for trial := 0; trial < 2000; trial++ {
		n := 1 + rng.Intn(50)
		w := make([]int64, n)
		for i := range w {
			w[i] = int64(rng.Intn(1000))
		}
		total := int64(rng.Intn(100000))
		out := allocate(total, w, nil)
		if sum(out) != total {
			t.Fatalf("sum=%d want %d (weights=%v)", sum(out), total, w)
		}
		var W int64
		for _, x := range w {
			W += x
		}
		for i, o := range out {
			if o < 0 {
				t.Fatalf("negative cell")
			}
			if W > 0 {
				// 每格必须是精确份额的 floor 或 ceil
				lo := total * w[i] / W
				if o != lo && o != lo+1 {
					t.Fatalf("cell %d: got %d, share in [%d,%d]", i, o, lo, lo+1)
				}
			}
		}
	}
}

func TestAllocateZeroWeightsAverage(t *testing.T) {
	out := allocate(10, []int64{0, 0, 0}, nil)
	if sum(out) != 10 {
		t.Fatalf("sum=%d want 10", sum(out))
	}
	for _, v := range out {
		if v != 3 && v != 4 {
			t.Fatalf("not averaged: %v", out)
		}
	}
	out = allocate(0, []int64{0, 0}, nil)
	if sum(out) != 0 {
		t.Fatalf("sum=%d want 0", sum(out))
	}
}

func TestDeallocateExactAndNonNegative(t *testing.T) {
	rng := rand.New(rand.NewSource(2))
	for trial := 0; trial < 2000; trial++ {
		n := 1 + rng.Intn(50)
		vs := make([]int64, n)
		for i := range vs {
			vs[i] = int64(rng.Intn(1000))
		}
		total := sum(vs)
		amount := int64(rng.Intn(int(total) + 1))
		before := sum(vs)
		deallocate(amount, vs)
		if sum(vs) != before-amount {
			t.Fatalf("sum=%d want %d", sum(vs), before-amount)
		}
		for _, v := range vs {
			if v < 0 {
				t.Fatalf("negative after deallocate")
			}
		}
	}
}
