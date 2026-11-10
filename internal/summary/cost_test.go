package summary

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/paulpham157/sdf/internal/pricing"
	"github.com/paulpham157/sdf/internal/record"
)

func TestComputedCostStaysUnknownWhenTheCatalogHasNoCachePrice(t *testing.T) {
	session := Summarize(claudeFixture(), false).Sessions[0]
	if session.AgentReportedCostUSD == nil || *session.AgentReportedCostUSD != 0.065346 {
		t.Fatalf("agent %v", session.AgentReportedCostUSD)
	}
	if session.ComputedCost == nil || session.ComputedCost.Status != "unknown" || session.ComputedCost.USD != "" {
		t.Fatalf("%+v", session.ComputedCost)
	}
	if !strings.Contains(session.ComputedCost.Reason, "input_cache_read") || !strings.Contains(session.ComputedCost.Reason, freeModel) {
		t.Fatal(session.ComputedCost.Reason)
	}
	if session.ComputedCost.PriceSource != pricing.SourceURL || session.ComputedCost.PriceDate == "" {
		t.Fatalf("%+v", session.ComputedCost)
	}
	text := Format(Report{Sessions: []SessionSummary{session}})
	if !strings.Contains(text, "agent_reported_cost_usd: 0.065346") || !strings.Contains(text, "computed_cost_usd: unknown") {
		t.Fatal(text)
	}
	body, err := json.Marshal(session.ComputedCost)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatal(err)
	}
	if _, ok := decoded["usd"]; ok {
		t.Fatalf("unknown cost included usd: %s", body)
	}
}

func TestPiFreeModelComputesZeroFromTheBundledTable(t *testing.T) {
	session := Summarize(piRows(t), false).Sessions[0]
	if session.CostUSD == nil || *session.CostUSD != 0 {
		t.Fatalf("agent %v", session.CostUSD)
	}
	if session.ComputedCost == nil || session.ComputedCost.Status != "known" || session.ComputedCost.USD != "0" {
		t.Fatalf("%+v", session.ComputedCost)
	}
	if session.ComputedCost.CatalogID != freeModel || session.ComputedCost.MatchedBy != "exact" {
		t.Fatalf("%+v", session.ComputedCost)
	}
}

func TestOpenCodeComputedCostUsesTheReducedTokenTotals(t *testing.T) {
	cat := mustPrices(t, &pricing.Model{
		ID: freeModel,
		Prices: map[string]string{
			"prompt": "0", "completion": "0", "input_cache_read": "0",
		},
	})
	session := SummarizeWith(opencodeRows(t), false, cat).Sessions[0]
	if session.CostUSD != nil {
		t.Fatal(session.CostUSD)
	}
	if session.ComputedCost.Status != "known" || session.ComputedCost.USD != "0" {
		t.Fatalf("%+v", session.ComputedCost)
	}
	var input string
	for _, item := range session.ComputedCost.Components {
		if item.Name == "input" {
			input = item.Tokens
		}
	}
	if input != "62" {
		t.Fatalf("input tokens %s components %+v", input, session.ComputedCost.Components)
	}
}

func TestSeveralModelsAreNotPricedAsOne(t *testing.T) {
	cat := mustPrices(t,
		&pricing.Model{ID: "vendor/a", Prices: map[string]string{"prompt": "1", "completion": "1"}},
		&pricing.Model{ID: "vendor/b", Prices: map[string]string{"prompt": "2", "completion": "2"}},
	)
	session := SummarizeWith([]record.Record{{
		Signal: "logs", ServiceName: record.Str("claude-code"), SessionID: record.Str("s"),
		Name: record.Str("claude_code.api_request"), TimeUnixNano: record.Int64(1),
		Attributes: map[string]any{"model": "vendor/a", "input_tokens": 1, "output_tokens": 1},
		Resource:   map[string]any{},
	}, {
		Signal: "logs", ServiceName: record.Str("claude-code"), SessionID: record.Str("s"),
		Name: record.Str("claude_code.user_prompt"), TimeUnixNano: record.Int64(2),
		Attributes: map[string]any{"model": "vendor/b"},
		Resource:   map[string]any{},
	}}, false, cat).Sessions[0]
	if session.ComputedCost.Status != "unknown" || session.ComputedCost.USD != "" || !strings.Contains(session.ComputedCost.Reason, "different catalog ids") {
		t.Fatalf("%+v models %v", session.ComputedCost, session.Models)
	}
}

func mustPrices(t *testing.T, models ...*pricing.Model) *pricing.Catalog {
	t.Helper()
	cat, err := pricing.NewFixture("2026-11-10T00:00:00Z", models)
	if err != nil {
		t.Fatal(err)
	}
	return cat
}
