package store

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"modernc.org/sqlite"
)

// TestOpenRetriesWALSwitchUnderReservedLock is the regression for a
// rollback-journal file whose journal_mode change cannot take the lock.
// Another connection holds BEGIN IMMEDIATE (a reserved lock), which is the
// lock a second process holds. SQLite then returns SQLITE_BUSY ("database
// is locked") from PRAGMA journal_mode=WAL immediately, without waiting out
// busy_timeout. A setWAL that returns that error fails the open. This setWAL
// retries; walBusyHook drops the reserved lock after that first busy result
// so the next attempt can switch the file. The open must succeed and the
// file must end in WAL mode.
func TestOpenRetriesWALSwitchUnderReservedLock(t *testing.T) {
	path := writeDeleteJournalDB(t)
	conn := holdReservedLock(t, path)
	var once sync.Once
	setWALBusyHook(t, func() {
		once.Do(func() {
			_, _ = conn.ExecContext(context.Background(), `ROLLBACK`)
		})
	})

	s, err := openWithin(t, path, 2*time.Second)
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

// TestSetWALTimeoutReportsDatabaseLocked holds the reserved lock for longer
// than setWAL's retry budget. The open fails, and the error is still the
// SQLite busy error, not the budget's context deadline.
func TestSetWALTimeoutReportsDatabaseLocked(t *testing.T) {
	path := writeDeleteJournalDB(t)
	holdReservedLock(t, path)
	walSwitchMu.Lock()
	prev := walSwitchWait
	walSwitchWait = 200 * time.Millisecond
	walSwitchMu.Unlock()
	t.Cleanup(func() {
		walSwitchMu.Lock()
		walSwitchWait = prev
		walSwitchMu.Unlock()
	})

	started := time.Now()
	_, err := openWithin(t, path, 2*time.Second)
	if err == nil {
		t.Fatal("Open succeeded while the reserved lock was held")
	}
	if time.Since(started) > 2*time.Second {
		t.Fatalf("Open took %s, want the shortened budget", time.Since(started))
	}
	if !strings.Contains(err.Error(), "database is locked") {
		t.Fatalf("Open: %v", err)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Open returned the budget deadline: %v", err)
	}
	var se *sqlite.Error
	if !errors.As(err, &se) {
		t.Fatalf("errors.As *sqlite.Error: %v", err)
	}
	if se.Code()&0xff != sqliteBusy && se.Code()&0xff != sqliteLocked {
		t.Fatalf("sqlite code %d, want busy or locked: %v", se.Code(), err)
	}
}

// setWALBusyHook installs hook for this test and clears it afterward.
// walBusyHook is process-wide, so the test must not call t.Parallel.
func setWALBusyHook(t *testing.T, hook func()) {
	t.Helper()
	walSwitchMu.Lock()
	walBusyHook = hook
	walSwitchMu.Unlock()
	t.Cleanup(func() {
		walSwitchMu.Lock()
		walBusyHook = nil
		walSwitchMu.Unlock()
	})
}

// holdReservedLock takes BEGIN IMMEDIATE on path and releases it when the
// test ends.
func holdReservedLock(t *testing.T, path string) *sql.Conn {
	t.Helper()
	hold, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	hold.SetMaxOpenConns(1)
	conn, err := hold.Conn(t.Context())
	if err != nil {
		hold.Close()
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(t.Context(), `BEGIN IMMEDIATE`); err != nil {
		conn.Close()
		hold.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = conn.ExecContext(context.Background(), `ROLLBACK`)
		conn.Close()
		hold.Close()
	})
	return conn
}

// openWithin fails the test if Open does not return within d.
func openWithin(t *testing.T, path string, d time.Duration) (*Store, error) {
	t.Helper()
	type result struct {
		s   *Store
		err error
	}
	ch := make(chan result, 1)
	go func() {
		s, err := Open(path)
		ch <- result{s, err}
	}()
	select {
	case r := <-ch:
		return r.s, r.err
	case <-time.After(d):
		t.Fatalf("Open did not return within %s", d)
		return nil, nil
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
