package store

import (
	"database/sql"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// Two writable opens of a file that has no records_revision row can both
// observe the empty table. Both inserts must succeed, and the generation
// must stay 0 with one row.
//
// The opens rendezvous in the insert hook, which runs only after each
// connection is already open. SQLite will not change journal_mode while
// another connection has the file, and busy_timeout turns that wait into
// SQLITE_BUSY, so the second open never reaches the hook. The fixture is
// put into WAL first — the mode a file keeps after its first successful
// Open — and the test goroutine waits on the hook channel and the open
// results together. An open that fails, or that finishes without entering
// the hook, fails the test without a timer.
func TestMigrateRecordRevisionTwoOpens(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(schema); err != nil {
		t.Fatal(err)
	}
	var mode string
	if err := db.QueryRow(`PRAGMA journal_mode = WAL`).Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if !strings.EqualFold(mode, "wal") {
		t.Fatalf("journal_mode %q, want wal", mode)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseBoth := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(releaseBoth)
	recordRevisionInsertHook = func() {
		entered <- struct{}{}
		<-release
	}
	t.Cleanup(func() { recordRevisionInsertHook = nil })

	type opened struct {
		s   *Store
		err error
	}
	results := make(chan opened, 2)
	openOne := func() {
		s, err := Open(path)
		results <- opened{s, err}
	}
	go openOne()
	go openOne()

	var finished []opened
	nEntered := 0
	early := ""
	for nEntered < 2 && early == "" {
		select {
		case <-entered:
			nEntered++
		case got := <-results:
			finished = append(finished, got)
			if got.err != nil {
				early = got.err.Error()
			} else {
				early = "open finished before both reached the generation insert"
			}
		}
	}
	releaseBoth()
	for len(finished) < 2 {
		finished = append(finished, <-results)
	}
	if early != "" {
		for _, got := range finished {
			if got.s != nil {
				_ = got.s.Close()
			}
		}
		t.Fatal(early)
	}

	var stores [2]*Store
	for i, got := range finished {
		if got.err != nil {
			t.Fatal(got.err)
		}
		stores[i] = got.s
	}
	defer stores[0].Close()
	defer stores[1].Close()

	snap, ok, err := stores[0].RecordSnapshot()
	if err != nil || !ok || snap.Generation != 0 || snap.MaxID != 0 {
		t.Fatalf("snapshot %+v ok %v err %v", snap, ok, err)
	}
	var n int
	if err := stores[0].db.QueryRow(`SELECT COUNT(*) FROM records_revision`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("records_revision rows %d, want 1", n)
	}
}
