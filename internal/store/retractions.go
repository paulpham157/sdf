package store

import (
	"context"
	"database/sql"
	"errors"
)

// retractionsSchema is the one table retractions add. It is created by
// migrateRetractions, not by the base schema, so the change is additive:
// no existing table or column changes, and a binary that predates it reads
// the file exactly as before and never sees the table.
//
// decision_id is UNIQUE: a decision is retracted at most once. A row is
// never updated or deleted.
const retractionsSchema = `
CREATE TABLE IF NOT EXISTS decision_retractions (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    decision_id TEXT NOT NULL UNIQUE,
    reason TEXT,
    retracted_by TEXT,
    recorded_at TEXT NOT NULL
);
`

// DecisionRetraction is one append-only retraction of a decision. Reason and
// RetractedBy are empty when the caller named none.
type DecisionRetraction struct {
	DecisionID  string
	Reason      string
	RetractedBy string
	RecordedAt  string
}

// ErrNoRetractionsTable is returned by AppendDecisionRetraction when the
// file has no decision_retractions table because the migration could not
// write it (a read-only file).
var ErrNoRetractionsTable = errors.New("store: this database has no decision_retractions table (it could not be migrated)")

// migrateRetractions creates decision_retractions if it is missing. It is
// idempotent. On a file this process cannot write, the table cannot be
// created; that is not an error, since reads treat a missing table as no
// retractions, and the store stays readable as before.
func (s *Store) migrateRetractions() error {
	if _, err := s.db.Exec(retractionsSchema); err != nil {
		if sqliteCode(err)&0xff == sqliteReadonly {
			return nil
		}
		return err
	}
	return nil
}

// LoadDecisionRetractions returns every retraction in insertion order. A
// file without the table returns an empty list.
func (s *Store) LoadDecisionRetractions() ([]DecisionRetraction, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return loadDecisionRetractions(context.Background(), s.db)
}

// AppendDecisionRetraction appends r only if check, given every retraction
// already stored, returns nil. As with AppendDecisionEdge, the read, the
// check, and the insert run in one BEGIN IMMEDIATE transaction, so a
// concurrent retraction or link waits and then sees this row. The UNIQUE
// decision_id is a second guard. Nothing is updated or deleted.
func (s *Store) AppendDecisionRetraction(r DecisionRetraction, check func(existing []DecisionRetraction) error) error {
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
	ok, err := hasRetractionsTable(ctx, conn)
	if err != nil {
		return err
	}
	if !ok {
		return ErrNoRetractionsTable
	}
	existing, err := loadDecisionRetractions(ctx, conn)
	if err != nil {
		return err
	}
	if err := check(existing); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, `
		INSERT INTO decision_retractions (decision_id, reason, retracted_by, recorded_at)
		VALUES (?, ?, ?, ?)
	`, r.DecisionID, nullText(r.Reason), nullText(r.RetractedBy), r.RecordedAt); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		return err
	}
	committed = true
	return nil
}

type rowQuerier interface {
	querier
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func hasRetractionsTable(ctx context.Context, q rowQuerier) (bool, error) {
	var n int
	err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'decision_retractions'`).Scan(&n)
	return n > 0, err
}

func loadDecisionRetractions(ctx context.Context, q rowQuerier) ([]DecisionRetraction, error) {
	out := []DecisionRetraction{}
	ok, err := hasRetractionsTable(ctx, q)
	if err != nil || !ok {
		return out, err
	}
	rows, err := q.QueryContext(ctx, `
		SELECT decision_id, reason, retracted_by, recorded_at
		FROM decision_retractions
		ORDER BY id
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var r DecisionRetraction
		var reason, by sql.NullString
		if err := rows.Scan(&r.DecisionID, &reason, &by, &r.RecordedAt); err != nil {
			return nil, err
		}
		r.Reason, r.RetractedBy = reason.String, by.String
		out = append(out, r)
	}
	return out, rows.Err()
}
