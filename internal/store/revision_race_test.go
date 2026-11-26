package store

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

// Two writable opens of a file that has no records_revision row can both
// observe the empty table. Both inserts must succeed, and the generation
// must stay 0 with one row.
func TestMigrateRecordRevisionTwoOpens(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(schema); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	entered := make(chan struct{}, 2)
	release := make(chan struct{})
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
	for i := 0; i < 2; i++ {
		select {
		case <-entered:
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for both opens to reach the generation insert")
		}
	}
	close(release)
	var stores [2]*Store
	for i := 0; i < 2; i++ {
		got := <-results
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
