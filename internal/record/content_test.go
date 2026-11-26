package record

import (
	"strings"
	"testing"
)

func TestIsContentKey(t *testing.T) {
	for _, key := range []string{
		"prompt", "Prompt", "request_text", "response_text", "tool_input", "TOOL_INPUT", "tool_output",
		"tool_parameters", "full_command", "arguments", "result", "response", "completion", "messages",
		"content", "input", "output", "function_args", "gen_ai.prompt", "gen_ai.completion",
		"gen_ai.prompt.0.content", "gen_ai.completion.1.role", "gen_ai.input.messages",
		"gen_ai.input.messages.0.content", "Gen_AI.Input.Messages", "GEN_AI.OUTPUT.MESSAGES",
		"gen_ai.system_instructions", "gen_ai.tool.call.arguments", "gen_ai.tool.call.result",
		"gen_ai.tool.definitions", "gen_ai.input.messages_ref", "llm.input_messages",
		"llm.output_messages.0.message.content", "llm.prompts", "input.value", "output.value",
		// Under the gen_ai.prompt prefix, so dropped too (Gemini's prompt name).
		"gen_ai.prompt.name",
	} {
		if !IsContentKey(key) {
			t.Errorf("%s is content", key)
		}
	}
	for _, key := range []string{
		"input_tokens", "output_tokens", "gen_ai.usage.input_tokens", "gen_ai.usage.prompt_tokens",
		"gen_ai.prompt_tokens", "prompt_id", "prompt_length", "tool_name",
		"gen_ai.tool.name", "gen_ai.tool.call.id", "gen_ai.request.model", "gen_ai.input.messages_count",
		"error.type", "session.id", "event.name", "model", "type", "messages_count", "input.mime_type",
	} {
		if IsContentKey(key) {
			t.Errorf("%s is not content", key)
		}
	}
}

// TestIsContentKeyMatchesPrefixScan compares the segment tree with the
// previous linear scan, including mixed case and keys that share a prefix
// up to a character that is not a dot.
func TestIsContentKeyMatchesPrefixScan(t *testing.T) {
	keys := []string{
		"prompt", "Prompt", "PROMPT", "ai", "AI", "Ai.prompt", "aim", "air",
		"ai.prompt", "ai.usage.promptTokens", "gen_ai.prompt", "gen_ai.prompt.0.content",
		"gen_ai.prompt_tokens", "gen_ai.prompts", "Gen_AI.Input.Messages",
		"GEN_AI.OUTPUT.MESSAGES", "gen_ai.input.messages_ref", "gen_ai.input.messages_count",
		"input", "input.value", "input.value.extra", "input.mime_type", "tool.input",
		"tool.input.x", "tool.inputs", "tool.output", "session.id", "", "ai.", ".ai",
		"gen_ai.tool.call.arguments", "gen_ai.tool.call.arguments.0", "gen_ai.tool.call.id",
		"llm.prompts.0", "İnput",
	}
	for _, key := range keys {
		if got, want := IsContentKey(key), contentKeySlow(key); got != want {
			t.Errorf("IsContentKey(%q) = %v, slow scan %v", key, got, want)
		}
	}
}

func contentKeySlow(key string) bool {
	k := strings.ToLower(key)
	if contentKeys[k] {
		return true
	}
	for _, p := range contentPrefixes {
		if k == p || strings.HasPrefix(k, p+".") {
			return true
		}
	}
	return false
}

func TestRedactNestedResourceBodyAndNumbers(t *testing.T) {
	in := []Record{{
		Signal: "logs", Name: Str("qwen-code.api_request"), Body: Str("Request text: secret prompt"),
		Attributes: map[string]any{
			"event.name": "qwen-code.api_request",
			"input":      int64(1200),
			"messages":   int64(3),
			"gen_ai":     map[string]any{"input": map[string]any{"messages": "secret nested"}, "model": "m"},
			"meta":       map[string]any{"Prompt": "secret meta", "count": int64(2)},
		},
		Resource: map[string]any{"service.name": "qwen-code", "gen_ai.system_instructions": "secret resource"},
	}}
	out := Redact(in)
	r := out[0]
	if r.Body != nil || r.Attributes["body_bytes"] != int64(27) {
		t.Fatalf("body %v %v", r.Body, r.Attributes["body_bytes"])
	}
	if r.Attributes["input"] != int64(1200) || r.Attributes["messages"] != int64(3) {
		t.Fatalf("numbers under content keys must stay: %v", r.Attributes)
	}
	g := r.Attributes["gen_ai"].(map[string]any)
	// gen_ai.input is itself a content key (input) holding text, so it goes whole.
	if g["model"] != "m" || g["input"] != nil {
		t.Fatalf("nested %v", g)
	}
	if m := r.Attributes["meta"].(map[string]any); m["Prompt"] != nil || m["count"] != int64(2) {
		t.Fatalf("meta %v", m)
	}
	if r.Resource["gen_ai.system_instructions"] != nil || r.Resource["service.name"] != "qwen-code" {
		t.Fatalf("resource %v", r.Resource)
	}
	if got := r.Attributes["sdf.redacted"]; got != "body,gen_ai.input,meta.Prompt,resource.gen_ai.system_instructions" {
		t.Fatalf("sdf.redacted %v", got)
	}
	if in[0].Body == nil || in[0].Resource["gen_ai.system_instructions"] == nil || in[0].Attributes["gen_ai"].(map[string]any)["input"].(map[string]any)["messages"] == nil {
		t.Fatal("caller's record changed")
	}
	again := Redact(out)
	if &again[0] != &out[0] {
		t.Fatalf("second pass changed the record: %v", again[0].Attributes)
	}
}

func TestRedactKeepsEventNameBodies(t *testing.T) {
	for _, body := range []string{"claude_code.api_request", "codex.sse_event"} {
		rec := Record{Signal: "logs", Name: Str("custom.event"), Body: Str(body), Attributes: map[string]any{}}
		out := Redact([]Record{rec})[0]
		if deref(out.Body) != body {
			t.Errorf("body %q dropped", body)
		}
	}
	for _, body := range []string{"custom.event"} {
		rec := Record{Signal: "logs", Name: Str("custom.event"), Body: Str(body), Attributes: map[string]any{"event.name": "custom.event"}}
		out := Redact([]Record{rec})[0]
		if deref(out.Body) != body {
			t.Errorf("body %q dropped", body)
		}
	}
	rec := Record{Signal: "logs", Name: Str("other"), Body: Str("SDF-LEAK-x"), Attributes: map[string]any{}}
	if out := Redact([]Record{rec})[0]; out.Body != nil {
		t.Fatal("a body that is not the event name must go, even without spaces")
	}
}

func TestRedactCopiesTheCommitOutOfToolParameters(t *testing.T) {
	sha := "0123456789abcdef0123456789abcdef01234567"
	for _, params := range []any{
		`{"bash_command":"git commit -m secret","git_commit_id":"` + sha + `"}`,
		map[string]any{"bash_command": "git commit -m secret", "git_commit_id": sha},
	} {
		out := Redact([]Record{{Signal: "logs", Name: Str("claude_code.tool_result"), Attributes: map[string]any{"tool_parameters": params, "tool_name": "Bash"}}})[0]
		if out.Attributes["sdf.git_commit_id"] != sha || out.Attributes["tool_parameters"] != nil || out.Attributes["tool_name"] != "Bash" {
			t.Fatalf("%v", out.Attributes)
		}
	}
}

func TestRedactWithKeepsContentForChosenRecords(t *testing.T) {
	msg := "upstream 503"
	in := []Record{
		{Signal: "traces", Name: Str("keep"), StatusMessage: &msg, Attributes: map[string]any{"prompt": "hello", "tool_parameters": `{"git_commit_id":"0123456789abcdef0123456789abcdef01234567"}`}},
		{Signal: "traces", Name: Str("drop"), StatusCode: "ERROR", StatusMessage: &msg, Attributes: map[string]any{"prompt": "hello"}},
	}
	out := RedactWith(in, func(r Record) bool { return deref(r.Name) == "keep" })
	k, d := out[0], out[1]
	if k.Attributes["prompt"] != "hello" || deref(k.StatusMessage) != msg || k.Attributes[ContentKept] != "kept" || k.Attributes["sdf.git_commit_id"] == nil || k.Attributes["tool_parameters"] == nil {
		t.Fatalf("kept %v %q", k.Attributes, deref(k.StatusMessage))
	}
	if d.Attributes["prompt"] != nil || deref(d.StatusMessage) != "error" || d.Attributes[ContentKept] != nil {
		t.Fatalf("dropped %v", d.Attributes)
	}
	if _, ok := in[0].Attributes[ContentKept]; ok {
		t.Fatal("caller's map changed")
	}
}
