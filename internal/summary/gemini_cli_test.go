package summary

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/paulpham157/sdf/internal/record"
	"github.com/paulpham157/sdf/internal/samplepath"
)

// Gemini CLI fixtures are derived from the published telemetry docs
// (docs/cli/telemetry.md) and the telemetry source
// (packages/core/src/telemetry) of google-gemini/gemini-cli at
// eecccfcd5aff25de5e78135ca20f7abe3f566955 (0.71.0-nightly). They were not
// captured from a live session.

const geminiSession = "sample-gemini-1"

func geminiRows(t *testing.T) []record.Record {
	t.Helper()
	rows := loadSignal(t, "logs", "gemini_logs.json")
	rows = append(rows, loadSignal(t, "metrics", "gemini_metrics.json")...)
	return append(rows, loadSignal(t, "traces", "gemini_traces.json")...)
}

func geminiSessionOf(t *testing.T, report Report) SessionSummary {
	t.Helper()
	for _, s := range report.Sessions {
		if s.SessionID == geminiSession {
			return s
		}
	}
	t.Fatalf("no session %s in %#v", geminiSession, report.Sessions)
	return SessionSummary{}
}

func geminiLog(name string, when int64, attrs map[string]any) record.Record {
	all := map[string]any{"session.id": geminiSession, "event.name": name}
	for k, v := range attrs {
		all[k] = v
	}
	return record.Record{
		Signal:       "logs",
		ServiceName:  record.Str("gemini-cli"),
		SessionID:    record.Str(geminiSession),
		Name:         record.Str(name),
		Body:         record.Str("API response from gemini-2.5-flash. Status: 200. Duration: 900ms."),
		TimeUnixNano: record.Int64(when),
		Attributes:   all,
		Resource:     map[string]any{"service.name": "gemini-cli", "sdf.fixture": "sample"},
	}
}

func TestToolOfRecognizesGeminiCLI(t *testing.T) {
	if ToolOf(record.Record{ServiceName: record.Str("gemini-cli")}) != "gemini-cli" {
		t.Fatal("service.name")
	}
	if ToolOf(record.Record{Name: record.Str("gemini_cli.token.usage")}) != "gemini-cli" {
		t.Fatal("metric name")
	}
	if ToolOf(record.Record{Attributes: map[string]any{"event.name": "gemini_cli.tool_call"}}) != "gemini-cli" {
		t.Fatal("event.name")
	}
	// Gemini CLI also records gen_ai.client.token.usage. It must not be read as Pi.
	if ToolOf(record.Record{ServiceName: record.Str("gemini-cli"), Name: record.Str("gen_ai.client.token.usage")}) != "gemini-cli" {
		t.Fatal("gen_ai metric from gemini-cli")
	}
}

func TestGeminiSamplesAreMarkedAndHaveNoPromptText(t *testing.T) {
	for _, name := range []string{"gemini_logs.json", "gemini_metrics.json", "gemini_traces.json"} {
		body, err := os.ReadFile(filepath.Join(samplepath.Dir(), name))
		if err != nil {
			t.Fatal(err)
		}
		text := string(body)
		if !strings.Contains(text, `"sdf.fixture"`) || !strings.Contains(text, `"stringValue": "sample"`) {
			t.Fatal(name, "not marked sdf.fixture=sample")
		}
		for _, banned := range []string{`"prompt"`, "request_text", "response_text", "function_args", "user.email", "gen_ai.input.messages", "gen_ai.output.messages", "api_key\"", "authorization"} {
			if strings.Contains(strings.ToLower(text), strings.ToLower(banned)) {
				t.Fatal(name, "contains", banned)
			}
		}
	}
}

func TestGeminiSampleSessionTokensToolsDurationAndCost(t *testing.T) {
	report := Summarize(geminiRows(t), false)
	if len(report.Unattributed) != 0 {
		t.Fatalf("unattributed %#v", report.Unattributed)
	}
	s := geminiSessionOf(t, report)
	if s.Tool != "gemini-cli" || len(s.ServiceNames) != 1 || s.ServiceNames[0] != "gemini-cli" {
		t.Fatalf("tool %q services %v", s.Tool, s.ServiceNames)
	}
	if len(s.Models) != 1 || s.Models[0] != "gemini-2.5-pro" {
		t.Fatalf("models %v", s.Models)
	}
	if s.Traces != 6 || s.Logs != 21 || s.Metrics != 21 {
		t.Fatalf("signals %d %d %d", s.Traces, s.Logs, s.Metrics)
	}
	// Two cumulative exports: the 20 s point is the session total. The first
	// export (after two responses) must not be added to it, and
	// gen_ai.client.token.usage must not be added either.
	want := map[string]float64{"input": 27354, "output": 369, "cache": 20480, "thought": 640, "tool": 0}
	if s.Tokens.Len() != len(want) {
		t.Fatalf("tokens %v", s.Tokens)
	}
	for k, v := range want {
		if s.Tokens.Vals[k] != v {
			t.Fatalf("tokens[%s]=%v want %v (%v)", k, s.Tokens.Vals[k], v, s.Tokens)
		}
	}
	if deref(s.TokensSource) != "metrics gemini_cli.token.usage" {
		t.Fatalf("tokens source %q", deref(s.TokensSource))
	}
	if s.DurationMS == nil || *s.DurationMS != 16000 || deref(s.DurationSource) != "logs and traces gemini_cli window" {
		t.Fatalf("duration %v %q", s.DurationMS, deref(s.DurationSource))
	}
	if s.CostUSD != nil || s.AgentReportedCostUSD != nil {
		t.Fatalf("Gemini CLI reports no cost, got %v", *s.CostUSD)
	}
	if s.ComputedCost == nil || s.ComputedCost.Status != "unknown" || s.ComputedCost.USD != "" {
		t.Fatalf("computed cost should be unknown: %#v", s.ComputedCost)
	}
	foundCost := false
	for _, m := range s.Missing {
		if m.Signal == "agent cost" && strings.Contains(m.Reason, "no cost metric") {
			foundCost = true
		}
		if m.Signal == "traces" || m.Signal == "logs" || m.Signal == "metrics" {
			t.Fatalf("unexpected missing %#v", m)
		}
	}
	if !foundCost {
		t.Fatalf("missing %#v", s.Missing)
	}
	type call struct {
		name, success string
		ms            float64
	}
	wantCalls := []call{{"read_file", "true", 14}, {"replace", "true", 2210}, {"run_shell_command", "false", 830}}
	if len(s.ToolCalls) != len(wantCalls) {
		t.Fatalf("tool calls %#v", s.ToolCalls)
	}
	for i, w := range wantCalls {
		got := s.ToolCalls[i]
		if deref(got.Name) != w.name || deref(got.Success) != w.success || got.DurationMS == nil || *got.DurationMS != w.ms || got.Source != "logs gemini_cli.tool_call" {
			t.Fatalf("call %d = %+v (name %q success %q)", i, got, deref(got.Name), deref(got.Success))
		}
	}
	logs := map[string]int{}
	for _, line := range s.LogEvents {
		logs[line.Name] = line.Count
	}
	if logs["gemini_cli.api_error"] != 1 || logs["gemini_cli.api_response"] != 3 || logs["gemini_cli.tool_call"] != 3 {
		t.Fatalf("log events %#v", s.LogEvents)
	}
}

func TestGeminiTokensFallBackToAPIResponseLogs(t *testing.T) {
	rows := []record.Record{
		geminiLog("gemini_cli.api_response", 1_000, map[string]any{
			"model": "gemini-2.5-flash", "duration_ms": 900, "status_code": 200,
			"input_token_count": 1200, "output_token_count": 80, "cached_content_token_count": 1024,
			"thoughts_token_count": 40, "tool_token_count": 6, "total_token_count": 1326,
		}),
		geminiLog("gemini_cli.api_response", 2_000, map[string]any{
			"model": "gemini-2.5-flash", "duration_ms": 700, "status_code": 200,
			"input_token_count": 1500, "output_token_count": 20, "cached_content_token_count": 0,
			"thoughts_token_count": 0, "tool_token_count": 0, "total_token_count": 1520,
		}),
		// The semantic twin carries the same input/output and is not added.
		geminiLog("gen_ai.client.inference.operation.details", 2_000, map[string]any{
			"gen_ai.request.model": "gemini-2.5-flash", "gen_ai.usage.input_tokens": 1500, "gen_ai.usage.output_tokens": 20,
		}),
	}
	s := geminiSessionOf(t, Summarize(rows, false))
	if deref(s.TokensSource) != "logs gemini_cli.api_response" {
		t.Fatalf("source %q", deref(s.TokensSource))
	}
	want := map[string]float64{
		"input_token_count": 2700, "output_token_count": 100, "cached_content_token_count": 1024,
		"thoughts_token_count": 40, "tool_token_count": 6,
	}
	for k, v := range want {
		if s.Tokens.Vals[k] != v {
			t.Fatalf("tokens[%s]=%v want %v (%v)", k, s.Tokens.Vals[k], v, s.Tokens)
		}
	}
	if _, ok := s.Tokens.Vals["total_token_count"]; ok {
		t.Fatal("total_token_count was added")
	}
	if len(s.Models) != 1 || s.Models[0] != "gemini-2.5-flash" {
		t.Fatalf("models %v", s.Models)
	}
	// sdf has no pricing rule for Gemini token sources yet (internal/pricing
	// is unchanged here), so computed cost is unknown, not zero.
	if s.ComputedCost == nil || s.ComputedCost.Status != "unknown" || !strings.Contains(s.ComputedCost.Reason, `no pricing rule for token source "logs gemini_cli.api_response"`) {
		t.Fatalf("computed cost %#v", s.ComputedCost)
	}
	if s.DurationMS == nil || *s.DurationMS != 0.001 {
		t.Fatalf("duration %v %q", s.DurationMS, deref(s.DurationSource))
	}
}
