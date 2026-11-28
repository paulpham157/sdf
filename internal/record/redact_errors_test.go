package record

import (
	"reflect"
	"strings"
	"testing"
)

func redactOneRec(t *testing.T, rec Record) Record {
	t.Helper()
	out := Redact([]Record{rec})
	again := Redact(out)
	if &again[0] != &out[0] {
		t.Fatalf("second pass changed the record: %+v", again[0])
	}
	return out[0]
}

// A log with no event name gets its body copied into name by the decoder;
// dropping the body must drop that copy too, whatever the body looks like.
func TestRedactDropsTheBodyCopiedIntoName(t *testing.T) {
	for _, body := range []string{"user said SDF-LEAK-prose secret things", "SDF-LEAK-single-token"} {
		out := redactOneRec(t, Record{Signal: "logs", Name: Str(body), Body: Str(body), Attributes: map[string]any{}})
		if out.Body != nil || out.Name != nil {
			t.Fatalf("%q: body %q name %q", body, deref(out.Body), deref(out.Name))
		}
		if out.Attributes["sdf.redacted"] != "body,name" || out.Attributes["body_bytes"] != int64(len(body)) {
			t.Fatalf("%q: %v", body, out.Attributes)
		}
	}
	// A name from event.name is not the body's copy and stays.
	out := redactOneRec(t, Record{Signal: "logs", Name: Str("custom.event"), Body: Str("some text"), Attributes: map[string]any{"event.name": "custom.event"}})
	if deref(out.Name) != "custom.event" || out.Body != nil {
		t.Fatalf("%q %q", deref(out.Name), deref(out.Body))
	}
}

func TestRedactWalksListsAndNestedErrors(t *testing.T) {
	out := redactOneRec(t, Record{Signal: "logs", Name: Str("x"), Attributes: map[string]any{
		"conversation": []any{map[string]any{"role": "user", "content": "SDF-LEAK-a"}, []any{map[string]any{"prompt": "SDF-LEAK-b"}}, "plain"},
		"tool":         map[string]any{"name": "Bash", "error": "SDF-LEAK-c", "errorMessage": "SDF-LEAK-d", "result": map[string]any{"stack": "SDF-LEAK-e"}},
		"steps":        []any{map[string]any{"exception.stack": "SDF-LEAK-f", "id": int64(1)}},
	}})
	got := out.Attributes
	if strings.Contains(stringOf(got), "SDF-LEAK") {
		t.Fatalf("kept: %v", got)
	}
	want := []any{map[string]any{"role": "user"}, []any{map[string]any{}}, "plain"}
	if !reflect.DeepEqual(got["conversation"], want) || got["tool"].(map[string]any)["name"] != "Bash" || got["steps"].([]any)[0].(map[string]any)["id"] != int64(1) {
		t.Fatalf("%v", got)
	}
	if got["sdf.redacted"] != "conversation.content,conversation.prompt,steps.exception.stack,tool.error,tool.errorMessage,tool.result" {
		t.Fatalf("sdf.redacted %v", got["sdf.redacted"])
	}
}

func TestRedactDropsMoreContentKeys(t *testing.T) {
	keys := []string{"ai.prompt", "ai.prompt.messages", "ai.response.text", "ai.response.toolCalls", "ai.toolCall.args", "ai.toolCall.result", "ai.model.id",
		"stdout", "stderr", "command", "query", "user_prompt", "message", "text", "gen_ai.choice", "gen_ai.choice.0.message", "tool.input", "tool.output.text"}
	attrs := map[string]any{"ai.usage.promptTokens": int64(12), "errorMessage": "x", "error.stack": "x", "exception.stack": "x"}
	for _, k := range keys {
		attrs[k] = "SDF-LEAK-" + k
	}
	out := redactOneRec(t, Record{Signal: "traces", Name: Str("ai.streamText"), Attributes: attrs})
	for _, k := range append(keys, "errorMessage", "error.stack", "exception.stack") {
		if _, ok := out.Attributes[k]; ok {
			t.Errorf("%s kept", k)
		}
	}
	if out.Attributes["ai.usage.promptTokens"] != int64(12) {
		t.Fatalf("token count dropped: %v", out.Attributes)
	}
}

func TestRedactKeepsOnlyTokenErrorInfo(t *testing.T) {
	attrs := map[string]any{
		"error.type":                     "FileNotFoundError",
		"exception.type":                 "java.io.IOException",
		"error_type":                     "free text with SDF-LEAK-g",
		"http.response.status_code":      int64(429),
		"http.status_code":               "429 because SDF-LEAK-h",
		"rpc.grpc.status_code":           int64(14),
		"gen_ai.response.finish_reasons": []any{"stop", "length"},
		"finish_reasons":                 []any{"stop", "SDF-LEAK-i said no"},
		"finish_reason":                  strings.Repeat("x", 65),
		"status_code":                    "ERR_TIMEOUT",
	}
	out := redactOneRec(t, Record{Signal: "metrics", Name: Str("m"), Attributes: attrs})
	for _, k := range []string{"error.type", "exception.type", "http.response.status_code", "rpc.grpc.status_code", "gen_ai.response.finish_reasons", "status_code"} {
		if !reflect.DeepEqual(out.Attributes[k], attrs[k]) {
			t.Errorf("%s: %v", k, out.Attributes[k])
		}
	}
	for _, k := range []string{"error_type", "http.status_code", "finish_reasons", "finish_reason"} {
		if _, ok := out.Attributes[k]; ok {
			t.Errorf("%s kept", k)
		}
	}
	if out.Attributes["error_bytes"] == nil {
		t.Fatalf("no size recorded: %v", out.Attributes)
	}
}

func TestRedactBuildsTheStatusFromKeptFields(t *testing.T) {
	msg := "SDF-LEAK-status: Command exited with code 1"
	for _, tc := range []struct {
		name  string
		attrs map[string]any
		want  string
	}{
		{"execute_tool bash", map[string]any{"error.type": "tool_error"}, "tool error: tool_error"},
		{"Tool.execute", map[string]any{}, "tool error"},
		{"tool_call", map[string]any{"exception.type": "FileNotFoundError"}, "tool error: FileNotFoundError"},
		{"gemini_cli.tool_call", map[string]any{}, "tool error"},
		{"Qwen-Code.Hook", map[string]any{"error.type": "hook_failed"}, "hook error: hook_failed"},
		{"qwen-code.hook", map[string]any{}, "hook error"},
		{"chat m-1", map[string]any{"http.response.status_code": int64(429)}, "error: HTTP 429 Too Many Requests"},
		{"chat m-1", map[string]any{"error.type": "api_error", "http.status_code": "503"}, "error: api_error, HTTP 503 Service Unavailable"},
		{"chat m-1", map[string]any{"http.response.status_code": int64(599)}, "error: HTTP 599"},
		{"grpc call", map[string]any{"rpc.grpc.status_code": int64(14)}, "error: gRPC code 14"},
		{"invoke_agent", map[string]any{"gen_ai.operation.name": "execute_tool"}, "tool error"},
		{"chat m-1", map[string]any{"error.type": "not a token SDF-LEAK-j"}, "error"},
		{"SessionStep.attempt", map[string]any{}, "error"},
	} {
		out := redactOneRec(t, Record{Signal: "traces", Name: Str(tc.name), StatusCode: "ERROR", StatusMessage: Str(msg), Attributes: tc.attrs})
		if deref(out.StatusMessage) != tc.want {
			t.Errorf("%s %v: %q, want %q", tc.name, tc.attrs, deref(out.StatusMessage), tc.want)
		}
	}
	for _, code := range []string{"OK", "UNSET", ""} {
		out := redactOneRec(t, Record{Signal: "traces", Name: Str("chat"), StatusCode: code, StatusMessage: Str(msg), Attributes: map[string]any{}})
		if out.StatusMessage != nil || out.Attributes["sdf.redacted"] != "status.message" {
			t.Errorf("%q span: message %q, %v", code, deref(out.StatusMessage), out.Attributes)
		}
	}
}

func TestRedactKeepsQwenHookStatus(t *testing.T) {
	msg := "hook runner returned no output without error detail"
	if len(msg) != 51 {
		t.Fatalf("sentinel length %d", len(msg))
	}
	out := redactOneRec(t, Record{Signal: "traces", ServiceName: Str("qwen-code"), Name: Str("qwen-code.hook"), StatusCode: "ERROR", StatusMessage: Str(msg),
		Attributes: map[string]any{"error": msg, "error.type": "hook_error", "hook_event": "PreToolUse", "success": false}})
	if out.StatusCode != "ERROR" || deref(out.StatusMessage) != "hook error: hook_error" {
		t.Fatalf("status %s %q", out.StatusCode, deref(out.StatusMessage))
	}
	if out.Attributes["error.type"] != "hook_error" || out.Attributes["hook_event"] != "PreToolUse" || out.Attributes["error"] != nil || out.Attributes["sdf.hook.unconfigured"] != nil || out.Attributes["error_bytes"] != int64(51) {
		t.Fatalf("%v", out.Attributes)
	}
}

func stringOf(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case []any:
		var b strings.Builder
		for _, x := range t {
			b.WriteString(stringOf(x) + " ")
		}
		return b.String()
	case map[string]any:
		var b strings.Builder
		for k, x := range t {
			b.WriteString(k + "=" + stringOf(x) + " ")
		}
		return b.String()
	}
	return ""
}
