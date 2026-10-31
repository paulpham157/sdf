package toolproxy

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

// Audit event names match sdf_core.policy.AuditEvent plus the internal
// action_claimed fence row (ADR 0026). policy_decided is one row per delivery.
// The three execution events are at most one each per attempt_id + action_id.
const (
	EventPolicyDecided  = "policy_decided"
	EventActionClaimed  = "action_claimed"
	EventActionExecuted = "action_executed"
	EventActionFailed   = "action_failed"
)

// Observation is one append-only Tool Proxy audit row.
// Value is set only on action_executed so a Go process can replay Result.Value.
// Python tool_audits omit the executor return value; that deviation is recorded
// in ADR 0037.
type Observation struct {
	AuditID   string
	AttemptID string
	ActionID  string
	Event     string
	Actor     string
	Principal string
	Tool      string
	Action    string
	Resource  string
	Decision  string // allow | deny
	Reason    string
	Policy    string
	Value     any
	Error     string
	CreatedAt time.Time
}

// StoredOutcome is the terminal execution observation for one action
// (action_executed or action_failed). Lookup replays this; it is not a
// policy_decided row.
type StoredOutcome struct {
	AttemptID string
	ActionID  string
	Status    string // executed | failed
	Actor     string
	Principal string
	Tool      string
	Action    string
	Resource  string
	Reason    string
	Policy    string
	Value     any
	Error     string
	CreatedAt time.Time
}

// OutcomeStore is the durable idempotency fence keyed by attempt_id + action_id.
// Execution events are unique per action. policy_decided is appended once per
// delivery (ADR 0026). Lookup replays the terminal execution event.
type OutcomeStore interface {
	// Lookup returns a terminal outcome (executed|failed) if one exists.
	Lookup(attemptID, actionID string) (StoredOutcome, bool, error)
	// Claim atomically reserves the execution slot and appends action_claimed.
	// o.AttemptID and o.ActionID identify the action; the other fields are
	// stored on that row. Returns false when the action is already claimed
	// or already terminal.
	Claim(o Observation) (bool, error)
	// Complete appends action_executed or action_failed for a claimed action.
	// The first terminal outcome sticks: the same status is a no-op, and the
	// other status is refused.
	Complete(o StoredOutcome) error
	// AppendPolicy records one policy_decided row. A new AuditID inserts a
	// row; the same AuditID is a no-op (redelivery of that record).
	AppendPolicy(o Observation) error
	// Observations returns events for one action, oldest first.
	Observations(attemptID, actionID string) ([]Observation, error)
	Close() error
}

// MemoryStore is a process-local fence (tests / default when no path is set).
type MemoryStore struct {
	mu     sync.Mutex
	byID   map[string]struct{}
	events map[string][]Observation
}

// NewMemoryStore builds an in-process OutcomeStore.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		byID:   make(map[string]struct{}),
		events: make(map[string][]Observation),
	}
}

func fenceKey(attemptID, actionID string) string {
	return attemptID + "\x00" + actionID
}

func (s *MemoryStore) Lookup(attemptID, actionID string) (StoredOutcome, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return terminalFrom(s.events[fenceKey(attemptID, actionID)])
}

func (s *MemoryStore) Claim(o Observation) (bool, error) {
	if err := requireIDs(o.AttemptID, o.ActionID); err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	k := fenceKey(o.AttemptID, o.ActionID)
	if _, found, err := terminalFrom(s.events[k]); err != nil || found {
		return false, err
	}
	for _, ev := range s.events[k] {
		if ev.Event == EventActionClaimed {
			return false, nil
		}
	}
	o.Event = EventActionClaimed
	o.AuditID = newAuditID("AUDIT-CLAIM")
	if o.CreatedAt.IsZero() {
		o.CreatedAt = time.Now().UTC()
	}
	s.byID[o.AuditID] = struct{}{}
	s.events[k] = append(s.events[k], o)
	return true, nil
}

func (s *MemoryStore) Complete(o StoredOutcome) error {
	if o.Status != StatusExecuted && o.Status != StatusFailed {
		return fmt.Errorf("toolproxy: Complete requires executed or failed, got %q", o.Status)
	}
	if err := requireIDs(o.AttemptID, o.ActionID); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	k := fenceKey(o.AttemptID, o.ActionID)
	existing, found, err := terminalFrom(s.events[k])
	if err != nil {
		return err
	}
	if found {
		return sameTerminal(existing.Status, o.Status)
	}
	ev := observationFromOutcome(o)
	ev.AuditID = newAuditID("AUDIT")
	s.byID[ev.AuditID] = struct{}{}
	s.events[k] = append(s.events[k], ev)
	return nil
}

func (s *MemoryStore) AppendPolicy(o Observation) error {
	if err := requireIDs(o.AttemptID, o.ActionID); err != nil {
		return err
	}
	o.Event = EventPolicyDecided
	o.Value = nil
	if o.AuditID == "" {
		o.AuditID = newAuditID("AUDIT")
	}
	if o.CreatedAt.IsZero() {
		o.CreatedAt = time.Now().UTC()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.byID[o.AuditID]; ok {
		return nil
	}
	s.byID[o.AuditID] = struct{}{}
	k := fenceKey(o.AttemptID, o.ActionID)
	s.events[k] = append(s.events[k], o)
	return nil
}

func (s *MemoryStore) Observations(attemptID, actionID string) ([]Observation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	src := s.events[fenceKey(attemptID, actionID)]
	out := make([]Observation, len(src))
	copy(out, src)
	return out, nil
}

func (s *MemoryStore) Close() error { return nil }

// SQLiteStore persists the event log with modernc.org/sqlite (no CGO).
// Execution events use a partial unique index (ADR 0026: WHERE event <> 'policy_decided').
// A second partial unique index allows only one terminal event per action.
type SQLiteStore struct {
	db *sql.DB
}

// OpenSQLiteStore opens (or creates) a durable fence at path.
// Use ":memory:" for ephemeral SQLite (still SQL semantics, not restart-safe).
// A file written by the #107 tool_outcomes table is copied into the event log
// and that table is dropped.
func OpenSQLiteStore(path string) (*SQLiteStore, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	// One writer; claims are serialised on this connection.
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`PRAGMA busy_timeout = 5000`); err != nil {
		_ = db.Close()
		return nil, err
	}
	s := &SQLiteStore{db: db}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func (s *SQLiteStore) migrate() error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	stmts := []string{
		`CREATE TABLE IF NOT EXISTS tool_outcome_events (
  event_id    TEXT PRIMARY KEY,
  attempt_id  TEXT NOT NULL,
  action_id   TEXT NOT NULL,
  event       TEXT NOT NULL CHECK (event IN ('policy_decided', 'action_claimed', 'action_executed', 'action_failed')),
  actor       TEXT NOT NULL DEFAULT '',
  principal   TEXT NOT NULL DEFAULT '',
  tool        TEXT NOT NULL DEFAULT '',
  action      TEXT NOT NULL DEFAULT '',
  resource    TEXT NOT NULL DEFAULT '',
  decision    TEXT NOT NULL DEFAULT '',
  reason      TEXT NOT NULL DEFAULT '',
  policy      TEXT NOT NULL DEFAULT '',
  value_json  TEXT,
  error       TEXT NOT NULL DEFAULT '',
  created_at  TEXT NOT NULL
)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS uq_tool_outcome_execution_event
  ON tool_outcome_events (attempt_id, action_id, event)
  WHERE event <> 'policy_decided'`,
		`CREATE UNIQUE INDEX IF NOT EXISTS uq_tool_outcome_terminal
  ON tool_outcome_events (attempt_id, action_id)
  WHERE event IN ('action_executed', 'action_failed')`,
		`CREATE INDEX IF NOT EXISTS ix_tool_outcome_events_action
  ON tool_outcome_events (attempt_id, action_id)`,
	}
	for _, stmt := range stmts {
		if _, err := tx.Exec(stmt); err != nil {
			return err
		}
	}
	if err := migrateLegacyOutcomes(tx); err != nil {
		return err
	}
	return tx.Commit()
}

func migrateLegacyOutcomes(tx *sql.Tx) error {
	var name string
	err := tx.QueryRow(`SELECT name FROM sqlite_master WHERE type = 'table' AND name = 'tool_outcomes'`).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	rows, err := tx.Query(`
SELECT attempt_id, action_id, status, actor, reason, policy, value_json, error, created_at
FROM tool_outcomes`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var attemptID, actionID, status, actor, reason, policy, errText, created string
		var valueJSON sql.NullString
		if err := rows.Scan(&attemptID, &actionID, &status, &actor, &reason, &policy, &valueJSON, &errText, &created); err != nil {
			return err
		}
		if status != "claimed" && status != StatusExecuted && status != StatusFailed {
			return fmt.Errorf("toolproxy: cannot migrate tool_outcomes status %q", status)
		}
		claim := Observation{
			AuditID:   legacyAuditID("claim", attemptID, actionID),
			AttemptID: attemptID,
			ActionID:  actionID,
			Event:     EventActionClaimed,
			Actor:     actor,
			Reason:    reason,
			Policy:    policy,
			CreatedAt: parseTime(created),
		}
		if err := insertEvent(tx, claim); err != nil {
			return err
		}
		if status == "claimed" {
			continue
		}
		var value any
		if valueJSON.Valid && valueJSON.String != "" {
			_ = json.Unmarshal([]byte(valueJSON.String), &value)
		}
		terminal := Observation{
			AuditID:   legacyAuditID(status, attemptID, actionID),
			AttemptID: attemptID,
			ActionID:  actionID,
			Event:     eventForStatus(status),
			Actor:     actor,
			Decision:  "allow",
			Reason:    reason,
			Policy:    policy,
			Value:     value,
			Error:     errText,
			CreatedAt: parseTime(created),
		}
		if err := insertEvent(tx, terminal); err != nil {
			return err
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	// Close the read before DROP. The same transaction cannot drop a table
	// it is still scanning.
	if err := rows.Close(); err != nil {
		return err
	}
	_, err = tx.Exec(`DROP TABLE tool_outcomes`)
	return err
}

func (s *SQLiteStore) Lookup(attemptID, actionID string) (StoredOutcome, bool, error) {
	o, found, err := lookupTerminal(s.db, attemptID, actionID)
	return o, found, err
}

func (s *SQLiteStore) Claim(o Observation) (bool, error) {
	if err := requireIDs(o.AttemptID, o.ActionID); err != nil {
		return false, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()

	if _, found, err := lookupTerminal(tx, o.AttemptID, o.ActionID); err != nil || found {
		return false, err
	}
	var existing string
	err = tx.QueryRow(`
SELECT event_id FROM tool_outcome_events
WHERE attempt_id = ? AND action_id = ? AND event = ?`,
		o.AttemptID, o.ActionID, EventActionClaimed).Scan(&existing)
	if err == nil {
		return false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	o.Event = EventActionClaimed
	o.AuditID = newAuditID("AUDIT-CLAIM")
	o.Value = nil
	if o.CreatedAt.IsZero() {
		o.CreatedAt = time.Now().UTC()
	}
	if err := insertEvent(tx, o); err != nil {
		if isUniqueViolation(err) {
			return false, nil
		}
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

func (s *SQLiteStore) Complete(o StoredOutcome) error {
	if o.Status != StatusExecuted && o.Status != StatusFailed {
		return fmt.Errorf("toolproxy: Complete requires executed or failed, got %q", o.Status)
	}
	if err := requireIDs(o.AttemptID, o.ActionID); err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	existing, found, err := lookupTerminal(tx, o.AttemptID, o.ActionID)
	if err != nil {
		return err
	}
	if found {
		return sameTerminal(existing.Status, o.Status)
	}
	ev := observationFromOutcome(o)
	ev.AuditID = newAuditID("AUDIT")
	if err := insertEvent(tx, ev); err != nil {
		if isUniqueViolation(err) {
			existing, found, lerr := lookupTerminal(tx, o.AttemptID, o.ActionID)
			if lerr != nil {
				return lerr
			}
			if found {
				return sameTerminal(existing.Status, o.Status)
			}
		}
		return err
	}
	return tx.Commit()
}

func (s *SQLiteStore) AppendPolicy(o Observation) error {
	if err := requireIDs(o.AttemptID, o.ActionID); err != nil {
		return err
	}
	o.Event = EventPolicyDecided
	o.Value = nil
	if o.AuditID == "" {
		o.AuditID = newAuditID("AUDIT")
	}
	if o.CreatedAt.IsZero() {
		o.CreatedAt = time.Now().UTC()
	}
	err := insertEvent(s.db, o)
	if isUniqueViolation(err) {
		// Same audit_id redelivery. A policy_decided row is not in the
		// execution unique index, so this is the primary key.
		return nil
	}
	return err
}

func (s *SQLiteStore) Observations(attemptID, actionID string) ([]Observation, error) {
	rows, err := s.db.Query(`
SELECT `+observationColumns+`
FROM tool_outcome_events
WHERE attempt_id = ? AND action_id = ?
ORDER BY created_at ASC, rowid ASC`, attemptID, actionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Observation
	for rows.Next() {
		o, err := scanObservation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

func (s *SQLiteStore) Close() error {
	if s.db == nil {
		return nil
	}
	err := s.db.Close()
	s.db = nil
	return err
}

const observationColumns = `event_id, attempt_id, action_id, event, actor, principal, tool, action, resource, decision, reason, policy, value_json, error, created_at`

const insertEventSQL = `
INSERT INTO tool_outcome_events (
  event_id, attempt_id, action_id, event, actor, principal, tool, action, resource,
  decision, reason, policy, value_json, error, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

type queryRower interface {
	QueryRow(query string, args ...any) *sql.Row
}

type execer interface {
	Exec(query string, args ...any) (sql.Result, error)
}

func lookupTerminal(q queryRower, attemptID, actionID string) (StoredOutcome, bool, error) {
	row := q.QueryRow(`
SELECT `+observationColumns+`
FROM tool_outcome_events
WHERE attempt_id = ? AND action_id = ? AND event IN (?, ?)`,
		attemptID, actionID, EventActionExecuted, EventActionFailed)
	o, err := scanObservation(row)
	if errors.Is(err, sql.ErrNoRows) {
		return StoredOutcome{}, false, nil
	}
	if err != nil {
		return StoredOutcome{}, false, err
	}
	return outcomeFromObservation(o), true, nil
}

func insertEvent(ex execer, o Observation) error {
	var value any
	if o.Value != nil && (o.Event == EventActionExecuted || o.Event == EventActionFailed) {
		b, err := json.Marshal(o.Value)
		if err != nil {
			return err
		}
		value = string(b)
	}
	_, err := ex.Exec(insertEventSQL,
		o.AuditID, o.AttemptID, o.ActionID, o.Event, o.Actor, o.Principal, o.Tool, o.Action, o.Resource,
		o.Decision, o.Reason, o.Policy, value, o.Error, formatTime(o.CreatedAt))
	return err
}

type scanner interface {
	Scan(dest ...any) error
}

func scanObservation(sc scanner) (Observation, error) {
	var o Observation
	var valueJSON sql.NullString
	var created string
	err := sc.Scan(
		&o.AuditID, &o.AttemptID, &o.ActionID, &o.Event, &o.Actor, &o.Principal, &o.Tool, &o.Action, &o.Resource,
		&o.Decision, &o.Reason, &o.Policy, &valueJSON, &o.Error, &created,
	)
	if err != nil {
		return Observation{}, err
	}
	if valueJSON.Valid && valueJSON.String != "" {
		_ = json.Unmarshal([]byte(valueJSON.String), &o.Value)
	}
	o.CreatedAt = parseTime(created)
	return o, nil
}

func outcomeFromObservation(o Observation) StoredOutcome {
	status := StatusFailed
	if o.Event == EventActionExecuted {
		status = StatusExecuted
	}
	return StoredOutcome{
		AttemptID: o.AttemptID,
		ActionID:  o.ActionID,
		Status:    status,
		Actor:     o.Actor,
		Principal: o.Principal,
		Tool:      o.Tool,
		Action:    o.Action,
		Resource:  o.Resource,
		Reason:    o.Reason,
		Policy:    o.Policy,
		Value:     o.Value,
		Error:     o.Error,
		CreatedAt: o.CreatedAt,
	}
}

func observationFromOutcome(o StoredOutcome) Observation {
	if o.CreatedAt.IsZero() {
		o.CreatedAt = time.Now().UTC()
	}
	return Observation{
		AttemptID: o.AttemptID,
		ActionID:  o.ActionID,
		Event:     eventForStatus(o.Status),
		Actor:     o.Actor,
		Principal: o.Principal,
		Tool:      o.Tool,
		Action:    o.Action,
		Resource:  o.Resource,
		Decision:  "allow",
		Reason:    o.Reason,
		Policy:    o.Policy,
		Value:     o.Value,
		Error:     o.Error,
		CreatedAt: o.CreatedAt.UTC(),
	}
}

func eventForStatus(status string) string {
	if status == StatusExecuted {
		return EventActionExecuted
	}
	return EventActionFailed
}

func terminalFrom(events []Observation) (StoredOutcome, bool, error) {
	var found *Observation
	for i := range events {
		ev := &events[i]
		if ev.Event != EventActionExecuted && ev.Event != EventActionFailed {
			continue
		}
		if found != nil {
			return StoredOutcome{}, false, fmt.Errorf("toolproxy: multiple terminal outcomes for action %s", ev.ActionID)
		}
		found = ev
	}
	if found == nil {
		return StoredOutcome{}, false, nil
	}
	return outcomeFromObservation(*found), true, nil
}

func sameTerminal(existing, next string) error {
	if existing == next {
		return nil
	}
	return fmt.Errorf("toolproxy: terminal outcome is %s; refusing %s", existing, next)
}

func requireIDs(attemptID, actionID string) error {
	if strings.TrimSpace(attemptID) == "" || strings.TrimSpace(actionID) == "" {
		return fmt.Errorf("toolproxy: attempt_id and action_id are required")
	}
	return nil
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		t = time.Now().UTC()
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func parseTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

func newAuditID(prefix string) string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
	}
	return prefix + "-" + hex.EncodeToString(b[:])
}

func legacyAuditID(kind, attemptID, actionID string) string {
	sum := sha256.Sum256([]byte(kind + "\x00" + attemptID + "\x00" + actionID))
	return "legacy-" + kind + "-" + hex.EncodeToString(sum[:8])
}

func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}

var (
	_ OutcomeStore = (*MemoryStore)(nil)
	_ OutcomeStore = (*SQLiteStore)(nil)
)
