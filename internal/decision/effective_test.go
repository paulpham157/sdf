package decision_test

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/paulpham157/sdf/internal/decision"
	"github.com/paulpham157/sdf/internal/record"
	"github.com/paulpham157/sdf/internal/store"
)

func addAll(t *testing.T, db *store.Store, ids ...string) {
	t.Helper()
	for _, id := range ids {
		if _, err := decision.Add(db, decision.RecordInput{ID: id, Statement: "Statement " + id, Status: decision.StatusDecision}); err != nil {
			t.Fatal(err)
		}
	}
}

func link(t *testing.T, db *store.Store, rel string, from, to decision.Endpoint) {
	t.Helper()
	if _, err := decision.Link(db, decision.EdgeInput{Relation: rel, From: from, To: to}); err != nil {
		t.Fatal(err)
	}
}

// legacyEdge writes an edge straight into decision_edges, bypassing Link,
// the way an sdf without link validation could have stored it.
func legacyEdge(t *testing.T, db *store.Store, rel string, from, to decision.Endpoint) {
	t.Helper()
	raw, err := sql.Open("sqlite", db.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if _, err := raw.Exec(`INSERT INTO decision_edges (relation, from_kind, from_ref, to_kind, to_ref, recorded_at) VALUES (?, ?, ?, ?, ?, ?)`,
		rel, from.Kind, from.Ref, to.Kind, to.Ref, "2026-01-01T00:00:00.000000+00:00"); err != nil {
		t.Fatal(err)
	}
}

func dec(id string) decision.Endpoint {
	return decision.Endpoint{Kind: decision.KindDecision, Ref: id}
}

func TestLoadComputesSupersededAtReadTime(t *testing.T) {
	db := openDB(t)
	if _, err := db.Insert([]record.Record{{
		Signal: "logs", SessionID: record.Str("sess-1"), Name: record.Str("sdf.git.commit"),
		Attributes: map[string]any{"vcs.ref.head.revision": sha}, Resource: map[string]any{},
	}}); err != nil {
		t.Fatal(err)
	}
	addAll(t, db, "a", "b", "c", "d", "e")
	// Chain: b supersedes a, c supersedes b. d also supersedes a.
	link(t, db, decision.RelSupersedes, dec("b"), dec("a"))
	link(t, db, decision.RelSupersedes, dec("c"), dec("b"))
	link(t, db, decision.RelSupersedes, dec("d"), dec("a"))
	// A repeated edge names the replacement once. Link now rejects a
	// duplicate, so it is written the way an older sdf stored it.
	legacyEdge(t, db, decision.RelSupersedes, dec("b"), dec("a"))
	// Ignored: other relations, and (from older databases, since Link now
	// rejects them) a self-edge and non-decision endpoints.
	link(t, db, decision.RelImplements, dec("e"), dec("c"))
	legacyEdge(t, db, decision.RelSupersedes, dec("e"), dec("e"))
	legacyEdge(t, db, decision.RelSupersedes, decision.Endpoint{Kind: decision.KindSession, Ref: "sess-1"}, dec("e"))
	legacyEdge(t, db, decision.RelSupersedes, decision.Endpoint{Kind: decision.KindCommit, Ref: sha}, dec("e"))
	legacyEdge(t, db, decision.RelSupersedes, dec("e"), decision.Endpoint{Kind: decision.KindSession, Ref: "sess-1"})

	before, err := db.LoadDecisions()
	if err != nil {
		t.Fatal(err)
	}
	g, err := decision.Load(db)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]struct {
		effective string
		by        []string
	}{
		"a": {decision.EffectiveSuperseded, []string{"b", "d"}},
		"b": {decision.EffectiveSuperseded, []string{"c"}},
		"c": {decision.EffectiveActive, nil},
		"d": {decision.EffectiveActive, nil},
		"e": {decision.EffectiveActive, nil},
	}
	if len(g.Decisions) != len(want) {
		t.Fatalf("%d decisions", len(g.Decisions))
	}
	for _, r := range g.Decisions {
		w := want[r.ID]
		if r.EffectiveStatus != w.effective || !reflect.DeepEqual(r.SupersededBy, w.by) {
			t.Fatalf("%s: effective %q by %v, want %q %v", r.ID, r.EffectiveStatus, r.SupersededBy, w.effective, w.by)
		}
		if r.Status != decision.StatusDecision {
			t.Fatalf("%s: stored status changed to %q", r.ID, r.Status)
		}
	}
	if len(g.Edges) != 9 {
		t.Fatalf("edges %d: every stored edge is still listed", len(g.Edges))
	}
	after, err := db.LoadDecisions()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("stored rows changed on read:\n%+v\n%+v", before, after)
	}
}

func TestSupersededFieldsInJSON(t *testing.T) {
	db := openDB(t)
	addAll(t, db, "old", "new")
	link(t, db, decision.RelSupersedes, dec("new"), dec("old"))
	g, err := decision.Load(db)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := decision.WriteJSON(&buf, g); err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Decisions []map[string]any `json:"decisions"`
	}
	if err := json.Unmarshal(buf.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	old, cur := doc.Decisions[0], doc.Decisions[1]
	if old["status"] != "decision" || old["effective_status"] != "superseded" || !reflect.DeepEqual(old["superseded_by"], []any{"new"}) {
		t.Fatalf("old %#v", old)
	}
	if cur["effective_status"] != "active" {
		t.Fatalf("new %#v", cur)
	}
	if _, ok := cur["superseded_by"]; ok {
		t.Fatalf("active record carries superseded_by: %#v", cur)
	}
}

func TestSupersedesCycleMarksBothAndTerminates(t *testing.T) {
	db := openDB(t)
	addAll(t, db, "x", "y")
	link(t, db, decision.RelSupersedes, dec("x"), dec("y"))
	// Link now rejects the cycle; an older sdf could store it.
	legacyEdge(t, db, decision.RelSupersedes, dec("y"), dec("x"))
	g, err := decision.Load(db)
	if err != nil {
		t.Fatal(err)
	}
	if g.Decisions[0].EffectiveStatus != decision.EffectiveSuperseded || !reflect.DeepEqual(g.Decisions[0].SupersededBy, []string{"y"}) ||
		g.Decisions[1].EffectiveStatus != decision.EffectiveSuperseded || !reflect.DeepEqual(g.Decisions[1].SupersededBy, []string{"x"}) {
		t.Fatalf("%+v", g.Decisions)
	}
}

func TestTextQuotesDELAndRTLButJSONKeepsThemRaw(t *testing.T) {
	db := openDB(t)
	if _, err := decision.Add(db, decision.RecordInput{ID: "old", Statement: "del\x7f rtl\u202e", Status: decision.StatusDecision}); err != nil {
		t.Fatal(err)
	}
	addAll(t, db, "new")
	link(t, db, decision.RelSupersedes, dec("new"), dec("old"))
	g, err := decision.Load(db)
	if err != nil {
		t.Fatal(err)
	}
	var text, raw bytes.Buffer
	if err := decision.WriteText(&text, g); err != nil {
		t.Fatal(err)
	}
	if err := decision.WriteJSON(&raw, g); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text.String(), `"del\x7f rtl\u202e"`) || strings.ContainsAny(text.String(), "\x7f\u202e") {
		t.Fatalf("table did not quote the cell:\n%s", text.String())
	}
	if !strings.Contains(text.String(), "superseded by new") {
		t.Fatalf("%s", text.String())
	}
	if !strings.Contains(raw.String(), "del\x7f rtl\u202e") {
		t.Fatalf("json escaped DEL/RTL:\n%s", raw.String())
	}
}

// TestSupersedesFromAnUnknownDecisionChangesNoStatus covers an older
// database whose edge names a decision that was never stored. Only an edge
// whose two endpoints are both stored decisions counts.
func TestSupersedesFromAnUnknownDecisionChangesNoStatus(t *testing.T) {
	db := openDB(t)
	addAll(t, db, "real")
	legacyEdge(t, db, decision.RelSupersedes, dec("ghost"), dec("real"))
	legacyEdge(t, db, decision.RelSupersedes, dec("real"), dec("ghost"))
	g, err := decision.Load(db)
	if err != nil {
		t.Fatal(err)
	}
	if len(g.Decisions) != 1 || len(g.Edges) != 2 {
		t.Fatalf("%d decisions, %d edges", len(g.Decisions), len(g.Edges))
	}
	if r := g.Decisions[0]; r.EffectiveStatus != decision.EffectiveActive || r.SupersededBy != nil {
		t.Fatalf("ghost edge counted: %+v", r)
	}
}
