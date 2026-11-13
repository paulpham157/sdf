package decision_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/paulpham157/sdf/internal/decision"
	"github.com/paulpham157/sdf/internal/record"
	"github.com/paulpham157/sdf/internal/store"
)

const sha = "0123456789abcdef0123456789abcdef01234567"

func TestRoundTrip(t *testing.T) {
	db := openDB(t)
	if _, err := db.Insert([]record.Record{{
		Signal:     "logs",
		SessionID:  record.Str("sess-1"),
		Name:       record.Str("sdf.git.commit"),
		Attributes: map[string]any{"vcs.ref.head.revision": sha},
		Resource:   map[string]any{},
	}}); err != nil {
		t.Fatal(err)
	}

	first, err := decision.Add(db, decision.RecordInput{
		ID:        "use-sqlite",
		Statement: "Keep the collector store in SQLite.",
		Status:    decision.StatusDecision,
		Source:    "issue 168",
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := decision.Add(db, decision.RecordInput{
		ID:        "pure-go",
		Statement: "The driver stays pure Go.",
		Status:    decision.StatusHypothesis,
	})
	if err != nil {
		t.Fatal(err)
	}
	edge, err := decision.Link(db, decision.EdgeInput{
		Relation: decision.RelImplements,
		From:     decision.Endpoint{Kind: decision.KindDecision, Ref: second.ID},
		To:       decision.Endpoint{Kind: decision.KindDecision, Ref: first.ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	sessionEdge, err := decision.Link(db, decision.EdgeInput{
		Relation: decision.RelValidates,
		From:     decision.Endpoint{Kind: decision.KindSession, Ref: "sess-1"},
		To:       decision.Endpoint{Kind: decision.KindDecision, Ref: first.ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	commitEdge, err := decision.Link(db, decision.EdgeInput{
		Relation: decision.RelValidates,
		From:     decision.Endpoint{Kind: decision.KindCommit, Ref: strings.ToUpper(sha)},
		To:       decision.Endpoint{Kind: decision.KindDecision, Ref: first.ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	if commitEdge.From.Ref != sha {
		t.Fatalf("sha %s", commitEdge.From.Ref)
	}

	got, err := decision.Load(db)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Decisions) != 2 || len(got.Edges) != 3 {
		t.Fatalf("%d decisions, %d edges", len(got.Decisions), len(got.Edges))
	}
	if got.Decisions[0] != first || got.Decisions[1] != second {
		t.Fatalf("decisions %+v", got.Decisions)
	}
	if got.Edges[0] != edge || got.Edges[1] != sessionEdge || got.Edges[2] != commitEdge {
		t.Fatalf("edges %+v", got.Edges)
	}
	if got.Decisions[0].Source != "issue 168" || got.Decisions[1].Source != "" {
		t.Fatalf("sources %q %q", got.Decisions[0].Source, got.Decisions[1].Source)
	}
}

func TestHypothesisIsDistinctFromDecisionInJSON(t *testing.T) {
	db := openDB(t)
	if _, err := decision.Add(db, decision.RecordInput{
		ID: "h", Statement: "This might be true.", Status: decision.StatusHypothesis,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := decision.Add(db, decision.RecordInput{
		ID: "d", Statement: "This is settled.", Status: decision.StatusDecision,
	}); err != nil {
		t.Fatal(err)
	}
	got, err := decision.Load(db)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := decision.WriteJSON(&buf, got); err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Decisions []map[string]any `json:"decisions"`
	}
	if err := json.Unmarshal(buf.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Decisions) != 2 {
		t.Fatalf("%d", len(doc.Decisions))
	}
	if doc.Decisions[0]["status"] != "hypothesis" || doc.Decisions[1]["status"] != "decision" {
		t.Fatalf("statuses %#v %#v", doc.Decisions[0], doc.Decisions[1])
	}
	if doc.Decisions[0]["status"] == doc.Decisions[1]["status"] {
		t.Fatal("status did not distinguish them")
	}
	if _, ok := doc.Decisions[0]["source"]; ok {
		t.Fatal("empty source was written")
	}
}

func TestEdgeRejectsUnknownSessionOrCommit(t *testing.T) {
	db := openDB(t)
	if _, err := decision.Add(db, decision.RecordInput{
		ID: "d", Statement: "Recorded.", Status: decision.StatusDecision,
	}); err != nil {
		t.Fatal(err)
	}
	_, err := decision.Link(db, decision.EdgeInput{
		Relation: decision.RelValidates,
		From:     decision.Endpoint{Kind: decision.KindSession, Ref: "missing-session"},
		To:       decision.Endpoint{Kind: decision.KindDecision, Ref: "d"},
	})
	if !errors.Is(err, decision.ErrUnknownRef) {
		t.Fatalf("session: %v", err)
	}
	_, err = decision.Link(db, decision.EdgeInput{
		Relation: decision.RelContradicts,
		From:     decision.Endpoint{Kind: decision.KindCommit, Ref: sha},
		To:       decision.Endpoint{Kind: decision.KindDecision, Ref: "d"},
	})
	if !errors.Is(err, decision.ErrUnknownRef) {
		t.Fatalf("commit: %v", err)
	}
	_, err = decision.Link(db, decision.EdgeInput{
		Relation: decision.RelSupersedes,
		From:     decision.Endpoint{Kind: decision.KindDecision, Ref: "d"},
		To:       decision.Endpoint{Kind: decision.KindDecision, Ref: "never-recorded"},
	})
	if !errors.Is(err, decision.ErrUnknownRef) {
		t.Fatalf("decision: %v", err)
	}
	got, err := decision.Load(db)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Edges) != 0 {
		t.Fatalf("rejected edges were stored: %+v", got.Edges)
	}
}

func TestKnownCommitOnToolResult(t *testing.T) {
	db := openDB(t)
	if _, err := db.Insert([]record.Record{{
		Signal:     "logs",
		SessionID:  record.Str("sess-1"),
		Name:       record.Str("claude_code.tool_result"),
		Attributes: map[string]any{"git_commit_id": sha},
		Resource:   map[string]any{},
	}}); err != nil {
		t.Fatal(err)
	}
	if _, err := decision.Add(db, decision.RecordInput{
		ID: "d", Statement: "Recorded.", Status: decision.StatusDecision,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := decision.Link(db, decision.EdgeInput{
		Relation: decision.RelImplements,
		From:     decision.Endpoint{Kind: decision.KindCommit, Ref: sha},
		To:       decision.Endpoint{Kind: decision.KindDecision, Ref: "d"},
	}); err != nil {
		t.Fatal(err)
	}
}

func TestKnownCommitNestedInToolParameters(t *testing.T) {
	db := openDB(t)
	if _, err := db.Insert([]record.Record{
		{
			Signal:     "logs",
			SessionID:  record.Str("sess-1"),
			Name:       record.Str("claude_code.tool_result"),
			Attributes: map[string]any{"tool_parameters": map[string]any{"git_commit_id": sha}},
			Resource:   map[string]any{},
		},
		{
			Signal:     "logs",
			SessionID:  record.Str("sess-2"),
			Name:       record.Str("claude_code.tool_result"),
			Attributes: map[string]any{"tool_parameters": `{"git_commit_id":"` + sha + `"}`},
			Resource:   map[string]any{},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := decision.Add(db, decision.RecordInput{
		ID: "d", Statement: "Recorded.", Status: decision.StatusDecision,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := decision.Link(db, decision.EdgeInput{
		Relation: decision.RelValidates,
		From:     decision.Endpoint{Kind: decision.KindCommit, Ref: sha},
		To:       decision.Endpoint{Kind: decision.KindDecision, Ref: "d"},
	}); err != nil {
		t.Fatal(err)
	}
}

func TestRejectedFields(t *testing.T) {
	db := openDB(t)
	if _, err := decision.Add(db, decision.RecordInput{
		ID: "d", Statement: "line one\nline two", Status: decision.StatusDecision,
	}); err == nil {
		t.Fatal("multiline statement accepted")
	}
	if _, err := decision.Add(db, decision.RecordInput{
		ID: "d", Statement: "ok", Status: "assumption",
	}); err == nil {
		t.Fatal("status accepted")
	}
	if _, err := decision.Link(db, decision.EdgeInput{
		Relation: "causes",
		From:     decision.Endpoint{Kind: decision.KindDecision, Ref: "d"},
		To:       decision.Endpoint{Kind: decision.KindDecision, Ref: "d"},
	}); err == nil {
		t.Fatal("relation accepted")
	}
	if _, err := decision.Link(db, decision.EdgeInput{
		Relation: decision.RelConstrains,
		From:     decision.Endpoint{Kind: decision.KindCommit, Ref: "abc"},
		To:       decision.Endpoint{Kind: decision.KindDecision, Ref: "d"},
	}); err == nil {
		t.Fatal("short sha accepted")
	}
}

func openDB(t *testing.T) *store.Store {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "telemetry.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}
