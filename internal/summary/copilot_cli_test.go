package summary

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/paulpham157/sdf/internal/record"
	"github.com/paulpham157/sdf/internal/samplepath"
)

// Copilot CLI fixtures are derived from the GitHub Copilot CLI OpenTelemetry
// reference on docs.github.com, `copilot help monitoring` from
// @github/copilot 1.0.114, and the copilot-cli changelog. They were not
// captured from a live session (a live run needs a Copilot subscription).

const copilotSession = "sample-copilot-1"

func copilotRows(t *testing.T) []record.Record {
	t.Helper()
	rows := loadSignal(t, "traces", "copilot_traces.json")
	return append(rows, loadSignal(t, "metrics", "copilot_metrics.json")...)
}

func copilotSessionOf(t *testing.T, report Report) SessionSummary {
	t.Helper()
	for _, s := range report.Sessions {
		if s.SessionID == copilotSession {
			return s
		}
	}
	t.Fatalf("no session %s in %#v", copilotSession, report.Sessions)
	return SessionSummary{}
}

func copilotSpan(trace, id, parent, name string, start, end int64, attrs map[string]any) record.Record {
	all := map[string]any{"gen_ai.conversation.id": copilotSession}
	for k, v := range attrs {
		all[k] = v
	}
	rec := record.Record{
		Signal:        "traces",
		ServiceName:   record.Str("github-copilot"),
		SessionID:     record.Str(copilotSession),
		TraceID:       record.Str(trace),
		SpanID:        record.Str(id),
		Name:          record.Str(name),
		StartUnixNano: record.Int64(start),
		EndUnixNano:   record.Int64(end),
		Attributes:    all,
		Resource:      map[string]any{"service.name": "github-copilot"},
		StatusCode:    "UNSET",
	}
	if parent != "" {
		rec.ParentSpanID = record.Str(parent)
	}
	return rec
}

func TestToolOfRecognizesCopilotCLI(t *testing.T) {
	if ToolOf(record.Record{ServiceName: record.Str("github-copilot")}) != "copilot-cli" {
		t.Fatal("service.name")
	}
	if ToolOf(record.Record{Name: record.Str("github.copilot.tool.call.count")}) != "copilot-cli" {
		t.Fatal("metric name")
	}
	// OTEL_SERVICE_NAME can rename the service; the agent id still identifies it.
	if ToolOf(record.Record{ServiceName: record.Str("my-copilot"), Attributes: map[string]any{"gen_ai.agent.id": "github.copilot.default"}}) != "copilot-cli" {
		t.Fatal("gen_ai.agent.id")
	}
	if ToolOf(record.Record{ServiceName: record.Str("other"), Name: record.Str("invoke_agent")}) == "copilot-cli" {
		t.Fatal("a bare invoke_agent span is not Copilot")
	}
}

func TestCopilotSamplesAreMarkedAndHaveNoContent(t *testing.T) {
	for _, name := range []string{"copilot_traces.json", "copilot_metrics.json"} {
		body, err := os.ReadFile(filepath.Join(samplepath.Dir(), name))
		if err != nil {
			t.Fatal(err)
		}
		text := string(body)
		if !strings.Contains(text, `"sdf.fixture"`) || !strings.Contains(text, `"stringValue": "sample"`) {
			t.Fatal(name, "not marked sdf.fixture=sample")
		}
		for _, banned := range []string{"gen_ai.input.messages", "gen_ai.output.messages", "gen_ai.system_instructions", "gen_ai.tool.call.arguments", "gen_ai.tool.call.result"} {
			if strings.Contains(text, banned) {
				t.Fatal(name, "has content attribute", banned)
			}
		}
	}
}

func TestCopilotSampleSessionTokensToolsDurationAndCost(t *testing.T) {
	report := Summarize(copilotRows(t), false)
	if len(report.Unattributed) != 0 {
		t.Fatalf("unattributed %#v", report.Unattributed)
	}
	s := copilotSessionOf(t, report)
	if s.Tool != "copilot-cli" || len(s.ServiceNames) != 1 || s.ServiceNames[0] != "github-copilot" {
		t.Fatalf("tool %q services %v", s.Tool, s.ServiceNames)
	}
	if len(s.Models) != 1 || s.Models[0] != "claude-sonnet-4.5" {
		t.Fatalf("models %v", s.Models)
	}
	if s.Traces != 13 || s.Logs != 0 || s.Metrics != 11 {
		t.Fatalf("signals %d %d %d", s.Traces, s.Logs, s.Metrics)
	}
	// Five chat spans, one inside the explore subagent. The invoke_agent
	// totals and the metric counters repeat these numbers and are not added.
	want := map[string]float64{
		"input_tokens": 63600, "output_tokens": 990, "cache_read.input_tokens": 41300,
		"cache_write.input_tokens": 11800, "reasoning.output_tokens": 96,
	}
	if s.Tokens.Len() != len(want) {
		t.Fatalf("tokens %v", s.Tokens)
	}
	for k, v := range want {
		if s.Tokens.Vals[k] != v {
			t.Fatalf("tokens[%s]=%v want %v (%v)", k, s.Tokens.Vals[k], v, s.Tokens)
		}
	}
	if deref(s.TokensSource) != "traces chat github-copilot" {
		t.Fatalf("tokens source %q", deref(s.TokensSource))
	}
	// Two top-level invoke_agent spans: 15.8 s + 13.6 s. The nested explore
	// subagent (5.35 s) is inside the first and is not added.
	if s.DurationMS == nil || *s.DurationMS != 29400 || deref(s.DurationSource) != "traces invoke_agent (top-level, github-copilot)" {
		t.Fatalf("duration %v %q", s.DurationMS, deref(s.DurationSource))
	}
	if s.CostUSD != nil || s.AgentReportedCostUSD != nil {
		t.Fatalf("Copilot CLI reports no USD cost, got %v %v", s.CostUSD, s.AgentReportedCostUSD)
	}
	if s.ComputedCost == nil || s.ComputedCost.Status != "unknown" || s.ComputedCost.USD != "" {
		t.Fatalf("computed cost should be unknown: %#v", s.ComputedCost)
	}
	foundCost, foundLogs := false, false
	for _, m := range s.Missing {
		switch m.Signal {
		case "agent cost":
			foundCost = strings.Contains(m.Reason, "1.9 AIU") && strings.Contains(m.Reason, "not money")
		case "logs":
			foundLogs = strings.Contains(m.Reason, "no OTLP log signal")
		default:
			t.Fatalf("unexpected missing %#v", m)
		}
	}
	if !foundCost || !foundLogs {
		t.Fatalf("missing %#v", s.Missing)
	}
	type call struct {
		name, success string
		ms            float64
	}
	wantCalls := []call{{"view", "true", 50}, {"task", "true", 5500}, {"grep", "true", 120}, {"edit", "true", 200}, {"bash", "false", 4500}}
	if len(s.ToolCalls) != len(wantCalls) {
		t.Fatalf("tool calls %#v", s.ToolCalls)
	}
	for i, w := range wantCalls {
		got := s.ToolCalls[i]
		if deref(got.Name) != w.name || deref(got.Success) != w.success || got.DurationMS == nil || *got.DurationMS != w.ms || got.Source != "traces execute_tool "+w.name {
			t.Fatalf("call %d = %+v (name %q success %q)", i, got, deref(got.Name), deref(got.Success))
		}
	}
}

func TestCopilotTokensFallBackToInvokeAgentThenMetrics(t *testing.T) {
	const s1 = int64(1_000_000_000)
	root := copilotSpan("t1", "a1", "", "invoke_agent", s1, 5*s1, map[string]any{
		"gen_ai.agent.id": "github.copilot.default", "gen_ai.usage.input_tokens": 900, "gen_ai.usage.output_tokens": 40,
		"gen_ai.usage.cache_read.input_tokens": 512, "github.copilot.nano_aiu": 250_000_000,
	})
	tool := copilotSpan("t1", "b1", "a1", "execute_tool task", 2*s1, 4*s1, nil)
	sub := copilotSpan("t1", "c1", "b1", "invoke_agent explore", 2*s1, 3*s1, map[string]any{
		"gen_ai.agent.id": "github.copilot.explore", "gen_ai.usage.input_tokens": 300, "gen_ai.usage.output_tokens": 10,
		"github.copilot.nano_aiu": 90_000_000,
	})
	s := copilotSessionOf(t, Summarize([]record.Record{root, tool, sub}, false))
	if deref(s.TokensSource) != "traces invoke_agent github-copilot" {
		t.Fatalf("source %q", deref(s.TokensSource))
	}
	if s.Tokens.Vals["input_tokens"] != 900 || s.Tokens.Vals["output_tokens"] != 40 || s.Tokens.Vals["cache_read.input_tokens"] != 512 {
		t.Fatalf("subagent invoke_agent must not be added: %v", s.Tokens)
	}
	if s.DurationMS == nil || *s.DurationMS != 4000 {
		t.Fatalf("duration %v", s.DurationMS)
	}
	for _, m := range s.Missing {
		if m.Signal == "agent cost" && !strings.Contains(m.Reason, "0.25 AIU") {
			t.Fatalf("AIU must come from the top-level span only: %s", m.Reason)
		}
	}

	// No usage on spans: the inference usage counters are used, and the
	// legacy cache_creation name maps to cache_write.
	bare := copilotSpan("t2", "d1", "", "invoke_agent", s1, 5*s1, map[string]any{"gen_ai.agent.id": "github.copilot.default"})
	metric := func(name string, v float64) record.Record {
		return record.Record{
			Signal: "metrics", ServiceName: record.Str("github-copilot"), Name: record.Str(name),
			MetricKind: record.Str("sum"), MetricTemporality: record.Str("cumulative"), Value: record.Float(v),
			StartUnixNano: record.Int64(s1), TimeUnixNano: record.Int64(4 * s1),
			Attributes: map[string]any{"gen_ai.request.model": "gpt-5"}, Resource: map[string]any{"service.name": "github-copilot"},
		}
	}
	rows := []record.Record{bare,
		metric("gen_ai.client.inference.usage.input_tokens", 700),
		metric("gen_ai.client.inference.usage.output_tokens", 30),
		metric("gen_ai.client.inference.usage.cache_creation.input_tokens", 650),
	}
	s = copilotSessionOf(t, Summarize(rows, false))
	if deref(s.TokensSource) != "metrics gen_ai.client.inference.usage github-copilot" {
		t.Fatalf("source %q (%v)", deref(s.TokensSource), s.Tokens)
	}
	if s.Tokens.Vals["input_tokens"] != 700 || s.Tokens.Vals["output_tokens"] != 30 || s.Tokens.Vals["cache_write.input_tokens"] != 650 {
		t.Fatalf("tokens %v", s.Tokens)
	}
}

func TestCopilotToolSuccessFromErrorType(t *testing.T) {
	ok := copilotSpan("t", "a", "", "execute_tool view", 1, 2, map[string]any{"gen_ai.tool.name": "view"})
	failed := copilotSpan("t", "b", "", "execute_tool bash", 1, 2, map[string]any{"gen_ai.tool.name": "bash", "error.type": "tool_error"})
	status := copilotSpan("t", "c", "", "execute_tool edit", 1, 2, map[string]any{"gen_ai.tool.name": "edit"})
	status.StatusCode = "ERROR"
	for _, c := range []struct {
		rec  record.Record
		want string
	}{{ok, "true"}, {failed, "false"}, {status, "false"}} {
		if got, _ := reportedSuccess(c.rec); deref(got) != c.want {
			t.Fatalf("%s: %q want %q", deref(c.rec.Name), deref(got), c.want)
		}
	}
	// Other tools keep their own rules.
	other := ok
	other.ServiceName = record.Str("someone-else")
	other.Attributes = map[string]any{"gen_ai.tool.name": "view"}
	if got, _ := reportedSuccess(other); deref(got) == "true" {
		t.Fatal("non-Copilot execute_tool without a flag must not be a success")
	}
}
