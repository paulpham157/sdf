package store

import (
	"database/sql"
	"errors"
	"strings"

	"github.com/paulpham157/sdf/internal/record"
)

// recordsRevisionTable is one row, id 1, whose generation advances once per
// committed insert and once per content scrub that rewrites a row. It is
// created by migrateRecordRevision, not by the base schema, so the change
// is additive: a binary that predates it never reads the table, and a
// read-only open does not create it.
//
// The summary cache keys a report by this generation together with the
// greatest record id. Inserts advance both. A scrub advances the generation
// while the greatest id stays put, which a max-id check alone would miss.
// A delete or a prune must bump the generation too. Within one generation
// the cache treats the greater id as the newer snapshot, so a lower max
// id after a delete would look older and would not replace the cached
// report.
const recordsRevisionTable = `
CREATE TABLE IF NOT EXISTS records_revision (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    generation INTEGER NOT NULL
);
`

// RecordSnapshot names the records visible to one read. Generation is the
// records_revision counter in that snapshot. MaxID is the greatest
// records.id, or 0 when the table is empty. A database this process did
// not migrate has no counter; RecordSnapshot then reports cacheable false
// and the caller reads the rows without caching.
type RecordSnapshot struct {
	Generation int64
	MaxID      int64
}

// migrateRecordRevision creates the generation row if it is missing. It is
// idempotent and does not reset a counter that is already there. On a file
// this process cannot write, the table cannot be created; that is not an
// error, since a read then reports the snapshot as not cacheable.
func (s *Store) migrateRecordRevision() error {
	if _, err := s.db.Exec(recordsRevisionTable); err != nil {
		if sqliteCode(err)&0xff == sqliteReadonly {
			return nil
		}
		return err
	}
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM records_revision WHERE id = 1`).Scan(&n); err != nil {
		if sqliteCode(err)&0xff == sqliteReadonly {
			return nil
		}
		return err
	}
	if n > 0 {
		return nil
	}
	// Two writable opens can both see an empty table. OR IGNORE keeps the
	// loser from failing the migration, and it does not reset a row the
	// winner already inserted.
	if recordRevisionInsertHook != nil {
		recordRevisionInsertHook()
	}
	_, err := s.db.Exec(`INSERT OR IGNORE INTO records_revision (id, generation) VALUES (1, 0)`)
	if err != nil && sqliteCode(err)&0xff == sqliteReadonly {
		return nil
	}
	return err
}

// recordRevisionInsertHook, when set, runs after the migration has seen no
// generation row and before the insert. Tests use it to overlap two opens.
// Nil in production.
var recordRevisionInsertHook func()

// RecordSnapshot is the generation and greatest id visible to this call.
// cacheable is false when the file has no records_revision row.
func (s *Store) RecordSnapshot() (snap RecordSnapshot, cacheable bool, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return readRecordSnapshot(s.db)
}

// LoadSnapshot reads every record in insertion order and the RecordSnapshot
// of that same transaction. cacheable is false when the file has no
// records_revision row; the records are still returned.
func (s *Store) LoadSnapshot() (records []record.Record, snap RecordSnapshot, cacheable bool, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return nil, RecordSnapshot{}, false, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	snap, cacheable, err = readRecordSnapshot(tx)
	if err != nil {
		return nil, RecordSnapshot{}, false, err
	}
	rows, err := tx.Query(loadRecordsSQL)
	if err != nil {
		return nil, RecordSnapshot{}, false, err
	}
	records, err = scanRecords(rows)
	_ = rows.Close()
	if err != nil {
		return nil, RecordSnapshot{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return nil, RecordSnapshot{}, false, err
	}
	committed = true
	return records, snap, cacheable, nil
}

type rowQuery interface {
	QueryRow(query string, args ...any) *sql.Row
}

func readRecordSnapshot(q rowQuery) (RecordSnapshot, bool, error) {
	var snap RecordSnapshot
	err := q.QueryRow(`
		SELECT generation, (SELECT COALESCE(MAX(id), 0) FROM records)
		FROM records_revision WHERE id = 1
	`).Scan(&snap.Generation, &snap.MaxID)
	if err == nil {
		return snap, true, nil
	}
	if errors.Is(err, sql.ErrNoRows) || noSuchTable(err) {
		return RecordSnapshot{}, false, nil
	}
	return RecordSnapshot{}, false, err
}

func noSuchTable(err error) bool {
	return err != nil && strings.Contains(err.Error(), "no such table")
}

// bumpRecordGeneration advances the counter once. The caller runs it in the
// same transaction as the insert or scrub it belongs to.
func bumpRecordGeneration(tx *sql.Tx) error {
	res, err := tx.Exec(`UPDATE records_revision SET generation = generation + 1 WHERE id = 1`)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return errors.New("store: records generation was not updated")
	}
	return nil
}
