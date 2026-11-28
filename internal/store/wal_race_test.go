package store

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// TestConcurrentOpensSwitchDeleteJournalToWAL is the race on a
// rollback-journal file: several processes open it together, and each
// setWAL runs PRAGMA journal_mode=WAL while the others hold the file. A
// goroutine with its own Open is a separate connection, which is the lock
// SQLite takes between processes. Every open must succeed, including when
// SQLite returns "database is locked" from the journal-mode change, and the
// file must end in WAL mode. A read-only open is not part of this race: it
// must not change a legacy journal mode.
func TestConcurrentOpensSwitchDeleteJournalToWAL(t *testing.T) {
	path := writeDeleteJournalDB(t)

	const n = 32
	start := make(chan struct{})
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			s, err := Open(path)
			if err != nil {
				errs[i] = err
				return
			}
			errs[i] = s.Close()
		}(i)
	}
	close(start)
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("opener %d: %v", i, err)
		}
		if err != nil && strings.Contains(err.Error(), "database is locked") {
			t.Errorf("opener %d: database is locked", i)
		}
	}
	if t.Failed() {
		return
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
