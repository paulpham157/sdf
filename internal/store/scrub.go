package store

import (
	"fmt"

	"github.com/paulpham157/sdf/internal/record"
)

// contentPolicyVersion is PRAGMA user_version for a database whose rows
// were all written, or scrubbed, under the default content drop
// (record.Redact). An older database is 0.
const contentPolicyVersion = 1

func (s *Store) userVersion() (int, error) {
	var v int
	err := s.db.QueryRow(`PRAGMA user_version`).Scan(&v)
	return v, err
}

// markNewDatabase stamps a database with no telemetry rows as written under
// the content policy. A database that already has rows is left alone: Open
// never changes stored rows; only Scrub (sdf redact) does.
func (s *Store) markNewDatabase() error {
	v, err := s.userVersion()
	if err != nil || v >= contentPolicyVersion {
		return err
	}
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM records`).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	_, err = s.db.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, contentPolicyVersion))
	return err
}

// NeedsScrub reports whether the database holds rows written by an sdf that
// stored content (before content was dropped by default) and has not been
// scrubbed since.
func (s *Store) NeedsScrub() (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, err := s.userVersion()
	return v < contentPolicyVersion, err
}

// ScrubResult counts what Scrub looked at and rewrote.
type ScrubResult struct {
	Rows    int
	Changed int
}

// Scrub applies the default content drop (record.Redact) to every stored
// row, including rows kept under --store-content (their sdf.content mark is
// removed), and rewrites the rows that changed. Unless dryRun, it then
// checkpoints and truncates the WAL, VACUUMs so the old text does not stay
// in free pages, and stamps the database as scrubbed.
func (s *Store) Scrub(dryRun bool) (ScrubResult, error) {
	rows, err := s.Load()
	if err != nil {
		return ScrubResult{}, err
	}
	var res ScrubResult
	res.Rows = len(rows)
	var changed []record.Record
	for _, rec := range rows {
		in := rec
		kept := false
		if _, ok := rec.Attributes[record.ContentKept]; ok {
			attrs := make(map[string]any, len(rec.Attributes))
			for k, v := range rec.Attributes {
				if k != record.ContentKept {
					attrs[k] = v
				}
			}
			in.Attributes = attrs
			kept = true
		}
		batch := []record.Record{in}
		out := record.Redact(batch)
		// Redact returns the same slice when nothing in it changed.
		if kept || &out[0] != &batch[0] {
			changed = append(changed, out[0])
		}
	}
	res.Changed = len(changed)
	if dryRun {
		return res, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// secure_delete overwrites the old cells as they are freed.
	if _, err := s.db.Exec(`PRAGMA secure_delete = ON`); err != nil {
		return res, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return res, err
	}
	stmt, err := tx.Prepare(`UPDATE records SET attributes_json = ?, resource_json = ?, body = ?, status_message = ? WHERE id = ?`)
	if err != nil {
		_ = tx.Rollback()
		return res, err
	}
	defer stmt.Close()
	for _, rec := range changed {
		attrs, err := marshalSorted(rec.Attributes)
		if err != nil {
			_ = tx.Rollback()
			return res, err
		}
		resource, err := marshalSorted(rec.Resource)
		if err != nil {
			_ = tx.Rollback()
			return res, err
		}
		if _, err := stmt.Exec(string(attrs), string(resource), nullString(rec.Body), statusMessageValue(rec), rec.ID); err != nil {
			_ = tx.Rollback()
			return res, err
		}
	}
	if err := tx.Commit(); err != nil {
		return res, err
	}
	// In WAL mode VACUUM writes the new pages to the WAL, so checkpoint
	// again after it: that copies them over the old file and truncates it
	// and the WAL.
	for _, q := range []string{`PRAGMA wal_checkpoint(TRUNCATE)`, `VACUUM`, `PRAGMA wal_checkpoint(TRUNCATE)`, fmt.Sprintf(`PRAGMA user_version = %d`, contentPolicyVersion), `PRAGMA wal_checkpoint(TRUNCATE)`} {
		if _, err := s.db.Exec(q); err != nil {
			return res, fmt.Errorf("%s: %w", q, err)
		}
	}
	return res, nil
}
