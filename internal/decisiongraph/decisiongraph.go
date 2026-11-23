// Package decisiongraph arranges the decision records for the Decisions tab.
//
// It reads a decision.Graph and the summarized sessions and returns a View:
// chapters, each a connected component of decisions, with nodes placed in
// three lanes (the chosen-path spine, a left lane for abandoned routes and
// endings, and a right lane for side decisions and not-taken alternatives),
// the edges between them, the endings the chapter reached, and the counts the
// page headline shows. Build is pure: the same inputs give the same View, so
// GET /api/decisions/graph and sdf decision graph --json print the same bytes.
// Nothing here writes to the store, and nothing is inferred from telemetry
// content: a node exists only for a stored decision, an alternative named on
// one, or a session outcome that a stored edge points at.
package decisiongraph

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"sort"
	"strings"

	"github.com/paulpham157/sdf/internal/decision"
	"github.com/paulpham157/sdf/internal/store"
	"github.com/paulpham157/sdf/internal/summary"
)

// CompactThreshold is the node count above which a chapter collapses every
// not-taken alternative into a "+N not taken" pill on its parent.
const CompactThreshold = 25

// Node kinds.
const (
	KindStart       = "start"
	KindDecision    = "decision"
	KindAlternative = "alternative"
	KindEnding      = "ending"
)

// Lanes. LaneNone is an alternative collapsed into its parent's pill.
const (
	LaneSpine = "spine"
	LaneLeft  = "left"
	LaneRight = "right"
	LaneNone  = "none"
)

// Node roles. A role says how a node is drawn; it is derived, never stored.
const (
	RolePath         = "path"          // active decision on the chosen path
	RoleLocked       = "locked"        // active decision on the path with an open blocker
	RoleHypothesis   = "hypothesis"    // active hypothesis, off the path
	RoleRefinement   = "refinement"    // refines another decision, off the path
	RoleSuperseded   = "superseded"    // replaced by a later decision
	RoleRetracted    = "retracted"     // withdrawn
	RoleNotTaken     = "not_taken"     // an alternative named on a decision
	RoleReached      = "reached"       // ending that happened
	RoleUnreached    = "unreached"     // ending that has not happened, for example an open PR
	RoleLockedEnding = "locked_ending" // ending behind a blocked decision
)

// Derived relations. Stored relations keep their decision package names.
const (
	RelNext     = "next"      // chosen-path sequence, by recorded_at
	RelAfter    = "after"     // an off-path decision recorded after this path decision
	RelNotTaken = "not_taken" // decision -> one of its alternatives
	RelOutcome  = "outcome"   // decision -> ending
)

// View is the whole document.
type View struct {
	Chapters      []Chapter      `json:"chapters"`
	RelationTypes []RelationType `json:"relation_types"`
}

// RelationType tells the page how to draw an edge. A relation the page does
// not know is drawn with Style "generic", so a new relation needs no page
// change to appear.
type RelationType struct {
	Name      string `json:"name"`
	Style     string `json:"style"`
	Glyph     string `json:"glyph,omitempty"`
	Symmetric bool   `json:"symmetric,omitempty"`
	Tension   bool   `json:"tension,omitempty"`
	Derived   bool   `json:"derived,omitempty"`
	Label     string `json:"label"`
}

// Chapter is one connected component of decisions.
type Chapter struct {
	ID           string      `json:"id"`
	Title        string      `json:"title"`
	LabelSource  string      `json:"label_source"`
	Branches     []string    `json:"branches"`
	PullRequests []int       `json:"pull_requests"`
	Sessions     []string    `json:"sessions"`
	Start        string      `json:"start"`
	End          string      `json:"end"`
	Cost         Cost        `json:"cost"`
	Facts        Facts       `json:"facts"`
	Explored     Explored    `json:"explored"`
	Attention    []Attention `json:"needs_attention"`
	Compact      bool        `json:"compact"`
	Rows         int         `json:"rows"`
	Nodes        []Node      `json:"nodes"`
	Edges        []Edge      `json:"edges"`
}

// Cost is the computed cost of the chapter's sessions. Status is "known" when
// every session has a known cost, "partial" when some do, and "unknown" when
// none do or there are no sessions. USD is empty when Status is "unknown".
type Cost struct {
	Status string `json:"status"`
	USD    string `json:"usd,omitempty"`
}

// Facts is the panel headline: "4 of 5 endings reached · 1 locked · 1 conflict".
type Facts struct {
	EndingsReached int `json:"endings_reached"`
	EndingsTotal   int `json:"endings_total"`
	Locked         int `json:"locked"`
	Conflicts      int `json:"conflicts"`
	Challenged     int `json:"challenged"`
}

// Explored counts choice points only. A refinement or a commit is never
// one. Each other record counts once:
//
//   - Locked: it is blocked (active with an open blocker), whether it is a
//     decision or a hypothesis. Facts.Locked counts the same records.
//   - Decided: any other decision record, including one that was later
//     superseded or retracted: the choice was made when it was recorded.
//   - Hypotheses: any other hypothesis that is still active.
//
// A superseded or retracted hypothesis is not a choice point any more (a
// later record settled or withdrew it). NotTaken adds the alternatives of
// every record counted above. Percent is nil when there are none.
type Explored struct {
	Decided    int  `json:"decided"`
	NotTaken   int  `json:"not_taken"`
	Hypotheses int  `json:"hypotheses"`
	Locked     int  `json:"locked"`
	Total      int  `json:"total"`
	Percent    *int `json:"percent"`
}

// Attention is one "Needs attention" row. Nodes are the ids to focus.
type Attention struct {
	Kind  string   `json:"kind"` // conflict, locked, challenged
	Nodes []string `json:"nodes"`
	Text  string   `json:"text"`
}

// Node is one box on the canvas.
type Node struct {
	ID           string           `json:"id"`
	Kind         string           `json:"kind"`
	Role         string           `json:"role"`
	Lane         string           `json:"lane"`
	Row          int              `json:"row"`
	Title        string           `json:"title"`
	Detail       string           `json:"detail,omitempty"`
	DecidedBy    string           `json:"decided_by,omitempty"`
	OnPath       bool             `json:"on_path"`
	Parent       string           `json:"parent,omitempty"`
	Command      string           `json:"command,omitempty"`
	Alternatives []string         `json:"alternatives,omitempty"`
	Decision     *decision.Record `json:"decision,omitempty"`
	Links        []Link           `json:"links,omitempty"`
	Ending       *Ending          `json:"ending,omitempty"`
}

// Link is one stored edge seen from a decision node. Direction is "in" when
// the edge points at this decision and "out" when it starts here.
type Link struct {
	Relation   string `json:"relation"`
	Direction  string `json:"direction"`
	Kind       string `json:"kind"`
	Ref        string `json:"ref"`
	RecordedAt string `json:"recorded_at"`
}

// Ending is an outcome a chapter reached or is waiting on.
type Ending struct {
	Type     string   `json:"type"` // pull_request, commit, pending
	Reached  bool     `json:"reached"`
	Locked   bool     `json:"locked"`
	PR       int      `json:"pull_request,omitempty"`
	PRState  string   `json:"pull_request_state,omitempty"`
	URL      string   `json:"url,omitempty"`
	SHA      string   `json:"sha,omitempty"`
	Sessions []string `json:"sessions"`
	Via      []string `json:"via"`
	Cost     Cost     `json:"cost"`
}

// Edge is one line on the canvas. From and To are node ids.
type Edge struct {
	From       string `json:"from"`
	To         string `json:"to"`
	Relation   string `json:"relation"`
	Derived    bool   `json:"derived"`
	OnPath     bool   `json:"on_path"`
	Tension    bool   `json:"tension"`
	RecordedAt string `json:"recorded_at,omitempty"`
}

// Relations lists every relation the page draws, stored then derived.
func Relations() []RelationType {
	return []RelationType{
		{Name: decision.RelConstrains, Style: "rule", Label: "constrains"},
		{Name: decision.RelImplements, Style: "evidence", Label: "implements"},
		{Name: decision.RelSupersedes, Style: "supersedes", Glyph: "↶", Label: "supersedes"},
		{Name: decision.RelValidates, Style: "evidence", Label: "validates"},
		{Name: decision.RelContradicts, Style: "crack", Glyph: "⚡", Tension: true, Label: "contradicts"},
		{Name: decision.RelBlocks, Style: "lock", Glyph: "🔒", Label: "blocks"},
		{Name: decision.RelConflictsWith, Style: "crack", Glyph: "⚡", Symmetric: true, Tension: true, Label: "conflicts with"},
		{Name: decision.RelRefines, Style: "refines", Glyph: "↑", Label: "refines"},
		{Name: RelNext, Style: "path", Derived: true, Label: "then"},
		{Name: RelAfter, Style: "anchor", Derived: true, Label: "recorded after"},
		{Name: RelNotTaken, Style: "ghost", Derived: true, Label: "not taken"},
		{Name: RelOutcome, Style: "outcome", Derived: true, Label: "leads to"},
	}
}

// Build arranges g into chapters. sessions is the summarized report's session
// list; it supplies branches, pull requests, linked commits, and cost. A nil
// slice is fine: chapters then fall back to session ids and decision ids.
func Build(g decision.Graph, sessions []summary.SessionSummary) View {
	b := newBuilder(g, sessions)
	out := View{Chapters: []Chapter{}, RelationTypes: Relations()}
	for _, comp := range b.components() {
		out.Chapters = append(out.Chapters, b.chapter(comp))
	}
	sort.SliceStable(out.Chapters, func(i, j int) bool {
		if out.Chapters[i].End != out.Chapters[j].End {
			return out.Chapters[i].End > out.Chapters[j].End
		}
		return out.Chapters[i].ID < out.Chapters[j].ID
	})
	return out
}

// FromStore loads the decision records and builds the View against the
// sessions of report, the summary the caller already computed.
func FromStore(s *store.Store, report summary.Report) (View, error) {
	g, err := decision.Load(s)
	if err != nil {
		return View{}, err
	}
	return Build(g, report.Sessions), nil
}

// WriteJSON writes v the way the decisions and summary JSON are written.
func WriteJSON(w io.Writer, v View) error {
	if v.Chapters == nil {
		v.Chapters = []Chapter{}
	}
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// WriteText writes one line per chapter: the facts headline and the explored
// count. It reads the same View as WriteJSON.
func WriteText(w io.Writer, v View) error {
	if len(v.Chapters) == 0 {
		_, err := fmt.Fprintln(w, "no decision")
		return err
	}
	for _, c := range v.Chapters {
		pct := "-"
		if c.Explored.Percent != nil {
			pct = fmt.Sprintf("%d%%", *c.Explored.Percent)
		}
		if _, err := fmt.Fprintf(w, "%s\t%d of %d endings reached · %d locked · %d conflict · %d challenged · %d/%d decided (%s)\n",
			c.Title, c.Facts.EndingsReached, c.Facts.EndingsTotal, c.Facts.Locked, c.Facts.Conflicts, c.Facts.Challenged,
			c.Explored.Decided, c.Explored.Total, pct); err != nil {
			return err
		}
		for _, a := range c.Attention {
			if _, err := fmt.Fprintf(w, "\t%s: %s\n", a.Kind, a.Text); err != nil {
				return err
			}
		}
	}
	return nil
}

type builder struct {
	g        decision.Graph
	index    map[string]int // decision id -> position in g.Decisions
	sessions map[string]*summary.SessionSummary
	order    map[string]int    // session id -> position in the report
	commitTo map[string]string // full commit SHA -> session id

	// Edge indexes, built once so a chapter or a node reads only its own
	// edges instead of every stored edge.
	incident   map[string][]int         // decision id -> indexes into g.Edges with it at either end, ascending, once each
	refinement map[string]bool          // decision id -> has a refines edge to another stored decision
	evidence   map[string]decision.Edge // decision id -> latest counted validates/contradicts (decision.LatestEvidenceByDecision)
}

func newBuilder(g decision.Graph, sessions []summary.SessionSummary) *builder {
	b := &builder{g: g, index: map[string]int{}, sessions: map[string]*summary.SessionSummary{}, order: map[string]int{}, commitTo: map[string]string{},
		incident: map[string][]int{}, refinement: map[string]bool{}}
	for i, d := range g.Decisions {
		if _, dup := b.index[d.ID]; !dup {
			b.index[d.ID] = i
		}
	}
	for i, e := range g.Edges {
		if e.From.Kind == decision.KindDecision {
			b.incident[e.From.Ref] = append(b.incident[e.From.Ref], i)
		}
		if e.To.Kind == decision.KindDecision && !(e.From.Kind == decision.KindDecision && e.From.Ref == e.To.Ref) {
			b.incident[e.To.Ref] = append(b.incident[e.To.Ref], i)
		}
		if e.Relation == decision.RelRefines && e.From.Kind == decision.KindDecision && b.stored(e.To) && e.To.Ref != e.From.Ref {
			b.refinement[e.From.Ref] = true
		}
	}
	b.evidence = decision.LatestEvidenceByDecision(g)
	for i := range sessions {
		s := &sessions[i]
		if _, dup := b.sessions[s.SessionID]; dup {
			continue
		}
		b.sessions[s.SessionID] = s
		b.order[s.SessionID] = i
		for _, sha := range sessionCommits(s) {
			if _, taken := b.commitTo[sha]; !taken {
				b.commitTo[sha] = s.SessionID
			}
		}
	}
	return b
}

func sessionCommits(s *summary.SessionSummary) []string {
	var out []string
	for _, c := range s.Repository.HookCommits {
		out = append(out, strings.ToLower(c.SHA))
	}
	for _, sha := range s.Repository.CommitSHAs {
		out = append(out, strings.ToLower(sha))
	}
	if s.Outcome != nil {
		for _, c := range s.Outcome.Commits {
			out = append(out, strings.ToLower(c.SHA))
		}
	}
	return out
}

func (b *builder) stored(e decision.Endpoint) bool {
	if e.Kind != decision.KindDecision {
		return false
	}
	_, ok := b.index[e.Ref]
	return ok
}

// groupKey is the key a session or commit endpoint joins decisions under:
// a commit belongs to its session when the report attributes it to one.
func (b *builder) groupKey(e decision.Endpoint) string {
	switch e.Kind {
	case decision.KindSession:
		return "session:" + e.Ref
	case decision.KindCommit:
		if s, ok := b.commitTo[e.Ref]; ok {
			return "session:" + s
		}
		return "commit:" + e.Ref
	}
	return ""
}

// components returns the decision ids of each chapter in recorded order.
// Two decisions share a chapter when an edge joins them, or when both are
// linked to the same session (directly or through a commit of it).
func (b *builder) components() [][]string {
	parent := map[string]string{}
	find := func(x string) string {
		for parent[x] != x {
			parent[x] = parent[parent[x]]
			x = parent[x]
		}
		return x
	}
	union := func(a, c string) {
		ra, rc := find(a), find(c)
		if ra == rc {
			return
		}
		// Keep the earlier-recorded decision as the root, so ids are stable.
		if b.before(rc, ra) {
			ra, rc = rc, ra
		}
		parent[rc] = ra
	}
	for _, d := range b.g.Decisions {
		if _, ok := parent[d.ID]; !ok {
			parent[d.ID] = d.ID
		}
	}
	group := map[string]string{}
	for _, e := range b.g.Edges {
		fromD, toD := b.stored(e.From), b.stored(e.To)
		switch {
		case fromD && toD:
			union(e.From.Ref, e.To.Ref)
		case fromD || toD:
			id, other := e.From.Ref, e.To
			if toD {
				id, other = e.To.Ref, e.From
			}
			key := b.groupKey(other)
			if key == "" {
				continue
			}
			if first, ok := group[key]; ok {
				union(first, id)
			} else {
				group[key] = id
			}
		}
	}
	byRoot := map[string][]string{}
	var roots []string
	for _, id := range b.sorted(keys(parent)) {
		r := find(id)
		if _, ok := byRoot[r]; !ok {
			roots = append(roots, r)
		}
		byRoot[r] = append(byRoot[r], id)
	}
	out := make([][]string, 0, len(roots))
	for _, r := range roots {
		out = append(out, byRoot[r])
	}
	return out
}

func keys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// before orders decisions by recorded_at, then by insertion order.
func (b *builder) before(x, y string) bool {
	dx, dy := b.g.Decisions[b.index[x]], b.g.Decisions[b.index[y]]
	if dx.RecordedAt != dy.RecordedAt {
		return dx.RecordedAt < dy.RecordedAt
	}
	return b.index[x] < b.index[y]
}

func (b *builder) sorted(ids []string) []string {
	out := append([]string(nil), ids...)
	sort.SliceStable(out, func(i, j int) bool { return b.before(out[i], out[j]) })
	return out
}

func (b *builder) rec(id string) *decision.Record { return &b.g.Decisions[b.index[id]] }

func (b *builder) isRefinement(id string) bool { return b.refinement[id] }

// chapterEdges is the indexes of every stored edge with a decision of ids
// at either end, ascending (edge order), once each.
func (b *builder) chapterEdges(ids []string) []int {
	seen := map[int]bool{}
	var out []int
	for _, id := range ids {
		for _, i := range b.incident[id] {
			if !seen[i] {
				seen[i] = true
				out = append(out, i)
			}
		}
	}
	sort.Ints(out)
	return out
}

func (b *builder) role(id string) (role, lane string) {
	d := b.rec(id)
	switch {
	case d.EffectiveStatus == decision.EffectiveRetracted:
		return RoleRetracted, LaneLeft
	case d.EffectiveStatus == decision.EffectiveSuperseded:
		return RoleSuperseded, LaneLeft
	case b.isRefinement(id):
		return RoleRefinement, LaneRight
	case d.Status == decision.StatusHypothesis:
		return RoleHypothesis, LaneRight
	case d.Blocked:
		return RoleLocked, LaneSpine
	default:
		return RolePath, LaneSpine
	}
}

// SplitAlternatives splits the one-line alternatives text into options. It
// splits on ";" when present, else on ", or ", else keeps the whole text.
// Empty parts are dropped.
func SplitAlternatives(text string) []string {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	var parts []string
	switch {
	case strings.Contains(text, ";"):
		parts = strings.Split(text, ";")
	case strings.Contains(text, ", or "):
		parts = strings.Split(text, ", or ")
	default:
		parts = []string{text}
	}
	var out []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		p = strings.TrimPrefix(p, "or ")
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// WhatIfCommand is the command the page copies for a not-taken alternative.
// It records the alternative as a new hypothesis; the page never runs it.
//
// The new id is PARENT-alt-N. A decision id is at most 128 characters, so
// a parent longer than 110 is cut to 100 and followed by the first 8 hex
// digits of its SHA-256: two long parents that share a prefix still get
// different ids.
func WhatIfCommand(parent string, n int, text string) string {
	base := parent
	if len(base) > 110 {
		sum := sha256.Sum256([]byte(parent))
		base = base[:100] + "-" + hex.EncodeToString(sum[:4])
	}
	id := fmt.Sprintf("%s-alt-%d", base, n)
	return "sdf decision add --id " + id + " --status hypothesis --statement " + shellQuote(text) +
		" --source " + shellQuote("what-if: alternative to "+parent)
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

type slots map[string]map[int]bool

func (s slots) take(lane string, from int) int {
	if s[lane] == nil {
		s[lane] = map[int]bool{}
	}
	r := from
	for s[lane][r] {
		r++
	}
	s[lane][r] = true
	return r
}

func (b *builder) chapter(ids []string) Chapter {
	ch := Chapter{
		ID:           "ch-" + ids[0],
		Branches:     []string{},
		PullRequests: []int{},
		Sessions:     []string{},
		Attention:    []Attention{},
		Start:        b.rec(ids[0]).RecordedAt,
		End:          b.rec(ids[len(ids)-1]).RecordedAt,
	}
	in := map[string]bool{}
	for _, id := range ids {
		in[id] = true
	}

	// Sessions and commits linked to the chapter, and which decisions they
	// carry out (implements, validates) or merely touch.
	type outcomeLink struct {
		strong, weak []string
		seen         map[[2]string]bool // {"strong"|"weak", id}
	}
	sessionVia := map[string]*outcomeLink{}
	commitVia := map[string]*outcomeLink{}
	var sessionOrder, commitOrder []string
	note := func(m map[string]*outcomeLink, order *[]string, key, id, rel string) {
		l, ok := m[key]
		if !ok {
			l = &outcomeLink{seen: map[[2]string]bool{}}
			m[key] = l
			*order = append(*order, key)
		}
		strong := rel == decision.RelImplements || rel == decision.RelValidates
		k := [2]string{"weak", id}
		if strong {
			k[0] = "strong"
		}
		if l.seen[k] {
			return
		}
		l.seen[k] = true
		if strong {
			l.strong = append(l.strong, id)
		} else {
			l.weak = append(l.weak, id)
		}
	}
	chEdges := b.chapterEdges(ids)
	for _, ei := range chEdges {
		e := b.g.Edges[ei]
		for _, pair := range [][2]decision.Endpoint{{e.From, e.To}, {e.To, e.From}} {
			self, other := pair[0], pair[1]
			if !b.stored(self) || !in[self.Ref] {
				continue
			}
			switch other.Kind {
			case decision.KindSession:
				note(sessionVia, &sessionOrder, other.Ref, self.Ref, e.Relation)
			case decision.KindCommit:
				// A known commit puts its session in the chapter. The commit
				// is still its own ending unless that session has a linked
				// outcome, which then stands for it.
				if s, ok := b.commitTo[other.Ref]; ok {
					note(sessionVia, &sessionOrder, s, self.Ref, e.Relation)
				}
				note(commitVia, &commitOrder, other.Ref, self.Ref, e.Relation)
			}
		}
	}
	sort.SliceStable(sessionOrder, func(i, j int) bool { return b.sessionBefore(sessionOrder[i], sessionOrder[j]) })
	ch.Sessions = append(ch.Sessions, sessionOrder...)

	// Chapter label: branch, then pull request, then session, then decision.
	for _, sid := range sessionOrder {
		s := b.sessions[sid]
		if s == nil {
			continue
		}
		for _, c := range s.Repository.HookCommits {
			if c.Branch != "" {
				ch.Branches = appendOnce(ch.Branches, c.Branch)
			}
		}
		if s.Outcome != nil && s.Outcome.PullRequest != nil {
			ch.PullRequests = appendOnceInt(ch.PullRequests, s.Outcome.PullRequest.Number)
		}
	}
	switch {
	case len(ch.Branches) == 1:
		ch.Title, ch.LabelSource = ch.Branches[0], "branch"
	case len(ch.Branches) > 1:
		ch.Title, ch.LabelSource = ch.Branches[0]+" → "+ch.Branches[len(ch.Branches)-1], "branch"
	case len(ch.PullRequests) == 1:
		ch.Title, ch.LabelSource = fmt.Sprintf("PR #%d", ch.PullRequests[0]), "pr"
	case len(ch.PullRequests) > 1:
		ch.Title, ch.LabelSource = fmt.Sprintf("PR #%d → #%d", ch.PullRequests[0], ch.PullRequests[len(ch.PullRequests)-1]), "pr"
	case len(sessionOrder) > 0:
		ch.Title, ch.LabelSource = "session "+short(sessionOrder[0], 12), "session"
	default:
		ch.Title, ch.LabelSource = ids[0], "decision"
	}
	ch.Cost = b.cost(sessionOrder)

	// Endings: one per pull request or, without one, per linked commit.
	type endingAcc struct {
		node    Node
		via     []string
		viaSeen map[string]bool
	}
	var endings []*endingAcc
	byKey := map[string]*endingAcc{}
	addEnding := func(key string, e Ending, title string, via []string, sid string) {
		acc, ok := byKey[key]
		if !ok {
			acc = &endingAcc{node: Node{ID: "ending:" + key, Kind: KindEnding, Title: title}, viaSeen: map[string]bool{}}
			e.Sessions, e.Via = []string{}, []string{}
			acc.node.Ending = &e
			byKey[key] = acc
			endings = append(endings, acc)
		}
		if sid != "" {
			acc.node.Ending.Sessions = appendOnce(acc.node.Ending.Sessions, sid)
		}
		for _, id := range via {
			if !acc.viaSeen[id] {
				acc.viaSeen[id] = true
				acc.via = append(acc.via, id)
			}
		}
	}
	viaOf := func(l *outcomeLink) []string {
		if len(l.strong) > 0 {
			return l.strong
		}
		return l.weak
	}
	covered := map[string]bool{}
	for _, sid := range sessionOrder {
		s := b.sessions[sid]
		if s == nil || s.Outcome == nil || s.Outcome.Status != "linked" {
			continue
		}
		covered[sid] = true
		via := viaOf(sessionVia[sid])
		if pr := s.Outcome.PullRequest; pr != nil {
			title := fmt.Sprintf("PR #%d merged", pr.Number)
			if !pr.Merged {
				state := pr.State
				if state == "" {
					state = "not merged"
				}
				title = fmt.Sprintf("PR #%d %s", pr.Number, state)
			}
			addEnding(fmt.Sprintf("pr-%d", pr.Number), Ending{Type: "pull_request", Reached: pr.Merged, PR: pr.Number, PRState: pr.State, URL: pr.URL}, title, via, sid)
			continue
		}
		if len(s.Outcome.Commits) > 0 {
			sha := strings.ToLower(s.Outcome.Commits[0].SHA)
			addEnding("commit-"+sha, Ending{Type: "commit", Reached: true, SHA: sha}, "Commit "+short(sha, 7), via, sid)
		}
	}
	for _, sha := range commitOrder {
		if sid, ok := b.commitTo[sha]; ok && covered[sid] {
			continue
		}
		addEnding("commit-"+sha, Ending{Type: "commit", Reached: true, SHA: sha}, "Commit "+short(sha, 7), viaOf(commitVia[sha]), "")
	}

	// Layout. Row 0 is the chapter start on the spine. A path decision takes
	// the next spine row; an off-path decision takes the first free row in its
	// lane at or after the latest path row, so it sits beside what it followed.
	nodeCount := 1 + len(ids) + len(endings)
	for _, id := range ids {
		nodeCount += len(SplitAlternatives(b.rec(id).Alternatives))
	}
	ch.Compact = nodeCount > CompactThreshold
	used := slots{}
	used.take(LaneSpine, 0)
	start := Node{ID: "start:" + ch.ID, Kind: KindStart, Role: KindStart, Lane: LaneSpine, Row: 0, Title: "Chapter start", Detail: ch.Start, OnPath: true}
	nodes := []Node{start}
	var edges []Edge
	rowOf := map[string]int{}
	laneOf := map[string]string{}
	lastSpineRow, lastSpine := 0, start.ID
	var spine []string
	for _, id := range ids {
		d := b.rec(id)
		role, lane := b.role(id)
		n := Node{ID: id, Kind: KindDecision, Role: role, Lane: lane, Title: id, Detail: d.Statement, DecidedBy: d.DecidedBy, Decision: d}
		anchor := lastSpine
		switch lane {
		case LaneSpine:
			lastSpineRow = used.take(LaneSpine, lastSpineRow+1)
			n.Row, n.OnPath = lastSpineRow, true
			edges = append(edges, Edge{From: lastSpine, To: id, Relation: RelNext, Derived: true, OnPath: true})
			lastSpine = id
			spine = append(spine, id)
		case LaneRight:
			from := max(lastSpineRow, 1)
			if role == RoleRefinement {
				for _, ei := range b.incident[id] {
					e := b.g.Edges[ei]
					if e.Relation == decision.RelRefines && e.From.Ref == id && e.From.Kind == decision.KindDecision {
						if r, ok := rowOf[e.To.Ref]; ok && laneOf[e.To.Ref] != LaneRight {
							from = r
						}
					}
				}
			}
			n.Row = used.take(LaneRight, from)
		default:
			n.Row = used.take(LaneLeft, max(lastSpineRow, 1))
		}
		rowOf[id], laneOf[id] = n.Row, lane
		if lane != LaneSpine && role != RoleRefinement {
			edges = append(edges, Edge{From: anchor, To: id, Relation: RelAfter, Derived: true})
		}
		n.Links = append(n.Links, b.links(id)...)
		alts := SplitAlternatives(d.Alternatives)
		ghost := len(alts) == 1 && !ch.Compact && lane != LaneRight && !used[LaneRight][n.Row]
		var altNodes []Node
		for i, text := range alts {
			a := Node{ID: fmt.Sprintf("%s#alt%d", id, i+1), Kind: KindAlternative, Role: RoleNotTaken, Lane: LaneNone, Row: n.Row,
				Title: text, Parent: id, DecidedBy: "", Command: WhatIfCommand(id, i+1, text)}
			if ghost {
				a.Lane = LaneRight
				a.Row = used.take(LaneRight, n.Row)
				edges = append(edges, Edge{From: id, To: a.ID, Relation: RelNotTaken, Derived: true})
			}
			n.Alternatives = append(n.Alternatives, a.ID)
			altNodes = append(altNodes, a)
		}
		nodes = append(nodes, n)
		nodes = append(nodes, altNodes...)
	}

	// Locked endings: a blocked path decision with no ending of its own waits
	// on one.
	hasEnding := map[string]bool{}
	for _, acc := range endings {
		for _, v := range acc.via {
			hasEnding[v] = true
		}
	}
	for _, id := range spine {
		if b.rec(id).Blocked && !hasEnding[id] {
			addEnding("pending-"+id, Ending{Type: "pending", Locked: true}, "Locked ending", []string{id}, "")
		}
	}
	for _, acc := range endings {
		e := acc.node.Ending
		e.Via = append(e.Via, acc.via...)
		e.Cost = b.cost(e.Sessions)
		// Anchor on the latest path decision it came from, else the latest
		// off-path one, else the chapter start.
		anchor, anchorRow, anchorSpine := start.ID, 0, false
		for _, v := range acc.via {
			if !in[v] {
				continue
			}
			spineV := laneOf[v] == LaneSpine
			if anchor == start.ID || (spineV && !anchorSpine) || (spineV == anchorSpine && rowOf[v] > anchorRow) {
				anchor, anchorRow, anchorSpine = v, rowOf[v], spineV
			}
		}
		switch {
		case e.Locked:
			acc.node.Role = RoleLockedEnding
		case e.Reached:
			acc.node.Role = RoleReached
		default:
			acc.node.Role = RoleUnreached
		}
		acc.node.Detail = endingDetail(e)
		if e.Type == "pending" && len(e.Via) > 0 {
			acc.node.Detail = "needs " + strings.Join(b.rec(e.Via[0]).OpenBlockers, ", ")
		}
		if e.Locked && anchor == lastSpine && !used[LaneSpine][anchorRow+1] {
			acc.node.Lane, acc.node.Row = LaneSpine, used.take(LaneSpine, anchorRow+1)
		} else {
			acc.node.Lane, acc.node.Row = LaneLeft, used.take(LaneLeft, anchorRow)
		}
		acc.node.OnPath = e.Reached && anchorSpine
		edges = append(edges, Edge{From: anchor, To: acc.node.ID, Relation: RelOutcome, Derived: true, OnPath: acc.node.OnPath})
		nodes = append(nodes, acc.node)
		ch.Facts.EndingsTotal++
		if e.Reached {
			ch.Facts.EndingsReached++
		}
	}

	// Stored edges between two decisions of this chapter, once per
	// relation and pair. conflicts_with is symmetric, so its key is the
	// pair in sorted order: a legacy row stored in the other order is the
	// same conflict and is drawn once, as Facts.Conflicts counts it once.
	seen := map[[3]string]bool{}
	for _, ei := range chEdges {
		e := b.g.Edges[ei]
		if !b.stored(e.From) || !b.stored(e.To) || !in[e.From.Ref] || e.From.Ref == e.To.Ref {
			continue
		}
		key := [3]string{e.Relation, e.From.Ref, e.To.Ref}
		if e.Relation == decision.RelConflictsWith && key[2] < key[1] {
			key[1], key[2] = key[2], key[1]
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		edges = append(edges, Edge{From: e.From.Ref, To: e.To.Ref, Relation: e.Relation, RecordedAt: e.RecordedAt,
			Tension: e.Relation == decision.RelConflictsWith || e.Relation == decision.RelContradicts})
	}

	// Facts, attention, and explored.
	conflictPairs := map[[2]string]bool{}
	for _, id := range ids {
		d := b.rec(id)
		role, _ := b.role(id)
		// locked is the one rule for Facts.Locked, its attention row, and
		// Explored.Locked: a blocked record that is a choice point.
		locked := d.Blocked && role != RoleRefinement
		if locked {
			ch.Facts.Locked++
			ch.Attention = append(ch.Attention, Attention{Kind: "locked", Nodes: append([]string{id}, d.OpenBlockers...),
				Text: id + " waits on " + strings.Join(d.OpenBlockers, ", ")})
		}
		if d.Conflicted {
			for _, other := range d.ConflictsWith {
				o, ok := b.index[other]
				if !ok || b.g.Decisions[o].EffectiveStatus != decision.EffectiveActive {
					continue
				}
				pair := [2]string{id, other}
				if other < id {
					pair = [2]string{other, id}
				}
				if conflictPairs[pair] {
					continue
				}
				conflictPairs[pair] = true
				ch.Facts.Conflicts++
				ch.Attention = append(ch.Attention, Attention{Kind: "conflict", Nodes: []string{pair[0], pair[1]},
					Text: pair[0] + " ⟷ " + pair[1] + ", both active"})
			}
		}
		if d.Challenged {
			ch.Facts.Challenged++
			ch.Attention = append(ch.Attention, Attention{Kind: "challenged", Nodes: []string{id},
				Text: id + ": the latest evidence contradicts it" + b.challenger(id)})
		}
		if role == RoleRefinement {
			continue
		}
		alts := len(SplitAlternatives(d.Alternatives))
		switch {
		case locked:
			ch.Explored.Locked++
		case d.Status == decision.StatusDecision:
			// Superseded and retracted decision records count too: the
			// choice was made when the record was written (documented in
			// docs/outcomes.md).
			ch.Explored.Decided++
		case d.EffectiveStatus == decision.EffectiveActive:
			ch.Explored.Hypotheses++
		default:
			continue // a superseded or retracted hypothesis is not a choice point
		}
		ch.Explored.NotTaken += alts
	}
	sort.SliceStable(ch.Attention, func(i, j int) bool { return attentionRank(ch.Attention[i].Kind) < attentionRank(ch.Attention[j].Kind) })
	ch.Explored.Total = ch.Explored.Decided + ch.Explored.NotTaken + ch.Explored.Hypotheses + ch.Explored.Locked
	if ch.Explored.Total > 0 {
		p := (ch.Explored.Decided*200 + ch.Explored.Total) / (ch.Explored.Total * 2)
		ch.Explored.Percent = &p
	}
	for _, n := range nodes {
		if n.Lane != LaneNone && n.Row+1 > ch.Rows {
			ch.Rows = n.Row + 1
		}
	}
	ch.Nodes, ch.Edges = nodes, edges
	if ch.Edges == nil {
		ch.Edges = []Edge{}
	}
	return ch
}

func attentionRank(kind string) int {
	switch kind {
	case "conflict":
		return 0
	case "locked":
		return 1
	}
	return 2
}

// challenger names the source of the latest evidence that counts toward
// Challenged, using the decision package's rule (b.evidence, from
// decision.LatestEvidenceByDecision), so a validates or contradicts from a
// retracted or superseded decision is skipped here too.
func (b *builder) challenger(id string) string {
	e, ok := b.evidence[id]
	if !ok || e.Relation != decision.RelContradicts {
		return ""
	}
	return " (" + e.From.Kind + " " + short(e.From.Ref, 12) + ")"
}

func (b *builder) links(id string) []Link {
	var out []Link
	for _, ei := range b.incident[id] {
		e := b.g.Edges[ei]
		if e.From.Kind == decision.KindDecision && e.From.Ref == id {
			out = append(out, Link{Relation: e.Relation, Direction: "out", Kind: e.To.Kind, Ref: e.To.Ref, RecordedAt: e.RecordedAt})
		}
		if e.To.Kind == decision.KindDecision && e.To.Ref == id && !(e.From.Kind == decision.KindDecision && e.From.Ref == id) {
			out = append(out, Link{Relation: e.Relation, Direction: "in", Kind: e.From.Kind, Ref: e.From.Ref, RecordedAt: e.RecordedAt})
		}
	}
	return out
}

func (b *builder) sessionBefore(x, y string) bool {
	sx, sy := b.sessions[x], b.sessions[y]
	tx, ty := startOf(sx), startOf(sy)
	if tx != ty {
		return tx < ty
	}
	ox, okx := b.order[x]
	oy, oky := b.order[y]
	if okx != oky {
		return okx
	}
	if ox != oy {
		return ox < oy
	}
	return x < y
}

func startOf(s *summary.SessionSummary) int64 {
	if s == nil || s.Repository.StartUnixNano == nil {
		return 1<<63 - 1
	}
	return *s.Repository.StartUnixNano
}

func (b *builder) cost(sessionIDs []string) Cost {
	var total big.Rat
	known, unknown := 0, 0
	for _, sid := range sessionIDs {
		s := b.sessions[sid]
		if s == nil || s.ComputedCost == nil || s.ComputedCost.Status != "known" || s.ComputedCost.USD == "" {
			unknown++
			continue
		}
		part, ok := new(big.Rat).SetString(s.ComputedCost.USD)
		if !ok {
			unknown++
			continue
		}
		total.Add(&total, part)
		known++
	}
	switch {
	case known == 0:
		return Cost{Status: "unknown"}
	case unknown > 0:
		return Cost{Status: "partial", USD: trimDecimal(total.FloatString(6))}
	default:
		return Cost{Status: "known", USD: trimDecimal(total.FloatString(6))}
	}
}

func trimDecimal(s string) string {
	if strings.Contains(s, ".") {
		s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
	}
	return s
}

func endingDetail(e *Ending) string {
	switch e.Type {
	case "pull_request":
		if e.Reached {
			return "merged"
		}
		if e.PRState == "closed" {
			return "closed without merging"
		}
		return "open, not merged yet"
	case "commit":
		return "commit " + short(e.SHA, 12)
	default:
		return "needs " + strings.Join(e.Via, ", ")
	}
}

func short(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

func appendOnce(list []string, v string) []string {
	for _, x := range list {
		if x == v {
			return list
		}
	}
	return append(list, v)
}

func appendOnceInt(list []int, v int) []int {
	for _, x := range list {
		if x == v {
			return list
		}
	}
	return append(list, v)
}
