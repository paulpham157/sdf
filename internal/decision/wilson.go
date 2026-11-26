package decision

import "math"

// wilsonZ is the 95% standard-normal quantile.
const wilsonZ = 1.959963984540054

// WilsonInterval is a 95% Wilson score interval on [0, 1].
type WilsonInterval struct {
	Low  float64 `json:"low"`
	High float64 `json:"high"`
}

// Wilson95 is the 95% Wilson score interval for k successes in n trials.
// ok is false when n is 0 or k is outside 0..n. Zero successes pin Low
// at 0, and n successes pin High at 1.
func Wilson95(k, n int) (WilsonInterval, bool) {
	if n <= 0 || k < 0 || k > n {
		return WilsonInterval{}, false
	}
	z := wilsonZ
	nf := float64(n)
	p := float64(k) / nf
	z2 := z * z
	denom := 1 + z2/nf
	center := (p + z2/(2*nf)) / denom
	margin := z * math.Sqrt(p*(1-p)/nf+z2/(4*nf*nf)) / denom
	low := center - margin
	high := center + margin
	if k == 0 || low < 0 {
		low = 0
	}
	if k == n || high > 1 {
		high = 1
	}
	return WilsonInterval{Low: low, High: high}, true
}
