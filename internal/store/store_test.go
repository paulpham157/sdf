package store_test

import (
	"database/sql"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/paulpham157/sdf/internal/record"
	"github.com/paulpham157/sdf/internal/store"
)

func TestMigrationAddsIDColumnsAndReadsPythonJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`
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
		INSERT INTO records (
			received_at, signal, service_name, session_id, name,
			time_unix_nano, value, metric_temporality, metric_kind,
			attributes_json, resource_json, body
		) VALUES (
			'2026-11-10T00:00:00+00:00', 'metrics', 'claude-code', 'old-session',
			'claude_code.token.usage', 10, 7.0, 'delta', 'sum',
			'{"model": "claude-sonnet-5", "session.id": "old-session", "type": "input"}',
			'{"sdf.fixture": "sample", "service.name": "claude-code"}',
			NULL
		);
	`)
	if err != nil {
		t.Fatal(err)
	}
	_ = db.Close()

	opened, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer opened.Close()
	if _, err := opened.Insert([]record.Record{{
		Signal:      "logs",
		ServiceName: record.Str("codex-cli"),
		SessionID:   record.Str("old-session"),
		Name:        record.Str("codex.user_prompt"),
		Attributes:  map[string]any{"conversation.id": "old-session"},
		Resource:    map[string]any{"service.name": "codex-cli"},
		TraceID:     record.Str("abc"),
	}}); err != nil {
		t.Fatal(err)
	}
	rows, err := opened.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("%d rows", len(rows))
	}
	if rows[0].TraceID != nil || rows[0].Attributes["type"] != "input" || rows[0].Resource["service.name"] != "claude-code" {
		t.Fatalf("old row %+v attrs %#v", rows[0], rows[0].Attributes)
	}
	if deref(rows[1].TraceID) != "abc" || deref(rows[1].Name) != "codex.user_prompt" {
		t.Fatalf("new row %+v", rows[1])
	}
}

func TestStatusColumnsMigrateAndMissingMeansUnset(t *testing.T) {
	path := filepath.Join(t.TempDir(), "before-status.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`
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
			body TEXT,
			trace_id TEXT,
			span_id TEXT,
			parent_span_id TEXT
		);
		INSERT INTO records (
			received_at, signal, service_name, session_id, name,
			start_unix_nano, end_unix_nano,
			attributes_json, resource_json, trace_id, span_id
		) VALUES (
			'2026-11-10T00:00:00+00:00', 'traces', 'claude-code', 'old',
			'claude_code.tool', 1, 2,
			'{"tool_name":"Bash"}', '{"service.name":"claude-code"}',
			'abcd', 'ef01'
		);
	`)
	if err != nil {
		t.Fatal(err)
	}
	_ = db.Close()

	opened, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer opened.Close()
	if _, err := opened.Insert([]record.Record{{
		Signal:        "traces",
		ServiceName:   record.Str("opencode"),
		SessionID:     record.Str("new"),
		Name:          record.Str("Tool.execute"),
		StatusCode:    "ERROR",
		StatusMessage: record.Str("write failed"),
		Attributes:    map[string]any{},
		Resource:      map[string]any{"service.name": "opencode"},
	}}); err != nil {
		t.Fatal(err)
	}
	rows, err := opened.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("%d rows", len(rows))
	}
	if rows[0].StatusCode != "UNSET" || rows[0].StatusMessage != nil || deref(rows[0].Name) != "claude_code.tool" {
		t.Fatalf("old span %+v %s", rows[0].StatusCode, deref(rows[0].StatusMessage))
	}
	if rows[1].StatusCode != "ERROR" || deref(rows[1].StatusMessage) != "write failed" {
		t.Fatalf("new span %s %s", rows[1].StatusCode, deref(rows[1].StatusMessage))
	}
	if rows[0].Signal == "traces" && rows[1].StatusCode == "" {
		t.Fatal("status was not stored")
	}
}

func TestPythonWrittenDatabaseRemainsReadable(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("caller")
	}
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(file), "testdata", "python-receiver.db"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "python-receiver.db")
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	opened, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer opened.Close()
	rows, err := opened.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("%d rows", len(rows))
	}
	row := rows[0]
	if deref(row.ServiceName) != "claude-code" || deref(row.SessionID) != "py-session" || deref(row.Name) != "claude_code.token.usage" {
		t.Fatalf("%+v", row)
	}
	if row.Value == nil || *row.Value != 11 || row.Attributes["type"] != "input" || row.Attributes["n"] != int64(3) {
		t.Fatalf("value %v attrs %#v", row.Value, row.Attributes)
	}
	nested, ok := row.Attributes["nested"].(map[string]any)
	if !ok || nested["ok"] != true || row.Resource["sdf.fixture"] != "sample" {
		t.Fatalf("nested %#v resource %#v", row.Attributes["nested"], row.Resource)
	}
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
