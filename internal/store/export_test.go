package store

// Pragmas reports journal_mode and busy_timeout as seen by s's connection.
func Pragmas(s *Store) (mode string, busyTimeout int, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err = s.db.QueryRow(`PRAGMA journal_mode`).Scan(&mode); err != nil {
		return "", 0, err
	}
	err = s.db.QueryRow(`PRAGMA busy_timeout`).Scan(&busyTimeout)
	return mode, busyTimeout, err
}
