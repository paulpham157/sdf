package summary

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/paulpham157/sdf/internal/record"
	"github.com/paulpham157/sdf/internal/samplepath"
)

// Qwen Code fixtures are derived from QwenLM/qwen-code v0.28.4 (e8424c88):
// docs/developers/development/telemetry.md and packages/core/src/telemetry,
// with logPrompts=false. They were not captured from a live session.

const qwenSession = "sample-qwen-1"

func qwenRows(t *testing.T) []record.Record {
	t.Helper()
	rows := loadSignal(t, "traces", "qwen_traces.json")
	rows = append(rows, loadSignal(t, "logs", "qwen_logs.json")...)
	return append(rows, loadSignal(t, "metrics", "qwen_metrics.json")...)
}

func qwenSessionOf(t *testing.T, report Report) SessionSummary {
	t.Helper()
	for _, s := range report.Sessions {
		if s.SessionID == qwenSession {
			return s
		}
	}
	t.Fatalf("no session %s in %#v", qwenSession, report.Sessions)
	return SessionSummary{}
}

func qwenLog(name string, when int64, attrs map[string]any) record.Record {
	all := map[string]any{"session.id": qwenSession, "event.name": name}
	for k, v := range attrs {
		all[k] = v
	}
	return record.Record{
		Signal: "logs", ServiceName: record.Str("qwen-code"), SessionID: record.Str(qwenSession),
		Name: record.Str(name), TimeUnixNano: record.Int64(when), Attributes: all,
		Resource: map[string]any{"service.name": "qwen-code"},
	}
}

func TestToolOfRecognizesQwenCode(t *testing.T) {
	if ToolOf(record.Record{ServiceName: record.Str("qwen-code")}) != "qwen-code" {
		t.Fatal("service.name")
	}
	if ToolOf(record.Record{Name: record.Str("qwen-code.token.usage")}) != "qwen-code" {
		t.Fatal("metric name")
	}
	if ToolOf(record.Record{Attributes: map[string]any{"event.name": "qwen-code.tool_call"}}) != "qwen-code" {
		t.Fatal("event.name")
	}
	// Qwen spans set gen_ai.operation.name=invoke_agent; they are not Pi.
	if ToolOf(record.Record{ServiceName: record.Str("qwen-code"), Name: record.Str("qwen-code.interaction"), Attributes: map[string]any{"gen_ai.system": "pi"}}) != "qwen-code" {
		t.Fatal("interaction span")
	}
}

func TestQwenSamplesAreMarkedAndHaveNoPromptText(t *testing.T) {
	for _, name := range []string{"qwen_logs.json", "qwen_metrics.json", "qwen_traces.json"} {
		body, err := os.ReadFile(filepath.Join(samplepath.Dir(), name))
		if err != nil {
			t.Fatal(err)
		}
		text := string(body)
		if !strings.Contains(text, `"sdf.fixture"`) || !strings.Contains(text, `"stringValue": "sample"`) {
			t.Fatal(name, "not marked sdf.fixture=sample")
		}
		for _, banned := range []string{`"prompt"`, `"request_text"`, `"response_text"`, "gen_ai.input.messages", "gen_ai.output.messages", "gen_ai.tool.call.arguments"} {
			if strings.Contains(text, banned) {
				t.Fatal(name, "has content key", banned)
			}
		}
		// Tool arguments are on purpose: Qwen Code sends them whatever
		// logPrompts says, and the store must drop them (internal/store
		// and tests/e2e check that).
		if name == "qwen_logs.json" && (!strings.Contains(text, `"function_args"`) || !strings.Contains(text, "SDF-FIXTURE-SECRET")) {
			t.Fatal(name, "lacks the realistic function_args the redaction tests need")
		}
	}
}

func TestQwenSampleSessionTokensToolsDurationAndCost(t *testing.T) {
	report := Summarize(qwenRows(t), false)
	if len(report.Unattributed) != 0 {
		t.Fatalf("unattributed %#v", report.Unattributed)
	}
	s := qwenSessionOf(t, report)
	if s.Tool != "qwen-code" || len(s.ServiceNames) != 1 || s.ServiceNames[0] != "qwen-code" {
		t.Fatalf("tool %q services %v", s.Tool, s.ServiceNames)
	}
	if len(s.Models) != 1 || s.Models[0] != "qwen3-coder-plus" {
		t.Fatalf("models %v", s.Models)
	}
	if s.Traces != 31 || s.Logs != 27 || s.Metrics != 11 {
		t.Fatalf("signals %d %d %d", s.Traces, s.Logs, s.Metrics)
	}
	// Six api_response logs (main agent, subagent, memory extractor). The
	// llm_request spans and the token metric repeat these and are not added.
	want := map[string]float64{"input_token_count": 67800, "output_token_count": 1920, "cached_content_token_count": 44596, "thoughts_token_count": 710}
	if s.Tokens.Len() != len(want) {
		t.Fatalf("tokens %v", s.Tokens)
	}
	for k, v := range want {
		if s.Tokens.Vals[k] != v {
			t.Fatalf("tokens[%s]=%v want %v (%v)", k, s.Tokens.Vals[k], v, s.Tokens)
		}
	}
	if deref(s.TokensSource) != "logs qwen-code.api_response" {
		t.Fatalf("tokens source %q", deref(s.TokensSource))
	}
	// Two interactions: 20 s + 18 s. The memory extractor trace is not one.
	if s.DurationMS == nil || *s.DurationMS != 38000 || deref(s.DurationSource) != "traces qwen-code.interaction" {
		t.Fatalf("duration %v %q", s.DurationMS, deref(s.DurationSource))
	}
	if s.CostUSD != nil || s.AgentReportedCostUSD != nil {
		t.Fatal("Qwen Code reports no cost")
	}
	if s.ComputedCost == nil || s.ComputedCost.Status != "unknown" || s.ComputedCost.USD != "" {
		t.Fatalf("computed cost should be unknown: %#v", s.ComputedCost)
	}
	var signals []string
	for _, m := range s.Missing {
		signals = append(signals, m.Signal)
	}
	if strings.Join(signals, ",") != "agent cost,tokens by agent" {
		t.Fatalf("missing %#v", s.Missing)
	}
	type call struct {
		name, success string
		ms, wait      float64
	}
	wantCalls := []call{
		{"read_file", "true", 180, 10},
		{"agent", "true", 6680, 10},
		{"glob", "true", 35, 5},
		{"write_file", "true", 390, 4900},
		{"run_shell_command", "false", 1370, 20},
	}
	if len(s.ToolCalls) != len(wantCalls) {
		t.Fatalf("tool calls %#v", s.ToolCalls)
	}
	for i, w := range wantCalls {
		got := s.ToolCalls[i]
		if deref(got.Name) != w.name || deref(got.Success) != w.success || got.DurationMS == nil || *got.DurationMS != w.ms ||
			got.WaitMS == nil || *got.WaitMS != w.wait || got.Source != "traces qwen-code.tool; logs qwen-code.tool_call" {
			t.Fatalf("call %d = %+v (name %q success %q dur %v wait %v)", i, got, deref(got.Name), deref(got.Success), got.DurationMS, got.WaitMS)
		}
	}
	shown := map[string]bool{}
	var walk func([]SpanNode)
	walk = func(nodes []SpanNode) {
		for _, n := range nodes {
			shown[n.Name] = true
			walk(n.Children)
		}
	}
	walk(s.Spans)
	for _, name := range []string{"qwen-code.interaction", "qwen-code.llm_request", "qwen-code.tool", "qwen-code.subagent"} {
		if !shown[name] {
			t.Fatalf("span %s hidden by default (%v)", name, shown)
		}
	}
}

func TestQwenTokensFallBackToSpansThenMetrics(t *testing.T) {
	span := record.Record{
		Signal: "traces", ServiceName: record.Str("qwen-code"), SessionID: record.Str(qwenSession),
		Name: record.Str("qwen-code.llm_request"), TraceID: record.Str("t"), SpanID: record.Str("a"),
		StartUnixNano: record.Int64(1_000_000_000), EndUnixNano: record.Int64(3_000_000_000), StatusCode: "UNSET",
		Attributes: map[string]any{"session.id": qwenSession, "gen_ai.request.model": "qwen3-max",
			"gen_ai.usage.input_tokens": 1200, "gen_ai.usage.output_tokens": 90, "gen_ai.usage.cache_read.input_tokens": 1024, "thoughts_token_count": 30},
	}
	s := qwenSessionOf(t, Summarize([]record.Record{span}, false))
	if deref(s.TokensSource) != "traces qwen-code.llm_request" || s.Tokens.Vals["gen_ai.usage.input_tokens"] != 1200 || s.Tokens.Vals["thoughts_token_count"] != 30 {
		t.Fatalf("%q %v", deref(s.TokensSource), s.Tokens)
	}

	start := qwenLog("qwen-code.user_prompt", 1_000_000_000, map[string]any{"prompt_length": 5})
	end := qwenLog("qwen-code.conversation_finished", 9_000_000_000, nil)
	metric := func(kind string, v float64) record.Record {
		return record.Record{
			Signal: "metrics", ServiceName: record.Str("qwen-code"), Name: record.Str("qwen-code.token.usage"),
			MetricKind: record.Str("sum"), MetricTemporality: record.Str("cumulative"), Value: record.Float(v),
			StartUnixNano: record.Int64(2_000_000_000), TimeUnixNano: record.Int64(8_000_000_000),
			Attributes: map[string]any{"model": "qwen3-max", "type": kind}, Resource: map[string]any{"service.name": "qwen-code"},
		}
	}
	s = qwenSessionOf(t, Summarize([]record.Record{start, end, metric("input", 500), metric("output", 40)}, false))
	if deref(s.TokensSource) != "metrics qwen-code.token.usage" || s.Tokens.Vals["input"] != 500 || s.Tokens.Vals["output"] != 40 {
		t.Fatalf("%q %v", deref(s.TokensSource), s.Tokens)
	}
	if s.DurationMS == nil || *s.DurationMS != 8000 || deref(s.DurationSource) != "logs qwen-code window" {
		t.Fatalf("duration %v %q", s.DurationMS, deref(s.DurationSource))
	}
}

func TestQwenToolCallsFromLogsWithoutSpans(t *testing.T) {
	rows := []record.Record{
		qwenLog("qwen-code.tool_call", 2_000_000_000, map[string]any{"function_name": "read_file", "call_id": "c1", "duration_ms": 12, "success": true, "status": "success"}),
		qwenLog("qwen-code.tool_call", 3_000_000_000, map[string]any{"function_name": "run_shell_command", "call_id": "c2", "duration_ms": 900, "success": false, "status": "error", "error_type": "tool_error"}),
	}
	s := qwenSessionOf(t, Summarize(rows, false))
	if len(s.ToolCalls) != 2 || deref(s.ToolCalls[0].Name) != "read_file" || deref(s.ToolCalls[1].Success) != "false" || s.ToolCalls[1].Source != "logs qwen-code.tool_call" {
		t.Fatalf("%#v", s.ToolCalls)
	}
	var signals []string
	for _, m := range s.Missing {
		signals = append(signals, m.Signal)
	}
	if strings.Join(signals, ",") != "traces,metrics,agent cost" {
		t.Fatalf("missing %v", signals)
	}
}

func qwenTokenPoint(session string, kind string, value float64, start, at int64, id int64, attrs map[string]any) record.Record {
	a := map[string]any{"model": "qwen3-max", "type": kind}
	for k, v := range attrs {
		a[k] = v
	}
	return record.Record{
		ID: id, Signal: "metrics", ServiceName: record.Str("qwen-code"), Name: record.Str("qwen-code.token.usage"),
		MetricKind: record.Str("sum"), MetricTemporality: record.Str("cumulative"), Value: record.Float(value),
		StartUnixNano: record.Int64(start), TimeUnixNano: record.Int64(at),
		Attributes: a, Resource: map[string]any{"service.name": "qwen-code"},
	}
}

func qwenSessionLog(session, name string, at int64) record.Record {
	return record.Record{
		Signal: "logs", ServiceName: record.Str("qwen-code"), SessionID: record.Str(session),
		Name: record.Str(name), TimeUnixNano: record.Int64(at),
		Attributes: map[string]any{"session.id": session, "event.name": name},
		Resource:   map[string]any{"service.name": "qwen-code"},
	}
}

func qwenTokensFor(t *testing.T, rows []record.Record, session string) OrderedFloats {
	t.Helper()
	for _, s := range Summarize(rows, false).Sessions {
		if s.SessionID == session {
			if deref(s.TokensSource) != "metrics qwen-code.token.usage" {
				t.Fatalf("source %q", deref(s.TokensSource))
			}
			return s.Tokens
		}
	}
	t.Fatalf("no session %s", session)
	return OrderedFloats{}
}

const sec = int64(1_000_000_000)

func TestQwenMetricTokensDoNotCarryIntoTheNextSessionOfOneProcess(t *testing.T) {
	// One process (start 1 s) runs session A, then /clear starts session B.
	// The counter keeps counting: 300 at the end of A, 700 at the end of B.
	rows := []record.Record{
		qwenSessionLog("A", "qwen-code.user_prompt", 2*sec),
		qwenSessionLog("A", "qwen-code.conversation_finished", 10*sec),
		qwenSessionLog("B", "qwen-code.user_prompt", 60*sec),
		qwenSessionLog("B", "qwen-code.conversation_finished", 90*sec),
		qwenTokenPoint("", "input", 100, 1*sec, 5*sec, 1, nil),
		qwenTokenPoint("", "input", 300, 1*sec, 10*sec, 2, nil),
		qwenTokenPoint("", "input", 450, 1*sec, 65*sec, 3, nil),
		qwenTokenPoint("", "input", 700, 1*sec, 90*sec, 4, nil),
	}
	if a := qwenTokensFor(t, rows, "A"); a.Vals["input"] != 300 {
		t.Fatalf("A input %v, want 300 (the process started with A)", a.Vals["input"])
	}
	// B starts 59 s after the process: its baseline is the run's latest
	// point before B's first one (300, A's last export), so 700 - 300.
	if b := qwenTokensFor(t, rows, "B"); b.Vals["input"] != 400 {
		t.Fatalf("B input %v, want 400, not 700 or 250", b.Vals["input"])
	}
}

func TestQwenMetricTokensWithSessionIDAndResume(t *testing.T) {
	// includeSessionId: the series is the session's own, so no baseline,
	// even in a process that started long before.
	rows := []record.Record{
		qwenSessionLog("B", "qwen-code.user_prompt", 60*sec),
		qwenSessionLog("B", "qwen-code.conversation_finished", 90*sec),
		qwenTokenPoint("B", "input", 40, 1*sec, 65*sec, 1, map[string]any{"session.id": "B"}),
		qwenTokenPoint("B", "input", 120, 1*sec, 90*sec, 2, map[string]any{"session.id": "B"}),
	}
	rows[2].SessionID, rows[3].SessionID = record.Str("B"), record.Str("B")
	if b := qwenTokensFor(t, rows, "B"); b.Vals["input"] != 120 {
		t.Fatalf("B input %v, want 120", b.Vals["input"])
	}
	// qwen --resume in a new process: two runs, each counted from 0.
	rows = []record.Record{
		qwenSessionLog("R", "qwen-code.user_prompt", 2*sec),
		qwenSessionLog("R", "qwen-code.conversation_finished", 100*sec),
		qwenTokenPoint("", "input", 1000, 1*sec, 20*sec, 1, nil),
		qwenTokenPoint("", "input", 500, 50*sec, 90*sec, 2, nil),
	}
	if r := qwenTokensFor(t, rows, "R"); r.Vals["input"] != 1500 {
		t.Fatalf("resumed input %v, want 1500", r.Vals["input"])
	}
}

func qwenSessionFor(t *testing.T, rows []record.Record, session string) (SessionSummary, int) {
	t.Helper()
	r := Summarize(rows, false)
	left := 0
	for _, u := range r.Unattributed {
		left += u.Metrics
	}
	for _, s := range r.Sessions {
		if s.SessionID == session {
			return s, left
		}
	}
	t.Fatalf("no session %s", session)
	return SessionSummary{}, 0
}

func TestQwenMetricTokensKeepTheExitFlushAfterTheLastLog(t *testing.T) {
	// The process exports 100 during the session and flushes 300 at exit,
	// 20 s after the last log. The flush is the session's.
	rows := []record.Record{
		qwenSessionLog("A", "qwen-code.user_prompt", 2*sec),
		qwenSessionLog("A", "qwen-code.conversation_finished", 10*sec),
		qwenTokenPoint("", "input", 100, 1*sec, 5*sec, 1, nil),
		qwenTokenPoint("", "input", 300, 1*sec, 30*sec, 2, nil),
	}
	if a := qwenTokensFor(t, rows, "A"); a.Vals["input"] != 300 {
		t.Fatalf("A input %v, want 300 (exit flush kept)", a.Vals["input"])
	}
	if _, left := qwenSessionFor(t, rows, "A"); left != 0 {
		t.Fatalf("%d metric points unattributed, want 0", left)
	}
}

func TestQwenMetricTokensExitFlushAfterClearGoesToTheLatestSession(t *testing.T) {
	// After /clear, the exit flush goes to the latest session of the run.
	rows := []record.Record{
		qwenSessionLog("A", "qwen-code.user_prompt", 2*sec),
		qwenSessionLog("A", "qwen-code.conversation_finished", 10*sec),
		qwenSessionLog("B", "qwen-code.user_prompt", 60*sec),
		qwenSessionLog("B", "qwen-code.conversation_finished", 90*sec),
		qwenTokenPoint("", "input", 300, 1*sec, 10*sec, 1, nil),
		qwenTokenPoint("", "input", 450, 1*sec, 65*sec, 2, nil),
		qwenTokenPoint("", "input", 800, 1*sec, 120*sec, 3, nil),
	}
	if a := qwenTokensFor(t, rows, "A"); a.Vals["input"] != 300 {
		t.Fatalf("A input %v, want 300", a.Vals["input"])
	}
	if b := qwenTokensFor(t, rows, "B"); b.Vals["input"] != 500 {
		t.Fatalf("B input %v, want 500 (800 - 300)", b.Vals["input"])
	}
}

func TestQwenMetricTokensLateFlushStaysInItsOwnRun(t *testing.T) {
	// A late point of a run that started after the only session is not
	// that session's (another process).
	rows := []record.Record{
		qwenSessionLog("A", "qwen-code.user_prompt", 2*sec),
		qwenSessionLog("A", "qwen-code.conversation_finished", 10*sec),
		qwenTokenPoint("", "input", 100, 1*sec, 5*sec, 1, nil),
		qwenTokenPoint("", "input", 900, 50*sec, 70*sec, 2, nil),
	}
	if a := qwenTokensFor(t, rows, "A"); a.Vals["input"] != 100 {
		t.Fatalf("A input %v, want 100 (the other run's point is not A's)", a.Vals["input"])
	}
	if _, left := qwenSessionFor(t, rows, "A"); left != 1 {
		t.Fatalf("%d metric points unattributed, want 1", left)
	}
}

func TestQwenMetricTokensSinglePointUsesTheRunsEarlierPoint(t *testing.T) {
	// B has one export point; the run's earlier point (A's) is its baseline.
	rows := []record.Record{
		qwenSessionLog("A", "qwen-code.user_prompt", 2*sec),
		qwenSessionLog("A", "qwen-code.conversation_finished", 10*sec),
		qwenSessionLog("B", "qwen-code.user_prompt", 60*sec),
		qwenSessionLog("B", "qwen-code.conversation_finished", 90*sec),
		qwenTokenPoint("", "input", 300, 1*sec, 10*sec, 1, nil),
		qwenTokenPoint("", "input", 700, 1*sec, 85*sec, 2, nil),
	}
	if b := qwenTokensFor(t, rows, "B"); b.Vals["input"] != 400 {
		t.Fatalf("B input %v, want 400, not a certain 0", b.Vals["input"])
	}
}

func TestQwenMetricTokensUnknownWithoutABaseline(t *testing.T) {
	// An earlier session in the run and no earlier point: B's share cannot
	// be split from A's, so tokens are unknown, not 0 and not 700.
	rows := []record.Record{
		qwenSessionLog("A", "qwen-code.user_prompt", 2*sec),
		qwenSessionLog("A", "qwen-code.conversation_finished", 10*sec),
		qwenSessionLog("B", "qwen-code.user_prompt", 60*sec),
		qwenSessionLog("B", "qwen-code.conversation_finished", 90*sec),
		qwenTokenPoint("", "input", 700, 1*sec, 85*sec, 1, nil),
	}
	b, _ := qwenSessionFor(t, rows, "B")
	if b.TokensSource != nil || len(b.Tokens.Vals) != 0 {
		t.Fatalf("B tokens %v from %q, want unknown", b.Tokens.Vals, deref(b.TokensSource))
	}
}

func TestQwenMetricTokensSlowStartupOfTheOnlySession(t *testing.T) {
	// The process started 15 s before the only session's first log: nothing
	// to subtract.
	rows := []record.Record{
		qwenSessionLog("A", "qwen-code.user_prompt", 16*sec),
		qwenSessionLog("A", "qwen-code.conversation_finished", 60*sec),
		qwenTokenPoint("", "input", 100, 1*sec, 20*sec, 1, nil),
		qwenTokenPoint("", "input", 300, 1*sec, 60*sec, 2, nil),
	}
	if a := qwenTokensFor(t, rows, "A"); a.Vals["input"] != 300 {
		t.Fatalf("A input %v, want 300 (slow startup, one session)", a.Vals["input"])
	}
}

func TestQwenMetricTokensResumedSessionKeepsTheNewProcessExitFlush(t *testing.T) {
	// qwen --resume R in a second process (start 50 s). That process's exit
	// flush (200 at 80 s) comes after R's last log; it belongs to R even
	// though R first started in the earlier process. 300 + 200.
	rows := []record.Record{
		qwenSessionLog("R", "qwen-code.user_prompt", 2*sec),
		qwenSessionLog("R", "qwen-code.conversation_finished", 10*sec),
		qwenSessionLog("R", "qwen-code.user_prompt", 52*sec),
		qwenSessionLog("R", "qwen-code.conversation_finished", 60*sec),
		qwenTokenPoint("", "input", 100, 1*sec, 5*sec, 1, nil),
		qwenTokenPoint("", "input", 300, 1*sec, 10*sec, 2, nil),
		qwenTokenPoint("", "input", 50, 50*sec, 55*sec, 3, nil),
		qwenTokenPoint("", "input", 200, 50*sec, 80*sec, 4, nil),
	}
	if r := qwenTokensFor(t, rows, "R"); r.Vals["input"] != 500 {
		t.Fatalf("resumed input %v, want 500 (the second process's exit flush kept)", r.Vals["input"])
	}
	if _, left := qwenSessionFor(t, rows, "R"); left != 0 {
		t.Fatalf("%d metric points unattributed, want 0", left)
	}
}
