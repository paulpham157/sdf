package decision

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/paulpham157/sdf/internal/store"
)

// AuditSchema names the shape of the --json audit document. A change that
// removes or renames a field gets a new name.
const AuditSchema = "sdf.decision.audit/v1"

// SessionSourceEvent and SessionSourceTrailer are the values of
// AuditCommit.Source. Audit only reads the store, which holds the session id
// a stored event carries and never a commit message, so today every finding
// has SessionSourceEvent. SessionSourceTrailer is reserved for a session
// read from a stored SDF-Session trailer, should the store ever hold one.
const (
	SessionSourceEvent   = "event"
	SessionSourceTrailer = "trailer"
)

// AuditOptions bounds what Audit counts and reports. A zero time is no
// bound. Since is inclusive and Until exclusive, so two runs split at one
// instant count every item once. The window applies to a decision's
// recorded_at (findings a, decisions_* totals), a commit's time (findings
// b, commits_* totals) and an edge's recorded_at (findings c,
// edges_checked). Whether a decision has an implements edge, and whether a
// session or commit is linked, is read from every stored edge whatever its
// time, because a link is often recorded after the work.
type AuditOptions struct {
	Since time.Time
	Until time.Time
}

// AuditReport is the result of Audit, and the --json document.
type AuditReport struct {
	Schema       string        `json:"schema"`
	Window       AuditWindow   `json:"window"`
	Totals       AuditTotals   `json:"totals"`
	FindingCount int           `json:"finding_count"`
	Findings     AuditFindings `json:"findings"`
}

// AuditWindow echoes the bounds, RFC3339 in UTC, empty when unbounded.
type AuditWindow struct {
	Since string `json:"since,omitempty"`
	Until string `json:"until,omitempty"`
}

// AuditTotals are the denominators of the findings, counted inside the
// window. A commit is one distinct SHA.
//
//   - DecisionsActive: decisions whose effective_status is active.
//   - DecisionsActiveWithImplements: of those, the ones some implements
//     edge points at (to: decision:ID). DecisionsActive minus this is
//     len(Findings.ActiveWithoutImplements).
//   - CommitsTotal: every commit SHA a stored event names (the SHAs
//     KnownCommit accepts), with or without a session.
//   - CommitsWithSession: of those, the ones at least one event names a
//     session for.
//   - CommitsLinked: commits one of whose sessions has an edge to or from a
//     stored decision, or that have such an edge themselves (commit:SHA).
//     A commit with a session that is not linked is in
//     Findings.UnlinkedSessionCommits.
//   - EdgesChecked: edges, other than supersedes, with at least one end a
//     stored decision. Findings.StaleEdges is a subset.
type AuditTotals struct {
	CommitsTotal                  int `json:"commits_total"`
	CommitsWithSession            int `json:"commits_with_session"`
	CommitsLinked                 int `json:"commits_linked"`
	DecisionsActive               int `json:"decisions_active"`
	DecisionsActiveWithImplements int `json:"decisions_active_with_implements"`
	EdgesChecked                  int `json:"edges_checked"`
}

// AuditFindings lists each kind of finding, in store insertion order. Each
// list is empty, never null, in JSON.
type AuditFindings struct {
	ActiveWithoutImplements []AuditDecision `json:"active_without_implements"`
	UnlinkedSessionCommits  []AuditCommit   `json:"unlinked_session_commits"`
	StaleEdges              []AuditEdge     `json:"stale_edges"`
}

// AuditDecision is an active decision no implements edge points at.
type AuditDecision struct {
	ID         string `json:"id"`
	Status     string `json:"status"`
	RecordedAt string `json:"recorded_at"`
	Statement  string `json:"statement"`
}

// AuditCommit is a commit whose session is not linked to any decision, and
// that has no edge to a decision itself. SessionID is the session the
// commit's first event with a session names; Timestamp is that event's
// record time (UTC); Source says where the session id came from.
type AuditCommit struct {
	SHA       string `json:"sha"`
	SessionID string `json:"session_id"`
	Timestamp string `json:"timestamp"`
	Source    string `json:"source"`
	Event     string `json:"event"`
	Repo      string `json:"repo,omitempty"`
	Branch    string `json:"branch,omitempty"`
}

// AuditEdge is an edge with a retracted or superseded decision at one or
// both ends. Stale lists those ends.
type AuditEdge struct {
	Relation   string          `json:"relation"`
	From       Endpoint        `json:"from"`
	To         Endpoint        `json:"to"`
	RecordedAt string          `json:"recorded_at"`
	Stale      []AuditStaleEnd `json:"stale"`
}

// AuditStaleEnd is one decision end of a stale edge.
type AuditStaleEnd struct {
	End             string   `json:"end"`
	ID              string   `json:"id"`
	EffectiveStatus string   `json:"effective_status"`
	SupersededBy    []string `json:"superseded_by,omitempty"`
}

// Audit reports gaps in the decision graph from stored structure only:
// decisions, edges, retractions (through Load, so effective status is the
// one sdf decision list shows), and the commit SHA and session id stored
// events carry (store.LoadCommitEvents). It reads no log body, message, or
// tool content, infers no decision, and writes nothing.
func Audit(s *store.Store, opt AuditOptions) (AuditReport, error) {
	g, err := Load(s)
	if err != nil {
		return AuditReport{}, err
	}
	events, err := s.LoadCommitEvents()
	if err != nil {
		return AuditReport{}, err
	}
	return audit(g, events, opt), nil
}

func audit(g Graph, events []store.CommitEvent, opt AuditOptions) AuditReport {
	out := AuditReport{
		Schema: AuditSchema,
		Findings: AuditFindings{
			ActiveWithoutImplements: []AuditDecision{},
			UnlinkedSessionCommits:  []AuditCommit{},
			StaleEdges:              []AuditEdge{},
		},
	}
	if !opt.Since.IsZero() {
		out.Window.Since = opt.Since.UTC().Format(time.RFC3339Nano)
	}
	if !opt.Until.IsZero() {
		out.Window.Until = opt.Until.UTC().Format(time.RFC3339Nano)
	}
	byID := make(map[string]Record, len(g.Decisions))
	for _, d := range g.Decisions {
		byID[d.ID] = d
	}
	isDecision := func(e Endpoint) bool {
		if e.Kind != KindDecision {
			return false
		}
		_, ok := byID[e.Ref]
		return ok
	}
	implemented := map[string]bool{}
	linked := map[Endpoint]bool{}
	for _, e := range g.Edges {
		if e.Relation == RelImplements && isDecision(e.To) {
			implemented[e.To.Ref] = true
		}
		if isDecision(e.To) && e.From.Kind != KindDecision {
			linked[e.From] = true
		}
		if isDecision(e.From) && e.To.Kind != KindDecision {
			linked[e.To] = true
		}
	}

	// (a) active decisions with no implements edge.
	for _, d := range g.Decisions {
		if d.EffectiveStatus != EffectiveActive || !opt.within(d.RecordedAt) {
			continue
		}
		out.Totals.DecisionsActive++
		if implemented[d.ID] {
			out.Totals.DecisionsActiveWithImplements++
			continue
		}
		out.Findings.ActiveWithoutImplements = append(out.Findings.ActiveWithoutImplements, AuditDecision{
			ID: d.ID, Status: d.Status, RecordedAt: d.RecordedAt, Statement: d.Statement,
		})
	}

	// (b) commits whose session is not linked. A commit is a distinct SHA;
	// its time is its first stored event's time.
	type commit struct {
		first    store.CommitEvent
		session  *store.CommitEvent
		sessions []string
	}
	var order []string
	commits := map[string]*commit{}
	for _, ev := range events {
		c := commits[ev.SHA]
		if c == nil {
			c = &commit{first: ev}
			commits[ev.SHA] = c
			order = append(order, ev.SHA)
		}
		if ev.SessionID != "" {
			if c.session == nil {
				ev := ev
				c.session = &ev
			}
			c.sessions = append(c.sessions, ev.SessionID)
		}
	}
	for _, sha := range order {
		c := commits[sha]
		if !opt.within(c.first.Time) {
			continue
		}
		out.Totals.CommitsTotal++
		isLinked := linked[Endpoint{Kind: KindCommit, Ref: sha}]
		for _, id := range c.sessions {
			if linked[Endpoint{Kind: KindSession, Ref: id}] {
				isLinked = true
			}
		}
		if isLinked {
			out.Totals.CommitsLinked++
		}
		if c.session == nil {
			continue
		}
		out.Totals.CommitsWithSession++
		if isLinked {
			continue
		}
		out.Findings.UnlinkedSessionCommits = append(out.Findings.UnlinkedSessionCommits, AuditCommit{
			SHA:       sha,
			SessionID: c.session.SessionID,
			Timestamp: c.session.Time,
			Source:    SessionSourceEvent,
			Event:     c.session.Name,
			Repo:      c.session.Repo,
			Branch:    c.session.Branch,
		})
	}

	// (c) edges at a retracted or superseded decision. A supersedes edge is
	// how a decision becomes superseded, so it is not checked.
	for _, e := range g.Edges {
		if e.Relation == RelSupersedes || !opt.within(e.RecordedAt) {
			continue
		}
		if !isDecision(e.From) && !isDecision(e.To) {
			continue
		}
		out.Totals.EdgesChecked++
		var stale []AuditStaleEnd
		for _, end := range []struct {
			name string
			ep   Endpoint
		}{{"from", e.From}, {"to", e.To}} {
			if !isDecision(end.ep) {
				continue
			}
			d := byID[end.ep.Ref]
			if d.EffectiveStatus == EffectiveRetracted || d.EffectiveStatus == EffectiveSuperseded {
				stale = append(stale, AuditStaleEnd{End: end.name, ID: d.ID, EffectiveStatus: d.EffectiveStatus, SupersededBy: d.SupersededBy})
			}
		}
		if len(stale) > 0 {
			out.Findings.StaleEdges = append(out.Findings.StaleEdges, AuditEdge{
				Relation: e.Relation, From: e.From, To: e.To, RecordedAt: e.RecordedAt, Stale: stale,
			})
		}
	}
	out.FindingCount = len(out.Findings.ActiveWithoutImplements) + len(out.Findings.UnlinkedSessionCommits) + len(out.Findings.StaleEdges)
	return out
}

// within reports whether a stored time falls in the window. With no bound
// everything is inside. With a bound, a time that does not parse as
// RFC3339 is outside, since it cannot be placed.
func (o AuditOptions) within(stamp string) bool {
	if o.Since.IsZero() && o.Until.IsZero() {
		return true
	}
	t, err := time.Parse(time.RFC3339Nano, stamp)
	if err != nil {
		return false
	}
	if !o.Since.IsZero() && t.Before(o.Since) {
		return false
	}
	if !o.Until.IsZero() && !t.Before(o.Until) {
		return false
	}
	return true
}

// WriteAuditJSON writes the report as indented JSON, like WriteJSON.
func WriteAuditJSON(w io.Writer, r AuditReport) error {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}

// WriteAuditText writes a summary line, then one section per finding kind
// that has findings. Cells are quoted by the same rule as WriteText.
func WriteAuditText(w io.Writer, r AuditReport) error {
	var b strings.Builder
	t := r.Totals
	if r.Window.Since != "" || r.Window.Until != "" {
		fmt.Fprintf(&b, "Window: since %s, until %s\n", orDash(r.Window.Since), orDash(r.Window.Until))
	}
	if r.FindingCount == 0 {
		fmt.Fprintf(&b, "No findings: %d active decisions, %d commits (%d with a session, %d linked), %d edges checked.\n",
			t.DecisionsActive, t.CommitsTotal, t.CommitsWithSession, t.CommitsLinked, t.EdgesChecked)
		_, err := io.WriteString(w, b.String())
		return err
	}
	fmt.Fprintf(&b, "Findings: %d\n", r.FindingCount)
	if n := len(r.Findings.ActiveWithoutImplements); n > 0 {
		fmt.Fprintf(&b, "\nActive decisions with no implements edge: %d of %d\n", n, t.DecisionsActive)
		for _, d := range r.Findings.ActiveWithoutImplements {
			fmt.Fprintf(&b, "  decision:%s  %s  %s  %s\n", cell(d.ID), cell(d.Status), cell(d.RecordedAt), cell(d.Statement))
		}
	}
	if n := len(r.Findings.UnlinkedSessionCommits); n > 0 {
		fmt.Fprintf(&b, "\nCommits whose session is not linked to a decision: %d of %d with a session\n", n, t.CommitsWithSession)
		for _, c := range r.Findings.UnlinkedSessionCommits {
			fmt.Fprintf(&b, "  commit:%s  session:%s  %s  (%s %s)\n", c.SHA, cell(c.SessionID), cell(c.Timestamp), c.Source, cell(c.Event))
		}
	}
	if n := len(r.Findings.StaleEdges); n > 0 {
		fmt.Fprintf(&b, "\nEdges at a retracted or superseded decision: %d of %d checked\n", n, t.EdgesChecked)
		for _, e := range r.Findings.StaleEdges {
			var why []string
			for _, s := range e.Stale {
				text := s.End + " " + cell(s.ID) + " " + s.EffectiveStatus
				if s.EffectiveStatus == EffectiveSuperseded && len(s.SupersededBy) > 0 {
					text += " by " + cell(strings.Join(s.SupersededBy, ", "))
				}
				why = append(why, text)
			}
			fmt.Fprintf(&b, "  %s  %s  %s  %s  (%s)\n", cell(e.Relation), endpointText(e.From), endpointText(e.To), cell(e.RecordedAt), strings.Join(why, "; "))
		}
	}
	_, err := io.WriteString(w, b.String())
	return err
}
