//go:build unix

package store_test

import (
	"database/sql"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/paulpham157/sdf/internal/record"
	"github.com/paulpham157/sdf/internal/store"
)

// noUmask clears the process umask for the test, so a file or directory
// mode comes only from what sdf asks for. The umask is process-wide: these
// tests must not run in parallel.
func noUmask(t *testing.T) {
	t.Helper()
	old := syscall.Umask(0)
	t.Cleanup(func() { syscall.Umask(old) })
}

func wantMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Errorf("%s: mode %#o, want %#o", filepath.Base(path), got, want)
	}
}

func insertOne(t *testing.T, s *store.Store) {
	t.Helper()
	if _, err := s.Insert([]record.Record{{Signal: "logs", Name: record.Str("x"), Attributes: map[string]any{}, Resource: map[string]any{}}}); err != nil {
		t.Fatal(err)
	}
}

// TestOpenNewDatabaseIsOwnerOnly: under umask 0, a new database in a
// directory Open creates is 0600, its -wal and -shm (which SQLite creates
// with the main file's mode) are 0600, and the created directory is 0700.
func TestOpenNewDatabaseIsOwnerOnly(t *testing.T) {
	noUmask(t)
	dir := filepath.Join(t.TempDir(), "data")
	path := filepath.Join(dir, "sdf-telemetry.sqlite")
	s, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	insertOne(t, s)
	for _, p := range []string{path, path + "-wal", path + "-shm"} {
		wantMode(t, p, 0o600)
	}
	wantMode(t, dir, 0o700)
}

// TestOpenLeavesExistingDirectoryMode: Open never changes the mode of a
// directory it did not create.
func TestOpenLeavesExistingDirectoryMode(t *testing.T) {
	noUmask(t)
	dir := filepath.Join(t.TempDir(), "shared")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	s, err := store.Open(filepath.Join(dir, "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	wantMode(t, dir, 0o755)
	wantMode(t, filepath.Join(dir, "db.sqlite"), 0o600)
}

// looseWAL writes a WAL database at path whose main, -wal and -shm files
// are 0644, and keeps a connection open so the -wal and -shm stay.
func looseWAL(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	raw.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = raw.Close() })
	var mode string
	if err := raw.QueryRow(`PRAGMA journal_mode = WAL`).Scan(&mode); err != nil || mode != "wal" {
		t.Fatalf("journal_mode %q: %v", mode, err)
	}
	if _, err := raw.Exec(`CREATE TABLE t (x); INSERT INTO t VALUES (1)`); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{path, path + "-wal", path + "-shm"} {
		wantMode(t, p, 0o644)
	}
}

// TestOpenTightensLooseDatabase: an existing 0644 database and its -wal and
// -shm are made 0600 by Open.
func TestOpenTightensLooseDatabase(t *testing.T) {
	noUmask(t)
	path := filepath.Join(t.TempDir(), "old.sqlite")
	looseWAL(t, path)
	s, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	insertOne(t, s)
	for _, p := range []string{path, path + "-wal", path + "-shm"} {
		wantMode(t, p, 0o600)
	}
}

// TestOpenReadOnlyTightensLooseDatabase: a read-only open (sdf summary)
// also tightens a 0644 database it owns, and still reads it.
func TestOpenReadOnlyTightensLooseDatabase(t *testing.T) {
	noUmask(t)
	path := filepath.Join(t.TempDir(), "sdf.sqlite")
	s, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	insertOne(t, s)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	ro, err := store.OpenReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	recs, err := ro.Load()
	if err != nil || len(recs) != 1 {
		t.Fatalf("Load: %d records, %v", len(recs), err)
	}
	wantMode(t, path, 0o600)
}

// TestOpenForScrubTightensLooseDatabase: sdf redact tightens too.
func TestOpenForScrubTightensLooseDatabase(t *testing.T) {
	noUmask(t)
	path := filepath.Join(t.TempDir(), "sdf.sqlite")
	s, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	insertOne(t, s)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	sc, err := store.OpenForScrub(path, false)
	if err != nil {
		t.Fatal(err)
	}
	defer sc.Close()
	wantMode(t, path, 0o600)
}
