package summary

import (
	"strings"
	"testing"

	"github.com/paulpham157/sdf/internal/pricing"
	"github.com/paulpham157/sdf/internal/record"
)

// Fixtures in this file are labeled. Docs-schema tests follow the published
// Grok CLI external OTEL guide (24-monitoring-usage.md, grok_code.schema.version=v1,
// alpha) and keep event names on the record. Live-shape tests follow the
// authenticated grok 1.0.92 capture on 2026-11-23 (Asia/Bangkok): unnamed logs,
// grok_code.cost.usage in USD, api_request cost_usd_micros, and token overlap.

const grokDocsSession = "ses_docs_grok_code_0001"

func grokLog(name, session string, when int64, attrs map[string]any) record.Record {
	all := map[string]any{"session.id": session, "event.name": name}
	for k, v := range attrs {
		all[k] = v
	}
	return record.Record{
		Signal:       "logs",
		ServiceName:  record.Str("grok-cli"),
		SessionID:    record.Str(session),
		Name:         record.Str(name),
		Body:         record.Str(name),
		TimeUnixNano: record.Int64(when),
		Attributes:   all,
		Resource: map[string]any{
			"service.name":             "grok-cli",
			"grok_code.schema.version": "v1",
			"sdf.fixture":              "docs-schema",
		},
	}
}

func TestToolOfRecognizesGrokCLI(t *testing.T) {
	if ToolOf(record.Record{ServiceName: record.Str("grok-cli")}) != "grok-cli" {
		t.Fatal("service.name")
	}
	if ToolOf(record.Record{Name: record.Str("grok_code.token.usage")}) != "grok-cli" {
		t.Fatal("metric name")
	}
	if ToolOf(record.Record{Attributes: map[string]any{"event.name": "grok_code.session_start"}}) != "grok-cli" {
		t.Fatal("event.name")
	}
}

func TestGrokDocsSchemaSessionTokensCostToolsAndDuration(t *testing.T) {
	session := grokDocsSession
	// Happy-path model is in the bundled catalog and has no pricing.overrides.
	// A real Grok CLI session may emit grok-4.6, which suffixes to x-ai/grok-4.6
	// and is unknown because that row has overrides (tested separately).
	model := "openai/gpt-4o"
	rows := []record.Record{
		grokLog("grok_code.session_start", session, 1_000, map[string]any{"model": model, "permission_mode": "default"}),
		metric(grokTokenMetric, 2000, &session, "delta", "sum", map[string]any{"type": "input", "model": model}, 2_000, 2, "grok-cli"),
		metric(grokTokenMetric, 150, &session, "delta", "sum", map[string]any{"type": "output", "model": model}, 2_000, 3, "grok-cli"),
		metric(grokTokenMetric, 40, &session, "delta", "sum", map[string]any{"type": "reasoning", "model": model}, 2_000, 4, "grok-cli"),
		metric(grokTokenMetric, 100, &session, "delta", "sum", map[string]any{"type": "cache_read", "model": model}, 2_000, 5, "grok-cli"),
		grokLog("grok_code.api_request", session, 2_000, map[string]any{
			"model": model, "duration_ms": 800,
			"input_tokens": 2000, "output_tokens": 150,
			"reasoning_tokens": 40, "cache_read_tokens": 100,
		}),
		grokLog("grok_code.tool_result", session, 3_000, map[string]any{
			"tool_name": "bash", "outcome": "success", "success": "true",
			"duration_ms": 25, "tool_use_id": "tool_docs_1", "file_extension": "",
		}),
		grokLog("grok_code.turn_completed", session, 4_000, map[string]any{
			"outcome": "completed", "duration_ms": 1200, "tool_call_count": 1, "model": model,
		}),
		grokLog("grok_code.session_end", session, 5_000, map[string]any{
			"duration_secs": 12, "turn_count": 1, "tool_call_count": 1, "model": model,
		}),
	}
	report := Summarize(rows, false)
	if len(report.Sessions) != 1 || len(report.Unattributed) != 0 {
		t.Fatalf("sessions=%d unattributed=%+v", len(report.Sessions), report.Unattributed)
	}
	got := report.Sessions[0]
	if got.Tool != "grok-cli" || strings.Join(got.Models, ",") != model {
		t.Fatalf("tool=%s models=%v", got.Tool, got.Models)
	}
	if deref(got.TokensSource) != "metrics grok_code.token.usage" {
		t.Fatalf("tokens source %s", deref(got.TokensSource))
	}
	in, _ := got.Tokens.Get("input")
	out, _ := got.Tokens.Get("output")
	reason, _ := got.Tokens.Get("reasoning")
	cache, _ := got.Tokens.Get("cache_read")
	if in != 2000 || out != 150 || reason != 40 || cache != 100 {
		t.Fatalf("tokens %+v", got.Tokens.Vals)
	}
	if got.CostUSD != nil || got.AgentReportedCostUSD != nil {
		t.Fatalf("this fixture has no cost.usage metric; got agent cost %v / %v", got.CostUSD, got.AgentReportedCostUSD)
	}
	if got.ComputedCost.Status != "known" || got.ComputedCost.USD == "" {
		t.Fatalf("computed cost %+v", got.ComputedCost)
	}
	// Overlap proven by the live 2026-11-23 session: cache_read subset of input,
	// reasoning subset of output. gpt-4o rates:
	// (2000-100)*0.0000025 + 100*0.00000125 + (150-40)*0.00001 + 40*0.00001
	// = 0.00475 + 0.000125 + 0.0011 + 0.0004 = 0.006375
	if got.ComputedCost.USD != "0.006375" {
		t.Fatalf("computed usd %s note=%s", got.ComputedCost.USD, got.ComputedCost.Note)
	}
	if got.DurationMS == nil || *got.DurationMS != 12000 || deref(got.DurationSource) != "logs grok_code.session_end" {
		t.Fatalf("duration %v %s", got.DurationMS, deref(got.DurationSource))
	}
	if len(got.ToolCalls) != 1 || deref(got.ToolCalls[0].Name) != "bash" || deref(got.ToolCalls[0].Success) != "true" {
		t.Fatalf("tools %+v", got.ToolCalls)
	}
	if got.ToolCalls[0].DurationMS == nil || *got.ToolCalls[0].DurationMS != 25 {
		t.Fatalf("tool duration %+v", got.ToolCalls[0])
	}
	var traces, logs, metrics string
	for _, item := range got.Missing {
		switch item.Signal {
		case "traces":
			traces = item.Reason
		case "logs":
			logs = item.Reason
		case "metrics":
			metrics = item.Reason
		}
	}
	if !strings.Contains(traces, "logs and metrics only") || logs != "" || metrics != "" {
		t.Fatalf("missing %+v", got.Missing)
	}
}

func TestGrokAPIRequestIsTokenFallbackWhenMetricsAbsent(t *testing.T) {
	session := "ses_docs_grok_api_only"
	model := "openai/gpt-4o"
	report := Summarize([]record.Record{
		grokLog("grok_code.api_request", session, 1, map[string]any{
			"model": model, "input_tokens": 10, "output_tokens": 4,
			"reasoning_tokens": 2, "cache_read_tokens": 1,
		}),
	}, false)
	got := report.Sessions[0]
	if deref(got.TokensSource) != "logs grok_code.api_request" {
		t.Fatalf("source %s", deref(got.TokensSource))
	}
	in, _ := got.Tokens.Get("input_tokens")
	if in != 10 {
		t.Fatalf("%+v", got.Tokens.Vals)
	}
	if got.ComputedCost.Status != "known" {
		t.Fatalf("%+v", got.ComputedCost)
	}
}

func TestGrok46CatalogRowWithOverridesIsUnknownCost(t *testing.T) {
	// Suffix match of grok-4.6 (and live grok-4.7) hits x-ai/grok-4.6 or x-ai/grok-4.7,
	// which have pricing.overrides in the bundled catalog, so computed cost stays unknown.
	session := "ses_docs_grok_46"
	model := "grok-4.6"
	report := Summarize([]record.Record{
		metric(grokTokenMetric, 10, &session, "delta", "sum", map[string]any{"type": "input", "model": model}, 1, 1, "grok-cli"),
		metric(grokTokenMetric, 2, &session, "delta", "sum", map[string]any{"type": "output", "model": model}, 1, 2, "grok-cli"),
	}, false)
	got := report.Sessions[0]
	if got.ComputedCost.Status != "unknown" || !strings.Contains(got.ComputedCost.Reason, "overrides") {
		t.Fatalf("%+v models=%v", got.ComputedCost, got.Models)
	}
	_ = pricing.Bundled()
}

func TestCapturedGrokStartupMetricsWithoutSessionStayUnattributed(t *testing.T) {
	// Shape captured from grok 1.0.92 headless run that failed auth
	// (2026-11-23 Asia/Bangkok). No session.id, no logs, no token metrics.
	rows := []record.Record{
		metric("grok_code.startup.total", 43, nil, "delta", "histogram", map[string]any{
			"auth_mode": "personal", "outcome": "error", "error_kind": "sign_in", "failed_phase": "eager_auth",
		}, 1, 1, "grok-cli"),
		metric("grok_code.startup.phase_duration", 2, nil, "delta", "histogram", map[string]any{
			"auth_mode": "personal", "outcome": "error", "phase": "bootstrap",
		}, 1, 2, "grok-cli"),
		metric("grok_code.startup.subtimer_duration", 10, nil, "delta", "histogram", map[string]any{
			"auth_mode": "personal", "outcome": "cancelled", "phase": "async_http_warmup",
		}, 1, 3, "grok-cli"),
	}
	for i := range rows {
		rows[i].Resource = map[string]any{
			"service.name":             "grok-cli",
			"grok_code.schema.version": "v1",
			"app.entrypoint":           "headless",
			"sdf.fixture":              "captured-unauthenticated-startup",
		}
	}
	report := Summarize(rows, false)
	if len(report.Sessions) != 0 {
		t.Fatalf("sessions %+v", report.Sessions)
	}
	if len(report.Unattributed) != 1 || report.Unattributed[0].Tool != "grok-cli" || report.Unattributed[0].Metrics != 3 {
		t.Fatalf("unattributed %+v", report.Unattributed)
	}
}


func grokUnnamedLog(session string, when int64, attrs map[string]any) record.Record {
	all := map[string]any{"session.id": session}
	for k, v := range attrs {
		all[k] = v
	}
	return record.Record{
		Signal:       "logs",
		ServiceName:  record.Str("grok-cli"),
		SessionID:    record.Str(session),
		TimeUnixNano: record.Int64(when),
		Attributes:   all,
		Resource: map[string]any{
			"service.name":             "grok-cli",
			"grok_code.schema.version": "v1",
			"sdf.fixture":              "live-unnamed-logs",
		},
	}
}

func TestGrokLiveUnnamedLogsInferDurationToolsAndEvents(t *testing.T) {
	// Shape captured from the authenticated grok 1.0.92 session
	// 01a20690-f615-7340-9a7e-42cf29188edf on 2026-11-23: log name, body,
	// and event.name were empty in the stored records. Attribute shapes
	// still identify the events.
	session := "ses_live_unnamed_grok"
	model := "openai/gpt-4o"
	report := Summarize([]record.Record{
		grokUnnamedLog(session, 1_000, map[string]any{
			"event.sequence": 0, "model": model, "permission_mode": "always_approve",
			"hook_count": 0, "is_git_repo": false,
		}),
		grokUnnamedLog(session, 2_000, map[string]any{
			"event.sequence": 3, "model": model,
			"input_tokens": 10, "output_tokens": 4,
			"reasoning_tokens": 2, "cache_read_tokens": 1,
			"cost_usd_micros": 1234,
		}),
		grokUnnamedLog(session, 3_000, map[string]any{
			"event.sequence": 5, "tool_name": "list_dir", "success": true,
			"outcome": "success", "duration_ms": 12, "tool_use_id": "call-1",
		}),
		grokUnnamedLog(session, 4_000, map[string]any{
			"event.sequence": 15, "outcome": "completed", "duration_ms": 9193,
			"tool_call_count": 1, "model": model,
		}),
		grokUnnamedLog(session, 5_000, map[string]any{
			"event.sequence": 17, "duration_secs": 9, "turn_count": 1,
			"tool_call_count": 1, "compaction_count": 0, "model": model,
		}),
	}, false)
	if len(report.Sessions) != 1 {
		t.Fatalf("sessions %+v", report.Sessions)
	}
	got := report.Sessions[0]
	if got.DurationMS == nil || *got.DurationMS != 9000 || deref(got.DurationSource) != "logs grok_code.session_end" {
		t.Fatalf("duration %v %s", got.DurationMS, deref(got.DurationSource))
	}
	if len(got.ToolCalls) != 1 || deref(got.ToolCalls[0].Name) != "list_dir" || deref(got.ToolCalls[0].Success) != "true" {
		t.Fatalf("tools %+v", got.ToolCalls)
	}
	if deref(got.TokensSource) != "logs grok_code.api_request" {
		t.Fatalf("tokens source %s", deref(got.TokensSource))
	}
	if got.AgentReportedCostUSD == nil || *got.AgentReportedCostUSD != 0.001234 {
		t.Fatalf("cost micros fallback %v source=%s", got.AgentReportedCostUSD, deref(got.AgentReportedCostSource))
	}
	if deref(got.AgentReportedCostSource) != "logs grok_code.api_request cost_usd_micros" {
		t.Fatalf("cost source %s", deref(got.AgentReportedCostSource))
	}
	names := map[string]int{}
	for _, line := range got.LogEvents {
		names[line.Name] = line.Count
	}
	for _, want := range []string{
		"grok_code.session_start", "grok_code.api_request", "grok_code.tool_result",
		"grok_code.turn_completed", "grok_code.session_end",
	} {
		if names[want] != 1 {
			t.Fatalf("logs %+v missing %s", got.LogEvents, want)
		}
	}
}

func TestGrokCostUsageMetricIsAgentReportedUSD(t *testing.T) {
	// Live capture exported grok_code.cost.usage in USD (docs had said there
	// was no cost metric). Two delta points 0.014802 + 0.040192 = 0.054994.
	session := "ses_live_grok_cost"
	model := "openai/gpt-4o"
	report := Summarize([]record.Record{
		metric(grokCostMetric, 0.014802, &session, "delta", "sum", map[string]any{"model": model}, 1, 1, "grok-cli"),
		metric(grokCostMetric, 0.040192, &session, "delta", "sum", map[string]any{"model": model}, 2, 2, "grok-cli"),
		metric(grokTokenMetric, 10, &session, "delta", "sum", map[string]any{"type": "input", "model": model}, 1, 3, "grok-cli"),
	}, false)
	got := report.Sessions[0]
	if got.AgentReportedCostUSD == nil || *got.AgentReportedCostUSD != 0.054994 {
		t.Fatalf("agent cost %v", got.AgentReportedCostUSD)
	}
	if deref(got.AgentReportedCostSource) != "metrics grok_code.cost.usage" {
		t.Fatalf("source %s", deref(got.AgentReportedCostSource))
	}
	if got.CostUSD == nil || *got.CostUSD != 0.054994 {
		t.Fatalf("cost_usd %v", got.CostUSD)
	}
}

func TestGrokLiveRequestCostMatchesOverlapPricing(t *testing.T) {
	// First api_request of the live grok-4.7 session, priced at the bundled
	// x-ai/grok-4.7 *base* rates (the catalog row also has overrides, which
	// sdf will not apply to a session total). 4342*0.000002 + 10112*0.0000005
	// + 56*0.000006 + 121*0.000006 = 0.014802, matching cost_usd_micros.
	session := "ses_live_grok_overlap"
	rows := []record.Record{
		grokLog("grok_code.api_request", session, 1, map[string]any{
			"model": "openai/gpt-4o",
			"input_tokens": 14454, "output_tokens": 177,
			"reasoning_tokens": 121, "cache_read_tokens": 10112,
			"cost_usd_micros": 14802,
		}),
	}
	// Use gpt-4o in the named log so computed cost is known; separately check
	// the overlap arithmetic with a direct price of those counts against the
	// same buckets used for grok metrics, via the session computed cost for
	// gpt-4o: (14454-10112)*0.0000025 + 10112*0.00000125 + (177-121)*0.00001 + 121*0.00001
	// = 0.010855 + 0.01264 + 0.00056 + 0.00121 = 0.025265
	report := Summarize(rows, false)
	got := report.Sessions[0]
	if got.ComputedCost.Status != "known" || got.ComputedCost.USD != "0.025265" {
		t.Fatalf("computed %+v", got.ComputedCost)
	}
	if got.AgentReportedCostUSD == nil || *got.AgentReportedCostUSD != 0.014802 {
		t.Fatalf("agent %v", got.AgentReportedCostUSD)
	}
}

func TestGrokStartupWithoutSessionIDStaysUnattributedNextToSession(t *testing.T) {
	session := "ses_live_grok_startup_window"
	model := "openai/gpt-4o"
	rows := []record.Record{
		grokLog("grok_code.session_start", session, 1_000, map[string]any{"model": model, "permission_mode": "default"}),
		grokLog("grok_code.session_end", session, 5_000, map[string]any{"duration_secs": 4, "turn_count": 1, "model": model}),
		metric("grok_code.startup.total", 547, nil, "delta", "histogram", map[string]any{"outcome": "success"}, 2_000, 3, "grok-cli"),
		metric("grok_code.startup.subtimer_duration", 15, nil, "delta", "histogram", map[string]any{"phase": "eager_auth"}, 2_100, 4, "grok-cli"),
	}
	report := Summarize(rows, false)
	if len(report.Sessions) != 1 || report.Sessions[0].Metrics != 0 {
		t.Fatalf("session metrics should exclude startup: %+v", report.Sessions)
	}
	if len(report.Unattributed) != 1 || report.Unattributed[0].Tool != "grok-cli" || report.Unattributed[0].Metrics != 2 {
		t.Fatalf("unattributed %+v", report.Unattributed)
	}
}
