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

func TestOpenCodePricesTheMaxNotTheSum(t *testing.T) {
	cat := mustFixture(t, &Model{
		ID: "recursive/flint-v1.0:free",
		Prices: map[string]string{
			"prompt": "0", "completion": "0", "input_cache_read": "0",
		},
	})
	// Counts are the already-reduced max, not 5601+5822.
	got := Price(cat, Usage{
		Models: []string{"openrouter/recursive/flint-v1.0:free"},
		Source: "traces SessionStep.attempt",
		Tokens: map[string]float64{
			"input_tokens": 5822, "output_tokens": 306, "cache_read.input_tokens": 5760,
		},
	})
	if got.Status != "known" || got.USD != "0" || got.MatchedBy != "openrouter_prefix" {
		t.Fatalf("%+v", got)
	}
	foundInput := false
	for _, item := range got.Components {
		if item.Name == "input" {
			foundInput = item.Tokens == "62"
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
