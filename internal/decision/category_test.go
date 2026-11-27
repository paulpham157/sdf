package decision

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/paulpham157/sdf/internal/store"
)

func TestNewRecordCategoryAndLogin(t *testing.T) {
	for _, cat := range []string{CategoryArchitecture, CategoryPolicy, CategoryProduct, CategoryImplementation, CategoryOperations} {
		status := StatusDecision
		if cat != CategoryPolicy {
			status = StatusHypothesis
		}
		got, err := newRecord(RecordInput{
			ID: "use-sqlite", Statement: "  Keep the store.  ", Status: status,
			DecidedBy: "  paulpham157  ", Category: "  " + cat + "  ",
			Source: "s", Alternatives: "a", Reason: "r",
		}, "2026-11-27T00:00:00.000000+00:00")
		if err != nil {
			t.Fatalf("%s: %v", cat, err)
		}
		if got.Category != cat || got.DecidedBy != "paulpham157" || got.DecidedByLegacy || got.Status != status || got.Statement != "Keep the store." {
			t.Fatalf("%+v", got)
		}
		if got.Source != "s" || got.Alternatives != "a" || got.Reason != "r" {
			t.Fatalf("fields %+v", got)
		}
	}

	legacy, err := newRecord(RecordInput{
		ID: "old", Statement: "Stay.", Status: StatusDecision, DecidedBy: DecidedByUser,
	}, "t")
	if err != nil || !legacy.DecidedByLegacy || legacy.DecidedBy != DecidedByUser || legacy.Category != "" {
		t.Fatalf("%+v %v", legacy, err)
	}
	agent, err := newRecord(RecordInput{
		ID: "old-agent", Statement: "Stay.", Status: StatusHypothesis, DecidedBy: DecidedByAgent,
	}, "t")
	if err != nil || !agent.DecidedByLegacy || agent.Category != "" {
		t.Fatalf("%+v %v", agent, err)
	}

	unset, err := newRecord(RecordInput{
		ID: "plain", Statement: "Stay.", Status: StatusDecision, Category: "   ",
	}, "t")
	if err != nil || unset.Category != "" || unset.DecidedBy != "" || unset.DecidedByLegacy {
		t.Fatalf("%+v %v", unset, err)
	}

	for _, bad := range []RecordInput{
		{ID: "x", Statement: "S", Status: StatusDecision, Category: "none"},
		{ID: "x", Statement: "S", Status: StatusDecision, Category: "Architecture"},
		{ID: "x", Statement: "S", Status: StatusDecision, Category: "implementation extra"},
		{ID: "x", Statement: "S", Status: StatusHypothesis, Category: CategoryPolicy},
		{ID: "x", Statement: "S", Status: StatusDecision, DecidedBy: "a--b"},
		{ID: "x", Statement: "S", Status: StatusDecision, DecidedBy: strings.Repeat("a", 40)},
	} {
		if _, err := newRecord(bad, "t"); err == nil {
			t.Fatalf("accepted %+v", bad)
		}
	}
	if _, err := newRecord(RecordInput{
		ID: "pol", Statement: "Do not push.", Status: StatusDecision, Category: CategoryPolicy, DecidedBy: "paulpham157",
	}, "t"); err != nil {
		t.Fatal(err)
	}
	_, err = newRecord(RecordInput{
		ID: "pol", Statement: "Waiting.", Status: StatusHypothesis, Category: CategoryPolicy,
	}, "t")
	if err == nil || err.Error() != `category "policy" requires status "decision"` {
		t.Fatalf("%v", err)
	}
}

func TestNewRecordDoesNotWriteCategoryIntoOtherFields(t *testing.T) {
	db := openDB(t)
	got, err := Add(db, RecordInput{
		ID: "pure-go", Statement: "The driver stays pure Go.", Status: StatusDecision,
		Category: CategoryImplementation, DecidedBy: "paulpham157",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != StatusDecision || got.DecidedBy != "paulpham157" || got.Category != CategoryImplementation {
		t.Fatalf("%+v", got)
	}
	rows, err := db.LoadDecisions()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Status != StatusDecision || rows[0].DecidedBy != "paulpham157" || rows[0].Category != CategoryImplementation {
		t.Fatalf("%+v", rows)
	}
	loaded, err := Load(db)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(loaded.Decisions[0], got) {
		t.Fatalf("load %+v add %+v", loaded.Decisions[0], got)
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

func TestPolicyHypothesisStoresNothing(t *testing.T) {
	db := openDB(t)
	_, err := Add(db, RecordInput{
		ID: "wait", Statement: "Waiting for the user's OK to push.", Status: StatusHypothesis, Category: CategoryPolicy,
	})
	if err == nil {
		t.Fatal("stored a policy hypothesis")
	}
	rows, err := db.LoadDecisions()
	if err != nil || len(rows) != 0 {
		t.Fatalf("%v %+v", err, rows)
	}
}

func TestFilterByCategory(t *testing.T) {
	g := Graph{
		Decisions: []Record{
			{ID: "arch", Category: CategoryArchitecture},
			{ID: "impl", Category: CategoryImplementation},
			{ID: "unset"},
			{ID: "bogus", Category: "security"},
			{ID: "literal-none", Category: CategoryNone},
			{ID: "old", Category: "", EffectiveStatus: EffectiveSuperseded},
		},
		Edges: []Edge{
			{From: Endpoint{Kind: KindDecision, Ref: "arch"}, To: Endpoint{Kind: KindDecision, Ref: "impl"}},
			{From: Endpoint{Kind: KindSession, Ref: "sess-1"}, To: Endpoint{Kind: KindDecision, Ref: "unset"}},
			{From: Endpoint{Kind: KindDecision, Ref: "bogus"}, To: Endpoint{Kind: KindCommit, Ref: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}},
		},
	}
	impl := FilterByCategory(g, CategoryImplementation)
	if ids := decisionIDs(impl); ids != "impl unset old" {
		t.Fatalf("implementation %s", ids)
	}
	if len(impl.Edges) != 2 {
		t.Fatalf("edges %+v", impl.Edges)
	}
	arch := FilterByCategory(g, CategoryArchitecture)
	if ids := decisionIDs(arch); ids != "arch" {
		t.Fatalf("architecture %s", ids)
	}
	if len(arch.Edges) != 1 || arch.Edges[0].To.Ref != "impl" {
		t.Fatalf("kept edge should still name the omitted decision: %+v", arch.Edges)
	}
	none := FilterByCategory(g, CategoryNone)
	if ids := decisionIDs(none); ids != "bogus" {
		t.Fatalf("none %s", ids)
	}
}

func decisionIDs(g Graph) string {
	var ids []string
	for _, d := range g.Decisions {
		ids = append(ids, d.ID)
	}
	return strings.Join(ids, " ")
}

func TestCategoryColumnRoundTrip(t *testing.T) {
	db := openDB(t)
	for _, cat := range []string{CategoryArchitecture, CategoryPolicy, CategoryProduct, CategoryImplementation, CategoryOperations} {
		if _, err := Add(db, RecordInput{
			ID: cat, Statement: "Statement " + cat, Status: StatusDecision, Category: cat,
		}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := Add(db, RecordInput{ID: "unset", Statement: "No category.", Status: StatusHypothesis}); err != nil {
		t.Fatal(err)
	}
	rows, err := db.LoadDecisions()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 6 || rows[5].Category != "" {
		t.Fatalf("%+v", rows)
	}
	for i, cat := range []string{CategoryArchitecture, CategoryPolicy, CategoryProduct, CategoryImplementation, CategoryOperations} {
		if rows[i].Category != cat {
			t.Fatalf("%d %+v", i, rows[i])
		}
	}
}
