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
type Record struct {
	ID              string   `json:"id"`
	Statement       string   `json:"statement"`
	Status          string   `json:"status"`
	RecordedAt      string   `json:"recorded_at"`
	Source          string   `json:"source,omitempty"`
	DecidedBy       string   `json:"decided_by,omitempty"`
	Alternatives    string   `json:"alternatives,omitempty"`
	Reason          string   `json:"reason,omitempty"`
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
//   - ErrSupersedesKind: supersedes with a session or commit endpoint.
//     "from supersedes to" means the from decision replaces the to
//     decision, so both ends are decisions;
//   - ErrUnknownRef: a session or commit the store has no telemetry for,
//     or a decision id not stored;
//   - ErrDuplicateEdge: the same relation, from, and to already stored;
//   - ErrSupersedesCycle: from supersedes to when to already reaches from
//     through decision-to-decision supersedes edges, including to
//     superseding from directly.
//
// It warns, and still stores the edge, when the same two endpoints, in
// either direction, now have both a validates and a contradicts edge.
//
// The duplicate, cycle, and warning checks read the stored edges in the
// same transaction as the insert (store.AppendDecisionEdge), so concurrent
// links cannot both pass them. Nothing is updated or deleted. Edges an older
// sdf stored without these checks stay as they are; they are read back
// unchanged and count as existing edges here.
func LinkWithWarnings(s *store.Store, in EdgeInput) (Edge, []string, error) {
	edge, err := newEdge(in, now())
	if err != nil {
		return Edge{}, nil, err
	}
	if edge.From == edge.To {
		return Edge{}, nil, fmt.Errorf("%w: %s", ErrSelfLink, endpointText(edge.From))
	}
	if edge.Relation == RelSupersedes {
		if edge.From.Kind != KindDecision {
			return Edge{}, nil, fmt.Errorf("%w: from is %s", ErrSupersedesKind, endpointText(edge.From))
		}
		if edge.To.Kind != KindDecision {
			return Edge{}, nil, fmt.Errorf("%w: to is %s", ErrSupersedesKind, endpointText(edge.To))
		}
	}
	if err := known(s, edge.From); err != nil {
		return Edge{}, nil, err
	}
	if err := known(s, edge.To); err != nil {
		return Edge{}, nil, err
	}
	// The stored decision ids, for the cycle check. Decisions are
	// append-only, so reading them before the transaction is safe: an id can
	// appear but never disappear.
	rows, err := s.LoadDecisions()
	if err != nil {
		return Edge{}, nil, err
	}
	stored := make(map[string]bool, len(rows))
	for _, d := range rows {
		stored[d.ID] = true
	}
	var warnings []string
	err = s.AppendDecisionEdge(store.DecisionEdge{
		Relation:   edge.Relation,
		FromKind:   edge.From.Kind,
		FromRef:    edge.From.Ref,
		ToKind:     edge.To.Kind,
		ToRef:      edge.To.Ref,
		RecordedAt: edge.RecordedAt,
	}, func(rows []store.DecisionEdge) error {
		existing := make([]Edge, 0, len(rows))
		for _, r := range rows {
			existing = append(existing, Edge{
				Relation: r.Relation,
				From:     Endpoint{Kind: r.FromKind, Ref: r.FromRef},
				To:       Endpoint{Kind: r.ToKind, Ref: r.ToRef},
			})
		}
		var err error
		warnings, err = checkAgainst(edge, existing, stored)
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
		if e.Relation == edge.Relation && e.From == edge.From && e.To == edge.To {
			return nil, fmt.Errorf("%w: %s %s %s", ErrDuplicateEdge, endpointText(edge.From), edge.Relation, endpointText(edge.To))
		}
	}
	if edge.Relation == RelSupersedes {
		if path := supersedesPath(existing, stored, edge.To.Ref, edge.From.Ref); path != nil {
			return nil, fmt.Errorf("%w: %s already reaches %s through supersedes (%s), so %s cannot supersede %s",
				ErrSupersedesCycle, endpointText(edge.To), endpointText(edge.From), strings.Join(path, " -> "),
				endpointText(edge.From), endpointText(edge.To))
		}
	}
	var warnings []string
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
	return e.Relation == RelSupersedes &&
		e.From.Kind == KindDecision && e.To.Kind == KindDecision &&
		e.From.Ref != e.To.Ref &&
		stored[e.From.Ref] && stored[e.To.Ref]
}

// supersedesPath returns the decision ids from start to goal along edges
// that countsAsSupersedes (from -> to), or nil when goal is not reachable.
// The search is breadth-first, so the path is a shortest one.
func supersedesPath(edges []Edge, stored map[string]bool, start, goal string) []string {
	next := map[string][]string{}
	for _, e := range edges {
		if countsAsSupersedes(e, stored) {
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
	resolve(&out)
	return out, nil
}

// resolve fills EffectiveStatus and SupersededBy from the edges. An edge
// "from supersedes to" means from replaces to. Only an edge whose two
// endpoints are both stored decisions, and are different decisions, counts; any
// other supersedes edge is still listed in Edges but changes no status.
// Replacement is direct, not transitive: when b supersedes a and c
// supersedes b, a is superseded by b and b by c. A reader follows the chain
// through SupersededBy. Nothing here writes to the store.
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
// cannot break the columns or the terminal. An empty source or decided-by
// prints as "-". EFFECTIVE is the read-time status Load computed, see
// Record. Alternatives and reason, when set, follow the row on indented
// "alternatives: " and "reason: " lines in the STATEMENT column.
func WriteText(w io.Writer, g Graph) error {
	if len(g.Decisions) == 0 && len(g.Edges) == 0 {
		_, err := io.WriteString(w, "No decisions in the store.\n")
		return err
	}
	decisions := [][]string{{"ID", "STATUS", "EFFECTIVE", "DECIDED BY", "RECORDED AT", "SOURCE", "STATEMENT"}}
	for _, d := range g.Decisions {
		row := []string{d.ID, d.Status, effectiveText(d), orDash(d.DecidedBy), d.RecordedAt, orDash(d.Source), d.Statement}
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
