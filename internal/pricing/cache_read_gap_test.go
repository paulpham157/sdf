package pricing

import (
	"strings"
	"testing"
)

// Issue #243. On the 2026-11-10 snapshot (and the live API on 2026-11-23)
// every OpenRouter :free row publishes only prompt "0" and completion "0".
// OpenRouter's Models API reference says models ending in :free set prompt
// and completion to 0, and the Free Variant guide says they are used without
// cost, so a cache-read token there is input on a $0 model.

func claudeWithCacheRead(model string) Usage {
	return Usage{
		Models: []string{model},
		Source: "metrics claude_code.token.usage",
		// Live Claude Code on flint (live_shapes_test): 10739 input,
		// 251 output, 10752 cache read, 0 cache creation.
		Tokens: map[string]float64{"input": 10739, "output": 251, "cacheRead": 10752, "cacheCreation": 0},
	}
}

func TestBundledFreeRowsPriceCacheReadAtZero(t *testing.T) {
	cat := Bundled()
	free := 0
	for id, model := range cat.Models {
		if !strings.HasSuffix(id, ":free") {
			continue
		}
		free++
		if _, ok := model.Prices["input_cache_read"]; ok {
			continue
		}
		got := Price(cat, claudeWithCacheRead(id))
		if got.Status != "known" || got.USD != "0" {
			t.Errorf("%s: %+v", id, got)
			continue
		}
		var cache *Component
		for i := range got.Components {
			if got.Components[i].Name == "cache_read" {
				cache = &got.Components[i]
			}
		}
		if cache == nil || cache.Tokens != "10752" || cache.USDPerToken != "0" || cache.USD != "0" {
			t.Errorf("%s: cache_read component %+v", id, got.Components)
		}
		if !strings.Contains(got.Note, ":free") || !strings.Contains(got.Note, "input_cache_read") {
			t.Errorf("%s: note %q", id, got.Note)
		}
	}
	if free == 0 {
		t.Fatal("bundled snapshot has no :free rows")
	}
}

func TestFlintCodexAndOpenCodeCacheReadIsZero(t *testing.T) {
	cat := Bundled()
	codex := Price(cat, Usage{
		Models: []string{"recursive/flint-v1.0:free"},
		Source: "logs codex.sse_event response.completed",
		Tokens: map[string]float64{"input_token_count": 37400, "cached_token_count": 18560, "output_token_count": 109},
	})
	if codex.Status != "known" || codex.USD != "0" {
		t.Fatalf("codex %+v", codex)
	}
	openCode := Price(cat, Usage{
		Models: []string{"openrouter/recursive/flint-v1.0:free"},
		Source: "traces SessionStep.attempt",
		Tokens: map[string]float64{"input_tokens": 11423, "output_tokens": 306, "cache_read.input_tokens": 6016},
	})
	if openCode.Status != "known" || openCode.USD != "0" {
		t.Fatalf("opencode %+v", openCode)
	}
}

// mistral-nemo's model-level rates are DeepInfra's, which does not cache.
// The endpoints that do cache (Io Net, Mistral) publish their own prompt and
// cache-read rates, so the catalog cannot say what a cached token cost.
func TestMistralNemoCacheReadStaysUnknown(t *testing.T) {
	cat := Bundled()
	nemo := cat.Models["mistralai/mistral-nemo"]
	if nemo == nil {
		t.Fatal("bundled snapshot has no mistralai/mistral-nemo")
	}
	if _, ok := nemo.Prices["input_cache_read"]; ok {
		t.Fatal("snapshot gained a mistral-nemo cache-read price; update this test and docs/summary.md")
	}
	// Real capture (#204) totals: 37400 input, 18560 cached, 109 output.
	got := Price(cat, Usage{
		Models: []string{"mistralai/mistral-nemo"},
		Source: "logs codex.sse_event response.completed",
		Tokens: map[string]float64{"input_token_count": 37400, "cached_token_count": 18560, "output_token_count": 109},
	})
	if got.Status != "unknown" || got.USD != "" || !strings.Contains(got.Reason, "input_cache_read") || !strings.Contains(got.Reason, "mistralai/mistral-nemo") {
		t.Fatalf("%+v", got)
	}
	// Without cached tokens the model-level rates apply.
	plain := Price(cat, Usage{
		Models: []string{"mistralai/mistral-nemo"},
		Source: "logs codex.sse_event response.completed",
		Tokens: map[string]float64{"input_token_count": 9142, "cached_token_count": 0, "output_token_count": 48},
	})
	// 9142*0.000000029 + 48*0.00000003 = 0.000265118 + 0.00000144.
	if plain.Status != "known" || plain.USD != "0.000266558" {
		t.Fatalf("%+v", plain)
	}
}

// The zero rule is only for a :free row whose prompt and completion are 0.
func TestMissingCacheReadIsNotZeroOutsideFreeRows(t *testing.T) {
	cat := mustFixture(t,
		&Model{ID: "vendor/preview", Prices: map[string]string{"prompt": "0", "completion": "0"}},
		&Model{ID: "vendor/odd:free", Prices: map[string]string{"prompt": "0.000001", "completion": "0"}},
		&Model{ID: "vendor/write:free", Prices: map[string]string{"prompt": "0", "completion": "0"}},
	)
	for _, id := range []string{"vendor/preview", "vendor/odd:free"} {
		got := Price(cat, claudeWithCacheRead(id))
		if got.Status != "unknown" || got.USD != "" {
			t.Errorf("%s: %+v", id, got)
		}
	}
	// Cache writes are not covered by the rule.
	write := Price(cat, Usage{
		Models: []string{"vendor/write:free"},
		Source: "metrics claude_code.token.usage",
		Tokens: map[string]float64{"input": 10, "output": 1, "cacheCreation": 5},
	})
	if write.Status != "unknown" || !strings.Contains(write.Reason, "input_cache_write") {
		t.Fatalf("%+v", write)
	}
}
