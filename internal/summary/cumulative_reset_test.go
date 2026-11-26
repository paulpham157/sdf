package summary

import (
	"testing"

	"github.com/paulpham157/sdf/internal/record"
)

// cum is one cumulative claude_code.token.usage point. start 0 means the
// exporter sent no StartTimeUnixNano. instance, when set, becomes the
// service.instance.id resource attribute.
type cum struct {
	value    float64
	start    int64
	when     int64
	order    int64
	typ      string
	instance string
}

func cumulativeRecords(points ...cum) []record.Record {
	session := "s"
	var out []record.Record
	for _, p := range points {
		typ := p.typ
		if typ == "" {
			typ = "input"
		}
		rec := metric("claude_code.token.usage", p.value, &session, "cumulative", "sum", map[string]any{"type": typ}, p.when, p.order, "")
		if p.start != 0 {
			rec.StartUnixNano = record.Int64(p.start)
		}
		if p.instance != "" {
			rec.Resource = map[string]any{"service.instance.id": p.instance}
		}
		out = append(out, rec)
	}
	return out
}

func cumulativeTokens(t *testing.T, typ string, points ...cum) float64 {
	t.Helper()
	sessions := Summarize(cumulativeRecords(points...), false).Sessions
	if len(sessions) != 1 {
		t.Fatalf("sessions %d", len(sessions))
	}
	got, ok := sessions[0].Tokens.Get(typ)
	if !ok {
		t.Fatalf("no %s tokens: %v", typ, sessions[0].Tokens.Vals)
	}
	return got
}

func TestCumulativeResetByStartTimeSumsEachRun(t *testing.T) {
	got := cumulativeTokens(t, "input",
		cum{value: 600, start: 1, when: 10, order: 1},
		cum{value: 1000, start: 1, when: 20, order: 2},
		cum{value: 200, start: 30, when: 40, order: 3},
		cum{value: 500, start: 30, when: 50, order: 4},
	)
	if got != 1500 {
		t.Fatalf("got %v, want 1500", got)
	}
}

func TestCumulativeResetByValueDropWithoutStartTime(t *testing.T) {
	got := cumulativeTokens(t, "input",
		cum{value: 1000, when: 10, order: 1},
		cum{value: 500, when: 20, order: 2},
	)
	if got != 1500 {
		t.Fatalf("got %v, want 1500", got)
	}
}

func TestCumulativeDropWithinTheSameStartTimeIsNotAReset(t *testing.T) {
	// A cumulative UpDownCounter may go down without a restart. Within one
	// start time the latest value is the run's value: 5, 3, 4 is 4, not 9.
	got := cumulativeTokens(t, "input",
		cum{value: 5, start: 5, when: 10, order: 1},
		cum{value: 3, start: 5, when: 20, order: 2},
		cum{value: 4, start: 5, when: 30, order: 3},
	)
	if got != 4 {
		t.Fatalf("got %v, want 4", got)
	}
	drop := cumulativeTokens(t, "input",
		cum{value: 1000, start: 5, when: 10, order: 1},
		cum{value: 500, start: 5, when: 20, order: 2},
	)
	if drop != 500 {
		t.Fatalf("got %v, want 500 (same start time is one run)", drop)
	}
}

func TestCumulativeResourceChangeWithinOneStartTimeIsOneRun(t *testing.T) {
	// The resource gains process.pid mid-run; the start time is unchanged.
	got := cumulativeTokens(t, "input",
		cum{value: 1000, start: 1, when: 10, order: 1, instance: "a"},
		cum{value: 1500, start: 1, when: 20, order: 2, instance: "a pid=7"},
	)
	if got != 1500 {
		t.Fatalf("got %v, want 1500", got)
	}
}

func TestCumulativeMixedStartAndNoStartPoints(t *testing.T) {
	// Only some points carry a start time. A point without one joins the run
	// of the same resource whose start is the latest at or before its time.
	got := cumulativeTokens(t, "input",
		cum{value: 1000, start: 1, when: 10, order: 1},
		cum{value: 1500, when: 20, order: 2},
	)
	if got != 1500 {
		t.Fatalf("got %v, want 1500", got)
	}
	// An older start-time point does not replace a later no-start point.
	got = cumulativeTokens(t, "input",
		cum{value: 1500, when: 20, order: 1},
		cum{value: 1000, start: 1, when: 10, order: 2},
	)
	if got != 1500 {
		t.Fatalf("got %v, want 1500", got)
	}
	// Two runs by start time; the no-start point belongs to the second.
	got = cumulativeTokens(t, "input",
		cum{value: 1000, start: 1, when: 10, order: 1},
		cum{value: 200, start: 30, when: 40, order: 2},
		cum{value: 500, when: 50, order: 3},
	)
	if got != 1500 {
		t.Fatalf("got %v, want 1500", got)
	}
	// A no-start point before every start time is its own run.
	got = cumulativeTokens(t, "input",
		cum{value: 300, when: 5, order: 1},
		cum{value: 1000, start: 10, when: 20, order: 2},
	)
	if got != 1300 {
		t.Fatalf("got %v, want 1300", got)
	}
}

func TestCumulativeSameTimeIsOnePoint(t *testing.T) {
	// Same timestamp, different values: an update of one point, the one
	// stored last wins. Not 1900, and not a reset.
	got := cumulativeTokens(t, "input",
		cum{value: 1000, when: 10, order: 1},
		cum{value: 900, when: 10, order: 2},
	)
	if got != 900 {
		t.Fatalf("no start: got %v, want 900", got)
	}
	got = cumulativeTokens(t, "input",
		cum{value: 900, when: 10, order: 1},
		cum{value: 1000, when: 10, order: 2},
		cum{value: 1200, when: 20, order: 3},
	)
	if got != 1200 {
		t.Fatalf("no start, later point: got %v, want 1200", got)
	}
	got = cumulativeTokens(t, "input",
		cum{value: 1000, start: 1, when: 10, order: 1},
		cum{value: 900, start: 1, when: 10, order: 2},
	)
	if got != 900 {
		t.Fatalf("start: got %v, want 900", got)
	}
}

// E1: process A reports a start time, process B (another resource) does not.
// B's point is not A's run: 300 + 200 = 500. Before, B's later point replaced
// A's run value and the total was 200.
func TestCumulativeE1StartlessPointOfAnotherResourceIsItsOwnRun(t *testing.T) {
	got := cumulativeTokens(t, "input",
		cum{value: 300, start: 1, when: 10, order: 1, instance: "a"},
		cum{value: 200, when: 20, order: 2, instance: "b"},
	)
	if got != 500 {
		t.Fatalf("got %v, want 500", got)
	}
}

// E7: a resume where run 2 has no start time. Run 1 (resource a, start 1)
// ends at 1000; run 2 is a new process (resource b) without a start time
// that reaches 500. 1000 + 500 = 1500; before, run 2 joined run 1 and the
// total was 500.
func TestCumulativeE7ResumeWithoutStartTimeIsANewRun(t *testing.T) {
	got := cumulativeTokens(t, "input",
		cum{value: 600, start: 1, when: 10, order: 1, instance: "a"},
		cum{value: 1000, start: 1, when: 20, order: 2, instance: "a"},
		cum{value: 200, when: 40, order: 3, instance: "b"},
		cum{value: 500, when: 50, order: 4, instance: "b"},
	)
	if got != 1500 {
		t.Fatalf("got %v, want 1500", got)
	}
}

// E3b: two concurrent processes interleave; a has a start time, b does not.
// Each ends at 650, so 650 + 650 = 1300; before, b's points joined a's run
// and the total was 650.
func TestCumulativeE3bConcurrentStartAndStartlessProcesses(t *testing.T) {
	got := cumulativeTokens(t, "input",
		cum{value: 100, start: 1, when: 10, order: 1, instance: "a"},
		cum{value: 300, when: 15, order: 2, instance: "b"},
		cum{value: 650, start: 1, when: 20, order: 3, instance: "a"},
		cum{value: 650, when: 25, order: 4, instance: "b"},
	)
	if got != 1300 {
		t.Fatalf("got %v, want 1300", got)
	}
}

// TestCumulativeKnownLimits pins each bullet under "Retried delta exports"
// in dev-docs/gaps.md, so a change in behavior shows up here and in the docs.
func TestCumulativeKnownLimits(t *testing.T) {
	// Bullet 3: without a start time, a restart whose first value is not lower than
	// the last is not seen: 100 then a new run at 150 reads as 150.
	if got := cumulativeTokens(t, "input",
		cum{value: 100, when: 10, order: 1},
		cum{value: 150, when: 20, order: 2},
	); got != 150 {
		t.Fatalf("got %v, want 150 (documented undercount)", got)
	}
	// Bullet 1: two concurrent processes with the same start time are one
	// run even with different resources, so the total is whichever reported
	// last (50), not 150.
	if got := cumulativeTokens(t, "input",
		cum{value: 100, start: 1, when: 10, order: 1, instance: "a"},
		cum{value: 50, start: 1, when: 11, order: 2, instance: "b"},
	); got != 50 {
		t.Fatalf("got %v, want 50 (documented limit)", got)
	}
	// Bullet 2: without a start time, two processes with the same resource
	// interleave and their drops read as resets: 100, 10, 200, 20 is read
	// as 100 + 200 + 20 = 320, not 200 + 20 = 220.
	if got := cumulativeTokens(t, "input",
		cum{value: 100, when: 10, order: 1},
		cum{value: 10, when: 11, order: 2},
		cum{value: 200, when: 20, order: 3},
		cum{value: 20, when: 21, order: 4},
	); got != 320 {
		t.Fatalf("got %v, want 320 (documented overcount)", got)
	}
	// Bullet 3 is the 100-then-150 case above.
	// Bullet 4: a cumulative UpDownCounter without a start time that goes
	// down (5, 3) reads as a reset: 8, not 3.
	if got := cumulativeTokens(t, "input",
		cum{value: 5, when: 10, order: 1},
		cum{value: 3, when: 20, order: 2},
	); got != 8 {
		t.Fatalf("got %v, want 8 (documented limit)", got)
	}
}

func TestCumulativeMultipleResets(t *testing.T) {
	byStart := cumulativeTokens(t, "input",
		cum{value: 1000, start: 1, when: 10, order: 1},
		cum{value: 500, start: 20, when: 30, order: 2},
		cum{value: 100, start: 40, when: 45, order: 3},
		cum{value: 250, start: 40, when: 50, order: 4},
	)
	if byStart != 1750 {
		t.Fatalf("by start time: got %v, want 1750", byStart)
	}
	byDrop := cumulativeTokens(t, "input",
		cum{value: 100, when: 1, order: 1},
		cum{value: 300, when: 2, order: 2},
		cum{value: 50, when: 3, order: 3},
		cum{value: 80, when: 4, order: 4},
		cum{value: 20, when: 5, order: 5},
	)
	if byDrop != 400 {
		t.Fatalf("by value drop: got %v, want 400", byDrop)
	}
}

func TestCumulativeWithoutResetKeepsTheLatestValue(t *testing.T) {
	got := cumulativeTokens(t, "input",
		cum{value: 100, start: 1, when: 10, order: 1},
		cum{value: 250, start: 1, when: 20, order: 2},
		cum{value: 250, start: 1, when: 25, order: 3},
		cum{value: 400, start: 1, when: 30, order: 4},
	)
	if got != 400 {
		t.Fatalf("got %v, want 400", got)
	}
}

func TestCumulativeOutOfOrderPointsAreOrderedByTime(t *testing.T) {
	// Stored (order) in a different sequence from their timestamps.
	got := cumulativeTokens(t, "input",
		cum{value: 500, start: 30, when: 50, order: 1},
		cum{value: 1000, start: 1, when: 20, order: 2},
		cum{value: 200, start: 30, when: 40, order: 3},
		cum{value: 600, start: 1, when: 10, order: 4},
	)
	if got != 1500 {
		t.Fatalf("got %v, want 1500", got)
	}
	noStart := cumulativeTokens(t, "input",
		cum{value: 300, when: 3, order: 1},
		cum{value: 100, when: 1, order: 2},
		cum{value: 200, when: 2, order: 3},
	)
	if noStart != 300 {
		t.Fatalf("no start time: got %v, want 300 (a late point is not a reset)", noStart)
	}
}

func TestCumulativeSeriesWithDifferentAttributesAreIndependent(t *testing.T) {
	points := []cum{
		{value: 1000, start: 1, when: 10, order: 1, typ: "input"},
		{value: 300, start: 1, when: 11, order: 2, typ: "output"},
		{value: 500, start: 30, when: 40, order: 3, typ: "input"},
		{value: 350, start: 1, when: 41, order: 4, typ: "output"},
	}
	if got := cumulativeTokens(t, "input", points...); got != 1500 {
		t.Fatalf("input: got %v, want 1500", got)
	}
	if got := cumulativeTokens(t, "output", points...); got != 350 {
		t.Fatalf("output: got %v, want 350", got)
	}
}

func TestCumulativeConcurrentProcessesAreEachCountedOnce(t *testing.T) {
	// Two processes share one session.id and export at the same time. Their
	// points interleave, but each keeps its own start time, so each run
	// counts once: 300 + 200.
	byStart := cumulativeTokens(t, "input",
		cum{value: 100, start: 1, when: 10, order: 1},
		cum{value: 50, start: 2, when: 11, order: 2},
		cum{value: 300, start: 1, when: 20, order: 3},
		cum{value: 200, start: 2, when: 21, order: 4},
	)
	if byStart != 500 {
		t.Fatalf("by start time: got %v, want 500", byStart)
	}
	// Without start times, distinct resources keep them apart.
	byResource := cumulativeTokens(t, "input",
		cum{value: 100, when: 10, order: 1, instance: "a"},
		cum{value: 50, when: 11, order: 2, instance: "b"},
		cum{value: 300, when: 20, order: 3, instance: "a"},
		cum{value: 200, when: 21, order: 4, instance: "b"},
	)
	if byResource != 500 {
		t.Fatalf("by resource: got %v, want 500", byResource)
	}
}

func TestDeltaAndGaugeAreUnchangedByResetHandling(t *testing.T) {
	session := "s"
	delta := Summarize([]record.Record{
		metric("claude_code.token.usage", 1000, &session, "delta", "sum", map[string]any{"type": "input"}, 1, 1, ""),
		metric("claude_code.token.usage", 500, &session, "delta", "sum", map[string]any{"type": "input"}, 2, 2, ""),
	}, false).Sessions
	if got, _ := delta[0].Tokens.Get("input"); got != 1500 {
		t.Fatalf("delta: got %v", got)
	}
	gauge := reduce([]point{
		{name: "g", kind: "gauge", temporality: "unspecified", value: 1000, time: 1, order: 1, attributes: map[string]any{}},
		{name: "g", kind: "gauge", temporality: "unspecified", value: 500, time: 2, order: 2, attributes: map[string]any{}},
	})
	if len(gauge) != 1 || gauge[0].value != 500 {
		t.Fatalf("gauge: %+v", gauge)
	}
}
