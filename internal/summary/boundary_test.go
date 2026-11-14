package summary

import (
	"testing"

	"github.com/paulpham157/sdf/internal/record"
)

// boundary is the log a hook posts. It carries the session id, so it scopes
// the session, and its timestamp is the edge. Stamps are Unix nanoseconds,
// because the inferred window allows two seconds of slack.
func boundary(name, tool, session string, when int64) record.Record {
	return record.Record{
		Signal:       "logs",
		ServiceName:  record.Str(tool),
		SessionID:    record.Str(session),
		Name:         record.Str(name),
		TimeUnixNano: record.Int64(when),
		Attributes:   map[string]any{},
		Resource:     map[string]any{},
	}
}

// loose is a metric with no session id, the kind the time window decides.
func loose(tool string, when, order int64) record.Record {
	return record.Record{
		ID:                order,
		Signal:            "metrics",
		ServiceName:       record.Str(tool),
		Name:              record.Str("codex.turn.token_usage"),
		TimeUnixNano:      record.Int64(when),
		Value:             record.Float(1),
		MetricTemporality: record.Str("delta"),
		MetricKind:        record.Str("sum"),
		Attributes:        map[string]any{},
		Resource:          map[string]any{},
	}
}

const boundarySecond int64 = 1_000_000_000

func TestRecordedBoundaryReplacesTheInferredWindow(t *testing.T) {
	// Telemetry sits at 2s. The session opened at 1s and closed at 3s, so a
	// record at 20s is past the close. The record at 2s is inside.
	report := Summarize([]record.Record{
		boundary("sdf.session.open", "codex", "root", 1*boundarySecond),
		boundary("sdf.session.close", "codex", "root", 3*boundarySecond),
		loose("codex", 2*boundarySecond, 1),
		loose("codex", 20*boundarySecond, 2),
	}, false)
	if len(report.Unattributed) != 1 || report.Unattributed[0].Metrics != 1 {
		t.Fatalf("the record past the close should be the only one left over: %+v", report.Unattributed)
	}
	if len(report.Sessions) != 1 || report.Sessions[0].SessionID != "root" {
		t.Fatalf("%+v", report.Sessions)
	}
}

func TestOpenWithoutCloseKeepsTheWindowOpen(t *testing.T) {
	// No close arrived. A record 30s after the last telemetry still belongs to
	// the session, where the inferred window would have dropped it.
	report := Summarize([]record.Record{
		boundary("sdf.session.open", "codex", "root", 1*boundarySecond),
		loose("codex", 2*boundarySecond, 1),
		loose("codex", 30*boundarySecond, 2),
	}, false)
	if len(report.Unattributed) != 0 {
		t.Fatalf("a session with no close should still take later records: %+v", report.Unattributed)
	}
	if len(report.Sessions) != 1 || report.Sessions[0].SessionID != "root" {
		t.Fatalf("%+v", report.Sessions)
	}
}

func TestNoBoundaryKeepsTheInferredWindow(t *testing.T) {
	// Nothing reported an edge. The slack is two seconds, so a record 20s
	// after the only telemetry stays unattributed, exactly as before.
	root := "root"
	report := Summarize([]record.Record{
		metric("codex.turn.token_usage", 1, &root, "delta", "sum", map[string]any{}, 1*boundarySecond, 1, "codex"),
		loose("codex", 20*boundarySecond, 2),
	}, false)
	if len(report.Unattributed) != 1 {
		t.Fatalf("inferred window changed: unattributed=%d", len(report.Unattributed))
	}
}
