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

// TestSetWALTimeoutReportsDatabaseLocked holds a lock for longer than
// setWAL's retry budget. A reserved lock makes the WAL pragma return
// SQLITE_BUSY at once. A shared lock makes it wait. Both must fail with
// the SQLite lock error, and neither may return before the budget: an
// immediate error would pass a test that only checks the text.
func TestSetWALTimeoutReportsDatabaseLocked(t *testing.T) {
	t.Run("immediate", func(t *testing.T) {
		assertWALTimeoutLocked(t, false)
	})
	t.Run("shared", func(t *testing.T) {
		assertWALTimeoutLocked(t, true)
	})
}

func assertWALTimeoutLocked(t *testing.T, shared bool) {
	t.Helper()
	const budget = 200 * time.Millisecond
	path := writeDeleteJournalDB(t)
	holdLock(t, path, shared)
	walSwitchMu.Lock()
	prev := walSwitchWait
	walSwitchWait = budget
	walSwitchMu.Unlock()
	t.Cleanup(func() {
		walSwitchMu.Lock()
		walSwitchWait = prev
		walSwitchMu.Unlock()
	})

	started := time.Now()
	_, err := openWithin(t, path, 2*time.Second)
	elapsed := time.Since(started)
	if err == nil {
		t.Fatal("Open succeeded while the lock was held")
	}
	if elapsed < budget/2 {
		t.Fatalf("Open returned in %s, want at least %s so an immediate busy error cannot pass", elapsed, budget/2)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("Open took %s, want the shortened budget", elapsed)
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
	return holdLock(t, path, false)
}

// holdLock takes a reserved lock, or a shared lock when shared is set.
// A shared lock is a deferred transaction that has read a row: BEGIN alone
// does not hold the database. The lock lasts until the test ends.
func holdLock(t *testing.T, path string, shared bool) *sql.Conn {
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
	begin := `BEGIN IMMEDIATE`
	if shared {
		begin = `BEGIN`
	}
	if _, err := conn.ExecContext(t.Context(), begin); err != nil {
		conn.Close()
		hold.Close()
		t.Fatal(err)
	}
	if shared {
		// SELECT 1 does not read a database page, so it takes no lock.
		// sqlite_master does, and the deferred transaction holds that shared
		// lock until rollback.
		var name string
		if err := conn.QueryRowContext(t.Context(), `SELECT name FROM sqlite_master LIMIT 1`).Scan(&name); err != nil {
			conn.Close()
			hold.Close()
			t.Fatal(err)
		}
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
