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

// EffectiveActive and EffectiveSuperseded are the values of
// Record.EffectiveStatus. They are computed when the graph is read and are
// never stored.
const (
	EffectiveActive     = "active"
	EffectiveSuperseded = "superseded"
)

// Record is one decision. Status is StatusHypothesis or StatusDecision, as
// stored. Source is empty when the record named none. RecordedAt is UTC
// RFC3339 with a numeric zone, the same form the store uses for received_at.
//
// EffectiveStatus and SupersededBy are not stored. Load computes them from
// the edges: a record is EffectiveSuperseded when at least one supersedes
// edge runs from another decision to it, and SupersededBy lists those
// decisions' ids once each, in edge order. Otherwise it is EffectiveActive
// and SupersededBy is empty.
type Record struct {
	ID              string   `json:"id"`
	Statement       string   `json:"statement"`
	Status          string   `json:"status"`
	RecordedAt      string   `json:"recorded_at"`
	Source          string   `json:"source,omitempty"`
	EffectiveStatus string   `json:"effective_status"`
	SupersededBy    []string `json:"superseded_by,omitempty"`
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
	resolve(&out)
	return out, nil
}

// resolve fills EffectiveStatus and SupersededBy from the edges. An edge
// "from supersedes to" means from replaces to. Only an edge whose two
// endpoints are both decisions, and are different decisions, counts; any
// other supersedes edge is still listed in Edges but changes no status.
// Replacement is direct, not transitive: when b supersedes a and c
// supersedes b, a is superseded by b and b by c. A reader follows the chain
// through SupersededBy. Nothing here writes to the store.
func resolve(g *Graph) {
	by := map[string][]string{}
	seen := map[[2]string]bool{}
	for _, e := range g.Edges {
		if e.Relation != RelSupersedes || e.From.Kind != KindDecision || e.To.Kind != KindDecision || e.From.Ref == e.To.Ref {
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
		if len(d.SupersededBy) > 0 {
			d.EffectiveStatus = EffectiveSuperseded
		} else {
			d.EffectiveStatus = EffectiveActive
		}
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
// cannot break the columns or the terminal. An empty source prints as "-".
// EFFECTIVE is the read-time status Load computed, see Record.
func WriteText(w io.Writer, g Graph) error {
	if len(g.Decisions) == 0 && len(g.Edges) == 0 {
		_, err := io.WriteString(w, "No decisions in the store.\n")
		return err
	}
	decisions := [][]string{{"ID", "STATUS", "EFFECTIVE", "RECORDED AT", "SOURCE", "STATEMENT"}}
	for _, d := range g.Decisions {
		source := d.Source
		if source == "" {
			source = "-"
		}
		decisions = append(decisions, []string{d.ID, d.Status, effectiveText(d), d.RecordedAt, source, d.Statement})
	}
	edges := [][]string{{"RELATION", "FROM", "TO", "RECORDED AT"}}
	for _, e := range g.Edges {
		edges = append(edges, []string{e.Relation, e.From.Kind + ":" + e.From.Ref, e.To.Kind + ":" + e.To.Ref, e.RecordedAt})
	}
	if err := writeTable(w, fmt.Sprintf("Decisions: %d", len(g.Decisions)), decisions); err != nil {
		return err
	}
	if _, err := io.WriteString(w, "\n"); err != nil {
		return err
	}
	return writeTable(w, fmt.Sprintf("Edges: %d", len(g.Edges)), edges)
}

// effectiveText is the EFFECTIVE cell: "active", or "superseded by" and the
// replacing ids. A record built by hand with no effective status prints "-".
func effectiveText(d Record) string {
	switch {
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
	// A new id has no edges yet, so the record starts active.
	return Record{
		ID:              id,
		Statement:       statement,
		Status:          in.Status,
		RecordedAt:      recordedAt,
		Source:          source,
		EffectiveStatus: EffectiveActive,
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
