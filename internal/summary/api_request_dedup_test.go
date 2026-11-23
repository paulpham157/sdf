package summary

import (
	"math"
	"testing"

	"github.com/paulpham157/sdf/internal/record"
)

// Issue #214: a claude_code.api_request or grok_code.api_request log stored
// twice (for example after an OTLP export retry) is one request. The
// fixtures follow the documented attribute sets: Claude Code's monitoring
// docs (request_id, client_request_id, prompt.id, event.sequence,
// event.timestamp, cost_usd on api_request) and the Grok CLI external OTEL
// guide plus the grok 1.0.92 live capture (event.sequence, prompt.id,
// turn_number, cost_usd_micros; no event.timestamp). Ids are fake.

const claudeDedupSession = "sample-claude-dedup"

func claudeAPIRequestLog(id, when int64, seq int, stamp, requestID string, counts map[string]any) record.Record {
	attributes := map[string]any{
		"event.name":        "api_request",
		"event.timestamp":   stamp,
		"event.sequence":    seq,
		"session.id":        claudeDedupSession,
		"prompt.id":         "6f1c2f0e-0000-4000-8000-000000000214",
		"request_id":        requestID,
		"client_request_id": "c0ffee00-0000-4000-8000-" + requestID[len(requestID)-12:],
		"model":             "anthropic/claude-sonnet-4.5",
		"duration_ms":       2100,
		"speed":             "normal",
		"query_source":      "repl_main_thread",
	}
	for k, v := range counts {
		attributes[k] = v
	}
	return record.Record{
		ID:           id,
		Signal:       "logs",
		ServiceName:  record.Str("claude-code"),
		SessionID:    record.Str(claudeDedupSession),
		Name:         record.Str("claude_code.api_request"),
		Body:         record.Str("claude_code.api_request"),
		TimeUnixNano: record.Int64(when),
		Attributes:   attributes,
		Resource:     map[string]any{"service.name": "claude-code", "sdf.fixture": "docs-schema"},
	}
}

func claudeDedupRequests() []record.Record {
	return []record.Record{
		// Prompt total 5 + 210000 + 0 = 210005 crosses the 200000
		// min_prompt_tokens override of anthropic/claude-sonnet-4.5. Cache
		// writes are 0: that row has two cache-write rates, so a nonzero
		// cache_creation_tokens would leave the cost unknown.
		claudeAPIRequestLog(1, 1_795_400_000_000_000_000, 4, "2026-11-23T12:13:20.000Z", "req_011CVdedup0000000000001", map[string]any{
			"input_tokens": 5, "output_tokens": 400, "cache_read_tokens": 210000, "cache_creation_tokens": 0,
			"cost_usd": 0.13503, "cost_usd_micros": 135030,
		}),
		claudeAPIRequestLog(2, 1_795_400_004_000_000_000, 9, "2026-11-23T12:13:24.000Z", "req_011CVdedup0000000000002", map[string]any{
			"input_tokens": 1200, "output_tokens": 150, "cache_read_tokens": 20000, "cache_creation_tokens": 0,
			"cost_usd": 0.01185, "cost_usd_micros": 11850,
		}),
	}
}

func TestIssue214DuplicateClaudeAPIRequestCountsOnce(t *testing.T) {
	once := claudeDedupRequests()
	dup := once[0]
	dup.ID = 3 // same log stored again under a new row id
	withDuplicate := append(append([]record.Record{}, once...), dup)
	for name, records := range map[string][]record.Record{"once": once, "duplicate": withDuplicate} {
		got := Summarize(records, false).Sessions[0]
		if deref(got.TokensSource) != "logs claude_code.api_request" {
			t.Fatalf("%s: tokens source %s", name, deref(got.TokensSource))
		}
		in, _ := got.Tokens.Get("input_tokens")
		out, _ := got.Tokens.Get("output_tokens")
		read, _ := got.Tokens.Get("cache_read_tokens")
		write, _ := got.Tokens.Get("cache_creation_tokens")
		if in != 1205 || out != 550 || read != 230000 || write != 0 {
			t.Fatalf("%s: tokens %+v", name, got.Tokens.Vals)
		}
		if requests := requestTokens(records, "logs claude_code.api_request"); len(requests) != 2 {
			t.Fatalf("%s: requests %+v", name, requests)
		}
		// Per-request override pricing (#185): the first request is at the
		// override rates, the second at base rates.
		// 5*0.000006 + 210000*0.0000006 + 400*0.0000225 = 0.13503
		// 1200*0.000003 + 20000*0.0000003 + 150*0.000015 = 0.01185
		if got.ComputedCost.Status != "known" || got.ComputedCost.USD != "0.14688" {
			t.Fatalf("%s: computed %+v", name, got.ComputedCost)
		}
		if got.AgentReportedCostUSD == nil || math.Abs(*got.AgentReportedCostUSD-0.14688) > 1e-12 {
			t.Fatalf("%s: agent cost %v", name, got.AgentReportedCostUSD)
		}
	}
}

func TestIssue214DistinctClaudeRequestsWithEqualCountsStaySeparate(t *testing.T) {
	// Two real requests in one prompt can report the same counts and cost.
	// They differ in request_id, event.sequence and timestamps, so both count.
	counts := map[string]any{"input_tokens": 10, "output_tokens": 2, "cache_read_tokens": 0, "cache_creation_tokens": 0, "cost_usd": 0.00006}
	records := []record.Record{
		claudeAPIRequestLog(1, 1_795_400_000_000_000_000, 4, "2026-11-23T12:13:20.000Z", "req_011CVdedup0000000000001", counts),
		claudeAPIRequestLog(2, 1_795_400_001_000_000_000, 7, "2026-11-23T12:13:21.000Z", "req_011CVdedup0000000000002", counts),
	}
	got := Summarize(records, false).Sessions[0]
	if in, _ := got.Tokens.Get("input_tokens"); in != 20 {
		t.Fatalf("tokens %+v", got.Tokens.Vals)
	}
	if len(requestTokens(records, "logs claude_code.api_request")) != 2 {
		t.Fatal("distinct requests were merged")
	}
	if got.AgentReportedCostUSD == nil || math.Abs(*got.AgentReportedCostUSD-0.00012) > 1e-12 {
		t.Fatalf("agent cost %v", got.AgentReportedCostUSD)
	}
}

func TestIssue214ClaudeRequestsWithoutAnyTimestampAreNotMerged(t *testing.T) {
	// With neither event.timestamp nor a log time there is no stable key,
	// so identical-looking records are kept, as before.
	base := record.Record{
		Signal: "logs", ServiceName: record.Str("claude-code"), SessionID: record.Str("s"),
		Name:       record.Str("claude_code.api_request"),
		Attributes: map[string]any{"model": "anthropic/claude-sonnet-4.5", "input_tokens": 1, "output_tokens": 1},
		Resource:   map[string]any{},
	}
	second := base
	second.ID = 2
	if got := requestTokens([]record.Record{base, second}, "logs claude_code.api_request"); len(got) != 2 {
		t.Fatalf("requests %+v", got)
	}
}

func grokDedupRequests(session string) []record.Record {
	shape := func(when int64, seq int, attrs map[string]any) record.Record {
		all := map[string]any{
			"event.sequence": seq, "prompt.id": "0f7a3c52-0000-4000-8000-000000000214",
			"turn_number": 1, "model": "grok-4.7", "duration_ms": 3100, "stop_reason": "tool_use",
			"cache_creation_tokens": 0,
		}
		for k, v := range attrs {
			all[k] = v
		}
		return grokLog("grok_code.api_request", session, when, all)
	}
	return []record.Record{
		// Prompt total 214454 crosses the x-ai/grok-4.7 200000 override.
		shape(1_795_400_000_000_000_000, 3, map[string]any{
			"input_tokens": 214454, "output_tokens": 177, "reasoning_tokens": 121, "cache_read_tokens": 190112,
			"cost_usd_micros": 289604,
		}),
		// First request of the live grok-4.7 session (base rates, 0.014802).
		shape(1_795_400_006_000_000_000, 8, map[string]any{
			"input_tokens": 14454, "output_tokens": 177, "reasoning_tokens": 121, "cache_read_tokens": 10112,
			"cost_usd_micros": 14802,
		}),
	}
}

func TestIssue214DuplicateGrokAPIRequestCountsOnce(t *testing.T) {
	session := "ses_live_grok_dedup"
	once := grokDedupRequests(session)
	dup := once[0]
	dup.ID = 99
	withDuplicate := append(append([]record.Record{}, once...), dup)
	for name, records := range map[string][]record.Record{"once": once, "duplicate": withDuplicate} {
		got := Summarize(records, false).Sessions[0]
		if deref(got.TokensSource) != "logs grok_code.api_request" {
			t.Fatalf("%s: tokens source %s", name, deref(got.TokensSource))
		}
		in, _ := got.Tokens.Get("input_tokens")
		read, _ := got.Tokens.Get("cache_read_tokens")
		out, _ := got.Tokens.Get("output_tokens")
		if in != 228908 || read != 200224 || out != 354 {
			t.Fatalf("%s: tokens %+v", name, got.Tokens.Vals)
		}
		if requests := requestTokens(records, "logs grok_code.api_request"); len(requests) != 2 {
			t.Fatalf("%s: requests %+v", name, requests)
		}
		// (214454-190112)*0.000004 + 190112*0.000001 + 177*0.000012 = 0.289604
		// plus the live base-rate request 0.014802.
		if got.ComputedCost.Status != "known" || got.ComputedCost.USD != "0.304406" {
			t.Fatalf("%s: computed %+v", name, got.ComputedCost)
		}
		if got.AgentReportedCostUSD == nil || math.Abs(*got.AgentReportedCostUSD-0.304406) > 1e-12 {
			t.Fatalf("%s: agent cost %v", name, got.AgentReportedCostUSD)
		}
	}
}

func TestIssue214DuplicateUnnamedGrokAPIRequestCountsOnce(t *testing.T) {
	// The live grok 1.0.92 logs arrived without a name; the event is inferred
	// from its attributes. A duplicate of such a record is dropped too.
	session := "ses_live_unnamed_grok_dedup"
	request := grokUnnamedLog(session, 1_795_400_000_000_000_000, map[string]any{
		"event.sequence": 3, "prompt.id": "0f7a3c52-0000-4000-8000-000000000215", "model": "openai/gpt-4o",
		"input_tokens": 10, "output_tokens": 4, "reasoning_tokens": 2, "cache_read_tokens": 1,
		"cost_usd_micros": 1234,
	})
	got := Summarize([]record.Record{request, request}, false).Sessions[0]
	if in, _ := got.Tokens.Get("input_tokens"); in != 10 {
		t.Fatalf("tokens %+v source=%s", got.Tokens.Vals, deref(got.TokensSource))
	}
	if got.AgentReportedCostUSD == nil || *got.AgentReportedCostUSD != 0.001234 {
		t.Fatalf("agent cost %v", got.AgentReportedCostUSD)
	}
}
