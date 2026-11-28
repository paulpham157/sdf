package record

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strconv"
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
//   - attributes whose key names content, at any depth of nested maps and
//     lists, in record attributes and in the resource, when the value is
//     text (a string, list, or map; a number or bool under such a key, such
//     as a token count named input, is kept);
//   - on logs and spans, error text at any depth: error, error.message,
//     error_message, errorMessage, error.stack, exception.message,
//     exception.stacktrace, exception.stack, stack, stacktrace (any case);
//   - an error class or code (error.type, exception.type, status codes,
//     finish reasons; see errorInfoKeys) that is not a short token;
//   - a log body other than an event name, and the record name too when
//     the decoder copied the body into it (a log with no event name);
//   - a span status message. On an ERROR span it is replaced by text built
//     from the kept class and code only ("tool error: tool_error",
//     "error: HTTP 429 Too Many Requests"); on any other span it is cleared.
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
	"stdout":                         true,
	"stderr":                         true,
	"command":                        true,
	"query":                          true,
	"user_prompt":                    true,
	"message":                        true,
	"text":                           true,
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
	"gen_ai.choice",
	"tool.input",
	"tool.output",
	// Vercel AI SDK spans (opencode with experimental.openTelemetry):
	// ai.prompt, ai.prompt.messages, ai.response.text, ai.toolCall.args,
	// ai.toolCall.result, and the rest. Numbers (ai.usage.*) are kept.
	"ai",
}

// contentTree matches a content prefix on segment boundaries: the key equals
// the prefix, or the prefix is followed by ".". A linear scan of the prefix
// list was a large share of ingest CPU.
var contentTree = func() *contentNode {
	root := &contentNode{child: map[string]*contentNode{}}
	for _, p := range contentPrefixes {
		root.add(p)
	}
	return root
}()

type contentNode struct {
	terminal bool
	child    map[string]*contentNode
}

func (n *contentNode) add(prefix string) {
	node := n
	rest := prefix
	for {
		seg, next, ok := strings.Cut(rest, ".")
		child := node.child[seg]
		if child == nil {
			child = &contentNode{child: map[string]*contentNode{}}
			node.child[seg] = child
		}
		if !ok {
			child.terminal = true
			return
		}
		node = child
		rest = next
	}
}

// match reports whether key equals a content prefix or continues past one
// with a dot. key must already be lower case.
func (n *contentNode) match(key string) bool {
	node := n
	rest := key
	for {
		seg, next, ok := strings.Cut(rest, ".")
		child := node.child[seg]
		if child == nil {
			return false
		}
		if child.terminal {
			return true
		}
		if !ok {
			return false
		}
		node = child
		rest = next
	}
}

// errorKeys hold error text on logs and spans (compared lower-cased).
// They are dropped at any depth.
var errorKeys = map[string]bool{
	"error":                true,
	"error.message":        true,
	"error_message":        true,
	"errormessage":         true,
	"error.stack":          true,
	"exception.message":    true,
	"exception.stacktrace": true,
	"exception.stack":      true,
	"stack":                true,
	"stacktrace":           true,
}

// errorInfoKeys (compared lower-cased) hold an error class, a status code,
// or a finish reason. They are kept, at any depth and on every signal, only
// while the value is a short token (or a list of them); anything else could
// be sent text and is dropped, its size recorded as error_bytes.
var errorInfoKeys = map[string]bool{
	"error.type":                     true,
	"error_type":                     true,
	"exception.type":                 true,
	"status_code":                    true,
	"http.response.status_code":      true,
	"http.status_code":               true,
	"rpc.grpc.status_code":           true,
	"finish_reason":                  true,
	"finish_reasons":                 true,
	"gen_ai.response.finish_reasons": true,
}

var tokenPattern = regexp.MustCompile(`^[A-Za-z0-9_.:/-]{1,64}$`)

// IsErrorToken reports whether s is a value sdf keeps for an error class,
// code, or finish reason: ^[A-Za-z0-9_.:/-]{1,64}$. Readers that show such
// a value (the span tree's error_type) use it too, so a free-text class in
// a row kept with --store-content or written by an older sdf is not shown.
func IsErrorToken(s string) bool { return tokenPattern.MatchString(s) }

// isToken reports whether v is a short token or a list of them.
func isToken(v any) bool {
	switch t := v.(type) {
	case string:
		return tokenPattern.MatchString(t)
	case []any:
		for _, x := range t {
			if s, ok := x.(string); ok {
				if !tokenPattern.MatchString(s) {
					return false
				}
			} else if textual(x) {
				return false
			}
		}
		return true
	}
	return false
}

// IsContentKey reports whether an attribute key names prompt, response,
// message, or tool payload text. Case is ignored.
func IsContentKey(key string) bool {
	if contentKeys[key] || contentTree.match(key) {
		return true
	}
	folded, changed := foldASCIIKey(key)
	if !changed {
		return false
	}
	return contentKeys[folded] || contentTree.match(folded)
}

// foldASCIIKey returns key with A-Z folded to a-z. changed is false when
// key is already ASCII lowercase, so the caller can skip a second lookup.
// A non-ASCII byte uses strings.ToLower so the result matches the old
// comparison.
func foldASCIIKey(key string) (string, bool) {
	upper := -1
	for i := 0; i < len(key); i++ {
		c := key[i]
		if c >= 0x80 {
			return strings.ToLower(key), true
		}
		if upper < 0 && c >= 'A' && c <= 'Z' {
			upper = i
		}
	}
	if upper < 0 {
		return key, false
	}
	b := make([]byte, len(key))
	copy(b, key)
	for j := upper; j < len(b); j++ {
		if b[j] >= 'A' && b[j] <= 'Z' {
			b[j] += 'a' - 'A'
		}
	}
	return string(b), true
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

// qwenEmptyHookMessage is the status Qwen Code sets on qwen-code.hook when
// the hook runner returns no output (v0.29.0 toolHookTriggers: success with
// no output). With no hook configured for that event, every PreToolUse and
// PostToolUse span is ERROR and error.type is hook_error, even when the
// tool call succeeded. sdf init qwen installs only a Stop hook, so those
// two events are this sentinel on a normal run.
const qwenEmptyHookMessage = "hook runner returned no output without error detail"

// QwenUnconfiguredHook is the hook event (PreToolUse, PostToolUse, ...) when
// rec is that empty-runner span, or was already labeled sdf.hook.unconfigured
// at ingest. A different status message is a hook that actually failed.
func QwenUnconfiguredHook(rec Record) (string, bool) {
	if !qwenHookSpan(rec) {
		return "", false
	}
	if s, ok := rec.Attributes["sdf.hook.unconfigured"].(string); ok && hookEventToken(s) {
		return s, true
	}
	return qwenEmptyHook(rec)
}

func qwenHookSpan(rec Record) bool {
	if rec.Signal != "traces" || rec.Name == nil {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(*rec.Name), "qwen-code.hook")
}

// qwenEmptyHook matches the span before redaction replaces the status
// message. The event name is kept only when it is a short token; anything
// else is stored as "unknown" so a status string cannot land in the label.
func qwenEmptyHook(rec Record) (string, bool) {
	if !qwenHookSpan(rec) || rec.StatusCode != "ERROR" || rec.StatusMessage == nil {
		return "", false
	}
	if strings.TrimSpace(*rec.StatusMessage) != qwenEmptyHookMessage {
		return "", false
	}
	event := "unknown"
	if s, ok := rec.Attributes["hook_event"].(string); ok && hookEventToken(s) {
		event = s
	}
	return event, true
}

func hookEventToken(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' {
			continue
		}
		return false
	}
	return true
}

// errorKind is "hook error" for a hook span, "tool error" for a tool span,
// and "error" otherwise. It reads the words of the span name in any case
// and separator (Qwen-Code.Hook, execute_tool bash, Tool.execute,
// tool_call), then gen_ai.operation.name and a tool name attribute.
func errorKind(rec Record) string {
	words := strings.FieldsFunc(strings.ToLower(name(rec)), func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9')
	})
	has := func(w ...string) bool {
		for _, x := range words {
			for _, y := range w {
				if x == y {
					return true
				}
			}
		}
		return false
	}
	switch {
	case has("hook", "hooks"):
		return "hook error"
	case has("tool", "tools"):
		return "tool error"
	}
	if op, ok := rec.Attributes["gen_ai.operation.name"].(string); ok && strings.EqualFold(op, "execute_tool") {
		return "tool error"
	}
	for _, k := range []string{"gen_ai.tool.name", "tool_name", "tool.name"} {
		if s, ok := rec.Attributes[k].(string); ok && s != "" {
			return "tool error"
		}
	}
	return "error"
}

// errorDetail is the kept error class and status code of a span, written
// by sdf: "FileNotFoundError", "HTTP 429 Too Many Requests", "gRPC code
// 14". Only token values are used, and the HTTP reason phrase comes from
// net/http, never from the sent text.
func errorDetail(attrs map[string]any) string {
	var bits []string
	for _, k := range []string{"error.type", "error_type", "exception.type"} {
		if s, ok := attrs[k].(string); ok && tokenPattern.MatchString(s) {
			bits = append(bits, s)
			break
		}
	}
	for _, k := range []string{"http.response.status_code", "http.status_code"} {
		if code, ok := intOf(attrs[k]); ok && code >= 100 && code <= 999 {
			if reason := http.StatusText(code); reason != "" {
				bits = append(bits, fmt.Sprintf("HTTP %d %s", code, reason))
			} else {
				bits = append(bits, fmt.Sprintf("HTTP %d", code))
			}
			break
		}
	}
	if code, ok := intOf(attrs["rpc.grpc.status_code"]); ok && code >= 0 && code <= 99 {
		bits = append(bits, fmt.Sprintf("gRPC code %d", code))
	}
	return strings.Join(bits, ", ")
}

func intOf(v any) (int, bool) {
	switch t := v.(type) {
	case int64:
		return int(t), true
	case int:
		return t, true
	case float64:
		if t == float64(int(t)) {
			return int(t), true
		}
	case string:
		if n, err := strconv.Atoi(t); err == nil {
			return n, true
		}
	}
	return 0, false
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

// walk returns m without content keys (and, if errs, error text keys) at
// any depth of nested maps and lists, or m itself when nothing matched.
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
		case textual(v) && errs && errorKeys[strings.ToLower(k)]:
			if sized {
				sizeKey = "error_bytes"
			}
		case textual(v) && errorInfoKeys[strings.ToLower(k)] && !isToken(v):
			if sized {
				sizeKey = "error_bytes"
			}
		default:
			before := len(s.dropped)
			cleaned := s.value(v, full, errs, sized)
			if len(s.dropped) != before {
				edit()
				out[k] = cleaned
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

// value walks the maps inside v (a map, or a list at any depth); list
// elements share the list's path. It returns v itself when nothing matched.
func (s *scrub) value(v any, path string, errs, sized bool) any {
	switch t := v.(type) {
	case map[string]any:
		return s.walk(t, path, errs, sized)
	case []any:
		var out []any
		for i, x := range t {
			before := len(s.dropped)
			cleaned := s.value(x, path, errs, sized)
			if len(s.dropped) != before {
				if out == nil {
					out = append([]any(nil), t...)
				}
				out[i] = cleaned
			}
		}
		if out != nil {
			return out
		}
	}
	return v
}

// bodyIsName keeps a log body that is only an event name: the record's
// event.name, or an agent-prefixed name (Claude Code and Codex put it
// there and ToolOf reads it). The record name is not evidence: for a log
// with no event name the decoder copies the body into it.
func bodyIsName(rec Record) bool {
	b := *rec.Body
	if strings.ContainsAny(b, " \t\r\n") || len(b) > 128 {
		return false
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
	// Read the sentinel before the status message is replaced. Qwen's
	// empty-runner text is not a failed tool, and after this pass the
	// original message is gone.
	hookEvent, unconfigured := qwenEmptyHook(rec)
	s := &scrub{}
	errs := rec.Signal == "logs" || rec.Signal == "traces"
	attrs := s.walk(rec.Attributes, "", errs, true)
	resource := s.walk(rec.Resource, "resource", false, false)
	if unconfigured {
		// walk returns the input map when it drops nothing. Copy before
		// writing so the caller's attributes stay as they arrived.
		copied := make(map[string]any, len(attrs)+1)
		for k, v := range attrs {
			copied[k] = v
		}
		attrs = copied
		attrs["sdf.hook.unconfigured"] = hookEvent
		if et, ok := attrs["error.type"].(string); ok && et == "hook_error" {
			delete(attrs, "error.type")
			s.dropped = append(s.dropped, "error.type")
		}
		if rec.StatusMessage != nil && *rec.StatusMessage != "" {
			s.dropped = append(s.dropped, "status.message")
		}
		rec.StatusCode = "UNSET"
		rec.StatusMessage = nil
	}
	if rec.Body != nil && *rec.Body != "" && !bodyIsName(rec) {
		body := *rec.Body
		s.drop("body", "body_bytes", body)
		rec.Body = nil
		// A log with no event name has its body copied into name
		// (otlp.logName); that copy is the same text.
		if rec.Name != nil && *rec.Name == body {
			s.dropped = append(s.dropped, "name")
			rec.Name = nil
		}
	}
	if rec.StatusMessage != nil && *rec.StatusMessage != "" {
		if rec.StatusCode == "ERROR" {
			text := errorKind(rec)
			if detail := errorDetail(attrs); detail != "" {
				text += ": " + detail
			}
			if *rec.StatusMessage != text {
				s.dropped = append(s.dropped, "status.message")
				rec.StatusMessage = &text
			}
		} else {
			// An OK or UNSET span's message is not an error; it is
			// sent text, and nothing reads it.
			s.dropped = append(s.dropped, "status.message")
			rec.StatusMessage = nil
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
