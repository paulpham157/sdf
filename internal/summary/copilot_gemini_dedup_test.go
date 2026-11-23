package summary

import (
	"strings"
	"testing"

	"github.com/paulpham157/sdf/internal/record"
)

// A retried or re-sent OTLP export stores the same span or log again under a
// new row id. Copilot chat / invoke_agent spans and Gemini CLI
// gemini_cli.api_response logs must then be counted once, like the Claude and
// Grok api_request logs (#214) and OpenCode / Pi spans. Two genuinely
// different spans or logs, even with the same counts, are still both counted.

// resend returns rows followed by a second copy of each, with new row ids, as
// a retried export batch is stored.
func resend(rows []record.Record) []record.Record {
	out := append([]record.Record{}, rows...)
	for i, rec := range rows {
		rec.ID = int64(1000 + i)
		out = append(out, rec)
	}
	return out
}

func TestCopilotResentTracesCountChatTokensOnce(t *testing.T) {
	once := copilotSessionOf(t, Summarize(copilotRows(t), false))
	twice := copilotSessionOf(t, Summarize(resend(copilotRows(t)), false))
	if deref(twice.TokensSource) != "traces chat github-copilot" {
		t.Fatalf("source %q", deref(twice.TokensSource))
	}
	for _, k := range once.Tokens.Keys {
		if twice.Tokens.Vals[k] != once.Tokens.Vals[k] {
			t.Fatalf("tokens[%s]=%v after a re-sent export, want %v (%v)", k, twice.Tokens.Vals[k], once.Tokens.Vals[k], twice.Tokens)
		}
	}
	if twice.Tokens.Len() != once.Tokens.Len() {
		t.Fatalf("tokens %v want %v", twice.Tokens, once.Tokens)
	}
	if twice.DurationMS == nil || once.DurationMS == nil || *twice.DurationMS != *once.DurationMS {
		t.Fatalf("duration %v want %v", twice.DurationMS, once.DurationMS)
	}
	for _, m := range twice.Missing {
		if m.Signal == "agent cost" && !strings.Contains(m.Reason, "1.9 AIU") {
			t.Fatalf("AIU counted twice: %s", m.Reason)
		}
	}
}

func TestCopilotDistinctChatSpansWithSameCountsBothCount(t *testing.T) {
	const s1 = int64(1_000_000_000)
	usage := map[string]any{"gen_ai.request.model": "gpt-5", "gen_ai.usage.input_tokens": 100, "gen_ai.usage.output_tokens": 7}
	root := copilotSpan("t1", "a1", "", "invoke_agent", s1, 9*s1, map[string]any{"gen_ai.agent.id": "github.copilot.default"})
	first := copilotSpan("t1", "c1", "a1", "chat gpt-5", 2*s1, 3*s1, usage)
	// Same span id in another trace is another span (span ids are only
	// unique within a trace).
	otherTrace := copilotSpan("t2", "c1", "", "chat gpt-5", 4*s1, 5*s1, usage)
	sameTrace := copilotSpan("t1", "c2", "a1", "chat gpt-5", 2*s1, 3*s1, usage)
	rows := []record.Record{root, first, otherTrace, sameTrace}
	for name, rows := range map[string][]record.Record{"once": rows, "re-sent": resend(rows)} {
		s := copilotSessionOf(t, Summarize(rows, false))
		if s.Tokens.Vals["input_tokens"] != 300 || s.Tokens.Vals["output_tokens"] != 21 {
			t.Fatalf("%s: tokens %v, want three chat spans", name, s.Tokens)
		}
	}
}

func TestCopilotResentInvokeAgentFallbackCountsOnce(t *testing.T) {
	const s1 = int64(1_000_000_000)
	root := copilotSpan("t1", "a1", "", "invoke_agent", s1, 5*s1, map[string]any{
		"gen_ai.agent.id": "github.copilot.default", "gen_ai.usage.input_tokens": 900, "gen_ai.usage.output_tokens": 40,
		"github.copilot.nano_aiu": 250_000_000,
	})
	next := copilotSpan("t2", "a1", "", "invoke_agent", 6*s1, 8*s1, map[string]any{
		"gen_ai.agent.id": "github.copilot.default", "gen_ai.usage.input_tokens": 900, "gen_ai.usage.output_tokens": 40,
		"github.copilot.nano_aiu": 250_000_000,
	})
	s := copilotSessionOf(t, Summarize(resend([]record.Record{root, next}), false))
	if deref(s.TokensSource) != "traces invoke_agent github-copilot" {
		t.Fatalf("source %q", deref(s.TokensSource))
	}
	if s.Tokens.Vals["input_tokens"] != 1800 || s.Tokens.Vals["output_tokens"] != 80 {
		t.Fatalf("tokens %v, want each top-level span once", s.Tokens)
	}
	for _, m := range s.Missing {
		if m.Signal == "agent cost" && !strings.Contains(m.Reason, " 0.5 AIU") {
			t.Fatalf("AIU: %s", m.Reason)
		}
	}
}

func geminiResponse(id, when int64, stamp string, input, output int) record.Record {
	rec := geminiLog("gemini_cli.api_response", when, map[string]any{
		"event.timestamp": stamp, "prompt_id": geminiSession + "########1",
		"model": "gemini-2.5-flash", "duration_ms": 900, "status_code": 200,
		"input_token_count": input, "output_token_count": output, "cached_content_token_count": 0,
		"thoughts_token_count": 0, "tool_token_count": 0, "total_token_count": input + output,
	})
	rec.ID = id
	return rec
}

func TestGeminiResentAPIResponseLogCountsOnce(t *testing.T) {
	rows := []record.Record{
		geminiResponse(1, 1_000, "2027-01-15T08:00:04.000Z", 1200, 80),
		geminiResponse(2, 2_000, "2027-01-15T08:00:09.500Z", 1500, 20),
	}
	for name, rows := range map[string][]record.Record{"once": rows, "re-sent": resend(rows)} {
		s := geminiSessionOf(t, Summarize(rows, false))
		if deref(s.TokensSource) != "logs gemini_cli.api_response" {
			t.Fatalf("%s: source %q", name, deref(s.TokensSource))
		}
		if s.Tokens.Vals["input_token_count"] != 2700 || s.Tokens.Vals["output_token_count"] != 100 {
			t.Fatalf("%s: tokens %v", name, s.Tokens)
		}
	}
}

func TestGeminiDistinctAPIResponsesWithSameCountsBothCount(t *testing.T) {
	// Same prompt_id, model, and counts: two responses of one prompt (a tool
	// round trip) that differ only in when they happened.
	rows := []record.Record{
		geminiResponse(1, 1_000, "2027-01-15T08:00:04.000Z", 1200, 80),
		geminiResponse(2, 2_000, "2027-01-15T08:00:09.500Z", 1200, 80),
	}
	// Same log time but another event.timestamp is still another response.
	third := geminiResponse(3, 2_000, "2027-01-15T08:00:09.600Z", 1200, 80)
	rows = append(rows, third)
	s := geminiSessionOf(t, Summarize(rows, false))
	if s.Tokens.Vals["input_token_count"] != 3600 || s.Tokens.Vals["output_token_count"] != 240 {
		t.Fatalf("tokens %v, want three responses", s.Tokens)
	}
	// A log with no event.timestamp and no log time is never merged.
	bare := func(id int64) record.Record {
		rec := geminiResponse(id, 0, "", 10, 1)
		rec.TimeUnixNano = nil
		delete(rec.Attributes, "event.timestamp")
		return rec
	}
	s = geminiSessionOf(t, Summarize([]record.Record{bare(1), bare(2)}, false))
	if s.Tokens.Vals["input_token_count"] != 20 {
		t.Fatalf("undated logs merged: %v", s.Tokens)
	}
}

func TestGeminiResentSampleLogsCountOnce(t *testing.T) {
	logs := loadSignal(t, "logs", "gemini_logs.json")
	once := geminiSessionOf(t, Summarize(logs, false))
	twice := geminiSessionOf(t, Summarize(resend(logs), false))
	if deref(once.TokensSource) != "logs gemini_cli.api_response" || once.Tokens.Len() == 0 {
		t.Fatalf("source %q %v", deref(once.TokensSource), once.Tokens)
	}
	for _, k := range once.Tokens.Keys {
		if twice.Tokens.Vals[k] != once.Tokens.Vals[k] {
			t.Fatalf("tokens[%s]=%v after a re-sent export, want %v", k, twice.Tokens.Vals[k], once.Tokens.Vals[k])
		}
	}
}
