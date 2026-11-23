package summary

import (
	"fmt"
	"sort"
	"strings"

	"github.com/paulpham157/sdf/internal/genai"
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
	genai.AttrUsageInputTokens,
	genai.AttrUsageOutputTokens,
	genai.AttrUsageCacheReadInputTokens,
	genai.AttrUsageCacheCreationInputTokens,
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

// qwenUnique drops records stored twice, for example after an OTLP export
// retry: a log by exactCopyKey (an exact copy only; a log without
// event.timestamp and log time is never merged) and a span by trace id +
// span id (spanKey; a span without both ids is never merged). The first copy
// is kept.
func qwenUnique(records []record.Record) []record.Record {
	seen := map[string]bool{}
	out := records[:0:0]
	for _, rec := range records {
		key, ok := "", false
		switch rec.Signal {
		case "logs":
			key = exactCopyKey(rec)
			ok = key != ""
		case "traces":
			key, ok = spanKey(rec)
		}
		if ok {
			if seen[rec.Signal+"|"+key] {
				continue
			}
			seen[rec.Signal+"|"+key] = true
		}
		out = append(out, rec)
	}
	return out
}

// qwenSummary replaces the fields whose generic rules do not fit Qwen Code.
func qwenSummary(s SessionSummary, records []record.Record, points []point, prices *pricing.Catalog, idx *qwenIndex) SessionSummary {
	s.DurationMS, s.DurationSource = qwenDuration(records)
	s.Tokens, s.TokensSource, s.TokensMethod = qwenTokens(records, points, s.SessionID, idx)
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
func qwenTokens(records []record.Record, points []point, sessionID string, idx *qwenIndex) (OrderedFloats, *string, *string) {
	totals := OrderedFloats{}
	found := false
	for _, rec := range qwenUnique(qwenRecords(records, "logs", qwenAPIResponse)) {
		if addFields(&totals, rec, qwenLogTokens) {
			found = true
		}
	}
	if found {
		return totals, record.Str("logs " + qwenAPIResponse), record.Str("sum of input_token_count, output_token_count, cached_content_token_count, and thoughts_token_count on qwen-code.api_response; total_token_count is not added; a log stored twice (same session, log time, body, and every attribute) counts once. Input includes cached tokens. With OpenAI-compatible providers output includes thoughts (which may be estimated from text); with Gemini-native responses it does not")
	}
	for _, rec := range qwenUnique(qwenRecords(records, "traces", qwenLLMRequest)) {
		if addFields(&totals, rec, qwenSpanTokens) {
			found = true
		}
	}
	if found {
		return totals, record.Str("traces " + qwenLLMRequest), record.Str("sum of gen_ai.usage.* and thoughts_token_count on qwen-code.llm_request spans (a span stored twice counts once); a request whose provider reported only a total has no usage fields")
	}
	if metric, ok := qwenMetricTokens(records, sessionID, idx); ok {
		return metric, record.Str("metrics " + qwenTokenMetric), record.Str(qwenMetricMethod)
	}
	return OrderedFloats{}, nil, nil
}

const qwenMetricMethod = "sum of delta points; a cumulative series adds up the latest point of each process run (start time), minus the run's latest point before this session when the points carry no session.id (the process counted an earlier session first). type=input|output|thought|cache; attributed by time window (a point after the last session window of its process run goes to the latest session that started in that run) unless telemetry.metrics.includeSessionId is true"

// qwenStamps places a Qwen Code metric point by its own time, not its
// start time. A cumulative point's start time is when the process began
// counting, so in a process that ran several sessions (/clear, /resume)
// every point would land in the first one.
func qwenStamps(tool string, rec record.Record, st []int64) []int64 {
	if tool == "qwen-code" && rec.Signal == "metrics" && rec.TimeUnixNano != nil {
		return []int64{*rec.TimeUnixNano}
	}
	return st
}

// qwenStartupSlack is how long before a session's first log or span its
// process may have started and still count as started for that session.
// Qwen Code logs session.start while it loads its config.
const qwenStartupSlack = int64(10_000_000_000)

// qwenRunPoint is one cumulative qwen-code.token.usage point of a process run.
type qwenRunPoint struct {
	at, id int64
	value  float64
}

// qwenIndex is what the Qwen token fallback needs from outside one session:
// every cumulative token point of each process run, wherever it was
// attributed, and when each Qwen session started.
type qwenIndex struct {
	runs   map[string][]qwenRunPoint
	starts map[string]int64
}

// qwenRunKey identifies a process run of one cumulative series: its
// attributes, resource, and start time.
func qwenRunKey(rec record.Record) string {
	start := int64(0)
	if rec.StartUnixNano != nil {
		start = *rec.StartUnixNano
	}
	return fmt.Sprintf("%s|%s|%d", freeze(rec.Attributes), freeze(rec.Resource), start)
}

func qwenSharedToken(rec record.Record) bool {
	if rec.Signal != "metrics" || rec.Value == nil || deref(rec.Name) != qwenTokenMetric || deref(rec.MetricTemporality) == "delta" {
		return false
	}
	_, perSession := rec.Attributes["session.id"]
	return !perSession
}

func qwenFirstTime(rec record.Record) *int64 {
	if rec.Signal == "traces" {
		return rec.StartUnixNano
	}
	return rec.TimeUnixNano
}

func buildQwenIndex(records []record.Record) *qwenIndex {
	idx := &qwenIndex{runs: map[string][]qwenRunPoint{}, starts: map[string]int64{}}
	for _, rec := range records {
		if ToolOf(rec) != "qwen-code" {
			continue
		}
		if rec.Signal != "metrics" {
			id := deref(rec.SessionID)
			when := qwenFirstTime(rec)
			if id == "" || when == nil || *when <= 0 {
				continue
			}
			if prev, ok := idx.starts[id]; !ok || *when < prev {
				idx.starts[id] = *when
			}
			continue
		}
		if !qwenSharedToken(rec) {
			continue
		}
		at := int64(0)
		if rec.TimeUnixNano != nil {
			at = *rec.TimeUnixNano
		}
		key := qwenRunKey(rec)
		idx.runs[key] = append(idx.runs[key], qwenRunPoint{at: at, id: rec.ID, value: *rec.Value})
	}
	for _, run := range idx.runs {
		sort.SliceStable(run, func(i, j int) bool {
			if run[i].at != run[j].at {
				return run[i].at < run[j].at
			}
			return run[i].id < run[j].id
		})
	}
	return idx
}

// before is the run's latest point earlier than (at, id), from any session
// or none.
func (idx *qwenIndex) before(key string, at, id int64) (qwenRunPoint, bool) {
	if idx == nil {
		return qwenRunPoint{}, false
	}
	var out qwenRunPoint
	found := false
	for _, p := range idx.runs[key] {
		if p.at > at || (p.at == at && p.id >= id) {
			break
		}
		out, found = p, true
	}
	return out, found
}

// earlierSession reports whether another Qwen session started before
// sessionStart and no more than qwenStartupSlack before the run began
// (any time, for a run with no start time).
func (idx *qwenIndex) earlierSession(self string, runStart, sessionStart int64) bool {
	if idx == nil {
		return false
	}
	for id, at := range idx.starts {
		if id == self || at >= sessionStart {
			continue
		}
		if runStart <= 0 || at >= runStart-qwenStartupSlack {
			return true
		}
	}
	return false
}

// qwenSegments lists, per Qwen Code session, the sorted timestamps of its
// logs and spans, so a late metric point can find the part of a session
// that ran in the point's process (a resumed session runs in several).
func qwenSegments(scoped map[sessionKey][]record.Record) map[sessionKey][]int64 {
	out := map[sessionKey][]int64{}
	for key, grouped := range scoped {
		if key.tool != "qwen-code" {
			continue
		}
		var st []int64
		for _, rec := range grouped {
			if rec.Signal != "metrics" {
				st = append(st, stamps(rec)...)
			}
		}
		if len(st) == 0 {
			continue
		}
		sort.Slice(st, func(i, j int) bool { return st[i] < st[j] })
		out[key] = st
	}
	return out
}

// qwenLateFlush places a cumulative Qwen Code metric point that no session
// window holds, such as the flush at process exit after the last log. It
// goes to the session whose part in the point's process run started latest
// at or before the point. A session's part in a run starts at its first log
// or span no more than qwenStartupSlack before the run's start time, so a
// session resumed in a new process (qwen --resume) is placed by when it
// resumed, not when it first started. A tie is left unattributed.
func qwenLateFlush(tool string, rec record.Record, stamp int64, segments map[sessionKey][]int64) (sessionKey, bool) {
	if tool != "qwen-code" || rec.Signal != "metrics" || deref(rec.MetricTemporality) != "cumulative" || rec.StartUnixNano == nil || *rec.StartUnixNano <= 0 {
		return sessionKey{}, false
	}
	start := *rec.StartUnixNano
	var best sessionKey
	var bestLo int64
	found, tie := false, false
	for key, st := range segments {
		i := sort.Search(len(st), func(i int) bool { return st[i] >= start-qwenStartupSlack })
		if key.tool != tool || i == len(st) || st[i] > stamp {
			continue
		}
		lo := st[i]
		switch {
		case !found || lo > bestLo:
			best, bestLo, found, tie = key, lo, true, false
		case lo == bestLo:
			tie = true
		}
	}
	if !found || tie {
		return sessionKey{}, false
	}
	return best, true
}

// qwenMetricTokens totals qwen-code.token.usage for one session.
//
// The counters belong to the process, not the session: after /clear or
// /resume (startNewSession) the same process keeps counting, and without
// telemetry.metrics.includeSessionId the points carry no session.id, so the
// next session's points still hold the earlier session's tokens. Each
// cumulative series (attributes, resource) is split into runs by
// StartTimeUnixNano, and a run's share is its latest point in this session
// (by time, then storage order) minus a baseline:
//
//   - 0 when the points carry session.id (the series is this session's);
//   - 0 when the session has no log or span;
//   - the run's latest point before the session's first point in it, from
//     any session or none, because the process was already counting;
//   - with no such point, 0 when no other Qwen session started in the run
//     (the process began with this session, however slow its startup);
//   - otherwise unknown: an earlier session of the run used tokens that
//     no point separates from this one's, so the fallback reports nothing
//     rather than a guess.
//
// Runs are added up, so a session resumed in a new process counts both
// processes. Delta points are summed.
func qwenMetricTokens(records []record.Record, sessionID string, idx *qwenIndex) (OrderedFloats, bool) {
	sessionStart := int64(0)
	for _, rec := range records {
		if rec.Signal == "metrics" || ToolOf(rec) != "qwen-code" {
			continue
		}
		when := qwenFirstTime(rec)
		if when != nil && *when > 0 && (sessionStart == 0 || *when < sessionStart) {
			sessionStart = *when
		}
	}
	type run struct {
		label           string
		perSession      bool
		key             string
		start           int64
		first, last     record.Record
		firstT, lastT   int64
		firstID, lastID int64
	}
	var keys []string
	runs := map[string]*run{}
	totals := OrderedFloats{}
	found := false
	for _, rec := range records {
		if rec.Signal != "metrics" || rec.Value == nil || deref(rec.Name) != qwenTokenMetric {
			continue
		}
		found = true
		label := "value"
		if text := record.SummaryText(rec.Attributes["type"]); text != nil {
			label = *text
		}
		if deref(rec.MetricTemporality) == "delta" {
			totals.Add(label, *rec.Value)
			continue
		}
		start := int64(0)
		if rec.StartUnixNano != nil {
			start = *rec.StartUnixNano
		}
		when := int64(0)
		if rec.TimeUnixNano != nil {
			when = *rec.TimeUnixNano
		}
		key := qwenRunKey(rec)
		r, ok := runs[key]
		if !ok {
			r = &run{label: label, perSession: !qwenSharedToken(rec), key: key, start: start, first: rec, last: rec, firstT: when, lastT: when, firstID: rec.ID, lastID: rec.ID}
			runs[key] = r
			keys = append(keys, key)
			totals.Add(label, 0)
			continue
		}
		if when < r.firstT || (when == r.firstT && rec.ID < r.firstID) {
			r.first, r.firstT, r.firstID = rec, when, rec.ID
		}
		if when > r.lastT || (when == r.lastT && rec.ID > r.lastID) {
			r.last, r.lastT, r.lastID = rec, when, rec.ID
		}
	}
	for _, key := range keys {
		r := runs[key]
		share := *r.last.Value
		if !r.perSession && sessionStart > 0 {
			if prev, ok := idx.before(r.key, r.firstT, r.firstID); ok {
				share -= prev.value
			} else if idx.earlierSession(sessionID, r.start, sessionStart) {
				return OrderedFloats{}, false
			}
		}
		if share < 0 {
			share = 0
		}
		totals.Add(r.label, share)
	}
	return totals, found
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
			if text := record.SummaryText(rec.Attributes[genai.AttrResponseModel]); text != nil {
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
	logs := qwenUnique(qwenRecords(records, "logs", qwenToolCallLog))
	logByID := map[string]record.Record{}
	for _, rec := range logs {
		if id := callID(rec); id != nil {
			logByID[*id] = rec
		}
	}
	seen := map[string]bool{}
	var calls []ToolCall
	for _, rec := range qwenUnique(qwenRecords(records, "traces", qwenToolSpan)) {
		id := firstText(rec.Attributes, genai.AttrToolCallID, "tool.call_id")
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
			Name:            firstText(rec.Attributes, genai.AttrToolName, "tool.name"),
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
