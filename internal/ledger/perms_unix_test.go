//go:build unix

package ledger_test

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// noUmask clears the process umask for the test. It is process-wide, so
// these tests must not run in parallel.
func noUmask(t *testing.T) {
	t.Helper()
	old := syscall.Umask(0)
	t.Cleanup(func() { syscall.Umask(old) })
}

func wantMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Errorf("%s: mode %#o, want %#o", filepath.Base(path), got, want)
	}
}

// TestLedgerFilesAreOwnerOnly: under umask 0 the ledger, its -wal and -shm
// are 0600 and the directory the ledger creates is 0700.
func TestLedgerFilesAreOwnerOnly(t *testing.T) {
	noUmask(t)
	dir := filepath.Join(t.TempDir(), "sdf")
	path := filepath.Join(dir, "ledger.db")
	s := open(t, path)
	appendOK(t, s, taskKind, "task-1", `{"objective":"a"}`)
	for _, p := range []string{path, path + "-wal", path + "-shm"} {
		wantMode(t, p, 0o600)
	}
	wantMode(t, dir, 0o700)
}

// TestLedgerTightensLooseFile: an existing 0644 ledger is made 0600 on
// the next write, and on a read.
func TestLedgerTightensLooseFile(t *testing.T) {
	noUmask(t)
	path := filepath.Join(t.TempDir(), "ledger.db")
	s := open(t, path)
	appendOK(t, s, taskKind, "task-1", `{"objective":"a"}`)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	r := open(t, path)
	if _, err := r.Head(context.Background()); err != nil {
		t.Fatal(err)
	}
	wantMode(t, path, 0o600)
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}

	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	w := open(t, path)
	appendOK(t, w, taskKind, "task-2", `{"objective":"b"}`)
	for _, p := range []string{path, path + "-wal", path + "-shm"} {
		wantMode(t, p, 0o600)
	}
}
