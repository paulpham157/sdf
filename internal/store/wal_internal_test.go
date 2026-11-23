package store

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

// TestSetWALKeepsModeOnReadOnlyConnection runs the read-only path of setWAL
// through SQLite's own mode=ro, which holds even as root (where file
// permissions do not stop writes).
func TestSetWALKeepsModeOnReadOnlyConnection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.db")
	rw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rw.Exec(schema); err != nil {
		t.Fatal(err)
	}
	_ = rw.Close()

	name, err := dsn(path, "mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	ro, err := sql.Open("sqlite", name)
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	ro.SetMaxOpenConns(1)
	if err := setWAL(ro); err != nil {
		t.Fatalf("setWAL on a read-only connection: %v", err)
	}
	var mode string
	if err := ro.QueryRow(`PRAGMA journal_mode`).Scan(&mode); err != nil || mode != "delete" {
		t.Fatalf("journal_mode %q %v, want delete", mode, err)
	}
}

func TestDSNSpecialNames(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{":memory:", "file::memory:?q"},
		{"", "file:?q"},
	} {
		got, err := dsn(tc.in, "q")
		if err != nil || got != tc.want {
			t.Errorf("dsn(%q) = %q, %v; want %q", tc.in, got, err, tc.want)
		}
	}
	got, err := dsn("rel.db", "q")
	if err != nil || !strings.HasPrefix(got, "file:///") || !strings.HasSuffix(got, "/rel.db?q") {
		t.Errorf("dsn(rel.db) = %q, %v", got, err)
	}
}

func TestOpenPrivateDatabases(t *testing.T) {
	for _, path := range []string{":memory:", ""} {
		s, err := Open(path)
		if err != nil {
			t.Fatalf("Open(%q): %v", path, err)
		}
		var timeout int
		if err := s.db.QueryRow(`PRAGMA busy_timeout`).Scan(&timeout); err != nil || timeout != busyTimeoutMS {
			t.Errorf("Open(%q) busy_timeout %d %v", path, timeout, err)
		}
		_ = s.Close()
	}
}
