package record

import (
	"sort"
	"strings"
)

// Redact drops tool arguments and tool error text before records are stored.
//
// Qwen Code v0.28.4 (packages/core/src/telemetry/loggers.ts, logToolCall)
// puts the full JSON of every tool call's arguments in function_args on
// qwen-code.tool_call logs whatever logPrompts says: shell commands, and the
// whole content of write_file and edit calls. A failed call also carries the
// tool's error text in error and error.message, and its qwen-code.tool and
// qwen-code.tool.execution spans repeat that text in error and the span
// status message. Gemini CLI sends function_args on gemini_cli.tool_call when
// its logPrompts is on, and error.message on failed calls. sdf needs none of
// that text, so it keeps the tool name, call id, duration, success, and error
// type, records the size of what it dropped (function_args_bytes,
// error_bytes), and lists the dropped keys in sdf.redacted.
//
// Qwen Code also copies messages and tool data into its spans when
// telemetry.includeSensitiveSpanAttributes is on, and a workspace
// (<cwd>/.qwen/settings.json) or system settings file can turn that, or
// logPrompts, back on over what sdf init writes: gen_ai.tool.call.arguments
// and gen_ai.tool.call.result on qwen-code.tool, and gen_ai.input.messages,
// gen_ai.output.messages, and gen_ai.system_instructions on
// qwen-code.llm_request. These are the OpenTelemetry GenAI content
// attributes, so they are dropped too.
//
// The keys in RedactedKeys are dropped from every record (log, span, or
// metric) of every agent, whatever its settings. The error text is dropped
// only from the tool-call and hook records named in toolErrorRecords, so
// other agents' API error messages stay readable.
//
// Records are copied before they are changed; the caller's maps are not
// modified.
func Redact(records []Record) []Record {
	out := records
	copied := false
	for i, rec := range records {
		red, ok := redactOne(rec)
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

// RedactedKeys are attribute keys dropped from every record.
var RedactedKeys = []string{
	"function_args",
	"gen_ai.tool.call.arguments",
	"gen_ai.tool.call.result",
	"gen_ai.input.messages",
	"gen_ai.output.messages",
	"gen_ai.system_instructions",
}

// toolErrorRecords are the tool-call and hook logs and spans whose error
// text is dropped, with the status message each one's text is replaced by.
// A qwen-code.hook span's error and status carry the hook command's output.
var toolErrorRecords = map[string]string{
	"qwen-code.tool_call":      toolErrorStatus,
	"qwen-code.tool":           toolErrorStatus,
	"qwen-code.tool.execution": toolErrorStatus,
	"qwen-code.hook":           hookErrorStatus,
	"gemini_cli.tool_call":     toolErrorStatus,
}

var toolErrorKeys = []string{"error", "error.message", "error_message"}

// toolErrorStatus replaces a tool span's status message; it is the text Qwen
// Code itself uses when a failed tool has no message.
const toolErrorStatus = "tool error"

// hookErrorStatus replaces a qwen-code.hook span's status message.
const hookErrorStatus = "hook error"

func redactOne(rec Record) (Record, bool) {
	var dropped []string
	var attrs map[string]any
	take := func(key, sizeKey string) {
		v, ok := rec.Attributes[key]
		if !ok {
			return
		}
		if attrs == nil {
			attrs = make(map[string]any, len(rec.Attributes)+2)
			for k, v := range rec.Attributes {
				attrs[k] = v
			}
		}
		delete(attrs, key)
		if s, ok := v.(string); ok && sizeKey != "" {
			if prev, ok := attrs[sizeKey].(int64); ok {
				attrs[sizeKey] = prev + int64(len(s))
			} else {
				attrs[sizeKey] = int64(len(s))
			}
		}
		dropped = append(dropped, key)
	}
	for _, key := range RedactedKeys {
		take(key, key+"_bytes")
	}
	status, toolRecord := toolErrorRecords[name(rec)]
	if toolRecord {
		for _, key := range toolErrorKeys {
			take(key, "error_bytes")
		}
	}
	statusRedacted := toolRecord && rec.StatusMessage != nil && *rec.StatusMessage != "" && *rec.StatusMessage != status
	if len(dropped) == 0 && !statusRedacted {
		return rec, false
	}
	if attrs == nil {
		attrs = make(map[string]any, len(rec.Attributes)+1)
		for k, v := range rec.Attributes {
			attrs[k] = v
		}
	}
	if statusRedacted {
		dropped = append(dropped, "status.message")
		msg := status
		rec.StatusMessage = &msg
	}
	sort.Strings(dropped)
	attrs["sdf.redacted"] = strings.Join(dropped, ",")
	rec.Attributes = attrs
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
