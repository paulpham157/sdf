package ui_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/paulpham157/sdf/internal/decision"
	"github.com/paulpham157/sdf/internal/record"
	"github.com/paulpham157/sdf/internal/store"
)

func seedGraph(t *testing.T, db *store.Store) {
	t.Helper()
	if _, err := db.Insert([]record.Record{{
		Signal: "logs", SessionID: record.Str("ses-a"), Name: record.Str("sdf.git.commit"),
		Attributes: map[string]any{"vcs.ref.head.revision": "0123456789abcdef0123456789abcdef01234567", "sdf.git.branch": "feature-x"},
		Resource:   map[string]any{},
	}}); err != nil {
		t.Fatal(err)
	}
	adds := []decision.RecordInput{
		{ID: "capture", Statement: "Capture live spans.", Status: decision.StatusDecision, DecidedBy: "user", Alternatives: "Parse the log"},
		{ID: "status-ui", Statement: "Show agent status in the UI.", Status: decision.StatusDecision, DecidedBy: "user",
			Alternatives: "Warnings only; A check command", Reason: "Users cannot tell when an agent stopped."},
		{ID: "merge-first", Statement: "Merge main first.", Status: decision.StatusHypothesis, DecidedBy: "agent"},
		{ID: "silent", Statement: "Hooks fail silently.", Status: decision.StatusDecision, DecidedBy: "agent"},
	}
	for _, in := range adds {
		if _, err := decision.Add(db, in); err != nil {
			t.Fatal(err)
		}
	}
	for _, l := range [][3]string{
		{"implements", "commit:0123456789abcdef0123456789abcdef01234567", "decision:capture"},
		{"constrains", "decision:capture", "decision:status-ui"},
		{"blocks", "decision:merge-first", "decision:status-ui"},
		{"conflicts_with", "decision:status-ui", "decision:silent"},
	} {
		kind := func(s string) decision.Endpoint {
			k, ref, _ := strings.Cut(s, ":")
			return decision.Endpoint{Kind: k, Ref: ref}
		}
		if _, err := decision.Link(db, decision.EdgeInput{Relation: l[0], From: kind(l[1]), To: kind(l[2])}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestDecisionGraphRoute(t *testing.T) {
	db := openDB(t)
	srv := startUI(t, db)
	status, header, body := get(t, srv.URL()+"api/decisions/graph")
	if status != 200 || !strings.HasPrefix(header.Get("Content-Type"), "application/json") {
		t.Fatalf("status %d %s", status, body)
	}
	if !strings.Contains(body, "\"chapters\": []") {
		t.Fatalf("empty graph %s", body)
	}
	seedGraph(t, db)
	_, _, body = get(t, srv.URL()+"api/decisions/graph")
	var doc struct {
		Chapters []struct {
			ID          string `json:"id"`
			Title       string `json:"title"`
			LabelSource string `json:"label_source"`
			Facts       struct {
				EndingsReached int `json:"endings_reached"`
				EndingsTotal   int `json:"endings_total"`
				Locked         int `json:"locked"`
				Conflicts      int `json:"conflicts"`
			} `json:"facts"`
			Explored struct {
				Decided int  `json:"decided"`
				Total   int  `json:"total"`
				Percent *int `json:"percent"`
			} `json:"explored"`
			Attention []struct {
				Kind string `json:"kind"`
			} `json:"needs_attention"`
			Nodes []struct {
				ID      string `json:"id"`
				Kind    string `json:"kind"`
				Role    string `json:"role"`
				Command string `json:"command"`
			} `json:"nodes"`
		} `json:"chapters"`
	}
	if err := json.Unmarshal([]byte(body), &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Chapters) != 1 {
		t.Fatalf("%s", body)
	}
	c := doc.Chapters[0]
	if c.ID != "ch-capture" || c.Title != "feature-x" || c.LabelSource != "branch" {
		t.Fatalf("chapter %q %q %q", c.ID, c.Title, c.LabelSource)
	}
	// capture, status-ui (locked), merge-first, silent, 3 alternatives:
	// decided capture + silent = 2 of 2 + 1 + 1 + 3 = 7.
	if c.Facts.Locked != 1 || c.Facts.Conflicts != 1 || c.Facts.EndingsReached != 1 || c.Facts.EndingsTotal != 2 ||
		c.Explored.Decided != 2 || c.Explored.Total != 7 || c.Explored.Percent == nil || *c.Explored.Percent != 29 {
		t.Fatalf("facts %+v explored %+v", c.Facts, c.Explored)
	}
	if len(c.Attention) != 2 || c.Attention[0].Kind != "conflict" || c.Attention[1].Kind != "locked" {
		t.Fatalf("attention %+v", c.Attention)
	}
	var commands int
	for _, n := range c.Nodes {
		if n.Kind == "alternative" && strings.HasPrefix(n.Command, "sdf decision add --id ") {
			commands++
		}
	}
	if commands != 3 {
		t.Fatalf("%d what-if commands: %s", commands, body)
	}
	if post, _, posted := getPost(t, srv.URL()+"api/decisions/graph"); post != 405 && post != 404 {
		t.Fatalf("write route answered %d %s", post, posted)
	}
}

func TestPageRendersDecisionGraph(t *testing.T) {
	db := openDB(t)
	seedGraph(t, db)
	srv := startUI(t, db)

	page := renderedPageAt(t, srv, "#/decisions")
	for _, want := range []string{
		"Decisions", "Chapters", "feature-x", "Chapter start", "capture", "status-ui",
		"1 of 2 endings reached", "Needs attention", "Conflict:", "status-ui waits on merge-first",
		"Explored", "2 of 7 choice points decided", "29%", "Show tensions", "Not taken",
		"Locked ending", "needs merge-first", "Parse the log",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("#/decisions is missing %q:\n%s", want, page)
		}
	}
	if strings.Contains(page, "Copy command") {
		t.Errorf("chapter view shows what-if commands before a decision is focused:\n%s", page)
	}

	focused := renderedPageAt(t, srv, "#/decisions/ch-capture/status-ui")
	for _, want := range []string{
		"← Chapter", "Show agent status in the UI.", "Users cannot tell when an agent stopped.",
		"Blocked by", "merge-first", "Conflicts with", "silent", "Constrained by",
		"Warnings only", "A check command", "Copy command",
		"sdf decision add --id status-ui-alt-1 --status hypothesis --statement 'Warnings only'",
	} {
		if !strings.Contains(focused, want) {
			t.Errorf("focused decision is missing %q:\n%s", want, focused)
		}
	}

	// A decision with no reason says so instead of leaving a gap.
	if p := renderedPageAt(t, srv, "#/decisions/ch-capture/silent"); !strings.Contains(p, "none recorded") {
		t.Errorf("no-reason decision:\n%s", p)
	}
	// An unknown chapter falls back to the newest one.
	if p := renderedPageAt(t, srv, "#/decisions/ch-missing"); !strings.Contains(p, "feature-x") {
		t.Errorf("unknown chapter:\n%s", p)
	}
}

func TestDecisionsTabAbsentWithoutDecisions(t *testing.T) {
	srv := startUI(t, openDB(t))
	if page := renderedPage(t, srv); strings.Contains(page, "Decisions") {
		t.Fatalf("empty store shows a Decisions tab:\n%s", page)
	}
	// Opening the route directly still explains the empty state.
	if page := renderedPageAt(t, srv, "#/decisions"); !strings.Contains(page, "sdf decision add") {
		t.Fatalf("empty Decisions tab:\n%s", page)
	}
}
