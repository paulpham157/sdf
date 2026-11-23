package pricing

import (
	"strings"
	"testing"
)

func TestGrok47LiveRequestPricesAtBaseRates(t *testing.T) {
	cat := Bundled()
	model := cat.Models["x-ai/grok-4.7"]
	if model == nil || !model.HasOverrides || len(model.Overrides) != 1 || model.Overrides[0].MinPromptTokens == nil || *model.Overrides[0].MinPromptTokens != 200000 {
		t.Fatalf("unexpected grok-4.7 overrides: %+v", model)
	}
	tokens := map[string]float64{
		"input_tokens": 14454, "output_tokens": 177,
		"reasoning_tokens": 121, "cache_read_tokens": 10112,
	}
	got := Price(cat, Usage{
		Models:   []string{"x-ai/grok-4.7"},
		Source:   "logs grok_code.api_request",
		Tokens:   tokens,
		Requests: []map[string]float64{tokens},
	})
	// (14454-10112)*0.000002 + 10112*0.0000005 + (177-121)*0.000006 + 121*0.000006 = 0.014802
	if got.Status != "known" || got.USD != "0.014802" {
		t.Fatalf("%+v", got)
	}
	if !strings.Contains(got.Note, "base rates") {
		t.Fatalf("note %s", got.Note)
	}
}

func TestGrok47SessionTotalBelowThresholdUsesBaseRates(t *testing.T) {
	cat := Bundled()
	got := Price(cat, Usage{
		Models: []string{"x-ai/grok-4.7"},
		Source: "metrics grok_code.token.usage",
		Tokens: map[string]float64{
			"input": 64649, "output": 384, "reasoning": 167, "cache_read": 51072,
		},
	})
	// (64649-51072)*0.000002 + 51072*0.0000005 + (384-167)*0.000006 + 167*0.000006 = 0.054994
	if got.Status != "known" || got.USD != "0.054994" {
		t.Fatalf("%+v", got)
	}
	if !strings.Contains(got.Note, "base rates") || !strings.Contains(got.Note, "200000") {
		t.Fatalf("note %s", got.Note)
	}
}

func TestGrok47RequestAtThresholdUsesOverrideRates(t *testing.T) {
	cat := Bundled()
	tokens := map[string]float64{
		"input_tokens": 200000, "output_tokens": 10,
		"reasoning_tokens": 4, "cache_read_tokens": 1000,
	}
	got := Price(cat, Usage{
		Models:   []string{"grok-4.7"},
		Source:   "logs grok_code.api_request",
		Tokens:   tokens,
		Requests: []map[string]float64{tokens},
	})
	// (200000-1000)*0.000004 + 1000*0.000001 + (10-4)*0.000012 + 4*0.000012
	// = 0.796 + 0.001 + 0.000072 + 0.000048 = 0.79712
	if got.Status != "known" || got.USD != "0.79712" {
		t.Fatalf("%+v", got)
	}
	if !strings.Contains(got.Note, "override rates") || !strings.Contains(got.Note, "200000") {
		t.Fatalf("note %s", got.Note)
	}
}

func TestAggregateAtOrAboveThresholdStaysUnknown(t *testing.T) {
	cat := Bundled()
	got := Price(cat, Usage{
		Models: []string{"x-ai/grok-4.6"},
		Source: "metrics grok_code.token.usage",
		Tokens: map[string]float64{"input": 200000, "output": 1},
	})
	if got.Status != "unknown" || got.USD != "" || !strings.Contains(got.Reason, "overrides") {
		t.Fatalf("%+v", got)
	}
	if !strings.Contains(got.Reason, "not one request") || !strings.Contains(got.Reason, "200000") {
		t.Fatalf("reason %s", got.Reason)
	}
}

func TestPerRequestTiersArePricedAndSummed(t *testing.T) {
	cat := mustFixture(t, &Model{
		ID:           "vendor/tiered",
		HasOverrides: true,
		Prices:       map[string]string{"prompt": "0.000001", "completion": "0.000002"},
		Overrides: []Override{{
			MinPromptTokens: int64Ptr(100),
			Prices:          map[string]string{"prompt": "0.000003", "completion": "0.000004"},
		}},
	})
	low := map[string]float64{"input_tokens": 10, "output_tokens": 2}
	high := map[string]float64{"input_tokens": 100, "output_tokens": 2}
	got := Price(cat, Usage{
		Models:   []string{"vendor/tiered"},
		Source:   "traces chat",
		Tokens:   map[string]float64{"input_tokens": 110, "output_tokens": 4},
		Requests: []map[string]float64{low, high},
	})
	// low: 10*0.000001 + 2*0.000002 = 0.000014
	// high: 100*0.000003 + 2*0.000004 = 0.000308
	// sum = 0.000322
	if got.Status != "known" || got.USD != "0.000322" {
		t.Fatalf("%+v", got)
	}
	if !strings.Contains(got.Method, "sum of 2 per-request costs") {
		t.Fatalf("method %s", got.Method)
	}
}

func TestUnknownOverrideConditionIsUnknown(t *testing.T) {
	cat := mustFixture(t, &Model{
		ID:           "vendor/timed",
		HasOverrides: true,
		Prices:       map[string]string{"prompt": "1", "completion": "1"},
		Overrides: []Override{{
			UnknownConditions: []string{"utc_start", "utc_end"},
			Prices:            map[string]string{"prompt": "2", "completion": "2"},
		}},
	})
	got := Price(cat, Usage{
		Models: []string{"vendor/timed"},
		Source: "traces chat",
		Tokens: map[string]float64{"input_tokens": 1, "output_tokens": 1},
	})
	if got.Status != "unknown" || got.USD != "" || !strings.Contains(got.Reason, "utc_start") {
		t.Fatalf("%+v", got)
	}
	if !strings.Contains(got.Reason, "unrecognized condition") {
		t.Fatalf("reason %s", got.Reason)
	}
}

func TestBundledUTCOverridesStayUnknown(t *testing.T) {
	cat := Bundled()
	model := cat.Models["minimax/minimax-m3.1-flash"]
	if model == nil || !model.HasOverrides {
		t.Fatal("expected minimax/minimax-m3.1-flash to have overrides")
	}
	got := Price(cat, Usage{
		Models: []string{"minimax/minimax-m3.1-flash"},
		Source: "traces chat",
		Tokens: map[string]float64{"input_tokens": 1, "output_tokens": 1},
	})
	if got.Status != "known" {
		if got.Status != "unknown" || got.USD != "" || !strings.Contains(got.Reason, "unrecognized condition") {
			t.Fatalf("%+v", got)
		}
		return
	}
	t.Fatalf("utc overrides should stay unknown: %+v", got)
}

func TestMultiTierMinPromptTokens(t *testing.T) {
	cat := mustFixture(t, &Model{
		ID:           "vendor/multi",
		HasOverrides: true,
		Prices:       map[string]string{"prompt": "1", "completion": "2"},
		Overrides: []Override{
			{MinPromptTokens: int64Ptr(10), Prices: map[string]string{"prompt": "3", "completion": "4"}},
			{MinPromptTokens: int64Ptr(20), Prices: map[string]string{"prompt": "5", "completion": "6"}},
		},
	})
	below := Price(cat, Usage{
		Models: []string{"vendor/multi"}, Source: "traces chat",
		Tokens: map[string]float64{"input_tokens": 9, "output_tokens": 1},
	})
	if below.Status != "known" || below.USD != "11" { // 9*1 + 1*2
		t.Fatalf("below %+v", below)
	}
	mid := Price(cat, Usage{
		Models: []string{"vendor/multi"}, Source: "traces chat",
		Tokens:   map[string]float64{"input_tokens": 15, "output_tokens": 1},
		Requests: []map[string]float64{{"input_tokens": 15, "output_tokens": 1}},
	})
	if mid.Status != "known" || mid.USD != "49" { // 15*3 + 1*4
		t.Fatalf("mid %+v", mid)
	}
	high := Price(cat, Usage{
		Models: []string{"vendor/multi"}, Source: "traces chat",
		Tokens:   map[string]float64{"input_tokens": 20, "output_tokens": 1},
		Requests: []map[string]float64{{"input_tokens": 20, "output_tokens": 1}},
	})
	if high.Status != "known" || high.USD != "106" { // 20*5 + 1*6
		t.Fatalf("high %+v", high)
	}
}

func TestFetchParsesMinPromptTokensOverride(t *testing.T) {
	cat := Bundled()
	model := cat.Models["x-ai/grok-4.6"]
	if model == nil || !model.HasOverrides || len(model.Overrides) != 1 {
		t.Fatalf("%+v", model)
	}
	ov := model.Overrides[0]
	if ov.MinPromptTokens == nil || *ov.MinPromptTokens != 200000 {
		t.Fatalf("%+v", ov)
	}
	if ov.Prices["prompt"] != "0.000004" || ov.Prices["completion"] != "0.000012" || ov.Prices["input_cache_read"] != "0.000001" {
		t.Fatalf("%+v", ov.Prices)
	}
	if len(ov.UnknownConditions) != 0 {
		t.Fatalf("unknown %v", ov.UnknownConditions)
	}
}

func TestClaudeDisjointPromptIncludesCache(t *testing.T) {
	cat := mustFixture(t, &Model{
		ID:           "anthropic/with-threshold",
		HasOverrides: true,
		Prices: map[string]string{
			"prompt": "0.000001", "completion": "0.000002",
			"input_cache_read": "0.0000001",
		},
		Overrides: []Override{{
			MinPromptTokens: int64Ptr(100),
			Prices: map[string]string{
				"prompt": "0.000003", "completion": "0.000004",
				"input_cache_read": "0.0000003",
			},
		}},
	})
	// disjoint: prompt = input 40 + cache_read 60 = 100, so override applies
	got := Price(cat, Usage{
		Models: []string{"anthropic/with-threshold"},
		Source: "logs claude_code.api_request",
		Tokens: map[string]float64{"input_tokens": 40, "output_tokens": 2, "cache_read_tokens": 60},
		Requests: []map[string]float64{{
			"input_tokens": 40, "output_tokens": 2, "cache_read_tokens": 60,
		}},
	})
	// 40*0.000003 + 2*0.000004 + 60*0.0000003 = 0.00012 + 0.000008 + 0.000018 = 0.000146
	if got.Status != "known" || got.USD != "0.000146" {
		t.Fatalf("%+v", got)
	}
}

func int64Ptr(n int64) *int64 { return &n }
