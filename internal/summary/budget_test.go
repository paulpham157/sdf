package summary

import (
	"bytes"
	"encoding/json"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/paulpham157/sdf/internal/pricing"
)

func TestBudgetWithinOverAndUnknown(t *testing.T) {
	known := func(id, usd string) SessionSummary {
		return SessionSummary{SessionID: id, ComputedCost: &pricing.Cost{Status: "known", USD: usd}}
	}
	sessions := []SessionSummary{known("a", "1.25"), known("b", "0.75")}

	within := evaluateBudget(sessions, &Budget{CeilingUSD: "2", Window: "store"})
	if within.Status != "within" || within.TotalUSD != "2" || within.CeilingUSD != "2" || within.UnknownCost != 0 || within.Sessions != 2 {
		t.Fatalf("within %+v", within)
	}

	equal := evaluateBudget(sessions, &Budget{CeilingUSD: "2.0", Window: "store"})
	if equal.Status != "within" || equal.TotalUSD != "2" {
		t.Fatalf("equal to the ceiling is within %+v", equal)
	}

	over := evaluateBudget(sessions, &Budget{CeilingUSD: "1.5", Window: "store"})
	if over.Status != "over" || over.TotalUSD != "2" || over.CeilingUSD != "1.5" {
		t.Fatalf("over %+v", over)
	}

	missing := append([]SessionSummary{}, sessions...)
	missing = append(missing, SessionSummary{SessionID: "c", ComputedCost: nil})
	unknown := evaluateBudget(missing, &Budget{CeilingUSD: "100", Window: "store"})
	if unknown.Status != "unknown" || unknown.TotalUSD != "" || unknown.UnknownCost != 1 || unknown.Sessions != 3 {
		t.Fatalf("nil cost %+v", unknown)
	}
	raw, err := json.Marshal(unknown)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte(`"total_usd"`)) {
		t.Fatalf("unknown budget produced a total: %s", raw)
	}

	unpriced := append([]SessionSummary{}, sessions...)
	unpriced = append(unpriced, SessionSummary{SessionID: "d", ComputedCost: &pricing.Cost{Status: "unknown"}})
	again := evaluateBudget(unpriced, &Budget{CeilingUSD: "100", Window: "store"})
	if again.Status != "unknown" || again.TotalUSD != "" || again.UnknownCost != 1 {
		t.Fatalf("unknown status %+v", again)
	}
}

func TestBudgetDayWindowUsesSessionStart(t *testing.T) {
	day := time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC)
	inside := day.Add(3 * time.Hour).UnixNano()
	before := day.Add(-time.Second).UnixNano()
	sessions := []SessionSummary{
		{SessionID: "today", ComputedCost: &pricing.Cost{Status: "known", USD: "3"}, Repository: RepositoryFacts{StartUnixNano: &inside}},
		{SessionID: "yesterday", ComputedCost: &pricing.Cost{Status: "known", USD: "9"}, Repository: RepositoryFacts{StartUnixNano: &before}},
	}
	got := evaluateBudget(sessions, &Budget{CeilingUSD: "5", Window: "day", Day: day})
	if got.Status != "within" || got.TotalUSD != "3" || got.Sessions != 1 || got.Day != "2026-11-12" || got.Window != "day" {
		t.Fatalf("%+v", got)
	}
	// A session with no start time cannot be placed in the day, so it
	// excludes the comparison instead of disappearing from the total.
	untimed := append(sessions, SessionSummary{SessionID: "untimed", ComputedCost: &pricing.Cost{Status: "known", USD: "9"}})
	placed := evaluateBudget(untimed, &Budget{CeilingUSD: "5", Window: "day", Day: day})
	if placed.Status != "unknown" || placed.TotalUSD != "" || placed.UnknownCost != 1 || placed.Sessions != 2 {
		t.Fatalf("unplaced session %+v", placed)
	}
	over := evaluateBudget(sessions, &Budget{CeilingUSD: "2", Window: "day", Day: day})
	if over.Status != "over" || over.TotalUSD != "3" {
		t.Fatalf("%+v", over)
	}
	unknownStart := day.Add(4 * time.Hour).UnixNano()
	sessions = append(sessions, SessionSummary{
		SessionID: "blank", ComputedCost: nil, Repository: RepositoryFacts{StartUnixNano: &unknownStart},
	})
	blank := evaluateBudget(sessions, &Budget{CeilingUSD: "5", Window: "day", Day: day})
	if blank.Status != "unknown" || blank.TotalUSD != "" || blank.UnknownCost != 1 || blank.Sessions != 2 {
		t.Fatalf("%+v", blank)
	}
}

func TestBudgetDayWindowAllSessionsUnplaced(t *testing.T) {
	day := time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC)
	sessions := []SessionSummary{
		{SessionID: "a", ComputedCost: &pricing.Cost{Status: "known", USD: "1"}},
		{SessionID: "b", ComputedCost: &pricing.Cost{Status: "known", USD: "2"}},
	}
	got := evaluateBudget(sessions, &Budget{CeilingUSD: "5", Window: "day", Day: day})
	if got.Status != "unknown" || got.TotalUSD != "" || got.UnknownCost != 2 || got.Sessions != 2 {
		t.Fatalf("%+v", got)
	}
	if got.Reason == "no sessions" || got.Reason != unknownCostPhrase(2) {
		t.Fatalf("reason %q", got.Reason)
	}
	one := evaluateBudget(sessions[:1], &Budget{CeilingUSD: "5", Window: "day", Day: day})
	if one.Status != "unknown" || one.TotalUSD != "" || one.UnknownCost != 1 || one.Sessions != 1 || one.Reason != unknownCostPhrase(1) {
		t.Fatalf("single unplaced %+v", one)
	}
	none := evaluateBudget(nil, &Budget{CeilingUSD: "5", Window: "day", Day: day})
	if none.Status != "unknown" || none.TotalUSD != "" || none.UnknownCost != 0 || none.Sessions != 0 || none.Reason != "no sessions" {
		t.Fatalf("empty window %+v", none)
	}
	store := evaluateBudget(nil, &Budget{CeilingUSD: "5", Window: "store"})
	if store.Status != "unknown" || store.TotalUSD != "" || store.UnknownCost != 0 || store.Sessions != 0 || store.Reason != "no sessions" {
		t.Fatalf("empty store %+v", store)
	}
}

func TestBudgetDecimalKeepsALongTerminatingValue(t *testing.T) {
	// 25 fractional digits. A fixed-width rendering rounds this and the
	// round trip would reject a value that terminates exactly.
	usd := "0.0000000000000000000000001"
	rat, ok := new(big.Rat).SetString(usd)
	if !ok {
		t.Fatal(usd)
	}
	got, ok := budgetDecimal(rat)
	if !ok || got != usd {
		t.Fatalf("ok=%v got %q", ok, got)
	}
	// 1/3 does not terminate in base 10, so it is not a cost to print.
	if text, ok := budgetDecimal(big.NewRat(1, 3)); ok {
		t.Fatalf("1/3 rendered as %q", text)
	}
}

func TestNoBudgetLeavesTheReportUnchanged(t *testing.T) {
	report := Report{
		Sessions: []SessionSummary{{
			SessionID:    "a",
			ComputedCost: &pricing.Cost{Status: "known", USD: "1"},
		}},
		Unattributed:    []Unattributed{},
		CostPerAccepted: &CostPerAccepted{Status: "unknown", Reason: "no merged outcome", Sessions: 1},
	}
	before, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	ApplyBudget(&report, nil)
	after, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) || bytes.Contains(after, []byte(`"budget"`)) {
		t.Fatalf("nil budget changed the report: %s", after)
	}
	loaded, err := LoadBudget("")
	if err != nil || loaded != nil {
		t.Fatalf("empty path %+v %v", loaded, err)
	}
}

func TestLoadBudgetFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "budget.json")
	if err := os.WriteFile(path, []byte("{\"ceiling_usd\":\"1.50\",\"window\":\"day\",\"day\":\"2026-11-12\"}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := LoadBudget(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.CeilingUSD != "1.50" || got.Window != "day" || got.Day.Format("2006-01-02") != "2026-11-12" || got.Day.Location() != time.UTC {
		t.Fatalf("%+v", got)
	}
	bad := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(bad, []byte("{\"ceiling_usd\":\"-1\",\"window\":\"store\"}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadBudget(bad); err == nil {
		t.Fatal("negative ceiling accepted")
	}
}
