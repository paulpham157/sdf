package summary

import (
	"strings"
	"testing"

	"github.com/paulpham157/sdf/internal/pricing"
	"github.com/paulpham157/sdf/internal/record"
)

const sec = int64(1_000_000_000)

func topAgent(trace, id string, start, end int64, attrs map[string]any) record.Record {
	all := map[string]any{"gen_ai.agent.id": "github.copilot.default", "server.address": "api.githubcopilot.com", "server.port": 443}
	for k, v := range attrs {
		all[k] = v
	}
	return copilotSpan(trace, id, "", "invoke_agent", start, end, all)
}

func TestCopilotParallelTopLevelRunsUseTheUnionOfIntervals(t *testing.T) {
	rows := []record.Record{
		topAgent("t1", "a1", 0, 10*sec, nil),
		topAgent("t2", "a2", 5*sec, 15*sec, nil),
		topAgent("t3", "a3", 20*sec, 22*sec, nil),
	}
	s := copilotSessionOf(t, Summarize(rows, false))
	if s.DurationMS == nil || *s.DurationMS != 17000 {
		t.Fatalf("duration %v, want 15 s + 2 s", s.DurationMS)
	}
}

func TestCopilotSubagentWithoutServerAddressIsNotTopLevel(t *testing.T) {
	// The subagent's parent link is missing (its parent span was not
	// exported), so only the server.address marker tells it apart.
	root := topAgent("t1", "a1", 0, 10*sec, map[string]any{"gen_ai.usage.input_tokens": 900, "github.copilot.nano_aiu": 250_000_000})
	sub := copilotSpan("t1", "c1", "gone", "invoke_agent explore", 2*sec, 12*sec, map[string]any{
		"gen_ai.agent.id": "github.copilot.explore", "gen_ai.usage.input_tokens": 300, "github.copilot.nano_aiu": 90_000_000,
	})
	s := copilotSessionOf(t, Summarize([]record.Record{root, sub}, false))
	if s.Tokens.Vals["input_tokens"] != 900 {
		t.Fatalf("subagent tokens added: %v", s.Tokens)
	}
	if s.DurationMS == nil || *s.DurationMS != 10000 {
		t.Fatalf("duration %v", s.DurationMS)
	}
	for _, m := range s.Missing {
		if m.Signal == "agent cost" && !strings.Contains(m.Reason, "0.25 AIU") {
			t.Fatalf("AIU: %s", m.Reason)
		}
	}
}

func TestCopilotFleetWorkflowSubagentsCountOnce(t *testing.T) {
	// A standalone fleet run: invoke_workflow with two parallel subagents.
	wf := copilotSpan("t1", "w1", "", "invoke_workflow fleet", 0, 20*sec, nil)
	a := copilotSpan("t1", "s1", "w1", "invoke_agent task", 1*sec, 12*sec, map[string]any{
		"gen_ai.agent.id": "github.copilot.task", "gen_ai.usage.input_tokens": 100, "gen_ai.usage.output_tokens": 10, "github.copilot.nano_aiu": 100_000_000,
	})
	b := copilotSpan("t1", "s2", "w1", "invoke_agent task", 3*sec, 18*sec, map[string]any{
		"gen_ai.agent.id": "github.copilot.task", "gen_ai.usage.input_tokens": 200, "gen_ai.usage.output_tokens": 20, "github.copilot.nano_aiu": 200_000_000,
	})
	// A subagent nested inside a fleet subagent is not added again.
	nested := copilotSpan("t1", "s3", "s1", "invoke_agent explore", 2*sec, 4*sec, map[string]any{
		"gen_ai.agent.id": "github.copilot.explore", "gen_ai.usage.input_tokens": 999,
	})
	s := copilotSessionOf(t, Summarize([]record.Record{wf, a, b, nested}, false))
	if s.Tokens.Vals["input_tokens"] != 300 || s.Tokens.Vals["output_tokens"] != 30 {
		t.Fatalf("tokens %v", s.Tokens)
	}
	if s.DurationMS == nil || *s.DurationMS != 20000 {
		t.Fatalf("duration %v, want the workflow's 20 s", s.DurationMS)
	}
	for _, m := range s.Missing {
		if m.Signal == "agent cost" && !strings.Contains(m.Reason, "0.3 AIU") {
			t.Fatalf("AIU: %s", m.Reason)
		}
	}

	// The same workflow inside a top-level invoke_agent: the parent's totals
	// already include its subagents.
	root := topAgent("t2", "r1", 0, 25*sec, map[string]any{"gen_ai.usage.input_tokens": 1000})
	wf2 := copilotSpan("t2", "w2", "r1", "invoke_workflow fleet", 1*sec, 20*sec, nil)
	c := copilotSpan("t2", "s4", "w2", "invoke_agent task", 2*sec, 10*sec, map[string]any{"gen_ai.usage.input_tokens": 400})
	s = copilotSessionOf(t, Summarize([]record.Record{root, wf2, c}, false))
	if s.Tokens.Vals["input_tokens"] != 1000 {
		t.Fatalf("tokens %v", s.Tokens)
	}
	if s.DurationMS == nil || *s.DurationMS != 25000 {
		t.Fatalf("duration %v", s.DurationMS)
	}
}

func TestCopilotLegacyUsageNames(t *testing.T) {
	chat := copilotSpan("t1", "c1", "a1", "chat gpt-5", 1*sec, 2*sec, map[string]any{
		"gen_ai.usage.input_tokens": 500, "gen_ai.usage.output_tokens": 50,
		"gen_ai.usage.cache_read_input_tokens": 200, "gen_ai.usage.cache_creation_input_tokens": 30,
		"gen_ai.usage.reasoning_output_tokens": 7,
	})
	// Both the current and an older name on one span: read once.
	both := copilotSpan("t1", "c2", "a1", "chat gpt-5", 2*sec, 3*sec, map[string]any{
		"gen_ai.usage.input_tokens": 100, "gen_ai.usage.cache_write.input_tokens": 10, "gen_ai.usage.cache_creation.input_tokens": 10,
	})
	s := copilotSessionOf(t, Summarize([]record.Record{topAgent("t1", "a1", 0, 4*sec, nil), chat, both}, false))
	want := map[string]float64{"input_tokens": 600, "output_tokens": 50, "cache_read.input_tokens": 200, "cache_write.input_tokens": 40, "reasoning.output_tokens": 7}
	for k, v := range want {
		if s.Tokens.Vals[k] != v {
			t.Fatalf("%s = %v, want %v (%v)", k, s.Tokens.Vals[k], v, s.Tokens)
		}
	}
}

// Copilot CLI bills premium requests and AI units, not tokens, so sdf never
// prices its token counts. This fails if internal/pricing gains a rule for
// a Copilot token source; change it only if that is intended.
func TestCopilotIsNeverPricedPerToken(t *testing.T) {
	cat := pricing.Bundled()
	if cat == nil || cat.Models["openai/gpt-5"] == nil {
		t.Skip("bundled catalog has no openai/gpt-5")
	}
	usage := map[string]any{"gen_ai.request.model": "gpt-5", "gen_ai.usage.input_tokens": 1000, "gen_ai.usage.output_tokens": 100}
	metric := func(name string, v float64) record.Record {
		return record.Record{
			Signal: "metrics", ServiceName: record.Str("github-copilot"), Name: record.Str(name),
			MetricKind: record.Str("sum"), MetricTemporality: record.Str("cumulative"), Value: record.Float(v),
			StartUnixNano: record.Int64(sec), TimeUnixNano: record.Int64(3 * sec),
			Attributes: map[string]any{"gen_ai.request.model": "gpt-5"}, Resource: map[string]any{"service.name": "github-copilot"},
		}
	}
	cases := map[string][]record.Record{
		"traces chat github-copilot": {
			topAgent("t1", "a1", 0, 4*sec, map[string]any{"gen_ai.request.model": "gpt-5"}),
			copilotSpan("t1", "c1", "a1", "chat gpt-5", sec, 2*sec, usage),
		},
		"traces invoke_agent github-copilot": {topAgent("t1", "a1", 0, 4*sec, usage)},
		"metrics gen_ai.client.inference.usage github-copilot": {
			topAgent("t1", "a1", 0, 4*sec, map[string]any{"gen_ai.request.model": "gpt-5"}),
			metric("gen_ai.client.inference.usage.input_tokens", 1000),
			metric("gen_ai.client.inference.usage.output_tokens", 100),
		},
	}
	for source, rows := range cases {
		s := copilotSessionOf(t, SummarizeWith(rows, false, cat))
		if deref(s.TokensSource) != source {
			t.Fatalf("source %q, want %q", deref(s.TokensSource), source)
		}
		if s.ComputedCost == nil || s.ComputedCost.Status != "unknown" || s.ComputedCost.USD != "" {
			t.Fatalf("%s: Copilot tokens were priced: %#v", source, s.ComputedCost)
		}
		if !strings.Contains(s.ComputedCost.Reason, "no pricing rule") {
			t.Fatalf("%s: unknown for another reason: %q", source, s.ComputedCost.Reason)
		}
	}
}
