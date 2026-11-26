package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// Schema is the store schema, for tests that build a database by hand.
const Schema = schema

// Pragmas reports journal_mode and busy_timeout as seen by s's connection.
// Reading one connection is enough: Open caps the pool at one connection
// (SetMaxOpenConns(1)), so this is the connection every query uses, and
// busy_timeout is in the DSN, so any replacement connection gets it too.
func Pragmas(s *Store) (mode string, busyTimeout int, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err = s.db.QueryRow(`PRAGMA journal_mode`).Scan(&mode); err != nil {
		return "", 0, err
	}
	err = s.db.QueryRow(`PRAGMA busy_timeout`).Scan(&busyTimeout)
	return mode, busyTimeout, err
}

// SynchronousNormal is the integer PRAGMA synchronous returns for NORMAL.
// SynchronousFull is SQLite's default, FULL.
const (
	SynchronousNormal = synchronousNormal
	SynchronousFull   = 2
)

// ExpireWriterConn makes the next use of s discard the pooled writer
// connection and open another. The replacement runs the writer's connection
// hook.
func ExpireWriterConn(s *Store) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.db.SetConnMaxLifetime(time.Nanosecond)
}

// KeepWriterConn stops ExpireWriterConn from discarding further connections,
// so the connection just opened stays in the pool.
func KeepWriterConn(s *Store) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.db.SetConnMaxLifetime(0)
}

// WriterConnID is the pooled connection's pointer, for tests that check a
// replacement really happened.
func WriterConnID(s *Store) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	conn, err := s.db.Conn(context.Background())
	if err != nil {
		return "", err
	}
	defer conn.Close()
	var id string
	err = conn.Raw(func(driverConn any) error {
		id = fmt.Sprintf("%p", driverConn)
		return nil
	})
	return id, err
}

// Synchronous reports PRAGMA synchronous on s's connection. The value is
// per connection: the writer pool is one connection, and a reader opened
// on the same file has its own.
func Synchronous(s *Store) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var level int
	err := s.db.QueryRow(`PRAGMA synchronous`).Scan(&level)
	return level, err
}

// DropRetractionsForTest removes decision_retractions from a file, to stand
// in for a database the migration could not write.
func DropRetractionsForTest(path string) error {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return err
	}
	defer db.Close()
	_, err = db.Exec(`DROP TABLE IF EXISTS decision_retractions`)
	return err
}

// LoadDecisionRetractionsAt reads retractions from path without running the
// migration, as a read-only open would.
func LoadDecisionRetractionsAt(path string) ([]DecisionRetraction, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	return loadDecisionRetractions(context.Background(), db)
}
