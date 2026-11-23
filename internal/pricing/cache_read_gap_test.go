package pricing

import (
	"sort"
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
	free, viaRule := 0, 0
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
			continue
		}
		viaRule++
	}
	if free == 0 {
		t.Fatal("bundled snapshot has no :free rows")
	}
	// Without this the test passes vacuously once every :free row publishes
	// input_cache_read. Then the zero rule is no longer exercised by the
	// bundled table and this test (and docs/summary.md) need an update.
	if viaRule == 0 {
		t.Fatalf("no bundled :free row was priced by the zero rule (%d :free rows all publish input_cache_read)", free)
	}
	if cat.RetrievedAtRaw == "2026-11-10T10:53:46Z" && viaRule != 13 {
		t.Fatalf("2026-11-10 snapshot: %d :free rows priced by the zero rule, want 13", viaRule)
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

// cacheReadOnly isolates the cache_read bucket: input and output are 0, so
// only the input_cache_read lookup (and the :free zero rule) decides.
func cacheReadOnly(model string) Usage {
	return Usage{
		Models: []string{model},
		Source: "metrics claude_code.token.usage",
		Tokens: map[string]float64{"input": 0, "output": 0, "cacheRead": 100, "cacheCreation": 0},
	}
}

// A :free row whose prompt or completion is not a parsable zero does not get
// the zero rule (follow-up to #246). "-1" is OpenRouter's dynamic-router
// sentinel, not a free price.
func TestFreeZeroRuleNeedsParsableZeroPromptAndCompletion(t *testing.T) {
	cases := map[string][2]string{
		"vendor/router:free":         {"-1", "-1"},
		"vendor/neg-prompt:free":     {"-1", "0"},
		"vendor/neg-completion:free": {"0", "-1"},
		"vendor/empty-prompt:free":   {"", "0"},
		"vendor/empty-compl:free":    {"0", ""},
		"vendor/junk-prompt:free":    {"free", "0"},
		"vendor/junk-compl:free":     {"0", "n/a"},
		"vendor/nonzero:free":        {"0.000001", "0"},
	}
	var models []*Model
	for id, pc := range cases {
		models = append(models, &Model{ID: id, Prices: map[string]string{"prompt": pc[0], "completion": pc[1]}})
	}
	// Controls: a parsable zero in another spelling does get the rule.
	models = append(models,
		&Model{ID: "vendor/zero:free", Prices: map[string]string{"prompt": "0", "completion": "0"}},
		&Model{ID: "vendor/zero-decimal:free", Prices: map[string]string{"prompt": "0.0", "completion": "0.000"}},
	)
	cat := mustFixture(t, models...)
	// NewFixture requires prompt and completion keys, so a row that lacks
	// both is injected after the fixture is built.
	cat.Models["vendor/missing-keys:free"] = &Model{ID: "vendor/missing-keys:free", Prices: map[string]string{}}
	for id := range cases {
		got := Price(cat, cacheReadOnly(id))
		if got.Status != "unknown" || got.USD != "" || !strings.Contains(got.Reason, "input_cache_read") {
			t.Errorf("%s %v: want unknown for missing input_cache_read, got %+v", id, cases[id], got)
		}
	}
	got := Price(cat, cacheReadOnly("vendor/missing-keys:free"))
	if got.Status != "unknown" || got.USD != "" || !strings.Contains(got.Reason, "input_cache_read") {
		t.Errorf("vendor/missing-keys:free: want unknown for a :free row with no prompt or completion key, got %+v", got)
	}
	for _, id := range []string{"vendor/zero:free", "vendor/zero-decimal:free"} {
		got := Price(cat, cacheReadOnly(id))
		wantNote := "cache_read tokens are priced at 0 because " + id + " is an OpenRouter :free variant with prompt and completion 0 and no input_cache_read price"
		if got.Status != "known" || got.USD != "0" || got.Note != wantNote {
			t.Errorf("%s: %+v, want note %q", id, got, wantNote)
		}
	}
}

// openrouter/free (the Free Models Router) and the Lyria preview rows have
// prompt and completion "0" in the bundled snapshot but no :free suffix, so
// the zero rule does not apply: cache reads leave them unknown. Their 0/0
// input and output still price at a known $0.
func TestBundledZeroRowsWithoutFreeSuffixKeepCacheReadUnknown(t *testing.T) {
	cat := Bundled()
	for _, id := range []string{"openrouter/free", "google/lyria-3-clip-preview", "google/lyria-3-pro-preview"} {
		model := cat.Models[id]
		if model == nil {
			t.Errorf("bundled snapshot has no %s; update docs/summary.md", id)
			continue
		}
		if model.Prices["prompt"] != "0" || model.Prices["completion"] != "0" {
			t.Errorf("%s: prices %v; update docs/summary.md", id, model.Prices)
		}
		if _, ok := model.Prices["input_cache_read"]; ok {
			t.Errorf("%s gained input_cache_read; update this test and docs/summary.md", id)
			continue
		}
		got := Price(cat, cacheReadOnly(id))
		if got.Status != "unknown" || !strings.Contains(got.Reason, "catalog has no input_cache_read price for model "+id) {
			t.Errorf("%s: %+v", id, got)
		}
		plain := Price(cat, Usage{
			Models: []string{id},
			Source: "metrics claude_code.token.usage",
			Tokens: map[string]float64{"input": 100, "output": 10, "cacheRead": 0, "cacheCreation": 0},
		})
		if plain.Status != "known" || plain.USD != "0" {
			t.Errorf("%s without cache reads: %+v", id, plain)
		}
	}
}

// Every bundled row whose prompt and completion are both "-1" is a dynamic
// router (OpenRouter's sentinel), including the non-openrouter routers
// nvidia/switchyard and typesafe/jev-router. They stay unknown: the catalog
// has no real rate to apply.
func TestBundledNegativeRouterRowsStayUnknown(t *testing.T) {
	cat := Bundled()
	want := []string{
		"nvidia/switchyard",
		"openrouter/auto",
		"openrouter/auto-beta",
		"openrouter/auto-code",
		"openrouter/bodybuilder",
		"openrouter/fusion",
		"openrouter/pareto-code",
		"typesafe/jev-router",
	}
	var found []string
	seen := map[string]bool{}
	for id, model := range cat.Models {
		if model.Prices["prompt"] != "-1" || model.Prices["completion"] != "-1" {
			continue
		}
		found = append(found, id)
		seen[id] = true
		plain := Price(cat, Usage{
			Models: []string{id},
			Source: "metrics claude_code.token.usage",
			Tokens: map[string]float64{"input": 100, "output": 10, "cacheRead": 0, "cacheCreation": 0},
		})
		if plain.Status != "unknown" || plain.USD != "" || !strings.Contains(plain.Reason, "catalog price prompt for model "+id+" is negative") {
			t.Errorf("%s: %+v, want unknown because prompt is negative", id, plain)
		}
		got := Price(cat, cacheReadOnly(id))
		if got.Status != "unknown" || got.USD != "" {
			t.Errorf("%s with cache reads: %+v", id, got)
		}
	}
	sort.Strings(found)
	for _, id := range want {
		if !seen[id] {
			t.Errorf("bundled snapshot has no -1/-1 row %s; update this test and docs/summary.md", id)
		}
	}
	if cat.RetrievedAtRaw == "2026-11-10T10:53:46Z" && strings.Join(found, ",") != strings.Join(want, ",") {
		t.Fatalf("2026-11-10 snapshot -1/-1 rows %v, want %v", found, want)
	}
}
