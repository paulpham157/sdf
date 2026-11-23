package store_test

import (
	"bytes"
	"database/sql"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/paulpham157/sdf/internal/record"
	"github.com/paulpham157/sdf/internal/store"
)

func seed(t *testing.T, path string, sessions ...string) {
	t.Helper()
	s, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range sessions {
		insertSession(t, s, id)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
}

func insertSession(t *testing.T, s *store.Store, id string) {
	t.Helper()
	session := id
	if _, err := s.Insert([]record.Record{{Signal: "logs", SessionID: &session, Attributes: map[string]any{}, Resource: map[string]any{}}}); err != nil {
		t.Error(err)
	}
}

func sidecars(path string) []string {
	var found []string
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		if _, err := os.Stat(path + suffix); err == nil {
			found = append(found, filepath.Base(path+suffix))
		}
	}
	return found
}

func loadSessions(t *testing.T, s *store.Store) []string {
	t.Helper()
	rows, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, r := range rows {
		if r.SessionID != nil {
			ids = append(ids, *r.SessionID)
		}
	}
	return ids
}

func TestOpenReadOnlyMissingFileIsEmptyAndNotCreated(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.sqlite")
	s, err := store.OpenReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	if rows, err := s.Load(); err != nil || len(rows) != 0 {
		t.Fatalf("rows %v err %v", rows, err)
	}
	if _, err := s.Insert([]record.Record{{Signal: "logs", Attributes: map[string]any{}, Resource: map[string]any{}}}); err == nil {
		t.Error("Insert into a read-only store succeeded")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("OpenReadOnly created %s (%v)", path, err)
	}
	if got := sidecars(path); len(got) != 0 {
		t.Fatalf("OpenReadOnly created %v", got)
	}
}

func TestOpenReadOnlyMissingDirectoryIsAnError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nope", "x.sqlite")
	if _, err := store.OpenReadOnly(path); err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("err %v", err)
	}
	if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
		t.Fatalf("OpenReadOnly created the directory (%v)", err)
	}
}

func TestOpenReadOnlyDirectoryIsAnError(t *testing.T) {
	if _, err := store.OpenReadOnly(t.TempDir()); err == nil || !strings.Contains(err.Error(), "directory") {
		t.Fatalf("err %v", err)
	}
}

// TestOpenReadOnlyKeepsRollbackJournalDB is root-safe: mode=ro and
// query_only hold regardless of file permissions.
func TestOpenReadOnlyKeepsRollbackJournalDB(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.db")
	seed(t, path, "a")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`PRAGMA journal_mode = DELETE`); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	s, err := store.OpenReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := loadSessions(t, s); len(got) != 1 || got[0] != "a" {
		t.Fatalf("sessions %v", got)
	}
	mode, timeout, err := store.Pragmas(s)
	if err != nil || mode != "delete" || timeout != 5000 {
		t.Fatalf("journal_mode %q busy_timeout %d err %v, want delete and 5000", mode, timeout, err)
	}
	if _, err := s.Insert([]record.Record{{Signal: "logs", Attributes: map[string]any{}, Resource: map[string]any{}}}); err == nil {
		t.Error("Insert through OpenReadOnly succeeded")
	}
	_ = s.Close()
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Error("OpenReadOnly changed the file")
	}
	if got := sidecars(path); len(got) != 0 {
		t.Errorf("OpenReadOnly created %v", got)
	}
}

func readOnlyDir(t *testing.T, dir string) {
	t.Helper()
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
}

// TestOpenReadOnlyWALInReadOnlyDirectory: no -shm and a directory SQLite
// cannot create one in. A normal mode=ro open fails with
// SQLITE_READONLY_DIRECTORY; with no -wal file the immutable=1 fallback
// reads every committed row.
func TestOpenReadOnlyWALInReadOnlyDirectory(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: a 0555 directory does not stop SQLite creating -shm")
	}
	dir := filepath.Join(t.TempDir(), "ro")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "t.db")
	seed(t, path, "a", "b")
	if got := sidecars(path); len(got) != 0 {
		t.Fatalf("setup left %v", got)
	}
	readOnlyDir(t, dir)
	s, err := store.OpenReadOnly(path)
	if err != nil {
		t.Fatalf("OpenReadOnly: %v", err)
	}
	defer s.Close()
	if got := loadSessions(t, s); len(got) != 2 {
		t.Fatalf("sessions %v", got)
	}
	if got := sidecars(path); len(got) != 0 {
		t.Errorf("OpenReadOnly created %v", got)
	}
}

// TestOpenReadOnlyRefusesWALWithUncheckpointedWrites: a copy that kept its
// -wal (writes not yet in the main file) but no -shm, in a read-only
// directory. immutable=1 would read the main file alone and drop those
// writes, so OpenReadOnly reports the problem instead.
func TestOpenReadOnlyRefusesWALWithUncheckpointedWrites(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: a 0555 directory does not stop SQLite creating -shm")
	}
	src := filepath.Join(t.TempDir(), "live.db")
	seed(t, src, "a")
	live, err := store.Open(src)
	if err != nil {
		t.Fatal(err)
	}
	defer live.Close()
	insertSession(t, live, "only-in-wal")
	dir := filepath.Join(t.TempDir(), "copy")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"", "-wal"} {
		raw, err := os.ReadFile(src + suffix)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "t.db"+suffix), raw, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	readOnlyDir(t, dir)
	_, err = store.OpenReadOnly(filepath.Join(dir, "t.db"))
	if err == nil || !strings.Contains(err.Error(), "-wal") {
		t.Fatalf("err %v, want a refusal naming the -wal file", err)
	}
}

// TestOpenReadOnlyWhileWriterHoldsWriteLock: in WAL mode a reader does not
// wait for a writer's open transaction and sees the last committed state.
func TestOpenReadOnlyWhileWriterHoldsWriteLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.db")
	seed(t, path, "committed")
	w, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	w.SetMaxOpenConns(1)
	if _, err := w.Exec(`BEGIN IMMEDIATE`); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Exec(`INSERT INTO records (received_at, signal, session_id, attributes_json, resource_json)
		VALUES ('2026-11-23T00:00:00+00:00', 'logs', 'uncommitted', '{}', '{}')`); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	s, err := store.OpenReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	got := loadSessions(t, s)
	_ = s.Close()
	if len(got) != 1 || got[0] != "committed" {
		t.Fatalf("sessions %v", got)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("read waited %v for the writer", d)
	}
	if _, err := w.Exec(`COMMIT`); err != nil {
		t.Fatal(err)
	}
}

// TestOpenReadOnlyWaitsForRollbackExclusiveLock: in rollback-journal mode an
// exclusive lock blocks readers; busy_timeout makes the reader wait for it
// instead of failing with SQLITE_BUSY.
func TestOpenReadOnlyWaitsForRollbackExclusiveLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.db")
	seed(t, path, "a")
	w, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	w.SetMaxOpenConns(1)
	if _, err := w.Exec(`PRAGMA journal_mode = DELETE`); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Exec(`BEGIN EXCLUSIVE`); err != nil {
		t.Fatal(err)
	}
	released := make(chan struct{})
	go func() {
		time.Sleep(300 * time.Millisecond)
		_, _ = w.Exec(`COMMIT`)
		close(released)
	}()
	s, err := store.OpenReadOnly(path)
	if err != nil {
		t.Fatalf("OpenReadOnly under an exclusive lock: %v", err)
	}
	got := loadSessions(t, s)
	_ = s.Close()
	<-released
	if len(got) != 1 {
		t.Fatalf("sessions %v", got)
	}
}

// TestOpenReadOnlyReadersAlongsideWriter: readers opened with OpenReadOnly
// while a writer inserts never fail, and every insert is stored.
func TestOpenReadOnlyReadersAlongsideWriter(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.db")
	seed(t, path, "seed")
	w, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	const inserts, readers = 50, 8
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < inserts; i++ {
			insertSession(t, w, "w")
		}
	}()
	for r := 0; r < readers; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 5; i++ {
				s, err := store.OpenReadOnly(path)
				if err != nil {
					t.Error(err)
					return
				}
				if _, err := s.Load(); err != nil {
					t.Error(err)
				}
				_ = s.Close()
			}
		}()
	}
	wg.Wait()
	s, err := store.OpenReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if got := loadSessions(t, s); len(got) != inserts+1 {
		t.Fatalf("%d records, want %d", len(got), inserts+1)
	}
}

// TestOpenReadOnlyReadsPythonWrittenDatabaseWithoutMigrating: the Python
// receiver's file lacks status_code and status_message. Open adds them;
// OpenReadOnly reads them as NULL (UNSET for spans) and leaves the file
// unchanged.
func TestOpenReadOnlyReadsPythonWrittenDatabaseWithoutMigrating(t *testing.T) {
	_, file, _, _ := runtime.Caller(0)
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(file), "testdata", "python-receiver.db"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "python-receiver.db")
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := store.OpenReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	_ = s.Close()
	if len(rows) != 1 || rows[0].SessionID == nil || *rows[0].SessionID != "py-session" {
		t.Fatalf("rows %+v", rows)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(raw, after) {
		t.Error("OpenReadOnly changed the Python-written file")
	}
	if got := sidecars(path); len(got) != 0 {
		t.Errorf("OpenReadOnly created %v", got)
	}
}

// TestOpenReadOnlyFileWithoutTables: an empty SQLite file (zero bytes) has no
// tables; it reads as an empty store and stays empty.
func TestOpenReadOnlyFileWithoutTables(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty.sqlite")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := store.OpenReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	if rows, err := s.Load(); err != nil || len(rows) != 0 {
		t.Fatalf("rows %v err %v", rows, err)
	}
	_ = s.Close()
	if info, err := os.Stat(path); err != nil || info.Size() != 0 {
		t.Fatalf("file changed: %v %v", info, err)
	}
}
