package decision

import "testing"

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
