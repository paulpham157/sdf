package summary

import (
	"strings"
	"testing"

	"github.com/paulpham157/sdf/internal/pricing"
	"github.com/paulpham157/sdf/internal/record"
)

// Regression tests for issue #200. One OpenCode SessionStep.attempt span is
// one provider request (anomalyco/opencode
// packages/core/src/session/runner/step.ts annotates the span with the
// finish usage of that one llm.stream call). Every request is billed its
// full input, and cache read and cache write are per-request counts, so the
// session total is the sum over requests, not the max.
//
// Prices are test prices, the one-row catalog from the issue repro
// (/workspace/sdf-issue-repro/prices.json): prompt 1e-6, completion 2e-6,
// input_cache_read 1e-7, input_cache_write 1.25e-6 USD per token.
func issue200Catalog(t *testing.T) *pricing.Catalog {
	t.Helper()
	return mustPrices(t, &pricing.Model{
		ID: freeModel,
		Prices: map[string]string{
			"prompt": "0.000001", "completion": "0.000002",
			"input_cache_read": "0.0000001", "input_cache_write": "0.00000125",
		},
	})
}

func issue200Session(t *testing.T, rows []record.Record) SessionSummary {
	t.Helper()
	report := SummarizeWith(rows, false, issue200Catalog(t))
	if len(report.Sessions) != 1 || report.Sessions[0].Tool != "opencode" {
		t.Fatalf("%+v", report.Sessions)
	}
	return report.Sessions[0]
}

func wantTokens(t *testing.T, session SessionSummary, want map[string]float64) {
	t.Helper()
	for key, value := range want {
		if got, ok := session.Tokens.Get(key); !ok || got != value {
			t.Errorf("%s = %v (present %v), want %v; tokens %v", key, got, ok, value, session.Tokens.Vals)
		}
	}
}

func wantCost(t *testing.T, cost *pricing.Cost, usd string, components map[string]string) {
	t.Helper()
	if cost == nil {
		t.Fatal("no computed cost")
	}
	if cost.Status != "known" || cost.USD != usd {
		t.Fatalf("cost %s %q (reason %q), want known %s: %+v", cost.Status, cost.USD, cost.Reason, usd, cost)
	}
	for name, tokens := range components {
		if got := component(cost, name); got != tokens {
			t.Errorf("component %s = %q, want %q: %+v", name, got, tokens, cost.Components)
		}
	}
}

// The bundled sample has two requests: input 5601 and 5822, cache_read 256
// and 5760, output 212 and 94.
func TestIssue200OpenCodeSumsInputAndCacheAcrossRequests(t *testing.T) {
	session := issue200Session(t, opencodeRows(t))
	wantTokens(t, session, map[string]float64{
		"input_tokens":            5601 + 5822,
		"cache_read.input_tokens": 256 + 5760,
		"output_tokens":           212 + 94,
	})
	// 5407*1e-6 + 6016*1e-7 + 306*2e-6 = 0.0066206 (was 0.00125 with the max).
	wantCost(t, session.ComputedCost, "0.0066206", map[string]string{
		"input": "5407", "cache_read": "6016", "output": "306",
	})
	if method := deref(session.TokensMethod); strings.Contains(method, "max") || !strings.Contains(method, "sum") {
		t.Fatalf("tokens method %q", method)
	}
}

// A large cache write on the first request and a large cache read on the
// second used to be paired by the max rule (5500 + 5760 > 5822) and left the
// cost unknown. Each request is consistent on its own, and so are the sums.
func TestIssue200LargeFirstCacheWriteStaysPriced(t *testing.T) {
	for _, key := range []string{"gen_ai.usage.cache_write.input_tokens", "gen_ai.usage.cache_creation.input_tokens"} {
		t.Run(key, func(t *testing.T) {
			session := issue200Session(t, opencodeAttempts(t,
				map[string]any{key: int64(5500), "gen_ai.usage.cache_read.input_tokens": int64(0)},
				map[string]any{key: int64(50)},
			))
			wantTokens(t, session, map[string]float64{
				"input_tokens":             11423,
				"cache_read.input_tokens":  5760,
				"cache_write.input_tokens": 5550,
			})
			// 113*1e-6 + 5760*1e-7 + 5550*1.25e-6 + 306*2e-6 = 0.0082385.
			wantCost(t, session.ComputedCost, "0.0082385", map[string]string{
				"input": "113", "cache_read": "5760", "cache_write": "5550", "output": "306",
			})
		})
	}
}

// The #198 rule still holds inside one span: old and new names of one
// quantity are the same tokens, so the largest is kept and they are never
// added. Only the per-span values are summed across requests.
func TestIssue200AliasesAreMaxPerSpanThenSummed(t *testing.T) {
	session := issue200Session(t, opencodeAttempts(t,
		map[string]any{"gen_ai.usage.cache_creation.input_tokens": int64(30), "gen_ai.usage.cache_write.input_tokens": int64(20)},
		map[string]any{"gen_ai.usage.cache_creation.input_tokens": int64(20), "gen_ai.usage.cache_creation_input_tokens": int64(20)},
	))
	wantTokens(t, session, map[string]float64{"cache_write.input_tokens": 30 + 20})
}

// Per-modality input breakdowns follow input_tokens and are summed too.
func TestIssue200ModalityInputIsSummed(t *testing.T) {
	session := issue200Session(t, opencodeAttempts(t,
		map[string]any{"gen_ai.usage.text.input_tokens": int64(5601)},
		map[string]any{"gen_ai.usage.text.input_tokens": int64(5822)},
	))
	wantTokens(t, session, map[string]float64{"text.input_tokens": 11423, "input_tokens": 11423})
}

// One span is one request. If an exporter retry delivers the same span
// twice, it is counted once.
func TestIssue200DuplicateSpanIsCountedOnce(t *testing.T) {
	rows := opencodeRows(t)
	for _, row := range rows {
		if row.Signal == "traces" && deref(row.Name) == "SessionStep.attempt" {
			dup := row
			dup.ID += 100000
			rows = append(rows, dup)
		}
	}
	session := issue200Session(t, rows)
	wantTokens(t, session, map[string]float64{
		"input_tokens":            11423,
		"cache_read.input_tokens": 6016,
		"output_tokens":           306,
	})
}

// Pi chat spans are summed per request as before; a span delivered twice is
// still one request.
func TestIssue200PiDuplicateChatSpanIsCountedOnce(t *testing.T) {
	rows := piRows(t)
	for _, row := range rows {
		if row.Signal == "traces" && spanHead(deref(row.Name)) == "chat" {
			dup := row
			dup.ID += 100000
			rows = append(rows, dup)
		}
	}
	sessions := Summarize(rows, false).Sessions
	if len(sessions) != 1 || deref(sessions[0].TokensSource) != "traces chat" {
		t.Fatalf("%+v", sessions)
	}
	wantTokens(t, sessions[0], map[string]float64{"input_tokens": 1245, "output_tokens": 832})
}
