package store

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
)

// TestDuplicateColumnMessage checks the boundary of the SQLite message.
// "decided_by" is a prefix of "decided_by_source", and a match on the
// shorter name must not treat the longer column's error as its own.
func TestDuplicateColumnMessage(t *testing.T) {
	cases := []struct {
		msg  string
		col  string
		want bool
	}{
		{"duplicate column name: decided_by_source (1)", "decided_by_source", true},
		{"duplicate column name: decided_by_source (1)", "decided_by", false},
		{"duplicate column name: category", "category", true},
		{"duplicate column name: category_extra", "category", false},
		{"SQL logic error: duplicate column name: category (1)", "category", true},
		{"no such table: decisions", "category", false},
	}
	for _, c := range cases {
		if got := duplicateColumn(errors.New(c.msg), c.col); got != c.want {
			t.Errorf("duplicateColumn(%q, %q) = %v, want %v", c.msg, c.col, got, c.want)
		}
	}
}

// TestAddColumnDuplicateIsSuccessWhenPresent adds a column twice inside one
// transaction. The second ALTER is the duplicate-column error, and it must
// leave the column in place exactly once so the transaction can commit.
func TestAddColumnDuplicateIsSuccessWhenPresent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dup.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE decisions (
		id TEXT PRIMARY KEY,
		statement TEXT NOT NULL,
		status TEXT NOT NULL,
		recorded_at TEXT NOT NULL,
		source TEXT
	)`); err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		t.Fatal(err)
	}
	if err := addColumn(ctx, conn, "decisions", "category"); err != nil {
		t.Fatal(err)
	}
	if err := addColumn(ctx, conn, "decisions", "category"); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		t.Fatal(err)
	}
	if got := pragmaColumnCount(t, path, "decisions", "category"); got != 1 {
		t.Fatalf("category columns = %d, want 1", got)
	}
	if got := schemaColumnCount(t, path, "decisions", "category"); got != 1 {
		t.Fatalf("schema mentions category %d times, want 1", got)
	}
}

// TestAddColumnRollsBackWithTheTransaction checks that ADD COLUMN stays in
// the migration transaction. A second connection must not observe the
// column until commit, and a rollback must leave the table unchanged.
func TestAddColumnRollsBackWithTheTransaction(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rollback.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`CREATE TABLE decisions (
		id TEXT PRIMARY KEY,
		statement TEXT NOT NULL,
		status TEXT NOT NULL,
		recorded_at TEXT NOT NULL,
		source TEXT
	)`); err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		t.Fatal(err)
	}
	if err := addColumn(ctx, conn, "decisions", "decided_by_source"); err != nil {
		t.Fatal(err)
	}
	other, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(200)")
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	var n int
	err = other.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('decisions') WHERE name = 'decided_by_source'`).Scan(&n)
	if err == nil && n != 0 {
		t.Fatalf("another connection saw decided_by_source before commit (count %d)", n)
	}
	if _, err := conn.ExecContext(ctx, `ROLLBACK`); err != nil {
		t.Fatal(err)
	}
	if got := pragmaColumnCount(t, path, "decisions", "decided_by_source"); got != 0 {
		t.Fatalf("decided_by_source columns after rollback = %d, want 0", got)
	}
}

// TestConcurrentFirstWritesMigrateOldSchema is the race reported on an old
// file: several processes do their first write together, each opens the
// file, and each tries to add columns the old schema lacks. A goroutine
// with its own Open is a separate connection, which is the lock SQLite
// takes between processes. The file is already WAL, as a file written by
// an older sdf is: setWAL then returns without changing journal_mode, so
// the opens meet in addMissingColumns. None of the writes may fail, and
// each added column exists once. A read-only open of the same file first
// must not add them.
func TestConcurrentFirstWritesMigrateOldSchema(t *testing.T) {
	path := writeOldSchemaDB(t)

	ro, err := OpenReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := ro.LoadDecisions()
	if err != nil {
		t.Fatal(err)
	}
	if err := ro.Close(); err != nil {
		t.Fatal(err)
	}
	if len(loaded) != 1 || loaded[0].ID != "keep-sqlite" || loaded[0].DecidedBy != "" || loaded[0].Category != "" {
		t.Fatalf("read-only load %+v", loaded)
	}
	for _, col := range decisionMigrationColumns {
		if got := pragmaColumnCount(t, path, "decisions", col); got != 0 {
			t.Fatalf("read-only open added decisions.%s (%d)", col, got)
		}
	}

	const n = 8
	start := make(chan struct{})
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			errs[i] = firstDecisionAdd(path, i)
		}(i)
	}
	close(start)
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("writer %d: %v", i, err)
		}
	}
	if t.Failed() {
		return
	}

	for _, col := range decisionMigrationColumns {
		if got := pragmaColumnCount(t, path, "decisions", col); got != 1 {
			t.Errorf("decisions.%s count = %d, want 1", col, got)
		}
		if got := schemaColumnCount(t, path, "decisions", col); got != 1 {
			t.Errorf("schema mentions decisions.%s %d times, want 1", col, got)
		}
	}
	for _, col := range recordMigrationColumns {
		if got := pragmaColumnCount(t, path, "records", col); got != 1 {
			t.Errorf("records.%s count = %d, want 1", col, got)
		}
	}

	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	rows, err := s.LoadDecisions()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != n+1 {
		t.Fatalf("decisions = %d, want %d", len(rows), n+1)
	}
	byID := map[string]Decision{}
	for _, d := range rows {
		byID[d.ID] = d
	}
	old, ok := byID["keep-sqlite"]
	if !ok || old.Statement != "Keep the store in SQLite." || old.DecidedBy != "" || old.Category != "" || old.DecidedBySource != "" {
		t.Fatalf("original row %+v", old)
	}
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("add-%d", i)
		d, ok := byID[id]
		if !ok || d.DecidedBy != "octocat" || d.DecidedBySource != "flag" || d.Category != "implementation" {
			t.Fatalf("row %s %+v", id, d)
		}
	}
}

var (
	decisionMigrationColumns = []string{"decided_by", "decided_by_source", "alternatives", "reason", "category"}
	recordMigrationColumns   = []string{"trace_id", "span_id", "parent_span_id", "status_code", "status_message"}
)

func writeOldSchemaDB(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "old.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`
		CREATE TABLE records (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			received_at TEXT NOT NULL,
			signal TEXT NOT NULL,
			service_name TEXT,
			session_id TEXT,
			name TEXT,
			start_unix_nano INTEGER,
			end_unix_nano INTEGER,
			time_unix_nano INTEGER,
			value REAL,
			metric_unit TEXT,
			metric_temporality TEXT,
			metric_kind TEXT,
			attributes_json TEXT NOT NULL,
			resource_json TEXT NOT NULL,
			body TEXT
		);
		CREATE TABLE decisions (
			id TEXT PRIMARY KEY,
			statement TEXT NOT NULL,
			status TEXT NOT NULL,
			recorded_at TEXT NOT NULL,
			source TEXT
		);
		INSERT INTO decisions (id, statement, status, recorded_at, source)
		VALUES ('keep-sqlite', 'Keep the store in SQLite.', 'decision', '2026-11-23T00:00:00.000000+00:00', 'issue 168');
	`); err != nil {
		t.Fatal(err)
	}
	var mode string
	if err := db.QueryRow(`PRAGMA journal_mode = WAL`).Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if !strings.EqualFold(mode, "wal") {
		t.Fatalf("journal_mode %q, want wal", mode)
	}
	// SQLite creates the file 0644. Open tightens that to 0600 and would
	// print a warning for every writer. Close runs as this function
	// returns, after the mode is set.
	for _, p := range []string{path, path + "-wal", path + "-shm"} {
		if err := os.Chmod(p, 0o600); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
	}
	return path
}

func firstDecisionAdd(path string, i int) error {
	s, err := Open(path)
	if err != nil {
		return err
	}
	defer s.Close()
	return s.InsertDecision(Decision{
		ID:              fmt.Sprintf("add-%d", i),
		Statement:       "Record the decision.",
		Status:          "decision",
		RecordedAt:      "2026-11-28T00:00:00.000000+00:00",
		DecidedBy:       "octocat",
		DecidedBySource: "flag",
		Category:        "implementation",
	})
}

func pragmaColumnCount(t *testing.T, path, table, col string) int {
	t.Helper()
	db, err := sql.Open("sqlite", path+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info(?) WHERE name = ?`, table, col).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func schemaColumnCount(t *testing.T, path, table, col string) int {
	t.Helper()
	db, err := sql.Open("sqlite", path+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var schema string
	if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&schema); err != nil {
		t.Fatal(err)
	}
	re := regexp.MustCompile(`\b` + regexp.QuoteMeta(col) + `\b`)
	return len(re.FindAllString(schema, -1))
}
