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
// function_args is dropped from every record, whatever the agent. The error
// text is dropped only from the tool-call records named in toolErrorRecords,
// so other agents' API error messages stay readable.
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
var RedactedKeys = []string{"function_args"}

// toolErrorRecords are the tool-call logs and spans whose error text is
// dropped.
var toolErrorRecords = map[string]bool{
	"qwen-code.tool_call":      true,
	"qwen-code.tool":           true,
	"qwen-code.tool.execution": true,
	"gemini_cli.tool_call":     true,
}

var toolErrorKeys = []string{"error", "error.message", "error_message"}

// toolErrorStatus replaces a tool span's status message; it is the text Qwen
// Code itself uses when a failed tool has no message.
const toolErrorStatus = "tool error"

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
	toolRecord := toolErrorRecords[name(rec)]
	if toolRecord {
		for _, key := range toolErrorKeys {
			take(key, "error_bytes")
		}
	}
	statusRedacted := toolRecord && rec.StatusMessage != nil && *rec.StatusMessage != "" && *rec.StatusMessage != toolErrorStatus
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
		msg := toolErrorStatus
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
