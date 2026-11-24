package decisiongraph_test

import (
	"fmt"
	"testing"

	"github.com/paulpham157/sdf/internal/decision"
	"github.com/paulpham157/sdf/internal/decisiongraph"
)

// manyChapters builds n decisions, each implemented by its own session, so
// every decision is its own chapter.
func manyChapters(n int) decision.Graph {
	var g decision.Graph
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("d%05d", i)
		g.Decisions = append(g.Decisions, decision.Record{ID: id, Status: decision.StatusDecision, Statement: "s",
			EffectiveStatus: decision.EffectiveActive, RecordedAt: fmt.Sprintf("2026-01-01T00:00:%05d", i)})
		g.Edges = append(g.Edges, decision.Edge{Relation: decision.RelImplements,
			From: decision.Endpoint{Kind: decision.KindSession, Ref: fmt.Sprintf("s%05d", i)},
			To:   decision.Endpoint{Kind: decision.KindDecision, Ref: id}})
	}
	return g
}

// oneChapter chains n decisions with constrains edges and challenges each
// from one session: a single chapter with n challenged records.
func oneChapter(n int) decision.Graph {
	var g decision.Graph
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("d%05d", i)
		g.Decisions = append(g.Decisions, decision.Record{ID: id, Status: decision.StatusDecision, Statement: "s",
			EffectiveStatus: decision.EffectiveActive, Challenged: true, RecordedAt: fmt.Sprintf("2026-01-01T00:00:%05d", i)})
		g.Edges = append(g.Edges, decision.Edge{Relation: decision.RelContradicts,
			From: decision.Endpoint{Kind: decision.KindSession, Ref: "s"},
			To:   decision.Endpoint{Kind: decision.KindDecision, Ref: id}})
		if i > 0 {
			g.Edges = append(g.Edges, decision.Edge{Relation: decision.RelConstrains,
				From: decision.Endpoint{Kind: decision.KindDecision, Ref: fmt.Sprintf("d%05d", i-1)},
				To:   decision.Endpoint{Kind: decision.KindDecision, Ref: id}})
		}
	}
	return g
}

// Build walks every edge once per chapter and once per node (role, links,
// LatestEvidence), so both shapes grow quadratically. Compare ns/op across
// sizes: doubling n should roughly double the time once this is linear.
func BenchmarkBuild(b *testing.B) {
	for _, n := range []int{1000, 2000, 4000} {
		for name, gen := range map[string]func(int) decision.Graph{"chapters": manyChapters, "one-chapter": oneChapter} {
			g := gen(n)
			b.Run(fmt.Sprintf("%s/n=%d", name, n), func(b *testing.B) {
				for i := 0; i < b.N; i++ {
					_ = decisiongraph.Build(g, nil)
				}
			})
		}
	}
}
