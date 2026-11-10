// Package store is a SQLite file of flattened OTLP records.
//
// The schema matches the Python receiver, including the trace_id, span_id,
// and parent_span_id columns added by its migration, plus status_code and
// status_message. Files written by that receiver stay readable: this driver
// is modernc.org/sqlite, a pure-Go SQLite, and attribute JSON is ordinary
// JSON (Python wrote it with sort_keys and spaces; both forms decode here).
// A missing status_code on a span is UNSET.
package store

import (
	"database/sql"
	"encoding/json"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"

	"github.com/paulpham157/sdf/internal/record"
)

const schema = `
CREATE TABLE IF NOT EXISTS records (
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
    parent_span_id TEXT,
    status_code TEXT,
    status_message TEXT
);
CREATE INDEX IF NOT EXISTS idx_records_session ON records(session_id);
CREATE INDEX IF NOT EXISTS idx_records_signal_name ON records(signal, name);
`

// Store is one SQLite file. A mutex serializes use of the single connection,
// matching the Python store's lock around check_same_thread=False.
type Store struct {
	path string
	mu   sync.Mutex
	db   *sql.DB
}

// Open creates the file if needed and applies the schema and column migration.
func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &Store{path: path, db: db}
	if _, err := db.Exec(schema); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// Path is the SQLite file path.
func (s *Store) Path() string { return s.path }

func (s *Store) migrate() error {
	rows, err := s.db.Query(`PRAGMA table_info(records)`)
	if err != nil {
		return err
	}
	defer rows.Close()
	present := map[string]bool{}
	for rows.Next() {
		var cid int
		var name, colType string
		var notNull int
		var dflt sql.NullString
		var pk int
		if err := rows.Scan(&cid, &name, &colType, &notNull, &dflt, &pk); err != nil {
			return err
		}
		present[name] = true
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, col := range []string{"trace_id", "span_id", "parent_span_id", "status_code", "status_message"} {
		if !present[col] {
			if _, err := s.db.Exec(`ALTER TABLE records ADD COLUMN ` + col + ` TEXT`); err != nil {
				return err
			}
		}
	}
	return nil
}

// Close releases the database.
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.db.Close()
}

// Insert appends records. An empty batch is a no-op. Every row in the batch
// shares one received_at timestamp.
func (s *Store) Insert(records []record.Record) (int, error) {
	if len(records) == 0 {
		return 0, nil
	}
	received := time.Now().UTC().Format("2006-01-02T15:04:05.000000-07:00")
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	stmt, err := tx.Prepare(`
		INSERT INTO records (
			received_at, signal, service_name, session_id, name,
			start_unix_nano, end_unix_nano, time_unix_nano, value,
			metric_unit, metric_temporality, metric_kind,
			attributes_json, resource_json, body,
			trace_id, span_id, parent_span_id,
			status_code, status_message
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`)
	if err != nil {
		_ = tx.Rollback()
		return 0, err
	}
	defer stmt.Close()
	for _, rec := range records {
		attrs, err := marshalSorted(rec.Attributes)
		if err != nil {
			_ = tx.Rollback()
			return 0, err
		}
		res, err := marshalSorted(rec.Resource)
		if err != nil {
			_ = tx.Rollback()
			return 0, err
		}
		if _, err := stmt.Exec(
			received,
			rec.Signal,
			nullString(rec.ServiceName),
			nullString(rec.SessionID),
			nullString(rec.Name),
			nullInt(rec.StartUnixNano),
			nullInt(rec.EndUnixNano),
			nullInt(rec.TimeUnixNano),
			nullFloat(rec.Value),
			nullString(rec.MetricUnit),
			nullString(rec.MetricTemporality),
			nullString(rec.MetricKind),
			string(attrs),
			string(res),
			nullString(rec.Body),
			nullString(rec.TraceID),
			nullString(rec.SpanID),
			nullString(rec.ParentSpanID),
			statusCodeValue(rec),
			statusMessageValue(rec),
		); err != nil {
			_ = tx.Rollback()
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return len(records), nil
}

// Load returns every record in insertion order.
func (s *Store) Load() ([]record.Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query(`
		SELECT id, signal, service_name, session_id, name,
		       start_unix_nano, end_unix_nano, time_unix_nano, value,
		       metric_unit, metric_temporality, metric_kind,
		       attributes_json, resource_json, body,
		       trace_id, span_id, parent_span_id,
		       status_code, status_message
		FROM records
		ORDER BY id
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []record.Record
	for rows.Next() {
		var rec record.Record
		var service, session, name, unit, temp, kind, body, trace, span, parent sql.NullString
		var statusCode, statusMessage sql.NullString
		var start, end, when sql.NullInt64
		var value sql.NullFloat64
		var attrs, res string
		if err := rows.Scan(
			&rec.ID, &rec.Signal, &service, &session, &name,
			&start, &end, &when, &value,
			&unit, &temp, &kind,
			&attrs, &res, &body,
			&trace, &span, &parent,
			&statusCode, &statusMessage,
		); err != nil {
			return nil, err
		}
		rec.ServiceName = fromNullString(service)
		rec.SessionID = fromNullString(session)
		rec.Name = fromNullString(name)
		rec.StartUnixNano = fromNullInt(start)
		rec.EndUnixNano = fromNullInt(end)
		rec.TimeUnixNano = fromNullInt(when)
		rec.Value = fromNullFloat(value)
		rec.MetricUnit = fromNullString(unit)
		rec.MetricTemporality = fromNullString(temp)
		rec.MetricKind = fromNullString(kind)
		rec.Body = fromNullString(body)
		rec.TraceID = fromNullString(trace)
		rec.SpanID = fromNullString(span)
		rec.ParentSpanID = fromNullString(parent)
		if rec.Signal == "traces" {
			rec.StatusCode = "UNSET"
			if statusCode.Valid && statusCode.String != "" {
				rec.StatusCode = statusCode.String
			}
			rec.StatusMessage = fromNullString(statusMessage)
		}
		parsedAttrs, err := unmarshalJSON(attrs)
		if err != nil {
			return nil, err
		}
		parsedRes, err := unmarshalJSON(res)
		if err != nil {
			return nil, err
		}
		rec.Attributes = parsedAttrs
		rec.Resource = parsedRes
		out = append(out, rec)
	}
	if out == nil {
		out = []record.Record{}
	}
	return out, rows.Err()
}

func marshalSorted(v map[string]any) ([]byte, error) {
	if v == nil {
		v = map[string]any{}
	}
	return json.Marshal(v)
}

func unmarshalJSON(s string) (map[string]any, error) {
	dec := json.NewDecoder(strings.NewReader(s))
	dec.UseNumber()
	var raw map[string]any
	if err := dec.Decode(&raw); err != nil {
		return nil, err
	}
	out := map[string]any{}
	for k, v := range raw {
		out[k] = normalizeJSON(v)
	}
	return out, nil
}

func normalizeJSON(v any) any {
	switch t := v.(type) {
	case json.Number:
		text := t.String()
		if strings.ContainsAny(text, ".eE") {
			f, err := t.Float64()
			if err != nil {
				return text
			}
			return f
		}
		i, err := t.Int64()
		if err != nil {
			f, ferr := t.Float64()
			if ferr != nil {
				return text
			}
			return f
		}
		return i
	case map[string]any:
		out := map[string]any{}
		for k, item := range t {
			out[k] = normalizeJSON(item)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, item := range t {
			out[i] = normalizeJSON(item)
		}
		return out
	default:
		return v
	}
}

func statusCodeValue(rec record.Record) any {
	if rec.Signal != "traces" {
		return nil
	}
	switch rec.StatusCode {
	case "OK", "ERROR", "UNSET":
		return rec.StatusCode
	default:
		return "UNSET"
	}
}

func statusMessageValue(rec record.Record) any {
	if rec.Signal != "traces" {
		return nil
	}
	return nullString(rec.StatusMessage)
}

func nullString(v *string) any {
	if v == nil {
		return nil
	}
	return *v
}

func nullInt(v *int64) any {
	if v == nil {
		return nil
	}
	return *v
}

func nullFloat(v *float64) any {
	if v == nil {
		return nil
	}
	return *v
}

func fromNullString(v sql.NullString) *string {
	if !v.Valid {
		return nil
	}
	return record.Str(v.String)
}

func fromNullInt(v sql.NullInt64) *int64 {
	if !v.Valid {
		return nil
	}
	return record.Int64(v.Int64)
}

func fromNullFloat(v sql.NullFloat64) *float64 {
	if !v.Valid {
		return nil
	}
	return record.Float(v.Float64)
}
