package record

import "testing"

func TestRedactDropsFunctionArgsEverywhere(t *testing.T) {
	in := []Record{
		{Signal: "logs", Name: Str("gemini_cli.tool_call"), Attributes: map[string]any{"event.name": "gemini_cli.tool_call", "function_name": "run_shell_command", "function_args": `{"command":"rm -rf /tmp/x"}`, "error.message": "boom secret", "error.type": "tool_error"}},
		{Signal: "logs", Name: Str("other.event"), Attributes: map[string]any{"function_args": "abc", "error.message": "kept"}},
		{Signal: "logs", Name: Str("claude_code.api_error"), Attributes: map[string]any{"error": "rate limited"}},
	}
	out := Redact(in)
	if _, ok := in[0].Attributes["function_args"]; !ok {
		t.Fatal("caller's map was modified")
	}
	g := out[0].Attributes
	if _, ok := g["function_args"]; ok {
		t.Fatal("gemini function_args kept")
	}
	if _, ok := g["error.message"]; ok {
		t.Fatal("gemini tool error text kept")
	}
	if g["function_args_bytes"] != int64(27) || g["error_bytes"] != int64(11) || g["error.type"] != "tool_error" || g["sdf.redacted"] != "error.message,function_args" {
		t.Fatalf("%v", g)
	}
	o := out[1].Attributes
	if _, ok := o["function_args"]; ok || o["error.message"] != "kept" || o["sdf.redacted"] != "function_args" {
		t.Fatalf("%v", o)
	}
	if out[2].Attributes["error"] != "rate limited" || out[2].Attributes["sdf.redacted"] != nil {
		t.Fatalf("non-tool error text should stay: %v", out[2].Attributes)
	}
}

func TestRedactToolSpanStatusMessage(t *testing.T) {
	msg := "Command exited with code 1.\nOutput: secret"
	in := []Record{{Signal: "traces", Name: Str("qwen-code.tool.execution"), StatusCode: "ERROR", StatusMessage: &msg,
		Attributes: map[string]any{"error": msg, "error.type": "tool_error", "gen_ai.tool.name": "run_shell_command"}}}
	out := Redact(in)
	if *in[0].StatusMessage != msg {
		t.Fatal("caller's record changed")
	}
	if deref(out[0].StatusMessage) != "tool error" || out[0].Attributes["error"] != nil || out[0].Attributes["error.type"] != "tool_error" || out[0].Attributes["sdf.redacted"] != "error,status.message" {
		t.Fatalf("%v %q", out[0].Attributes, deref(out[0].StatusMessage))
	}
	clean := []Record{{Signal: "traces", Name: Str("qwen-code.tool"), StatusCode: "OK", Attributes: map[string]any{"success": true}}}
	if got := Redact(clean); &got[0] != &clean[0] {
		t.Fatal("clean batch was copied")
	}
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
