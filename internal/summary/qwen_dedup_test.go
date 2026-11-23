package summary

import (
	"reflect"
	"testing"

	"github.com/paulpham157/sdf/internal/record"
)

// A retried or re-sent OTLP export stores Qwen Code logs and spans again
// under new row ids. qwen-code.api_response tokens and Qwen tool calls are
// still counted once: logs by exactCopyKey (an exact copy only), spans by
// trace id + span id. Two real responses with equal counts both count.

func TestQwenResentSampleCountsTokensAndToolCallsOnce(t *testing.T) {
	logs := loadSignal(t, "logs", "qwen_logs.json")
	once := qwenSessionOf(t, Summarize(logs, false))
	twice := qwenSessionOf(t, Summarize(resend(logs), false))
	if deref(once.TokensSource) != "logs "+qwenAPIResponse || once.Tokens.Len() == 0 {
		t.Fatalf("source %q %v", deref(once.TokensSource), once.Tokens)
	}
	if !reflect.DeepEqual(once.Tokens, twice.Tokens) {
		t.Errorf("tokens %v after a re-sent export, want %v", twice.Tokens.Vals, once.Tokens.Vals)
	}
	all := qwenRows(t)
	onceAll := qwenSessionOf(t, Summarize(all, false))
	twiceAll := qwenSessionOf(t, Summarize(resend(all), false))
	if len(onceAll.ToolCalls) == 0 {
		t.Fatal("sample has no tool calls")
	}
	if !reflect.DeepEqual(onceAll.ToolCalls, twiceAll.ToolCalls) {
		t.Errorf("%d tool calls once, %d after a re-sent export", len(onceAll.ToolCalls), len(twiceAll.ToolCalls))
	}
	if !reflect.DeepEqual(onceAll.Tokens, twiceAll.Tokens) {
		t.Errorf("tokens %v after a re-sent export, want %v", twiceAll.Tokens.Vals, onceAll.Tokens.Vals)
	}
	logOnly := qwenSessionOf(t, Summarize(logs, false))
	logTwice := qwenSessionOf(t, Summarize(resend(logs), false))
	if !reflect.DeepEqual(logOnly.ToolCalls, logTwice.ToolCalls) {
		t.Errorf("logs only: %d tool calls once, %d after a re-sent export", len(logOnly.ToolCalls), len(logTwice.ToolCalls))
	}
}

func qwenResponse(id, when int64, stamp string, input, output int) record.Record {
	attrs := map[string]any{
		"prompt_id": qwenSession + "########1", "model": "qwen3-coder-plus", "duration_ms": 900, "status_code": 200,
		"input_token_count": input, "output_token_count": output, "cached_content_token_count": 0,
		"thoughts_token_count": 0, "total_token_count": input + output,
	}
	if stamp != "" {
		attrs["event.timestamp"] = stamp
	}
	rec := qwenLog(qwenAPIResponse, when, attrs)
	rec.ID = id
	return rec
}

func TestQwenDistinctEqualCountResponsesStaySeparate(t *testing.T) {
	rows := []record.Record{
		qwenResponse(1, 1_000, "2027-01-15T08:00:04.000Z", 1200, 80),
		qwenResponse(2, 2_000, "2027-01-15T08:00:09.500Z", 1200, 80),
		qwenResponse(3, 2_000, "2027-01-15T08:00:09.600Z", 1200, 80),
	}
	for name, rows := range map[string][]record.Record{"once": rows, "re-sent": resend(rows)} {
		s := qwenSessionOf(t, Summarize(rows, false))
		if s.Tokens.Vals["input_token_count"] != 3600 || s.Tokens.Vals["output_token_count"] != 240 {
			t.Fatalf("%s: tokens %v, want three responses once each", name, s.Tokens)
		}
	}
	// A log with no event.timestamp and no log time is never merged.
	bare := func(id int64) record.Record {
		rec := qwenResponse(id, 0, "", 10, 1)
		rec.TimeUnixNano = nil
		return rec
	}
	s := qwenSessionOf(t, Summarize([]record.Record{bare(1), bare(2)}, false))
	if s.Tokens.Vals["input_token_count"] != 20 {
		t.Fatalf("undated logs merged: %v", s.Tokens)
	}
}

func TestQwenDistinctToolCallLogsAreAllListed(t *testing.T) {
	call := func(id, when int64, stamp string) record.Record {
		rec := qwenLog(qwenToolCallLog, when, map[string]any{
			"event.timestamp": stamp, "function_name": "read_file", "duration_ms": 14, "success": true, "decision": "auto_accept",
		})
		rec.ID = id
		return rec
	}
	rows := []record.Record{
		call(1, 1_000, "2027-01-15T08:00:04.400Z"),
		call(2, 2_000, "2027-01-15T08:00:05.400Z"),
		call(3, 2_000, "2027-01-15T08:00:05.500Z"),
	}
	for name, rows := range map[string][]record.Record{"once": rows, "re-sent": resend(rows)} {
		if got := len(qwenSessionOf(t, Summarize(rows, false)).ToolCalls); got != 3 {
			t.Fatalf("%s: %d tool calls, want 3", name, got)
		}
	}
}

func qwenTestSpan(trace, id, name string, attrs map[string]any) record.Record {
	all := map[string]any{"session.id": qwenSession}
	for k, v := range attrs {
		all[k] = v
	}
	return record.Record{
		Signal: "traces", ServiceName: record.Str("qwen-code"), SessionID: record.Str(qwenSession),
		Name: record.Str(name), TraceID: record.Str(trace), SpanID: record.Str(id),
		StartUnixNano: record.Int64(1_000_000_000), EndUnixNano: record.Int64(3_000_000_000), StatusCode: "UNSET",
		Attributes: all,
	}
}

func TestQwenResentSpansCountOnce(t *testing.T) {
	usage := map[string]any{"gen_ai.request.model": "qwen3-max", "gen_ai.usage.input_tokens": 1200, "gen_ai.usage.output_tokens": 90}
	tool := map[string]any{"gen_ai.tool.name": "read_file"}
	rows := []record.Record{
		qwenTestSpan("t", "a", qwenLLMRequest, usage),
		qwenTestSpan("t", "b", qwenLLMRequest, usage), // another request, same counts
		qwenTestSpan("t", "c", qwenToolSpan, tool),
		qwenTestSpan("t", "d", qwenToolSpan, tool), // another call of the same tool
	}
	for name, rows := range map[string][]record.Record{"once": rows, "re-sent": resend(rows)} {
		s := qwenSessionOf(t, Summarize(rows, false))
		if deref(s.TokensSource) != "traces "+qwenLLMRequest || s.Tokens.Vals["gen_ai.usage.input_tokens"] != 2400 {
			t.Fatalf("%s: %q %v", name, deref(s.TokensSource), s.Tokens)
		}
		if len(s.ToolCalls) != 2 {
			t.Fatalf("%s: %d tool calls, want 2", name, len(s.ToolCalls))
		}
	}
}

func TestQwenSpansWithoutIDsAreAllKept(t *testing.T) {
	usage := map[string]any{"gen_ai.request.model": "qwen3-max", "gen_ai.usage.input_tokens": 100, "gen_ai.usage.output_tokens": 5}
	tool := map[string]any{"gen_ai.tool.name": "read_file"}
	var rows []record.Record
	for _, name := range []string{qwenLLMRequest, qwenToolSpan} {
		attrs := usage
		if name == qwenToolSpan {
			attrs = tool
		}
		noSpan := qwenTestSpan("t", "", name, attrs)
		noSpan.SpanID = nil
		noTrace := qwenTestSpan("", "a", name, attrs)
		noTrace.TraceID = nil
		emptyTrace := qwenTestSpan("", "a", name, attrs)
		rows = append(rows, noSpan, noTrace, emptyTrace)
	}
	// Each row and its re-sent copy are all kept: six llm_request spans
	// and six tool spans.
	s := qwenSessionOf(t, Summarize(resend(rows), false))
	if deref(s.TokensSource) != "traces "+qwenLLMRequest || s.Tokens.Vals["gen_ai.usage.input_tokens"] != 600 || s.Tokens.Vals["gen_ai.usage.output_tokens"] != 30 {
		t.Fatalf("%q %v, want all six llm_request rows", deref(s.TokensSource), s.Tokens)
	}
	if len(s.ToolCalls) != 6 {
		t.Fatalf("%d tool calls, want all six tool rows", len(s.ToolCalls))
	}
}

func TestQwenResentInteractionSpansCountDurationOnce(t *testing.T) {
	first := qwenTestSpan("t1", "i1", qwenInteraction, nil)
	second := qwenTestSpan("t2", "i1", qwenInteraction, nil) // same span id, another trace
	second.StartUnixNano = record.Int64(5_000_000_000)
	second.EndUnixNano = record.Int64(6_000_000_000)
	rows := []record.Record{first, second}
	for name, rows := range map[string][]record.Record{"once": rows, "re-sent": resend(rows)} {
		s := qwenSessionOf(t, Summarize(rows, false))
		if deref(s.DurationSource) != "traces "+qwenInteraction || s.DurationMS == nil || *s.DurationMS != 3000 {
			t.Fatalf("%s: duration %v %q, want 2000 + 1000 ms", name, derefFloat(s.DurationMS), deref(s.DurationSource))
		}
	}
	// Interaction spans without both ids are never merged.
	bare := qwenTestSpan("t1", "", qwenInteraction, nil)
	bare.SpanID = nil
	s := qwenSessionOf(t, Summarize(resend([]record.Record{bare}), false))
	if s.DurationMS == nil || *s.DurationMS != 4000 {
		t.Fatalf("duration %v, want both id-less copies", derefFloat(s.DurationMS))
	}
}

func TestQwenResentSampleDurationOnce(t *testing.T) {
	once := qwenSessionOf(t, Summarize(qwenRows(t), false))
	twice := qwenSessionOf(t, Summarize(resend(qwenRows(t)), false))
	if once.DurationMS == nil || deref(once.DurationSource) != "traces "+qwenInteraction {
		t.Fatalf("duration %v %q", derefFloat(once.DurationMS), deref(once.DurationSource))
	}
	if twice.DurationMS == nil || *twice.DurationMS != *once.DurationMS {
		t.Fatalf("duration %v after a re-sent export, want %v", derefFloat(twice.DurationMS), *once.DurationMS)
	}
}
