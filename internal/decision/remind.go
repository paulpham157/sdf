package decision

import (
	"strings"

	"github.com/paulpham157/sdf/internal/store"
)

// UnrecordedDecision reports whether sessionID should be reminded to record
// a decision. It is true when the session has at least one sdf.git.commit
// and either no stored decision is linked to the session, or no implements
// edge runs from the session or from one of those commits to a stored
// decision. A claude_code.tool_result commit does not count. It only reads.
func UnrecordedDecision(s *store.Store, sessionID string) (bool, error) {
	if sessionID == "" {
		return false, nil
	}
	events, err := s.LoadCommitEvents()
	if err != nil {
		return false, err
	}
	shas := map[string]bool{}
	for _, ev := range events {
		if ev.Name == "sdf.git.commit" && ev.SessionID == sessionID && ev.SHA != "" {
			shas[strings.ToLower(ev.SHA)] = true
		}
	}
	if len(shas) == 0 {
		return false, nil
	}
	g, err := Load(s)
	if err != nil {
		return false, err
	}
	byID := make(map[string]struct{}, len(g.Decisions))
	for _, d := range g.Decisions {
		byID[d.ID] = struct{}{}
	}
	isDecision := func(e Endpoint) bool {
		if e.Kind != KindDecision {
			return false
		}
		_, ok := byID[e.Ref]
		return ok
	}
	session := Endpoint{Kind: KindSession, Ref: sessionID}
	linked := false
	implemented := false
	for _, e := range g.Edges {
		if (isDecision(e.To) && e.From == session) || (isDecision(e.From) && e.To == session) {
			linked = true
		}
		if e.Relation != RelImplements || !isDecision(e.To) {
			continue
		}
		if e.From == session || (e.From.Kind == KindCommit && shas[strings.ToLower(e.From.Ref)]) {
			implemented = true
		}
	}
	return !linked || !implemented, nil
}

// FilterBySession keeps the edges that touch session:sessionID and the
// decisions those edges name. Other decisions and edges are dropped.
// Insertion order is kept. A session with no edges yields empty lists.
func FilterBySession(g Graph, sessionID string) Graph {
	session := Endpoint{Kind: KindSession, Ref: sessionID}
	keep := map[string]bool{}
	edges := make([]Edge, 0)
	for _, e := range g.Edges {
		if e.From != session && e.To != session {
			continue
		}
		edges = append(edges, e)
		if e.From.Kind == KindDecision {
			keep[e.From.Ref] = true
		}
		if e.To.Kind == KindDecision {
			keep[e.To.Ref] = true
		}
	}
	decisions := make([]Record, 0)
	for _, d := range g.Decisions {
		if keep[d.ID] {
			decisions = append(decisions, d)
		}
	}
	return Graph{Decisions: decisions, Edges: edges}
}
