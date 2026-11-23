package store

import (
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/paulpham157/sdf/internal/record"
)

func fileHash(t *testing.T, path string) [32]byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return sha256.Sum256(raw)
}

func rawExec(t *testing.T, path string, stmts ...string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, q := range stmts {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
}

func sidecars(t *testing.T, path string) []string {
	t.Helper()
	files, _ := filepath.Glob(path + "?*")
	return files
}

// Review S7: a dry run opened the file read-write (schema, migration, and
// stamp). It now opens it read-only: an empty legacy database, an
// old-schema one, and a file that is not sdf's are byte-identical after.
func TestDryRunWritesNothing(t *testing.T) {
	dir := t.TempDir()
	empty := filepath.Join(dir, "empty.sqlite")
	rawExec(t, empty, schema, `PRAGMA user_version = 0`)
	old := filepath.Join(dir, "old.sqlite")
	rawExec(t, old, `CREATE TABLE records (id INTEGER PRIMARY KEY AUTOINCREMENT, received_at TEXT NOT NULL, signal TEXT NOT NULL,
		service_name TEXT, session_id TEXT, name TEXT, start_unix_nano INTEGER, end_unix_nano INTEGER, time_unix_nano INTEGER,
		value REAL, metric_unit TEXT, metric_temporality TEXT, metric_kind TEXT, attributes_json TEXT NOT NULL,
		resource_json TEXT NOT NULL, body TEXT)`,
		`INSERT INTO records (received_at, signal, service_name, name, attributes_json, resource_json, body) VALUES
		('t', 'logs', 'qwen-code', 'qwen-code.api_request', '{"prompt":"SDF-LEAK-old"}', '{}', NULL),
		('t', 'logs', 'opencode', 'user typed SDF-LEAK-name', '{}', '{}', 'user typed SDF-LEAK-name'),
		('t', 'logs', 'claude-code', 'claude_code.api_request', '{}', '{}', 'claude_code.api_request')`)
	other := filepath.Join(dir, "other.sqlite")
	rawExec(t, other, `CREATE TABLE notes (x TEXT)`, `INSERT INTO notes VALUES ('mine')`)

	for _, tc := range []struct {
		path    string
		changed int
		rows    int
		err     error
	}{{empty, 0, 0, nil}, {old, 2, 3, nil}, {other, 0, 0, ErrNotSDF}} {
		before := fileHash(t, tc.path)
		s, err := OpenForScrub(tc.path, true)
		if tc.err != nil {
			if !errors.Is(err, tc.err) {
				t.Fatalf("%s: %v", tc.path, err)
			}
		} else {
			if err != nil {
				t.Fatalf("%s: %v", tc.path, err)
			}
			res, err := s.Scrub(true)
			if err != nil || res.Changed != tc.changed || res.Rows != tc.rows {
				t.Fatalf("%s: %+v %v", tc.path, res, err)
			}
			_ = s.Close()
		}
		if fileHash(t, tc.path) != before || len(sidecars(t, tc.path)) != 0 {
			t.Fatalf("%s: the dry run wrote (sidecars %v)", tc.path, sidecars(t, tc.path))
		}
	}
	// The real run refuses a file that is not sdf's too, and never creates one.
	if _, err := OpenForScrub(other, false); !errors.Is(err, ErrNotSDF) {
		t.Fatalf("not sdf: %v", err)
	}
	missing := filepath.Join(dir, "missing.sqlite")
	if _, err := OpenForScrub(missing, false); err == nil {
		t.Fatal("opened a missing file")
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatal("created the missing file")
	}
	// The old-schema file scrubs for real after its migration, name column included.
	s, err := OpenForScrub(old, false)
	if err != nil {
		t.Fatal(err)
	}
	if res, err := s.Scrub(false); err != nil || res.Changed != 2 {
		t.Fatalf("%+v %v", res, err)
	}
	_ = s.Close()
	if dbContains(t, old, "SDF-LEAK-") {
		t.Fatal("content left after scrubbing the old-schema file")
	}
}

// Review B1 for sdf redact: a log stored with its body as its name loses
// both. Review S9: rows are read in batches, with every row still seen.
func TestScrubBatchesAndCleansTheName(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.sqlite")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.KeepContent(func(record.Record) bool { return true })
	n := 3*scrubBatch + 7
	var recs []record.Record
	for i := 0; i < n; i++ {
		text := fmt.Sprintf("the user typed SDF-LEAK-%d", i)
		recs = append(recs, record.Record{Signal: "logs", ServiceName: record.Str("opencode"), Name: record.Str(text), Body: record.Str(text),
			Attributes: map[string]any{}, Resource: map[string]any{}})
	}
	if _, err := s.Insert(recs); err != nil {
		t.Fatal(err)
	}
	res, err := s.Scrub(false)
	if err != nil || res.Rows != n || res.Changed != n {
		t.Fatalf("%+v %v", res, err)
	}
	if dbContains(t, path, "SDF-LEAK-") {
		t.Fatal("content left in name or body")
	}
	var names int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM records WHERE name IS NOT NULL OR body IS NOT NULL`).Scan(&names); err != nil || names != 0 {
		t.Fatalf("%d rows kept a name or body (%v)", names, err)
	}
}

// Review S8: serve holds the lock shared, redact exclusive; each refuses
// while the other runs, and several serves can share one file.
func TestServeAndScrubLocksExclude(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.sqlite")
	a, err := LockForServe(path)
	if err != nil {
		t.Fatal(err)
	}
	b, err := LockForServe(path)
	if err != nil {
		t.Fatalf("a second serve: %v", err)
	}
	if _, err := LockForScrub(path); !errors.Is(err, ErrLocked) {
		t.Fatalf("redact under serve: %v", err)
	}
	_ = a.Release()
	if _, err := LockForScrub(path); !errors.Is(err, ErrLocked) {
		t.Fatalf("redact under one serve: %v", err)
	}
	_ = b.Release()
	x, err := LockForScrub(path)
	if err != nil {
		t.Fatalf("redact with no serve: %v", err)
	}
	if _, err := LockForServe(path); !errors.Is(err, ErrLocked) {
		t.Fatalf("serve under redact: %v", err)
	}
	if !strings.Contains(LockHolder(path), fmt.Sprint(os.Getpid())) {
		t.Fatalf("holder %q", LockHolder(path))
	}
	_ = x.Release()
	if l, err := LockForServe(path); err != nil {
		t.Fatalf("serve after redact: %v", err)
	} else {
		_ = l.Release()
	}
}

// Review S8: with busy_timeout an insert waits out another connection's
// write lock instead of failing with SQLITE_BUSY (a 500 to the exporter).
func TestInsertWaitsForABusyDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.sqlite")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	other, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	other.SetMaxOpenConns(1)
	tx, err := other.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO records (received_at, signal, attributes_json, resource_json) VALUES ('t', 'logs', '{}', '{}')`); err != nil {
		t.Fatal(err)
	}
	go func() {
		time.Sleep(300 * time.Millisecond)
		_ = tx.Commit()
	}()
	if _, err := s.Insert([]record.Record{{Signal: "logs", Name: record.Str("x"), Attributes: map[string]any{}, Resource: map[string]any{}}}); err != nil {
		t.Fatalf("insert under a held write lock: %v", err)
	}
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM records`).Scan(&n); err != nil || n != 2 {
		t.Fatalf("%d rows %v", n, err)
	}
}
