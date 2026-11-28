package store

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// TestConcurrentOpensSwitchDeleteJournalToWAL is the regression for a
// rollback-journal file whose journal_mode change cannot take the lock.
// Another connection holds BEGIN IMMEDIATE (a reserved lock), which is the
// lock a second process holds. SQLite then returns SQLITE_BUSY ("database
// is locked") from PRAGMA journal_mode=WAL immediately, without waiting out
// busy_timeout. A setWAL that returns that error fails the open. This setWAL
// retries; walBusyHook drops the reserved lock after that first busy result
// so the next attempt can switch the file. The open must succeed and the
// file must end in WAL mode.
func TestConcurrentOpensSwitchDeleteJournalToWAL(t *testing.T) {
	path := writeDeleteJournalDB(t)
	ctx := t.Context()

	hold, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	hold.SetMaxOpenConns(1)
	conn, err := hold.Conn(ctx)
	if err != nil {
		hold.Close()
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		conn.Close()
		hold.Close()
		t.Fatal(err)
	}
	var once sync.Once
	walBusyHook = func() {
		once.Do(func() {
			_, _ = conn.ExecContext(context.Background(), `ROLLBACK`)
		})
	}
	t.Cleanup(func() {
		walBusyHook = nil
		_, _ = conn.ExecContext(context.Background(), `ROLLBACK`)
		conn.Close()
		hold.Close()
	})

	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	db, err := sql.Open("sqlite", path+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var mode string
	if err := db.QueryRow(`PRAGMA journal_mode`).Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if !strings.EqualFold(mode, "wal") {
		t.Fatalf("journal_mode %q, want wal", mode)
	}
}

func writeDeleteJournalDB(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "delete.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(schema); err != nil {
		t.Fatal(err)
	}
	var mode string
	if err := db.QueryRow(`PRAGMA journal_mode = DELETE`).Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if !strings.EqualFold(mode, "delete") {
		t.Fatalf("journal_mode %q, want delete", mode)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{path + "-wal", path + "-shm", path + "-journal"} {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
	}
	// SQLite creates the file 0644. Open tightens that to 0600 and would
	// print a warning for every opener.
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
