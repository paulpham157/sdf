package ledger_test

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/paulpham157/sdf/internal/ledger"
)

// TestMain doubles as a writer that dies in the middle of a transaction.
// LEDGER_TEST_CRASH names the ledger file; LEDGER_TEST_JOURNAL is the
// journal mode the crashing connection uses.
func TestMain(m *testing.M) {
	if path := os.Getenv("LEDGER_TEST_CRASH"); path != "" {
		crashMidWrite(path, os.Getenv("LEDGER_TEST_JOURNAL"))
	}
	os.Exit(m.Run())
}

func crashMidWrite(path, journal string) {
	db, err := sql.Open("sqlite", path+"?_pragma=journal_mode("+journal+")")
	if err != nil {
		os.Exit(3)
	}
	db.SetMaxOpenConns(1)
	// A one-page cache makes SQLite spill changed pages to the file (or the
	// WAL) before COMMIT, so a crash leaves a hot journal behind.
	for _, q := range []string{`PRAGMA cache_size = 1`, `BEGIN`, `CREATE TABLE scratch (b BLOB)`} {
		if _, err := db.Exec(q); err != nil {
			os.Exit(3)
		}
	}
	for i := 0; i < 500; i++ {
		if _, err := db.Exec(`INSERT INTO scratch VALUES (randomblob(2000))`); err != nil {
			os.Exit(3)
		}
	}
	if _, err := db.Exec(`UPDATE ledger_entries SET payload = 'torn' WHERE seq = 1`); err != nil {
		os.Exit(3)
	}
	os.Exit(9) // no COMMIT, no Close
}

func crash(t *testing.T, path, journal string) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Env = append(os.Environ(), "LEDGER_TEST_CRASH="+path, "LEDGER_TEST_JOURNAL="+journal)
	err := cmd.Run()
	if ee, ok := err.(*exec.ExitError); !ok || ee.ExitCode() != 9 {
		t.Fatalf("crashing writer: %v", err)
	}
}

func TestReadAfterACrashedWriterInWAL(t *testing.T) {
	ctx := context.Background()
	path, s := seeded(t)
	_ = s.Close()
	crash(t, path, "WAL")
	if _, err := os.Stat(path + "-wal"); err != nil {
		t.Fatalf("expected a -wal file after the crash: %v", err)
	}
	before, _ := os.ReadFile(path)
	rep, err := open(t, path).Verify(ctx)
	if err != nil || !rep.OK || rep.Entries != 4 {
		t.Fatalf("%+v %v", rep, err)
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("verify changed the ledger file")
	}
	// The next append recovers and goes on.
	s = open(t, path)
	appendOK(t, s, taskKind, "after-crash", `{}`)
	if rep, err := s.Verify(ctx); err != nil || !rep.OK || rep.Entries != 5 {
		t.Fatalf("%+v %v", rep, err)
	}
}

// A ledger written by an earlier build is in rollback-journal mode. After a
// crash its hot journal can only be read by rolling it back, which is a
// write, so a read reports an error instead. The next Append rolls it back
// and switches the file to WAL.
func TestHotJournalInAnOldRollbackLedger(t *testing.T) {
	ctx := context.Background()
	path, s := seeded(t)
	_ = s.Close()
	tamper(t, path, `PRAGMA journal_mode = DELETE`)
	crash(t, path, "DELETE")
	if _, err := os.Stat(path + "-journal"); err != nil {
		t.Fatalf("expected a hot journal after the crash: %v", err)
	}
	before, _ := os.ReadFile(path)
	_, err := open(t, path).Verify(ctx)
	if err == nil || !strings.Contains(err.Error(), "hot journal") {
		t.Fatalf("verify: %v", err)
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("verify changed the ledger file")
	}
	if _, err := os.Stat(path + "-journal"); err != nil {
		t.Fatal("verify removed the hot journal")
	}

	s = open(t, path)
	appendOK(t, s, taskKind, "after-crash", `{}`)
	rep, err := s.Verify(ctx)
	if err != nil || !rep.OK || rep.Entries != 5 {
		t.Fatalf("%+v %v", rep, err)
	}
	_ = s.Close()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var mode string
	if err := db.QueryRow(`PRAGMA journal_mode`).Scan(&mode); err != nil || mode != "wal" {
		t.Fatalf("journal_mode %q %v", mode, err)
	}
}

func TestReadOnlyDirectoryWithoutSidecars(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	path, s := seeded(t)
	_ = s.Close()
	dir := filepath.Dir(path)
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	rep, err := open(t, path).Verify(context.Background())
	if err != nil || !rep.OK || rep.Entries != 4 {
		t.Fatalf("%+v %v", rep, err)
	}
}

// Two handles on one file append at the same time. BEGIN IMMEDIATE and the
// busy timeout serialize them; the chain stays contiguous.
func TestConcurrentAppendsFromTwoHandles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ledger.db")
	a, b := open(t, path), open(t, path)
	errs := make(chan error, 2)
	for i, s := range []*ledger.Store{a, b} {
		go func(name string, s *ledger.Store) {
			for j := 0; j < 20; j++ {
				p := []byte(fmt.Sprintf(`{"n":%d}`, j))
				id := fmt.Sprintf("%s-%d", name, j)
				if _, err := s.Append(context.Background(), taskKind, id, p, ledger.PayloadHash(taskKind, p)); err != nil {
					errs <- err
					return
				}
			}
			errs <- nil
		}(fmt.Sprint(i), s)
	}
	for range 2 {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	rep, err := a.Verify(context.Background())
	if err != nil || !rep.OK || rep.Entries != 41 {
		t.Fatalf("%+v %v", rep, err)
	}
}
