package summary

import (
	"strings"
	"testing"

	"github.com/paulpham157/sdf/internal/pricing"
	"github.com/paulpham157/sdf/internal/record"
)

// opencodeAttempts returns the sample OpenCode rows with extra
// gen_ai.usage.* attributes on the two SessionStep.attempt spans, in order.
// A nil value removes that attribute.
// The sample's attempts report input 5601/5822 and cache_read 256/5760.
func opencodeAttempts(t *testing.T, extra ...map[string]any) []record.Record {
	t.Helper()
	rows := opencodeRows(t)
	n := 0
	for i := range rows {
		if rows[i].Signal != "traces" || deref(rows[i].Name) != "SessionStep.attempt" {
			continue
		}
		if n < len(extra) {
			attrs := map[string]any{}
			for k, v := range rows[i].Attributes {
				attrs[k] = v
			}
			for k, v := range extra[n] {
				if v == nil {
					delete(attrs, k)
					continue
				}
				attrs[k] = v
			}
			rows[i].Attributes = attrs
		}
		n++
	}
	if n != 2 {
		t.Fatalf("want 2 SessionStep.attempt spans, got %d", n)
	}
	return rows
}

func opencodeCatalog(t *testing.T) *pricing.Catalog {
	t.Helper()
	cat, err := pricing.NewFixture("2026-11-10T00:00:00Z", []*pricing.Model{{
		ID: freeModel,
		Prices: map[string]string{
			"prompt": "0.000001", "completion": "0.000002",
			"input_cache_read": "0.0000001", "input_cache_write": "0.0000015",
			"internal_reasoning": "0.000002",
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	return cat
}

func onlySession(t *testing.T, rows []record.Record) SessionSummary {
	t.Helper()
	report := SummarizeWith(rows, false, opencodeCatalog(t))
	if len(report.Sessions) != 1 || report.Sessions[0].Tool != "opencode" {
		t.Fatalf("%+v", report.Sessions)
	}
	return report.Sessions[0]
}

func component(cost *pricing.Cost, name string) string {
	for _, item := range cost.Components {
		if item.Name == name {
			return item.Tokens
		}
	}
	return ""
}

// OpenCode (anomalyco/opencode packages/core/src/session/runner/step.ts)
// sends gen_ai.usage.cache_creation.input_tokens. GenAI semconv v0.1.0
// renamed it to gen_ai.usage.cache_write.input_tokens. Before this fix the
// count was summed across requests and the computed cost was unknown.
func TestOpenCodeCacheCreationIsCacheWriteAndPriced(t *testing.T) {
	session := onlySession(t, opencodeAttempts(t,
		map[string]any{"gen_ai.usage.cache_creation.input_tokens": int64(40)},
		map[string]any{"gen_ai.usage.cache_creation.input_tokens": int64(50)},
	))
	if _, ok := session.Tokens.Get("cache_creation.input_tokens"); ok {
		t.Fatalf("old key kept: %v", session.Tokens.Vals)
	}
	if got, _ := session.Tokens.Get("cache_write.input_tokens"); got != 50 {
		t.Fatalf("cache write should be the max 50, not the sum 90: %v", session.Tokens.Vals)
	}
	cost := session.ComputedCost
	if cost == nil || cost.Status != "known" || component(cost, "cache_write") != "50" || component(cost, "input") != "12" {
		t.Fatalf("%+v", cost)
	}
}

// A span that dual-emits the old and new names during a transition reports
// one quantity, not two.
func TestOpenCodeDualEmittedCacheWriteIsNotAdded(t *testing.T) {
	both := map[string]any{
		"gen_ai.usage.cache_creation.input_tokens": int64(50),
		"gen_ai.usage.cache_creation_input_tokens": int64(30),
		"gen_ai.usage.cache_write.input_tokens":    int64(50),
	}
	session := onlySession(t, opencodeAttempts(t, map[string]any{}, both))
	if got, _ := session.Tokens.Get("cache_write.input_tokens"); got != 50 {
		t.Fatalf("%v", session.Tokens.Vals)
	}
	cost := session.ComputedCost
	if cost == nil || cost.Status != "known" || component(cost, "cache_write") != "50" {
		t.Fatalf("%+v", cost)
	}
}

func TestOpenCodeReasoningOutputTokensArePriced(t *testing.T) {
	session := onlySession(t, opencodeAttempts(t,
		map[string]any{"gen_ai.usage.reasoning.output_tokens": int64(10)},
		map[string]any{"gen_ai.usage.reasoning.output_tokens": int64(20)},
	))
	if got, _ := session.Tokens.Get("reasoning.output_tokens"); got != 30 {
		t.Fatalf("reasoning is per-request output and is summed: %v", session.Tokens.Vals)
	}
	cost := session.ComputedCost
	if cost == nil || cost.Status != "known" || component(cost, "reasoning") != "30" || component(cost, "output") != "276" {
		t.Fatalf("%+v", cost)
	}
}

// Per-modality keys (gen_ai.usage.text.input_tokens and the like) are
// breakdowns of the totals. They are kept in the token map as reported, use
// the same max/sum rule as their total, and are not priced a second time.
func TestOpenCodeModalityTokensAreInformational(t *testing.T) {
	session := onlySession(t, opencodeAttempts(t,
		map[string]any{"gen_ai.usage.text.input_tokens": int64(5601), "gen_ai.usage.text.output_tokens": int64(212), "gen_ai.usage.video.input_tokens": int64(1)},
		map[string]any{"gen_ai.usage.text.input_tokens": int64(5822), "gen_ai.usage.text.output_tokens": int64(94)},
	))
	if got, _ := session.Tokens.Get("text.input_tokens"); got != 5822 {
		t.Fatalf("modality input takes the max: %v", session.Tokens.Vals)
	}
	if got, _ := session.Tokens.Get("text.output_tokens"); got != 306 {
		t.Fatalf("modality output is summed: %v", session.Tokens.Vals)
	}
	cost := session.ComputedCost
	if cost == nil || cost.Status != "known" || component(cost, "input") != "62" || component(cost, "output") != "306" {
		t.Fatalf("%+v", cost)
	}
	if !strings.Contains(cost.Method, "per-modality") || !strings.Contains(cost.Method, "text.input_tokens") || !strings.Contains(cost.Method, "video.input_tokens") {
		t.Fatal(cost.Method)
	}
}

// reasoning_tokens is the pre-v0.1.0 spelling of reasoning.output_tokens. On
// one span the two are one quantity (max); across requests they are summed.
func TestOpenCodeReasoningTokensFoldIntoReasoningOutputTokens(t *testing.T) {
	session := onlySession(t, opencodeAttempts(t,
		map[string]any{"gen_ai.usage.reasoning_tokens": int64(10)},
		map[string]any{"gen_ai.usage.reasoning_tokens": int64(4), "gen_ai.usage.reasoning.output_tokens": int64(20)},
	))
	if _, ok := session.Tokens.Get("reasoning_tokens"); ok {
		t.Fatalf("old key kept: %v", session.Tokens.Vals)
	}
	if got, _ := session.Tokens.Get("reasoning.output_tokens"); got != 30 {
		t.Fatalf("want 10 + max(4, 20): %v", session.Tokens.Vals)
	}
	cost := session.ComputedCost
	if cost == nil || cost.Status != "known" || component(cost, "reasoning") != "30" {
		t.Fatalf("%+v", cost)
	}
}

// Review of #198: per-modality keys without the totals they break down must
// leave the cost unknown, not a known $0.
func TestOpenCodeModalityWithoutTotalsIsUnknown(t *testing.T) {
	strip := map[string]any{
		"gen_ai.usage.input_tokens": nil, "gen_ai.usage.output_tokens": nil, "gen_ai.usage.cache_read.input_tokens": nil,
		"gen_ai.usage.text.input_tokens": int64(100), "gen_ai.usage.text.output_tokens": int64(50),
	}
	session := onlySession(t, opencodeAttempts(t, strip, strip))
	cost := session.ComputedCost
	if cost == nil || cost.Status != "unknown" || cost.USD != "" || !strings.Contains(cost.Reason, "without the") {
		t.Fatalf("%+v", cost)
	}
}
