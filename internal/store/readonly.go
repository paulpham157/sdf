package store

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// SQLite extended result codes OpenReadOnly handles.
const (
	// sqliteReadonlyRollback is SQLITE_READONLY_ROLLBACK: the file has a hot
	// journal from an interrupted write, and reading would roll it back.
	sqliteReadonlyRollback = 776
	// sqliteReadonlyCantInit is SQLITE_READONLY_CANTINIT: a WAL file's -shm
	// cannot be initialized.
	sqliteReadonlyCantInit = 1288
	// sqliteReadonlyDirectory is SQLITE_READONLY_DIRECTORY: a WAL file's
	// -shm (and -wal) cannot be created because the directory is read-only.
	sqliteReadonlyDirectory = 1544
	// sqliteCantOpen is SQLITE_CANTOPEN, the primary code SQLite returns when
	// a -wal file exists but the -shm beside it cannot be created.
	sqliteCantOpen = 14
)

// OpenReadOnly opens path for commands that only read, such as sdf summary.
// It never writes: it does not create the file, apply the schema, add
// missing columns, or change the journal mode, and every connection is
// mode=ro with PRAGMA query_only, so Insert and other writes fail.
//
// A missing file in an existing directory (or ":memory:" or "") opens as an
// empty in-memory store, so a read of a store nothing has written to yet
// reports no records, as it did when summary used Open, without leaving a
// file behind. A missing directory is an error, as it was with Open.
//
// The file is opened with mode=ro and the same busy_timeout as Open, so a
// read waits for a writer's lock instead of failing with SQLITE_BUSY; in WAL
// mode a reader does not wait at all. Reading a WAL file normally needs its
// -shm file, which SQLite creates (with -wal) beside the database when the
// directory is writable. When it cannot (a read-only directory with no -shm,
// SQLITE_READONLY_DIRECTORY or SQLITE_READONLY_CANTINIT) and there is no
// -wal file, every committed row is in the main file, and it is read with
// immutable=1 instead, as the ledger does. immutable=1 tells SQLite the file
// cannot change, so it takes no locks and ignores any concurrent writer: it
// is only a fallback after a normal mode=ro open has failed, and only when
// no -wal file exists. A writer that can write the directory would have
// created -wal and -shm, and then the normal open would have worked.
//
// A file older than the current schema (a Python-receiver file without the
// status columns, or one with no decision tables) is read through temporary
// views that supply the missing columns and tables as NULL or empty, which is
// what Open's migration would give them, without writing the file.
func OpenReadOnly(path string) (*Store, error) {
	if private(path) {
		return openEmptyReadOnly(path)
	}
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		// As with Open, a missing file in an existing directory is a store
		// nothing has written yet; a missing directory is an error (Open
		// failed there with SQLITE_CANTOPEN), so a mistyped path is caught.
		dir := filepath.Dir(path)
		if dirInfo, derr := os.Stat(dir); derr != nil || !dirInfo.IsDir() {
			return nil, fmt.Errorf("store: cannot open %q: directory %q does not exist", path, dir)
		}
		return openEmptyReadOnly(path)
	}
	if err != nil {
		return nil, err
	}
	if info.IsDir() {
		return nil, fmt.Errorf("store: %q is a directory, not a database file", path)
	}
	db, err := openReadOnlyProbe(path, fmt.Sprintf("mode=ro&_pragma=busy_timeout(%d)", busyTimeoutMS))
	if err != nil {
		code := sqliteCode(err)
		walExists := exists(path + "-wal")
		switch {
		case readOnlyFallback(code, walExists):
			db, err = openReadOnlyProbe(path, "mode=ro&immutable=1")
		case walExists && !exists(path+"-shm") &&
			(code == sqliteReadonlyDirectory || code == sqliteReadonlyCantInit || code&0xff == sqliteCantOpen):
			return nil, fmt.Errorf("store: %q is a WAL database with writes still in %q, and SQLite cannot create the "+
				"-shm file it needs to read them (the directory is read-only); make the directory writable, or open "+
				"the file once with a writer (sdf serve) so the writes are checkpointed into the main file: %w",
				path, path+"-wal", err)
		case code == sqliteReadonlyRollback:
			return nil, fmt.Errorf("store: %q has a hot journal from an interrupted write; reading it would mean "+
				"rolling that write back, and read-only commands do not write. Opening it with a writer (sdf serve) "+
				"rolls it back: %w", path, err)
		}
	}
	if err != nil {
		return nil, err
	}
	if err := addCompatViews(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	if _, err := db.Exec(`PRAGMA query_only = 1`); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &Store{path: path, db: db}, nil
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return !errors.Is(err, os.ErrNotExist)
}

// readOnlyFallback reports whether a mode=ro open that failed with code
// should be retried with immutable=1: only when SQLite could not set up the
// WAL shared memory and no -wal file holds writes the main file lacks.
func readOnlyFallback(code int, walExists bool) bool {
	return (code == sqliteReadonlyDirectory || code == sqliteReadonlyCantInit) && !walExists
}

// openReadOnlyProbe opens path with one connection and reads the schema, so
// an unreadable file fails here and not halfway through a query. The pool
// keeps that one connection (database/sql replaces it only after a driver
// reports it broken), so the temporary views and query_only set on it
// apply to every query.
func openReadOnlyProbe(path, query string) (*sql.DB, error) {
	name, err := dsn(path, query)
	if err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", name)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master`).Scan(&n); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

// openEmptyReadOnly is a store with the current schema and no rows, held in
// memory and closed to writes.
func openEmptyReadOnly(path string) (*Store, error) {
	db, err := sql.Open("sqlite", "file::memory:")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if _, err := db.Exec(schema); err != nil {
		_ = db.Close()
		return nil, err
	}
	if _, err := db.Exec(`PRAGMA query_only = 1`); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &Store{path: path, db: db}, nil
}

type column struct{ name, typ string }

// addCompatViews gives db, opened read-only, every table and column of the
// current schema. A table the file lacks, or one missing columns that Open's
// migration would add, is shadowed by a TEMP view of the same name (TEMP
// objects live in the connection's temporary database, so creating them does
// not write the file, and an unqualified name resolves to temp first).
func addCompatViews(db *sql.DB) error {
	want, err := schemaColumns()
	if err != nil {
		return err
	}
	for _, table := range want.order {
		cols := want.cols[table]
		have, err := tableColumnsOf(db, "main", table)
		if err != nil {
			return err
		}
		if len(have) > 0 && hasAll(have, cols) {
			continue
		}
		exprs := make([]string, len(cols))
		for i, c := range cols {
			if have[c.name] {
				exprs[i] = quoteIdent(c.name)
			} else {
				exprs[i] = fmt.Sprintf("CAST(NULL AS %s) AS %s", c.typ, quoteIdent(c.name))
			}
		}
		from := "FROM main." + quoteIdent(table)
		if len(have) == 0 {
			from = "WHERE 0"
		}
		view := fmt.Sprintf("CREATE TEMP VIEW %s AS SELECT %s %s", quoteIdent(table), strings.Join(exprs, ", "), from)
		if _, err := db.Exec(view); err != nil {
			return err
		}
	}
	return nil
}

type tableSet struct {
	order []string
	cols  map[string][]column
}

// schemaColumns lists the tables and columns of the current schema, read
// from a scratch in-memory database, so new tables and columns are covered
// without a second list to keep in step.
func schemaColumns() (tableSet, error) {
	db, err := sql.Open("sqlite", "file::memory:")
	if err != nil {
		return tableSet{}, err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		return tableSet{}, err
	}
	rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' ORDER BY rowid`)
	if err != nil {
		return tableSet{}, err
	}
	var order []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			_ = rows.Close()
			return tableSet{}, err
		}
		order = append(order, name)
	}
	if err := rows.Close(); err != nil {
		return tableSet{}, err
	}
	set := tableSet{order: order, cols: map[string][]column{}}
	for _, table := range order {
		r, err := db.Query(`SELECT name, type FROM pragma_table_info(?)`, table)
		if err != nil {
			return tableSet{}, err
		}
		for r.Next() {
			var c column
			if err := r.Scan(&c.name, &c.typ); err != nil {
				_ = r.Close()
				return tableSet{}, err
			}
			set.cols[table] = append(set.cols[table], c)
		}
		if err := r.Close(); err != nil {
			return tableSet{}, err
		}
	}
	return set, nil
}

// tableColumnsOf is the set of column names of schema.table in db, empty when
// the table does not exist.
func tableColumnsOf(db *sql.DB, schemaName, table string) (map[string]bool, error) {
	rows, err := db.Query(`SELECT name FROM pragma_table_info(?, ?)`, table, schemaName)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	have := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		have[name] = true
	}
	return have, rows.Err()
}

func hasAll(have map[string]bool, cols []column) bool {
	for _, c := range cols {
		if !have[c.name] {
			return false
		}
	}
	return true
}

func quoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}
