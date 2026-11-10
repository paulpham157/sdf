package summary

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/paulpham157/sdf/internal/record"
)

func TestErrorStatusMarksTheTreeAndFailsAToolCallWithoutAFlag(t *testing.T) {
	tool := record.Record{
		Signal: "traces", ServiceName: record.Str("opencode"), SessionID: record.Str("s"),
		Name: record.Str("Tool.execute"), SpanID: record.Str("tool"),
		StartUnixNano: record.Int64(0), EndUnixNano: record.Int64(1_000_000),
		StatusCode: "ERROR", StatusMessage: record.Str("write failed"),
		Attributes: map[string]any{"gen_ai.tool.name": "write", "gen_ai.tool.call.id": "call_1"},
		Resource:   map[string]any{},
	}
	parent := record.Record{
		Signal: "traces", ServiceName: record.Str("opencode"), SessionID: record.Str("s"),
		Name: record.Str("SessionStep.attempt"), SpanID: record.Str("step"),
		StartUnixNano: record.Int64(0), EndUnixNano: record.Int64(2_000_000),
		StatusCode: "ERROR", StatusMessage: record.Str("turn failed"),
		Attributes: map[string]any{}, Resource: map[string]any{},
	}
	tool.ParentSpanID = record.Str("step")
	report := Summarize([]record.Record{parent, tool}, false)
	session := report.Sessions[0]
	if session.Spans[0].Name != "SessionStep.attempt" || session.Spans[0].StatusCode != "ERROR" {
		t.Fatalf("%+v", session.Spans[0])
	}
	if session.Spans[0].Success != nil {
		t.Fatal("non-tool span should not gain a success flag")
	}
	call := session.ToolCalls[0]
	if deref(call.Success) != "false" || deref(call.Name) != "write" {
		t.Fatalf("%+v", call)
	}
	child := session.Spans[0].Children[0]
	if child.StatusCode != "ERROR" || deref(child.StatusMessage) != "write failed" || deref(child.Success) != "false" || deref(child.SuccessSource) != "traces status" {
		t.Fatalf("%+v", child)
	}
	text := Format(report)
	if !strings.Contains(text, "SessionStep.attempt 2ms status=ERROR (turn failed)") {
		t.Fatalf("%s", text)
	}
	if !strings.Contains(text, "status=ERROR (write failed)") || !strings.Contains(text, "success=false (traces status)") {
		t.Fatalf("%s", text)
	}
	var buf bytes.Buffer
	if err := WriteJSON(&buf, report); err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(buf.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	span := payload["sessions"].([]any)[0].(map[string]any)["spans"].([]any)[0].(map[string]any)
	if span["status_code"] != "ERROR" || span["status_message"] != "turn failed" {
		t.Fatalf("%v", span)
	}
	childJSON := span["children"].([]any)[0].(map[string]any)
	if childJSON["status_code"] != "ERROR" || childJSON["success"] != "false" {
		t.Fatalf("%v", childJSON)
	}
}

func TestExplicitSuccessWinsOverErrorStatus(t *testing.T) {
	okFlag := record.Record{
		Signal: "traces", ServiceName: record.Str("pi"), SessionID: record.Str("s"),
		Name: record.Str("execute_tool bash"), StartUnixNano: record.Int64(0), EndUnixNano: record.Int64(1),
		StatusCode: "ERROR",
		Attributes: map[string]any{"gen_ai.tool.name": "bash", "pi.tool.is_error": false},
		Resource:   map[string]any{},
	}
	call := Summarize([]record.Record{okFlag}, false).Sessions[0].ToolCalls[0]
	if deref(call.Success) != "true" {
		t.Fatalf("explicit flag lost: %+v", call)
	}
	logged := record.Record{
		Signal: "traces", ServiceName: record.Str("codex_exec"), SessionID: record.Str("s"),
		Name: record.Str("exec_command"), SpanID: record.Str("cmd"),
		StartUnixNano: record.Int64(0), EndUnixNano: record.Int64(1_000_000),
		StatusCode: "ERROR",
		Attributes: map[string]any{"call_id": "call_a", "tool_name": "shell"},
		Resource:   map[string]any{},
	}
	log := record.Record{
		Signal: "logs", ServiceName: record.Str("codex_exec"), SessionID: record.Str("s"),
		Name:       record.Str("codex.tool_result"),
		Attributes: map[string]any{"call_id": "call_a", "success": "true", "tool_name": "shell"},
		Resource:   map[string]any{},
	}
	session := Summarize([]record.Record{logged, log}, false).Sessions[0]
	if deref(session.ToolCalls[0].Success) != "true" || deref(session.Spans[0].Success) != "true" {
		t.Fatalf("log flag lost calls %+v span %+v", session.ToolCalls[0], session.Spans[0])
	}
}

func TestUnsetStatusIsTheDefault(t *testing.T) {
	span := record.Record{
		Signal: "traces", ServiceName: record.Str("claude-code"), SessionID: record.Str("s"),
		Name: record.Str("claude_code.interaction"), StartUnixNano: record.Int64(0), EndUnixNano: record.Int64(1_000_000),
		Attributes: map[string]any{}, Resource: map[string]any{},
	}
	node := Summarize([]record.Record{span}, false).Sessions[0].Spans[0]
	if node.StatusCode != "UNSET" || node.StatusMessage != nil || node.Success != nil {
		t.Fatalf("%+v", node)
	}
	text := Format(Summarize([]record.Record{span}, false))
	if strings.Contains(text, "status=") {
		t.Fatalf("unset should stay unmarked\n%s", text)
	}
}
