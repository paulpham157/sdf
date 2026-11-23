package summary

import (
	"strings"
	"testing"

	"github.com/paulpham157/sdf/internal/record"
)

// Since #191 a cumulative token series adds up the latest point of each
// process run. The method text must say so, not "keeps its latest point".
func TestGeminiCumulativeTokenMethodNamesEachProcessRun(t *testing.T) {
	session := "s"
	var recs []record.Record
	for i, p := range []struct {
		value       float64
		start, when int64
	}{{1000, 1, 10}, {500, 30, 40}} {
		rec := metric("gemini_cli.token.usage", p.value, &session, "cumulative", "sum", map[string]any{"type": "input"}, p.when, int64(i+1), "")
		rec.StartUnixNano = record.Int64(p.start)
		rec.Resource = map[string]any{"service.name": "gemini-cli"}
		recs = append(recs, rec)
	}
	sessions := Summarize(recs, false).Sessions
	if len(sessions) != 1 {
		t.Fatalf("sessions %d", len(sessions))
	}
	if got, _ := sessions[0].Tokens.Get("input"); got != 1500 {
		t.Fatalf("input %v, want 1500 (1000 then a resumed run of 500)", got)
	}
	method := deref(sessions[0].TokensMethod)
	if !strings.Contains(method, "latest point of each process run") || strings.Contains(method, "keeps its latest point") {
		t.Fatalf("method %q", method)
	}
}
