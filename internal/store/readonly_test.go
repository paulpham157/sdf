package store_test

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/paulpham157/sdf/internal/record"
	"github.com/paulpham157/sdf/internal/store"
)

// TestOpenReadOnlyRollbackJournalDB covers a store file sdf cannot write: an
// existing rollback-journal database whose file (0444) and directory (0555)
// are read-only, as `sdf summary --db` meets on a copied or archived file.
// Switching it to WAL needs a write, so Open must keep its journal mode and
// still read it, as it did before WAL was set on open.
//
// Root ignores file permissions, so the check cannot be exercised as root;
// the test skips there rather than pass without testing anything.
func TestOpenReadOnlyRollbackJournalDB(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: 0444/0555 do not stop writes, so a read-only file cannot be made this way")
	}
	dir := filepath.Join(t.TempDir(), "ro")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "t.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(store.Schema); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO records (received_at, signal, session_id, attributes_json, resource_json)
		VALUES ('2026-11-23T00:00:00+00:00', 'logs', 'ro-session', '{}', '{}')`); err != nil {
		t.Fatal(err)
	}
	var mode string
	if err := db.QueryRow(`PRAGMA journal_mode`).Scan(&mode); err != nil || mode != "delete" {
		t.Fatalf("setup journal_mode %q %v", mode, err)
	}
	_ = db.Close()
	if err := os.Chmod(path, 0o444); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	// Prove the file really is unwritable for this user, so the test
	// exercises the read-only path.
	if f, err := os.OpenFile(path, os.O_WRONLY, 0); err == nil {
		_ = f.Close()
		t.Fatal("setup: read-only file is writable")
	}

	s, err := store.Open(path)
	if err != nil {
		t.Fatalf("Open read-only rollback-journal DB: %v", err)
	}
	defer s.Close()
	rows, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].SessionID == nil || *rows[0].SessionID != "ro-session" {
		t.Fatalf("rows %+v", rows)
	}
	mode, timeout, err := store.Pragmas(s)
	if err != nil {
		t.Fatal(err)
	}
	if mode != "delete" || timeout != 5000 {
		t.Fatalf("journal_mode %q busy_timeout %d, want delete and 5000", mode, timeout)
	}
	// NORMAL is set only when WAL is active. A rollback-journal file keeps
	// the default FULL.
	level, err := store.Synchronous(s)
	if err != nil {
		t.Fatal(err)
	}
	if level != store.SynchronousFull {
		t.Fatalf("synchronous %d, want FULL (%d)", level, store.SynchronousFull)
	}
	if _, err := s.Insert([]record.Record{{Signal: "logs", Attributes: map[string]any{}, Resource: map[string]any{}}}); err == nil {
		t.Fatal("Insert into a read-only DB succeeded")
	}
}
