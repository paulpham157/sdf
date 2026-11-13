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
	"strings"
	"time"

	"github.com/paulpham157/sdf/internal/store"
)

// StatusHypothesis is an assumption. It is not a settled fact.
const StatusHypothesis = "hypothesis"

// StatusDecision is a settled choice.
const StatusDecision = "decision"

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
)

// ErrUnknownRef is returned when an edge names a session or commit the
// store has no telemetry for.
var ErrUnknownRef = errors.New("unknown reference")

var (
	idPattern  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	shaPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)
)

// Record is one decision. Status is StatusHypothesis or StatusDecision.
// Source is empty when the record named none. RecordedAt is UTC RFC3339
// with a numeric zone, the same form the store uses for received_at.
type Record struct {
	ID         string `json:"id"`
	Statement  string `json:"statement"`
	Status     string `json:"status"`
	RecordedAt string `json:"recorded_at"`
	Source     string `json:"source,omitempty"`
}

// Endpoint is one end of an edge. Kind is KindDecision, KindSession, or
// KindCommit. Ref is a decision id, a session id, or a full commit SHA.
type Endpoint struct {
	Kind string `json:"kind"`
	Ref  string `json:"ref"`
}

// Edge connects two endpoints. Relation is one of constrains, implements,
// supersedes, validates, contradicts.
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
	ID        string
	Statement string
	Status    string
	Source    string
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
		ID:         rec.ID,
		Statement:  rec.Statement,
		Status:     rec.Status,
		RecordedAt: rec.RecordedAt,
		Source:     rec.Source,
	}); err != nil {
		return Record{}, err
	}
	return rec, nil
}

// Link stores one edge. A session or commit endpoint is rejected with
// ErrUnknownRef unless the store already has telemetry for it. A decision
// endpoint must name a decision already stored.
func Link(s *store.Store, in EdgeInput) (Edge, error) {
	edge, err := newEdge(in, now())
	if err != nil {
		return Edge{}, err
	}
	if err := known(s, edge.From); err != nil {
		return Edge{}, err
	}
	if err := known(s, edge.To); err != nil {
		return Edge{}, err
	}
	if err := s.InsertDecisionEdge(store.DecisionEdge{
		Relation:   edge.Relation,
		FromKind:   edge.From.Kind,
		FromRef:    edge.From.Ref,
		ToKind:     edge.To.Kind,
		ToRef:      edge.To.Ref,
		RecordedAt: edge.RecordedAt,
	}); err != nil {
		return Edge{}, err
	}
	return edge, nil
}

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
	out := Graph{Decisions: make([]Record, 0, len(decisions)), Edges: make([]Edge, 0, len(edges))}
	for _, d := range decisions {
		out.Decisions = append(out.Decisions, Record{
			ID:         d.ID,
			Statement:  d.Statement,
			Status:     d.Status,
			RecordedAt: d.RecordedAt,
			Source:     d.Source,
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
	return out, nil
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
	return Record{
		ID:         id,
		Statement:  statement,
		Status:     in.Status,
		RecordedAt: recordedAt,
		Source:     source,
	}, nil
}

func newEdge(in EdgeInput, recordedAt string) (Edge, error) {
	switch in.Relation {
	case RelConstrains, RelImplements, RelSupersedes, RelValidates, RelContradicts:
	default:
		return Edge{}, fmt.Errorf("relation must be constrains, implements, supersedes, validates, or contradicts")
	}
	from, err := endpoint(in.From)
	if err != nil {
		return Edge{}, fmt.Errorf("from: %w", err)
	}
	to, err := endpoint(in.To)
	if err != nil {
		return Edge{}, fmt.Errorf("to: %w", err)
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
