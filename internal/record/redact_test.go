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

func TestRedactDropsGenAIContentFromEveryAgent(t *testing.T) {
	content := map[string]any{
		"gen_ai.tool.call.arguments": `{"command":"cat secret"}`,
		"gen_ai.tool.call.result":    "secret output",
		"gen_ai.input.messages":      `[{"role":"user"}]`,
		"gen_ai.output.messages":     []any{map[string]any{"role": "assistant", "content": "secret"}},
		"gen_ai.system_instructions": "secret system",
	}
	with := func(extra map[string]any) map[string]any {
		out := map[string]any{}
		for k, v := range content {
			out[k] = v
		}
		for k, v := range extra {
			out[k] = v
		}
		return out
	}
	in := []Record{
		{Signal: "traces", Name: Str("qwen-code.tool"), Attributes: with(map[string]any{"gen_ai.tool.name": "run_shell_command"})},
		{Signal: "traces", Name: Str("qwen-code.llm_request"), Attributes: with(map[string]any{"gen_ai.usage.input_tokens": int64(10)})},
		{Signal: "logs", Name: Str("gen_ai.client.inference.operation.details"), Attributes: with(nil)},
		{Signal: "traces", Name: Str("chat"), ServiceName: Str("some-other-agent"), Attributes: with(nil)},
	}
	out := Redact(in)
	if _, ok := in[0].Attributes["gen_ai.tool.call.arguments"]; !ok {
		t.Fatal("caller's map was modified")
	}
	for i, rec := range out {
		for key := range content {
			if _, ok := rec.Attributes[key]; ok {
				t.Fatalf("record %d kept %s", i, key)
			}
		}
		if rec.Attributes["sdf.redacted"] != "gen_ai.input.messages,gen_ai.output.messages,gen_ai.system_instructions,gen_ai.tool.call.arguments,gen_ai.tool.call.result" {
			t.Fatalf("record %d: %v", i, rec.Attributes["sdf.redacted"])
		}
		if rec.Attributes["gen_ai.tool.call.arguments_bytes"] != int64(24) {
			t.Fatalf("record %d: arguments size %v", i, rec.Attributes["gen_ai.tool.call.arguments_bytes"])
		}
	}
	if out[0].Attributes["gen_ai.tool.name"] != "run_shell_command" || out[1].Attributes["gen_ai.usage.input_tokens"] != int64(10) {
		t.Fatalf("metadata lost: %v %v", out[0].Attributes, out[1].Attributes)
	}
}

func TestRedactQwenHookErrorAndStatus(t *testing.T) {
	msg := "hook stderr: token=abc"
	in := []Record{{Signal: "traces", Name: Str("qwen-code.hook"), StatusCode: "ERROR", StatusMessage: &msg,
		Attributes: map[string]any{"error": msg, "hook_event": "PreToolUse", "success": false}}}
	out := Redact(in)
	if *in[0].StatusMessage != msg || in[0].Attributes["error"] != msg {
		t.Fatal("caller's record changed")
	}
	a := out[0].Attributes
	if deref(out[0].StatusMessage) != "hook error" || a["error"] != nil || a["error_bytes"] != int64(len(msg)) || a["hook_event"] != "PreToolUse" || a["sdf.redacted"] != "error,status.message" || out[0].StatusCode != "ERROR" {
		t.Fatalf("%v %q", a, deref(out[0].StatusMessage))
	}
	// Other agents' non-tool spans keep their status text.
	other := "upstream 503"
	kept := Redact([]Record{{Signal: "traces", Name: Str("claude_code.llm_request"), StatusCode: "ERROR", StatusMessage: &other}})
	if deref(kept[0].StatusMessage) != other {
		t.Fatalf("status %q", deref(kept[0].StatusMessage))
	}
}
