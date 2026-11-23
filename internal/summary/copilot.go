package summary

import (
	"strings"

	"github.com/paulpham157/sdf/internal/record"
)

// GitHub Copilot CLI (shared Copilot runtime) exports traces and metrics, no
// log signal. Names follow the OTel GenAI conventions plus github.copilot.*:
// https://docs.github.com/en/copilot/reference/enterprise-administrators/opentelemetry-reference
// and `copilot help monitoring` (Copilot CLI 1.0.114). Each user message is an
// invoke_agent span with chat <model> and execute_tool <tool> children;
// subagents are nested invoke_agent spans in the same trace.
const (
	copilotService      = "github-copilot"
	copilotAgentID      = "github.copilot."
	copilotUsagePrefix  = "gen_ai.usage."
	copilotNanoAIU      = "github.copilot.nano_aiu"
	copilotMetricPrefix = "gen_ai.client.inference.usage."
)

// copilotUsageKeys maps a gen_ai.usage.* suffix (span) or a
// gen_ai.client.inference.usage.* suffix (metric) to the summary key.
// cache_creation is the name Copilot CLI 1.0.64 used; later builds and the
// docs use cache_write.
var copilotUsageKeys = []struct{ suffix, key string }{
	{"input_tokens", "input_tokens"},
	{"output_tokens", "output_tokens"},
	{"cache_read.input_tokens", "cache_read.input_tokens"},
	{"cache_write.input_tokens", "cache_write.input_tokens"},
	{"cache_creation.input_tokens", "cache_write.input_tokens"},
	{"reasoning.output_tokens", "reasoning.output_tokens"},
}

// isCopilotRecord is true for the default service.name, a github.copilot.*
// record name, or a span whose gen_ai.agent.id is a Copilot agent (the
// service name can be changed with OTEL_SERVICE_NAME).
func isCopilotRecord(service, name string, rec record.Record) bool {
	if service == copilotService || strings.HasPrefix(name, "github.copilot.") {
		return true
	}
	return strings.HasPrefix(strAttr(rec.Attributes, "gen_ai.agent.id"), copilotAgentID)
}

func copilotSpans(records []record.Record, head string) []record.Record {
	var out []record.Record
	for _, rec := range records {
		if rec.Signal == "traces" && spanHead(deref(rec.Name)) == head && ToolOf(rec) == "copilot-cli" {
			out = append(out, rec)
		}
	}
	return out
}

// copilotTopLevel keeps the invoke_agent spans with no invoke_agent
// ancestor. A subagent's invoke_agent sits inside the parent's span, so
// summing every invoke_agent would count that time and usage twice.
func copilotTopLevel(records []record.Record) []record.Record {
	byID := map[string]record.Record{}
	for _, rec := range records {
		if rec.Signal == "traces" && rec.SpanID != nil {
			byID[*rec.SpanID] = rec
		}
	}
	var out []record.Record
	for _, rec := range copilotSpans(records, "invoke_agent") {
		nested := false
		seen := map[string]bool{}
		parent := rec.ParentSpanID
		for parent != nil && *parent != "" && !seen[*parent] {
			seen[*parent] = true
			up, ok := byID[*parent]
			if !ok {
				break
			}
			if spanHead(deref(up.Name)) == "invoke_agent" {
				nested = true
				break
			}
			parent = up.ParentSpanID
		}
		if !nested {
			out = append(out, rec)
		}
	}
	return out
}

// copilotDuration sums top-level invoke_agent spans: agent time for each
// user message, not wall clock between messages.
func copilotDuration(records []record.Record) (*float64, *string) {
	var total []float64
	for _, rec := range copilotTopLevel(records) {
		if ms := spanMS(rec); ms != nil {
			total = append(total, *ms)
		}
	}
	if len(total) == 0 {
		return nil, nil
	}
	return record.Float(sum(total)), record.Str("traces invoke_agent (top-level, github-copilot)")
}

func copilotUsage(totals *OrderedFloats, attrs map[string]any) bool {
	found := false
	for _, item := range copilotUsageKeys {
		if amount := record.Number(attrs[copilotUsagePrefix+item.suffix]); amount != nil {
			totals.Add(item.key, *amount)
			found = true
		}
	}
	return found
}

// copilotTokens sums gen_ai.usage.* on chat spans (one per model request,
// subagent and compaction requests included). Without chat usage it sums
// the top-level invoke_agent totals, then the
// gen_ai.client.inference.usage.* counters. Those counters have no session
// id and reach a session only by its time window. Input includes cache read
// and cache write; output includes reasoning.
func copilotTokens(records []record.Record, points []point) (OrderedFloats, *string, *string, bool) {
	copilot := false
	for _, rec := range records {
		if ToolOf(rec) == "copilot-cli" {
			copilot = true
			break
		}
	}
	if !copilot {
		return OrderedFloats{}, nil, nil, false
	}
	chat := OrderedFloats{}
	found := false
	for _, rec := range copilotSpans(records, "chat") {
		if copilotUsage(&chat, rec.Attributes) {
			found = true
		}
	}
	if found {
		return chat, record.Str("traces chat github-copilot"), record.Str("sum of gen_ai.usage.* on chat spans; cache_read and cache_write are part of input_tokens, reasoning is part of output_tokens. invoke_agent totals repeat these counts and are not added"), true
	}
	top := OrderedFloats{}
	for _, rec := range copilotTopLevel(records) {
		if copilotUsage(&top, rec.Attributes) {
			found = true
		}
	}
	if found {
		return top, record.Str("traces invoke_agent github-copilot"), record.Str("sum of gen_ai.usage.* on top-level invoke_agent spans (all turns of one user message); subagent invoke_agent spans are not added"), true
	}
	metrics := OrderedFloats{}
	for _, item := range copilotUsageKeys {
		for _, p := range points {
			if p.name == copilotMetricPrefix+item.suffix {
				metrics.Add(item.key, p.value)
				found = true
			}
		}
	}
	if found {
		return metrics, record.Str("metrics gen_ai.client.inference.usage github-copilot"), record.Str("sum of gen_ai.client.inference.usage.* counter points attributed to this session by time window; a cumulative series keeps its latest point"), true
	}
	return OrderedFloats{}, nil, nil, false
}

// copilotToolSuccess reads an execute_tool span. Copilot sets error.type
// (and an ERROR status) only on failure and has no success attribute, so a
// tool span without either is a success.
func copilotToolSuccess(rec record.Record) (*string, *string, bool) {
	if rec.Signal != "traces" || spanHead(deref(rec.Name)) != "execute_tool" || ToolOf(rec) != "copilot-cli" {
		return nil, nil, false
	}
	if rec.StatusCode == "ERROR" || strAttr(rec.Attributes, "error.type") != "" {
		return record.Str("false"), record.Str("traces error.type"), true
	}
	return record.Str("true"), record.Str("traces (no error.type)"), true
}

func copilotMissing(records []record.Record, points []point) []Missing {
	var out []Missing
	if countSignal(records, "traces") == 0 {
		out = append(out, Missing{Signal: "traces", Reason: "no spans attributed to this session. Copilot CLI exports traces when OTEL_EXPORTER_OTLP_ENDPOINT (or COPILOT_OTEL_ENABLED=true) is set; spans carry gen_ai.conversation.id. An http:// endpoint is dropped when any OTEL_EXPORTER_OTLP_*CERTIFICATE or *CLIENT_KEY variable is set."})
	}
	if countSignal(records, "logs") == 0 {
		out = append(out, Missing{Signal: "logs", Reason: "Copilot CLI exports no OTLP log signal. Its lifecycle events (github.copilot.session.shutdown with total_premium_requests, gen_ai.client.operation.exception, hooks) are span events, which sdf does not store."})
	}
	if countSignal(records, "metrics") == 0 {
		out = append(out, Missing{Signal: "metrics", Reason: "no metrics attributed to this session. Copilot CLI metrics carry no session id, so a point is attached only when its timestamp falls inside exactly one Copilot session."})
	}
	reason := "Copilot CLI reports no USD cost. Usage is billed in premium requests and AI units: github.copilot.cost is a per-request multiplier, not money, and github.copilot.nano_aiu counts AI units."
	var nano []float64
	for _, rec := range copilotTopLevel(records) {
		if amount := record.Number(rec.Attributes[copilotNanoAIU]); amount != nil {
			nano = append(nano, *amount)
		}
	}
	if len(nano) > 0 {
		reason += " Top-level invoke_agent spans report " + strconvFormat(sum(nano)/1_000_000_000) + " AIU (" + copilotNanoAIU + "; chat spans repeat it and are not added)."
	}
	out = append(out, Missing{Signal: "agent cost", Reason: reason})
	return out
}
