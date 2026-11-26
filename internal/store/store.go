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
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"modernc.org/sqlite"

	"github.com/paulpham157/sdf/internal/dbfile"
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
	path   string
	mu     sync.Mutex
	db     *sql.DB
	insert *sql.Stmt
	keep   func(record.Record) bool
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
// A new file is created 0600, and a missing parent directory 0700; an
// existing file that group or others can access is tightened to owner-only
// (see dbfile).
//
// busy_timeout is set in the DSN, so it applies to every connection the pool
// opens, before any statement runs. The file is then switched to WAL, as the
// ledger does: in WAL a reader never blocks the writer and the writer never
// blocks readers, so sdf summary can read while the receiver ingests. WAL
// is a property of the file and persists, so it is set once after opening
// (not as a DSN pragma, which can fail with SQLITE_BUSY while a connection
// opens, before the busy timeout applies).
//
// When the file is in WAL mode, the writer connection sets
// PRAGMA synchronous=NORMAL. The pool is one connection. When database/sql
// replaces it, the new connection sets NORMAL again if it is in WAL mode.
// The pragma is per connection and is not stored in the file, so a reader
// keeps SQLite's default FULL. Under WAL, NORMAL
// does not corrupt the database: an application crash is safe, and a power
// loss or operating-system crash can lose only the most recently committed
// transactions. The ledger stays at synchronous=FULL. An in-memory store and
// a file that is not in WAL mode keep the default FULL, because NORMAL
// outside WAL can leave a rollback-journal database inconsistent.
//
// An error names a file database by its absolute path.
func Open(path string) (*Store, error) {
	s, err := open(path)
	if err != nil {
		if private(path) {
			return nil, err
		}
		if strings.Contains(err.Error(), "unable to open database file") || errors.Is(err, fs.ErrPermission) {
			return nil, fmt.Errorf("database %s: %w; check that its directory exists and is writable", AbsPath(path), err)
		}
		return nil, fmt.Errorf("database %s: %w", AbsPath(path), err)
	}
	return s, nil
}

// AbsPath is path made absolute for messages; path itself when that fails.
func AbsPath(path string) string {
	if abs, err := filepath.Abs(path); err == nil {
		return abs
	}
	return path
}

func open(path string) (*Store, error) {
	name, err := dsn(path, fmt.Sprintf("_pragma=busy_timeout(%d)", busyTimeoutMS))
	if err != nil {
		return nil, err
	}
	if !private(path) {
		// The file is created 0600 (and its directory 0700) before SQLite
		// opens it, so its -wal and -shm are 0600 too; see dbfile.
		if err := dbfile.Create(AbsPath(path)); err != nil {
			return nil, err
		}
	}
	db, err := openWriterDB(name)
	if err != nil {
		return nil, err
	}
	// One connection. synchronous=NORMAL and the prepared insert belong to
	// it. Idle is capped at one so that connection stays checked in. A
	// replacement is a new physical connection: the writer's connector sets
	// NORMAL again when that connection is in WAL mode, and database/sql
	// re-prepares the insert on it.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	s := &Store{path: path, db: db}
	if !private(path) {
		if err := setWAL(db); err != nil {
			_ = db.Close()
			return nil, err
		}
	}
	if err := setWriterSynchronous(db); err != nil {
		_ = db.Close()
		return nil, err
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
	if err := s.prepareInsert(); err != nil {
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

// synchronousNormal is the integer PRAGMA synchronous returns for NORMAL.
// FULL, the SQLite default, is 2.
const synchronousNormal = 1

// openWriterDB opens dsn on a driver whose connections belong to this
// writer. The package driver ("sqlite") is left alone, so a reader and the
// ledger do not run this hook.
//
// database/sql calls Connect again when it discards the single connection.
// The hook then sets synchronous=NORMAL only if that new connection is in
// WAL mode. A new file is not in WAL yet on the first connect; setWAL and
// setWriterSynchronous run after this returns, on that first connection.
func openWriterDB(dsn string) (*sql.DB, error) {
	drv := &sqlite.Driver{}
	drv.RegisterConnectionHook(func(conn sqlite.ExecQuerierContext, _ string) error {
		return writerSynchronousHook(conn)
	})
	return sql.OpenDB(&writerConnector{drv: drv, dsn: dsn}), nil
}

type writerConnector struct {
	drv *sqlite.Driver
	dsn string
}

func (c *writerConnector) Connect(ctx context.Context) (driver.Conn, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return c.drv.Open(c.dsn)
}

func (c *writerConnector) Driver() driver.Driver { return c.drv }

func writerSynchronousHook(conn sqlite.ExecQuerierContext) error {
	mode, err := pragmaText(conn, `PRAGMA journal_mode`)
	if err != nil {
		return err
	}
	if !strings.EqualFold(mode, "wal") {
		return nil
	}
	if _, err := conn.ExecContext(context.Background(), `PRAGMA synchronous = NORMAL`, nil); err != nil {
		return err
	}
	got, err := pragmaText(conn, `PRAGMA synchronous`)
	if err != nil {
		return err
	}
	if got != strconv.Itoa(synchronousNormal) {
		return fmt.Errorf("store: synchronous=%s after setting NORMAL under WAL, want %d", got, synchronousNormal)
	}
	return nil
}

func pragmaText(conn sqlite.ExecQuerierContext, query string) (string, error) {
	rows, err := conn.QueryContext(context.Background(), query, nil)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	dest := make([]driver.Value, len(rows.Columns()))
	if err := rows.Next(dest); err != nil {
		if errors.Is(err, io.EOF) {
			return "", fmt.Errorf("store: %s returned no row", query)
		}
		return "", err
	}
	return driverValueString(dest[0]), nil
}

func driverValueString(v driver.Value) string {
	switch t := v.(type) {
	case string:
		return t
	case []byte:
		return string(t)
	case int64:
		return strconv.FormatInt(t, 10)
	case int:
		return strconv.Itoa(t)
	default:
		return fmt.Sprint(t)
	}
}

// setWriterSynchronous sets PRAGMA synchronous=NORMAL on db's connection
// when the file is in WAL mode. It does nothing otherwise. The first
// connection of a new file reaches WAL only after setWAL, which is after
// the connector hook has already run, so this covers that connection. A
// later connection is covered by the hook. The setting is not written to
// the file: a connection opened separately (sdf summary) still reads FULL,
// and the ledger's own connection is unchanged.
func setWriterSynchronous(db *sql.DB) error {
	var mode string
	if err := db.QueryRow(`PRAGMA journal_mode`).Scan(&mode); err != nil {
		return err
	}
	if !strings.EqualFold(mode, "wal") {
		return nil
	}
	if _, err := db.Exec(`PRAGMA synchronous = NORMAL`); err != nil {
		return err
	}
	var level int
	if err := db.QueryRow(`PRAGMA synchronous`).Scan(&level); err != nil {
		return err
	}
	if level != synchronousNormal {
		return fmt.Errorf("store: synchronous=%d after setting NORMAL under WAL, want %d", level, synchronousNormal)
	}
	return nil
}

// Path is the SQLite file path.
func (s *Store) Path() string { return s.path }

// InMemory reports whether s is an in-memory (":memory:") or temporary ("")
// database, which no other connection can open by its path.
func (s *Store) InMemory() bool { return private(s.path) }

func (s *Store) migrate() error {
	if err := s.addMissingColumns("records", []string{"trace_id", "span_id", "parent_span_id", "status_code", "status_message"}); err != nil {
		return err
	}
	if err := s.addMissingColumns("decisions", []string{"decided_by", "alternatives", "reason"}); err != nil {
		return err
	}
	return s.migrateRetractions()
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
	var err error
	if s.insert != nil {
		err = s.insert.Close()
	}
	if cerr := s.db.Close(); err == nil {
		err = cerr
	}
	return err
}

const insertRecordSQL = `
	INSERT INTO records (
		received_at, signal, service_name, session_id, name,
		start_unix_nano, end_unix_nano, time_unix_nano, value,
		metric_unit, metric_temporality, metric_kind,
		attributes_json, resource_json, body,
		trace_id, span_id, parent_span_id,
		status_code, status_message
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
`

// prepareInsert compiles the insert once on the writer connection. A
// read-only file cannot prepare a write; Insert reports that when it runs.
func (s *Store) prepareInsert() error {
	stmt, err := s.db.Prepare(insertRecordSQL)
	if err != nil {
		if sqliteCode(err)&0xff == sqliteReadonly {
			return nil
		}
		return err
	}
	s.insert = stmt
	return nil
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
	// JSON encoding does not touch the database. Doing it before the lock
	// keeps the writer mutex off the CPU path of a large export.
	rows, err := rowsForInsert(records)
	if err != nil {
		return 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	received := time.Now().UTC().Format("2006-01-02T15:04:05.000000-07:00")
	return s.execInsert(received, rows)
}

type insertRow struct {
	signal        string
	service       any
	session       any
	name          any
	start         any
	end           any
	when          any
	value         any
	unit          any
	temp          any
	kind          any
	attrs         string
	res           string
	body          any
	trace         any
	span          any
	parent        any
	statusCode    any
	statusMessage any
}

func rowsForInsert(records []record.Record) ([]insertRow, error) {
	rows := make([]insertRow, len(records))
	for i, rec := range records {
		attrs, err := marshalSorted(rec.Attributes)
		if err != nil {
			return nil, err
		}
		res, err := marshalSorted(rec.Resource)
		if err != nil {
			return nil, err
		}
		rows[i] = insertRow{
			signal:        rec.Signal,
			service:       nullString(rec.ServiceName),
			session:       nullString(rec.SessionID),
			name:          nullString(rec.Name),
			start:         nullInt(rec.StartUnixNano),
			end:           nullInt(rec.EndUnixNano),
			when:          nullInt(rec.TimeUnixNano),
			value:         nullFloat(rec.Value),
			unit:          nullString(rec.MetricUnit),
			temp:          nullString(rec.MetricTemporality),
			kind:          nullString(rec.MetricKind),
			attrs:         string(attrs),
			res:           string(res),
			body:          nullString(rec.Body),
			trace:         nullString(rec.TraceID),
			span:          nullString(rec.SpanID),
			parent:        nullString(rec.ParentSpanID),
			statusCode:    statusCodeValue(rec),
			statusMessage: statusMessageValue(rec),
		}
	}
	return rows, nil
}

func (s *Store) execInsert(received string, rows []insertRow) (int, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	stmt, err := s.boundInsert(tx)
	if err != nil {
		_ = tx.Rollback()
		return 0, err
	}
	defer stmt.Close()
	for _, row := range rows {
		if _, err := stmt.Exec(
			received,
			row.signal,
			row.service,
			row.session,
			row.name,
			row.start,
			row.end,
			row.when,
			row.value,
			row.unit,
			row.temp,
			row.kind,
			row.attrs,
			row.res,
			row.body,
			row.trace,
			row.span,
			row.parent,
			row.statusCode,
			row.statusMessage,
		); err != nil {
			_ = tx.Rollback()
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return len(rows), nil
}

// boundInsert is the insert compiled at Open, attached to tx. A store that
// could not prepare (a read-only file) compiles it on tx, which fails the
// write there.
func (s *Store) boundInsert(tx *sql.Tx) (*sql.Stmt, error) {
	if s.insert != nil {
		return tx.Stmt(s.insert), nil
	}
	return tx.Prepare(insertRecordSQL)
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
// validates, contradicts, blocks, conflicts_with, refines.
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
	return s.LoadDecisionsContext(context.Background())
}

// LoadDecisionsContext is LoadDecisions bound to ctx.
func (s *Store) LoadDecisionsContext(ctx context.Context) ([]Decision, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.QueryContext(ctx, `
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

// DecisionGraph is what AppendDecisionEdge's check sees: every stored
// decision id, edge, and retraction, all read in the edge's transaction.
type DecisionGraph struct {
	DecisionIDs map[string]bool
	Edges       []DecisionEdge
	Retractions []DecisionRetraction
}

// AppendDecisionEdge appends e only if check, given every decision id, edge,
// and retraction already stored, returns nil; otherwise it writes nothing
// and returns check's error.
// The read, the check, and the insert run in one BEGIN IMMEDIATE
// transaction. IMMEDIATE takes SQLite's write lock before reading, so
// another Store or process appending at the same time waits (busy_timeout)
// and then reads a set that already holds this edge: two writers cannot both
// pass a check that only one of them should. The caller has already checked
// that both endpoints name something the store knows. Nothing is updated or
// deleted.
func (s *Store) AppendDecisionEdge(e DecisionEdge, check func(DecisionGraph) error) (err error) {
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
	ids, err := loadDecisionIDs(ctx, conn)
	if err != nil {
		return err
	}
	existing, err := loadDecisionEdges(ctx, conn)
	if err != nil {
		return err
	}
	retracted, err := loadDecisionRetractions(ctx, conn)
	if err != nil {
		return err
	}
	if err := check(DecisionGraph{DecisionIDs: ids, Edges: existing, Retractions: retracted}); err != nil {
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

// loadDecisionIDs reads every stored decision id through q.
func loadDecisionIDs(ctx context.Context, q querier) (map[string]bool, error) {
	rows, err := q.QueryContext(ctx, `SELECT id FROM decisions`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids[id] = true
	}
	return ids, rows.Err()
}

// LoadDecisionEdges returns every edge in insertion order.
func (s *Store) LoadDecisionEdges() ([]DecisionEdge, error) {
	return s.LoadDecisionEdgesContext(context.Background())
}

// LoadDecisionEdgesContext is LoadDecisionEdges bound to ctx.
func (s *Store) LoadDecisionEdgesContext(ctx context.Context) ([]DecisionEdge, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return loadDecisionEdges(ctx, s.db)
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
