package store_test

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/paulpham157/sdf/internal/store"
)

// TestAppendDecisionEdgeChecksAndInsertsInOneTransaction holds the first
// writer inside its check while a second store on the same file (another
// process) appends. The second check must run after the first insert and
// see it, so a check-then-insert cannot interleave.
func TestAppendDecisionEdgeChecksAndInsertsInOneTransaction(t *testing.T) {
	path := filepath.Join(t.TempDir(), "telemetry.sqlite")
	open := func() *store.Store {
		s, err := store.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = s.Close() })
		return s
	}
	first, second := open(), open()
	edge := store.DecisionEdge{Relation: "implements", FromKind: "decision", FromRef: "a", ToKind: "decision", ToRef: "b", RecordedAt: "2026-11-23T16:00:00.000000+00:00"}

	entered := make(chan struct{})
	firstDone := make(chan error, 1)
	go func() {
		firstDone <- first.AppendDecisionEdge(edge, func(existing []store.DecisionEdge, _ []store.DecisionRetraction) error {
			close(entered)
			time.Sleep(300 * time.Millisecond)
			return nil
		})
	}()
	<-entered
	seen := -1
	errDup := errors.New("duplicate")
	err := second.AppendDecisionEdge(edge, func(existing []store.DecisionEdge, _ []store.DecisionRetraction) error {
		seen = len(existing)
		if len(existing) > 0 {
			return errDup
		}
		return nil
	})
	if e := <-firstDone; e != nil {
		t.Fatal(e)
	}
	if seen != 1 || !errors.Is(err, errDup) {
		t.Fatalf("second check saw %d edges (err %v): the first insert was not visible to it", seen, err)
	}
	edges, err := second.LoadDecisionEdges()
	if err != nil {
		t.Fatal(err)
	}
	if len(edges) != 1 {
		t.Fatalf("%d edges stored, want 1", len(edges))
	}
}

func TestAppendDecisionEdgeWritesNothingWhenTheCheckFails(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "telemetry.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	no := errors.New("no")
	edge := store.DecisionEdge{Relation: "implements", FromKind: "decision", FromRef: "a", ToKind: "decision", ToRef: "b", RecordedAt: "x"}
	if err := s.AppendDecisionEdge(edge, func([]store.DecisionEdge, []store.DecisionRetraction) error { return no }); !errors.Is(err, no) {
		t.Fatalf("err %v", err)
	}
	edges, err := s.LoadDecisionEdges()
	if err != nil || len(edges) != 0 {
		t.Fatalf("%d edges, %v", len(edges), err)
	}
	// The store is still usable: the failed transaction was rolled back.
	if err := s.AppendDecisionEdge(edge, func([]store.DecisionEdge, []store.DecisionRetraction) error { return nil }); err != nil {
		t.Fatal(err)
	}
}
