//go:build reviewbugs

package decision_test

// Review tests for c3fc1f2 that state the behaviour the review asks for.
// They FAIL on c3fc1f2 and are behind the reviewbugs tag so the default
// suite stays green: go test -tags reviewbugs ./internal/decision/ -run ReviewBug

import (
	"testing"

	"github.com/paulpham157/sdf/internal/decision"
)

// An alias swaps the ends before the kind check, so the error names the
// wrong flag: the caller's --to is reported as "from".
func TestReviewBugAliasKindErrorNamesTheCallersFlag(t *testing.T) {
	db := openDB(t)
	seedSession(t, db)
	addAll(t, db, "b")
	rejects(t, db, decision.AliasDependsOn, dec("b"), session("sess-1"), decision.ErrEndpointKind,
		"depends_on links a decision to a decision: to is session:sess-1")
}

// Hypotheses that supersede each other (a legacy cycle) never reach a
// decision record, so the blocker is not settled. heads() returns no head
// for a cycle, which reads as settled.
func TestReviewBugHypothesisBlockerInASupersedesCycleStaysOpen(t *testing.T) {
	db := openDB(t)
	addHypotheses(t, db, "x", "y")
	addAll(t, db, "target")
	legacyEdge(t, db, decision.RelSupersedes, dec("x"), dec("y"))
	legacyEdge(t, db, decision.RelSupersedes, dec("y"), dec("x"))
	legacyEdge(t, db, decision.RelBlocks, dec("x"), dec("target"))
	if d := loadByID(t, db)["target"]; !d.Blocked {
		t.Fatalf("%+v: no decision record settled x, want blocked", d)
	}
}

// Evidence from a retracted decision still challenges its target, while a
// retracted blocker blocks nothing. The review suggests the same rule here.
func TestReviewBugContradictsFromARetractedDecisionDoesNotChallenge(t *testing.T) {
	db := openDB(t)
	addAll(t, db, "a", "b")
	link(t, db, decision.RelContradicts, dec("b"), dec("a"))
	retract(t, db, "b", "", "")
	if loadByID(t, db)["a"].Challenged {
		t.Fatal("a is challenged only by a withdrawn decision")
	}
}
