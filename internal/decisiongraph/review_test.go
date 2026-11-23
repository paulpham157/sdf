package decisiongraph_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/paulpham157/sdf/internal/decision"
	"github.com/paulpham157/sdf/internal/decisiongraph"
	"github.com/paulpham157/sdf/internal/summary"
)

func jsonOf(t *testing.T, v decisiongraph.View) string {
	t.Helper()
	var buf bytes.Buffer
	if err := decisiongraph.WriteJSON(&buf, v); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

func edgesOf(v decisiongraph.View) []decisiongraph.Edge {
	var out []decisiongraph.Edge
	for _, c := range v.Chapters {
		out = append(out, c.Edges...)
	}
	return out
}

func nodeIDs(v decisiongraph.View) map[string]bool {
	out := map[string]bool{}
	for _, c := range v.Chapters {
		for _, n := range c.Nodes {
			out[n.ID] = true
		}
	}
	return out
}

// An empty graph is an empty chapter list with the relation legend, and the
// JSON never carries null for chapters.
func TestReviewEmptyGraphShape(t *testing.T) {
	v := decisiongraph.Build(decision.Graph{}, nil)
	if v.Chapters == nil || len(v.Chapters) != 0 || len(v.RelationTypes) == 0 {
		t.Fatalf("%+v", v)
	}
	if out := jsonOf(t, decisiongraph.View{}); !strings.Contains(out, `"chapters": []`) {
		t.Fatalf("zero View JSON: %s", out)
	}
	var text bytes.Buffer
	if err := decisiongraph.WriteText(&text, v); err != nil || text.String() != "no decision\n" {
		t.Fatalf("%q %v", text.String(), err)
	}
}

// Alias input is stored as blocks with the ends swapped, so the graph draws
// one blocks edge from the blocker, and the blocked decision is locked.
func TestReviewAliasesDrawAsBlocks(t *testing.T) {
	for _, alias := range []string{"blocked_by", "depends_on"} {
		t.Run(alias, func(t *testing.T) {
			db := openDB(t)
			add(t, db, decision.RecordInput{ID: "gate", Status: decision.StatusHypothesis})
			add(t, db, decision.RecordInput{ID: "work"})
			link(t, db, alias, "decision:work", "decision:gate")
			v := decisiongraph.Build(load(t, db), nil)
			var stored []decisiongraph.Edge
			for _, e := range edgesOf(v) {
				if !e.Derived {
					stored = append(stored, e)
				}
			}
			if len(stored) != 1 || stored[0].Relation != decision.RelBlocks || stored[0].From != "gate" || stored[0].To != "work" {
				t.Fatalf("stored edges %+v", stored)
			}
			if n := nodes(v.Chapters[0])["work"]; n.Role != decisiongraph.RoleLocked {
				t.Fatalf("work role %q", n.Role)
			}
		})
	}
}

// A symmetric conflict linked once is drawn once, whichever order the
// caller gave, and counted once.
func TestReviewConflictDrawnOnce(t *testing.T) {
	db := openDB(t)
	add(t, db, decision.RecordInput{ID: "b"})
	add(t, db, decision.RecordInput{ID: "a"})
	link(t, db, "conflicts_with", "decision:b", "decision:a")
	v := decisiongraph.Build(load(t, db), nil)
	n := 0
	for _, e := range edgesOf(v) {
		if e.Relation == decision.RelConflictsWith {
			n++
			if !e.Tension {
				t.Fatalf("conflict edge not marked tension: %+v", e)
			}
		}
	}
	if n != 1 || v.Chapters[0].Facts.Conflicts != 1 {
		t.Fatalf("conflict edges %d facts %+v", n, v.Chapters[0].Facts)
	}
}

// Legacy rows the link checks would refuse today (a self-edge, an edge to an
// id never stored, a blocks cycle) must not create nodes, self-loops, or hang.
func TestReviewLegacyDanglingSelfAndCycle(t *testing.T) {
	db := openDB(t)
	add(t, db, decision.RecordInput{ID: "a"})
	add(t, db, decision.RecordInput{ID: "b"})
	legacyEdge(t, db, "refines", "decision:a", "decision:a")
	legacyEdge(t, db, "blocks", "decision:a", "decision:ghost")
	legacyEdge(t, db, "blocks", "decision:a", "decision:b")
	legacyEdge(t, db, "blocks", "decision:b", "decision:a")
	v := decisiongraph.Build(load(t, db), nil)
	ids := nodeIDs(v)
	if ids["ghost"] {
		t.Fatal("a dangling id became a node")
	}
	for _, e := range edgesOf(v) {
		if e.From == e.To {
			t.Fatalf("self-loop drawn: %+v", e)
		}
		if !ids[e.From] || !ids[e.To] {
			t.Fatalf("edge to a missing node: %+v", e)
		}
	}
	if len(v.Chapters) != 1 {
		t.Fatalf("a and b share a blocks edge, chapters = %d", len(v.Chapters))
	}
	a := nodes(v.Chapters[0])["a"]
	if a.Role == decisiongraph.RoleRefinement {
		t.Fatal("a self refines edge made a a refinement")
	}
	var dangling bool
	for _, l := range a.Links {
		if l.Ref == "ghost" && l.Direction == "out" {
			dangling = true
		}
	}
	if !dangling {
		t.Fatalf("the dangling link is not listed on a: %+v", a.Links)
	}
}

// The View does not depend on the order of the session list when sessions
// have distinct start times.
func TestReviewDeterministicAcrossSessionOrder(t *testing.T) {
	g, sessions := story(t)
	want := jsonOf(t, decisiongraph.Build(g, sessions))
	rev := append([]summary.SessionSummary(nil), sessions...)
	for i, j := 0, len(rev)-1; i < j; i, j = i+1, j-1 {
		rev[i], rev[j] = rev[j], rev[i]
	}
	for i := 0; i < 5; i++ {
		if got := jsonOf(t, decisiongraph.Build(g, rev)); got != want {
			t.Fatalf("session order changed the View")
		}
	}
}

// Every edge points at nodes of its own chapter and node ids are unique.
func TestReviewStoryIntegrity(t *testing.T) {
	g, sessions := story(t)
	v := decisiongraph.Build(g, sessions)
	for _, c := range v.Chapters {
		seen := map[string]bool{}
		for _, n := range c.Nodes {
			if seen[n.ID] {
				t.Fatalf("%s: duplicate node %s", c.ID, n.ID)
			}
			seen[n.ID] = true
			if n.Lane != decisiongraph.LaneNone && n.Row >= c.Rows {
				t.Fatalf("%s: node %s row %d outside rows %d", c.ID, n.ID, n.Row, c.Rows)
			}
		}
		for _, e := range c.Edges {
			if !seen[e.From] || !seen[e.To] {
				t.Fatalf("%s: edge %+v leaves the chapter", c.ID, e)
			}
		}
	}
}

// The what-if command keeps the new id inside the 128-character id limit
// for the longest parent id, and quotes shell metacharacters.
func TestReviewWhatIfCommandLongIDAndQuoting(t *testing.T) {
	parent := strings.Repeat("x", 128)
	cmd := decisiongraph.WhatIfCommand(parent, 12, "it's $HOME `id`; rm -rf /")
	fields := strings.SplitN(cmd, " ", 6)
	if len(fields) < 5 || fields[3] != "--id" {
		t.Fatalf("%q", cmd)
	}
	if id := fields[4]; len(id) > 128 || !strings.HasSuffix(id, "-alt-12") {
		t.Fatalf("id %q (%d chars)", id, len(id))
	}
	if !strings.Contains(cmd, `--statement 'it'\''s $HOME `+"`id`"+`; rm -rf /'`) {
		t.Fatalf("statement not single-quoted: %q", cmd)
	}
}
