package decision_test

// Review tests for c3fc1f2 (decision relations). They pin edge cases the
// commit's own tests do not reach. All pass on c3fc1f2.

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/paulpham157/sdf/internal/decision"
)

// Legacy rows can hold a conflicts_with pair in both orders and the same
// blocks or refines row twice. Read time lists each id once.
func TestReviewLegacyDuplicateRelationRowsListOnce(t *testing.T) {
	db := openDB(t)
	addHypotheses(t, db, "h")
	addAll(t, db, "a", "b", "c")
	legacyEdge(t, db, decision.RelConflictsWith, dec("a"), dec("b"))
	legacyEdge(t, db, decision.RelConflictsWith, dec("b"), dec("a"))
	legacyEdge(t, db, decision.RelBlocks, dec("h"), dec("c"))
	legacyEdge(t, db, decision.RelBlocks, dec("h"), dec("c"))
	legacyEdge(t, db, decision.RelRefines, dec("c"), dec("a"))
	legacyEdge(t, db, decision.RelRefines, dec("c"), dec("a"))
	got := loadByID(t, db)
	if !reflect.DeepEqual(got["a"].ConflictsWith, []string{"b"}) || !reflect.DeepEqual(got["b"].ConflictsWith, []string{"a"}) {
		t.Fatalf("conflicts_with a=%v b=%v, want each other once", got["a"].ConflictsWith, got["b"].ConflictsWith)
	}
	if !got["a"].Conflicted || !got["b"].Conflicted {
		t.Fatal("both sides active: want conflicted")
	}
	if c := got["c"]; !reflect.DeepEqual(c.BlockedBy, []string{"h"}) || !reflect.DeepEqual(c.OpenBlockers, []string{"h"}) || !c.Blocked {
		t.Fatalf("c %+v, want blocked by h once", c)
	}
	if !reflect.DeepEqual(got["a"].RefinedBy, []string{"c"}) {
		t.Fatalf("refined_by %v, want [c]", got["a"].RefinedBy)
	}
	// A legacy reversed conflicts_with row is still a duplicate for a new link in either order.
	for _, p := range [][2]string{{"a", "b"}, {"b", "a"}} {
		_, err := decision.Link(db, decision.EdgeInput{Relation: decision.RelConflictsWith, From: dec(p[0]), To: dec(p[1])})
		if !errors.Is(err, decision.ErrDuplicateEdge) {
			t.Fatalf("%v: err %v, want duplicate", p, err)
		}
	}
}

// Aliases, direct two-node cycles, and self-links through every new relation.
func TestReviewCyclesThroughAliasesAndDirectPairs(t *testing.T) {
	db := openDB(t)
	addAll(t, db, "a", "b", "c")
	link(t, db, decision.AliasDependsOn, dec("b"), dec("a")) // stores a blocks b
	rejects(t, db, decision.RelBlocks, dec("b"), dec("a"), decision.ErrBlocksCycle,
		"blocks cycle: decision:a already reaches decision:b through blocks (a -> b), so decision:b cannot block decision:a")
	rejects(t, db, decision.AliasDependsOn, dec("a"), dec("b"), decision.ErrBlocksCycle,
		"blocks cycle: decision:a already reaches decision:b through blocks (a -> b), so decision:b cannot block decision:a")
	link(t, db, decision.RelRefines, dec("b"), dec("c"))
	rejects(t, db, decision.RelRefines, dec("c"), dec("b"), decision.ErrRefinesCycle,
		"refines cycle: decision:b already reaches decision:c through refines (b -> c), so decision:c cannot refine decision:b")
	// Relations do not mix for cycles: b refines c does not stop c blocks b.
	link(t, db, decision.RelBlocks, dec("c"), dec("b"))
	for _, rel := range []string{decision.AliasBlockedBy, decision.AliasDependsOn, decision.RelBlocks, decision.RelRefines, decision.RelConflictsWith} {
		rejects(t, db, rel, dec("c"), dec("c"), decision.ErrSelfLink, "an edge cannot link an endpoint to itself: decision:c")
	}
}

// conflicts_with with a non-decision end is not swapped, so the kind error
// names the end as the caller gave it.
func TestReviewConflictsWithKindErrorKeepsTheCallersEnds(t *testing.T) {
	db := openDB(t)
	seedSession(t, db)
	addAll(t, db, "zzz")
	rejects(t, db, decision.RelConflictsWith, dec("zzz"), session("sess-1"), decision.ErrEndpointKind,
		"conflicts_with links a decision to a decision: to is session:sess-1")
	rejects(t, db, decision.RelConflictsWith, session("sess-1"), dec("zzz"), decision.ErrEndpointKind,
		"conflicts_with links a decision to a decision: from is session:sess-1")
}

// The usual way to settle a conflict, superseding one side, is allowed after
// conflicts_with (only the reverse order is refused) and clears conflicted.
func TestReviewSupersedingAfterAConflictResolvesIt(t *testing.T) {
	db := openDB(t)
	addAll(t, db, "use-sqlite", "use-postgres")
	link(t, db, decision.RelConflictsWith, dec("use-sqlite"), dec("use-postgres"))
	link(t, db, decision.RelSupersedes, dec("use-sqlite"), dec("use-postgres"))
	got := loadByID(t, db)
	if got["use-sqlite"].Conflicted || got["use-postgres"].Conflicted {
		t.Fatalf("superseded side resolves the conflict: %+v %+v", got["use-sqlite"], got["use-postgres"])
	}
}

// Pins current behaviour: a hypothesis blocker settled by a decision that is
// later retracted counts as settled (the retracted head contributes nothing),
// as docs/outcomes.md describes. See review finding on re-opening.
func TestReviewBlockerSettledByARetractedDecisionStaysSettled(t *testing.T) {
	db := openDB(t)
	addHypotheses(t, db, "h")
	addAll(t, db, "target", "answer")
	link(t, db, decision.RelBlocks, dec("h"), dec("target"))
	link(t, db, decision.RelSupersedes, dec("answer"), dec("h"))
	retract(t, db, "answer", "", "")
	if d := loadByID(t, db)["target"]; d.Blocked || len(d.OpenBlockers) != 0 {
		t.Fatalf("%+v: current rule says settled", d)
	}
}

// A legacy supersedes cycle (rows an older sdf or a dangling id later added
// could leave) must not hang Load.
func TestReviewLegacySupersedesCycleDoesNotHangLoad(t *testing.T) {
	db := openDB(t)
	addHypotheses(t, db, "x", "y")
	addAll(t, db, "target")
	legacyEdge(t, db, decision.RelSupersedes, dec("x"), dec("y"))
	legacyEdge(t, db, decision.RelSupersedes, dec("y"), dec("x"))
	legacyEdge(t, db, decision.RelBlocks, dec("x"), dec("target"))
	done := make(chan struct{})
	go func() { _, _ = decision.Load(db); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Load did not return on a supersedes cycle")
	}
}
