package decision

import "github.com/paulpham157/sdf/internal/store"

// Query is the read seam sdf decision check uses. The decision query that
// sdf decision brief will call is owned elsewhere and was not on a branch
// when this command was added, so check does not walk edges or the store
// itself. It calls Active and then matches action words in the statements.
//
// Active returns settled decisions that are still in force, in insertion
// order. A record is in force when its status is decision, its
// effective_status is active, and it has no retraction. That drops
// hypotheses, superseded records, and retracted records. It keeps a record
// the user decided and a record the agent decided. Blocked, conflicted, and
// challenged are separate read-time flags and do not remove a record here:
// a settled "do not push" still forbids push while it is active.
//
// loadQuery is the stand-in. Active reads Load, the same graph as
// sdf decision list and GET /api/decisions, and applies the filter above.
// When the brief query lands, change Active so it returns that query's
// in-force decisions (mapped onto Record if the item type differs) instead
// of filtering Load. Do not add a second walk of the edges in check.
//
// Needed from that API if its constraint slice is narrower than this: every
// in-force decision, not only user-locked ones and not only decisions that
// have a constrains or blocks edge. An agent decision whose statement
// forbids push has to be in the slice, with id, statement, and decided_by.
type Query interface {
	Active() ([]Record, error)
}

// NewQuery returns the stand-in query over s. s is read and not written.
func NewQuery(s *store.Store) Query {
	return loadQuery{s: s}
}

type loadQuery struct {
	s *store.Store
}

func (q loadQuery) Active() ([]Record, error) {
	g, err := Load(q.s)
	if err != nil {
		return nil, err
	}
	out := make([]Record, 0)
	for _, d := range g.Decisions {
		if !inForce(d) {
			continue
		}
		out = append(out, d)
	}
	return out, nil
}

// inForce is the Active filter. Check does not apply it again: a Query
// implementation is responsible for returning only these records.
func inForce(d Record) bool {
	return d.Status == StatusDecision && d.EffectiveStatus == EffectiveActive && d.Retraction == nil
}
