package decisiongraph_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/paulpham157/sdf/internal/decision"
	"github.com/paulpham157/sdf/internal/decisiongraph"
	"github.com/paulpham157/sdf/internal/pricing"
	"github.com/paulpham157/sdf/internal/record"
	"github.com/paulpham157/sdf/internal/store"
	"github.com/paulpham157/sdf/internal/summary"
)

func openDB(t *testing.T) *store.Store {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "t.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func add(t *testing.T, db *store.Store, in decision.RecordInput) {
	t.Helper()
	if in.Status == "" {
		in.Status = decision.StatusDecision
	}
	if in.Statement == "" {
		in.Statement = "Statement for " + in.ID
	}
	if _, err := decision.Add(db, in); err != nil {
		t.Fatal(err)
	}
}

func link(t *testing.T, db *store.Store, rel, from, to string) {
	t.Helper()
	ep := func(s string) decision.Endpoint {
		kind, ref, _ := strings.Cut(s, ":")
		return decision.Endpoint{Kind: kind, Ref: ref}
	}
	if _, _, err := decision.LinkWithWarnings(db, decision.EdgeInput{Relation: rel, From: ep(from), To: ep(to)}); err != nil {
		t.Fatal(err)
	}
}

// known stores one telemetry record so links to the session (and commit)
// pass the store's unknown-reference check.
func known(t *testing.T, db *store.Store, sessionID, sha string) {
	t.Helper()
	rec := record.Record{Signal: "logs", SessionID: record.Str(sessionID), Name: record.Str("sdf.git.commit"), Attributes: map[string]any{}, Resource: map[string]any{}}
	if sha != "" {
		rec.Attributes["vcs.ref.head.revision"] = sha
	}
	if _, err := db.Insert([]record.Record{rec}); err != nil {
		t.Fatal(err)
	}
}

func load(t *testing.T, db *store.Store) decision.Graph {
	t.Helper()
	g, err := decision.Load(db)
	if err != nil {
		t.Fatal(err)
	}
	return g
}

func session(id, branch string, start int64, usd string, pr *summary.PullRequest, shas ...string) summary.SessionSummary {
	s := summary.SessionSummary{SessionID: id}
	s.Repository.StartUnixNano = &start
	for _, sha := range shas {
		s.Repository.HookCommits = append(s.Repository.HookCommits, summary.HookCommit{SHA: sha, Branch: branch})
	}
	if usd != "" {
		s.ComputedCost = &pricing.Cost{Status: "known", USD: usd}
	}
	if len(shas) > 0 {
		out := &summary.SessionOutcome{Status: "linked", Match: "hook", PullRequest: pr}
		for _, sha := range shas {
			out.Commits = append(out.Commits, summary.LinkedCommit{SHA: sha})
		}
		s.Outcome = out
	}
	return s
}

func nodes(c decisiongraph.Chapter) map[string]decisiongraph.Node {
	out := map[string]decisiongraph.Node{}
	for _, n := range c.Nodes {
		out[n.ID] = n
	}
	return out
}

const (
	shaA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	shaB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

// story is a small version of the mockup chapter: a superseded PR decision,
// a decision locked by a hypothesis, a refinement, a conflict, and a
// contradicted decision.
func story(t *testing.T) (decision.Graph, []summary.SessionSummary) {
	db := openDB(t)
	known(t, db, "s1", shaA)
	known(t, db, "s2", shaB)
	known(t, db, "s3", "")
	add(t, db, decision.RecordInput{ID: "capture", DecidedBy: "user", Alternatives: "Parse the log"})
	add(t, db, decision.RecordInput{ID: "token", DecidedBy: "user", Alternatives: "Leave it open; Token for exporters only"})
	add(t, db, decision.RecordInput{ID: "silent", DecidedBy: "agent"})
	add(t, db, decision.RecordInput{ID: "pr-1", DecidedBy: "user"})
	add(t, db, decision.RecordInput{ID: "pr-2", DecidedBy: "user"})
	add(t, db, decision.RecordInput{ID: "status", DecidedBy: "user", Alternatives: "Log only, or A check command", Reason: "Users can't tell"})
	add(t, db, decision.RecordInput{ID: "merge-first", Status: decision.StatusHypothesis, DecidedBy: "agent"})
	add(t, db, decision.RecordInput{ID: "bucket", DecidedBy: "agent"})
	link(t, db, "implements", "commit:"+shaA, "decision:capture")
	link(t, db, "implements", "session:s1", "decision:pr-1")
	link(t, db, "supersedes", "decision:pr-2", "decision:pr-1")
	link(t, db, "implements", "commit:"+shaB, "decision:token")
	link(t, db, "constrains", "decision:token", "decision:silent")
	link(t, db, "implements", "session:s2", "decision:pr-2")
	link(t, db, "blocks", "decision:merge-first", "decision:status")
	link(t, db, "refines", "decision:bucket", "decision:status")
	link(t, db, "conflicts_with", "decision:status", "decision:silent")
	link(t, db, "contradicts", "session:s3", "decision:silent")
	sessions := []summary.SessionSummary{
		session("s1", "feat", 1e18, "1.25", &summary.PullRequest{Number: 1, State: "closed"}, shaA),
		session("s2", "feat", 2e18, "0.5", &summary.PullRequest{Number: 2, State: "closed", Merged: true}, shaB),
		session("s3", "", 3e18, "", nil),
	}
	return load(t, db), sessions
}

func TestBuildStoryChapter(t *testing.T) {
	g, sessions := story(t)
	v := decisiongraph.Build(g, sessions)
	if len(v.Chapters) != 1 {
		t.Fatalf("chapters = %d, want 1: %+v", len(v.Chapters), v.Chapters)
	}
	c := v.Chapters[0]
	if c.ID != "ch-capture" || c.Title != "feat" || c.LabelSource != "branch" {
		t.Fatalf("chapter label = %q %q %q", c.ID, c.Title, c.LabelSource)
	}
	if !reflect.DeepEqual(c.PullRequests, []int{1, 2}) || !reflect.DeepEqual(c.Sessions, []string{"s1", "s2", "s3"}) {
		t.Fatalf("prs %v sessions %v", c.PullRequests, c.Sessions)
	}
	// s3 has no computed cost, so the sum is partial.
	if c.Cost.Status != "partial" || c.Cost.USD != "1.75" {
		t.Fatalf("cost = %+v", c.Cost)
	}
	wantFacts := decisiongraph.Facts{EndingsReached: 1, EndingsTotal: 3, Locked: 1, Conflicts: 1, Challenged: 1}
	if c.Facts != wantFacts {
		t.Fatalf("facts = %+v, want %+v", c.Facts, wantFacts)
	}
	// Decided: capture, token, silent, pr-1 (superseded still counts), pr-2.
	// Locked: status. Hypotheses: merge-first. Not taken: 1 + 2 + 2.
	// bucket is a refinement and does not count.
	e := c.Explored
	if e.Decided != 5 || e.Locked != 1 || e.Hypotheses != 1 || e.NotTaken != 5 || e.Total != 12 || e.Percent == nil || *e.Percent != 42 {
		t.Fatalf("explored = %+v", e)
	}
	var kinds []string
	for _, a := range c.Attention {
		kinds = append(kinds, a.Kind)
	}
	if !reflect.DeepEqual(kinds, []string{"conflict", "locked", "challenged"}) {
		t.Fatalf("attention = %+v", c.Attention)
	}

	n := nodes(c)
	checks := []struct{ id, kind, role, lane string }{
		{"capture", "decision", "path", "spine"},
		{"pr-1", "decision", "superseded", "left"},
		{"pr-2", "decision", "path", "spine"},
		{"status", "decision", "locked", "spine"},
		{"merge-first", "decision", "hypothesis", "right"},
		{"bucket", "decision", "refinement", "right"},
		{"capture#alt1", "alternative", "not_taken", "right"},
		{"token#alt1", "alternative", "not_taken", "none"},
		{"token#alt2", "alternative", "not_taken", "none"},
		{"ending:pr-1", "ending", "unreached", "left"},
		{"ending:pr-2", "ending", "reached", "left"},
		{"ending:pending-status", "ending", "locked_ending", "spine"},
	}
	for _, want := range checks {
		got, ok := n[want.id]
		if !ok {
			t.Errorf("missing node %s", want.id)
			continue
		}
		if got.Kind != want.kind || got.Role != want.role || got.Lane != want.lane {
			t.Errorf("%s = %s/%s/%s, want %s/%s/%s", want.id, got.Kind, got.Role, got.Lane, want.kind, want.role, want.lane)
		}
	}
	if got := n["status"].Alternatives; !reflect.DeepEqual(got, []string{"status#alt1", "status#alt2"}) {
		t.Errorf("status alternatives = %v", got)
	}
	if n["status#alt2"].Title != "A check command" {
		t.Errorf("alt title = %q", n["status#alt2"].Title)
	}
	if !strings.HasPrefix(n["capture#alt1"].Command, "sdf decision add --id capture-alt-1 --status hypothesis ") {
		t.Errorf("command = %q", n["capture#alt1"].Command)
	}
	if end := n["ending:pr-2"].Ending; end == nil || !end.Reached || end.PR != 2 || !reflect.DeepEqual(end.Via, []string{"token", "pr-2"}) {
		t.Errorf("pr-2 ending = %+v", end)
	}
	if end := n["ending:pending-status"].Ending; end == nil || !end.Locked || n["ending:pending-status"].Detail != "needs merge-first" {
		t.Errorf("locked ending = %+v %q", end, n["ending:pending-status"].Detail)
	}
	if n["ending:pr-1"].Detail != "closed without merging" {
		t.Errorf("pr-1 detail = %q", n["ending:pr-1"].Detail)
	}

	// The spine is ordered; tensions are flagged so the page can hide them.
	if n["capture"].Row >= n["token"].Row || n["token"].Row >= n["status"].Row {
		t.Errorf("rows capture %d token %d status %d", n["capture"].Row, n["token"].Row, n["status"].Row)
	}
	var constrains, conflict *decisiongraph.Edge
	for i, e := range c.Edges {
		switch e.Relation {
		case "constrains":
			constrains = &c.Edges[i]
		case "conflicts_with":
			conflict = &c.Edges[i]
		}
	}
	if constrains == nil || constrains.Tension || conflict == nil || !conflict.Tension {
		t.Errorf("constrains %+v conflict %+v", constrains, conflict)
	}
}

func TestChaptersSplitAndLabelFallback(t *testing.T) {
	db := openDB(t)
	known(t, db, "ses-1", "")
	known(t, db, "ses-2", shaA)
	add(t, db, decision.RecordInput{ID: "alone"})
	add(t, db, decision.RecordInput{ID: "one"})
	add(t, db, decision.RecordInput{ID: "two"})
	add(t, db, decision.RecordInput{ID: "pr-only"})
	link(t, db, "implements", "session:ses-1", "decision:one")
	link(t, db, "constrains", "decision:one", "session:ses-1")
	link(t, db, "validates", "session:ses-1", "decision:two")
	link(t, db, "implements", "commit:"+shaA, "decision:pr-only")
	sessions := []summary.SessionSummary{
		session("ses-1", "", 1e18, "0.1", nil),
		session("ses-2", "", 2e18, "", &summary.PullRequest{Number: 9, State: "open"}, shaA),
	}
	// ses-2's commit has no branch; the chapter falls back to its PR.
	v := decisiongraph.Build(load(t, db), sessions)
	got := map[string]string{}
	for _, c := range v.Chapters {
		got[c.ID] = c.LabelSource + " " + c.Title
	}
	want := map[string]string{
		"ch-alone":   "decision alone",
		"ch-one":     "session session ses-1",
		"ch-pr-only": "pr PR #9",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("chapters = %v, want %v", got, want)
	}
	for _, c := range v.Chapters {
		if c.ID == "ch-pr-only" {
			end := nodes(c)["ending:pr-9"]
			if end.Ending == nil || end.Ending.Reached || end.Detail != "open, not merged yet" {
				t.Errorf("open PR ending = %+v", end)
			}
		}
		if c.ID == "ch-one" && c.Cost != (decisiongraph.Cost{Status: "known", USD: "0.1"}) {
			t.Errorf("cost = %+v", c.Cost)
		}
		if c.ID == "ch-alone" && c.Cost.Status != "unknown" {
			t.Errorf("alone cost = %+v", c.Cost)
		}
	}
}

func TestCommitEndingWithoutSession(t *testing.T) {
	db := openDB(t)
	known(t, db, "elsewhere", shaB)
	add(t, db, decision.RecordInput{ID: "d"})
	link(t, db, "implements", "commit:"+shaB, "decision:d")
	// Without a report, and with a report whose session has the commit but
	// no linked outcome (sdf serve without --repo), the commit is the ending.
	unlinked := session("elsewhere", "main", 1e18, "", nil)
	unlinked.Repository.HookCommits = []summary.HookCommit{{SHA: shaB, Branch: "main"}}
	for name, sessions := range map[string][]summary.SessionSummary{"no report": nil, "unlinked": {unlinked}} {
		v := decisiongraph.Build(load(t, db), sessions)
		end := nodes(v.Chapters[0])["ending:commit-"+shaB]
		if end.Ending == nil || end.Ending.Type != "commit" || !end.Ending.Reached || end.Ending.SHA != shaB {
			t.Fatalf("%s: commit ending = %+v", name, end)
		}
		if v.Chapters[0].Facts.EndingsReached != 1 || v.Chapters[0].Facts.EndingsTotal != 1 {
			t.Fatalf("%s: facts = %+v", name, v.Chapters[0].Facts)
		}
	}
}

func TestExploredPercentRoundsHalfUp(t *testing.T) {
	// 1 decided of 8 choice points is 12.5%, shown as 13.
	db := openDB(t)
	add(t, db, decision.RecordInput{ID: "d", Alternatives: "a; b; c; d; e; f; g"})
	v := decisiongraph.Build(load(t, db), nil)
	e := v.Chapters[0].Explored
	if e.Total != 8 || e.Percent == nil || *e.Percent != 13 {
		t.Fatalf("explored = %+v", e)
	}
	// A chapter of only refinements and hypotheses... has hypotheses, which count.
	db = openDB(t)
	add(t, db, decision.RecordInput{ID: "h", Status: decision.StatusHypothesis})
	e = decisiongraph.Build(load(t, db), nil).Chapters[0].Explored
	if e.Total != 1 || e.Decided != 0 || e.Percent == nil || *e.Percent != 0 {
		t.Fatalf("hypothesis-only explored = %+v", e)
	}
}

func TestRetractedDecisionLeavesTheSpine(t *testing.T) {
	db := openDB(t)
	add(t, db, decision.RecordInput{ID: "keep"})
	add(t, db, decision.RecordInput{ID: "drop"})
	link(t, db, "constrains", "decision:keep", "decision:drop")
	if _, err := decision.Retract(db, decision.RetractInput{ID: "drop", Reason: "wrong"}); err != nil {
		t.Fatal(err)
	}
	n := nodes(decisiongraph.Build(load(t, db), nil).Chapters[0])
	if n["drop"].Role != "retracted" || n["drop"].Lane != "left" || n["drop"].OnPath {
		t.Fatalf("retracted node = %+v", n["drop"])
	}
	if n["keep"].Role != "path" || !n["keep"].OnPath {
		t.Fatalf("kept node = %+v", n["keep"])
	}
}

func TestCompactChapterCollapsesGhosts(t *testing.T) {
	db := openDB(t)
	prev := ""
	for i := 0; i <= decisiongraph.CompactThreshold; i++ {
		id := fmt.Sprintf("d%02d", i)
		add(t, db, decision.RecordInput{ID: id, Alternatives: "the other way"})
		if prev != "" {
			link(t, db, "constrains", "decision:"+prev, "decision:"+id)
		}
		prev = id
	}
	c := decisiongraph.Build(load(t, db), nil).Chapters[0]
	if !c.Compact {
		t.Fatal("chapter over the threshold is not compact")
	}
	for _, n := range c.Nodes {
		if n.Kind == "alternative" && n.Lane != "none" {
			t.Fatalf("ghost %s drawn in a compact chapter (lane %s)", n.ID, n.Lane)
		}
	}
}

func TestSplitAlternatives(t *testing.T) {
	cases := map[string][]string{
		"":                          nil,
		"Leave it open":             {"Leave it open"},
		"A; B ;; C":                 {"A", "B", "C"},
		"Warnings only, or a check": {"Warnings only", "a check"},
		"red, green, blue":          {"red, green, blue"},
		"one; or two":               {"one", "two"},
	}
	for in, want := range cases {
		if got := decisiongraph.SplitAlternatives(in); !reflect.DeepEqual(got, want) {
			t.Errorf("SplitAlternatives(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestWhatIfCommandQuotes(t *testing.T) {
	got := decisiongraph.WhatIfCommand("agent-status", 2, "Don't add a UI")
	want := `sdf decision add --id agent-status-alt-2 --status hypothesis --statement 'Don'\''t add a UI' --source 'what-if: alternative to agent-status'`
	if got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
}

func TestEmptyAndDeterministic(t *testing.T) {
	var buf bytes.Buffer
	if err := decisiongraph.WriteJSON(&buf, decisiongraph.Build(decision.Graph{}, nil)); err != nil {
		t.Fatal(err)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(buf.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	if string(doc["chapters"]) != "[]" || len(doc["relation_types"]) < 3 {
		t.Fatalf("empty view = %s", buf.String())
	}
	buf.Reset()
	if err := decisiongraph.WriteText(&buf, decisiongraph.Build(decision.Graph{}, nil)); err != nil || buf.String() != "no decision\n" {
		t.Fatalf("empty text = %q %v", buf.String(), err)
	}

	g, sessions := story(t)
	var a, b bytes.Buffer
	if err := decisiongraph.WriteJSON(&a, decisiongraph.Build(g, sessions)); err != nil {
		t.Fatal(err)
	}
	if err := decisiongraph.WriteJSON(&b, decisiongraph.Build(g, sessions)); err != nil {
		t.Fatal(err)
	}
	if a.String() != b.String() {
		t.Fatal("Build is not deterministic")
	}
	buf.Reset()
	if err := decisiongraph.WriteText(&buf, decisiongraph.Build(g, sessions)); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(buf.String(), "feat\t1 of 3 endings reached · 1 locked · 1 conflict · 1 challenged · 5/12 decided (42%)\n\tconflict: ") {
		t.Fatalf("text = %q", buf.String())
	}
}

func TestRelationTypesCoverStoredRelations(t *testing.T) {
	names := map[string]decisiongraph.RelationType{}
	for _, r := range decisiongraph.Relations() {
		names[r.Name] = r
	}
	for _, rel := range []string{"supersedes", "implements", "validates", "contradicts", "constrains", "blocks", "conflicts_with", "refines"} {
		r, ok := names[rel]
		if !ok || r.Derived {
			t.Errorf("relation %s missing or derived: %+v", rel, r)
		}
	}
	if !names["conflicts_with"].Symmetric || names["constrains"].Tension || !names["contradicts"].Tension || names["blocks"].Style != "lock" {
		t.Errorf("relation styles = %+v", names)
	}
	for _, rel := range []string{"next", "not_taken", "outcome"} {
		if !names[rel].Derived {
			t.Errorf("derived relation %s = %+v", rel, names[rel])
		}
	}
}
