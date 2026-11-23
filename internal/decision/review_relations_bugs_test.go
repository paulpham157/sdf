package decision_test

// Review tests for c3fc1f2 that state the behaviour the review asked for.
// They failed on c3fc1f2 and were behind a reviewbugs build tag; with the
// fixes they run in the default suite.

import (
	"strings"
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

// The alias's ref errors name the caller's flag as well.
func TestReviewBugAliasRefErrorNamesTheCallersFlag(t *testing.T) {
	db := openDB(t)
	addAll(t, db, "b")
	for _, rel := range []string{decision.AliasBlockedBy, decision.AliasDependsOn} {
		_, err := decision.Link(db, decision.EdgeInput{Relation: rel, From: dec("b"), To: dec("bad ref")})
		if err == nil || !strings.HasPrefix(err.Error(), "to: ") {
			t.Fatalf("%s: err %v, want it to name to", rel, err)
		}
	}
}

// Two supersedes paths that meet again are not a cycle: b and c both
// supersede a, d supersedes both and is retracted, so the blocker a has no
// head and is settled (withdrawn), not open.
func TestReviewBugSupersedesDiamondIsNotACycle(t *testing.T) {
	db := openDB(t)
	addHypotheses(t, db, "a", "b", "c", "d")
	addAll(t, db, "target")
	link(t, db, decision.RelSupersedes, dec("b"), dec("a"))
	link(t, db, decision.RelSupersedes, dec("c"), dec("a"))
	link(t, db, decision.RelSupersedes, dec("d"), dec("b"))
	link(t, db, decision.RelSupersedes, dec("d"), dec("c"))
	link(t, db, decision.RelBlocks, dec("a"), dec("target"))
	retract(t, db, "d", "", "")
	if d := loadByID(t, db)["target"]; d.Blocked || len(d.OpenBlockers) != 0 {
		t.Fatalf("%+v: a's only head was retracted, want settled", d)
	}
}

// A legacy cycle on one branch does not reopen a blocker that a decision
// record settled on another: the blocker has a head, and it is a decision.
func TestReviewBugCycleBesideADecisionHeadIsSettled(t *testing.T) {
	db := openDB(t)
	addHypotheses(t, db, "x", "y")
	addAll(t, db, "r", "target")
	legacyEdge(t, db, decision.RelSupersedes, dec("x"), dec("y"))
	legacyEdge(t, db, decision.RelSupersedes, dec("y"), dec("x"))
	link(t, db, decision.RelSupersedes, dec("r"), dec("x"))
	link(t, db, decision.RelBlocks, dec("x"), dec("target"))
	if d := loadByID(t, db)["target"]; d.Blocked {
		t.Fatalf("%+v: r settled x, want not blocked", d)
	}
}

// A contradicts from a superseded decision does not challenge either, and
// an earlier contradicts from an active source still does.
func TestReviewBugContradictsFromASupersededDecisionDoesNotChallenge(t *testing.T) {
	db := openDB(t)
	seedSession(t, db)
	addAll(t, db, "a", "b", "b2", "c")
	link(t, db, decision.RelContradicts, dec("b"), dec("a"))
	link(t, db, decision.RelSupersedes, dec("b2"), dec("b"))
	if loadByID(t, db)["a"].Challenged {
		t.Fatal("a is challenged only by a superseded decision")
	}
	link(t, db, decision.RelContradicts, session("sess-1"), dec("c"))
	link(t, db, decision.RelContradicts, dec("b"), dec("c"))
	if !loadByID(t, db)["c"].Challenged {
		t.Fatal("c is contradicted by an active session, want challenged")
	}
}
