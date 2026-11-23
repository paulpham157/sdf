package store

import "fmt"

// contentPolicyVersion is PRAGMA user_version for a database whose rows
// were all written, or scrubbed, under the default content drop
// (record.Redact). A database an older sdf wrote is 0.
const contentPolicyVersion = 1

func (s *Store) userVersion() (int, error) {
	var v int
	err := s.db.QueryRow(`PRAGMA user_version`).Scan(&v)
	return v, err
}

// markNewDatabase stamps a database with no telemetry rows as written under
// the content policy, so a later sdf can tell it from one an older sdf
// filled. A database that already has rows is left alone: Open never
// changes stored rows.
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
