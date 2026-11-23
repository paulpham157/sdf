package summary

import (
	"strings"
	"testing"

	"github.com/paulpham157/sdf/internal/pricing"
	"github.com/paulpham157/sdf/internal/record"
)

// codexGPT54Response is one codex.sse_event response.completed log in the
// Codex CLI 0.171.0 shape (see codexRealResponse: counts partly as strings,
// no turn.id, no response id) for openai/gpt-5.4 on OpenRouter. That bundled
// row has a min_prompt_tokens 272000 override.
func codexGPT54Response(id int64, stamp, input string, cached int, output string) record.Record {
	rec := codexRealResponse(id, stamp, input, cached, output, "0")
	rec.SessionID = record.Str("019ab6f2-3c1d-7e40-9b51-5d2f8a0c4e11")
	rec.Attributes["conversation.id"] = "019ab6f2-3c1d-7e40-9b51-5d2f8a0c4e11"
	rec.Attributes["model"] = "openai/gpt-5.4"
	rec.Attributes["slug"] = "openai/gpt-5.4"
	return rec
}

func TestCodexSSEPricesEachRequestBelowThreshold(t *testing.T) {
	// Issue #213: three calls of a long Codex session. Their prompts sum to
	// 300000 input tokens, above the 272000 override threshold, but every
	// call is below it, so each call is priced at the base rates. Before,
	// only the aggregate existed and the cost was unknown.
	events := []record.Record{
		codexGPT54Response(1, "2026-11-23T10:02:11.418Z", "95000", 80000, "500"),
		codexGPT54Response(2, "2026-11-23T10:02:37.902Z", "100000", 90000, "600"),
		codexGPT54Response(3, "2026-11-23T10:03:05.266Z", "105000", 95000, "700"),
	}
	// The same export delivered twice must not add a request.
	withDuplicate := append(append([]record.Record{}, events...), codexGPT54Response(4, "2026-11-23T10:03:05.266Z", "105000", 95000, "700"))
	for name, records := range map[string][]record.Record{"once": events, "duplicate": withDuplicate} {
		session := Summarize(records, false).Sessions[0]
		if in, _ := session.Tokens.Get("input_token_count"); in != 300000 {
			t.Fatalf("%s: input %v", name, session.Tokens.Vals)
		}
		requests := requestTokens(records, deref(session.TokensSource))
		if len(requests) != 3 {
			t.Fatalf("%s: %d requests %v", name, len(requests), requests)
		}
		cost := session.ComputedCost
		// gpt-5.4 base rates per call: (input-cached)*0.0000025 +
		// cached*0.00000025 + output*0.000015
		// = 0.065 + 0.0565 + 0.05925 = 0.18075
		if cost == nil || cost.Status != "known" || cost.USD != "0.18075" || cost.CatalogID != "openai/gpt-5.4" {
			t.Fatalf("%s: computed cost %+v", name, cost)
		}
		if !strings.HasPrefix(cost.Method, "sum of 3 per-request costs") {
			t.Fatalf("%s: method %q", name, cost.Method)
		}
		for _, item := range cost.Components {
			if item.USDPerToken == "mixed" {
				t.Fatalf("%s: every call is below the threshold, got mixed rates %+v", name, cost.Components)
			}
		}
		// The aggregate alone is at or above the threshold, so without the
		// per-request items the cost stays unknown (the #185 rule).
		aggregate := pricing.Price(pricing.Bundled(), pricing.Usage{
			Models: session.Models,
			Tokens: tokenMap(session.Tokens),
			Source: deref(session.TokensSource),
		})
		if aggregate.Status != "unknown" {
			t.Fatalf("%s: aggregate cost %+v", name, aggregate)
		}
	}
}

func TestCodexSSEPricesOneRequestAboveThresholdAtOverrideRates(t *testing.T) {
	// One call alone has 280000 input tokens, at or above min_prompt_tokens
	// 272000, so it uses the override rates; the other call stays on base.
	records := []record.Record{
		codexGPT54Response(1, "2026-11-23T11:15:42.031Z", "280000", 200000, "1000"),
		codexGPT54Response(2, "2026-11-23T11:16:20.775Z", "20000", 0, "100"),
	}
	session := Summarize(records, false).Sessions[0]
	cost := session.ComputedCost
	// Call 1, override rates: 80000*0.000005 + 200000*0.0000005 +
	// 1000*0.0000225 = 0.5225. Call 2, base rates: 20000*0.0000025 +
	// 100*0.000015 = 0.0515. Total 0.574.
	if cost == nil || cost.Status != "known" || cost.USD != "0.574" {
		t.Fatalf("computed cost %+v", cost)
	}
	if !strings.Contains(cost.Note, "override rates applied for min_prompt_tokens 272000") {
		t.Fatalf("note %q", cost.Note)
	}
	rates := map[string]string{}
	for _, item := range cost.Components {
		rates[item.Name] = item.USDPerToken
	}
	if rates["input"] != "mixed" || rates["output"] != "mixed" {
		t.Fatalf("components %+v", cost.Components)
	}
	// The override call priced alone uses only override rates.
	alone := Summarize(records[:1], false).Sessions[0].ComputedCost
	if alone == nil || alone.Status != "known" || alone.USD != "0.5225" {
		t.Fatalf("single call cost %+v", alone)
	}
	for _, item := range alone.Components {
		want := map[string]string{"input": "0.000005", "cache_read": "0.0000005", "output": "0.0000225"}[item.Name]
		if want != "" && item.USDPerToken != want {
			t.Fatalf("%s rate %s, want %s; %+v", item.Name, item.USDPerToken, want, alone.Components)
		}
	}
}
