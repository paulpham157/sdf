package decisiongraph_test

import (
	"database/sql"
	"reflect"
	"strings"
	"testing"

	"github.com/paulpham157/sdf/internal/decision"
	"github.com/paulpham157/sdf/internal/decisiongraph"
	"github.com/paulpham157/sdf/internal/store"
)

// The graph reads blocked, conflicted, and challenged from the decision
// package's resolution. These tests pin the rules it inherits.

// legacyEdge writes an edge straight into decision_edges, bypassing Link,
// the way an sdf without link validation could have stored it.
func legacyEdge(t *testing.T, db *store.Store, rel, from, to string) {
	t.Helper()
	raw, err := sql.Open("sqlite", db.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	fk, fr, _ := strings.Cut(from, ":")
	tk, tr, _ := strings.Cut(to, ":")
	if _, err := raw.Exec(`INSERT INTO decision_edges (relation, from_kind, from_ref, to_kind, to_ref, recorded_at) VALUES (?, ?, ?, ?, ?, ?)`,
		rel, fk, fr, tk, tr, "2026-01-01T00:00:00.000000+00:00"); err != nil {
		t.Fatal(err)
	}
}

func retract(t *testing.T, db *store.Store, id string) {
	t.Helper()
	if _, err := decision.Retract(db, decision.RetractInput{ID: id}); err != nil {
		t.Fatal(err)
	}
}

// facts sums every chapter's facts and collects the attention items.
func facts(v decisiongraph.View) (decisiongraph.Facts, []decisiongraph.Attention) {
	var f decisiongraph.Facts
	var att []decisiongraph.Attention
	for _, c := range v.Chapters {
		f.Locked += c.Facts.Locked
		f.Conflicts += c.Facts.Conflicts
		f.Challenged += c.Facts.Challenged
		att = append(att, c.Attention...)
	}
	return f, att
}

func attention(att []decisiongraph.Attention, kind string) []decisiongraph.Attention {
	var out []decisiongraph.Attention
	for _, a := range att {
		if a.Kind == kind {
			out = append(out, a)
		}
	}
	return out
}

// Only active sides make a conflict: a side superseded after the
// conflicts_with edge, or retracted, is resolved.
func TestGraphConflictsCountOnlyActiveSides(t *testing.T) {
	db := openDB(t)
	for _, id := range []string{"a", "b", "c", "c2", "d", "e"} {
		add(t, db, decision.RecordInput{ID: id})
	}
	link(t, db, "conflicts_with", "decision:a", "decision:b")
	link(t, db, "conflicts_with", "decision:a", "decision:c")
	link(t, db, "supersedes", "decision:c2", "decision:c")
	link(t, db, "conflicts_with", "decision:d", "decision:e")
	retract(t, db, "e")
	f, att := facts(decisiongraph.Build(load(t, db), nil))
	if f.Conflicts != 1 {
		t.Fatalf("conflicts = %d, want 1 (a/b)", f.Conflicts)
	}
	got := attention(att, "conflict")
	if len(got) != 1 || !reflect.DeepEqual(got[0].Nodes, []string{"a", "b"}) {
		t.Fatalf("conflict attention = %+v, want only a/b", got)
	}
}

// A hypothesis blocker in a legacy supersedes cycle reaches no head, so no
// decision record settled it: the target is locked.
func TestGraphCycleBlockerIsOpen(t *testing.T) {
	db := openDB(t)
	add(t, db, decision.RecordInput{ID: "x", Status: decision.StatusHypothesis})
	add(t, db, decision.RecordInput{ID: "y", Status: decision.StatusHypothesis})
	add(t, db, decision.RecordInput{ID: "target"})
	legacyEdge(t, db, "supersedes", "decision:x", "decision:y")
	legacyEdge(t, db, "supersedes", "decision:y", "decision:x")
	link(t, db, "blocks", "decision:x", "decision:target")
	v := decisiongraph.Build(load(t, db), nil)
	f, att := facts(v)
	if f.Locked != 1 {
		t.Fatalf("locked = %d, want 1", f.Locked)
	}
	got := attention(att, "locked")
	if len(got) != 1 || !reflect.DeepEqual(got[0].Nodes, []string{"target", "x"}) {
		t.Fatalf("locked attention = %+v, want target waiting on x", got)
	}
	for _, c := range v.Chapters {
		if n, ok := nodes(c)["target"]; ok && n.Role != decisiongraph.RoleLocked {
			t.Fatalf("target role = %q, want locked", n.Role)
		}
	}
}

// Challenged counts validates and contradicts from active sources only. A
// validates from a retracted decision does not clear a session's
// contradicts, and a contradicts from a superseded decision does not
// challenge. The attention names the source that counts.
func TestGraphChallengedUsesActiveSourcesOnly(t *testing.T) {
	db := openDB(t)
	known(t, db, "s3", "")
	for _, id := range []string{"a", "r", "e", "d", "d2"} {
		add(t, db, decision.RecordInput{ID: id})
	}
	link(t, db, "contradicts", "session:s3", "decision:a")
	link(t, db, "validates", "decision:r", "decision:a")
	retract(t, db, "r")
	link(t, db, "contradicts", "decision:d", "decision:e")
	link(t, db, "supersedes", "decision:d2", "decision:d")
	f, att := facts(decisiongraph.Build(load(t, db), nil))
	if f.Challenged != 1 {
		t.Fatalf("challenged = %d, want 1 (a)", f.Challenged)
	}
	got := attention(att, "challenged")
	if len(got) != 1 || !reflect.DeepEqual(got[0].Nodes, []string{"a"}) || !strings.HasSuffix(got[0].Text, "(session s3)") {
		t.Fatalf("challenged attention = %+v, want a, named by session s3", got)
	}
}

// Facts.Locked and Explored.Locked count the same records: a blocked
// hypothesis is locked in both, as a blocked decision is. A blocked
// refinement is not a choice point, so it is in neither.
func TestGraphLockedFactsAndExploredAgree(t *testing.T) {
	db := openDB(t)
	add(t, db, decision.RecordInput{ID: "q", Status: decision.StatusHypothesis})
	add(t, db, decision.RecordInput{ID: "blocked-decision"})
	add(t, db, decision.RecordInput{ID: "blocked-hypothesis", Status: decision.StatusHypothesis})
	add(t, db, decision.RecordInput{ID: "blocked-refinement"})
	link(t, db, "blocks", "decision:q", "decision:blocked-decision")
	link(t, db, "blocks", "decision:q", "decision:blocked-hypothesis")
	link(t, db, "refines", "decision:blocked-refinement", "decision:blocked-decision")
	link(t, db, "blocks", "decision:q", "decision:blocked-refinement")
	v := decisiongraph.Build(load(t, db), nil)
	if len(v.Chapters) != 1 {
		t.Fatalf("%d chapters", len(v.Chapters))
	}
	c := v.Chapters[0]
	if c.Facts.Locked != 2 || c.Explored.Locked != 2 {
		t.Fatalf("facts.locked %d, explored.locked %d, want 2 and 2", c.Facts.Locked, c.Explored.Locked)
	}
	if got := attention(c.Attention, "locked"); len(got) != 2 {
		t.Fatalf("locked attention %+v, want 2 rows", got)
	}
	// q is the open hypothesis; the blocked hypothesis is locked, not open.
	if c.Explored.Hypotheses != 1 || c.Explored.Decided != 0 {
		t.Fatalf("explored %+v", c.Explored)
	}
}

// Two parents longer than the cut that share their first 110 characters
// get different what-if ids, each within the 128-character limit; a short
// parent is unchanged.
func TestWhatIfCommandLongParentsDoNotCollide(t *testing.T) {
	idOf := func(cmd string) string { return strings.Fields(cmd)[4] }
	prefix := strings.Repeat("p", 110)
	a := idOf(decisiongraph.WhatIfCommand(prefix+"-alpha", 1, "x"))
	b := idOf(decisiongraph.WhatIfCommand(prefix+"-beta", 1, "x"))
	if a == b {
		t.Fatalf("both parents map to %s", a)
	}
	for _, id := range []string{a, b} {
		if len(id) > 128 || !strings.HasSuffix(id, "-alt-1") || !strings.HasPrefix(id, strings.Repeat("p", 100)+"-") {
			t.Fatalf("id %q (%d chars)", id, len(id))
		}
	}
	if a != idOf(decisiongraph.WhatIfCommand(prefix+"-alpha", 1, "y")) {
		t.Fatal("the id depends only on the parent and n")
	}
	if got := idOf(decisiongraph.WhatIfCommand("short", 3, "x")); got != "short-alt-3" {
		t.Fatalf("short parent: %q", got)
	}
}
