package store_test

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
	"time"

	"github.com/paulpham157/sdf/internal/store"
)

func copyPythonDB(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("caller")
	}
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(file), "testdata", "python-receiver.db"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "python-receiver.db")
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func tables(t *testing.T, path string) map[string]string {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.Query(`SELECT name, sql FROM sqlite_master WHERE type = 'table'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var name, ddl string
		if err := rows.Scan(&name, &ddl); err != nil {
			t.Fatal(err)
		}
		out[name] = ddl
	}
	return out
}

// TestRetractionsMigrationIsAdditiveAndIdempotent opens the Python-written
// database, which predates decisions, twice. The first open adds
// decision_retractions; the second changes nothing. No existing table's
// definition changes, and the telemetry row is still there.
func TestRetractionsMigrationIsAdditiveAndIdempotent(t *testing.T) {
	path := copyPythonDB(t)
	before := tables(t, path)
	if _, ok := before["decision_retractions"]; ok {
		t.Fatal("fixture already has the table")
	}
	var snapshots []map[string]string
	for i := 0; i < 2; i++ {
		s, err := store.Open(path)
		if err != nil {
			t.Fatalf("open %d: %v", i, err)
		}
		rows, err := s.Load()
		if err != nil || len(rows) != 1 {
			t.Fatalf("open %d: %d rows, %v", i, len(rows), err)
		}
		got, err := s.LoadDecisionRetractions()
		if err != nil || len(got) != 0 {
			t.Fatalf("open %d: %v %v", i, got, err)
		}
		_ = s.Close()
		snapshots = append(snapshots, tables(t, path))
	}
	if _, ok := snapshots[0]["decision_retractions"]; !ok {
		t.Fatal("decision_retractions not created")
	}
	if !reflect.DeepEqual(snapshots[0], snapshots[1]) {
		t.Fatalf("second open changed the schema:\n%v\n%v", snapshots[0], snapshots[1])
	}
	// The retractions migration adds one table and touches nothing else: on
	// a file migrated except for it, every other definition stays the same.
	if err := store.DropRetractionsForTest(path); err != nil {
		t.Fatal(err)
	}
	without := tables(t, path)
	s0, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	_ = s0.Close()
	with := tables(t, path)
	delete(with, "decision_retractions")
	if !reflect.DeepEqual(without, with) {
		t.Fatalf("the migration changed another table:\n%v\n%v", without, with)
	}
	// A second migration over a database that already has rows keeps them.
	s, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	r := store.DecisionRetraction{DecisionID: "a", Reason: "wrong", RetractedBy: "user", RecordedAt: "2026-11-23T16:00:00.000000+00:00"}
	if err := s.AppendDecisionRetraction(r, func([]store.DecisionRetraction) error { return nil }); err != nil {
		t.Fatal(err)
	}
	_ = s.Close()
	s, err = store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	got, err := s.LoadDecisionRetractions()
	if err != nil || len(got) != 1 || got[0] != r {
		t.Fatalf("%+v %v", got, err)
	}
}

// TestOldReadersStillReadANewDatabase runs the queries a binary without
// retractions runs (schema, decisions, decision_edges) against a database
// that has retractions. They see the same rows as before.
func TestOldReadersStillReadANewDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "new.sqlite")
	s, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.InsertDecision(store.Decision{ID: "a", Statement: "A", Status: "decision", RecordedAt: "x"}); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendDecisionRetraction(store.DecisionRetraction{DecisionID: "a", RecordedAt: "y"}, func([]store.DecisionRetraction) error { return nil }); err != nil {
		t.Fatal(err)
	}
	_ = s.Close()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(store.Schema); err != nil {
		t.Fatalf("old schema over a new file: %v", err)
	}
	var status string
	if err := db.QueryRow(`SELECT status FROM decisions WHERE id = 'a'`).Scan(&status); err != nil || status != "decision" {
		t.Fatalf("%q %v", status, err)
	}
}

// TestRetractionsMissingTableReadsEmpty covers a file the migration could
// not write (read-only): loading retractions from it is empty, not an error.
func TestRetractionsMissingTableReadsEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "plain.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(store.Schema); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	if err := store.DropRetractionsForTest(path); err != nil {
		t.Fatal(err)
	}
	got, err := store.LoadDecisionRetractionsAt(path)
	if err != nil || len(got) != 0 {
		t.Fatalf("%v %v", got, err)
	}
}

func TestAppendDecisionRetractionIsAtomicWithLinks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "telemetry.sqlite")
	open := func() *store.Store {
		s, err := store.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = s.Close() })
		return s
	}
	retractor, linker := open(), open()
	entered := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- retractor.AppendDecisionRetraction(store.DecisionRetraction{DecisionID: "a", RecordedAt: "x"}, func([]store.DecisionRetraction) error {
			close(entered)
			time.Sleep(300 * time.Millisecond)
			return nil
		})
	}()
	<-entered
	seen := -1
	errRetracted := errors.New("retracted")
	err := linker.AppendDecisionEdge(store.DecisionEdge{Relation: "implements", FromKind: "decision", FromRef: "a", ToKind: "decision", ToRef: "b", RecordedAt: "y"},
		func(g store.DecisionGraph) error {
			seen = len(g.Retractions)
			if seen > 0 {
				return errRetracted
			}
			return nil
		})
	if e := <-done; e != nil {
		t.Fatal(e)
	}
	if seen != 1 || !errors.Is(err, errRetracted) {
		t.Fatalf("link check saw %d retractions (%v): it ran before the retraction committed", seen, err)
	}
}
