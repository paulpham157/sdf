// Package ledgertest gives tests a real ledger.Store in a temporary file.
package ledgertest

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/paulpham157/sdf/internal/ledger"
)

// New opens a ledger in t's temporary directory and closes it when the test
// ends. The file is created by the first Append.
func New(t testing.TB) *ledger.Store {
	t.Helper()
	s, err := ledger.Open(filepath.Join(t.TempDir(), "ledger.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// Entries verifies the whole chain and returns how many entries it holds,
// genesis included. A broken chain fails the test.
func Entries(t testing.TB, s *ledger.Store) int {
	t.Helper()
	rep, err := s.Verify(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !rep.OK {
		t.Fatalf("ledger %s does not verify: %+v", rep.Path, rep.Failure)
	}
	return int(rep.Entries)
}
