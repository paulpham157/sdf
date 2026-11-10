package summary

import (
	"strings"
	"testing"

	"github.com/paulpham157/sdf/internal/record"
)

var (
	sample = map[string]any{"sdf.fixture": "sample"}
	claude = "sample-claude-live"
	codex  = "sample-codex-live"
	model  = "recursive/flint-v1.0:free"
)

func rec(fields record.Record) record.Record {
	if fields.Resource == nil {
		fields.Resource = map[string]any{"sdf.fixture": "sample", "service.name": deref(fields.ServiceName)}
	}
	if fields.Attributes == nil {
		fields.Attributes = map[string]any{}
	}
	return fields
}

func attrs(extra map[string]any) map[string]any {
	out := map[string]any{"sdf.fixture": "sample"}
	for k, v := range extra {
		out[k] = v
	}
	return out
}

func claudeFixture() []record.Record {
	common := func(r record.Record) record.Record {
		r.ServiceName = record.Str("claude-code")
		r.SessionID = record.Str(claude)
		r.TraceID = record.Str(strings.Repeat("aa", 16))
		return rec(r)
	}
	spans := []record.Record{
		common(record.Record{ID: 1, Signal: "traces", Name: record.Str("claude_code.interaction"), SpanID: record.Str("aa"), StartUnixNano: record.Int64(0), EndUnixNano: record.Int64(3_571_881_593), Attributes: attrs(map[string]any{"session.id": claude, "model": model})}),
		common(record.Record{ID: 2, Signal: "traces", Name: record.Str("claude_code.llm_request"), SpanID: record.Str("l1"), ParentSpanID: record.Str("aa"), StartUnixNano: record.Int64(10), EndUnixNano: record.Int64(110_000_000), Attributes: attrs(map[string]any{"session.id": claude, "model": model})}),
		common(record.Record{ID: 3, Signal: "traces", Name: record.Str("claude_code.llm_request"), SpanID: record.Str("l2"), ParentSpanID: record.Str("aa"), StartUnixNano: record.Int64(200_000_000), EndUnixNano: record.Int64(400_000_000), Attributes: attrs(map[string]any{"session.id": claude, "model": model})}),
		common(record.Record{ID: 4, Signal: "traces", Name: record.Str("claude_code.tool"), SpanID: record.Str("tool"), ParentSpanID: record.Str("aa"), StartUnixNano: record.Int64(500_000_000), EndUnixNano: record.Int64(520_000_000), Attributes: attrs(map[string]any{"session.id": claude, "tool_name": "Write", "tool_use_id": "call_write"})}),
		common(record.Record{ID: 5, Signal: "traces", Name: record.Str("claude_code.tool.blocked_on_user"), SpanID: record.Str("wait"), ParentSpanID: record.Str("tool"), StartUnixNano: record.Int64(500_000_000), EndUnixNano: record.Int64(511_000_000), Attributes: attrs(map[string]any{"session.id": claude, "tool_use_id": "call_write"})}),
		common(record.Record{ID: 6, Signal: "traces", Name: record.Str("claude_code.tool.execution"), SpanID: record.Str("exec"), ParentSpanID: record.Str("tool"), StartUnixNano: record.Int64(511_000_000), EndUnixNano: record.Int64(520_000_000), Attributes: attrs(map[string]any{"session.id": claude, "tool_use_id": "call_write"})}),
	}
	logs := []record.Record{
		common(record.Record{ID: 7, Signal: "logs", Name: record.Str("claude_code.user_prompt"), TimeUnixNano: record.Int64(1), Attributes: attrs(map[string]any{"event.name": "user_prompt", "session.id": claude})}),
		common(record.Record{ID: 8, Signal: "logs", Name: record.Str("claude_code.api_request"), TimeUnixNano: record.Int64(2), Attributes: attrs(map[string]any{"event.name": "api_request", "session.id": claude, "model": model, "cost_usd": 0.05807, "input_tokens": 1})}),
		common(record.Record{ID: 9, Signal: "logs", Name: record.Str("claude_code.tool_result"), TimeUnixNano: record.Int64(3), Attributes: attrs(map[string]any{"event.name": "tool_result", "session.id": claude, "tool_name": "Write", "tool_use_id": "call_write", "success": "true", "duration_ms": 9})}),
	}
	var metrics []record.Record
	order := int64(10)
	for _, item := range []struct {
		kind  string
		value float64
	}{{"input", 10739}, {"output", 251}, {"cacheRead", 10752}, {"cacheCreation", 0}} {
		metrics = append(metrics, record.Record{
			ID: order, Signal: "metrics", Name: record.Str("claude_code.token.usage"), TimeUnixNano: record.Int64(4_000_000_000),
			Value: record.Float(item.value), MetricUnit: record.Str("tokens"), MetricTemporality: record.Str("delta"), MetricKind: record.Str("sum"),
			Attributes:  attrs(map[string]any{"type": item.kind, "model": model, "session.id": claude}),
			ServiceName: record.Str("claude-code"), SessionID: record.Str(claude),
			Resource: map[string]any{"sdf.fixture": "sample", "service.name": "claude-code"},
		})
		order++
	}
	metrics = append(metrics, record.Record{
		ID: order, Signal: "metrics", Name: record.Str("claude_code.cost.usage"), TimeUnixNano: record.Int64(4_000_000_000),
		Value: record.Float(0.065346), MetricUnit: record.Str("USD"), MetricTemporality: record.Str("delta"), MetricKind: record.Str("sum"),
		Attributes:  attrs(map[string]any{"model": model, "session.id": claude}),
		ServiceName: record.Str("claude-code"), SessionID: record.Str(claude),
		Resource: map[string]any{"sdf.fixture": "sample", "service.name": "claude-code"},
	})
	return append(append(spans, logs...), metrics...)
}

func codexFixture() []record.Record {
	common := func(r record.Record) record.Record {
		r.ServiceName = record.Str("codex_exec")
		r.SessionID = record.Str(codex)
		r.TraceID = record.Str(strings.Repeat("bb", 16))
		return rec(r)
	}
	spans := []record.Record{
		common(record.Record{ID: 1, Signal: "traces", Name: record.Str("run_turn"), SpanID: record.Str("turn"), StartUnixNano: record.Int64(0), EndUnixNano: record.Int64(6_682_000_000), Attributes: attrs(map[string]any{"conversation.id": codex, "model": model})}),
		common(record.Record{ID: 2, Signal: "traces", Name: record.Str("codex.sampling"), SpanID: record.Str("sample"), ParentSpanID: record.Str("turn"), StartUnixNano: record.Int64(1_000_000), EndUnixNano: record.Int64(2_000_000_000), Attributes: attrs(map[string]any{"conversation.id": codex, "model": model})}),
		common(record.Record{ID: 3, Signal: "traces", Name: record.Str("exec_command"), SpanID: record.Str("cmd-a"), ParentSpanID: record.Str("turn"), StartUnixNano: record.Int64(3_000_000_000), EndUnixNano: record.Int64(3_063_000_000), Attributes: attrs(map[string]any{"conversation.id": codex, "tool_name": "exec_command", "call_id": "call_a"})}),
		common(record.Record{ID: 4, Signal: "traces", Name: record.Str("codex.tool_blocking"), SpanID: record.Str("block-a"), ParentSpanID: record.Str("cmd-a"), StartUnixNano: record.Int64(3_000_000_000), EndUnixNano: record.Int64(3_012_000_000), Attributes: attrs(map[string]any{"conversation.id": codex, "call_id": "call_a"})}),
		common(record.Record{ID: 5, Signal: "traces", Name: record.Str("unified_exec.exec_command"), SpanID: record.Str("run-a"), ParentSpanID: record.Str("cmd-a"), StartUnixNano: record.Int64(3_012_000_000), EndUnixNano: record.Int64(3_063_000_000), Attributes: attrs(map[string]any{"conversation.id": codex, "call_id": "call_a"})}),
		common(record.Record{ID: 6, Signal: "traces", Name: record.Str("exec_command"), SpanID: record.Str("cmd-b"), ParentSpanID: record.Str("turn"), StartUnixNano: record.Int64(4_000_000_000), EndUnixNano: record.Int64(4_060_000_000), Attributes: attrs(map[string]any{"conversation.id": codex, "tool_name": "exec_command", "call_id": "call_b"})}),
		common(record.Record{ID: 7, Signal: "traces", Name: record.Str("append_items"), SpanID: record.Str("noise"), ParentSpanID: record.Str("turn"), StartUnixNano: record.Int64(10), EndUnixNano: record.Int64(20), Attributes: attrs(map[string]any{"conversation.id": codex})}),
	}
	logs := []record.Record{
		common(record.Record{ID: 8, Signal: "logs", Name: record.Str("codex.conversation_starts"), TimeUnixNano: record.Int64(1), Attributes: attrs(map[string]any{"conversation.id": codex, "model": model})}),
	}
	pairs := [][2]int{{8896, 273}, {9019, 180}, {9108, 76}}
	for i, pair := range pairs {
		logs = append(logs, common(record.Record{
			ID: int64(9 + i), Signal: "logs", Name: record.Str("codex.sse_event"), TimeUnixNano: record.Int64(int64(9+i) * 1_000_000),
			Attributes: attrs(map[string]any{
				"event.kind": "response.completed", "conversation.id": codex, "model": model,
				"input_token_count": pair[0], "output_token_count": pair[1], "cached_token_count": pair[0] / 2,
			}),
		}))
	}
	logs = append(logs,
		common(record.Record{ID: 12, Signal: "logs", Name: record.Str("codex.tool_result"), TimeUnixNano: record.Int64(3_100_000_000), Attributes: attrs(map[string]any{"conversation.id": codex, "tool_name": "exec_command", "call_id": "call_a", "success": "true", "duration_ms": 63})}),
		common(record.Record{ID: 13, Signal: "logs", Name: record.Str("codex.tool_result"), TimeUnixNano: record.Int64(4_100_000_000), Attributes: attrs(map[string]any{"conversation.id": codex, "tool_name": "exec_command", "call_id": "call_b", "success": "true", "duration_ms": 60})}),
	)
	unscoped := []record.Record{
		rec(record.Record{ID: 20, Signal: "logs", ServiceName: record.Str("codex_exec"), Name: record.Str("metrics histogram [codex.turn.token_usage] failed: tag value contains invalid characters: " + model), TimeUnixNano: record.Int64(1_000_000_000), Attributes: attrs(nil)}),
		rec(record.Record{ID: 21, Signal: "logs", ServiceName: record.Str("codex_exec"), Name: record.Str("metrics histogram [codex.turn.e2e_duration_ms] failed: tag value contains invalid characters: " + model), TimeUnixNano: record.Int64(1_000_000_000), Attributes: attrs(nil)}),
		rec(record.Record{ID: 22, Signal: "logs", ServiceName: record.Str("codex_exec"), Name: record.Str("metrics counter [codex.turn.cost_microusd] failed: tag value contains invalid characters: " + model), TimeUnixNano: record.Int64(1_000_000_000), Attributes: attrs(nil)}),
		rec(record.Record{ID: 23, Signal: "traces", ServiceName: record.Str("codex_exec"), Name: record.Str("get_model_info"), TraceID: record.Str(strings.Repeat("cc", 16)), SpanID: record.Str("info"), StartUnixNano: record.Int64(500_000_000), EndUnixNano: record.Int64(600_000_000), Attributes: attrs(map[string]any{"model": "gpt-5.6-luna"})}),
		rec(record.Record{ID: 24, Signal: "metrics", ServiceName: record.Str("codex_exec"), Name: record.Str("codex.process.start"), TimeUnixNano: record.Int64(2_000_000_000), Value: record.Float(1), MetricTemporality: record.Str("delta"), MetricKind: record.Str("sum"), Attributes: attrs(nil)}),
		rec(record.Record{ID: 25, Signal: "metrics", ServiceName: record.Str("codex_exec"), Name: record.Str("codex.sqlite.init"), TimeUnixNano: record.Int64(6_682_000_000 + 3_600_000_000_000), Value: record.Float(1), MetricTemporality: record.Str("delta"), MetricKind: record.Str("sum"), Attributes: attrs(nil)}),
	}
	return append(append(spans, logs...), unscoped...)
}

func TestFixturesAreMarkedSample(t *testing.T) {
	for _, item := range append(claudeFixture(), codexFixture()...) {
		if item.Resource["sdf.fixture"] != "sample" {
			t.Fatal(item.Resource)
		}
		if _, ok := item.Attributes["prompt"]; ok {
			t.Fatal("prompt attribute")
		}
	}
}

func TestClaudeSummaryShowsSpansLogsAndMetrics(t *testing.T) {
	session := Summarize(claudeFixture(), false).Sessions[0]
	if session.SessionID != claude || session.Traces != 6 || session.Logs != 3 || session.Metrics != 5 || len(session.Missing) != 0 {
		t.Fatalf("%+v missing %+v", session, session.Missing)
	}
	if deref(session.DurationSource) != "traces claude_code.interaction" || session.DurationMS == nil || *session.DurationMS != 3571.881593 {
		t.Fatalf("duration %v %s", session.DurationMS, deref(session.DurationSource))
	}
	for key, want := range map[string]float64{"input": 10739, "output": 251, "cacheRead": 10752, "cacheCreation": 0} {
		got, ok := session.Tokens.Get(key)
		if !ok || got != want {
			t.Fatalf("%s %v", key, session.Tokens.Vals)
		}
	}
	if !strings.HasPrefix(deref(session.TokensSource), "metrics ") || session.CostUSD == nil || *session.CostUSD != 0.065346 || deref(session.CostNote) != "agent estimate, not the provider price" {
		t.Fatalf("tokens/cost %s %v %s", deref(session.TokensSource), session.CostUSD, deref(session.CostNote))
	}
	call := session.ToolCalls[0]
	if deref(call.Name) != "Write" || deref(call.CallID) != "call_write" || deref(call.Success) != "true" || call.ExecutionMS == nil || *call.ExecutionMS != 9 || call.WaitMS == nil || *call.WaitMS != 11 || !strings.Contains(call.Source, "logs claude_code.tool_result") {
		t.Fatalf("%+v", call)
	}
	if session.Spans[0].Name != "claude_code.interaction" {
		t.Fatal(session.Spans[0].Name)
	}
	var childNames []string
	for _, child := range session.Spans[0].Children {
		childNames = append(childNames, child.Name)
	}
	if strings.Join(childNames, ",") != "claude_code.llm_request,claude_code.llm_request,claude_code.tool" {
		t.Fatal(childNames)
	}
	tool := session.Spans[0].Children[len(session.Spans[0].Children)-1]
	if deref(tool.Success) != "true" || len(tool.Children) != 2 || tool.Children[0].Success != nil || tool.Children[1].Success != nil {
		t.Fatalf("%+v", tool)
	}
	text := Format(Summarize(claudeFixture(), false))
	for _, want := range []string{"signals: traces=6 logs=3 metrics=5", "claude_code.tool.execution", "missing: (none)"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q\n%s", want, text)
		}
	}
}

func TestCodexOpenRouterShape(t *testing.T) {
	report := Summarize(codexFixture(), false)
	if len(report.Sessions) != 1 || report.Sessions[0].SessionID != codex {
		t.Fatalf("%+v", report.Sessions)
	}
	session := report.Sessions[0]
	if strings.Join(session.ServiceNames, ",") != "codex_exec" || strings.Join(session.Models, ",") != model {
		t.Fatalf("service %#v models %#v", session.ServiceNames, session.Models)
	}
	for _, item := range session.Models {
		if item == "gpt-5.6-luna" {
			t.Fatal("absorbed the other model")
		}
	}
	if deref(session.DurationSource) != "traces run_turn" || session.DurationMS == nil || *session.DurationMS != 6682 {
		t.Fatalf("duration %v %s", session.DurationMS, deref(session.DurationSource))
	}
	in, _ := session.Tokens.Get("input_token_count")
	out, _ := session.Tokens.Get("output_token_count")
	if in != 9108 || out != 273+180+76 || session.CostUSD != nil || session.Metrics != 1 {
		t.Fatalf("tokens %v cost %v metrics %d", session.Tokens.Vals, session.CostUSD, session.Metrics)
	}
	foundStart := false
	for _, line := range session.MetricLines {
		if line.Name == "codex.process.start" {
			foundStart = true
		}
	}
	if !foundStart {
		t.Fatal(session.MetricLines)
	}
	foundReason := false
	for _, item := range session.Missing {
		if item.Signal == "session metrics" && strings.Contains(item.Reason, ":") {
			foundReason = true
		}
	}
	if !foundReason {
		t.Fatal(session.Missing)
	}
	type callKey struct {
		id       string
		success  string
		duration float64
		wait     float64
		waitSet  bool
		exec     float64
		execSet  bool
	}
	got := map[callKey]bool{}
	for _, call := range session.ToolCalls {
		key := callKey{id: deref(call.CallID), success: deref(call.Success)}
		if call.DurationMS != nil {
			key.duration = *call.DurationMS
		}
		if call.WaitMS != nil {
			key.wait = *call.WaitMS
			key.waitSet = true
		}
		if call.ExecutionMS != nil {
			key.exec = *call.ExecutionMS
			key.execSet = true
		}
		got[key] = true
	}
	wantA := callKey{id: "call_a", success: "true", duration: 51, wait: 12, waitSet: true, exec: 51, execSet: true}
	wantB := callKey{id: "call_b", success: "true", duration: 60}
	if !got[wantA] || !got[wantB] || len(got) != 2 {
		t.Fatalf("%+v", session.ToolCalls)
	}
	if !strings.Contains(session.ToolCalls[0].Source, "logs codex.tool_result") {
		t.Fatal(session.ToolCalls[0].Source)
	}
	shown := names(session.Spans)
	if !shown["exec_command"] || !shown["run_turn"] || shown["append_items"] || shown["get_model_info"] || session.SpansHidden != 2 {
		t.Fatalf("shown %#v hidden %d", shown, session.SpansHidden)
	}
	foundErr := false
	for _, event := range session.LogEvents {
		if event.Name == "codex.metric_export_error" && event.Count == 3 {
			foundErr = true
		}
	}
	if !foundErr {
		t.Fatal(session.LogEvents)
	}
	if len(report.Unattributed) != 1 || report.Unattributed[0].Tool != "codex" || !strings.Contains(report.Unattributed[0].Reason, "not a session") {
		t.Fatalf("%+v", report.Unattributed)
	}
	foundInit := false
	for _, name := range report.Unattributed[0].Names {
		if strings.HasPrefix(name, "codex.sqlite.init=") {
			foundInit = true
		}
	}
	if !foundInit {
		t.Fatal(report.Unattributed[0].Names)
	}
	shownAll := names(Summarize(codexFixture(), true).Sessions[0].Spans)
	if !shownAll["append_items"] {
		t.Fatal(shownAll)
	}
}

func TestOverlappingSessionsDoNotAbsorbAmbiguousSpan(t *testing.T) {
	session := func(name string, start int64) record.Record {
		return rec(record.Record{
			Signal: "traces", ServiceName: record.Str("codex_exec"), SessionID: record.Str(name), Name: record.Str("run_turn"),
			SpanID: record.Str(name), TraceID: record.Str(name), StartUnixNano: record.Int64(start), EndUnixNano: record.Int64(start + 5_000_000_000),
			Attributes: attrs(map[string]any{"conversation.id": name}),
		})
	}
	ambiguous := rec(record.Record{Signal: "traces", ServiceName: record.Str("codex_exec"), Name: record.Str("append_items"), StartUnixNano: record.Int64(4_000_000_000), EndUnixNano: record.Int64(4_100_000_000), Attributes: attrs(nil)})
	traced := rec(record.Record{Signal: "traces", ServiceName: record.Str("codex_exec"), Name: record.Str("exec_command"), TraceID: record.Str("left"), SpanID: record.Str("cmd"), StartUnixNano: record.Int64(9_000_000_000), EndUnixNano: record.Int64(9_010_000_000), Attributes: attrs(map[string]any{"call_id": "call_z", "tool_name": "exec_command"})})
	report := Summarize([]record.Record{session("left", 0), session("right", 3_000_000_000), ambiguous, traced}, false)
	ids := map[string]bool{}
	for _, item := range report.Sessions {
		ids[item.SessionID] = true
	}
	if len(ids) != 2 || !ids["left"] || !ids["right"] {
		t.Fatal(ids)
	}
	var left SessionSummary
	for _, item := range report.Sessions {
		if item.SessionID == "left" {
			left = item
		}
	}
	if !names(left.Spans)["exec_command"] {
		t.Fatal(names(left.Spans))
	}
	found := false
	for _, name := range report.Unattributed[0].Names {
		if strings.HasPrefix(name, "append_items") {
			found = true
		}
	}
	if !found {
		t.Fatal(report.Unattributed)
	}
}

func names(nodes []SpanNode) map[string]bool {
	found := map[string]bool{}
	var walk func([]SpanNode)
	walk = func(nodes []SpanNode) {
		for _, node := range nodes {
			found[node.Name] = true
			walk(node.Children)
		}
	}
	walk(nodes)
	return found
}
