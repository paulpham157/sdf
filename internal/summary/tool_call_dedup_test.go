package summary

import (
	"reflect"
	"testing"

	"github.com/paulpham157/sdf/internal/record"
)

// Issue #258: a retried or re-sent OTLP export stores tool spans and tool
// logs again under new row ids. Each tool call is still listed once: a span by
// trace id + span id, a tool log only when it is an exact copy. Two real
// calls of the same tool with the same result are both listed.

func sampleRows(t *testing.T, files ...[2]string) []record.Record {
	t.Helper()
	var rows []record.Record
	for _, f := range files {
		rows = append(rows, loadSignal(t, f[0], f[1])...)
	}
	return rows
}

// withSpanIDs gives spans that have no ids (claude_traces.json has none) a
// trace and span id, as a real export carries. A span without ids is never
// merged (TestDistinctCopilotToolSpansAreAllListed).
func withSpanIDs(rows []record.Record) []record.Record {
	for i := range rows {
		if rows[i].Signal == "traces" && deref(rows[i].SpanID) == "" {
			rows[i].TraceID = record.Str("4bf92f3577b34da6a3ce929d0e0e4736")
			rows[i].SpanID = record.Str("00f067aa0ba902" + itoa(10+i))
		}
	}
	return rows
}

func allToolCalls(report Report) map[string][]ToolCall {
	out := map[string][]ToolCall{}
	for _, s := range report.Sessions {
		out[s.SessionID] = s.ToolCalls
	}
	return out
}

func TestResentExportListsEachToolCallOnce(t *testing.T) {
	cases := map[string][]record.Record{
		"copilot":          copilotRows(t),
		"gemini":           geminiRows(t),
		"grok":             sampleRows(t, [2]string{"traces", "grok_traces.json"}),
		"pi":               piRows(t),
		"opencode":         opencodeRows(t),
		"claude":           withSpanIDs(sampleRows(t, [2]string{"logs", "claude_logs.json"}, [2]string{"traces", "claude_traces.json"}, [2]string{"metrics", "claude_metrics.json"})),
		"claude logs only": sampleRows(t, [2]string{"logs", "claude_logs.json"}),
		"codex logs":       sampleRows(t, [2]string{"logs", "codex_logs.json"}),
	}
	for name, rows := range cases {
		once := allToolCalls(Summarize(rows, false))
		twice := allToolCalls(Summarize(resend(rows), false))
		calls := 0
		for _, c := range once {
			calls += len(c)
		}
		if calls == 0 {
			t.Fatalf("%s: sample has no tool calls", name)
		}
		if !reflect.DeepEqual(once, twice) {
			n := 0
			for _, c := range twice {
				n += len(c)
			}
			t.Errorf("%s: %d tool calls once, %d after a re-sent export", name, calls, n)
		}
	}
}

func TestDistinctCopilotToolSpansAreAllListed(t *testing.T) {
	const s1 = int64(1_000_000_000)
	root := copilotSpan("t1", "a1", "", "invoke_agent", s1, 9*s1, map[string]any{"gen_ai.agent.id": "github.copilot.default"})
	attrs := map[string]any{"gen_ai.tool.name": "view"}
	first := copilotSpan("t1", "b1", "a1", "execute_tool view", 2*s1, 3*s1, attrs)
	sameIDOtherTrace := copilotSpan("t2", "b1", "", "execute_tool view", 2*s1, 3*s1, attrs)
	other := copilotSpan("t1", "b2", "a1", "execute_tool view", 2*s1, 3*s1, attrs)
	noIDs := copilotSpan("t1", "", "a1", "execute_tool view", 2*s1, 3*s1, attrs)
	noIDs.SpanID = nil
	rows := []record.Record{root, first, sameIDOtherTrace, other, noIDs}
	if got := len(copilotSessionOf(t, Summarize(rows, false)).ToolCalls); got != 4 {
		t.Fatalf("once: %d tool calls, want 4", got)
	}
	// The span without ids is never merged, so its re-sent copy is listed too.
	if got := len(copilotSessionOf(t, Summarize(resend(rows), false)).ToolCalls); got != 5 {
		t.Fatalf("re-sent: %d tool calls, want 3 keyed spans + 2 copies of the span without ids", got)
	}
}

func geminiToolCallLog(id, when int64, stamp string) record.Record {
	rec := geminiLog("gemini_cli.tool_call", when, map[string]any{
		"event.timestamp": stamp, "prompt_id": geminiSession + "########1",
		"function_name": "read_file", "duration_ms": 14, "success": true, "decision": "auto_accept", "tool_type": "native",
	})
	rec.ID = id
	return rec
}

func TestDistinctGeminiToolCallLogsAreAllListed(t *testing.T) {
	// The same tool with the same result, twice in one prompt: two calls.
	rows := []record.Record{
		geminiToolCallLog(1, 1_000, "2027-01-15T08:00:04.400Z"),
		geminiToolCallLog(2, 2_000, "2027-01-15T08:00:05.400Z"),
		geminiToolCallLog(3, 2_000, "2027-01-15T08:00:05.500Z"),
	}
	for name, rows := range map[string][]record.Record{"once": rows, "re-sent": resend(rows)} {
		if got := len(geminiSessionOf(t, Summarize(rows, false)).ToolCalls); got != 3 {
			t.Fatalf("%s: %d tool calls, want 3", name, got)
		}
	}
	// A copy that differs in any attribute is not an exact copy.
	changed := geminiToolCallLog(4, 1_000, "2027-01-15T08:00:04.400Z")
	changed.Attributes["duration_ms"] = 140
	if got := len(geminiSessionOf(t, Summarize([]record.Record{rows[0], changed}, false)).ToolCalls); got != 2 {
		t.Fatalf("%d tool calls, want 2", got)
	}
}
