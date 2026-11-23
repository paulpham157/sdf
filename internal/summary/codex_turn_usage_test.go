package summary

import (
	"strings"
	"testing"

	"github.com/paulpham157/sdf/internal/record"
)

// codexTurnUsage returns the six codex.turn.token_usage histogram points one
// Codex turn exports for openai/gpt-5.4, in the Codex CLI 0.171.0 shape
// (codex-rs/core/src/state/turn_token_usage.rs): one sample per turn and
// model, summed over every Responses call of the turn, tagged with
// token_type, tmp_mem_enabled, and the session metadata tags. There is no
// turn.id and no conversation.id on this instrument. A delta point that
// merges two turns of one export interval looks the same, with the sums
// added. session is nil for a point attributed by time, as in the store.
func codexTurnUsage(session *string, input, cached, output float64, when, order int64) []record.Record {
	values := []struct {
		kind  string
		value float64
	}{
		{"total", input + output},
		{"input", input},
		{"cached_input", cached},
		{"cache_write_input", 0},
		{"output", output},
		{"reasoning_output", 0},
	}
	out := make([]record.Record, 0, len(values))
	for i, v := range values {
		out = append(out, metric("codex.turn.token_usage", v.value, session, "delta", "histogram", map[string]any{
			"token_type":      v.kind,
			"tmp_mem_enabled": "false",
			"session_source":  "exec",
			"originator":      "codex_exec",
			"model":           "openai/gpt-5.4",
			"app.version":     "0.171.0",
		}, when, order+int64(i), "codex_exec"))
	}
	return out
}

// codexTimedResponse is codexGPT54Response with a log time, so metric points
// without a session id are attributed to its session by time.
func codexTimedResponse(id int64, when int64, stamp, input string, cached int, output string) record.Record {
	rec := codexGPT54Response(id, stamp, input, cached, output)
	rec.TimeUnixNano = record.Int64(when)
	return rec
}

func TestCodexTurnUsageAloneStaysUnknownAtThreshold(t *testing.T) {
	// Issue #222: a metric-only session. Two turns of 195000 and 105000
	// input, each below min_prompt_tokens 272000, sum to 300000. The metric
	// holds one sample per turn summed over that turn's calls and no turn.id,
	// so per-request counts cannot be recovered and the #185 rule applies.
	session := record.Str("019ab6f2-3c1d-7e40-9b51-5d2f8a0c4e11")
	records := append(codexTurnUsage(session, 195000, 170000, 1100, 1_000, 1), codexTurnUsage(session, 105000, 95000, 700, 2_000, 10)...)
	got := Summarize(records, false).Sessions[0]
	if deref(got.TokensSource) != "metrics codex.turn.token_usage" {
		t.Fatalf("source %q", deref(got.TokensSource))
	}
	if in, _ := got.Tokens.Get("input"); in != 300000 {
		t.Fatalf("tokens %v", got.Tokens.Vals)
	}
	if requests := sessionRequests(records, deref(got.TokensSource), got.Tokens); requests != nil {
		t.Fatalf("metric-only session has per-request items %v", requests)
	}
	cost := got.ComputedCost
	if cost == nil || cost.Status != "unknown" || !strings.Contains(cost.Reason, "at or above min_prompt_tokens 272000") {
		t.Fatalf("computed cost %+v", cost)
	}
	// Below the threshold the aggregate is priced at base rates:
	// 25000*0.0000025 + 170000*0.00000025 + 1100*0.000015 = 0.1215.
	below := Summarize(codexTurnUsage(session, 195000, 170000, 1100, 1_000, 1), false).Sessions[0].ComputedCost
	if below == nil || below.Status != "known" || below.USD != "0.1215" {
		t.Fatalf("below threshold %+v", below)
	}
}

func TestCodexTurnUsageWithLogsPricesEachCall(t *testing.T) {
	// A mixed session: the metric and the response.completed logs both
	// arrive. The logs (95000, 100000, 105000 input; the first two calls are
	// turn 1) add up to the metric totals, so each call is priced on its own
	// at base rates, the same $0.18075 as the logs alone (#213). The
	// displayed totals stay the metric's and are not doubled. The second
	// variant merges both turns into one delta point (one export interval);
	// the third stores one log twice.
	session := "019ab6f2-3c1d-7e40-9b51-5d2f8a0c4e11"
	logs := []record.Record{
		codexTimedResponse(101, 1_000_000_000, "2026-11-23T10:02:11.418Z", "95000", 80000, "500"),
		codexTimedResponse(102, 2_000_000_000, "2026-11-23T10:02:37.902Z", "100000", 90000, "600"),
		codexTimedResponse(103, 3_000_000_000, "2026-11-23T10:03:05.266Z", "105000", 95000, "700"),
	}
	perTurn := append(codexTurnUsage(nil, 195000, 170000, 1100, 2_100_000_000, 1), codexTurnUsage(nil, 105000, 95000, 700, 3_100_000_000, 10)...)
	merged := codexTurnUsage(nil, 300000, 265000, 1800, 3_100_000_000, 1)
	duplicate := codexTimedResponse(104, 3_000_000_000, "2026-11-23T10:03:05.266Z", "105000", 95000, "700")
	cases := map[string][]record.Record{
		"per turn":  append(append([]record.Record{}, logs...), perTurn...),
		"merged":    append(append([]record.Record{}, logs...), merged...),
		"duplicate": append(append(append([]record.Record{}, logs...), duplicate), perTurn...),
	}
	for name, records := range cases {
		report := Summarize(records, false)
		if len(report.Sessions) != 1 || report.Sessions[0].SessionID != session {
			t.Fatalf("%s: sessions %+v unattributed %+v", name, report.Sessions, report.Unattributed)
		}
		got := report.Sessions[0]
		if deref(got.TokensSource) != "metrics codex.turn.token_usage" {
			t.Fatalf("%s: source %q", name, deref(got.TokensSource))
		}
		for key, want := range map[string]float64{"input": 300000, "cached_input": 265000, "output": 1800, "total": 301800} {
			if v, _ := got.Tokens.Get(key); v != want {
				t.Fatalf("%s: %s = %v, want %v; %v", name, key, v, want, got.Tokens.Vals)
			}
		}
		if _, ok := got.Tokens.Get("input_token_count"); ok {
			t.Fatalf("%s: log counts added to the metric totals %v", name, got.Tokens.Vals)
		}
		cost := got.ComputedCost
		if cost == nil || cost.Status != "known" || cost.USD != "0.18075" || cost.CatalogID != "openai/gpt-5.4" {
			t.Fatalf("%s: computed cost %+v", name, cost)
		}
		if !strings.HasPrefix(cost.Method, "sum of 3 per-request costs") {
			t.Fatalf("%s: method %q", name, cost.Method)
		}
	}
}

func TestCodexTurnUsageWithLogsOneCallAboveThreshold(t *testing.T) {
	// One turn of two calls: 280000 input (at or above 272000, override
	// rates) and 20000 (base). The turn alone is 300000; priced per turn it
	// would be all override, and the aggregate is unknown. Per call it is
	// 0.5225 + 0.0515 = 0.574, the same as the logs alone (#213).
	records := []record.Record{
		codexTimedResponse(101, 1_000_000_000, "2026-11-23T11:15:42.031Z", "280000", 200000, "1000"),
		codexTimedResponse(102, 2_000_000_000, "2026-11-23T11:16:20.775Z", "20000", 0, "100"),
	}
	records = append(records, codexTurnUsage(nil, 300000, 200000, 1100, 2_100_000_000, 1)...)
	got := Summarize(records, false).Sessions[0]
	if deref(got.TokensSource) != "metrics codex.turn.token_usage" {
		t.Fatalf("source %q", deref(got.TokensSource))
	}
	cost := got.ComputedCost
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
}

func TestCodexTurnUsageWithLogsThatDoNotAddUpStaysUnknown(t *testing.T) {
	// The logs miss the third call (for example a dropped log export), so
	// they do not add up to the metric totals and cannot split them. The
	// metric aggregate is priced as before: unknown at the threshold. A log
	// count that is not in the metric (extra logs) is refused the same way.
	session := "019ab6f2-3c1d-7e40-9b51-5d2f8a0c4e11"
	metrics := append(codexTurnUsage(nil, 195000, 170000, 1100, 2_100_000_000, 1), codexTurnUsage(nil, 105000, 95000, 700, 3_100_000_000, 10)...)
	missing := append([]record.Record{
		codexTimedResponse(101, 1_000_000_000, "2026-11-23T10:02:11.418Z", "95000", 80000, "500"),
		codexTimedResponse(102, 3_000_000_000, "2026-11-23T10:02:37.902Z", "100000", 90000, "600"),
	}, metrics...)
	extra := append([]record.Record{
		codexTimedResponse(101, 1_000_000_000, "2026-11-23T10:02:11.418Z", "95000", 80000, "500"),
		codexTimedResponse(102, 2_000_000_000, "2026-11-23T10:02:37.902Z", "100000", 90000, "600"),
		codexTimedResponse(103, 2_500_000_000, "2026-11-23T10:03:05.266Z", "105000", 95000, "700"),
		codexTimedResponse(104, 3_000_000_000, "2026-11-23T10:03:40.120Z", "1000", 0, "10"),
	}, metrics...)
	for name, records := range map[string][]record.Record{"missing": missing, "extra": extra} {
		report := Summarize(records, false)
		if len(report.Sessions) != 1 || report.Sessions[0].SessionID != session {
			t.Fatalf("%s: sessions %+v", name, report.Sessions)
		}
		got := report.Sessions[0]
		if in, _ := got.Tokens.Get("input"); in != 300000 || deref(got.TokensSource) != "metrics codex.turn.token_usage" {
			t.Fatalf("%s: %q %v", name, deref(got.TokensSource), got.Tokens.Vals)
		}
		if requests := sessionRequests(records, deref(got.TokensSource), got.Tokens); requests != nil {
			t.Fatalf("%s: requests %v", name, requests)
		}
		cost := got.ComputedCost
		if cost == nil || cost.Status != "unknown" || !strings.Contains(cost.Reason, "at or above min_prompt_tokens 272000") {
			t.Fatalf("%s: computed cost %+v", name, cost)
		}
	}
}
