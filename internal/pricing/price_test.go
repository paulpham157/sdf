package pricing

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestBundledCatalogIsTheOpenRouterSnapshot(t *testing.T) {
	cat := Bundled()
	if cat.Source != SourceURL || cat.Table != "bundled" {
		t.Fatalf("%s %s", cat.Source, cat.Table)
	}
	if _, err := time.Parse(time.RFC3339, cat.RetrievedAtRaw); err != nil {
		t.Fatal(err)
	}
	if len(cat.Models) < 100 {
		t.Fatalf("models %d", len(cat.Models))
	}
	flint := cat.Models["recursive/flint-v1.0:free"]
	if flint == nil || flint.Prices["prompt"] != "0" || flint.Prices["completion"] != "0" {
		t.Fatalf("%+v", flint)
	}
	if _, ok := flint.Prices["input_cache_read"]; ok {
		t.Fatal("snapshot gained a cache-read price; the missing-price test needs an update")
	}
	if flint.HasOverrides {
		t.Fatal("overrides")
	}
	sonnet := cat.Models["anthropic/claude-sonnet-4.5"]
	if sonnet == nil || !sonnet.HasOverrides {
		t.Fatal("expected conditional overrides on claude-sonnet-4.5")
	}
}

func TestPriceUsesPublishedRatesAndRefusesGaps(t *testing.T) {
	cat := mustFixture(t, &Model{
		ID: "vendor/sample",
		Prices: map[string]string{
			"prompt": "0.000003", "completion": "0.000015",
			"input_cache_read": "0.0000003", "input_cache_write": "0.00000375",
		},
	})
	got := Price(cat, Usage{
		Models: []string{"vendor/sample"},
		Source: "metrics claude_code.token.usage",
		Tokens: map[string]float64{"input": 1000, "output": 100, "cacheRead": 200, "cacheCreation": 0},
	})
	// 1000*0.000003 + 100*0.000015 + 200*0.0000003 = 0.00456. Zero cache writes are omitted.
	if got.Status != "known" || got.USD != "0.00456" {
		t.Fatalf("%+v", got)
	}
	byName := map[string]Component{}
	for _, item := range got.Components {
		byName[item.Name] = item
	}
	if byName["input"].USD != "0.003" || byName["cache_read"].Tokens != "200" || byName["cache_write"].Tokens != "" {
		t.Fatalf("%+v", got.Components)
	}

	missing := Price(cat, Usage{
		Models: []string{"vendor/other"},
		Source: "metrics claude_code.token.usage",
		Tokens: map[string]float64{"input": 1},
	})
	if missing.Status != "unknown" || missing.USD != "" || !strings.Contains(missing.Reason, "vendor/other") {
		t.Fatalf("%+v", missing)
	}

	none := Price(cat, Usage{Models: []string{"vendor/sample"}, Source: "metrics claude_code.token.usage"})
	if none.Status != "unknown" || none.USD != "" || !strings.Contains(none.Reason, "no token counts") {
		t.Fatalf("%+v", none)
	}
}

func TestCodexSubtractsCacheAndReasoningAndIgnoresTotal(t *testing.T) {
	cat := mustFixture(t, &Model{
		ID: "openai/gpt-test",
		Prices: map[string]string{
			"prompt": "2", "completion": "4",
			"input_cache_read": "1", "input_cache_write": "3",
			"internal_reasoning": "8",
		},
	})
	got := Price(cat, Usage{
		Models: []string{"gpt-test"},
		Source: "metrics codex.turn.token_usage",
		Tokens: map[string]float64{
			"total": 999, "input": 100, "cached_input": 40, "cache_write_input": 10,
			"output": 20, "reasoning_output": 5,
		},
	})
	// input 50*2 + output 15*4 + cache read 40*1 + cache write 10*3 + reasoning 5*8 = 270.
	if got.Status != "known" || got.USD != "270" || got.MatchedBy != "unique_suffix" || got.CatalogID != "openai/gpt-test" {
		t.Fatalf("%+v", got)
	}

	overlap := Price(cat, Usage{
		Models: []string{"openai/gpt-test"},
		Source: "logs codex.sse_event response.completed",
		Tokens: map[string]float64{"input_token_count": 10, "cached_token_count": 12, "output_token_count": 1},
	})
	if overlap.Status != "unknown" || !strings.Contains(overlap.Reason, "exceeds input") || overlap.USD != "" {
		t.Fatalf("%+v", overlap)
	}
}

func TestOpenCodePricesTheSummaryTotals(t *testing.T) {
	cat := mustFixture(t, &Model{
		ID: "recursive/flint-v1.0:free",
		Prices: map[string]string{
			"prompt": "0", "completion": "0", "input_cache_read": "0",
		},
	})
	// Counts are the summary's per-request sums (issue #200): input
	// 5601+5822 and cache read 256+5760. Pricing does not reduce them again.
	got := Price(cat, Usage{
		Models: []string{"openrouter/recursive/flint-v1.0:free"},
		Source: "traces SessionStep.attempt",
		Tokens: map[string]float64{
			"input_tokens": 11423, "output_tokens": 306, "cache_read.input_tokens": 6016,
		},
	})
	if got.Status != "known" || got.USD != "0" || got.MatchedBy != "openrouter_prefix" {
		t.Fatalf("%+v", got)
	}
	foundInput := false
	for _, item := range got.Components {
		if item.Name == "input" {
			foundInput = item.Tokens == "5407"
		}
	}
	if !foundInput {
		t.Fatalf("%+v", got.Components)
	}
}

func TestConditionalOverridesAndSplitCacheWritesAreUnknown(t *testing.T) {
	cat := mustFixture(t,
		&Model{
			ID: "anthropic/with-override", HasOverrides: true,
			Prices: map[string]string{"prompt": "0.000003", "completion": "0.000015", "input_cache_read": "0.0000003", "input_cache_write": "0.00000375", "input_cache_write_1h": "0.000006"},
		},
		&Model{
			ID:     "vendor/split-cache",
			Prices: map[string]string{"prompt": "1", "completion": "1", "input_cache_write": "2", "input_cache_write_1h": "4"},
		},
	)
	over := Price(cat, Usage{Models: []string{"anthropic/with-override"}, Source: "traces chat", Tokens: map[string]float64{"input_tokens": 1, "output_tokens": 1}})
	if over.Status != "unknown" || !strings.Contains(over.Reason, "overrides") || over.USD != "" {
		t.Fatalf("%+v", over)
	}
	split := Price(cat, Usage{
		Models: []string{"vendor/split-cache"},
		Source: "metrics claude_code.token.usage",
		Tokens: map[string]float64{"cacheCreation": 10, "input": 1, "output": 1},
	})
	if split.Status != "unknown" || !strings.Contains(split.Reason, "input_cache_write_1h") {
		t.Fatalf("%+v", split)
	}
}

func TestAmbiguousSuffixIsUnknown(t *testing.T) {
	cat := mustFixture(t,
		&Model{ID: "one/demo", Prices: map[string]string{"prompt": "1", "completion": "1"}},
		&Model{ID: "two/demo", Prices: map[string]string{"prompt": "2", "completion": "2"}},
	)
	got := Price(cat, Usage{Models: []string{"demo"}, Source: "traces chat", Tokens: map[string]float64{"input_tokens": 1}})
	if got.Status != "unknown" || !strings.Contains(got.Reason, "more than one") || got.USD != "" {
		t.Fatalf("%+v", got)
	}
}

func TestFetchCopiesPricesAndDoesNotSendAKey(t *testing.T) {
	var auth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		if r.URL.RawQuery != "" {
			t.Errorf("query %s", r.URL.RawQuery)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []any{
				map[string]any{"id": "vendor/priced", "pricing": map[string]any{
					"prompt": "0.000002", "completion": "0.00001",
					"input_cache_read": "0.0000002",
					"overrides":        []any{map[string]string{"min_prompt_tokens": "10", "prompt": "0.000004"}},
				}},
				map[string]any{"id": "vendor/incomplete", "pricing": map[string]any{"prompt": "1"}},
			},
		})
	}))
	defer server.Close()
	snap, err := Fetch(context.Background(), server.Client(), server.URL)
	if err != nil {
		t.Fatal(err)
	}
	if auth != "" {
		t.Fatal(auth)
	}
	if snap.Catalog.Source != SourceURL || len(snap.Catalog.Models) != 1 {
		t.Fatalf("%d %s", len(snap.Catalog.Models), snap.Catalog.Source)
	}
	model := snap.Catalog.Models["vendor/priced"]
	if model.Prices["prompt"] != "0.000002" || !model.HasOverrides {
		t.Fatalf("%+v", model)
	}
	if len(model.Overrides) != 1 || model.Overrides[0].MinPromptTokens == nil || *model.Overrides[0].MinPromptTokens != 10 || model.Overrides[0].Prices["prompt"] != "0.000004" {
		t.Fatalf("overrides %+v", model.Overrides)
	}
	if !strings.Contains(string(snap.Body), `"retrieved_at"`) || !strings.Contains(string(snap.Body), "min_prompt_tokens") {
		t.Fatalf("%s", snap.Body)
	}
	again, err := Parse(snap.Body, "cache")
	if err != nil || !again.Models["vendor/priced"].HasOverrides {
		t.Fatal(err)
	}
}

func mustFixture(t *testing.T, models ...*Model) *Catalog {
	t.Helper()
	cat, err := NewFixture("2026-11-10T00:00:00Z", models)
	if err != nil {
		t.Fatal(err)
	}
	return cat
}

func openCodePrice(t *testing.T) (func(map[string]float64) Cost, func(Cost, string) string) {
	t.Helper()
	cat := mustFixture(t, &Model{
		ID: "vendor/model",
		Prices: map[string]string{
			"prompt": "1", "completion": "1", "input_cache_read": "1", "input_cache_write": "1", "internal_reasoning": "1",
		},
	})
	price := func(tokens map[string]float64) Cost {
		return Price(cat, Usage{Models: []string{"vendor/model"}, Source: "traces SessionStep.attempt", Tokens: tokens})
	}
	tokensOf := func(cost Cost, name string) string {
		for _, item := range cost.Components {
			if item.Name == name {
				return item.Tokens
			}
		}
		return ""
	}
	return price, tokensOf
}

// The summary folds old names before pricing. These cases call Price with
// unfolded keys on purpose: the aliases in openCodeBuckets are a defensive
// fallback for a caller that did not fold them.
func TestOpenCodePricingFallbackAliases(t *testing.T) {
	price, tokensOf := openCodePrice(t)
	// The pre-v0.1.0 name alone is priced as cache write.
	old := price(map[string]float64{"input_tokens": 100, "output_tokens": 10, "cache_creation.input_tokens": 20})
	if old.Status != "known" || tokensOf(old, "cache_write") != "20" || tokensOf(old, "input") != "80" {
		t.Fatalf("%+v", old)
	}
	under := price(map[string]float64{"input_tokens": 100, "output_tokens": 10, "cache_creation_input_tokens": 7})
	if under.Status != "known" || tokensOf(under, "cache_write") != "7" {
		t.Fatalf("%+v", under)
	}
	reasoning := price(map[string]float64{"input_tokens": 100, "output_tokens": 10, "reasoning_tokens": 3})
	if reasoning.Status != "known" || tokensOf(reasoning, "reasoning") != "3" || tokensOf(reasoning, "output") != "7" {
		t.Fatalf("%+v", reasoning)
	}
}

// Old and new names for the same quantity price the largest count, never
// the sum, and the result does not depend on which key sorts first.
func TestOpenCodeRenamedUsageKeysAreOrderIndependent(t *testing.T) {
	price, tokensOf := openCodePrice(t)
	for _, tc := range []struct {
		tokens map[string]float64
		want   string
	}{
		{map[string]float64{"cache_creation.input_tokens": 30, "cache_write.input_tokens": 20, "cache_write_input_tokens": 20}, "30"},
		{map[string]float64{"cache_creation.input_tokens": 10, "cache_write.input_tokens": 20, "cache_write_input_tokens": 20}, "20"},
		{map[string]float64{"cache_creation.input_tokens": 10, "cache_write.input_tokens": 20, "cache_write_input_tokens": 25}, "25"},
		{map[string]float64{"cache_creation_input_tokens": 5, "cache_creation.input_tokens": 25, "cache_write.input_tokens": 20}, "25"},
	} {
		tokens := map[string]float64{"input_tokens": 100, "output_tokens": 10}
		for k, v := range tc.tokens {
			tokens[k] = v
		}
		got := price(tokens)
		if got.Status != "known" || tokensOf(got, "cache_write") != tc.want {
			t.Fatalf("%v: %+v", tc.tokens, got)
		}
	}
	reasoning := price(map[string]float64{"input_tokens": 100, "output_tokens": 10, "reasoning.output_tokens": 3, "reasoning_tokens": 4})
	if reasoning.Status != "known" || tokensOf(reasoning, "reasoning") != "4" {
		t.Fatalf("%+v", reasoning)
	}
	// Two pre-existing names for one bucket, with no renamed key, still refuse to guess.
	dup := price(map[string]float64{"input_tokens": 100, "cache_read.input_tokens": 1, "cache_read_input_tokens": 1})
	if dup.Status != "unknown" || !strings.Contains(dup.Reason, "twice") {
		t.Fatalf("%+v", dup)
	}
	dupWrite := price(map[string]float64{"input_tokens": 100, "cache_write.input_tokens": 1, "cache_write_input_tokens": 1})
	if dupWrite.Status != "unknown" || !strings.Contains(dupWrite.Reason, "twice") {
		t.Fatalf("%+v", dupWrite)
	}
}

func TestOpenCodeModalityKeys(t *testing.T) {
	price, tokensOf := openCodePrice(t)
	ok := price(map[string]float64{"input_tokens": 100, "output_tokens": 10, "cache_read.input_tokens": 5, "text.input_tokens": 100, "image.output_tokens": 2, "text.cache_read.input_tokens": 5})
	if ok.Status != "known" || tokensOf(ok, "input") != "95" || tokensOf(ok, "output") != "10" || !strings.Contains(ok.Method, "per-modality") {
		t.Fatalf("%+v", ok)
	}
	// Review of #198: modality keys without their totals must not price as a
	// known $0. On main this was unknown, and it stays unknown.
	for _, tokens := range []map[string]float64{
		{"text.input_tokens": 100, "text.output_tokens": 50},
		{"input_tokens": 100, "text.output_tokens": 50},
		{"input_tokens": 100, "output_tokens": 10, "text.cache_read.input_tokens": 5},
		{"input_tokens": 100, "output_tokens": 10, "audio.cache_write.input_tokens": 5},
	} {
		got := price(tokens)
		if got.Status != "unknown" || got.USD != "" || !strings.Contains(got.Reason, "without the") {
			t.Fatalf("%v: %+v", tokens, got)
		}
	}
	// A modality outside the allowlist is not skipped.
	unknownModality := price(map[string]float64{"input_tokens": 100, "output_tokens": 10, "hologram.input_tokens": 1})
	if unknownModality.Status != "unknown" || !strings.Contains(unknownModality.Reason, "hologram.input_tokens") {
		t.Fatalf("%+v", unknownModality)
	}
	// A key that is neither a known total nor a modality breakdown is still unknown.
	other := price(map[string]float64{"input_tokens": 100, "mystery_tokens": 1})
	if other.Status != "unknown" || !strings.Contains(other.Reason, "mystery_tokens") {
		t.Fatalf("%+v", other)
	}
}

func TestIsModalityUsage(t *testing.T) {
	for label, want := range map[string]bool{
		"text.input_tokens":             true,
		"image.output_tokens":           true,
		"audio.cache_read.input_tokens": true,
		"video.input_tokens":            true,
		"text.cache_write.input_tokens": true,
		"hologram.input_tokens":         false,
		"cache_read.input_tokens":       false,
		"cache_write.input_tokens":      false,
		"cache_creation.input_tokens":   false,
		"reasoning.output_tokens":       false,
		"input_tokens":                  false,
		"text.total_tokens":             false,
		"Text.input_tokens":             false,
	} {
		if got := IsModalityUsage(label); got != want {
			t.Errorf("%s: got %v want %v", label, got, want)
		}
	}
}
