package store

import (
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"

	"github.com/paulpham157/sdf/internal/record"
)

// ErrNotSDF is returned by OpenForScrub for a file with no records table.
var ErrNotSDF = errors.New("not an sdf database (no records table)")

// OpenForScrub opens an existing sdf database for Scrub. The file must
// exist and have a records table; it is never created. With readOnly (sdf
// redact --dry-run) it is opened read-only, so nothing is written: no
// schema, column migration, or stamp. Otherwise the schema and column
// migration are applied as Open does.
func OpenForScrub(path string, readOnly bool) (*Store, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	mode := "rw"
	if readOnly {
		mode = "ro"
	}
	// mode=rw and mode=ro never create the file.
	dsn := (&url.URL{Scheme: "file", Path: filepath.ToSlash(abs)}).String() + "?mode=" + mode
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &Store{path: path, db: db}
	fail := func(err error) (*Store, error) {
		_ = db.Close()
		return nil, err
	}
	if _, err := db.Exec(`PRAGMA busy_timeout = 5000`); err != nil {
		return fail(err)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'records'`).Scan(&n); err != nil {
		return fail(err)
	}
	if n == 0 {
		return fail(ErrNotSDF)
	}
	if !readOnly {
		if _, err := db.Exec(schema); err != nil {
			return fail(err)
		}
		if err := s.migrate(); err != nil {
			return fail(err)
		}
	}
	return s, nil
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

// scrubBatch is how many rows Scrub holds in memory at once.
const scrubBatch = 500

// columns lists the record columns Load reads, with NULL for the ones an
// older, not yet migrated file lacks (only a read-only dry run sees one).
func (s *Store) columns() (string, error) {
	rows, err := s.db.Query(`PRAGMA table_info(records)`)
	if err != nil {
		return "", err
	}
	present := map[string]bool{}
	for rows.Next() {
		var cid, notNull, pk int
		var name, colType string
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &colType, &notNull, &dflt, &pk); err != nil {
			_ = rows.Close()
			return "", err
		}
		present[name] = true
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return "", err
	}
	cols := []string{"id", "signal", "service_name", "session_id", "name",
		"start_unix_nano", "end_unix_nano", "time_unix_nano", "value",
		"metric_unit", "metric_temporality", "metric_kind",
		"attributes_json", "resource_json", "body",
		"trace_id", "span_id", "parent_span_id", "status_code", "status_message"}
	for i, c := range cols {
		if !present[c] {
			cols[i] = "NULL AS " + c
		}
	}
	return strings.Join(cols, ", "), nil
}

// Scrub applies the default content drop (record.Redact) to every stored
// row, including rows kept under --store-content (their sdf.content mark is
// removed), and rewrites the rows that changed (name, attributes, resource,
// body, status message). Rows are read scrubBatch at a time inside one
// transaction, so a crash leaves the file as it was. Unless dryRun, it then
// VACUUMs, with secure_delete on, so the old text does not stay in free
// pages, and stamps the database as scrubbed. The rollback journal and
// VACUUM's copy need free disk of about twice the file's size. A file in
// WAL mode is checkpointed and its WAL truncated too.
func (s *Store) Scrub(dryRun bool) (ScrubResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var res ScrubResult
	cols, err := s.columns()
	if err != nil {
		return res, err
	}
	if !dryRun {
		// secure_delete overwrites the old cells as they are freed.
		if _, err := s.db.Exec(`PRAGMA secure_delete = ON`); err != nil {
			return res, err
		}
	}
	tx, err := s.db.Begin()
	if err != nil {
		return res, err
	}
	defer func() { _ = tx.Rollback() }()
	var stmt *sql.Stmt
	if !dryRun {
		stmt, err = tx.Prepare(`UPDATE records SET name = ?, attributes_json = ?, resource_json = ?, body = ?, status_message = ? WHERE id = ?`)
		if err != nil {
			return res, err
		}
		defer stmt.Close()
	}
	query := `SELECT ` + cols + ` FROM records WHERE id > ? ORDER BY id LIMIT ?`
	last := int64(0)
	for {
		rows, err := tx.Query(query, last, scrubBatch)
		if err != nil {
			return res, err
		}
		batch, err := scanRecords(rows)
		_ = rows.Close()
		if err != nil {
			return res, err
		}
		if len(batch) == 0 {
			break
		}
		last = batch[len(batch)-1].ID
		res.Rows += len(batch)
		for _, rec := range batch {
			out, changed := scrubOne(rec)
			if !changed {
				continue
			}
			res.Changed++
			if dryRun {
				continue
			}
			attrs, err := marshalSorted(out.Attributes)
			if err != nil {
				return res, err
			}
			resource, err := marshalSorted(out.Resource)
			if err != nil {
				return res, err
			}
			if _, err := stmt.Exec(nullString(out.Name), string(attrs), string(resource), nullString(out.Body), statusMessageValue(out), out.ID); err != nil {
				return res, err
			}
		}
	}
	if dryRun {
		return res, nil
	}
	if err := tx.Commit(); err != nil {
		return res, err
	}
	// The checkpoints do nothing in sdf's default rollback-journal mode; in
	// WAL mode VACUUM writes the new pages to the WAL, so the second one
	// copies them over the old file and truncates the WAL.
	for _, q := range []string{`PRAGMA wal_checkpoint(TRUNCATE)`, `VACUUM`, `PRAGMA wal_checkpoint(TRUNCATE)`, fmt.Sprintf(`PRAGMA user_version = %d`, contentPolicyVersion), `PRAGMA wal_checkpoint(TRUNCATE)`} {
		if _, err := s.db.Exec(q); err != nil {
			return res, fmt.Errorf("%s: %w", q, err)
		}
	}
	return res, nil
}

// scrubOne is one row under the default drop, and whether it changed.
func scrubOne(rec record.Record) (record.Record, bool) {
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
	return out[0], kept || &out[0] != &batch[0]
}
