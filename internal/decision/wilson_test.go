package decision

import (
	"strconv"
	"testing"
)

func TestWilson95Bounds(t *testing.T) {
	if _, ok := Wilson95(0, 0); ok {
		t.Fatal("empty interval")
	}
	if _, ok := Wilson95(-1, 2); ok {
		t.Fatal("negative successes")
	}
	zero, ok := Wilson95(0, 7)
	if !ok || zero.Low != 0 || zero.High <= 0 || zero.High >= 1 {
		t.Fatalf("%+v", zero)
	}
	half, ok := Wilson95(1, 2)
	if !ok || half.Low >= 0.5 || half.High <= 0.5 || half.Low < 0 || half.High > 1 {
		t.Fatalf("%+v", half)
	}
	all, ok := Wilson95(6, 6)
	if !ok || all.High != 1 || all.Low <= 0 || all.Low >= 1 {
		t.Fatalf("%+v", all)
	}
	// 1/2 is the classic check that the interval is not the Wald interval.
	if half.Low > 0.1 || half.High < 0.9 {
		t.Fatalf("1/2 interval too narrow: %+v", half)
	}
}

func TestWilson95DocumentedBaseline(t *testing.T) {
	// High bounds quoted in docs/recall.md and the PR body for the
	// skill-only snapshot (0 hits).
	cases := []struct {
		k, n int
		high string
	}{
		{0, 1, "0.793451"},
		{0, 2, "0.657620"},
		{0, 4, "0.489891"},
		{0, 6, "0.390334"},
		{0, 7, "0.354330"},
	}
	for _, tc := range cases {
		w, ok := Wilson95(tc.k, tc.n)
		if !ok || w.Low != 0 || strconv.FormatFloat(w.High, 'f', 6, 64) != tc.high {
			t.Fatalf("%d/%d: %+v want high %s", tc.k, tc.n, w, tc.high)
		}
	}
}
