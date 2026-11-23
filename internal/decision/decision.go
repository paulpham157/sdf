// Package decision is an append-only local record of decisions.
//
// A decision is written by a caller. It is never inferred from telemetry.
// Recording one changes nothing about collection or summarization. An edge
// may name a session id or a commit SHA, and only when the store already
// has that session or commit. The reference is not a copy of the telemetry.
package decision

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/paulpham157/sdf/internal/store"
)

// StatusHypothesis is an assumption. It is not a settled fact.
const StatusHypothesis = "hypothesis"

// StatusDecision is a settled choice.
const StatusDecision = "decision"

// DecidedByUser is a choice the user made.
const DecidedByUser = "user"

// DecidedByAgent is a choice the agent made.
const DecidedByAgent = "agent"

const (
	KindDecision = "decision"
	KindSession  = "session"
	KindCommit   = "commit"
)

const (
	RelConstrains  = "constrains"
	RelImplements  = "implements"
	RelSupersedes  = "supersedes"
	RelValidates   = "validates"
	RelContradicts = "contradicts"
	// RelBlocks reads "from blocks to": the to decision cannot be settled or
	// acted on until the from decision is settled. Decision to decision.
	RelBlocks = "blocks"
	// RelConflictsWith says the two decisions cannot both hold. It is
	// symmetric and stored with the endpoints in canonical order.
	RelConflictsWith = "conflicts_with"
	// RelRefines reads "from refines to": the from decision elaborates,
	// narrows, or partly amends the to decision, which still holds.
	RelRefines = "refines"
)

// Relation aliases accepted on input. Each is stored as RelBlocks with the
// endpoints swapped: "b blocked_by a" and "b depends_on a" both store
// "a blocks b".
const (
	AliasBlockedBy = "blocked_by"
	AliasDependsOn = "depends_on"
)

// relationList is the message for an unknown relation.
const relationList = "relation must be constrains, implements, supersedes, validates, contradicts, blocks, conflicts_with, or refines (aliases: blocked_by, depends_on)"

// ErrUnknownRef is returned when an edge names a session or commit the
// store has no telemetry for.
var ErrUnknownRef = errors.New("unknown reference")

var (
	idPattern  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	shaPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)
)

// EffectiveActive, EffectiveSuperseded, and EffectiveRetracted are the values of
// Record.EffectiveStatus. They are computed when the graph is read and are
// never stored.
const (
	EffectiveActive     = "active"
	EffectiveSuperseded = "superseded"
	EffectiveRetracted  = "retracted"
)

// Record is one decision. Status is StatusHypothesis or StatusDecision, as
// stored. Source, DecidedBy, Alternatives, and Reason are empty when the
// record named none. DecidedBy is DecidedByUser or DecidedByAgent when set.
// RecordedAt is UTC RFC3339 with a numeric zone, the same form the store
// uses for received_at.
//
// EffectiveStatus and SupersededBy are not stored. Load computes them from
// the edges: a record is EffectiveSuperseded when at least one supersedes
// edge runs from another decision to it, and SupersededBy lists those
// decisions' ids once each, in edge order. Otherwise it is EffectiveActive
// and SupersededBy is empty.
//
// Retraction is set when the decision was retracted, and then
// EffectiveStatus is EffectiveRetracted whatever the edges say: retracted
// takes precedence over superseded. SupersededBy is still filled, so a
// reader sees both facts.
type Record struct {
	ID              string      `json:"id"`
	Statement       string      `json:"statement"`
	Status          string      `json:"status"`
	RecordedAt      string      `json:"recorded_at"`
	Source          string      `json:"source,omitempty"`
	DecidedBy       string      `json:"decided_by,omitempty"`
	Alternatives    string      `json:"alternatives,omitempty"`
	Reason          string      `json:"reason,omitempty"`
	EffectiveStatus string      `json:"effective_status"`
	SupersededBy    []string    `json:"superseded_by,omitempty"`
	Retraction      *Retraction `json:"retraction,omitempty"`

	// The fields below are derived when the graph is read and never stored.
	// They are omitted from JSON when empty or false. See resolve.
	BlockedBy     []string `json:"blocked_by,omitempty"`
	OpenBlockers  []string `json:"open_blockers,omitempty"`
	Blocked       bool     `json:"blocked,omitempty"`
	ConflictsWith []string `json:"conflicts_with,omitempty"`
	Conflicted    bool     `json:"conflicted,omitempty"`
	RefinedBy     []string `json:"refined_by,omitempty"`
	Challenged    bool     `json:"challenged,omitempty"`
}

// Retraction is the append-only record that a decision was withdrawn. It is
// stored in its own table, never on the decision row. Reason and RetractedBy
// are empty when the caller named none; RetractedBy is DecidedByUser or
// DecidedByAgent when set.
type Retraction struct {
	RetractedAt string `json:"retracted_at"`
	Reason      string `json:"reason,omitempty"`
	RetractedBy string `json:"retracted_by,omitempty"`
}

// RetractInput is what a caller supplies to Retract.
type RetractInput struct {
	ID          string
	Reason      string
	RetractedBy string
}

// Endpoint is one end of an edge. Kind is KindDecision, KindSession, or
// KindCommit. Ref is a decision id, a session id, or a full commit SHA.
type Endpoint struct {
	Kind string `json:"kind"`
	Ref  string `json:"ref"`
}

// Edge connects two endpoints. Relation is one of constrains, implements,
// supersedes, validates, contradicts, blocks, conflicts_with, refines. An
// alias is never stored.
type Edge struct {
	Relation   string   `json:"relation"`
	From       Endpoint `json:"from"`
	To         Endpoint `json:"to"`
	RecordedAt string   `json:"recorded_at"`
}

// Graph is every decision and edge, in insertion order.
type Graph struct {
	Decisions []Record `json:"decisions"`
	Edges     []Edge   `json:"edges"`
}

// RecordInput is what a caller supplies. RecordedAt is filled here.
type RecordInput struct {
	ID           string
	Statement    string
	Status       string
	Source       string
	DecidedBy    string
	Alternatives string
	Reason       string
}

// EdgeInput is what a caller supplies for one edge.
type EdgeInput struct {
	Relation string
	From     Endpoint
	To       Endpoint
}

// Add stores one decision. The id must be new. The statement is one line.
func Add(s *store.Store, in RecordInput) (Record, error) {
	rec, err := newRecord(in, now())
	if err != nil {
		return Record{}, err
	}
	if err := s.InsertDecision(store.Decision{
		ID:           rec.ID,
		Statement:    rec.Statement,
		Status:       rec.Status,
		RecordedAt:   rec.RecordedAt,
		Source:       rec.Source,
		DecidedBy:    rec.DecidedBy,
		Alternatives: rec.Alternatives,
		Reason:       rec.Reason,
	}); err != nil {
		return Record{}, err
	}
	return rec, nil
}

// ErrSelfLink, ErrSupersedesKind, ErrSupersedesCycle, and ErrDuplicateEdge
// are the link rules. Link wraps them with the endpoints involved, so
// errors.Is matches and the message names the edge.
var (
	ErrSelfLink        = errors.New("an edge cannot link an endpoint to itself")
	ErrSupersedesKind  = errors.New("supersedes links a decision to a decision")
	ErrSupersedesCycle = errors.New("supersedes cycle")
	ErrDuplicateEdge   = errors.New("edge already recorded")
	// ErrEndpointKind rejects an endpoint kind the relation does not take.
	// A supersedes kind error matches both it and ErrSupersedesKind.
	ErrEndpointKind = errors.New("endpoint kind not allowed for relation")
	// ErrBlocksCycle and ErrRefinesCycle reject an edge that would close a
	// cycle through edges of the same relation.
	ErrBlocksCycle  = errors.New("blocks cycle")
	ErrRefinesCycle = errors.New("refines cycle")
	// ErrAlreadySuperseded rejects conflicts_with or refines between two
	// decisions when one already supersedes the other.
	ErrAlreadySuperseded = errors.New("pair already linked by supersedes")
	// ErrRetracted rejects a new edge with a retracted decision at either end.
	ErrRetracted = errors.New("decision retracted")
	// ErrAlreadyRetracted rejects retracting a decision a second time, as
	// ErrDuplicateEdge rejects the same edge twice.
	ErrAlreadyRetracted = errors.New("decision already retracted")
)

// Link stores one edge, after LinkWithWarnings' checks. It drops the
// warnings; a caller that reports them uses LinkWithWarnings.
func Link(s *store.Store, in EdgeInput) (Edge, error) {
	edge, _, err := LinkWithWarnings(s, in)
	return edge, err
}

// LinkWithWarnings stores one edge and returns any warnings about it. In
// order, it rejects:
//
//   - a bad relation, kind, or ref;
//   - ErrSelfLink: from and to are the same endpoint, for any relation;
//   - ErrEndpointKind (and ErrSupersedesKind for supersedes): an endpoint
//     kind the relation does not take, see checkKinds;
//   - ErrUnknownRef: a session or commit the store has no telemetry for,
//     or a decision id not stored;
//   - ErrRetracted: either end is a retracted decision;
//   - ErrDuplicateEdge: the same relation, from, and to already stored
//     (for conflicts_with, in either order);
//   - ErrSupersedesCycle, ErrBlocksCycle, ErrRefinesCycle: the edge would
//     close a cycle through decision-to-decision edges of its relation;
//   - ErrAlreadySuperseded: conflicts_with or refines between two decisions
//     one of which already supersedes the other.
//
// It warns, and still stores the edge, when the same two endpoints, in
// either direction, now have both a validates and a contradicts edge, and
// when a blocks edge joins two decisions that already have a supersedes or
// conflicts_with edge.
//
// The retracted, duplicate, cycle, and warning checks read the stored
// decision ids, edges, and retractions in the same transaction as the insert
// (store.AppendDecisionEdge), so concurrent writers cannot slip a change
// between the check and the insert: two links cannot both pass, and a
// decision added meanwhile (for example one that turns a dangling edge into
// a real supersedes step) is seen by the cycle check. Nothing is updated or deleted. Edges an older
// sdf stored without these checks stay as they are; they are read back
// unchanged and count as existing edges here.
// beforeAppendEdge, when set by a test, runs after LinkWithWarnings' checks
// that need no transaction and before store.AppendDecisionEdge. It is nil
// in production.
var beforeAppendEdge func()

func LinkWithWarnings(s *store.Store, in EdgeInput) (Edge, []string, error) {
	edge, err := newEdge(in, now())
	if err != nil {
		return Edge{}, nil, err
	}
	if edge.From == edge.To {
		return Edge{}, nil, fmt.Errorf("%w: %s", ErrSelfLink, endpointText(edge.From))
	}
	if err := checkKinds(edge); err != nil {
		return Edge{}, nil, err
	}
	if err := known(s, edge.From); err != nil {
		return Edge{}, nil, err
	}
	if err := known(s, edge.To); err != nil {
		return Edge{}, nil, err
	}
	if beforeAppendEdge != nil {
		beforeAppendEdge()
	}
	var warnings []string
	err = s.AppendDecisionEdge(store.DecisionEdge{
		Relation:   edge.Relation,
		FromKind:   edge.From.Kind,
		FromRef:    edge.From.Ref,
		ToKind:     edge.To.Kind,
		ToRef:      edge.To.Ref,
		RecordedAt: edge.RecordedAt,
	}, func(g store.DecisionGraph) error {
		for _, r := range g.Retractions {
			for _, end := range []Endpoint{edge.From, edge.To} {
				if end.Kind == KindDecision && end.Ref == r.DecisionID {
					return fmt.Errorf("%w: %s takes no new edges", ErrRetracted, endpointText(end))
				}
			}
		}
		existing := make([]Edge, 0, len(g.Edges))
		for _, r := range g.Edges {
			existing = append(existing, Edge{
				Relation: r.Relation,
				From:     Endpoint{Kind: r.FromKind, Ref: r.FromRef},
				To:       Endpoint{Kind: r.ToKind, Ref: r.ToRef},
			})
		}
		var err error
		warnings, err = checkAgainst(edge, existing, g.DecisionIDs)
		return err
	})
	if err != nil {
		return Edge{}, nil, err
	}
	return edge, warnings, nil
}

// checkAgainst applies the rules that depend on the stored edges.
func checkAgainst(edge Edge, existing []Edge, stored map[string]bool) ([]string, error) {
	for _, e := range existing {
		if e.Relation != edge.Relation {
			continue
		}
		same := e.From == edge.From && e.To == edge.To
		if edge.Relation == RelConflictsWith && e.From == edge.To && e.To == edge.From {
			same = true
		}
		if same {
			return nil, fmt.Errorf("%w: %s %s %s", ErrDuplicateEdge, endpointText(edge.From), edge.Relation, endpointText(edge.To))
		}
	}
	cycles := map[string]struct {
		err  error
		verb string
	}{
		RelSupersedes: {ErrSupersedesCycle, "supersede"},
		RelBlocks:     {ErrBlocksCycle, "block"},
		RelRefines:    {ErrRefinesCycle, "refine"},
	}
	if c, ok := cycles[edge.Relation]; ok {
		if path := relationPath(existing, stored, edge.Relation, edge.To.Ref, edge.From.Ref); path != nil {
			return nil, fmt.Errorf("%w: %s already reaches %s through %s (%s), so %s cannot %s %s",
				c.err, endpointText(edge.To), endpointText(edge.From), edge.Relation, strings.Join(path, " -> "),
				endpointText(edge.From), c.verb, endpointText(edge.To))
		}
	}
	pairHas := func(rel string) bool {
		for _, e := range existing {
			if countsAs(e, stored, rel) && (e.From == edge.From && e.To == edge.To || e.From == edge.To && e.To == edge.From) {
				return true
			}
		}
		return false
	}
	if edge.Relation == RelConflictsWith || edge.Relation == RelRefines {
		if pairHas(RelSupersedes) {
			return nil, fmt.Errorf("%w: %s and %s; one already replaces the other, so %s is not recorded",
				ErrAlreadySuperseded, endpointText(edge.From), endpointText(edge.To), edge.Relation)
		}
	}
	var warnings []string
	if edge.Relation == RelBlocks {
		for _, rel := range []string{RelSupersedes, RelConflictsWith} {
			if pairHas(rel) {
				warnings = append(warnings, fmt.Sprintf("%s and %s also have a %s edge", endpointText(edge.From), endpointText(edge.To), rel))
			}
		}
	}
	if edge.Relation == RelValidates || edge.Relation == RelContradicts {
		other := RelContradicts
		if edge.Relation == RelContradicts {
			other = RelValidates
		}
		for _, e := range existing {
			if e.Relation == other && (e.From == edge.From && e.To == edge.To || e.From == edge.To && e.To == edge.From) {
				warnings = append(warnings, fmt.Sprintf("%s and %s have both validates and contradicts edges", endpointText(edge.From), endpointText(edge.To)))
				break
			}
		}
	}
	return warnings, nil
}

// countsAsSupersedes is the one rule, shared by read time (resolve) and the
// cycle check, for whether a stored edge replaces a decision: relation
// supersedes, two different decision endpoints, and both ids among the
// stored decisions. An older database may hold an edge that fails it (a
// self-edge, a session or commit end, or an id never stored); such an edge
// is still listed but replaces nothing.
func countsAsSupersedes(e Edge, stored map[string]bool) bool {
	return countsAs(e, stored, RelSupersedes)
}

// countsAs is the same rule for any decision-to-decision relation
// (supersedes, blocks, conflicts_with, refines): the relation matches, both
// ends are different decisions, and both ids are stored. Read time and the
// cycle checks use it, so an edge that fails it is listed but drives nothing.
func countsAs(e Edge, stored map[string]bool, rel string) bool {
	return e.Relation == rel &&
		e.From.Kind == KindDecision && e.To.Kind == KindDecision &&
		e.From.Ref != e.To.Ref &&
		stored[e.From.Ref] && stored[e.To.Ref]
}

// checkKinds applies each relation's endpoint kinds. It needs no store, so
// it runs before the refs are looked up.
//
//   - supersedes, blocks, conflicts_with, refines: decision to decision;
//   - implements: session or commit to decision;
//   - validates, contradicts: any kind to a decision;
//   - constrains: from a decision to any kind.
func checkKinds(e Edge) error {
	fail := func(rule, end string, ep Endpoint) error {
		msg := fmt.Sprintf("%s %s: %s is %s", e.Relation, rule, end, endpointText(ep))
		if e.Relation == RelSupersedes {
			return kindError{msg: msg, also: ErrSupersedesKind}
		}
		return kindError{msg: msg}
	}
	switch e.Relation {
	case RelSupersedes, RelBlocks, RelConflictsWith, RelRefines:
		if e.From.Kind != KindDecision {
			return fail("links a decision to a decision", "from", e.From)
		}
		if e.To.Kind != KindDecision {
			return fail("links a decision to a decision", "to", e.To)
		}
	case RelImplements:
		if e.From.Kind != KindSession && e.From.Kind != KindCommit {
			return fail("links a session or commit to a decision", "from", e.From)
		}
		if e.To.Kind != KindDecision {
			return fail("links a session or commit to a decision", "to", e.To)
		}
	case RelValidates, RelContradicts:
		if e.To.Kind != KindDecision {
			return fail("points to a decision", "to", e.To)
		}
	case RelConstrains:
		if e.From.Kind != KindDecision {
			return fail("starts from a decision", "from", e.From)
		}
	}
	return nil
}

// kindError is an endpoint kind error. Its message is the rule alone, for
// example "blocks links a decision to a decision: from is session:s1", and
// errors.Is matches ErrEndpointKind (and also, when set).
type kindError struct {
	msg  string
	also error
}

func (k kindError) Error() string { return k.msg }

func (k kindError) Is(target error) bool {
	return target == ErrEndpointKind || (k.also != nil && target == k.also)
}

// relationPath returns the decision ids from start to goal along edges
// that countsAs rel (from -> to), or nil when goal is not reachable.
// The search is breadth-first, so the path is a shortest one.
func relationPath(edges []Edge, stored map[string]bool, rel, start, goal string) []string {
	next := map[string][]string{}
	for _, e := range edges {
		if countsAs(e, stored, rel) {
			next[e.From.Ref] = append(next[e.From.Ref], e.To.Ref)
		}
	}
	prev := map[string]string{start: ""}
	queue := []string{start}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		if cur == goal {
			var path []string
			for n := goal; n != ""; n = prev[n] {
				path = append([]string{n}, path...)
				if n == start {
					break
				}
			}
			return path
		}
		for _, n := range next[cur] {
			if _, seen := prev[n]; !seen {
				prev[n] = cur
				queue = append(queue, n)
			}
		}
	}
	return nil
}

func endpointText(e Endpoint) string { return e.Kind + ":" + e.Ref }

// Load reads the graph back. An empty store returns empty lists, not nil.
func Load(s *store.Store) (Graph, error) {
	decisions, err := s.LoadDecisions()
	if err != nil {
		return Graph{}, err
	}
	edges, err := s.LoadDecisionEdges()
	if err != nil {
		return Graph{}, err
	}
	retractions, err := s.LoadDecisionRetractions()
	if err != nil {
		return Graph{}, err
	}
	out := Graph{Decisions: make([]Record, 0, len(decisions)), Edges: make([]Edge, 0, len(edges))}
	for _, d := range decisions {
		out.Decisions = append(out.Decisions, Record{
			ID:           d.ID,
			Statement:    d.Statement,
			Status:       d.Status,
			RecordedAt:   d.RecordedAt,
			Source:       d.Source,
			DecidedBy:    d.DecidedBy,
			Alternatives: d.Alternatives,
			Reason:       d.Reason,
		})
	}
	for _, e := range edges {
		out.Edges = append(out.Edges, Edge{
			Relation:   e.Relation,
			From:       Endpoint{Kind: e.FromKind, Ref: e.FromRef},
			To:         Endpoint{Kind: e.ToKind, Ref: e.ToRef},
			RecordedAt: e.RecordedAt,
		})
	}
	byID := make(map[string]*Retraction, len(retractions))
	for _, r := range retractions {
		if _, dup := byID[r.DecisionID]; !dup {
			byID[r.DecisionID] = &Retraction{RetractedAt: r.RecordedAt, Reason: r.Reason, RetractedBy: r.RetractedBy}
		}
	}
	for i := range out.Decisions {
		out.Decisions[i].Retraction = byID[out.Decisions[i].ID]
	}
	resolve(&out)
	return out, nil
}

// Retract records that a decision is withdrawn. The decision must be
// stored (ErrUnknownRef otherwise) and not already retracted
// (ErrAlreadyRetracted). The reason is one line of up to 500 bytes;
// RetractedBy is empty, DecidedByUser, or DecidedByAgent. It appends one
// row to decision_retractions in a BEGIN IMMEDIATE transaction that first
// reads the existing retractions, so concurrent retractions of one id store
// one row. The decision row and its edges are not changed.
func Retract(s *store.Store, in RetractInput) (Retraction, error) {
	id := strings.TrimSpace(in.ID)
	if !idPattern.MatchString(id) {
		return Retraction{}, fmt.Errorf("decision id must be 1-128 letters, digits, dot, underscore, or dash")
	}
	reason := strings.TrimSpace(in.Reason)
	if strings.ContainsAny(reason, "\r\n") || len(reason) > 500 {
		return Retraction{}, fmt.Errorf("reason must be one line")
	}
	by := strings.TrimSpace(in.RetractedBy)
	switch by {
	case "", DecidedByUser, DecidedByAgent:
	default:
		return Retraction{}, fmt.Errorf("retracted-by must be %q or %q", DecidedByUser, DecidedByAgent)
	}
	if err := known(s, Endpoint{Kind: KindDecision, Ref: id}); err != nil {
		return Retraction{}, err
	}
	out := Retraction{RetractedAt: now(), Reason: reason, RetractedBy: by}
	err := s.AppendDecisionRetraction(store.DecisionRetraction{
		DecisionID:  id,
		Reason:      reason,
		RetractedBy: by,
		RecordedAt:  out.RetractedAt,
	}, func(existing []store.DecisionRetraction) error {
		for _, r := range existing {
			if r.DecisionID == id {
				return fmt.Errorf("%w: %s at %s", ErrAlreadyRetracted, id, r.RecordedAt)
			}
		}
		return nil
	})
	if err != nil {
		return Retraction{}, err
	}
	return out, nil
}

// resolve fills EffectiveStatus and SupersededBy from the edges. An edge
// "from supersedes to" means from replaces to. Only an edge whose two
// endpoints are both stored decisions, and are different decisions, counts; any
// other supersedes edge is still listed in Edges but changes no status.
// Replacement is direct, not transitive: when b supersedes a and c
// supersedes b, a is superseded by b and b by c. A reader follows the chain
// through SupersededBy. A retracted record is EffectiveRetracted even when
// it is also superseded, and its SupersededBy is still filled. Retracting a
// decision does not change the status of the decisions it supersedes.
// Nothing here writes to the store.
func resolve(g *Graph) {
	stored := make(map[string]bool, len(g.Decisions))
	for _, d := range g.Decisions {
		stored[d.ID] = true
	}
	by := map[string][]string{}
	seen := map[[2]string]bool{}
	for _, e := range g.Edges {
		if !countsAsSupersedes(e, stored) {
			continue
		}
		pair := [2]string{e.To.Ref, e.From.Ref}
		if seen[pair] {
			continue
		}
		seen[pair] = true
		by[e.To.Ref] = append(by[e.To.Ref], e.From.Ref)
	}
	for i := range g.Decisions {
		d := &g.Decisions[i]
		d.SupersededBy = by[d.ID]
		switch {
		case d.Retraction != nil:
			d.EffectiveStatus = EffectiveRetracted
		case len(d.SupersededBy) > 0:
			d.EffectiveStatus = EffectiveSuperseded
		default:
			d.EffectiveStatus = EffectiveActive
		}
	}
	resolveRelations(g, stored, by)
}

// resolveRelations fills the fields blocks, conflicts_with, refines,
// validates, and contradicts drive. Only an edge that countsAs its relation
// (two different stored decisions) counts for blocks, conflicts_with, and
// refines; validates and contradicts count only when they point to a stored
// decision from another endpoint. Any other stored edge, such as one an
// older sdf wrote before the endpoint kinds were enforced, is listed in
// Edges and drives nothing. Ids are listed once each, in edge order.
//
//   - BlockedBy: every decision with a blocks edge to this one.
//     OpenBlockers: those still unsettled. A blocker is unsettled when any
//     of its current heads is a hypothesis. A head is found by following
//     SupersededBy from the blocker until an active decision is reached; a
//     retracted decision on the way contributes no head, because a withdrawn
//     blocker blocks nothing. So a blocking hypothesis clears once a
//     decision record supersedes it (records never change status, so
//     superseding is how a hypothesis gets settled), or once it is
//     retracted. Blocked: the record is active and OpenBlockers is not
//     empty.
//   - ConflictsWith: the other side of every conflicts_with edge.
//     Conflicted: the record is active and at least one of those is active.
//   - RefinedBy: every decision with a refines edge to this one. The record
//     keeps its own EffectiveStatus.
//   - Challenged: the record is active and the latest validates or
//     contradicts edge to it, in edge order, is contradicts.
func resolveRelations(g *Graph, stored map[string]bool, supersededBy map[string][]string) {
	index := make(map[string]int, len(g.Decisions))
	for i, d := range g.Decisions {
		if _, dup := index[d.ID]; !dup {
			index[d.ID] = i
		}
	}
	add := func(m map[string][]string, seen map[[2]string]bool, key, val string) {
		if seen[[2]string{key, val}] {
			return
		}
		seen[[2]string{key, val}] = true
		m[key] = append(m[key], val)
	}
	blockedBy, conflicts, refinedBy := map[string][]string{}, map[string][]string{}, map[string][]string{}
	seenB, seenC, seenR := map[[2]string]bool{}, map[[2]string]bool{}, map[[2]string]bool{}
	challenged := map[string]bool{}
	for _, e := range g.Edges {
		switch {
		case countsAs(e, stored, RelBlocks):
			add(blockedBy, seenB, e.To.Ref, e.From.Ref)
		case countsAs(e, stored, RelConflictsWith):
			add(conflicts, seenC, e.To.Ref, e.From.Ref)
			add(conflicts, seenC, e.From.Ref, e.To.Ref)
		case countsAs(e, stored, RelRefines):
			add(refinedBy, seenR, e.To.Ref, e.From.Ref)
		case (e.Relation == RelValidates || e.Relation == RelContradicts) &&
			e.To.Kind == KindDecision && stored[e.To.Ref] && e.From != e.To:
			challenged[e.To.Ref] = e.Relation == RelContradicts
		}
	}
	active := func(id string) bool {
		i, ok := index[id]
		return ok && g.Decisions[i].EffectiveStatus == EffectiveActive
	}
	var heads func(id string, seen map[string]bool) []string
	heads = func(id string, seen map[string]bool) []string {
		if seen[id] {
			return nil
		}
		seen[id] = true
		i, ok := index[id]
		if !ok || g.Decisions[i].Retraction != nil {
			return nil
		}
		if next := supersededBy[id]; len(next) > 0 {
			var out []string
			for _, n := range next {
				out = append(out, heads(n, seen)...)
			}
			return out
		}
		return []string{id}
	}
	unsettled := func(blocker string) bool {
		for _, h := range heads(blocker, map[string]bool{}) {
			if g.Decisions[index[h]].Status == StatusHypothesis {
				return true
			}
		}
		return false
	}
	for i := range g.Decisions {
		d := &g.Decisions[i]
		d.BlockedBy = blockedBy[d.ID]
		d.OpenBlockers = nil
		for _, b := range d.BlockedBy {
			if unsettled(b) {
				d.OpenBlockers = append(d.OpenBlockers, b)
			}
		}
		isActive := d.EffectiveStatus == EffectiveActive
		d.Blocked = isActive && len(d.OpenBlockers) > 0
		d.ConflictsWith = conflicts[d.ID]
		d.Conflicted = false
		if isActive {
			for _, c := range d.ConflictsWith {
				if active(c) {
					d.Conflicted = true
					break
				}
			}
		}
		d.RefinedBy = refinedBy[d.ID]
		d.Challenged = isActive && challenged[d.ID]
	}
}

// WriteJSON writes the graph the way the summary JSON is written.
func WriteJSON(w io.Writer, g Graph) error {
	if g.Decisions == nil {
		g.Decisions = []Record{}
	}
	if g.Edges == nil {
		g.Edges = []Edge{}
	}
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	return enc.Encode(g)
}

// WriteText writes the graph as two tables, decisions then edges, in the
// order Load returns them. It reads the same Graph that WriteJSON and
// GET /api/decisions write, so the two views cannot disagree. A cell that
// holds a control character or invalid UTF-8 is printed Go-quoted so it
// cannot break the columns or the terminal. An empty source or decided-by
// prints as "-". EFFECTIVE is the read-time status Load computed, see
// Record. Alternatives and reason, when set, follow the row on indented
// "alternatives: " and "reason: " lines in the STATEMENT column, and a
// retraction on a "retracted: TIME [by WHO][; reason: TEXT]" line.
func WriteText(w io.Writer, g Graph) error {
	if len(g.Decisions) == 0 && len(g.Edges) == 0 {
		_, err := io.WriteString(w, "No decisions in the store.\n")
		return err
	}
	decisions := [][]string{{"ID", "STATUS", "EFFECTIVE", "DECIDED BY", "RECORDED AT", "SOURCE", "STATEMENT"}}
	for _, d := range g.Decisions {
		row := []string{d.ID, d.Status, effectiveText(d) + flagsText(d), orDash(d.DecidedBy), d.RecordedAt, orDash(d.Source), d.Statement}
		decisions = append(decisions, row)
		// Alternatives and reason are free text, up to 500 bytes each, so
		// they go on their own lines under STATEMENT instead of widening
		// every row. The leading cells are empty so the columns still line
		// up. The value is quoted by the same rule as any cell; the label
		// stays outside the quotes.
		for _, extra := range [][2]string{{"alternatives", d.Alternatives}, {"reason", d.Reason}} {
			if extra[1] == "" {
				continue
			}
			line := make([]string, len(row))
			line[len(row)-1] = extra[0] + ": " + cell(extra[1])
			decisions = append(decisions, line)
		}
		for _, extra := range relationLines(d) {
			line := make([]string, len(row))
			line[len(row)-1] = extra
			decisions = append(decisions, line)
		}
		if r := d.Retraction; r != nil {
			text := "retracted: " + cell(r.RetractedAt)
			if r.RetractedBy != "" {
				text += " by " + cell(r.RetractedBy)
			}
			if r.Reason != "" {
				text += "; reason: " + cell(r.Reason)
			}
			line := make([]string, len(row))
			line[len(row)-1] = text
			decisions = append(decisions, line)
		}
	}
	edges := [][]string{{"RELATION", "FROM", "TO", "RECORDED AT"}}
	for _, e := range g.Edges {
		edges = append(edges, []string{e.Relation, endpointText(e.From), endpointText(e.To), e.RecordedAt})
	}
	if err := writeTable(w, fmt.Sprintf("Decisions: %d", len(g.Decisions)), decisions); err != nil {
		return err
	}
	if _, err := io.WriteString(w, "\n"); err != nil {
		return err
	}
	return writeTable(w, fmt.Sprintf("Edges: %d", len(g.Edges)), edges)
}

// flagsText is appended to the EFFECTIVE cell: ", blocked", ", conflicted",
// and ", challenged", in that order, for each derived flag that is set.
func flagsText(d Record) string {
	var b strings.Builder
	for _, f := range []struct {
		on   bool
		name string
	}{{d.Blocked, "blocked"}, {d.Conflicted, "conflicted"}, {d.Challenged, "challenged"}} {
		if f.on {
			b.WriteString(", " + f.name)
		}
	}
	return b.String()
}

// relationLines are the "blocked by: ", "conflicts with: ", and
// "refined by: " lines printed under STATEMENT. A blocked-by line ends with
// "(open: IDS)" when some blockers are still unsettled, or "(settled)".
func relationLines(d Record) []string {
	var out []string
	if len(d.BlockedBy) > 0 {
		text := "blocked by: " + cell(strings.Join(d.BlockedBy, ", "))
		if len(d.OpenBlockers) > 0 {
			text += " (open: " + cell(strings.Join(d.OpenBlockers, ", ")) + ")"
		} else {
			text += " (settled)"
		}
		out = append(out, text)
	}
	if len(d.ConflictsWith) > 0 {
		out = append(out, "conflicts with: "+cell(strings.Join(d.ConflictsWith, ", ")))
	}
	if len(d.RefinedBy) > 0 {
		out = append(out, "refined by: "+cell(strings.Join(d.RefinedBy, ", ")))
	}
	return out
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// effectiveText is the EFFECTIVE cell: "active", or "superseded by" and the
// replacing ids. A record built by hand with no effective status prints "-".
func effectiveText(d Record) string {
	switch {
	case d.EffectiveStatus == EffectiveRetracted && len(d.SupersededBy) > 0:
		return "retracted, superseded by " + strings.Join(d.SupersededBy, ", ")
	case d.EffectiveStatus == EffectiveSuperseded && len(d.SupersededBy) > 0:
		return "superseded by " + strings.Join(d.SupersededBy, ", ")
	case d.EffectiveStatus == "":
		return "-"
	default:
		return d.EffectiveStatus
	}
}

// writeTable prints a title line, then the header and rows aligned by
// column. A table with no rows prints the title only.
func writeTable(w io.Writer, title string, rows [][]string) error {
	if _, err := io.WriteString(w, title+"\n"); err != nil {
		return err
	}
	if len(rows) < 2 {
		return nil
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, row := range rows {
		cells := make([]string, len(row))
		for i, c := range row {
			cells[i] = cell(c)
		}
		if _, err := io.WriteString(tw, strings.Join(cells, "\t")+"\n"); err != nil {
			return err
		}
	}
	return tw.Flush()
}

func cell(s string) string {
	if utf8.ValidString(s) && strings.IndexFunc(s, func(r rune) bool { return !unicode.IsPrint(r) }) < 0 {
		return s
	}
	return strconv.QuoteToASCII(s)
}

func newRecord(in RecordInput, recordedAt string) (Record, error) {
	id := strings.TrimSpace(in.ID)
	if !idPattern.MatchString(id) {
		return Record{}, fmt.Errorf("decision id must be 1-128 letters, digits, dot, underscore, or dash")
	}
	statement := strings.TrimSpace(in.Statement)
	if statement == "" || strings.ContainsAny(statement, "\r\n") || len(statement) > 500 {
		return Record{}, fmt.Errorf("statement must be one non-empty line")
	}
	switch in.Status {
	case StatusHypothesis, StatusDecision:
	default:
		return Record{}, fmt.Errorf("status must be %q or %q", StatusHypothesis, StatusDecision)
	}
	source := strings.TrimSpace(in.Source)
	if strings.ContainsAny(source, "\r\n") || len(source) > 500 {
		return Record{}, fmt.Errorf("source must be one line")
	}
	decidedBy := strings.TrimSpace(in.DecidedBy)
	switch decidedBy {
	case "", DecidedByUser, DecidedByAgent:
	default:
		return Record{}, fmt.Errorf("decided-by must be %q or %q", DecidedByUser, DecidedByAgent)
	}
	alternatives := strings.TrimSpace(in.Alternatives)
	if strings.ContainsAny(alternatives, "\r\n") || len(alternatives) > 500 {
		return Record{}, fmt.Errorf("alternatives must be one line")
	}
	reason := strings.TrimSpace(in.Reason)
	if strings.ContainsAny(reason, "\r\n") || len(reason) > 500 {
		return Record{}, fmt.Errorf("reason must be one line")
	}
	// A new id has no edges yet, so the record starts active.
	return Record{
		ID:              id,
		Statement:       statement,
		Status:          in.Status,
		RecordedAt:      recordedAt,
		Source:          source,
		DecidedBy:       decidedBy,
		Alternatives:    alternatives,
		Reason:          reason,
		EffectiveStatus: EffectiveActive,
	}, nil
}

func newEdge(in EdgeInput, recordedAt string) (Edge, error) {
	switch in.Relation {
	case RelConstrains, RelImplements, RelSupersedes, RelValidates, RelContradicts,
		RelBlocks, RelConflictsWith, RelRefines:
	case AliasBlockedBy, AliasDependsOn:
		in = EdgeInput{Relation: RelBlocks, From: in.To, To: in.From}
	default:
		return Edge{}, fmt.Errorf("%s", relationList)
	}
	from, err := endpoint(in.From)
	if err != nil {
		return Edge{}, fmt.Errorf("from: %w", err)
	}
	to, err := endpoint(in.To)
	if err != nil {
		return Edge{}, fmt.Errorf("to: %w", err)
	}
	// conflicts_with is symmetric: store the smaller endpoint first, so "a
	// conflicts_with b" and "b conflicts_with a" are the same stored edge.
	// Only two decisions are swapped; any other pair is refused by the kind
	// check, whose message then names the ends as the caller gave them.
	if in.Relation == RelConflictsWith && from.Kind == KindDecision && to.Kind == KindDecision &&
		endpointText(to) < endpointText(from) {
		from, to = to, from
	}
	return Edge{Relation: in.Relation, From: from, To: to, RecordedAt: recordedAt}, nil
}

func endpoint(e Endpoint) (Endpoint, error) {
	ref := strings.TrimSpace(e.Ref)
	switch e.Kind {
	case KindDecision, KindSession:
		if !idPattern.MatchString(ref) {
			return Endpoint{}, fmt.Errorf("%s ref must be 1-128 letters, digits, dot, underscore, or dash", e.Kind)
		}
	case KindCommit:
		ref = strings.ToLower(ref)
		if !shaPattern.MatchString(ref) {
			return Endpoint{}, fmt.Errorf("commit ref must be 40 hex characters")
		}
	default:
		return Endpoint{}, fmt.Errorf("kind must be decision, session, or commit")
	}
	return Endpoint{Kind: e.Kind, Ref: ref}, nil
}

func known(s *store.Store, e Endpoint) error {
	switch e.Kind {
	case KindDecision:
		rows, err := s.LoadDecisions()
		if err != nil {
			return err
		}
		for _, row := range rows {
			if row.ID == e.Ref {
				return nil
			}
		}
		return fmt.Errorf("%w: decision %s", ErrUnknownRef, e.Ref)
	case KindSession:
		ok, err := s.KnownSession(e.Ref)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("%w: session %s", ErrUnknownRef, e.Ref)
		}
		return nil
	case KindCommit:
		ok, err := s.KnownCommit(e.Ref)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("%w: commit %s", ErrUnknownRef, e.Ref)
		}
		return nil
	default:
		return fmt.Errorf("kind must be decision, session, or commit")
	}
}

func now() string {
	return time.Now().UTC().Format("2006-01-02T15:04:05.000000-07:00")
}
