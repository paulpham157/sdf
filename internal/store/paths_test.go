package store_test

import (
	"bytes"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/paulpham157/sdf/internal/store"
)

// TestPathsMigrationIsAdditiveAndIdempotent opens the Python-written
// database twice. The first open adds decision_paths; dropping it and
// opening again adds only that table back. Rows survive a later open.
func TestPathsMigrationIsAdditiveAndIdempotent(t *testing.T) {
	path := copyPythonDB(t)
	before := tables(t, path)
	if _, ok := before["decision_paths"]; ok {
		t.Fatal("fixture already has the table")
	}
	s, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.LoadDecisionPaths()
	if err != nil || len(got) != 0 {
		t.Fatalf("%v %v", got, err)
	}
	_ = s.Close()
	if _, ok := tables(t, path)["decision_paths"]; !ok {
		t.Fatal("decision_paths not created")
	}
	if err := store.DropPathsForTest(path); err != nil {
		t.Fatal(err)
	}
	without := tables(t, path)
	s, err = store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	_ = s.Close()
	with := tables(t, path)
	delete(with, "decision_paths")
	if !reflect.DeepEqual(without, with) {
		t.Fatalf("the migration changed another table:\n%v\n%v", without, with)
	}

	s, err = store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	row := store.DecisionPath{DecisionID: "a", Glob: "internal/store/**", RecordedAt: "2026-11-23T16:00:00.000000+00:00"}
	if err := s.AppendDecisionPaths(row.DecisionID, row.RecordedAt, []string{row.Glob, "*.c"}, func([]store.DecisionPath) error { return nil }); err != nil {
		t.Fatal(err)
	}
	_ = s.Close()
	s, err = store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	got, err = s.LoadDecisionPaths()
	want := []store.DecisionPath{row, {DecisionID: "a", Glob: "*.c", RecordedAt: row.RecordedAt}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestAppendDecisionPathsRollsBackWhenTheCheckFails(t *testing.T) {
	path := filepath.Join(t.TempDir(), "db.sqlite")
	s, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.AppendDecisionPaths("a", "t", []string{"*.go"}, func([]store.DecisionPath) error { return nil }); err != nil {
		t.Fatal(err)
	}
	want := errors.New("nope")
	err = s.AppendDecisionPaths("a", "t", []string{"*.c"}, func(existing []store.DecisionPath) error {
		if len(existing) != 1 || existing[0].Glob != "*.go" {
			t.Errorf("check saw %+v", existing)
		}
		return want
	})
	if !errors.Is(err, want) {
		t.Fatalf("%v", err)
	}
	got, err := s.LoadDecisionPaths()
	if err != nil || len(got) != 1 || got[0].Glob != "*.go" {
		t.Fatalf("%v %v", got, err)
	}
}

func TestMissingPathsTableReadsEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "plain.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(store.Schema); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	if err := store.DropPathsForTest(path); err != nil {
		t.Fatal(err)
	}
	got, err := store.LoadDecisionPathsAt(path)
	if err != nil || len(got) != 0 {
		t.Fatalf("%v %v", got, err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	ro, err := store.OpenReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	got, err = ro.LoadDecisionPaths()
	_ = ro.Close()
	if err != nil || len(got) != 0 {
		t.Fatalf("readonly %v %v", got, err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("OpenReadOnly wrote the file")
	}
}
