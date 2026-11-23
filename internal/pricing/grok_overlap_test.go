package pricing

import (
	"math/big"
	"testing"
)

// TestGrok47LiveRequestOverlapAtBaseRates locks the live grok-4.7 api_request
// cost to the agent-reported 14802 micro-USD. The bundled x-ai/grok-4.7 row
// has pricing.overrides, so Price() reports unknown; this test multiplies the
// overlap buckets by the row's base rates directly:
// (14454-10112)*0.000002 + 10112*0.0000005 + (177-121)*0.000006 + 121*0.000006
// = 0.008684 + 0.005056 + 0.000336 + 0.000726 = 0.014802.
func TestGrok47LiveRequestOverlapAtBaseRates(t *testing.T) {
	cat := Bundled()
	if cat == nil {
		t.Fatal("no bundled catalog")
	}
	model, ok := cat.Models["x-ai/grok-4.7"]
	if !ok {
		t.Fatal("x-ai/grok-4.7 missing from bundled catalog")
	}
	if !model.HasOverrides {
		t.Fatal("expected x-ai/grok-4.7 to carry pricing.overrides; revisit this test if that changed")
	}
	values := map[string]*big.Rat{
		"input":      big.NewRat(14454, 1),
		"output":     big.NewRat(177, 1),
		"reasoning":  big.NewRat(121, 1),
		"cache_read": big.NewRat(10112, 1),
	}
	buckets, _, reason := overlapBuckets(values, "grok_code overlap")
	if reason != "" {
		t.Fatalf("overlap refused: %s", reason)
	}
	total := new(big.Rat)
	for _, b := range buckets {
		if b.tokens.Sign() == 0 {
			continue
		}
		rate, _, _, why := rateFor(model, b)
		if why != "" {
			t.Fatalf("bucket %s: %s", b.name, why)
		}
		total.Add(total, new(big.Rat).Mul(b.tokens, rate))
	}
	got, exact := decimalString(total)
	if !exact || got != "0.014802" {
		t.Fatalf("grok-4.7 overlap cost = %s (exact=%v), want 0.014802", got, exact)
	}
}
