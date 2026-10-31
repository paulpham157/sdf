package toolproxy

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

// StoredOutcome is one durable Tool Proxy execution observation
// (Python tool_audits action_executed / action_failed fence).
type StoredOutcome struct {
	AttemptID string
	ActionID  string
	Status    string // executed | failed (terminal); claimed is internal-only
	Actor     string
	Reason    string
	Policy    string
	Value     any
	Error     string
	CreatedAt time.Time
}

// OutcomeStore is the durable idempotency fence keyed by attempt_id + action_id.
type OutcomeStore interface {
	// Lookup returns a terminal outcome (executed|failed) if one exists.
	Lookup(attemptID, actionID string) (StoredOutcome, bool, error)
	// Claim atomically reserves the (attempt_id, action_id) execution slot.
	// Returns false if another delivery already claimed or finished the action.
	Claim(attemptID, actionID string) (bool, error)
	// Complete writes the terminal outcome for a previously claimed action.
	Complete(o StoredOutcome) error
	Close() error
}

// MemoryStore is a process-local fence (tests / default when no path is set).
type MemoryStore struct {
	mu       sync.Mutex
	claimed  map[string]struct{}
	outcomes map[string]StoredOutcome
}

// NewMemoryStore builds an in-process OutcomeStore.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		claimed:  make(map[string]struct{}),
		outcomes: make(map[string]StoredOutcome),
	}
}

func fenceKey(attemptID, actionID string) string {
	return attemptID + "\x00" + actionID
}

func (s *MemoryStore) Lookup(attemptID, actionID string) (StoredOutcome, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	o, ok := s.outcomes[fenceKey(attemptID, actionID)]
	return o, ok, nil
}

func (s *MemoryStore) Claim(attemptID, actionID string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := fenceKey(attemptID, actionID)
	if _, ok := s.outcomes[k]; ok {
		return false, nil
	}
	if _, ok := s.claimed[k]; ok {
		return false, nil
	}
	s.claimed[k] = struct{}{}
	return true, nil
}

func (s *MemoryStore) Complete(o StoredOutcome) error {
	if o.Status != StatusExecuted && o.Status != StatusFailed {
		return fmt.Errorf("toolproxy: Complete requires executed or failed, got %q", o.Status)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	k := fenceKey(o.AttemptID, o.ActionID)
	s.outcomes[k] = o
	delete(s.claimed, k)
	return nil
}

func (s *MemoryStore) Close() error { return nil }

// SQLiteStore persists outcomes with modernc.org/sqlite (no CGO).
// Mirrors Python SqlAlchemyAuditSink claim + unique (attempt_id, action_id).
type SQLiteStore struct {
	db *sql.DB
}

// OpenSQLiteStore opens (or creates) a durable fence at path.
// Use ":memory:" for ephemeral SQLite (still SQL semantics, not restart-safe).
func OpenSQLiteStore(path string) (*SQLiteStore, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	// One writer; serialise claims under SQLite busy handling.
	db.SetMaxOpenConns(1)
	s := &SQLiteStore{db: db}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func (s *SQLiteStore) migrate() error {
	_, err := s.db.Exec(`
CREATE TABLE IF NOT EXISTS tool_outcomes (
  attempt_id TEXT NOT NULL,
  action_id  TEXT NOT NULL,
  status     TEXT NOT NULL, -- claimed | executed | failed
  actor      TEXT NOT NULL DEFAULT '',
  reason     TEXT NOT NULL DEFAULT '',
  policy     TEXT NOT NULL DEFAULT '',
  value_json TEXT,
  error      TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  PRIMARY KEY (attempt_id, action_id)
);
`)
	return err
}

func (s *SQLiteStore) Lookup(attemptID, actionID string) (StoredOutcome, bool, error) {
	row := s.db.QueryRow(`
SELECT status, actor, reason, policy, value_json, error, created_at
FROM tool_outcomes
WHERE attempt_id = ? AND action_id = ? AND status IN ('executed', 'failed')`,
		attemptID, actionID)
	var o StoredOutcome
	var valueJSON sql.NullString
	var created string
	err := row.Scan(&o.Status, &o.Actor, &o.Reason, &o.Policy, &valueJSON, &o.Error, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return StoredOutcome{}, false, nil
	}
	if err != nil {
		return StoredOutcome{}, false, err
	}
	o.AttemptID = attemptID
	o.ActionID = actionID
	if valueJSON.Valid && valueJSON.String != "" {
		_ = json.Unmarshal([]byte(valueJSON.String), &o.Value)
	}
	if t, err := time.Parse(time.RFC3339Nano, created); err == nil {
		o.CreatedAt = t
	}
	return o, true, nil
}

func (s *SQLiteStore) Claim(attemptID, actionID string) (bool, error) {
	res, err := s.db.Exec(`
INSERT OR IGNORE INTO tool_outcomes (attempt_id, action_id, status, created_at)
VALUES (?, ?, 'claimed', ?)`,
		attemptID, actionID, time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n == 1, nil
}

func (s *SQLiteStore) Complete(o StoredOutcome) error {
	if o.Status != StatusExecuted && o.Status != StatusFailed {
		return fmt.Errorf("toolproxy: Complete requires executed or failed, got %q", o.Status)
	}
	var valueJSON string
	if o.Value != nil {
		b, err := json.Marshal(o.Value)
		if err != nil {
			return err
		}
		valueJSON = string(b)
	}
	created := o.CreatedAt
	if created.IsZero() {
		created = time.Now().UTC()
	}
	res, err := s.db.Exec(`
UPDATE tool_outcomes
SET status = ?, actor = ?, reason = ?, policy = ?, value_json = ?, error = ?, created_at = ?
WHERE attempt_id = ? AND action_id = ? AND status = 'claimed'`,
		o.Status, o.Actor, o.Reason, o.Policy, valueJSON, o.Error, created.UTC().Format(time.RFC3339Nano),
		o.AttemptID, o.ActionID)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		// Allow Complete after restart if row missing: upsert terminal.
		_, err = s.db.Exec(`
INSERT INTO tool_outcomes (attempt_id, action_id, status, actor, reason, policy, value_json, error, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(attempt_id, action_id) DO UPDATE SET
  status=excluded.status, actor=excluded.actor, reason=excluded.reason,
  policy=excluded.policy, value_json=excluded.value_json, error=excluded.error,
  created_at=excluded.created_at
WHERE tool_outcomes.status = 'claimed' OR tool_outcomes.status = excluded.status`,
			o.AttemptID, o.ActionID, o.Status, o.Actor, o.Reason, o.Policy, valueJSON, o.Error,
			created.UTC().Format(time.RFC3339Nano))
		return err
	}
	return nil
}

func (s *SQLiteStore) Close() error {
	if s.db == nil {
		return nil
	}
	err := s.db.Close()
	s.db = nil
	return err
}
