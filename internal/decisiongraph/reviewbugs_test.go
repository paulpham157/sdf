package decisiongraph_test

// Was behind the reviewbugs tag (failed at 5f0768e); the conflicts_with
// dedupe key is now order-independent, so it runs in the default suite.

import (
	"testing"

	"github.com/paulpham157/sdf/internal/decision"
	"github.com/paulpham157/sdf/internal/decisiongraph"
)

// conflicts_with is symmetric. A database written before the canonical
// order may hold the same pair in both directions; the chapter should draw
// one tension edge for it, as Facts.Conflicts already counts one pair.
func TestReviewBugLegacyReversedConflictDrawnTwice(t *testing.T) {
	db := openDB(t)
	add(t, db, decision.RecordInput{ID: "a"})
	add(t, db, decision.RecordInput{ID: "b"})
	legacyEdge(t, db, "conflicts_with", "decision:a", "decision:b")
	legacyEdge(t, db, "conflicts_with", "decision:b", "decision:a")
	v := decisiongraph.Build(load(t, db), nil)
	n := 0
	for _, e := range edgesOf(v) {
		if e.Relation == decision.RelConflictsWith {
			n++
		}
	}
	if v.Chapters[0].Facts.Conflicts != 1 {
		t.Fatalf("facts %+v", v.Chapters[0].Facts)
	}
	if n != 1 {
		t.Fatalf("%d conflicts_with edges drawn for one symmetric pair, want 1", n)
	}
}
