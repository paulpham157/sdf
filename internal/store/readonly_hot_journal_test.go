package store_test

import (
	"bytes"
	"database/sql"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/paulpham157/sdf/internal/store"
)

const hotJournalChildEnv = "SDF_STORE_HOT_JOURNAL_CHILD"

// TestMain also runs as the writer that dies mid-transaction, leaving a hot
// rollback journal. Copying a live -journal does not reproduce SQLITE_READONLY_ROLLBACK
// (776); the child has to start the write and exit without COMMIT or Close.
func TestMain(m *testing.M) {
	if path := os.Getenv(hotJournalChildEnv); path != "" {
		leaveHotJournal(path)
	}
	os.Exit(m.Run())
}

func leaveHotJournal(path string) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		os.Exit(3)
	}
	db.SetMaxOpenConns(1)
	for _, q := range []string{
		`PRAGMA journal_mode = DELETE`,
		`PRAGMA cache_size = 1`,
		`PRAGMA synchronous = FULL`,
		`BEGIN`,
		`CREATE TABLE scratch (b BLOB)`,
	} {
		if _, err := db.Exec(q); err != nil {
			os.Exit(3)
		}
	}
	// A one-page cache makes SQLite spill changed pages to the file before
	// COMMIT, so exiting here leaves a hot -journal.
	for i := 0; i < 500; i++ {
		if _, err := db.Exec(`INSERT INTO scratch VALUES (randomblob(2000))`); err != nil {
			os.Exit(3)
		}
	}
	os.Exit(9) // no COMMIT, no Close
}

func crashHotJournal(t *testing.T, path string) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Env = append(os.Environ(), hotJournalChildEnv+"="+path)
	err := cmd.Run()
	if ee, ok := err.(*exec.ExitError); !ok || ee.ExitCode() != 9 {
		t.Fatalf("hot-journal child: %v", err)
	}
}

// TestOpenReadOnlyReportsHotJournal is a live SQLITE_READONLY_ROLLBACK (776)
// case: a child starts a rollback-mode write and exits mid-transaction.
func TestOpenReadOnlyReportsHotJournal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.db")
	seed(t, path, "committed")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	var mode string
	if err := db.QueryRow(`PRAGMA journal_mode = DELETE`).Scan(&mode); err != nil || mode != "delete" {
		t.Fatalf("journal_mode %q %v", mode, err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	crashHotJournal(t, path)
	if _, err := os.Stat(path + "-journal"); err != nil {
		t.Fatalf("expected a hot -journal after the crash: %v", err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	journal, err := os.ReadFile(path + "-journal")
	if err != nil {
		t.Fatal(err)
	}

	_, err = store.OpenReadOnly(path)
	if err == nil || !strings.Contains(err.Error(), "hot journal") || !strings.Contains(err.Error(), "776") {
		t.Fatalf("OpenReadOnly: %v, want the explicit hot-journal error wrapping 776", err)
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("OpenReadOnly changed the database file")
	}
	afterJournal, err := os.ReadFile(path + "-journal")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(journal, afterJournal) {
		t.Fatal("OpenReadOnly changed or removed the hot journal")
	}
}
