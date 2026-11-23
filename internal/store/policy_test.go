package store

import (
	"path/filepath"
	"testing"

	"github.com/paulpham157/sdf/internal/record"
)

// A database this sdf creates is stamped, so a later sdf (sdf redact, the
// serve warning) can tell it from one an older sdf filled with content; a
// database that already has rows is not.
func TestOpenStampsOnlyANewDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.sqlite")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if v, err := s.userVersion(); err != nil || v != contentPolicyVersion {
		t.Fatalf("new database: %d %v", v, err)
	}
	if _, err := s.db.Exec(`PRAGMA user_version = 0`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Insert([]record.Record{{Signal: "logs", Name: record.Str("x"), Attributes: map[string]any{}, Resource: map[string]any{}}}); err != nil {
		t.Fatal(err)
	}
	_ = s.Close()
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if v, err := s.userVersion(); err != nil || v != 0 {
		t.Fatalf("a database with rows was stamped: %d %v", v, err)
	}
}
