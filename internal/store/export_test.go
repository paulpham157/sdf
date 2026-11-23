package store

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
