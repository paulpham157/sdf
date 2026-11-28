package store

import (
	"context"
	"errors"
)

// pathsSchema is the one table decision path globs add. It is created by
// migratePaths, not by the base schema, so the change is additive: no
// existing table or column changes, and a binary that predates it reads
// the file exactly as before and never sees the table.
//
// A row is never updated or deleted. A decision's scope changes by
// recording a new decision, not by editing these rows. decision_id is not
// unique: one decision has many globs, in insertion order.
const pathsSchema = `
CREATE TABLE IF NOT EXISTS decision_paths (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    decision_id TEXT NOT NULL,
    glob TEXT NOT NULL,
    recorded_at TEXT NOT NULL
);
`

// DecisionPath is one append-only path glob recorded for a decision.
// Glob is the caller's pattern, stored as given. Nothing here interprets it.
type DecisionPath struct {
	DecisionID string
	Glob       string
	RecordedAt string
}

// ErrNoPathsTable is returned by AppendDecisionPaths when the file has no
// decision_paths table because the migration could not write it (a
// read-only file).
var ErrNoPathsTable = errors.New("store: this database has no decision_paths table (it could not be migrated)")

// migratePaths creates decision_paths if it is missing. It is idempotent.
// On a file this process cannot write, the table cannot be created; that
// is not an error, since reads treat a missing table as no paths, and the
// store stays readable as before.
func (s *Store) migratePaths() error {
	if _, err := s.db.Exec(pathsSchema); err != nil {
		if sqliteCode(err)&0xff == sqliteReadonly {
			return nil
		}
		return err
	}
	return nil
}

// LoadDecisionPaths returns every path glob in insertion order. A file
// without the table returns an empty list.
func (s *Store) LoadDecisionPaths() ([]DecisionPath, error) {
	return s.LoadDecisionPathsContext(context.Background())
}

// LoadDecisionPathsContext is LoadDecisionPaths bound to ctx.
func (s *Store) LoadDecisionPathsContext(ctx context.Context) ([]DecisionPath, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return loadDecisionPaths(ctx, s.db)
}

// AppendDecisionPaths appends globs for decisionID only if check, given
// every path already stored, returns nil. As with AppendDecisionRetraction,
// the read, the check, and the inserts run in one BEGIN IMMEDIATE
// transaction. Nothing is updated or deleted. An empty globs list is a
// no-op and does not call check.
func (s *Store) AppendDecisionPaths(decisionID, recordedAt string, globs []string, check func(existing []DecisionPath) error) error {
	if len(globs) == 0 {
		return nil
	}
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
	ok, err := hasPathsTable(ctx, conn)
	if err != nil {
		return err
	}
	if !ok {
		return ErrNoPathsTable
	}
	existing, err := loadDecisionPaths(ctx, conn)
	if err != nil {
		return err
	}
	if err := check(existing); err != nil {
		return err
	}
	for _, g := range globs {
		if _, err := conn.ExecContext(ctx, `
			INSERT INTO decision_paths (decision_id, glob, recorded_at)
			VALUES (?, ?, ?)
		`, decisionID, g, recordedAt); err != nil {
			return err
		}
	}
	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		return err
	}
	committed = true
	return nil
}

func hasPathsTable(ctx context.Context, q rowQuerier) (bool, error) {
	var n int
	err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'decision_paths'`).Scan(&n)
	return n > 0, err
}

func loadDecisionPaths(ctx context.Context, q rowQuerier) ([]DecisionPath, error) {
	out := []DecisionPath{}
	ok, err := hasPathsTable(ctx, q)
	if err != nil || !ok {
		return out, err
	}
	rows, err := q.QueryContext(ctx, `
		SELECT decision_id, glob, recorded_at
		FROM decision_paths
		ORDER BY id
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var p DecisionPath
		if err := rows.Scan(&p.DecisionID, &p.Glob, &p.RecordedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
