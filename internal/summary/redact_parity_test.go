package summary

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/paulpham157/sdf/internal/otlp"
	"github.com/paulpham157/sdf/internal/record"
	"github.com/paulpham157/sdf/internal/samplepath"
)

// parityView is what the summary must keep when content is dropped at
// ingest: tokens and their source, cost, duration, models, tool calls,
// error status, repository facts, and record counts.
type parityView struct {
	Tokens       OrderedFloats
	Source       string
	Duration     *float64
	Models       []string
	Cost         *float64
	Computed     any
	ToolCalls    []ToolCall
	Errors       []string
	Commits      []string
	Counts       [3]int
	Unattributed int
}

func parityOf(rows []record.Record) map[string]parityView {
	r := Summarize(rows, false)
	out := map[string]parityView{}
	for _, s := range r.Sessions {
		v := parityView{Tokens: s.Tokens, Source: deref(s.TokensSource), Duration: s.DurationMS, Models: s.Models,
			Cost: s.CostUSD, Computed: s.ComputedCost, ToolCalls: s.ToolCalls, Commits: s.Repository.CommitSHAs,
			Counts: [3]int{s.Traces, s.Logs, s.Metrics}}
		var walk func([]SpanNode)
		walk = func(nodes []SpanNode) {
			for _, n := range nodes {
				v.Errors = append(v.Errors, n.Name+"="+n.StatusCode)
				walk(n.Children)
			}
		}
		walk(s.Spans)
		out[s.Tool+"/"+s.SessionID] = v
	}
	for _, u := range r.Unattributed {
		out["unattributed/"+u.Tool] = parityView{Unattributed: u.Traces + u.Logs + u.Metrics}
	}
	return out
}

func allSampleRows(t *testing.T) []record.Record {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(samplepath.Dir(), "*.json"))
	if err != nil || len(files) == 0 {
		t.Fatalf("samples: %v", err)
	}
	var all []record.Record
	for _, f := range files {
		signal := "logs"
		switch {
		case strings.HasSuffix(f, "_traces.json"):
			signal = "traces"
		case strings.HasSuffix(f, "_metrics.json"):
			signal = "metrics"
		}
		body, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		recs, err := otlp.Decode(signal, body, "application/json")
		if err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		all = append(all, recs...)
	}
	all = append(all, claudeFixture()...)
	all = append(all, codexFixture()...)
	all = append(all, grokLiveSpans(t)...)
	for i := range all {
		all[i].ID = int64(i + 1)
	}
	return all
}

// Dropping content at ingest (record.Redact, the default) must not change
// any summary number, tool, model, error status, or commit.
func TestRedactKeepsEverySummary(t *testing.T) {
	rows := allSampleRows(t)
	raw, red := parityOf(rows), parityOf(record.Redact(rows))
	if len(raw) < 10 || len(raw) != len(red) {
		t.Fatalf("sessions raw %d redacted %d", len(raw), len(red))
	}
	for key, a := range raw {
		ja, _ := json.Marshal(a)
		jb, _ := json.Marshal(red[key])
		if string(ja) != string(jb) {
			t.Errorf("%s changed:\nraw      %s\nredacted %s", key, ja, jb)
		}
	}
	commits := 0
	for _, v := range red {
		commits += len(v.Commits)
	}
	if commits == 0 {
		t.Fatal("no commit in the samples survives; the git_commit_id copy is untested")
	}
}
