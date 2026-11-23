package record

import (
	"encoding/json"
	"sort"
	"strings"
)

// Redact drops prompt, response, message, and tool content, and error
// text, from records before they are stored. It is the default for every
// agent, whatever that agent's own settings say: Qwen Code sends
// function_args whatever logPrompts says, a workspace or system Qwen
// settings file can turn logPrompts and includeSensitiveSpanAttributes back
// on over what sdf init writes, Copilot CLI puts messages on spans with
// OTEL_INSTRUMENTATION_GENAI_CAPTURE_CONTENT=SPAN_ONLY, and Gemini CLI puts
// them on spans with telemetry.traces. sdf's summaries need none of that
// text. sdf serve --store-content (RedactWith) is the only way to keep it.
//
// What goes (see IsContentKey for the key rules):
//   - attributes whose key names content, at any depth of a nested map, in
//     record attributes and in the resource, when the value is text (a
//     string, list, or map; a number or bool under such a key, such as a
//     token count named input, is kept);
//   - on logs and spans, top-level error text: error, error.message,
//     error_message, exception.message, exception.stacktrace (any case);
//   - a log body other than the record's own event name;
//   - a span status message, replaced by "tool error" on tool spans,
//     "hook error" on Qwen hook spans, and "error" elsewhere. The status
//     code, error.type, and success are kept.
//
// A git_commit_id inside tool_parameters (Claude Code's git commit tool
// result) is copied to sdf.git_commit_id first, so commit links survive.
// The size of each dropped text value is recorded as <key>_bytes
// (error_bytes for error text, body_bytes for the body), and the dropped
// keys are listed in sdf.redacted. Running Redact again changes nothing.
//
// Records are copied before they are changed; the caller's maps are not
// modified.
func Redact(records []Record) []Record {
	return RedactWith(records, nil)
}

// ContentKept marks a record stored with its content under the opt-in
// (sdf serve --store-content).
const ContentKept = "sdf.content"

// RedactWith is Redact, except that a record for which keep returns true is
// stored as sent (content, error text, and body), marked sdf.content=kept.
// The git_commit_id copy is made either way. A nil keep keeps nothing.
func RedactWith(records []Record, keep func(Record) bool) []Record {
	out := records
	copied := false
	for i, rec := range records {
		var red Record
		var ok bool
		if keep != nil && keep(rec) {
			red, ok = markKept(rec), true
		} else {
			red, ok = redactOne(rec)
		}
		if !ok {
			continue
		}
		if !copied {
			out = append([]Record(nil), records...)
			copied = true
		}
		out[i] = red
	}
	return out
}

// contentKeys are keys (compared lower-cased) whose text value is content.
var contentKeys = map[string]bool{
	"function_args":                  true,
	"prompt":                         true,
	"request_text":                   true,
	"response_text":                  true,
	"tool_input":                     true,
	"tool_output":                    true,
	"tool_parameters":                true,
	"full_command":                   true,
	"arguments":                      true,
	"result":                         true,
	"response":                       true,
	"completion":                     true,
	"messages":                       true,
	"content":                        true,
	"input":                          true,
	"output":                         true,
	"gen_ai.input.messages_ref":      true,
	"gen_ai.output.messages_ref":     true,
	"gen_ai.system_instructions_ref": true,
}

// contentPrefixes match the key itself or the key followed by "." and
// anything (indexed or flattened forms such as gen_ai.prompt.0.content).
var contentPrefixes = []string{
	"gen_ai.prompt",
	"gen_ai.completion",
	"gen_ai.input.messages",
	"gen_ai.output.messages",
	"gen_ai.system_instructions",
	"gen_ai.tool.call.arguments",
	"gen_ai.tool.call.result",
	"gen_ai.tool.definitions",
	"llm.input_messages",
	"llm.output_messages",
	"llm.prompts",
	"input.value",
	"output.value",
}

// errorKeys hold error text on logs and spans (compared lower-cased).
var errorKeys = map[string]bool{
	"error":                true,
	"error.message":        true,
	"error_message":        true,
	"exception.message":    true,
	"exception.stacktrace": true,
}

// IsContentKey reports whether an attribute key names prompt, response,
// message, or tool payload text. Case is ignored.
func IsContentKey(key string) bool {
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

// RedactedKeys lists the exact content keys and the prefixes, for tests and
// docs.
var RedactedKeys = func() []string {
	out := make([]string, 0, len(contentKeys)+len(contentPrefixes))
	for k := range contentKeys {
		out = append(out, k)
	}
	out = append(out, contentPrefixes...)
	sort.Strings(out)
	return out
}()

// statusText is what a span's status message becomes, by record name.
var statusText = map[string]string{
	"qwen-code.tool_call":      "tool error",
	"qwen-code.tool":           "tool error",
	"qwen-code.tool.execution": "tool error",
	"qwen-code.hook":           "hook error",
	"gemini_cli.tool_call":     "tool error",
}

// eventPrefixes are the agent event-name prefixes a log body may carry.
var eventPrefixes = []string{"claude_code.", "codex.", "grok_code.", "gemini_cli.", "qwen-code.", "pi.", "sdf."}

func textual(v any) bool {
	switch v.(type) {
	case string, []any, map[string]any:
		return true
	}
	return false
}

func textSize(v any) int64 {
	switch t := v.(type) {
	case string:
		return int64(len(t))
	case []any:
		n := int64(0)
		for _, x := range t {
			n += textSize(x)
		}
		return n
	case map[string]any:
		n := int64(0)
		for k, x := range t {
			n += int64(len(k)) + textSize(x)
		}
		return n
	}
	return 0
}

type scrub struct {
	dropped []string
	sizes   map[string]int64
}

func (s *scrub) drop(path, sizeKey string, v any) {
	s.dropped = append(s.dropped, path)
	if n := textSize(v); n > 0 && sizeKey != "" {
		if s.sizes == nil {
			s.sizes = map[string]int64{}
		}
		s.sizes[sizeKey] += n
	}
}

// walk returns m without content (and, if errs, top-level error text) keys
// at any depth, or m itself when nothing matched.
func (s *scrub) walk(m map[string]any, path string, errs, sized bool) map[string]any {
	var out map[string]any
	edit := func() {
		if out == nil {
			out = make(map[string]any, len(m))
			for k, v := range m {
				out[k] = v
			}
		}
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		v := m[k]
		full := k
		if path != "" {
			full = path + "." + k
		}
		sizeKey := ""
		switch {
		case textual(v) && (IsContentKey(k) || IsContentKey(full)):
			if sized {
				sizeKey = full + "_bytes"
			}
		case textual(v) && errs && path == "" && errorKeys[strings.ToLower(k)]:
			if sized {
				sizeKey = "error_bytes"
			}
		default:
			if nested, ok := v.(map[string]any); ok {
				before := len(s.dropped)
				cleaned := s.walk(nested, full, errs, sized)
				if len(s.dropped) != before {
					edit()
					out[k] = cleaned
				}
			}
			continue
		}
		edit()
		delete(out, k)
		s.drop(full, sizeKey, v)
	}
	if out == nil {
		return m
	}
	return out
}

// bodyIsName keeps a log body that is only the record's event name; Claude
// Code and Codex put it there and ToolOf reads it.
func bodyIsName(rec Record) bool {
	b := *rec.Body
	if strings.ContainsAny(b, " \t\r\n") || len(b) > 128 {
		return false
	}
	if rec.Name != nil && *rec.Name == b {
		return true
	}
	if s, ok := rec.Attributes["event.name"].(string); ok && s == b {
		return true
	}
	for _, p := range eventPrefixes {
		if strings.HasPrefix(b, p) {
			return true
		}
	}
	return false
}

// parameterCommit is git_commit_id inside tool_parameters, as an object or
// a JSON string.
func parameterCommit(v any) string {
	var m map[string]any
	switch t := v.(type) {
	case map[string]any:
		m = t
	case string:
		raw := strings.TrimSpace(t)
		if !strings.HasPrefix(raw, "{") || json.Unmarshal([]byte(raw), &m) != nil {
			return ""
		}
	}
	if s, ok := m["git_commit_id"].(string); ok {
		return strings.TrimSpace(s)
	}
	return ""
}

func markKept(rec Record) Record {
	out := make(map[string]any, len(rec.Attributes)+2)
	for k, v := range rec.Attributes {
		out[k] = v
	}
	if _, has := out["sdf.git_commit_id"]; !has {
		if commit := parameterCommit(rec.Attributes["tool_parameters"]); commit != "" {
			out["sdf.git_commit_id"] = commit
		}
	}
	out[ContentKept] = "kept"
	rec.Attributes = out
	return rec
}

func redactOne(rec Record) (Record, bool) {
	s := &scrub{}
	errs := rec.Signal == "logs" || rec.Signal == "traces"
	attrs := s.walk(rec.Attributes, "", errs, true)
	resource := s.walk(rec.Resource, "resource", false, false)
	if rec.Body != nil && *rec.Body != "" && !bodyIsName(rec) {
		s.drop("body", "body_bytes", *rec.Body)
		rec.Body = nil
	}
	if rec.StatusMessage != nil && *rec.StatusMessage != "" {
		text, ok := statusText[name(rec)]
		if !ok {
			text = "error"
		}
		if *rec.StatusMessage != text {
			s.dropped = append(s.dropped, "status.message")
			rec.StatusMessage = &text
		}
	}
	commit := ""
	if _, has := rec.Attributes["sdf.git_commit_id"]; !has {
		commit = parameterCommit(rec.Attributes["tool_parameters"])
	}
	if len(s.dropped) == 0 && commit == "" {
		return rec, false
	}
	out := make(map[string]any, len(attrs)+len(s.sizes)+2)
	for k, v := range attrs {
		out[k] = v
	}
	for k, n := range s.sizes {
		if prev, ok := out[k].(int64); ok {
			n += prev
		}
		out[k] = n
	}
	if commit != "" {
		out["sdf.git_commit_id"] = commit
	}
	if len(s.dropped) > 0 {
		seen := map[string]bool{}
		if prev, ok := rec.Attributes["sdf.redacted"].(string); ok && prev != "" {
			for _, k := range strings.Split(prev, ",") {
				seen[k] = true
			}
		}
		for _, k := range s.dropped {
			seen[k] = true
		}
		list := make([]string, 0, len(seen))
		for k := range seen {
			list = append(list, k)
		}
		sort.Strings(list)
		out["sdf.redacted"] = strings.Join(list, ",")
	}
	rec.Attributes = out
	rec.Resource = resource
	return rec, true
}

func name(rec Record) string {
	if s, ok := rec.Attributes["event.name"].(string); ok && s != "" {
		return s
	}
	if rec.Name != nil {
		return *rec.Name
	}
	return ""
}
