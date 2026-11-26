package store_test

import (
	"database/sql"
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
