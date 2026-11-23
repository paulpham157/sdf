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
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
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

CREATE TABLE IF NOT EXISTS decisions (
    id TEXT PRIMARY KEY,
    statement TEXT NOT NULL,
    status TEXT NOT NULL,
    recorded_at TEXT NOT NULL,
    source TEXT,
    decided_by TEXT,
    alternatives TEXT,
    reason TEXT
);
CREATE TABLE IF NOT EXISTS decision_edges (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    relation TEXT NOT NULL,
    from_kind TEXT NOT NULL,
    from_ref TEXT NOT NULL,
    to_kind TEXT NOT NULL,
    to_ref TEXT NOT NULL,
    recorded_at TEXT NOT NULL
);
`

// Store is one SQLite file. A mutex serializes use of the single connection,
// matching the Python store's lock around check_same_thread=False.
type Store struct {
	path string
	mu   sync.Mutex
	db   *sql.DB
	keep func(record.Record) bool
}

// KeepContent sets which records Insert stores with their prompt, tool, and
// error text (sdf serve --store-content). Nil, the default, drops that
// content from every record; see record.Redact.
func (s *Store) KeepContent(keep func(record.Record) bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.keep = keep
}

// busyTimeoutMS is how long a connection waits for another connection's
// lock (a concurrent sdf summary, a second receiver) before SQLITE_BUSY. It
// matches the ledger's busy_timeout.
const busyTimeoutMS = 5000

// Open creates the file if needed and applies the schema and column migration.
//
// busy_timeout is set in the DSN, so it applies to every connection the pool
// opens, before any statement runs. The file is then switched to WAL, as the
// ledger does: in WAL a reader never blocks the writer and the writer never
// blocks readers, so sdf summary can read while the receiver ingests. WAL
// is a property of the file and persists, so it is set once after opening
// (not as a DSN pragma, which can fail with SQLITE_BUSY while a connection
// opens, before the busy timeout applies).
func Open(path string) (*Store, error) {
	name, err := dsn(path, fmt.Sprintf("_pragma=busy_timeout(%d)", busyTimeoutMS))
	if err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", name)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &Store{path: path, db: db}
	if !private(path) {
		if err := setWAL(db); err != nil {
			_ = db.Close()
			return nil, err
		}
	}
	if _, err := db.Exec(schema); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := s.markNewDatabase(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// private reports whether path names an in-memory (":memory:") or temporary
// ("") database: one connection's own, so no other process shares it and WAL
// does not apply.
func private(path string) bool { return path == "" || path == ":memory:" }

// dsn is a file: URI for path with query. The path is made absolute so a
// relative --db keeps meaning the same file; ":memory:" and "" keep their
// SQLite meaning.
func dsn(path, query string) (string, error) {
	if private(path) {
		return "file:" + path + "?" + query, nil
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	p := filepath.ToSlash(abs)
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return (&url.URL{Scheme: "file", Path: p, RawQuery: query}).String(), nil
}

// sqliteReadonly is SQLITE_READONLY, the primary result code for a write to
// a database this process cannot write.
const sqliteReadonly = 8

func sqliteCode(err error) int {
	var coded interface{ Code() int }
	if errors.As(err, &coded) {
		return coded.Code()
	}
	return 0
}

// setWAL switches the file to WAL unless it already is. A file this process
// cannot write (a read-only file, directory, or mount) keeps its current
// journal mode: switching needs a write, and reading it (sdf summary) works
// in either mode, as it did before Open set WAL.
func setWAL(db *sql.DB) error {
	var mode string
	if err := db.QueryRow(`PRAGMA journal_mode`).Scan(&mode); err != nil {
		return err
	}
	if strings.EqualFold(mode, "wal") {
		return nil
	}
	if err := db.QueryRow(`PRAGMA journal_mode = WAL`).Scan(&mode); err != nil {
		if sqliteCode(err)&0xff == sqliteReadonly {
			return nil
		}
		return err
	}
	if !strings.EqualFold(mode, "wal") {
		return fmt.Errorf("store: could not switch the file to WAL (journal_mode is %q)", mode)
	}
	return nil
}

// Path is the SQLite file path.
func (s *Store) Path() string { return s.path }

func (s *Store) migrate() error {
	if err := s.addMissingColumns("records", []string{"trace_id", "span_id", "parent_span_id", "status_code", "status_message"}); err != nil {
		return err
	}
	return s.addMissingColumns("decisions", []string{"decided_by", "alternatives", "reason"})
}

func (s *Store) addMissingColumns(table string, cols []string) error {
	present, err := s.tableColumns(table)
	if err != nil {
		return err
	}
	for _, col := range cols {
		if present[col] {
			continue
		}
		if _, err := s.db.Exec("ALTER TABLE " + table + " ADD COLUMN " + col + " TEXT"); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) tableColumns(table string) (map[string]bool, error) {
	rows, err := s.db.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		return nil, err
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
			return nil, err
		}
		present[name] = true
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return present, nil
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
	s.mu.Lock()
	keep := s.keep
	s.mu.Unlock()
	records = record.RedactWith(records, keep)
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
	return scanRecords(rows)
}

// scanRecords reads rows selected in Load's column order.
func scanRecords(rows *sql.Rows) ([]record.Record, error) {
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

// Decision is one append-only decision record. Status is "hypothesis" or
// "decision". Source, DecidedBy, Alternatives, and Reason are empty when
// the record named none. DecidedBy is "user" or "agent" when set.
type Decision struct {
	ID           string
	Statement    string
	Status       string
	RecordedAt   string
	Source       string
	DecidedBy    string
	Alternatives string
	Reason       string
}

// DecisionEdge is one directed edge. Kind is "decision", "session", or
// "commit". Relation is one of constrains, implements, supersedes,
// validates, contradicts.
type DecisionEdge struct {
	Relation   string
	FromKind   string
	FromRef    string
	ToKind     string
	ToRef      string
	RecordedAt string
}

// InsertDecision appends one decision. The caller has already checked the
// fields. A repeated id is rejected by the primary key.
func (s *Store) InsertDecision(d Decision) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`
		INSERT INTO decisions (id, statement, status, recorded_at, source, decided_by, alternatives, reason)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`, d.ID, d.Statement, d.Status, d.RecordedAt, nullText(d.Source), nullText(d.DecidedBy), nullText(d.Alternatives), nullText(d.Reason))
	return err
}

// LoadDecisions returns every decision in insertion order.
func (s *Store) LoadDecisions() ([]Decision, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query(`
		SELECT id, statement, status, recorded_at, source, decided_by, alternatives, reason
		FROM decisions
		ORDER BY rowid
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Decision
	for rows.Next() {
		var d Decision
		var source, decidedBy, alternatives, reason sql.NullString
		if err := rows.Scan(&d.ID, &d.Statement, &d.Status, &d.RecordedAt, &source, &decidedBy, &alternatives, &reason); err != nil {
			return nil, err
		}
		if source.Valid {
			d.Source = source.String
		}
		if decidedBy.Valid {
			d.DecidedBy = decidedBy.String
		}
		if alternatives.Valid {
			d.Alternatives = alternatives.String
		}
		if reason.Valid {
			d.Reason = reason.String
		}
		out = append(out, d)
	}
	if out == nil {
		out = []Decision{}
	}
	return out, rows.Err()
}

// AppendDecisionEdge appends e only if check, given every edge already
// stored, returns nil; otherwise it writes nothing and returns check's error.
// The read, the check, and the insert run in one BEGIN IMMEDIATE
// transaction. IMMEDIATE takes SQLite's write lock before reading, so
// another Store or process appending at the same time waits (busy_timeout)
// and then reads a set that already holds this edge: two writers cannot both
// pass a check that only one of them should. The caller has already checked
// that both endpoints name something the store knows. Nothing is updated or
// deleted.
func (s *Store) AppendDecisionEdge(e DecisionEdge, check func(existing []DecisionEdge) error) (err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ctx := context.Background()
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(ctx, `ROLLBACK`)
		}
	}()
	existing, err := loadDecisionEdges(ctx, conn)
	if err != nil {
		return err
	}
	if err := check(existing); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, `
		INSERT INTO decision_edges (relation, from_kind, from_ref, to_kind, to_ref, recorded_at)
		VALUES (?, ?, ?, ?, ?, ?)
	`, e.Relation, e.FromKind, e.FromRef, e.ToKind, e.ToRef, e.RecordedAt); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		return err
	}
	committed = true
	return nil
}

// LoadDecisionEdges returns every edge in insertion order.
func (s *Store) LoadDecisionEdges() ([]DecisionEdge, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return loadDecisionEdges(context.Background(), s.db)
}

type querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

func loadDecisionEdges(ctx context.Context, q querier) ([]DecisionEdge, error) {
	rows, err := q.QueryContext(ctx, `
		SELECT relation, from_kind, from_ref, to_kind, to_ref, recorded_at
		FROM decision_edges
		ORDER BY id
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DecisionEdge
	for rows.Next() {
		var e DecisionEdge
		if err := rows.Scan(&e.Relation, &e.FromKind, &e.FromRef, &e.ToKind, &e.ToRef, &e.RecordedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	if out == nil {
		out = []DecisionEdge{}
	}
	return out, rows.Err()
}

// KnownSession reports whether any telemetry row carries this session id.
func (s *Store) KnownSession(id string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM records WHERE session_id = ?`, id).Scan(&n)
	return n > 0, err
}

// KnownCommit reports whether telemetry already names this full commit SHA.
// A SHA is known from an sdf.git.commit hook event, or from a
// claude_code.tool_result log: sdf.git_commit_id (copied out of
// tool_parameters at ingest, because tool_parameters itself is content and
// is not stored by default), vcs.ref.head.revision, git_commit_id, or a
// git_commit_id nested inside tool_parameters (rows stored before that, or
// under --store-content). tool_parameters is read both
// as an object and as a JSON string, matching how the outcome linker reads
// it. The comparison ignores case, so a SHA stored in its original case
// still matches the lowercase ref the decision endpoint passes in. This
// reads the telemetry; it does not copy it or rewrite a stored attribute.
func (s *Store) KnownCommit(sha string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var n int
	err := s.db.QueryRow(`
		SELECT COUNT(*) FROM records
		WHERE signal = 'logs' AND (
			(name = 'sdf.git.commit' AND json_extract(attributes_json, '$."vcs.ref.head.revision"') = ? COLLATE NOCASE)
			OR (name = 'claude_code.tool_result' AND (
				json_extract(attributes_json, '$."sdf.git_commit_id"') = ? COLLATE NOCASE
				OR json_extract(attributes_json, '$."vcs.ref.head.revision"') = ? COLLATE NOCASE
				OR json_extract(attributes_json, '$.git_commit_id') = ? COLLATE NOCASE
				OR json_extract(attributes_json, '$.tool_parameters.git_commit_id') = ? COLLATE NOCASE
				OR json_extract(json_extract(attributes_json, '$.tool_parameters'), '$.git_commit_id') = ? COLLATE NOCASE
			))
		)
	`, sha, sha, sha, sha, sha, sha).Scan(&n)
	return n > 0, err
}

func nullText(v string) any {
	if v == "" {
		return nil
	}
	return v
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
