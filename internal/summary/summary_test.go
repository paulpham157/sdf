package summary

import (
	"strings"
	"testing"

	"github.com/paulpham157/sdf/internal/record"
)

func metric(name string, value float64, session *string, temporality, kind string, attributes map[string]any, when, order int64, service string) record.Record {
	attrs := map[string]any{}
	for k, v := range attributes {
		attrs[k] = v
	}
	if session != nil {
		attrs["session.id"] = *session
	}
	if service == "" {
		if strings.HasPrefix(name, "claude_code.") {
			service = "claude-code"
		} else {
			service = "codex-cli"
		}
	}
	return record.Record{
		ID:                order,
		Signal:            "metrics",
		ServiceName:       record.Str(service),
		SessionID:         session,
		Name:              record.Str(name),
		TimeUnixNano:      record.Int64(when),
		Value:             record.Float(value),
		MetricTemporality: record.Str(temporality),
		MetricKind:        record.Str(kind),
		Attributes:        attrs,
		Resource:          map[string]any{},
	}
}

func TestDeltaTokensSumAndCumulativeKeepsLatest(t *testing.T) {
	session := "s"
	delta := Summarize([]record.Record{
		metric("claude_code.token.usage", 10, &session, "delta", "sum", map[string]any{"type": "input"}, 1, 1, ""),
		metric("claude_code.token.usage", 15, &session, "delta", "sum", map[string]any{"type": "input"}, 2, 2, ""),
	}, false).Sessions
	if got, _ := delta[0].Tokens.Get("input"); got != 25 || deref(delta[0].TokensSource) != "metrics claude_code.token.usage" {
		t.Fatalf("%v %s", delta[0].Tokens.Vals, deref(delta[0].TokensSource))
	}
	cumulative := Summarize([]record.Record{
		metric("claude_code.token.usage", 10, &session, "cumulative", "sum", map[string]any{"type": "input"}, 1, 1, ""),
		metric("claude_code.token.usage", 40, &session, "cumulative", "sum", map[string]any{"type": "input"}, 2, 2, ""),
	}, false).Sessions
	if got, _ := cumulative[0].Tokens.Get("input"); got != 40 {
		t.Fatal(cumulative[0].Tokens.Vals)
	}
}

func TestCodexSSETokensSumEveryResponse(t *testing.T) {
	// Synthetic: turn-a makes two responses, turn-b one. Every response is
	// its own billed call, so all counts add up and turn.id collapses nothing.
	event := func(order int64, turn string, input, output int, when int64) record.Record {
		return record.Record{
			ID:           order,
			Signal:       "logs",
			ServiceName:  record.Str("codex_exec"),
			SessionID:    record.Str("conv-1"),
			Name:         record.Str("codex.sse_event"),
			TimeUnixNano: record.Int64(when),
			Attributes: map[string]any{
				"event.kind":         "response.completed",
				"conversation.id":    "conv-1",
				"model":              "gpt-5.1",
				"turn.id":            turn,
				"input_token_count":  input,
				"output_token_count": output,
			},
			Resource: map[string]any{},
		}
	}
	session := Summarize([]record.Record{
		event(1, "turn-a", 100, 10, 1),
		event(2, "turn-a", 140, 99, 2),
		event(3, "turn-b", 200, 20, 3),
	}, false).Sessions[0]
	in, _ := session.Tokens.Get("input_token_count")
	out, _ := session.Tokens.Get("output_token_count")
	if in != 440 || out != 129 || !strings.Contains(deref(session.TokensMethod), "summed") || !strings.HasPrefix(deref(session.TokensSource), "logs codex.sse_event") {
		t.Fatalf("%v %s %s", session.Tokens.Vals, deref(session.TokensSource), deref(session.TokensMethod))
	}
}

// codexRealResponse is one codex.sse_event response.completed log as Codex
// CLI 0.171.0 exported it (attribute set and value types as received; Codex
// sends some counts as strings). No turn.id and no response id.
func codexRealResponse(id int64, stamp, input string, cached int, output, total string) record.Record {
	return record.Record{
		ID:           id,
		Signal:       "logs",
		ServiceName:  record.Str("codex_exec"),
		SessionID:    record.Str("01a207b5-8611-7031-a2e8-98e3a768ed2f"),
		Name:         record.Str("codex.sse_event"),
		TimeUnixNano: nil,
		Attributes: map[string]any{
			"app.version":             "0.171.0",
			"cache_write_token_count": 0,
			"cached_token_count":      cached,
			"conversation.id":         "01a207b5-8611-7031-a2e8-98e3a768ed2f",
			"event.kind":              "response.completed",
			"event.name":              "codex.sse_event",
			"event.timestamp":         stamp,
			"input_token_count":       input,
			"model":                   "mistralai/mistral-nemo",
			"originator":              "codex_exec",
			"output_token_count":      output,
			"reasoning_token_count":   0,
			"slug":                    "mistralai/mistral-nemo",
			"tool_token_count":        total,
		},
		Resource: map[string]any{"service.name": "codex_exec"},
	}
}

func TestCodexSSETokensRealOpenRouterCapture(t *testing.T) {
	// Real capture (#204): Codex CLI 0.171.0, mistralai/mistral-nemo on
	// OpenRouter, two turns, four Responses calls, three exec_command tool
	// calls. OpenRouter's per-call usage was 9142/0/48, 9355/9152/22,
	// 9399/0/27, 9504/9408/12 (input/cached/output), totals 37400/18560/109,
	// which is also what Codex's own codex.turn.token_usage reported. The old
	// max rule gave input 9504 and cached 9408.
	events := []record.Record{
		codexRealResponse(1, "2026-11-23T09:21:09.025Z", "9142", 0, "48", "9190"),
		codexRealResponse(2, "2026-11-23T09:21:10.223Z", "9355", 9152, "22", "9377"),
		codexRealResponse(3, "2026-11-23T09:21:12.639Z", "9399", 0, "27", "9426"),
		codexRealResponse(4, "2026-11-23T09:21:13.590Z", "9504", 9408, "12", "9516"),
	}
	// The same export delivered twice must not double the totals.
	withDuplicate := append(append([]record.Record{}, events...), codexRealResponse(5, "2026-11-23T09:21:13.590Z", "9504", 9408, "12", "9516"))
	for name, records := range map[string][]record.Record{"once": events, "duplicate": withDuplicate} {
		session := Summarize(records, false).Sessions[0]
		want := map[string]float64{
			"input_token_count":       37400,
			"cached_token_count":      18560,
			"cache_write_token_count": 0,
			"output_token_count":      109,
			"reasoning_token_count":   0,
		}
		for key, value := range want {
			if got, ok := session.Tokens.Get(key); !ok || got != value {
				t.Fatalf("%s: %s = %v (ok=%v), want %v; all %v", name, key, got, ok, value, session.Tokens.Vals)
			}
		}
	}
}

func TestLogFallbackUsesCodexSSEAndTurnCost(t *testing.T) {
	sessions := Summarize([]record.Record{
		{
			ID:           1,
			Signal:       "logs",
			ServiceName:  record.Str("codex-cli"),
			SessionID:    record.Str("conv-1"),
			Name:         record.Str("codex.sse_event"),
			TimeUnixNano: record.Int64(1_000_000_000),
			Attributes: map[string]any{
				"event.name":         "codex.sse_event",
				"event.kind":         "response.completed",
				"conversation.id":    "conv-1",
				"model":              "gpt-5.1",
				"input_token_count":  7,
				"output_token_count": 1,
			},
			Resource: map[string]any{},
		},
		{
			ID:           2,
			Signal:       "logs",
			ServiceName:  record.Str("codex-cli"),
			SessionID:    record.Str("conv-1"),
			Name:         record.Str("codex.turn_cost"),
			TimeUnixNano: record.Int64(6_000_000_000),
			Attributes: map[string]any{
				"event.name":          "codex.turn_cost",
				"conversation.id":     "conv-1",
				"usage.estimated_usd": "0.25",
			},
			Resource: map[string]any{},
		},
	}, false).Sessions
	if len(sessions) != 1 {
		t.Fatal(len(sessions))
	}
	session := sessions[0]
	in, _ := session.Tokens.Get("input_token_count")
	out, _ := session.Tokens.Get("output_token_count")
	if session.Tool != "codex" || strings.Join(session.Models, ",") != "gpt-5.1" || in != 7 || out != 1 {
		t.Fatalf("%+v", session)
	}
	if deref(session.TokensSource) != "logs codex.sse_event response.completed" {
		t.Fatal(deref(session.TokensSource))
	}
	if session.CostUSD == nil || *session.CostUSD != 0.25 || deref(session.CostSource) != "logs codex.turn_cost usage.estimated_usd" {
		t.Fatalf("cost %v %s", session.CostUSD, deref(session.CostSource))
	}
	if session.DurationMS == nil || *session.DurationMS != 5000 || deref(session.DurationSource) != "observed_window" {
		t.Fatalf("duration %v %s", session.DurationMS, deref(session.DurationSource))
	}
	if len(session.ToolCalls) != 0 {
		t.Fatal(session.ToolCalls)
	}
	missing := map[string]bool{}
	for _, item := range session.Missing {
		missing[item.Signal] = true
	}
	if !missing["traces"] || !missing["metrics"] || len(missing) != 2 {
		t.Fatalf("%+v", session.Missing)
	}
}

func TestUnscopedMetricsAreUnattributed(t *testing.T) {
	report := Summarize([]record.Record{
		metric("codex.turn.token_usage", 12, nil, "delta", "histogram", map[string]any{"token_type": "input"}, 1, 1, ""),
		metric("codex.websocket.event", 3, nil, "delta", "sum", map[string]any{"kind": "sample"}, 1, 2, ""),
	}, false)
	if len(report.Sessions) != 0 || len(report.Unattributed) != 1 {
		t.Fatalf("%+v", report)
	}
	group := report.Unattributed[0]
	if group.Tool != "codex" || group.Metrics != 2 {
		t.Fatalf("%+v", group)
	}
	found := false
	for _, name := range group.Names {
		if strings.HasPrefix(name, "codex.turn.token_usage=12") {
			found = true
		}
	}
	if !found {
		t.Fatal(group.Names)
	}
}

func TestTextMarksMissingSignals(t *testing.T) {
	text := Format(Summarize([]record.Record{{
		Signal:       "logs",
		ServiceName:  record.Str("claude-code"),
		SessionID:    record.Str("only-a-prompt"),
		Name:         record.Str("claude_code.user_prompt"),
		TimeUnixNano: record.Int64(1),
		Attributes:   map[string]any{"event.name": "user_prompt"},
		Resource:     map[string]any{},
	}}, false))
	for _, want := range []string{
		"session only-a-prompt",
		"tool: claude-code",
		"signals: traces=0 logs=1 metrics=0",
		"cost_usd: (not reported)",
		"tokens: (not reported)",
		"(none reported)",
		"missing:",
		"traces:",
		"metrics:",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in %s", want, text)
		}
	}
}

func TestEmptyStore(t *testing.T) {
	if got := Format(Report{}); got != "No telemetry sessions in the store.\n" {
		t.Fatalf("%q", got)
	}
}

func TestEmptyStoreWithBudgetStillPrintsTheBudget(t *testing.T) {
	report := Report{
		Budget: &BudgetStatus{
			Status:     "unknown",
			Reason:     "no sessions",
			CeilingUSD: "5",
			Window:     "store",
		},
	}
	got := Format(report)
	want := "No telemetry sessions in the store.\n" +
		"budget: " + formatBudget(report.Budget) + "\n"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}
