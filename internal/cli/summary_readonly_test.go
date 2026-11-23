package cli_test

import (
	"bytes"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/paulpham157/sdf/internal/record"
	"github.com/paulpham157/sdf/internal/store"
)

// seedStore writes one log record for session "ro-session" through the
// receiver's store.Open, so the file is a WAL database like a live one.
// Closing the last connection checkpoints it and removes -wal and -shm.
func seedStore(t *testing.T, path string) {
	t.Helper()
	s, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	session := "ro-session"
	if _, err := s.Insert([]record.Record{{
		Signal:     "logs",
		SessionID:  &session,
		Attributes: map[string]any{},
		Resource:   map[string]any{"service.name": "claude-code"},
	}}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
}

// journalMode reads the journal mode from the file header (bytes 18 and 19
// are 2 for WAL, 1 for a rollback journal) rather than opening the file, so
// checking it cannot itself create -wal or -shm.
func journalMode(t *testing.T, path string) string {
	t.Helper()
	header := make([]byte, 100)
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.ReadAt(header, 0); err != nil {
		t.Fatal(err)
	}
	switch {
	case header[18] == 2 && header[19] == 2:
		return "wal"
	case header[18] == 1 && header[19] == 1:
		return "delete"
	}
	return "unknown"
}

func assertNoSidecars(t *testing.T, path string) {
	t.Helper()
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		if _, err := os.Stat(path + suffix); err == nil {
			t.Errorf("sdf summary created %s", filepath.Base(path+suffix))
		}
	}
}

// TestSummaryReadsWALDatabaseInReadOnlyDirectory: a WAL store copied or
// archived into a directory sdf cannot write, with no -shm beside it. SQLite
// cannot create the -shm a normal read needs (SQLITE_READONLY_DIRECTORY),
// but with no -wal file every committed row is in the main file, so sdf
// summary reads it. Root ignores directory permissions, so it skips there;
// TestReadOnlyFallbackDecision in internal/store covers the decision as root.
func TestSummaryReadsWALDatabaseInReadOnlyDirectory(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: a 0555 directory does not stop SQLite creating -shm")
	}
	dir := filepath.Join(t.TempDir(), "archive")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "t.db")
	seedStore(t, path)
	if mode := journalMode(t, path); mode != "wal" {
		t.Fatalf("setup journal_mode %q, want wal", mode)
	}
	assertNoSidecars(t, path)
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	out := run(t, "summary", "--db", path, "--json")
	if out.code != 0 || !strings.Contains(out.stdout, "ro-session") {
		t.Fatalf("summary of a WAL DB in a read-only directory: code %d\nstdout %s\nstderr %s", out.code, out.stdout, out.stderr)
	}
	assertNoSidecars(t, path)
}

// TestSummaryLeavesRollbackJournalDatabaseUnchanged: sdf summary only reads,
// so a writable rollback-journal database keeps its journal mode, gets no
// -wal or -shm, and its bytes do not change. This holds as root too.
func TestSummaryLeavesRollbackJournalDatabaseUnchanged(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rollback.db")
	seedStore(t, path)
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	var mode string
	if err := db.QueryRow(`PRAGMA journal_mode = DELETE`).Scan(&mode); err != nil || mode != "delete" {
		t.Fatalf("setup journal_mode %q %v", mode, err)
	}
	_ = db.Close()
	assertNoSidecars(t, path)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	out := run(t, "summary", "--db", path, "--json")
	if out.code != 0 || !strings.Contains(out.stdout, "ro-session") {
		t.Fatalf("summary: code %d\nstdout %s\nstderr %s", out.code, out.stdout, out.stderr)
	}
	if mode := journalMode(t, path); mode != "delete" {
		t.Errorf("sdf summary changed journal_mode to %q, want delete", mode)
	}
	assertNoSidecars(t, path)
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Error("sdf summary changed the database file")
	}
}

// TestSummaryOfMissingDatabaseCreatesNoFile: a --db that does not exist
// reports an empty store, as before, but leaves no file behind.
func TestSummaryOfMissingDatabaseCreatesNoFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.sqlite")
	out := run(t, "summary", "--db", path)
	if out.code != 0 || out.stdout != "No telemetry sessions in the store.\n" {
		t.Fatalf("%d %q %q", out.code, out.stdout, out.stderr)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("sdf summary created %s (stat err %v)", path, err)
	}
	assertNoSidecars(t, path)
}
