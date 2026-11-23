package summary

import (
	"strings"

	"github.com/paulpham157/sdf/internal/pricing"
	"github.com/paulpham157/sdf/internal/record"
)

// Qwen Code (QwenLM/qwen-code) forked Gemini CLI's telemetry and renamed it:
// qwen-code.* events, metrics, and spans, service.name qwen-code. Names and
// attributes here follow docs/developers/development/telemetry.md and
// packages/core/src/telemetry at v0.28.4 (e8424c88). Differences that matter
// for the summary:
//   - logs and spans carry session.id; metrics do not unless
//     telemetry.metrics.includeSessionId is true, so tokens prefer the
//     qwen-code.api_response logs over the qwen-code.token.usage metric;
//   - each prompt is one qwen-code.interaction trace root, with
//     qwen-code.llm_request, qwen-code.tool (children
//     qwen-code.tool.blocked_on_user and qwen-code.tool.execution),
//     qwen-code.hook, and qwen-code.subagent spans;
//   - there is no tool_token_count and no "tool" token type.
//
// Once Gemini CLI support lands, the Gemini-family log handling could be
// shared; it is kept separate so neither branch depends on the other.
const (
	qwenService      = "qwen-code"
	qwenPrefix       = "qwen-code."
	qwenInteraction  = "qwen-code.interaction"
	qwenLLMRequest   = "qwen-code.llm_request"
	qwenToolSpan     = "qwen-code.tool"
	qwenToolWait     = "qwen-code.tool.blocked_on_user"
	qwenToolExec     = "qwen-code.tool.execution"
	qwenAPIResponse  = "qwen-code.api_response"
	qwenToolCallLog  = "qwen-code.tool_call"
	qwenTokenMetric  = "qwen-code.token.usage"
	qwenToolMetric   = "qwen-code.tool.call.count"
	qwenSubagentSpan = "qwen-code.subagent"
)

// qwenLogTokens are the per-response counts on qwen-code.api_response.
// total_token_count is not added.
var qwenLogTokens = []string{"input_token_count", "output_token_count", "cached_content_token_count", "thoughts_token_count"}

// qwenSpanTokens are the standard usage fields on qwen-code.llm_request,
// plus the compatibility thoughts_token_count.
var qwenSpanTokens = []string{
	"gen_ai.usage.input_tokens",
	"gen_ai.usage.output_tokens",
	"gen_ai.usage.cache_read.input_tokens",
	"gen_ai.usage.cache_creation.input_tokens",
	"thoughts_token_count",
}

// isQwenRecord matches the default service.name or a qwen-code.* record or
// event name. OTEL_SERVICE_NAME can rename the service; the names remain.
func isQwenRecord(service, name, event string) bool {
	return service == qwenService || strings.HasPrefix(name, qwenPrefix) || strings.HasPrefix(event, qwenPrefix)
}

func qwenRecords(records []record.Record, signal, name string) []record.Record {
	var out []record.Record
	for _, rec := range records {
		if rec.Signal == signal && logEventName(rec) == name && ToolOf(rec) == "qwen-code" {
			out = append(out, rec)
		}
	}
	return out
}

// qwenSummary replaces the fields whose generic rules do not fit Qwen Code.
func qwenSummary(s SessionSummary, records []record.Record, points []point, prices *pricing.Catalog) SessionSummary {
	s.DurationMS, s.DurationSource = qwenDuration(records)
	s.Tokens, s.TokensSource, s.TokensMethod = qwenTokens(records, points)
	s.Models = qwenModels(records, points)
	s.ToolCalls = nonNilCalls(qwenToolCalls(records, points))
	s.Missing = nonNilMissing(qwenMissing(records, points))
	computed := pricing.Price(prices, pricing.Usage{Models: s.Models, Tokens: tokenMap(s.Tokens), Source: deref(s.TokensSource)})
	s.ComputedCost = &computed
	return s
}

// qwenDuration sums qwen-code.interaction spans that have no interaction
// ancestor (one per prompt; an ACP or daemon parent may wrap them). The
// span covers model calls, tool approval waits, and tool runs. Without
// spans it falls back to the min-to-max timestamp of Qwen logs; metrics
// are left out because cumulative points start at process start.
func qwenDuration(records []record.Record) (*float64, *string) {
	byID := map[string]record.Record{}
	for _, rec := range records {
		if rec.Signal == "traces" && rec.SpanID != nil {
			byID[*rec.SpanID] = rec
		}
	}
	var total []float64
	for _, rec := range qwenRecords(records, "traces", qwenInteraction) {
		if qwenNested(rec, byID) {
			continue
		}
		if ms := spanMS(rec); ms != nil {
			total = append(total, *ms)
		} else if ms := record.Number(rec.Attributes["interaction.duration_ms"]); ms != nil {
			total = append(total, *ms)
		}
	}
	if len(total) > 0 {
		return record.Float(sum(total)), record.Str("traces " + qwenInteraction)
	}
	var lo, hi int64
	seen := 0
	for _, rec := range records {
		if rec.Signal != "logs" || ToolOf(rec) != "qwen-code" {
			continue
		}
		for _, stamp := range stamps(rec) {
			if seen == 0 || stamp < lo {
				lo = stamp
			}
			if seen == 0 || stamp > hi {
				hi = stamp
			}
			seen++
		}
	}
	if seen < 2 {
		return nil, nil
	}
	return record.Float(float64(hi-lo) / 1_000_000), record.Str("logs qwen-code window")
}

func qwenNested(rec record.Record, byID map[string]record.Record) bool {
	seen := map[string]bool{}
	parent := rec.ParentSpanID
	for parent != nil && *parent != "" && !seen[*parent] {
		seen[*parent] = true
		up, ok := byID[*parent]
		if !ok {
			return false
		}
		if deref(up.Name) == qwenInteraction {
			return true
		}
		parent = up.ParentSpanID
	}
	return false
}

// qwenTokens prefers qwen-code.api_response logs (they carry session.id),
// then qwen-code.llm_request span usage, then the qwen-code.token.usage
// metric, which has no session id by default and reaches a session only
// by time window. Every source includes background calls such as the
// memory extractor and subagents.
func qwenTokens(records []record.Record, points []point) (OrderedFloats, *string, *string) {
	totals := OrderedFloats{}
	found := false
	for _, rec := range qwenRecords(records, "logs", qwenAPIResponse) {
		if addFields(&totals, rec, qwenLogTokens) {
			found = true
		}
	}
	if found {
		return totals, record.Str("logs " + qwenAPIResponse), record.Str("sum of input_token_count, output_token_count, cached_content_token_count, and thoughts_token_count on qwen-code.api_response; total_token_count is not added. Input includes cached tokens. With OpenAI-compatible providers output includes thoughts (which may be estimated from text); with Gemini-native responses it does not")
	}
	for _, rec := range qwenRecords(records, "traces", qwenLLMRequest) {
		if addFields(&totals, rec, qwenSpanTokens) {
			found = true
		}
	}
	if found {
		return totals, record.Str("traces " + qwenLLMRequest), record.Str("sum of gen_ai.usage.* and thoughts_token_count on qwen-code.llm_request spans; a request whose provider reported only a total has no usage fields")
	}
	var metric []point
	for _, p := range points {
		if p.name == qwenTokenMetric {
			metric = append(metric, p)
		}
	}
	if len(metric) > 0 {
		return sumBy(metric, "type"), record.Str("metrics " + qwenTokenMetric), record.Str("sum of delta points; a cumulative series keeps its latest point. type=input|output|thought|cache; attributed by time window unless telemetry.metrics.includeSessionId is true")
	}
	return OrderedFloats{}, nil, nil
}

func qwenModels(records []record.Record, points []point) []string {
	var found []string
	for _, rec := range records {
		if ToolOf(rec) != "qwen-code" {
			continue
		}
		switch {
		case rec.Signal == "logs" && strings.HasPrefix(logEventName(rec), "qwen-code.api_"):
			addModel(&found, rec.Attributes)
		case rec.Signal == "traces" && deref(rec.Name) == qwenLLMRequest:
			addModel(&found, rec.Attributes)
			if text := record.SummaryText(rec.Attributes["gen_ai.response.model"]); text != nil {
				addModel(&found, map[string]any{"model": *text})
			}
		}
	}
	for _, p := range points {
		if strings.HasPrefix(p.name, qwenPrefix) {
			addModel(&found, p.attributes)
		}
	}
	return nonNilStr(found)
}

// qwenToolCalls reads qwen-code.tool spans, with wait and execution from
// their qwen-code.tool.blocked_on_user and qwen-code.tool.execution
// children, joined to the qwen-code.tool_call log by call id. A log with no
// span is reported from the log. Without either, qwen-code.tool.call.count
// gives counts.
func qwenToolCalls(records []record.Record, points []point) []ToolCall {
	var spans []record.Record
	for _, rec := range records {
		if rec.Signal == "traces" {
			spans = append(spans, rec)
		}
	}
	logs := qwenRecords(records, "logs", qwenToolCallLog)
	logByID := map[string]record.Record{}
	for _, rec := range logs {
		if id := callID(rec); id != nil {
			logByID[*id] = rec
		}
	}
	seen := map[string]bool{}
	var calls []ToolCall
	for _, rec := range qwenRecords(records, "traces", qwenToolSpan) {
		id := firstText(rec.Attributes, "gen_ai.tool.call.id", "tool.call_id")
		var log *record.Record
		if id != nil {
			seen[*id] = true
			if found, ok := logByID[*id]; ok {
				log = &found
			}
		}
		waitMS, waitSource := qwenChild(rec, spans, qwenToolWait)
		execMS, execSource := qwenChild(rec, spans, qwenToolExec)
		duration := spanMS(rec)
		if execMS != nil {
			duration = execMS
		}
		success, source := qwenSpanSuccess(rec)
		if log != nil {
			source += "; logs " + qwenToolCallLog
			if text := record.SummaryText(log.Attributes["success"]); text != nil {
				success = text
			}
		}
		calls = append(calls, ToolCall{
			Name:            firstText(rec.Attributes, "gen_ai.tool.name", "tool.name"),
			CallID:          id,
			Success:         success,
			DurationMS:      duration,
			Source:          "traces " + qwenToolSpan + source,
			WaitMS:          waitMS,
			WaitSource:      waitSource,
			ExecutionMS:     execMS,
			ExecutionSource: execSource,
		})
	}
	for _, rec := range logs {
		id := callID(rec)
		if id != nil && seen[*id] {
			continue
		}
		calls = append(calls, ToolCall{
			Name:       firstText(rec.Attributes, "function_name", "tool_name"),
			CallID:     id,
			Success:    record.SummaryText(rec.Attributes["success"]),
			DurationMS: record.Number(rec.Attributes["duration_ms"]),
			Source:     "logs " + qwenToolCallLog,
		})
	}
	if len(calls) > 0 {
		return calls
	}
	for _, p := range points {
		if p.name != qwenToolMetric {
			continue
		}
		v := p.value
		calls = append(calls, ToolCall{
			Name:    record.SummaryText(p.attributes["function_name"]),
			Success: record.SummaryText(p.attributes["success"]),
			Count:   &v,
			Source:  "metrics " + qwenToolMetric,
		})
	}
	return calls
}

// qwenChild returns the duration of a direct child span with this name.
func qwenChild(rec record.Record, spans []record.Record, name string) (*float64, *string) {
	if rec.SpanID == nil {
		return nil, nil
	}
	for _, span := range spans {
		if deref(span.Name) != name || span.ParentSpanID == nil || *span.ParentSpanID != *rec.SpanID {
			continue
		}
		if elapsed := spanMS(span); elapsed != nil {
			return elapsed, record.Str("traces " + name)
		}
	}
	return nil, nil
}

// qwenSpanSuccess reads success on a qwen-code.tool span; error.type or an
// ERROR status is a failure when the flag is missing.
func qwenSpanSuccess(rec record.Record) (*string, string) {
	if text := record.SummaryText(rec.Attributes["success"]); text != nil {
		return text, ""
	}
	if rec.StatusCode == "ERROR" || strAttr(rec.Attributes, "error.type") != "" {
		return record.Str("false"), " (status)"
	}
	return record.Str("unknown"), ""
}

func qwenMissing(records []record.Record, points []point) []Missing {
	var out []Missing
	if countSignal(records, "traces") == 0 {
		out = append(out, Missing{Signal: "traces", Reason: "no spans attributed to this session. Qwen Code exports spans when telemetry.enabled is true and an OTLP endpoint is set; telemetry.outfile turns OTLP export off."})
	}
	if countSignal(records, "logs") == 0 {
		out = append(out, Missing{Signal: "logs", Reason: "no qwen-code.* log events attributed to this session. They carry session.id and are exported with the same settings as spans; with otlpProtocol=http and only otlpTracesEndpoint set, logs become bridge spans instead."})
	}
	if countSignal(records, "metrics") == 0 {
		out = append(out, Missing{Signal: "metrics", Reason: "no metrics attributed to this session. Qwen Code metrics carry no session.id unless telemetry.metrics.includeSessionId is true, so a point is attached only when it falls inside exactly one Qwen Code session."})
	}
	out = append(out, Missing{Signal: "agent cost", Reason: "Qwen Code exports no cost metric or cost attribute. Agent-reported cost stays unknown; computed cost is shown only when a pricing rule covers the token source."})
	subagents := len(qwenRecords(records, "traces", qwenSubagentSpan))
	if subagents > 0 {
		out = append(out, Missing{Signal: "tokens by agent", Reason: "token totals include " + itoa(subagents) + " subagent run(s) and any background memory-extractor calls; Qwen Code telemetry does not split them out."})
	}
	return out
}
