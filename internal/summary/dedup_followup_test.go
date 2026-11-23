package summary

import (
	"strings"
	"testing"

	"github.com/paulpham157/sdf/internal/record"
)

// Follow-ups to #250 (issue #248): the Copilot span index and copilotSpans
// agree on which copy of a re-sent span is used, spans without ids are never
// merged, and the exact-copy keys cannot be forged by values that contain the
// separators.

func TestCopilotSpanIndexKeepsFirstCopy(t *testing.T) {
	first := copilotSpan("t1", "b1", "a1", "execute_tool task", 1, 2, map[string]any{"copy": "first"})
	second := first
	second.ID = 2
	second.Attributes = map[string]any{"copy": "second"}
	got := copilotSpanIndex([]record.Record{first, second})[copilotSpanKey(first.TraceID, "b1")]
	if got.Attributes["copy"] != "first" {
		t.Fatalf("index kept %v, copilotSpans keeps the first copy", got.Attributes)
	}
}

func TestCopilotResentParentWithDifferentAttrsKeepsFirstCopyTopology(t *testing.T) {
	// Older build: no server.address and no github.copilot.default, so a span
	// is top-level only when no invoke_agent is above it. A later copy of the
	// execute_tool span that lost its parent id must not turn the subagent
	// below it into a top-level span.
	const s1 = int64(1_000_000_000)
	root := copilotSpan("t1", "a1", "", "invoke_agent", s1, 5*s1, map[string]any{
		"gen_ai.usage.input_tokens": 900, "gen_ai.usage.output_tokens": 40,
	})
	tool := copilotSpan("t1", "b1", "a1", "execute_tool task", 2*s1, 4*s1, nil)
	sub := copilotSpan("t1", "c1", "b1", "invoke_agent explore", 2*s1, 3*s1, map[string]any{
		"gen_ai.usage.input_tokens": 300, "gen_ai.usage.output_tokens": 10,
	})
	toolCopy := tool
	toolCopy.ID = 9
	toolCopy.ParentSpanID = nil
	s := copilotSessionOf(t, Summarize([]record.Record{root, tool, sub, toolCopy}, false))
	if deref(s.TokensSource) != "traces invoke_agent github-copilot" {
		t.Fatalf("source %q", deref(s.TokensSource))
	}
	if s.Tokens.Vals["input_tokens"] != 900 || s.Tokens.Vals["output_tokens"] != 40 {
		t.Fatalf("tokens %v: the subagent was counted as top-level", s.Tokens)
	}
}

func TestCopilotResentChatSpanWithDifferentAttrsKeepsFirstCopy(t *testing.T) {
	first := copilotSpan("t1", "c1", "", "chat gpt-5", 1, 2, map[string]any{"gen_ai.usage.input_tokens": 100, "gen_ai.usage.output_tokens": 5})
	second := first
	second.ID = 2
	second.Attributes = map[string]any{"gen_ai.conversation.id": copilotSession, "gen_ai.usage.input_tokens": 999, "gen_ai.usage.output_tokens": 99}
	s := copilotSessionOf(t, Summarize([]record.Record{first, second}, false))
	if s.Tokens.Vals["input_tokens"] != 100 || s.Tokens.Vals["output_tokens"] != 5 {
		t.Fatalf("tokens %v, want the first copy only", s.Tokens)
	}
}

func TestCopilotSpansWithoutIDsAreAllKept(t *testing.T) {
	usage := map[string]any{"gen_ai.usage.input_tokens": 100, "gen_ai.usage.output_tokens": 5}
	noSpan := copilotSpan("t1", "", "", "chat gpt-5", 1, 2, usage)
	noSpan.SpanID = nil
	noTrace := copilotSpan("", "c1", "", "chat gpt-5", 1, 2, usage)
	noTrace.TraceID = nil
	emptyTrace := copilotSpan("", "c1", "", "chat gpt-5", 1, 2, usage)
	rows := resend([]record.Record{noSpan, noTrace, emptyTrace})
	s := copilotSessionOf(t, Summarize(rows, false))
	if s.Tokens.Vals["input_tokens"] != 600 || s.Tokens.Vals["output_tokens"] != 30 {
		t.Fatalf("tokens %v, want all six rows without both ids", s.Tokens)
	}
}

func TestFreezeIsUnambiguous(t *testing.T) {
	pairs := [][2]map[string]any{
		{{"a": "x;b=s:y"}, {"a": "x", "b": "y"}},
		{{"a=s:x;b": "y"}, {"a": "x", "b": "y"}},
		{{"a": []any{"x,s:y"}}, {"a": []any{"x", "y"}}},
		{{"a": map[string]any{"k": "v};b=s:w"}}, {"a": map[string]any{"k": "v"}, "b": "w"}},
	}
	for _, p := range pairs {
		if freeze(p[0]) == freeze(p[1]) {
			t.Fatalf("freeze(%v) == freeze(%v) = %q", p[0], p[1], freeze(p[0]))
		}
	}
}

func TestGeminiResponseKeyIsUnambiguous(t *testing.T) {
	a := geminiResponse(1, 1_000, "2027-01-15T08:00:04.000Z", 10, 1)
	b := a
	a.SessionID = record.Str("s|1000")
	a.TimeUnixNano = record.Int64(1)
	a.Body = record.Str("x")
	b.SessionID = record.Str("s")
	b.TimeUnixNano = record.Int64(1000)
	b.Body = record.Str("1|x")
	if ka, kb := geminiResponseKey(a), geminiResponseKey(b); ka == kb {
		t.Fatalf("distinct logs share key %q", ka)
	}
	// Two distinct responses forged into one key are both counted.
	b.SessionID = a.SessionID
	b.TimeUnixNano = a.TimeUnixNano
	b.Body = a.Body
	b.Attributes = map[string]any{}
	for k, v := range a.Attributes {
		b.Attributes[k] = v
	}
	a.Body = record.Str("x|n:1")
	b.Body = record.Str("x")
	if geminiResponseKey(a) == geminiResponseKey(b) {
		t.Fatal("body and attributes are not separated")
	}
}

func TestGeminiResentDistinctEqualCountResponsesStaySeparate(t *testing.T) {
	rows := []record.Record{
		geminiResponse(1, 1_000, "2027-01-15T08:00:04.000Z", 1200, 80),
		geminiResponse(2, 2_000, "2027-01-15T08:00:09.500Z", 1200, 80),
		geminiResponse(3, 2_000, "2027-01-15T08:00:09.600Z", 1200, 80),
	}
	s := geminiSessionOf(t, Summarize(resend(rows), false))
	if s.Tokens.Vals["input_token_count"] != 3600 || s.Tokens.Vals["output_token_count"] != 240 {
		t.Fatalf("tokens %v, want three responses once each", s.Tokens)
	}
}

func TestGeminiResentFullSampleLeavesMetricTokensUnchanged(t *testing.T) {
	once := geminiSessionOf(t, Summarize(geminiRows(t), false))
	twice := geminiSessionOf(t, Summarize(resend(geminiRows(t)), false))
	if deref(once.TokensSource) != "metrics "+geminiTokenMetric || deref(twice.TokensSource) != deref(once.TokensSource) {
		t.Fatalf("source %q / %q", deref(once.TokensSource), deref(twice.TokensSource))
	}
	if twice.Tokens.Len() != once.Tokens.Len() {
		t.Fatalf("tokens %v want %v", twice.Tokens, once.Tokens)
	}
	for _, k := range once.Tokens.Keys {
		if twice.Tokens.Vals[k] != once.Tokens.Vals[k] {
			t.Fatalf("tokens[%s]=%v after a re-sent export, want %v", k, twice.Tokens.Vals[k], once.Tokens.Vals[k])
		}
	}
}

func TestFreezeKeepsTrailingIntegerZeros(t *testing.T) {
	// strconvFormat trimmed "0" from integers, so 900 and 9 (and 0 and "")
	// encoded alike.
	if freeze(map[string]any{"duration_ms": 900}) == freeze(map[string]any{"duration_ms": 9}) {
		t.Fatal("900 and 9 share an encoding")
	}
	if got := strconvFormat(100); got != "100" {
		t.Fatalf("strconvFormat(100) = %q", got)
	}
	if got := strconvFormat(1.5); got != "1.5" {
		t.Fatalf("strconvFormat(1.5) = %q", got)
	}
}

func TestGeminiResponsesDifferingOnlyInTrailingZerosStaySeparate(t *testing.T) {
	a := geminiResponse(1, 1_000, "2027-01-15T08:00:04.000Z", 1200, 80)
	b := geminiResponse(2, 1_000, "2027-01-15T08:00:04.000Z", 1200, 80)
	b.Attributes["duration_ms"] = 9000
	s := geminiSessionOf(t, Summarize([]record.Record{a, b}, false))
	if s.Tokens.Vals["input_token_count"] != 2400 {
		t.Fatalf("tokens %v: duration_ms 900 and 9000 were merged", s.Tokens)
	}
}

func TestCopilotAIUNoteKeepsTrailingZeros(t *testing.T) {
	root := copilotSpan("t1", "a1", "", "invoke_agent", 1, 2, map[string]any{
		"gen_ai.agent.id": "github.copilot.default", "github.copilot.nano_aiu": 10_000_000_000,
	})
	s := copilotSessionOf(t, Summarize([]record.Record{root}, false))
	for _, m := range s.Missing {
		if m.Signal == "agent cost" && !strings.Contains(m.Reason, " report 10 AIU") {
			t.Fatalf("AIU note: %s", m.Reason)
		}
	}
}
