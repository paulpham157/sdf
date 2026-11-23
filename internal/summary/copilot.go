package summary

import (
	"sort"
	"strings"

	"github.com/paulpham157/sdf/internal/genai"
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
	copilotUsagePrefix  = genai.UsagePrefix
	copilotNanoAIU      = "github.copilot.nano_aiu"
	copilotMetricPrefix = genai.MetricInferenceUsagePrefix
)

// copilotUsageKeys maps each summary key to the gen_ai.usage.* suffixes
// (span) or gen_ai.client.inference.usage.* suffixes (metric) that carry it,
// current name first. Only the first one present on a span (or, for metrics,
// in the session) is read, so a build that sends two names is not counted
// twice. cache_creation is the name Copilot CLI 1.0.64 used; later builds and
// the docs use cache_write. Builds before that used underscore names
// (cache_read_input_tokens, cache_creation_input_tokens,
// reasoning_output_tokens; changelog 1.0.64).
var copilotUsageKeys = []struct {
	key      string
	suffixes []string
}{
	{genai.UsageLabelInputTokens, []string{genai.UsageLabelInputTokens}},
	{genai.UsageLabelOutputTokens, []string{genai.UsageLabelOutputTokens}},
	{genai.UsageLabelCacheReadInputTokens, []string{genai.UsageLabelCacheReadInputTokens, genai.UsageLabelCacheReadInputTokensUnderscore}},
	{genai.UsageLabelCacheWriteInputTokens, []string{genai.UsageLabelCacheWriteInputTokens, genai.UsageLabelCacheCreationInputTokens, genai.UsageLabelCacheCreationInputTokensUnderscore}},
	{genai.UsageLabelReasoningOutputTokens, []string{genai.UsageLabelReasoningOutputTokens, genai.UsageLabelReasoningOutputTokensUnderscore}},
}

const (
	copilotDefaultAgent = "github.copilot.default"
	copilotServerAddr   = "server.address"
)

// isCopilotRecord is true for the default service.name, a github.copilot.*
// record name, or a span whose gen_ai.agent.id is a Copilot agent (the
// service name can be changed with OTEL_SERVICE_NAME).
func isCopilotRecord(service, name string, rec record.Record) bool {
	if service == copilotService || strings.HasPrefix(name, "github.copilot.") {
		return true
	}
	return strings.HasPrefix(strAttr(rec.Attributes, genai.AttrAgentID), copilotAgentID)
}

// copilotSpans returns the session's Copilot spans whose name starts with
// head. A span stored twice (same trace id and span id, for example after an
// OTLP export retry or a re-sent batch) is returned once, the first copy, so
// chat and invoke_agent usage and nano_aiu are not added twice (issue #248),
// as OpenCode and Pi spans are counted once. A span without both ids is
// never merged.
func copilotSpans(records []record.Record, head string) []record.Record {
	var out []record.Record
	seen := map[string]bool{}
	for _, rec := range records {
		if rec.Signal != "traces" || spanHead(deref(rec.Name)) != head || ToolOf(rec) != "copilot-cli" {
			continue
		}
		if key, ok := spanKey(rec); ok {
			if seen[key] {
				continue
			}
			seen[key] = true
		}
		out = append(out, rec)
	}
	return out
}

// copilotSpanIndex maps trace id + span id to the span. Span ids are only
// unique within a trace, so the trace id is part of the key. A span stored
// twice keeps its first copy, the same copy copilotSpans returns, so the
// ancestry walk and the counted spans never disagree.
func copilotSpanIndex(records []record.Record) map[string]record.Record {
	byID := map[string]record.Record{}
	for _, rec := range records {
		if rec.Signal != "traces" || rec.SpanID == nil {
			continue
		}
		key := copilotSpanKey(rec.TraceID, *rec.SpanID)
		if _, seen := byID[key]; !seen {
			byID[key] = rec
		}
	}
	return byID
}

func copilotSpanKey(trace *string, span string) string {
	return deref(trace) + "/" + span
}

// copilotAncestor returns the nearest ancestor of rec whose span head is one
// of heads.
func copilotAncestor(rec record.Record, byID map[string]record.Record, heads ...string) (record.Record, bool) {
	seen := map[string]bool{}
	parent := rec.ParentSpanID
	for parent != nil && *parent != "" && !seen[*parent] {
		seen[*parent] = true
		up, ok := byID[copilotSpanKey(rec.TraceID, *parent)]
		if !ok {
			break
		}
		head := spanHead(deref(up.Name))
		for _, h := range heads {
			if head == h {
				return up, true
			}
		}
		parent = up.ParentSpanID
	}
	return record.Record{}, false
}

// copilotTopLevel is the set of invoke_agent spans whose usage and AIU are
// added up, so a subagent is never counted on top of its parent.
//
// Per the Copilot docs, a top-level invoke_agent carries server.address and
// server.port and a subagent does not, and the top-level default agent id is
// github.copilot.default. The first of those markers present on any span in
// the session decides, and a span without the marker is still top-level when
// it has no parent span at all: a session resumed across Copilot builds can
// mix spans with and without server.address, and a parentless invoke_agent
// cannot be a subagent. Without either marker (older builds) a span is
// top-level when no invoke_agent is above it. In fleet mode an invoke_workflow span
// runs subagents in parallel. When that workflow is not inside a top-level
// invoke_agent, its direct invoke_agent children carry the usage and are
// counted. When it is inside one, the parent's totals are assumed to include
// them, as for any subagent.
func copilotTopLevel(records []record.Record) []record.Record {
	byID := copilotSpanIndex(records)
	agents := copilotSpans(records, genai.OperationInvokeAgent)
	hasAddr, hasDefault := false, false
	for _, rec := range agents {
		if strAttr(rec.Attributes, copilotServerAddr) != "" {
			hasAddr = true
		}
		if strAttr(rec.Attributes, genai.AttrAgentID) == copilotDefaultAgent {
			hasDefault = true
		}
	}
	isTop := func(rec record.Record) bool {
		parentless := rec.ParentSpanID == nil || *rec.ParentSpanID == ""
		switch {
		case hasAddr:
			return strAttr(rec.Attributes, copilotServerAddr) != "" || parentless
		case hasDefault:
			return strAttr(rec.Attributes, genai.AttrAgentID) == copilotDefaultAgent || parentless
		default:
			_, nested := copilotAncestor(rec, byID, genai.OperationInvokeAgent)
			return !nested
		}
	}
	var out []record.Record
	for _, rec := range agents {
		if isTop(rec) {
			out = append(out, rec)
			continue
		}
		// A fleet subagent: its nearest agent-or-workflow ancestor is a
		// workflow that no top-level invoke_agent contains.
		up, ok := copilotAncestor(rec, byID, genai.OperationInvokeAgent, genai.OperationInvokeWorkflow)
		if !ok || spanHead(deref(up.Name)) != genai.OperationInvokeWorkflow {
			continue
		}
		if _, inside := copilotAncestor(up, byID, genai.OperationInvokeAgent); !inside {
			out = append(out, rec)
		}
	}
	return out
}

// copilotDuration is the union of the time covered by top-level invoke_agent
// spans and invoke_workflow (fleet mode) spans: agent time for each user
// message, not wall clock between messages. Overlapping spans (parallel
// runs, a workflow and its subagents) count once: 0-10 s and 5-15 s is
// 15 s, not 20 s.
func copilotDuration(records []record.Record) (*float64, *string) {
	type interval struct{ start, end int64 }
	var spans []interval
	add := func(rec record.Record) {
		if spanMS(rec) != nil {
			spans = append(spans, interval{*rec.StartUnixNano, *rec.EndUnixNano})
		}
	}
	for _, rec := range copilotTopLevel(records) {
		add(rec)
	}
	for _, rec := range copilotSpans(records, genai.OperationInvokeWorkflow) {
		add(rec)
	}
	if len(spans) == 0 {
		return nil, nil
	}
	sort.Slice(spans, func(i, j int) bool { return spans[i].start < spans[j].start })
	total := int64(0)
	cur := spans[0]
	for _, next := range spans[1:] {
		if next.start <= cur.end {
			if next.end > cur.end {
				cur.end = next.end
			}
			continue
		}
		total += cur.end - cur.start
		cur = next
	}
	total += cur.end - cur.start
	return record.Float(float64(total) / 1_000_000), record.Str("traces invoke_agent and invoke_workflow (top-level, union of intervals, github-copilot)")
}

func copilotUsage(totals *OrderedFloats, attrs map[string]any) bool {
	found := false
	for _, item := range copilotUsageKeys {
		for _, suffix := range item.suffixes {
			if amount := record.Number(attrs[copilotUsagePrefix+suffix]); amount != nil {
				totals.Add(item.key, *amount)
				found = true
				break
			}
		}
	}
	return found
}

// copilotTokens sums gen_ai.usage.* on chat spans (one per model request,
// subagent and compaction requests included; a span stored twice counts
// once, see copilotSpans). Without chat usage it sums
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
	for _, rec := range copilotSpans(records, genai.OperationChat) {
		if copilotUsage(&chat, rec.Attributes) {
			found = true
		}
	}
	if found {
		return chat, record.Str("traces " + genai.OperationChat + " " + copilotService), record.Str("sum of gen_ai.usage.* on chat spans; cache_read and cache_write are part of input_tokens, reasoning is part of output_tokens. invoke_agent totals repeat these counts and are not added; a span stored twice (same trace and span id) counts once"), true
	}
	top := OrderedFloats{}
	for _, rec := range copilotTopLevel(records) {
		if copilotUsage(&top, rec.Attributes) {
			found = true
		}
	}
	if found {
		return top, record.Str("traces " + genai.OperationInvokeAgent + " " + copilotService), record.Str("sum of gen_ai.usage.* on top-level invoke_agent spans (all turns of one user message); subagent invoke_agent spans are not added; a span stored twice (same trace and span id) counts once"), true
	}
	metrics := OrderedFloats{}
	for _, item := range copilotUsageKeys {
		for _, suffix := range item.suffixes {
			hit := false
			for _, p := range points {
				if p.name == copilotMetricPrefix+suffix {
					metrics.Add(item.key, p.value)
					hit = true
				}
			}
			if hit {
				found = true
				break
			}
		}
	}
	if found {
		return metrics, record.Str("metrics gen_ai.client.inference.usage github-copilot"), record.Str("sum of gen_ai.client.inference.usage.* counter points attributed to this session by time window; a cumulative series adds up the latest point of each process run"), true
	}
	return OrderedFloats{}, nil, nil, false
}

// copilotToolSuccess reads an execute_tool span. Copilot sets error.type
// (and an ERROR status) only on failure and has no success attribute, so a
// tool span without either is a success.
func copilotToolSuccess(rec record.Record) (*string, *string, bool) {
	if rec.Signal != "traces" || spanHead(deref(rec.Name)) != genai.OperationExecuteTool || ToolOf(rec) != "copilot-cli" {
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
	reason := "Copilot CLI reports no USD cost. Usage is billed in premium requests and AI units: github.copilot.cost is a per-request premium-request multiplier, not money, github.copilot.nano_aiu counts AI units, and the session's total_premium_requests is a span event sdf does not store. sdf does not price premium requests."
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
