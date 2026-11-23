package decision_test

import (
	"testing"

	"github.com/paulpham157/sdf/internal/decision"
	"github.com/paulpham157/sdf/internal/store"
)

// LatestEvidence applies the same rule as Challenged: evidence from a
// decision that is not active is skipped, sessions always count.
func TestLatestEvidenceSkipsNonActiveDecisionSources(t *testing.T) {
	db := openDB(t)
	seedSession(t, db)
	addAll(t, db, "a", "r", "b")
	if _, ok := decision.LatestEvidence(mustLoad(t, db), "a"); ok {
		t.Fatal("no evidence yet")
	}
	link(t, db, decision.RelContradicts, session("sess-1"), dec("a"))
	link(t, db, decision.RelValidates, dec("r"), dec("a"))
	if e, ok := decision.LatestEvidence(mustLoad(t, db), "a"); !ok || e.Relation != decision.RelValidates {
		t.Fatalf("got %+v %v, want r's validates while r is active", e, ok)
	}
	retract(t, db, "r", "", "")
	g := mustLoad(t, db)
	e, ok := decision.LatestEvidence(g, "a")
	if !ok || e.Relation != decision.RelContradicts || e.From != session("sess-1") {
		t.Fatalf("got %+v %v, want the session's contradicts", e, ok)
	}
	if !loadByID(t, db)["a"].Challenged {
		t.Fatal("Challenged and LatestEvidence disagree")
	}
}

func mustLoad(t *testing.T, db *store.Store) decision.Graph {
	t.Helper()
	g, err := decision.Load(db)
	if err != nil {
		t.Fatal(err)
	}
	return g
}
