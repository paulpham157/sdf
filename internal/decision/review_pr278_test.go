package decision_test

// Review test for PR #278 (1001dc5). It failed (timed out) on 1001dc5,
// whose heads() walked every path; it ran behind a reviewbugs build tag
// until headResolver memoized heads per decision, and now runs in the
// default suite.

import (
	"fmt"
	"testing"
	"time"

	"github.com/paulpham157/sdf/internal/decision"
)

// A supersedes "diamond ladder": each level has two decisions that both
// supersede the previous join, and one join that supersedes both. It is a
// valid graph (no cycle), so Link accepts every edge. heads() in
// resolveRelations walks every path without memoizing, so the number of
// paths, and Load's time, doubles with each level.
func TestReviewPR278LoadStaysFastOnASupersedesDiamondLadder(t *testing.T) {
	const levels = 26
	db := openDB(t)
	addHypotheses(t, db, "j0")
	addAll(t, db, "target")
	link(t, db, decision.RelBlocks, dec("j0"), dec("target"))
	prev := "j0"
	for i := 1; i <= levels; i++ {
		p, q, j := fmt.Sprintf("p%d", i), fmt.Sprintf("q%d", i), fmt.Sprintf("j%d", i)
		addHypotheses(t, db, p, q, j)
		link(t, db, decision.RelSupersedes, dec(p), dec(prev))
		link(t, db, decision.RelSupersedes, dec(q), dec(prev))
		link(t, db, decision.RelSupersedes, dec(j), dec(p))
		link(t, db, decision.RelSupersedes, dec(j), dec(q))
		prev = j
	}
	done := make(chan decision.Record, 1)
	start := time.Now()
	go func() { done <- loadByID(t, db)["target"] }()
	select {
	case d := <-done:
		t.Logf("Load took %v", time.Since(start))
		if !d.Blocked {
			t.Fatalf("%+v: the only head is a hypothesis, want blocked", d)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("Load did not return within 5s on a %d-level supersedes diamond ladder (%d paths)", levels, 1<<levels)
	}
}

// The mixed case: a blocker with no head where one path runs into a
// supersedes cycle and another ends in a retraction is open.
func TestPR278CycleOnOnePathRetractionOnAnotherIsOpen(t *testing.T) {
	db := openDB(t)
	addHypotheses(t, db, "x", "y", "w", "z")
	addAll(t, db, "target")
	link(t, db, decision.RelSupersedes, dec("y"), dec("x"))
	link(t, db, decision.RelSupersedes, dec("z"), dec("x"))
	legacyEdge(t, db, decision.RelSupersedes, dec("w"), dec("y"))
	legacyEdge(t, db, decision.RelSupersedes, dec("y"), dec("w"))
	retract(t, db, "z", "", "")
	link(t, db, decision.RelBlocks, dec("x"), dec("target"))
	if d := loadByID(t, db)["target"]; !d.Blocked || len(d.OpenBlockers) != 1 {
		t.Fatalf("%+v: x has no head and one path is a cycle, want open", d)
	}
}

// Two blockers enter the same legacy cycle through different members, and
// two more reach it below a diamond. Whichever member the walk entered
// first, each blocker is open. Once a decision record supersedes l, the
// blockers whose paths pass l have a head and are settled, while the ones
// that reach only the cycle stay open.
func TestPR278SharedCycleIsOpenFromEveryEntry(t *testing.T) {
	db := openDB(t)
	addHypotheses(t, db, "a", "b", "c", "e1", "e2", "top", "l", "r", "m")
	addAll(t, db, "settled-by", "t1", "t2", "t3", "t4")
	// Cycle a -> b -> c -> a (each superseded by the next).
	legacyEdge(t, db, decision.RelSupersedes, dec("b"), dec("a"))
	legacyEdge(t, db, decision.RelSupersedes, dec("c"), dec("b"))
	legacyEdge(t, db, decision.RelSupersedes, dec("a"), dec("c"))
	// e1 enters at a, e2 at b.
	link(t, db, decision.RelSupersedes, dec("a"), dec("e1"))
	link(t, db, decision.RelSupersedes, dec("b"), dec("e2"))
	// A diamond top -> l, r -> m, then m enters the cycle at c.
	link(t, db, decision.RelSupersedes, dec("l"), dec("top"))
	link(t, db, decision.RelSupersedes, dec("r"), dec("top"))
	link(t, db, decision.RelSupersedes, dec("m"), dec("l"))
	link(t, db, decision.RelSupersedes, dec("m"), dec("r"))
	link(t, db, decision.RelSupersedes, dec("c"), dec("m"))
	link(t, db, decision.RelBlocks, dec("e1"), dec("t1"))
	link(t, db, decision.RelBlocks, dec("e2"), dec("t2"))
	link(t, db, decision.RelBlocks, dec("top"), dec("t3"))
	link(t, db, decision.RelBlocks, dec("l"), dec("t4"))
	got := loadByID(t, db)
	for _, id := range []string{"t1", "t2", "t3", "t4"} {
		if !got[id].Blocked {
			t.Fatalf("%s %+v: blocker reaches only the cycle, want blocked", id, got[id])
		}
	}
	// A decision record now also supersedes l: l and top have a head (a
	// decision), so a cycle on their other path does not reopen them.
	link(t, db, decision.RelSupersedes, dec("settled-by"), dec("l"))
	got = loadByID(t, db)
	want := map[string]bool{"t1": true, "t2": true, "t3": false, "t4": false}
	for id, blocked := range want {
		if got[id].Blocked != blocked {
			t.Fatalf("%s %+v: blocked = %v, want %v", id, got[id], got[id].Blocked, blocked)
		}
	}
}
