package summary

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/paulpham157/sdf/internal/otlp"
	"github.com/paulpham157/sdf/internal/record"
	"github.com/paulpham157/sdf/internal/samplepath"
)

// tests/samples/grok_traces.json is Grok CLI 1.0.92's internal spans from a
// live headless session on 2026-11-23 (GROK_INTERNAL_OTLP_TRACES_ENDPOINT
// pointed at sdf, Authorization overridden with GROK_INTERNAL_OTLP_HEADERS).
// It keeps the agent.prompt trace, the session teardown trace, and one
// parentless sampling.stream_decode span. user.id and team.id were removed
// from the resource; the session id, request_id, and invocation_id were
// replaced. It is marked sdf.fixture=live-scrubbed.

const grokLiveSession = "live-grok-session-1"

var grokLiveCalls = []struct{ id, name, success string }{
	{"call-4357c83b-0640-4746-ae64-efd520163a3d-0", "search_replace", "true"},
	{"call-744b90c3-00d8-41fc-bc77-2bca1bff98a3-1", "run_terminal_command", "false"},
	{"call-73e2d194-6a18-43e9-89cf-3b9c47f9832c-2", "run_terminal_command", "true"},
}

func grokLiveSpans(t *testing.T) []record.Record {
	t.Helper()
	return loadSignal(t, "traces", "grok_traces.json")
}

func grokLiveSessionOf(t *testing.T, report Report) SessionSummary {
	t.Helper()
	if len(report.Sessions) != 1 || report.Sessions[0].SessionID != grokLiveSession {
		t.Fatalf("sessions %+v", report.Sessions)
	}
	return report.Sessions[0]
}

func TestGrokLiveFixtureIsScrubbed(t *testing.T) {
	body, err := os.ReadFile(filepath.Join(samplepath.Dir(), "grok_traces.json"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	if !strings.Contains(text, `"live-scrubbed"`) {
		t.Fatal("not marked sdf.fixture=live-scrubbed")
	}
	for _, banned := range []string{`"user.id"`, `"team.id"`, `"organization.id"`, `"api_key.id"`, `"deployment.id"`, `"user.email"`, "Bearer", "@", "01a20753-5c28"} {
		if strings.Contains(text, banned) {
			t.Fatalf("fixture contains %q", banned)
		}
	}
}

func TestGrokSpanSessionIDAttributesTheTrace(t *testing.T) {
	spans := grokLiveSpans(t)
	withID := 0
	for _, rec := range spans {
		if rec.SessionID != nil {
			if *rec.SessionID != grokLiveSession {
				t.Fatalf("session %s", *rec.SessionID)
			}
			withID++
		}
	}
	if withID == 0 || withID == len(spans) {
		t.Fatalf("%d of %d spans carry session_id; want some, not all", withID, len(spans))
	}
	report := Summarize(spans, false)
	if len(report.Unattributed) != 0 {
		t.Fatalf("unattributed %+v", report.Unattributed)
	}
	got := grokLiveSessionOf(t, report)
	if got.Tool != "grok-cli" {
		t.Fatal(got.Tool)
	}
	for _, item := range got.Missing {
		if item.Signal == "traces" {
			t.Fatalf("traces reported missing: %+v", item)
		}
	}
	if got.DurationMS == nil || deref(got.DurationSource) != "traces agent.prompt" || *got.DurationMS < 5800 || *got.DurationMS > 5830 {
		t.Fatalf("duration %v %s", got.DurationMS, deref(got.DurationSource))
	}
}

func TestGrokSpanTreeShowsPromptTurnsRequestsAndTools(t *testing.T) {
	got := grokLiveSessionOf(t, Summarize(grokLiveSpans(t), false))
	var prompt *SpanNode
	for i := range got.Spans {
		if got.Spans[i].Name == "agent.prompt" {
			prompt = &got.Spans[i]
		}
		if got.Spans[i].Name != "agent.prompt" {
			t.Fatalf("unexpected root %s", got.Spans[i].Name)
		}
	}
	if prompt == nil || len(prompt.Children) != 1 || prompt.Children[0].Name != "session.process_conversation_turn" {
		t.Fatalf("tree %+v", got.Spans)
	}
	names := map[string]int{}
	var tools []SpanNode
	var walk func(SpanNode)
	walk = func(n SpanNode) {
		names[n.Name]++
		if n.Name == "tool.execution" {
			tools = append(tools, n)
		}
		for _, c := range n.Children {
			walk(c)
		}
	}
	walk(*prompt)
	for name, want := range map[string]int{"turn.sampling": 4, "http.create_response_stream": 4, "tools.execute": 3, "permission.wait": 3, "tool.execution": 3} {
		if names[name] != want {
			t.Fatalf("%s=%d want %d (%v)", name, names[name], want, names)
		}
	}
	for _, hidden := range []string{"tool.decision", "edit.lines", "auth", "turn.build_request", "startup"} {
		if names[hidden] != 0 {
			t.Fatalf("%s is in the default tree", hidden)
		}
	}
	for i, n := range tools {
		if deref(n.CallID) != grokLiveCalls[i].id || deref(n.ToolName) != grokLiveCalls[i].name || deref(n.Success) != grokLiveCalls[i].success {
			t.Fatalf("tool %d %+v", i, n)
		}
	}
	all := grokLiveSessionOf(t, Summarize(grokLiveSpans(t), true))
	if got.SpansHidden == 0 || all.SpansHidden != 0 {
		t.Fatalf("hidden default=%d all=%d", got.SpansHidden, all.SpansHidden)
	}
}

func TestGrokToolCallsJoinSpansAndToolResultLogs(t *testing.T) {
	rows := grokLiveSpans(t)
	for i, call := range grokLiveCalls {
		rows = append(rows, grokLog("grok_code.tool_result", grokLiveSession, 1_763_883_240_000_000_000+int64(i), map[string]any{
			"tool_name": call.name, "success": call.success, "duration_ms": 3, "tool_use_id": call.id,
		}))
	}
	got := grokLiveSessionOf(t, Summarize(rows, false))
	if len(got.ToolCalls) != len(grokLiveCalls) {
		t.Fatalf("tool calls %+v", got.ToolCalls)
	}
	for i, call := range got.ToolCalls {
		want := grokLiveCalls[i]
		if deref(call.CallID) != want.id || deref(call.Name) != want.name || deref(call.Success) != want.success {
			t.Fatalf("call %d %+v", i, call)
		}
		if call.Source != "traces tool.execution; logs grok_code.tool_result" || call.DurationMS == nil {
			t.Fatalf("call %d source %q duration %v", i, call.Source, call.DurationMS)
		}
	}
}

func TestGrokSessionIDAttributeIsReadOnlyForGrokSpans(t *testing.T) {
	body := []byte(`{"resourceSpans":[
	 {"resource":{"attributes":[{"key":"service.name","value":{"stringValue":"grok-cli"}},{"key":"sdf.fixture","value":{"stringValue":"sample"}}]},
	  "scopeSpans":[{"spans":[{"name":"agent.prompt","traceId":"aa","spanId":"01","startTimeUnixNano":"1","endTimeUnixNano":"2","attributes":[{"key":"session_id","value":{"stringValue":"g1"}}]}]}]},
	 {"resource":{"attributes":[{"key":"service.name","value":{"stringValue":"codex"}},{"key":"sdf.fixture","value":{"stringValue":"sample"}}]},
	  "scopeSpans":[{"spans":[{"name":"run_turn","traceId":"bb","spanId":"02","startTimeUnixNano":"1","endTimeUnixNano":"2","attributes":[{"key":"session_id","value":{"stringValue":"c1"}}]}]}]}]}`)
	rows, err := otlp.Decode("traces", body, "application/json")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || deref(rows[0].SessionID) != "g1" || rows[1].SessionID != nil {
		t.Fatalf("%+v", rows)
	}
}
