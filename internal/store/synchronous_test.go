package store_test

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/paulpham157/sdf/internal/record"
	"github.com/paulpham157/sdf/internal/store"
)

// TestWriterSynchronousNormalOnlyUnderWAL checks that a file store is in WAL
// mode and that PRAGMA synchronous=NORMAL is set on the writer connection
// only. A reader and a connection opened beside the store keep FULL.
// synchronous is not stored in the file.
func TestWriterSynchronousNormalOnlyUnderWAL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "telemetry.sqlite")
	s, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.Insert([]record.Record{{
		Signal: "logs", Name: record.Str("bench"), Attributes: map[string]any{"k": "v"}, Resource: map[string]any{},
	}}); err != nil {
		t.Fatal(err)
	}

	mode, timeout, err := store.Pragmas(s)
	if err != nil {
		t.Fatal(err)
	}
	if mode != "wal" {
		t.Fatalf("writer journal_mode %q, want wal", mode)
	}
	if timeout != 5000 {
		t.Fatalf("busy_timeout %d, want 5000", timeout)
	}
	level, err := store.Synchronous(s)
	if err != nil {
		t.Fatal(err)
	}
	if level != store.SynchronousNormal {
		t.Fatalf("writer synchronous %d, want NORMAL (%d)", level, store.SynchronousNormal)
	}

	ro, err := store.OpenReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	roMode, _, err := store.Pragmas(ro)
	if err != nil {
		t.Fatal(err)
	}
	if roMode != "wal" {
		t.Fatalf("reader journal_mode %q, want wal", roMode)
	}
	roLevel, err := store.Synchronous(ro)
	if err != nil {
		t.Fatal(err)
	}
	if roLevel != store.SynchronousFull {
		t.Fatalf("reader synchronous %d, want FULL (%d)", roLevel, store.SynchronousFull)
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var otherMode string
	var otherLevel int
	if err := db.QueryRow(`PRAGMA journal_mode`).Scan(&otherMode); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`PRAGMA synchronous`).Scan(&otherLevel); err != nil {
		t.Fatal(err)
	}
	if otherMode != "wal" || otherLevel != store.SynchronousFull {
		t.Fatalf("other connection journal_mode %q synchronous %d, want wal and FULL (%d)", otherMode, otherLevel, store.SynchronousFull)
	}
}

// TestWriterSynchronousSurvivesConnReplace discards the pooled writer
// connection and checks the replacement is a different connection, still
// in WAL, still NORMAL, and still able to use the insert prepared at Open.
func TestWriterSynchronousSurvivesConnReplace(t *testing.T) {
	path := filepath.Join(t.TempDir(), "telemetry.sqlite")
	s, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.Insert([]record.Record{{
		Signal: "logs", Name: record.Str("before"), Attributes: map[string]any{"n": "1"}, Resource: map[string]any{},
	}}); err != nil {
		t.Fatal(err)
	}
	before, err := store.WriterConnID(s)
	if err != nil {
		t.Fatal(err)
	}
	if err := wantSync(t, s, "wal", store.SynchronousNormal); err != nil {
		t.Fatal(err)
	}

	store.ExpireWriterConn(s)
	after, err := store.WriterConnID(s)
	if err != nil {
		t.Fatal(err)
	}
	if after == "" || after == before {
		t.Fatalf("writer connection %q was not replaced (was %q)", after, before)
	}
	if err := wantSync(t, s, "wal", store.SynchronousNormal); err != nil {
		t.Fatal(err)
	}
	// The connection WriterConnID just opened is the one later inserts use.
	store.KeepWriterConn(s)
	if _, err := s.Insert([]record.Record{{
		Signal: "logs", Name: record.Str("after"), Attributes: map[string]any{"n": "2"}, Resource: map[string]any{},
	}}); err != nil {
		t.Fatal(err)
	}
	rows, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].Name == nil || *rows[0].Name != "before" || rows[1].Name == nil || *rows[1].Name != "after" {
		t.Fatalf("rows %+v", rows)
	}
	if err := wantSync(t, s, "wal", store.SynchronousNormal); err != nil {
		t.Fatal(err)
	}
}

// TestInMemorySynchronousSurvivesConnReplace: a replaced in-memory
// connection is not in WAL mode, so it stays at FULL.
func TestInMemorySynchronousSurvivesConnReplace(t *testing.T) {
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	before, err := store.WriterConnID(s)
	if err != nil {
		t.Fatal(err)
	}
	store.ExpireWriterConn(s)
	after, err := store.WriterConnID(s)
	if err != nil {
		t.Fatal(err)
	}
	if after == "" || after == before {
		t.Fatalf("connection %q was not replaced (was %q)", after, before)
	}
	store.KeepWriterConn(s)
	mode, _, err := store.Pragmas(s)
	if err != nil {
		t.Fatal(err)
	}
	if mode == "wal" {
		t.Fatalf("in-memory journal_mode %q", mode)
	}
	level, err := store.Synchronous(s)
	if err != nil {
		t.Fatal(err)
	}
	if level != store.SynchronousFull {
		t.Fatalf("replaced in-memory synchronous %d, want FULL (%d)", level, store.SynchronousFull)
	}
}

func wantSync(t *testing.T, s *store.Store, journal string, level int) error {
	t.Helper()
	mode, _, err := store.Pragmas(s)
	if err != nil {
		return err
	}
	if mode != journal {
		return fmt.Errorf("journal_mode %q, want %s", mode, journal)
	}
	got, err := store.Synchronous(s)
	if err != nil {
		return err
	}
	if got != level {
		return fmt.Errorf("synchronous %d, want %d", got, level)
	}
	return nil
}

// TestInMemoryStoreKeepsFullSynchronous: an in-memory store is not a WAL
// file, so Open does not set NORMAL.
func TestInMemoryStoreKeepsFullSynchronous(t *testing.T) {
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	mode, _, err := store.Pragmas(s)
	if err != nil {
		t.Fatal(err)
	}
	if mode == "wal" {
		t.Fatalf("in-memory journal_mode %q", mode)
	}
	level, err := store.Synchronous(s)
	if err != nil {
		t.Fatal(err)
	}
	if level != store.SynchronousFull {
		t.Fatalf("in-memory synchronous %d, want FULL (%d)", level, store.SynchronousFull)
	}
}
