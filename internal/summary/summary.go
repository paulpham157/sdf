package summary

import (
	"math"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/paulpham157/sdf/internal/pricing"
	"github.com/paulpham157/sdf/internal/record"
)

const timeSlackNS int64 = 2_000_000_000

const (
	claudeTokenMetric   = "claude_code.token.usage"
	codexTokenMetric    = "codex.turn.token_usage"
	claudeCostMetric    = "claude_code.cost.usage"
	codexCostMetric     = "codex.turn.cost_microusd"
	codexDurationMetric = "codex.turn.e2e_duration_ms"
	claudeActiveMetric  = "claude_code.active_time.total"
	claudeAPI           = "claude_code.api_request"
	codexSSE            = "codex.sse_event"
	codexTurnCost       = "codex.turn_cost"
	piTokenMetric       = "gen_ai.client.token.usage"
	grokTokenMetric     = "grok_code.token.usage"
	grokCostMetric      = "grok_code.cost.usage"
	grokAPI             = "grok_code.api_request"
	grokSessionEnd      = "grok_code.session_end"
	grokTurnCompleted   = "grok_code.turn_completed"
	grokToolResult      = "grok_code.tool_result"
	geminiTokenMetric   = "gemini_cli.token.usage"
	geminiAPIResponse   = "gemini_cli.api_response"
	geminiToolCall      = "gemini_cli.tool_call"
)

var (
	toolLogs = map[string]bool{
		"claude_code.tool_result": true,
		"codex.tool_result":       true,
		"grok_code.tool_result":   true,
		"gemini_cli.tool_call":    true,
	}
	claudeSpans = map[string]bool{
		"claude_code.interaction":          true,
		"claude_code.llm_request":          true,
		"claude_code.tool":                 true,
		"claude_code.tool.blocked_on_user": true,
		"claude_code.tool.execution":       true,
		"claude_code.hook":                 true,
	}
	codexSpans = map[string]bool{
		"run_turn":                  true,
		"codex.sampling":            true,
		"codex.turn_input":          true,
		"exec_command":              true,
		"unified_exec.exec_command": true,
		"codex.tool_blocking":       true,
	}
	toolSpans = map[string]bool{
		"claude_code.tool":          true,
		"exec_command":              true,
		"unified_exec.exec_command": true,
	}
	waitSpans = map[string]bool{
		"claude_code.tool.blocked_on_user": true,
		"codex.tool_blocking":              true,
	}
	execSpans = map[string]bool{
		"claude_code.tool.execution": true,
		"unified_exec.exec_command":  true,
	}
	opencodeSpans = map[string]bool{
		"SessionRunner.drain":   true,
		"SessionRunner.runStep": true,
		"SessionStep.attempt":   true,
		"Tool.execute":          true,
		"FileWrite.write":       true,
		"POST":                  true,
	}
	piSpanHeads = map[string]bool{
		"invoke_agent": true,
		"pi.turn":      true,
		"chat":         true,
		"execute_tool": true,
	}
	piLogs = map[string]bool{
		"pi.session.start": true,
		"pi.session.end":   true,
	}
	// usageAliases folds gen_ai.usage.* labels that name the same quantity
	// into one key. GenAI semconv v0.1.0 renamed
	// gen_ai.usage.cache_creation.input_tokens to
	// gen_ai.usage.cache_write.input_tokens and removed the old name;
	// OpenCode still sends the old one. reasoning_tokens is the pre-v0.1.0
	// spelling of reasoning.output_tokens. When a span carries more than one
	// of them, the largest value is kept; they are never added. Only the
	// per-span values are then summed across requests (issue #200).
	usageAliases = map[string]string{
		"cache_creation.input_tokens": "cache_write.input_tokens",
		"cache_creation_input_tokens": "cache_write.input_tokens",
		"reasoning_tokens":            "reasoning.output_tokens",
	}
	usageOrder = []string{
		"input_tokens",
		"output_tokens",
		"cache_read.input_tokens",
		"cache_read_input_tokens",
		"cache_write.input_tokens",
		"cache_write_input_tokens",
		"reasoning.output_tokens",
		"reasoning_tokens",
	}
	sessionCodexMetrics = []string{
		"codex.turn.token_usage",
		"codex.turn.cost_microusd",
		"codex.turn.e2e_duration_ms",
		"codex.tool.call",
	}
	modelLogs = map[string]bool{
		"claude_code.user_prompt":        true,
		"claude_code.assistant_response": true,
		"claude_code.api_request":        true,
		"claude_code.api_error":          true,
		"claude_code.tool_result":        true,
		"codex.conversation_starts":      true,
		"codex.user_prompt":              true,
		"codex.api_request":              true,
		"codex.sse_event":                true,
		"codex.tool_result":              true,
		"codex.turn_cost":                true,
		"grok_code.session_start":        true,
		"grok_code.session_end":          true,
		"grok_code.user_prompt":          true,
		"grok_code.assistant_response":   true,
		"grok_code.api_request":          true,
		"grok_code.api_error":            true,
		"grok_code.turn_completed":       true,
		"grok_code.model_switched":       true,
		"gemini_cli.config":              true,
		"gemini_cli.api_request":         true,
		"gemini_cli.api_response":        true,
		"gemini_cli.api_error":           true,
	}
	modelSpans = map[string]bool{
		"claude_code.interaction":          true,
		"claude_code.llm_request":          true,
		"claude_code.tool":                 true,
		"claude_code.tool.blocked_on_user": true,
		"claude_code.tool.execution":       true,
		"claude_code.hook":                 true,
		"run_turn":                         true,
		"codex.sampling":                   true,
		"exec_command":                     true,
	}
	cumulativeTokenFields = []string{"input_token_count", "cached_token_count", "cache_write_token_count"}
	additiveTokenFields   = []string{"output_token_count", "reasoning_token_count"}
	metricFail            = regexp.MustCompile(`metrics (?:histogram|counter|gauge) \[([^\]]+)\] failed: tag value contains invalid characters: (\S+)`)
	metricDetailKeys      = []string{
		"type", "token_type", "gen_ai.token.type", "model", "gen_ai.request.model",
		"tool", "gen_ai.tool.name", "gen_ai.operation.name", "decision", "success",
	}
)

type point struct {
	name        string
	kind        string
	temporality string
	value       float64
	time        int64
	order       int64
	unit        *string
	attributes  map[string]any
	// start is the point's StartTimeUnixNano, 0 when it was not sent.
	// resource is the frozen resource. Both only split cumulative series
	// into the runs of one process; see cumulativeTotal.
	start    int64
	resource string
}

type sessionKey struct {
	tool string
	id   string
}

// Summarize groups records into sessions. allSpans keeps internal span names
// in the tree; the default view hides them. Cost is priced from the bundled
// OpenRouter table.
func Summarize(records []record.Record, allSpans bool) Report {
	return SummarizeWith(records, allSpans, pricing.Bundled())
}

// SummarizeWith is Summarize with an explicit price catalog. A nil catalog
// leaves computed cost unknown.
func SummarizeWith(records []record.Record, allSpans bool, prices *pricing.Catalog) Report {
	attributed, leftover := attribute(records)
	sessions := make([]SessionSummary, 0, len(attributed))
	for key, grouped := range attributed {
		sessions = append(sessions, summarizeBucket(key.tool, key.id, grouped, allSpans, prices))
	}
	sort.SliceStable(sessions, func(i, j int) bool {
		if sessions[i].Tool != sessions[j].Tool {
			return sessions[i].Tool < sessions[j].Tool
		}
		return sessions[i].SessionID < sessions[j].SessionID
	})
	return Report{Sessions: sessions, Unattributed: unattributed(leftover)}
}

// ToolOf classifies a record as claude-code, codex, opencode, gemini-cli, pi,
// grok-cli, or unknown.
func ToolOf(rec record.Record) string {
	service := strings.ToLower(deref(rec.ServiceName))
	name := deref(rec.Name)
	body := deref(rec.Body)
	origin := strOr(rec.Attributes["originator"], rec.Resource["originator"])
	event := strAttr(rec.Attributes, "event.name")
	system := genAISystem(rec)
	if strings.HasPrefix(service, "claude-code") || strings.HasPrefix(name, "claude_code.") || strings.HasPrefix(body, "claude_code.") || strings.HasPrefix(event, "claude_code.") {
		return "claude-code"
	}
	if service == "codex" || strings.Contains(service, "codex") || strings.HasPrefix(strings.ToLower(origin), "codex") || strings.HasPrefix(name, "codex.") || strings.HasPrefix(body, "codex.") || strings.HasPrefix(event, "codex.") {
		return "codex"
	}
	if isQwenRecord(service, name, event) {
		return "qwen-code"
	}
	if service == "opencode" {
		return "opencode"
	}
	// Gemini CLI sets service.name=gemini-cli on the resource and names every
	// log in the event.name attribute (gemini_cli.*). Its metrics also record
	// gen_ai.client.token.usage, so it is classified before Pi.
	if service == "gemini-cli" || strings.HasPrefix(name, "gemini_cli.") || strings.HasPrefix(event, "gemini_cli.") {
		return "gemini-cli"
	}
	if service == "pi" || system == "pi" || strings.HasPrefix(name, "pi.session.") || strings.HasPrefix(event, "pi.session.") {
		return "pi"
	}
	if service == "grok-cli" || strings.HasPrefix(name, "grok_code.") || strings.HasPrefix(body, "grok_code.") || strings.HasPrefix(event, "grok_code.") {
		return "grok-cli"
	}
	if isCopilotRecord(service, name, rec) {
		return "copilot-cli"
	}
	return "unknown"
}

// logEventName is the event name the summary should use. For Grok CLI it
// prefers a stored name, then event.name, then an attribute-shape inference
// used when a live export left LogRecord name, body, and event.name empty
// (Grok CLI 1.0.92, 2026-11-23; the id may be on the OTEL event_name field
// which older ingest did not copy).
func logEventName(rec record.Record) string {
	if ToolOf(rec) == "grok-cli" && rec.Signal == "logs" {
		return grokEventName(rec)
	}
	return deref(rec.Name)
}

func grokEventName(rec record.Record) string {
	if n := deref(rec.Name); n != "" {
		return normalizeGrokEvent(n)
	}
	if n := strAttr(rec.Attributes, "event.name"); n != "" {
		return normalizeGrokEvent(n)
	}
	a := rec.Attributes
	switch {
	case hasAttr(a, "from_model") && hasAttr(a, "to_model"):
		return "grok_code.model_switched"
	case hasAttr(a, "prompt_length"):
		return "grok_code.user_prompt"
	case hasAttr(a, "response_length"):
		return "grok_code.assistant_response"
	case hasAttr(a, "cost_usd_micros") || (hasAttr(a, "input_tokens") && hasAttr(a, "output_tokens")):
		return grokAPI
	case hasAttr(a, "tool_name") && (hasAttr(a, "success") || hasAttr(a, "outcome")) && !hasAttr(a, "decision"):
		return grokToolResult
	case hasAttr(a, "decision") && (hasAttr(a, "tool_use_id") || hasAttr(a, "tool")):
		return "grok_code.tool_decision"
	case hasAttr(a, "duration_secs") && hasAttr(a, "turn_count"):
		return grokSessionEnd
	case hasAttr(a, "tool_call_count") && hasAttr(a, "duration_ms") && hasAttr(a, "outcome"):
		return grokTurnCompleted
	case hasAttr(a, "permission_mode") || (hasAttr(a, "hook_count") && hasAttr(a, "is_git_repo")):
		return "grok_code.session_start"
	default:
		return deref(rec.Name)
	}
}

func normalizeGrokEvent(name string) string {
	if strings.HasPrefix(name, "grok_code.") {
		return name
	}
	if !strings.Contains(name, ".") {
		return "grok_code." + name
	}
	return name
}

func hasAttr(attrs map[string]any, key string) bool {
	if attrs == nil {
		return false
	}
	_, ok := attrs[key]
	return ok
}

func genAISystem(rec record.Record) string {
	for _, source := range []map[string]any{rec.Attributes, rec.Resource} {
		if text := record.SummaryText(source["gen_ai.system"]); text != nil {
			return strings.ToLower(*text)
		}
	}
	return ""
}

func attribute(records []record.Record) (map[sessionKey][]record.Record, []record.Record) {
	scoped := map[sessionKey][]record.Record{}
	var unscoped []record.Record
	for _, rec := range records {
		if rec.SessionID != nil && *rec.SessionID != "" {
			key := sessionKey{tool: ToolOf(rec), id: *rec.SessionID}
			scoped[key] = append(scoped[key], rec)
		} else {
			unscoped = append(unscoped, rec)
		}
	}
	traceKeys := map[string]map[sessionKey]struct{}{}
	spanKeys := map[string]map[sessionKey]struct{}{}
	bounds := map[sessionKey][2]int64{}
	boundsSet := map[sessionKey]bool{}
	// A session that reported its own edge keeps that window instead of the
	// one inferred from its records. The close is optional: a session that
	// opened and never closed is still open, so its window has no end.
	opened := map[sessionKey]int64{}
	closed := map[sessionKey]int64{}
	for key, grouped := range scoped {
		for _, rec := range grouped {
			if rec.TraceID != nil && *rec.TraceID != "" {
				addKey(traceKeys, *rec.TraceID, key)
			}
			if rec.SpanID != nil && *rec.SpanID != "" {
				addKey(spanKeys, *rec.SpanID, key)
			}
			switch deref(rec.Name) {
			case "sdf.session.open":
				if stamp := rec.TimeUnixNano; stamp != nil {
					if prev, ok := opened[key]; !ok || *stamp < prev {
						opened[key] = *stamp
					}
				}
			case "sdf.session.close":
				if stamp := rec.TimeUnixNano; stamp != nil {
					if prev, ok := closed[key]; !ok || *stamp > prev {
						closed[key] = *stamp
					}
				}
			}
			for _, stamp := range stamps(rec) {
				if !boundsSet[key] {
					bounds[key] = [2]int64{stamp, stamp}
					boundsSet[key] = true
					continue
				}
				lohi := bounds[key]
				if stamp < lohi[0] {
					lohi[0] = stamp
				}
				if stamp > lohi[1] {
					lohi[1] = stamp
				}
				bounds[key] = lohi
			}
		}
	}
	for key, open := range opened {
		end, ok := closed[key]
		if !ok {
			// No close arrived. MaxInt64 keeps the window open, so a record
			// that lands after the last telemetry still belongs to it.
			end = math.MaxInt64
		}
		bounds[key] = [2]int64{open, end}
		boundsSet[key] = true
	}
	var leftover []record.Record
	for _, rec := range unscoped {
		key, ok := matchSession(rec, traceKeys, spanKeys, bounds)
		if !ok {
			leftover = append(leftover, rec)
			continue
		}
		scoped[key] = append(scoped[key], rec.WithSession(key.id))
	}
	return scoped, leftover
}

func addKey(index map[string]map[sessionKey]struct{}, id string, key sessionKey) {
	set := index[id]
	if set == nil {
		set = map[sessionKey]struct{}{}
		index[id] = set
	}
	set[key] = struct{}{}
}

func matchSession(rec record.Record, traceKeys, spanKeys map[string]map[sessionKey]struct{}, bounds map[sessionKey][2]int64) (sessionKey, bool) {
	if rec.TraceID != nil {
		if key, ok := onlyKey(traceKeys[*rec.TraceID]); ok {
			return key, true
		}
	}
	for _, candidate := range []*string{rec.SpanID, rec.ParentSpanID} {
		if candidate == nil || *candidate == "" {
			continue
		}
		if key, ok := onlyKey(spanKeys[*candidate]); ok {
			return key, true
		}
	}
	tool := ToolOf(rec)
	// OpenCode startup spans and process logs overlap a session in time but
	// are not that session. Pi metrics have no session id and still use the
	// time window below. Pi logs carry pi.session.id and are scoped at ingest.
	if tool == "opencode" && (rec.Signal == "logs" || rec.Signal == "traces") {
		return sessionKey{}, false
	}
	// Grok CLI startup histograms have no session.id. They can overlap a
	// session window but are process startup, not session work.
	if tool == "grok-cli" && rec.Signal == "metrics" && strings.HasPrefix(deref(rec.Name), "grok_code.startup.") {
		return sessionKey{}, false
	}
	st := stamps(rec)
	st = qwenStamps(tool, rec, st)
	if len(st) == 0 || tool == "unknown" {
		return sessionKey{}, false
	}
	stamp := st[0]
	var matches []sessionKey
	for key, lohi := range bounds {
		if key.tool != tool {
			continue
		}
		// A recorded close is exact, so no slack past it. A session with no
		// close uses MaxInt64 as its end, and adding the slack would overflow.
		hi := lohi[1]
		if hi < math.MaxInt64-timeSlackNS {
			hi += timeSlackNS
		}
		if lohi[0]-timeSlackNS <= stamp && stamp <= hi {
			matches = append(matches, key)
		}
	}
	if len(matches) == 1 {
		return matches[0], true
	}
	return sessionKey{}, false
}

func onlyKey(set map[sessionKey]struct{}) (sessionKey, bool) {
	if len(set) != 1 {
		return sessionKey{}, false
	}
	for key := range set {
		return key, true
	}
	return sessionKey{}, false
}

func unattributed(records []record.Record) []Unattributed {
	grouped := map[string][]record.Record{}
	var order []string
	for _, rec := range records {
		tool := ToolOf(rec)
		if _, ok := grouped[tool]; !ok {
			order = append(order, tool)
		}
		grouped[tool] = append(grouped[tool], rec)
	}
	sort.Strings(order)
	groups := make([]Unattributed, 0, len(order))
	for _, tool := range order {
		items := grouped[tool]
		names := unique(func() []string {
			out := make([]string, 0, len(items))
			for _, rec := range items {
				out = append(out, unattributedLabel(rec))
			}
			return out
		}())
		extra := len(names) - 20
		shown := names
		if extra > 0 {
			shown = append(append([]string{}, names[:20]...), "("+itoa(extra)+" more names)")
		}
		traces, logs, metrics := signalCounts(items)
		groups = append(groups, Unattributed{
			Tool:    tool,
			Traces:  traces,
			Logs:    logs,
			Metrics: metrics,
			Names:   nonNilStr(shown),
			Reason:  "not a session: no trace or span id shared with exactly one session, and the timestamp is not inside exactly one session of this tool",
		})
	}
	return groups
}

func summarizeBucket(tool, sessionID string, records []record.Record, allSpans bool, prices *pricing.Catalog) SessionSummary {
	points := reduce(pointsOf(records))
	spans, hidden := spanTree(records, allSpans)
	durationMS, durationSource := duration(records, points)
	tokens, tokensSource, tokensMethod := tokensOf(records, points)
	costUSD, costSource, costNote := costOf(records, points, tool)
	modelList := models(records, points)
	computed := pricing.Price(prices, pricing.Usage{
		Models: modelList,
		Tokens: tokenMap(tokens),
		Source: deref(tokensSource),
	})
	failures := metricFailures(records)
	summary := SessionSummary{
		SessionID:               sessionID,
		Tool:                    tool,
		ServiceNames:            serviceNames(records),
		Models:                  modelList,
		Traces:                  countSignal(records, "traces"),
		Logs:                    countSignal(records, "logs"),
		Metrics:                 countSignal(records, "metrics"),
		DurationMS:              durationMS,
		DurationSource:          durationSource,
		Tokens:                  tokens,
		TokensSource:            tokensSource,
		TokensMethod:            tokensMethod,
		CostUSD:                 costUSD,
		CostSource:              costSource,
		CostNote:                costNote,
		AgentReportedCostUSD:    costUSD,
		AgentReportedCostSource: costSource,
		AgentReportedCostNote:   costNote,
		ComputedCost:            &computed,
		Spans:                   nonNilSpans(spans),
		SpansHidden:             hidden,
		LogEvents:               nonNilLogs(logLines(records)),
		MetricLines:             nonNilMetrics(metricLines(points)),
		Missing:                 nonNilMissing(missing(tool, records, points, failures)),
		ToolCalls:               nonNilCalls(toolCalls(records, points)),
		Repository:              repositoryFacts(records),
	}
	if tool == "qwen-code" {
		summary = qwenSummary(summary, records, points, prices)
	}
	return summary
}

func spanTree(records []record.Record, allSpans bool) ([]SpanNode, int) {
	var spans []record.Record
	for _, rec := range records {
		if rec.Signal == "traces" {
			spans = append(spans, rec)
		}
	}
	var shown []record.Record
	for _, rec := range spans {
		if allSpans || interestingSpan(rec) {
			shown = append(shown, rec)
		}
	}
	byID := map[string]record.Record{}
	for _, rec := range spans {
		if rec.SpanID != nil && *rec.SpanID != "" {
			byID[*rec.SpanID] = rec
		}
	}
	shownIDs := map[string]bool{}
	for _, rec := range shown {
		if rec.SpanID != nil && *rec.SpanID != "" {
			shownIDs[*rec.SpanID] = true
		}
	}
	logs := map[string]record.Record{}
	for _, rec := range records {
		if rec.Signal == "logs" && rec.Name != nil && toolLogs[*rec.Name] {
			if id := callID(rec); id != nil {
				logs[*id] = rec
			}
		}
	}
	children := map[string][]record.Record{}
	var roots []record.Record
	for _, rec := range shown {
		parent := shownParent(rec, byID, shownIDs)
		if parent == nil {
			roots = append(roots, rec)
		} else {
			children[*parent] = append(children[*parent], rec)
		}
	}
	sort.SliceStable(roots, func(i, j int) bool { return spanLess(roots[i], roots[j]) })
	var build func(record.Record) SpanNode
	build = func(rec record.Record) SpanNode {
		id := ""
		if rec.SpanID != nil {
			id = *rec.SpanID
		}
		kids := append([]record.Record{}, children[id]...)
		sort.SliceStable(kids, func(i, j int) bool { return spanLess(kids[i], kids[j]) })
		built := make([]SpanNode, len(kids))
		for i, child := range kids {
			built[i] = build(child)
		}
		return spanNode(rec, built, logs, spans)
	}
	nodes := make([]SpanNode, len(roots))
	for i, rec := range roots {
		nodes[i] = build(rec)
	}
	return nodes, len(spans) - len(shown)
}

func shownParent(rec record.Record, byID map[string]record.Record, shownIDs map[string]bool) *string {
	seen := map[string]bool{}
	current := rec.ParentSpanID
	self := ""
	if rec.SpanID != nil {
		self = *rec.SpanID
	}
	for current != nil && *current != "" && !seen[*current] {
		seen[*current] = true
		if shownIDs[*current] && *current != self {
			id := *current
			return &id
		}
		parent, ok := byID[*current]
		if !ok {
			return nil
		}
		current = parent.ParentSpanID
	}
	return nil
}

func interestingSpan(rec record.Record) bool {
	name := deref(rec.Name)
	if claudeSpans[name] || codexSpans[name] || strings.HasPrefix(name, "claude_code.") {
		return true
	}
	if strings.HasPrefix(name, qwenPrefix) && ToolOf(rec) == "qwen-code" {
		return true
	}
	if opencodeSpans[name] || piSpanHeads[spanHead(name)] {
		return true
	}
	return callID(rec) != nil || toolName(rec) != nil
}

func spanHead(name string) string {
	if name == "" {
		return ""
	}
	head, _, _ := strings.Cut(name, " ")
	return head
}

func spanNode(rec record.Record, children []SpanNode, logs map[string]record.Record, spans []record.Record) SpanNode {
	call := callID(rec)
	tool := toolName(rec)
	var success, successSource, waitSource, execSource *string
	var waitMS, execMS *float64
	name := deref(rec.Name)
	if name == "claude_code.tool" || name == "exec_command" {
		var log *record.Record
		if call != nil {
			if found, ok := logs[*call]; ok {
				log = &found
			}
		}
		success = record.SummaryText(rec.Attributes["success"])
		if success != nil {
			successSource = record.Str("traces")
		}
		if log != nil {
			if text := record.SummaryText(log.Attributes["success"]); text != nil {
				success = text
				successSource = record.Str("logs " + deref(log.Name))
			}
		}
		if success == nil && rec.StatusCode == "ERROR" {
			success = record.Str("false")
			successSource = record.Str("traces status")
		}
		waitMS, waitSource = childDuration(rec, spans, waitSpans)
		execMS, execSource = childDuration(rec, spans, execSpans)
	} else if isDirectToolSpan(name) {
		success, successSource = reportedSuccess(rec)
	}
	if children == nil {
		children = []SpanNode{}
	}
	label := name
	if label == "" {
		label = "(unnamed)"
	}
	return SpanNode{
		Name:            label,
		DurationMS:      spanMS(rec),
		CallID:          call,
		ToolName:        tool,
		Success:         success,
		SuccessSource:   successSource,
		WaitMS:          waitMS,
		WaitSource:      waitSource,
		ExecutionMS:     execMS,
		ExecutionSource: execSource,
		StatusCode:      statusCodeOf(rec),
		StatusMessage:   rec.StatusMessage,
		Children:        children,
	}
}

func childDuration(rec record.Record, spans []record.Record, names map[string]bool) (*float64, *string) {
	call := callID(rec)
	name := deref(rec.Name)
	if !toolSpans[name] {
		return nil, nil
	}
	for _, span := range spans {
		spanName := deref(span.Name)
		if !names[spanName] {
			continue
		}
		sameCall := call != nil && callID(span) != nil && *callID(span) == *call
		child := rec.SpanID != nil && span.ParentSpanID != nil && *span.ParentSpanID == *rec.SpanID
		if sameCall || child {
			if elapsed := spanMS(span); elapsed != nil {
				return elapsed, record.Str("traces " + spanName)
			}
		}
	}
	return nil, nil
}

func duration(records []record.Record, points []point) (*float64, *string) {
	if total, source := copilotDuration(records); total != nil {
		return total, source
	}
	var interactions []float64
	for _, rec := range records {
		if rec.Signal != "traces" || deref(rec.Name) != "claude_code.interaction" {
			continue
		}
		elapsed := spanMS(rec)
		if elapsed == nil {
			elapsed = record.Number(rec.Attributes["interaction.duration_ms"])
		}
		if elapsed != nil {
			interactions = append(interactions, *elapsed)
		}
	}
	if len(interactions) > 0 {
		return record.Float(sum(interactions)), record.Str("traces claude_code.interaction")
	}
	var e2e []float64
	for _, p := range points {
		if p.name == codexDurationMetric {
			e2e = append(e2e, p.value)
		}
	}
	if len(e2e) > 0 {
		return record.Float(sum(e2e)), record.Str("metrics " + codexDurationMetric)
	}
	var turns []float64
	for _, rec := range records {
		if rec.Signal == "traces" && deref(rec.Name) == "run_turn" {
			if elapsed := spanMS(rec); elapsed != nil {
				turns = append(turns, *elapsed)
			}
		}
	}
	if len(turns) > 0 {
		return record.Float(sum(turns)), record.Str("traces run_turn")
	}
	if invoke := sumSpanMS(records, func(name string) bool { return spanHead(name) == "invoke_agent" }); invoke != nil {
		return invoke, record.Str("traces invoke_agent")
	}
	if drain := sumSpanMS(records, func(name string) bool { return name == "SessionRunner.drain" }); drain != nil {
		return drain, record.Str("traces SessionRunner.drain")
	}
	var active []float64
	for _, p := range points {
		if p.name == claudeActiveMetric {
			active = append(active, p.value)
		}
	}
	if len(active) > 0 {
		return record.Float(sum(active) * 1000), record.Str("metrics " + claudeActiveMetric)
	}
	var sessionEnd []float64
	for _, rec := range records {
		if rec.Signal != "logs" || logEventName(rec) != grokSessionEnd {
			continue
		}
		secs := record.Number(rec.Attributes["duration_secs"])
		if secs != nil {
			sessionEnd = append(sessionEnd, *secs*1000)
		}
	}
	if len(sessionEnd) > 0 {
		return record.Float(sum(sessionEnd)), record.Str("logs " + grokSessionEnd)
	}
	var turnDur []float64
	for _, rec := range records {
		if rec.Signal != "logs" || logEventName(rec) != grokTurnCompleted {
			continue
		}
		ms := record.Number(rec.Attributes["duration_ms"])
		if ms != nil {
			turnDur = append(turnDur, *ms)
		}
	}
	if len(turnDur) > 0 {
		return record.Float(sum(turnDur)), record.Str("logs " + grokTurnCompleted)
	}
	if window := geminiWindow(records); window != nil {
		return window, record.Str("logs and traces gemini_cli window")
	}
	var stampsAll []int64
	for _, rec := range records {
		stampsAll = append(stampsAll, stamps(rec)...)
	}
	if len(stampsAll) >= 2 {
		minS, maxS := stampsAll[0], stampsAll[0]
		for _, s := range stampsAll[1:] {
			if s < minS {
				minS = s
			}
			if s > maxS {
				maxS = s
			}
		}
		return record.Float(float64(maxS-minS) / 1_000_000), record.Str("observed_window")
	}
	return nil, nil
}

func tokenMap(tokens OrderedFloats) map[string]float64 {
	if tokens.Len() == 0 {
		return nil
	}
	out := make(map[string]float64, tokens.Len())
	for _, key := range tokens.Keys {
		out[key] = tokens.Vals[key]
	}
	return out
}

func tokensOf(records []record.Record, points []point) (OrderedFloats, *string, *string) {
	if tokens, source, method, ok := copilotTokens(records, points); ok {
		return tokens, source, method
	}
	var claude []point
	for _, p := range points {
		if p.name == claudeTokenMetric {
			claude = append(claude, p)
		}
	}
	if len(claude) > 0 {
		return sumBy(claude, "type"), record.Str("metrics " + claudeTokenMetric), record.Str("sum of delta points; a cumulative series adds up the latest point of each process run")
	}
	var codex []point
	for _, p := range points {
		if p.name == codexTokenMetric {
			codex = append(codex, p)
		}
	}
	if len(codex) > 0 {
		return sumBy(codex, "token_type"), record.Str("metrics " + codexTokenMetric), record.Str("sum of delta histogram points; a cumulative series adds up the latest point of each process run")
	}
	var claudeLogs []record.Record
	for _, rec := range records {
		if rec.Signal == "logs" && deref(rec.Name) == claudeAPI {
			claudeLogs = append(claudeLogs, rec)
		}
	}
	totals := OrderedFloats{}
	found := false
	for _, rec := range claudeLogs {
		if addFields(&totals, rec, []string{"input_tokens", "output_tokens", "cache_read_tokens", "cache_creation_tokens"}) {
			found = true
		}
	}
	if found {
		return totals, record.Str("logs " + claudeAPI), record.Str("sum of per-request token attributes")
	}
	var events []record.Record
	for _, rec := range records {
		if rec.Signal == "logs" && deref(rec.Name) == codexSSE && strAttr(rec.Attributes, "event.kind") == "response.completed" {
			events = append(events, rec)
		}
	}
	if len(events) > 0 {
		return codexSSETokens(events)
	}
	if gemini, source, method, ok := geminiTokens(records, points); ok {
		return gemini, source, method
	}
	if opencode, ok := genAIUsage(records, func(name string) bool { return name == "SessionStep.attempt" }, nil); ok {
		return opencode, record.Str("traces SessionStep.attempt"), record.Str(openCodeTokensMethod)
	}
	if pi, ok := genAIUsage(records, func(name string) bool { return spanHead(name) == "chat" }, []string{"input_tokens", "output_tokens"}); ok {
		return pi, record.Str("traces chat"), record.Str("sum of per-turn input_tokens and output_tokens; they are additive and are not added to gen_ai.client.token.usage; a span delivered more than once (same trace and span id) counts once")
	}
	var piMetrics []point
	for _, p := range points {
		if p.name == piTokenMetric {
			piMetrics = append(piMetrics, p)
		}
	}
	if len(piMetrics) > 0 {
		return sumBy(piMetrics, "gen_ai.token.type"), record.Str("metrics " + piTokenMetric), record.Str("sum of delta points; a cumulative series adds up the latest point of each process run")
	}
	var grok []point
	for _, p := range points {
		if p.name == grokTokenMetric {
			grok = append(grok, p)
		}
	}
	if len(grok) > 0 {
		return sumBy(grok, "type"), record.Str("metrics " + grokTokenMetric), record.Str("sum of delta points; a cumulative series adds up the latest point of each process run. Live Grok CLI 1.0.92 (2026-11-23) type=input|output|reasoning|cache_read; cache_read is a subset of input and reasoning is a subset of output")
	}
	var grokLogs []record.Record
	for _, rec := range records {
		if rec.Signal == "logs" && logEventName(rec) == grokAPI {
			grokLogs = append(grokLogs, rec)
		}
	}
	grokTotals := OrderedFloats{}
	grokFound := false
	for _, rec := range grokLogs {
		if addFields(&grokTotals, rec, []string{"input_tokens", "output_tokens", "reasoning_tokens", "cache_read_tokens", "cache_creation_tokens"}) {
			grokFound = true
		}
	}
	if grokFound {
		return grokTotals, record.Str("logs " + grokAPI), record.Str("sum of per-request token attributes from grok_code.api_request (input_tokens, output_tokens, reasoning_tokens, cache_read_tokens, cache_creation_tokens)")
	}
	return OrderedFloats{}, nil, nil
}

// openCodeTokensMethod explains the OpenCode SessionStep.attempt rule. One
// attempt span is one provider request: anomalyco/opencode
// packages/core/src/session/runner/step.ts annotates it with the finish
// usage of that single llm.stream call, not a session running total. Each
// request is billed its full input (the resent prefix is usually billed at
// the cache-read rate, but it is billed again), and cache read and cache
// write are what that request read and wrote. So every count is summed
// across requests (issue #200).
const openCodeTokensMethod = "sum over SessionStep.attempt spans (one span per provider request); input and cache counts are per request, because each request is billed its full input and its own cache reads and writes; old and new names of one count on the same span are one value (the largest), never added; a span delivered more than once (same trace and span id) counts once"

// genAIUsage sums gen_ai.usage.* attributes over the spans that match. Each
// matching span is one request whose counts are per-request deltas, so the
// totals are sums. Within one span, alias labels (usageAliases) are one
// quantity and keep the largest value. A span stored twice (same trace and
// span id, for example after an exporter retry) is one request; its copies
// are merged with the same per-label max before summing.
func genAIUsage(records []record.Record, match func(string) bool, only []string) (OrderedFloats, bool) {
	onlySet := map[string]bool{}
	for _, label := range only {
		onlySet[label] = true
	}
	var rows []map[string]float64
	spanRow := map[string]int{}
	for _, rec := range records {
		if rec.Signal != "traces" || !match(deref(rec.Name)) {
			continue
		}
		amounts := map[string]float64{}
		for key, value := range rec.Attributes {
			if !strings.HasPrefix(key, "gen_ai.usage.") {
				continue
			}
			label := strings.TrimPrefix(key, "gen_ai.usage.")
			if canonical, ok := usageAliases[label]; ok {
				label = canonical
			}
			if len(only) > 0 && !onlySet[label] {
				continue
			}
			amount := record.Number(value)
			if amount == nil {
				continue
			}
			if prev, ok := amounts[label]; ok && prev >= *amount {
				continue
			}
			amounts[label] = *amount
		}
		if len(amounts) == 0 {
			continue
		}
		if key, ok := spanKey(rec); ok {
			if index, seen := spanRow[key]; seen {
				for label, amount := range amounts {
					if prev, ok := rows[index][label]; !ok || amount > prev {
						rows[index][label] = amount
					}
				}
				continue
			}
			spanRow[key] = len(rows)
		}
		rows = append(rows, amounts)
	}
	if len(rows) == 0 {
		return OrderedFloats{}, false
	}
	var labels []string
	seen := map[string]bool{}
	for _, row := range rows {
		for label := range row {
			if !seen[label] {
				seen[label] = true
				labels = append(labels, label)
			}
		}
	}
	orderIndex := map[string]int{}
	for i, label := range usageOrder {
		orderIndex[label] = i
	}
	sort.SliceStable(labels, func(i, j int) bool {
		ai, aok := orderIndex[labels[i]]
		bi, bok := orderIndex[labels[j]]
		if !aok {
			ai = len(usageOrder)
		}
		if !bok {
			bi = len(usageOrder)
		}
		if ai != bi {
			return ai < bi
		}
		return labels[i] < labels[j]
	})
	var totals OrderedFloats
	for _, label := range labels {
		var values []float64
		for _, row := range rows {
			if v, ok := row[label]; ok {
				values = append(values, v)
			}
		}
		totals.Set(label, sum(values))
	}
	return totals, true
}

// spanKey identifies one span by trace and span id. A row without both ids
// has no key and is never merged with another row.
func spanKey(rec record.Record) (string, bool) {
	if rec.TraceID == nil || rec.SpanID == nil || *rec.TraceID == "" || *rec.SpanID == "" {
		return "", false
	}
	return *rec.TraceID + "/" + *rec.SpanID, true
}

func sumSpanMS(records []record.Record, match func(string) bool) *float64 {
	var found []float64
	for _, rec := range records {
		if rec.Signal != "traces" || !match(deref(rec.Name)) {
			continue
		}
		if elapsed := spanMS(rec); elapsed != nil {
			found = append(found, *elapsed)
		}
	}
	if len(found) == 0 {
		return nil
	}
	return record.Float(sum(found))
}

func codexSSETokens(events []record.Record) (OrderedFloats, *string, *string) {
	grouped := map[string][]record.Record{}
	var order []string
	for index, rec := range events {
		turn := "request-" + itoa(index)
		if text := record.SummaryText(rec.Attributes["turn.id"]); text != nil {
			turn = *text
		}
		if _, ok := grouped[turn]; !ok {
			order = append(order, turn)
		}
		grouped[turn] = append(grouped[turn], rec)
	}
	var chosen []record.Record
	for _, turn := range order {
		group := grouped[turn]
		best := group[0]
		for _, rec := range group[1:] {
			bt, rt := int64(0), int64(0)
			if best.TimeUnixNano != nil {
				bt = *best.TimeUnixNano
			}
			if rec.TimeUnixNano != nil {
				rt = *rec.TimeUnixNano
			}
			if rt > bt || (rt == bt && rec.ID > best.ID) {
				best = rec
			}
		}
		chosen = append(chosen, best)
	}
	var totals OrderedFloats
	for _, key := range cumulativeTokenFields {
		var values []float64
		for _, rec := range chosen {
			if amount := record.Number(rec.Attributes[key]); amount != nil {
				values = append(values, *amount)
			}
		}
		if len(values) > 0 {
			totals.Set(key, maxFloat(values))
		}
	}
	for _, key := range additiveTokenFields {
		var values []float64
		for _, rec := range chosen {
			if amount := record.Number(rec.Attributes[key]); amount != nil {
				values = append(values, *amount)
			}
		}
		if len(values) > 0 {
			totals.Set(key, sum(values))
		}
	}
	return totals,
		record.Str("logs " + codexSSE + " response.completed"),
		record.Str("input, cached, and cache_write are the max (the prefix is resent); output and reasoning are summed; last event per turn.id")
}

func costOf(records []record.Record, points []point, tool string) (*float64, *string, *string) {
	var note *string
	if tool == "claude-code" {
		note = record.Str("agent estimate, not the provider price")
	}
	var claude []float64
	for _, p := range points {
		if p.name == claudeCostMetric {
			claude = append(claude, p.value)
		}
	}
	if len(claude) > 0 {
		return record.Float(sum(claude)), record.Str("metrics " + claudeCostMetric), note
	}
	var codex []float64
	for _, p := range points {
		if p.name == codexCostMetric {
			codex = append(codex, p.value)
		}
	}
	if len(codex) > 0 {
		return record.Float(sum(codex) / 1_000_000), record.Str("metrics " + codexCostMetric), nil
	}
	var grokCost []float64
	for _, p := range points {
		if p.name == grokCostMetric {
			grokCost = append(grokCost, p.value)
		}
	}
	if len(grokCost) > 0 {
		return record.Float(sum(grokCost)), record.Str("metrics " + grokCostMetric), nil
	}
	total := 0.0
	found := false
	var source *string
	for _, rec := range records {
		if rec.Signal != "logs" {
			continue
		}
		var amount *float64
		var label string
		switch logEventName(rec) {
		case claudeAPI:
			amount = record.Number(rec.Attributes["cost_usd"])
			label = "logs " + claudeAPI + " cost_usd"
		case codexTurnCost:
			amount = record.Number(rec.Attributes["usage.estimated_usd"])
			label = "logs " + codexTurnCost + " usage.estimated_usd"
			note = nil
		case grokAPI:
			if micros := record.Number(rec.Attributes["cost_usd_micros"]); micros != nil {
				amount = record.Float(*micros / 1_000_000)
				label = "logs " + grokAPI + " cost_usd_micros"
			}
		default:
			continue
		}
		if amount == nil {
			continue
		}
		total += *amount
		found = true
		if source == nil {
			source = record.Str(label)
		}
	}
	if found {
		return record.Float(total), source, note
	}
	piTotal := 0.0
	piFound := false
	for _, rec := range records {
		if rec.Signal != "traces" || spanHead(deref(rec.Name)) != "chat" {
			continue
		}
		amount := record.Number(rec.Attributes["pi.cost.usd"])
		if amount == nil {
			continue
		}
		piTotal += *amount
		piFound = true
	}
	if piFound {
		return record.Float(piTotal), record.Str("traces chat pi.cost.usd"), nil
	}
	return nil, nil, nil
}

func toolCalls(records []record.Record, points []point) []ToolCall {
	var spans []record.Record
	var logs []record.Record
	for _, rec := range records {
		if rec.Signal == "traces" {
			spans = append(spans, rec)
		}
		if rec.Signal == "logs" {
			name := logEventName(rec)
			if toolLogs[name] {
				rec.Name = record.Str(name)
				logs = append(logs, rec)
			}
		}
	}
	logByID := map[string]record.Record{}
	for _, rec := range logs {
		if id := callID(rec); id != nil {
			logByID[*id] = rec
		}
	}
	var primary []record.Record
	seen := map[string]bool{}
	for _, rec := range spans {
		if !isDirectToolSpan(deref(rec.Name)) {
			continue
		}
		if id := callID(rec); id != nil {
			seen[*id] = true
		}
		primary = append(primary, rec)
	}
	for _, rec := range spans {
		if deref(rec.Name) != "unified_exec.exec_command" {
			continue
		}
		id := callID(rec)
		if id != nil && seen[*id] {
			continue
		}
		if id != nil {
			seen[*id] = true
		}
		primary = append(primary, rec)
	}
	var calls []ToolCall
	for _, rec := range primary {
		var log *record.Record
		if id := callID(rec); id != nil {
			if found, ok := logByID[*id]; ok {
				log = &found
			}
		}
		calls = append(calls, callFromSpan(rec, log, spans))
	}
	for _, rec := range logs {
		id := callID(rec)
		if id != nil && seen[*id] {
			continue
		}
		calls = append(calls, ToolCall{
			Name:       logToolName(rec),
			CallID:     id,
			Success:    record.SummaryText(rec.Attributes["success"]),
			DurationMS: record.Number(rec.Attributes["duration_ms"]),
			Source:     "logs " + deref(rec.Name),
		})
	}
	if len(calls) > 0 {
		return calls
	}
	for _, p := range points {
		if p.name != "codex.tool.call" {
			continue
		}
		v := p.value
		calls = append(calls, ToolCall{
			Name:    record.SummaryText(p.attributes["tool"]),
			Success: record.SummaryText(p.attributes["success"]),
			Count:   &v,
			Source:  "metrics codex.tool.call",
		})
	}
	return calls
}

func callFromSpan(rec record.Record, log *record.Record, spans []record.Record) ToolCall {
	waitMS, waitSource := childDuration(rec, spans, waitSpans)
	execMS, execSource := childDuration(rec, spans, execSpans)
	duration := spanMS(rec)
	if execMS != nil {
		duration = execMS
	}
	source := "traces " + deref(rec.Name)
	success, _ := reportedSuccess(rec)
	if log != nil {
		source = source + "; logs " + deref(log.Name)
		if text := record.SummaryText(log.Attributes["success"]); text != nil {
			success = text
		}
		if duration == nil {
			duration = record.Number(log.Attributes["duration_ms"])
		}
	}
	name := toolName(rec)
	if name == nil {
		name = rec.Name
	}
	return ToolCall{
		Name:            name,
		CallID:          callID(rec),
		Success:         success,
		DurationMS:      duration,
		Source:          source,
		WaitMS:          waitMS,
		WaitSource:      waitSource,
		ExecutionMS:     execMS,
		ExecutionSource: execSource,
	}
}

func logLines(records []record.Record) []LogLine {
	var logs []record.Record
	for _, rec := range records {
		if rec.Signal == "logs" {
			logs = append(logs, rec)
		}
	}
	var failures []record.Record
	var ordinary []record.Record
	for _, rec := range logs {
		if metricFail.MatchString(logText(rec)) {
			failures = append(failures, rec)
		} else {
			ordinary = append(ordinary, rec)
		}
	}
	counts := map[string][]record.Record{}
	var order []string
	for _, rec := range ordinary {
		name := logEventName(rec)
		if name == "" {
			name = "(unnamed)"
		}
		if _, ok := counts[name]; !ok {
			order = append(order, name)
		}
		counts[name] = append(counts[name], rec)
	}
	var lines []LogLine
	for _, name := range order {
		group := counts[name]
		var detail *string
		if name == codexSSE {
			completed := 0
			for _, rec := range group {
				if strAttr(rec.Attributes, "event.kind") == "response.completed" {
					completed++
				}
			}
			detail = record.Str("(response.completed x" + itoa(completed) + ")")
		} else if toolLogs[name] && len(group) == 1 {
			rec := group[0]
			var parts []string
			if text := logToolName(rec); text != nil {
				parts = append(parts, *text)
			}
			if text := record.SummaryText(rec.Attributes["success"]); text != nil {
				parts = append(parts, "success="+*text)
			}
			if len(parts) > 0 {
				detail = record.Str(strings.Join(parts, " "))
			}
		}
		lines = append(lines, LogLine{Name: name, Count: len(group), Detail: detail})
	}
	if len(failures) > 0 {
		sample := metricFail.FindStringSubmatch(logText(failures[0]))
		var detail *string
		if len(sample) >= 3 && sample[2] != "" {
			detail = record.Str("(model tag rejected: " + sample[2] + ")")
		} else {
			detail = record.Str("(model tag rejected)")
		}
		lines = append(lines, LogLine{Name: "codex.metric_export_error", Count: len(failures), Detail: detail})
	}
	return lines
}

func metricLines(points []point) []MetricLine {
	lines := make([]MetricLine, 0, len(points))
	for _, p := range points {
		var parts []string
		for _, key := range metricDetailKeys {
			if text := record.SummaryText(p.attributes[key]); text != nil {
				parts = append(parts, key+"="+*text)
			}
		}
		var detail *string
		if len(parts) > 0 {
			detail = record.Str(strings.Join(parts, " "))
		}
		lines = append(lines, MetricLine{
			Name:   p.name,
			Value:  p.value,
			Unit:   p.unit,
			Detail: detail,
			Source: "metrics",
		})
	}
	return lines
}

func metricFailures(records []record.Record) [][2]string {
	var found [][2]string
	for _, rec := range records {
		if rec.Signal != "logs" {
			continue
		}
		match := metricFail.FindStringSubmatch(logText(rec))
		if match != nil {
			found = append(found, [2]string{match[1], match[2]})
		}
	}
	return found
}

func missing(tool string, records []record.Record, points []point, failures [][2]string) []Missing {
	if tool == "copilot-cli" {
		return copilotMissing(records, points)
	}
	var out []Missing
	if countSignal(records, "traces") == 0 {
		var reason string
		switch tool {
		case "claude-code":
			reason = "no spans exported. Beta traces need CLAUDE_CODE_ENHANCED_TELEMETRY_BETA=1 and OTEL_TRACES_EXPORTER=otlp."
		case "codex":
			reason = "no spans exported. Set otel.trace_exporter to an otlp-http endpoint ending in /v1/traces."
		case "opencode":
			reason = "no spans exported. OpenCode v2 exports traces when OTEL_EXPORTER_OTLP_ENDPOINT is set."
		case "pi":
			reason = "no spans exported. Pi has no built-in OTLP exporter; install pi-otel and set OTEL_EXPORTER_OTLP_PROTOCOL=http/json."
		case "grok-cli":
			reason = "Grok CLI's external OTEL stream exports logs and metrics only (no traces). Documented in xAI monitoring-usage (alpha, grok_code.schema.version=v1)."
		case "gemini-cli":
			reason = "no spans attributed to this session. Gemini CLI spans carry gen_ai.conversation.id and are exported once telemetry.enabled is true and otlpEndpoint is set."
		default:
			reason = "no spans exported for this session."
		}
		out = append(out, Missing{Signal: "traces", Reason: reason})
	}
	if countSignal(records, "logs") == 0 {
		var reason string
		switch tool {
		case "claude-code":
			reason = "no log events exported. Set OTEL_LOGS_EXPORTER=otlp."
		case "codex":
			reason = "no log events exported. otel.exporter defaults to none."
		case "opencode":
			reason = "OpenCode v2 does not export per-session logs. Its OTLP logs are process events without session.id, so none are attributed to this session."
		case "pi":
			reason = "pi-otel logs only session start and end (pi.session.start, pi.session.end) and only when PI_OTEL_LOGS=1. None are attributed to this session."
		case "grok-cli":
			reason = "no log events attributed to this session. Set GROK_EXTERNAL_OTEL=1 and OTEL_LOGS_EXPORTER=otlp. Events carry session.id once a session exists."
		case "gemini-cli":
			reason = "no log events attributed to this session. Set telemetry.enabled=true and telemetry.otlpEndpoint in ~/.gemini/settings.json; telemetry.outfile overrides the endpoint."
		default:
			reason = "no log events exported for this session."
		}
		out = append(out, Missing{Signal: "logs", Reason: reason})
	} else if tool == "pi" {
		onlyCatalog := true
		for _, rec := range records {
			if rec.Signal == "logs" && !piLogs[deref(rec.Name)] {
				onlyCatalog = false
				break
			}
		}
		if onlyCatalog {
			out = append(out, Missing{
				Signal: "log catalog",
				Reason: "pi-otel logs only session start and end, not tool results or per-turn events.",
			})
		}
	}
	present := map[string]bool{}
	for _, p := range points {
		present[p.name] = true
	}
	failed := map[string]bool{}
	for _, item := range failures {
		failed[item[0]] = true
	}
	var sessionFailed []string
	for _, name := range sessionCodexMetrics {
		if failed[name] && !present[name] {
			sessionFailed = append(sessionFailed, name)
		}
	}
	if len(sessionFailed) > 0 {
		model := "the model id"
		if len(failures) > 0 && failures[0][1] != "" {
			model = failures[0][1]
		}
		out = append(out, Missing{
			Signal: "session metrics",
			Reason: "Codex 0.167.0 records " + strings.Join(sessionFailed, ", ") +
				" only when every metric tag is valid. The model tag " + pythonRepr(model) + " contains " +
				"':', which validate_tag_value rejects, so those instruments were not exported. " +
				"A slash is allowed; there is no config key that tags metrics with a different " +
				"name than the wire model. Tokens and duration below fall back to logs and spans.",
		})
	}
	if countSignal(records, "metrics") == 0 && len(sessionFailed) == 0 {
		var reason string
		switch tool {
		case "claude-code":
			reason = "no metrics exported. Set OTEL_METRICS_EXPORTER=otlp."
		case "codex":
			reason = "no metrics exported. otel.metrics_exporter defaults to statsig, which does not send to this receiver."
		case "opencode":
			reason = "OpenCode v2 does not export OTLP metrics."
		case "pi":
			reason = "pi-otel metrics have no session id and are off unless PI_OTEL_METRICS=1. None were attributed by trace, span, or this session's time window."
		case "grok-cli":
			reason = "no metrics attributed to this session. Set GROK_EXTERNAL_OTEL=1 and OTEL_METRICS_EXPORTER=otlp. session.id is on metrics by default (OTEL_METRICS_INCLUDE_SESSION_ID=1)."
		case "gemini-cli":
			reason = "no metrics attributed to this session. Gemini CLI exports cumulative metrics every 10 seconds, so a run shorter than one interval may end before the first export."
		default:
			reason = "no metrics exported for this session."
		}
		out = append(out, Missing{Signal: "metrics", Reason: reason})
	}
	if tool == "gemini-cli" {
		out = append(out, Missing{
			Signal: "agent cost",
			Reason: "Gemini CLI exports no cost metric or cost attribute (docs/cli/telemetry.md). Agent-reported cost stays unknown; computed cost is shown only when a pricing rule covers the token source.",
		})
	}
	return out
}

func models(records []record.Record, points []point) []string {
	var found []string
	for _, rec := range records {
		if rec.Signal == "logs" && rec.Name != nil && modelLogs[*rec.Name] {
			addModel(&found, rec.Attributes)
		} else if rec.Signal == "traces" && (modelSpans[deref(rec.Name)] || record.SummaryText(rec.Attributes["gen_ai.request.model"]) != nil) {
			addModel(&found, rec.Attributes)
		}
	}
	for _, p := range points {
		addModel(&found, p.attributes)
	}
	return nonNilStr(found)
}

func addModel(found *[]string, attributes map[string]any) {
	for _, key := range []string{"model", "gen_ai.request.model"} {
		text := record.SummaryText(attributes[key])
		if text == nil {
			continue
		}
		seen := false
		for _, item := range *found {
			if item == *text {
				seen = true
				break
			}
		}
		if !seen {
			*found = append(*found, *text)
		}
	}
}

func pointsOf(records []record.Record) []point {
	var points []point
	for _, rec := range records {
		if rec.Signal != "metrics" || rec.Value == nil {
			continue
		}
		kind := ""
		if rec.MetricKind != nil {
			kind = *rec.MetricKind
		}
		temp := "unspecified"
		if rec.MetricTemporality != nil && *rec.MetricTemporality != "" {
			temp = *rec.MetricTemporality
		}
		when := int64(0)
		if rec.TimeUnixNano != nil {
			when = *rec.TimeUnixNano
		}
		attrs := rec.Attributes
		if attrs == nil {
			attrs = map[string]any{}
		}
		start := int64(0)
		if rec.StartUnixNano != nil {
			start = *rec.StartUnixNano
		}
		points = append(points, point{
			name:        deref(rec.Name),
			kind:        kind,
			temporality: temp,
			value:       *rec.Value,
			time:        when,
			order:       rec.ID,
			unit:        rec.MetricUnit,
			attributes:  attrs,
			start:       start,
			resource:    freeze(rec.Resource),
		})
	}
	return points
}

func reduce(points []point) []point {
	type groupKey struct{ raw string }
	var order []groupKey
	grouped := map[groupKey][]point{}
	for _, p := range points {
		key := groupKey{raw: p.name + "\x00" + p.kind + "\x00" + p.temporality + "\x00" + freeze(p.attributes)}
		if _, ok := grouped[key]; !ok {
			order = append(order, key)
		}
		grouped[key] = append(grouped[key], p)
	}
	reduced := make([]point, 0, len(order))
	for _, key := range order {
		group := grouped[key]
		if group[0].kind == "gauge" || group[0].temporality == "cumulative" {
			best := group[0]
			for _, item := range group[1:] {
				if later(item, best) {
					best = item
				}
			}
			if group[0].kind != "gauge" {
				best.value = cumulativeTotal(group)
			}
			reduced = append(reduced, best)
			continue
		}
		total := 0.0
		maxTime := group[0].time
		maxOrder := group[0].order
		for _, item := range group {
			total += item.value
			if item.time > maxTime {
				maxTime = item.time
			}
			if item.order > maxOrder {
				maxOrder = item.order
			}
		}
		first := group[0]
		first.value = total
		first.time = maxTime
		first.order = maxOrder
		reduced = append(reduced, first)
	}
	return reduced
}

func later(a, b point) bool {
	return a.time > b.time || (a.time == b.time && a.order > b.order)
}

// cumulativeTotal is the session total of one cumulative series (same name,
// kind, and attributes). A cumulative counter restarts at 0 when its process
// restarts, for example after `gemini --resume` reuses the session.id. Each
// run is one segment, and the total is the sum of every segment's last value:
// 1000 in the first run and 500 after a resume is 1500, not 500.
//
// Points with a StartTimeUnixNano are keyed by start time only. The OTel SDK
// keeps one start time for the life of a cumulative stream, so a new start
// time is a new run, and two processes exporting at once with different start
// times are each counted once. Within one start time the latest point (by
// time, then storage order) is the run's value; a lower value is not a reset,
// because a cumulative UpDownCounter may go down (5, 3, 4 is 4). The resource
// is not part of the key, so a resource that gains an attribute mid-run (for
// example process.pid) does not split the run.
//
// A point without a start time (start 0) joins the run with the latest start
// time at or before its own time among the runs its own resource reported, if
// there is one. A startless point from another resource is another process (a
// resume whose exporter sends no start time, for example), so it never joins
// a run it did not report. Otherwise it is grouped by resource, ordered by
// time, and a value lower than the previous one starts a
// new segment; that is the only way to see a reset from an exporter that
// sends no start time. Points of one group with the same time are one point:
// the one stored last wins.
//
// Known limits (docs/gaps.md): two concurrent processes with the same start
// time (or no start time and the same resource) are read as one run or as
// resets; a restart without a start time whose first value is not lower than
// the last one is not seen, which undercounts.
func cumulativeTotal(group []point) float64 {
	var starts []int64
	byStart := map[int64]point{}
	// startsOf lists, per resource, the start times its points carry.
	startsOf := map[string][]int64{}
	for _, p := range group {
		if p.start == 0 {
			continue
		}
		best, ok := byStart[p.start]
		if !ok {
			starts = append(starts, p.start)
		}
		if !ok || later(p, best) {
			byStart[p.start] = p
		}
		if !slices.Contains(startsOf[p.resource], p.start) {
			startsOf[p.resource] = append(startsOf[p.resource], p.start)
		}
	}
	for _, list := range startsOf {
		slices.Sort(list)
	}
	var resources []string
	loose := map[string][]point{}
	for _, p := range group {
		if p.start != 0 {
			continue
		}
		own := startsOf[p.resource]
		i := sort.Search(len(own), func(i int) bool { return own[i] > p.time }) - 1
		if i >= 0 {
			if best := byStart[own[i]]; later(p, best) {
				p.start = own[i]
				byStart[own[i]] = p
			}
			continue
		}
		if _, ok := loose[p.resource]; !ok {
			resources = append(resources, p.resource)
		}
		loose[p.resource] = append(loose[p.resource], p)
	}
	total := 0.0
	for _, start := range starts {
		total += byStart[start].value
	}
	for _, resource := range resources {
		run := append([]point(nil), loose[resource]...)
		sort.SliceStable(run, func(i, j int) bool { return later(run[j], run[i]) })
		var last point
		for i, p := range run {
			switch {
			case i == 0:
			case p.time == last.time:
				// Same time: an update of the same point, not a new value.
			case p.value < last.value:
				total += last.value
			}
			last = p
		}
		total += last.value
	}
	return total
}

func sumBy(points []point, key string) OrderedFloats {
	var totals OrderedFloats
	for _, p := range points {
		label := "value"
		if text := record.SummaryText(p.attributes[key]); text != nil {
			label = *text
		}
		totals.Add(label, p.value)
	}
	return totals
}

func addFields(totals *OrderedFloats, rec record.Record, keys []string) bool {
	found := false
	for _, key := range keys {
		amount := record.Number(rec.Attributes[key])
		if amount == nil {
			continue
		}
		totals.Add(key, *amount)
		found = true
	}
	return found
}

func callID(rec record.Record) *string {
	return firstText(rec.Attributes, "call_id", "tool_use_id", "gen_ai.tool.call.id")
}

func toolName(rec record.Record) *string {
	return firstText(rec.Attributes, "tool_name", "gen_ai.tool.name")
}

func isDirectToolSpan(name string) bool {
	return name == "claude_code.tool" || name == "exec_command" || name == "Tool.execute" || spanHead(name) == "execute_tool"
}

func isToolCallSpan(name string) bool {
	return isDirectToolSpan(name) || name == "unified_exec.exec_command"
}

func statusCodeOf(rec record.Record) string {
	switch rec.StatusCode {
	case "OK", "ERROR", "UNSET":
		return rec.StatusCode
	default:
		return "UNSET"
	}
}

// reportedSuccess returns the success flag and, when the flag should be
// labeled, the source. An explicit success attribute or pi.tool.is_error wins.
// A tool-call span with OTLP status ERROR and no explicit flag is false.
func reportedSuccess(rec record.Record) (*string, *string) {
	if success, source, ok := copilotToolSuccess(rec); ok {
		return success, source
	}
	if _, ok := rec.Attributes["pi.tool.is_error"]; ok {
		flag, parsed := record.Boolish(rec.Attributes["pi.tool.is_error"])
		if !parsed {
			return record.Str("unknown"), nil
		}
		if flag {
			return record.Str("false"), record.Str("traces")
		}
		return record.Str("true"), record.Str("traces")
	}
	if text := record.SummaryText(rec.Attributes["success"]); text != nil {
		return text, record.Str("traces")
	}
	if rec.StatusCode == "ERROR" && isToolCallSpan(deref(rec.Name)) {
		return record.Str("false"), record.Str("traces status")
	}
	name := deref(rec.Name)
	if name == "Tool.execute" || spanHead(name) == "execute_tool" {
		return record.Str("unknown"), nil
	}
	return nil, nil
}

func unattributedLabel(rec record.Record) string {
	name := deref(rec.Name)
	if name == "" {
		name = "(unnamed)"
	}
	if rec.Signal == "metrics" && rec.Value != nil {
		return name + "=" + fmtNum(*rec.Value) + " (metrics)"
	}
	return name + " (" + rec.Signal + ")"
}

func logText(rec record.Record) string {
	var parts []string
	if rec.Name != nil && *rec.Name != "" {
		parts = append(parts, *rec.Name)
	}
	if rec.Body != nil && *rec.Body != "" {
		parts = append(parts, *rec.Body)
	}
	return strings.Join(parts, " ")
}

func stamps(rec record.Record) []int64 {
	var out []int64
	for _, stamp := range []*int64{rec.StartUnixNano, rec.EndUnixNano, rec.TimeUnixNano} {
		if stamp != nil {
			out = append(out, *stamp)
		}
	}
	return out
}

func spanLess(a, b record.Record) bool {
	ta, tb := spanKeyTime(a), spanKeyTime(b)
	if ta != tb {
		return ta < tb
	}
	return a.ID < b.ID
}

func spanKeyTime(rec record.Record) int64 {
	if rec.StartUnixNano != nil {
		return *rec.StartUnixNano
	}
	if rec.TimeUnixNano != nil {
		return *rec.TimeUnixNano
	}
	return 0
}

func spanMS(rec record.Record) *float64 {
	if rec.StartUnixNano == nil || rec.EndUnixNano == nil {
		return nil
	}
	if *rec.EndUnixNano < *rec.StartUnixNano {
		return nil
	}
	return record.Float(float64(*rec.EndUnixNano-*rec.StartUnixNano) / 1_000_000)
}

func firstText(attributes map[string]any, keys ...string) *string {
	for _, key := range keys {
		if text := record.SummaryText(attributes[key]); text != nil {
			return text
		}
	}
	return nil
}

func serviceNames(records []record.Record) []string {
	var values []string
	for _, rec := range records {
		if rec.ServiceName != nil {
			values = append(values, *rec.ServiceName)
		}
	}
	return nonNilStr(unique(values))
}

func unique(values []string) []string {
	var found []string
	seen := map[string]bool{}
	for _, value := range values {
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		found = append(found, value)
	}
	return found
}

func signalCounts(records []record.Record) (traces, logs, metrics int) {
	return countSignal(records, "traces"), countSignal(records, "logs"), countSignal(records, "metrics")
}

func countSignal(records []record.Record, signal string) int {
	n := 0
	for _, rec := range records {
		if rec.Signal == signal {
			n++
		}
	}
	return n
}

func sum(values []float64) float64 {
	total := 0.0
	for _, v := range values {
		total += v
	}
	return total
}

func maxFloat(values []float64) float64 {
	best := values[0]
	for _, v := range values[1:] {
		if v > best {
			best = v
		}
	}
	return best
}

func freeze(attributes map[string]any) string {
	keys := make([]string, 0, len(attributes))
	for key := range attributes {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, key := range keys {
		b.WriteString(key)
		b.WriteByte('=')
		b.WriteString(freezeValue(attributes[key]))
		b.WriteByte(';')
	}
	return b.String()
}

func freezeValue(v any) string {
	switch t := v.(type) {
	case nil:
		return "z"
	case string:
		return "s:" + t
	case bool:
		if t {
			return "b:1"
		}
		return "b:0"
	case int:
		return "n:" + strconvFormat(float64(t))
	case int64:
		return "n:" + strconvFormat(float64(t))
	case float64:
		return "n:" + strconvFormat(t)
	case []any:
		parts := make([]string, len(t))
		for i, item := range t {
			parts[i] = freezeValue(item)
		}
		return "l:[" + strings.Join(parts, ",") + "]"
	case map[string]any:
		return "m:{" + freeze(t) + "}"
	default:
		if n := record.Number(t); n != nil {
			return "n:" + strconvFormat(*n)
		}
		return "o:" + strOr(t)
	}
}

func strconvFormat(f float64) string {
	if math.IsNaN(f) {
		return "nan"
	}
	if math.IsInf(f, 0) {
		if f > 0 {
			return "inf"
		}
		return "-inf"
	}
	return strings.TrimRight(strings.TrimRight(formatFixed(f), "0"), ".")
}

func formatFixed(f float64) string {
	return strings.TrimSpace(strconvFormatFloat(f))
}

func strconvFormatFloat(f float64) string {
	return strconv.FormatFloat(f, 'f', -1, 64)
}

func strOr(values ...any) string {
	for _, v := range values {
		if text := record.SummaryText(v); text != nil {
			return *text
		}
	}
	return ""
}

func strAttr(attributes map[string]any, key string) string {
	if attributes == nil {
		return ""
	}
	if text := record.SummaryText(attributes[key]); text != nil {
		return *text
	}
	return ""
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func pythonRepr(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "\\'") + "'"
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	if neg {
		return "-" + string(digits)
	}
	return string(digits)
}

func nonNilStr(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func nonNilSpans(s []SpanNode) []SpanNode {
	if s == nil {
		return []SpanNode{}
	}
	return s
}

func nonNilLogs(s []LogLine) []LogLine {
	if s == nil {
		return []LogLine{}
	}
	return s
}

func nonNilMetrics(s []MetricLine) []MetricLine {
	if s == nil {
		return []MetricLine{}
	}
	return s
}

func nonNilMissing(s []Missing) []Missing {
	if s == nil {
		return []Missing{}
	}
	return s
}

func nonNilCalls(s []ToolCall) []ToolCall {
	if s == nil {
		return []ToolCall{}
	}
	return s
}

// geminiTokens reads Gemini CLI token counts. gemini_cli.token.usage is a
// cumulative counter by type (input, output, thought, cache, tool), so a
// series keeps its latest point. Without those points it sums the token
// attributes on gemini_cli.api_response. gen_ai.client.token.usage and the
// gen_ai.client.inference.operation.details log repeat the same input and
// output counts and are not added again. Field meanings follow
// packages/core/src/telemetry (ApiResponseEvent): input_token_count is the
// prompt count and includes cached_content_token_count; output_token_count
// excludes thoughts_token_count; tool_token_count is the tool-use prompt.
func geminiTokens(records []record.Record, points []point) (OrderedFloats, *string, *string, bool) {
	var metricPoints []point
	for _, p := range points {
		if p.name == geminiTokenMetric {
			metricPoints = append(metricPoints, p)
		}
	}
	if len(metricPoints) > 0 {
		return sumBy(metricPoints, "type"), record.Str("metrics " + geminiTokenMetric), record.Str("sum of delta points; a cumulative series keeps its latest point. Gemini CLI type=input|output|thought|cache|tool; cache is a subset of input, thought and tool are not part of input or output"), true
	}
	totals := OrderedFloats{}
	found := false
	for _, rec := range records {
		if rec.Signal != "logs" || logEventName(rec) != geminiAPIResponse {
			continue
		}
		if addFields(&totals, rec, []string{"input_token_count", "output_token_count", "cached_content_token_count", "thoughts_token_count", "tool_token_count"}) {
			found = true
		}
	}
	if found {
		return totals, record.Str("logs " + geminiAPIResponse), record.Str("sum of per-response token attributes from gemini_cli.api_response (input_token_count, output_token_count, cached_content_token_count, thoughts_token_count, tool_token_count); total_token_count is not added"), true
	}
	return OrderedFloats{}, nil, nil, false
}

// logToolName is the tool name on a tool-result log. Gemini CLI's
// gemini_cli.tool_call uses function_name; the others use tool_name.
func logToolName(rec record.Record) *string {
	return firstText(rec.Attributes, "tool_name", "function_name")
}

// geminiWindow is the min-to-max timestamp of a Gemini CLI session's logs and
// spans. Gemini CLI exports no session or turn duration. Its metrics are
// cumulative, start at process start, and are exported every 10 seconds, so
// they are left out of the window.
func geminiWindow(records []record.Record) *float64 {
	var lo, hi int64
	seen := 0
	gemini := false
	for _, rec := range records {
		if rec.Signal == "metrics" {
			continue
		}
		if ToolOf(rec) != "gemini-cli" {
			continue
		}
		if rec.Signal == "logs" && strings.HasPrefix(logEventName(rec), "gemini_cli.") {
			gemini = true
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
	if !gemini || seen < 2 {
		return nil
	}
	return record.Float(float64(hi-lo) / 1_000_000)
}
